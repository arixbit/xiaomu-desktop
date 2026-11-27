package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/source"
	"github.com/egoist/mygo/ui"
)

// browser holds the import dialog, the toolbar's search text and the one
// site opened from the sources page.
type browser struct {
	configURL, assets, query string
	importOpen               bool
	importWasOpen            bool
	importAdvanced           bool
	importURL, importAssets  string
	importErr                string
	importJobGen             uint64

	site       library.Entry
	session    source.Session
	categories []catalog.Category
	category   string
	page       int
	results    catalog.Page
	selected   int
	grid       ui.GridState
}

// jobSlot names a kind of foreground work. A new job replaces the one in its
// slot; jobs in different slots run side by side, so loading a configuration
// never blocks browsing or playback.
type jobSlot int

const (
	jobConfig   jobSlot = iota // importing or refreshing a configuration
	jobLive                    // importing a live list or reading its channels
	jobSite                    // browsing one site
	jobTitle                   // reading a title from one of its sources
	jobPlay                    // resolving an episode into media
	jobDanmaku                 // searching or loading this episode's comments
	jobSubtitle                // searching or downloading online external subtitles
	jobEPG                     // loading IPTV EPG program guides
	jobPan                     // verifying cloud-drive credentials or cleaning temp files
	jobLocalPoster                      // finding artwork for the local video just opened
	jobLocalPosterRead                  // reading visible artwork from the local cache
	jobLocalPosterSearch                // searching or saving a manual artwork match
	jobSlots
)

type job struct {
	cancel context.CancelFunc
	gen    uint64
	label  string
}

// A job returns an update to apply on the UI thread and optional cleanup for
// a result that arrives after the job was replaced, canceled or failed.
type jobResult struct {
	apply   func()
	discard func()
}

func (a *app) running(slot jobSlot) bool {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	return a.jobs[slot].cancel != nil
}

func (a *app) busy() bool {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	for i := range a.jobs {
		if jobSlot(i) >= jobLocalPoster && jobSlot(i) <= jobLocalPosterSearch {
			continue
		}
		if a.jobs[i].cancel != nil {
			return true
		}
	}
	return false
}

// cancelJob abandons the slot's job; its result is discarded when it returns.
func (a *app) cancelJob(slot jobSlot) {
	a.jobsMu.Lock()
	j := &a.jobs[slot]
	label := j.label
	if j.cancel != nil {
		j.cancel()
		j.cancel = nil
	}
	j.label = ""
	j.gen++
	a.jobsMu.Unlock()
	if label != "" && a.status == label+"…" {
		a.status = ""
	}
}

func (a *app) jobGen(slot jobSlot) uint64 {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	return a.jobs[slot].gen
}

func (a *app) cancelJobIfGen(slot jobSlot, gen uint64) {
	if gen == 0 {
		return
	}
	a.jobsMu.Lock()
	j := &a.jobs[slot]
	if j.gen != gen {
		a.jobsMu.Unlock()
		return
	}
	label := j.label
	if j.cancel != nil {
		j.cancel()
		j.cancel = nil
	}
	j.label = ""
	j.gen++
	a.jobsMu.Unlock()
	if label != "" && a.status == label+"…" {
		a.status = ""
	}
}

func (a *app) cancelJobs() {
	a.pendingLibrary = nil
	for slot := range a.jobs {
		a.cancelJob(jobSlot(slot))
	}
	a.setNotice("已取消，可以重试或选择其他站点")
}

// run starts work in slot, replacing the job already there. fail, when not
// nil, receives the error on the UI thread instead of the status line, for
// pages that show it in place.
func (a *app) run(slot jobSlot, label string, work func(context.Context) (jobResult, error), fail func(error)) {
	if a.closed {
		return
	}
	// Replacing the old job and recording the new one happen under one
	// lock, so a cancel landing at the same moment cannot leave the
	// slot with the new job's cancel and the old job's generation.
	a.jobsMu.Lock()
	j := &a.jobs[slot]
	if j.cancel != nil {
		j.cancel()
		j.cancel = nil
	}
	j.gen++
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout(slot))
	j.cancel = cancel
	j.label = label
	gen := j.gen
	a.jobsMu.Unlock()
	if label != "" && !a.hasActiveUndo() {
		a.status = label + "…"
	}
	dispatch := a.dispatch
	execute := func() {
		result, err := work(ctx)
		finish := func() {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			cancel()
			// Only the generation check needs the lock: the apply
			// and fail callbacks below can start other jobs, so
			// they must run without it.
			a.jobsMu.Lock()
			current := j.gen == gen
			if current {
				j.cancel = nil
				j.label = ""
			}
			a.jobsMu.Unlock()
			if current && !a.closed && label != "" && a.status == label+"…" {
				a.status = ""
			}
			if !current || a.closed || err != nil {
				if result.discard != nil {
					result.discard()
				}
				if current && !a.closed {
					if fail != nil {
						fail(err)
					} else {
						a.status = jobError(label, err)
					}
				}
				return
			}
			if result.apply != nil {
				result.apply()
			}
		}
		if dispatch == nil {
			finish()
		} else {
			dispatch(finish)
		}
	}
	if dispatch == nil {
		execute()
	} else {
		go execute()
	}
}

func jobTimeout(slot jobSlot) time.Duration {
	if slot == jobDanmaku {
		return 90 * time.Second
	}
	return 30 * time.Second
}

func jobError(label string, err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "已取消，可以重试或选择其他站点"
	case errors.Is(err, context.DeadlineExceeded):
		return "请求超时，请重试或换一个站点"
	}
	return label + "失败：" + err.Error()
}

// closeSession releases a site's runtime. Close waits for a call in flight,
// so it stays off the UI thread.
func (a *app) closeSession(s source.Session) {
	if s == nil {
		return
	}
	if a.dispatch == nil {
		s.Close()
	} else {
		go s.Close()
	}
}

func (a *app) close() {
	a.closed = true
	a.cancelLibraryWork()
	a.cancelSourceSpeedTest()
	a.pending, a.pendingLibrary = nil, nil
	a.cancelFollowingRefresh()
	a.stopScan(&a.library.feed)
	a.stopScan(&a.library.search)
	a.stopDiscovery()
	a.jobsMu.Lock()
	for i := range a.jobs {
		if cancel := a.jobs[i].cancel; cancel != nil {
			cancel()
		}
	}
	a.jobsMu.Unlock()
	a.flushHistory()
	if err := a.latency.flush(); err != nil {
		a.status = "测速记录保存失败：" + err.Error()
	}
	_ = a.siteHealth.flush()
	a.closeSite()
	a.clearTitle()
	a.closeCurrentPlayer()
	a.syncNowPlaying()
	if a.proxy != nil {
		_ = a.proxy.Close()
		a.proxy = nil
		a.proxySession = ""
	}
	a.closePanLoginWindow()
}

func (a *app) openImportDialog() {
	b := &a.browser
	b.importURL = b.configURL
	b.importAssets = b.assets
	b.importErr = ""
	b.importJobGen = 0
	b.importOpen = true
}

func (a *app) loadConfig(raw string) {
	a.readConfigMode(raw, false, false, false, "")
}

func (a *app) loadConfigFromDialog(raw, assets string) {
	a.readConfigMode(raw, false, false, true, assets)
}

func (a *app) refreshConfig(raw string) {
	a.readConfigMode(raw, false, true, false, "")
}

func (a *app) readConfig(raw string, restoring bool) {
	a.readConfigMode(raw, restoring, false, false, "")
}

func (a *app) readConfigMode(raw string, restoring, refreshing, fromDialog bool, draftAssets string) {
	raw = cleanUserURL(raw)
	if raw == "" {
		msg := "请先粘贴配置地址"
		if a.browser.importOpen {
			a.browser.importErr = msg
		} else {
			a.setNotice(msg)
		}
		return
	}
	a.browser.importErr = ""
	a.run(jobConfig, "加载配置", func(ctx context.Context) (jobResult, error) {
		doc, err := config.Read(ctx, raw, nil)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if fromDialog {
				a.browser.importJobGen = 0
			}
			a.applyConfigFull(raw, doc, restoring, refreshing, fromDialog, draftAssets)
			a.finishLibraryEvent()
		}}, nil
	}, func(err error) {
		a.pendingLibrary = nil
		if fromDialog {
			a.browser.importJobGen = 0
		}
		msg := jobError("加载配置", err)
		if a.browser.importOpen {
			a.browser.importErr = msg
		} else if refreshing {
			a.setBackgroundError(msg)
		} else {
			a.status = msg
		}
	})
	if fromDialog {
		a.browser.importJobGen = a.jobGen(jobConfig)
	}
}

func (a *app) applyConfig(raw string, doc *config.Config, restoring bool) {
	a.applyConfigFull(raw, doc, restoring, false, false, "")
}

func (a *app) applyConfigMode(raw string, doc *config.Config, restoring, refreshing bool) {
	a.applyConfigFull(raw, doc, restoring, refreshing, false, "")
}

func (a *app) applyConfigFull(raw string, doc *config.Config, restoring, refreshing, fromDialog bool, draftAssets string) {
	b := &a.browser
	if len(doc.Sites) == 0 && len(doc.Lines) == 0 {
		if len(doc.Lives) == 0 {
			msg := "这个地址里没有影视站点，也没有直播频道"
			if fromDialog && b.importOpen {
				b.importErr = msg
			} else if refreshing {
				a.setBackgroundError(msg)
			} else {
				b.importOpen = false
				b.importErr = ""
				a.status = msg
			}
			return
		}
		b.importOpen = false
		b.importErr = ""
		// A plain channel list is a live source; the film library and its script directory stay untouched.
		if !restoring || a.live.url == "" {
			a.applyLiveImport(raw, doc.Lives, !refreshing)
		}
		return
	}
	b.importOpen = false
	b.importErr = ""
	if fromDialog {
		b.assets = draftAssets
	}
	if !restoring {
		a.clearSiteHealth()
		a.showFailedSites = false
	}
	a.applyLibraryConfigMode(raw, doc, nil, nil, nil, refreshing)
	if !restoring {
		if refreshing {
			a.setNotice("影视配置已刷新")
		}
	}
	if a.persist {
		if err := a.saveImportedSource(raw); err != nil {
			msg := "保存片源地址失败：" + err.Error()
			if refreshing {
				a.setBackgroundError(msg)
			} else {
				a.status = msg
			}
		}
	}
}

func (a *app) applyCachedConfig(raw string, cached cachedSourceConfig) {
	root, docs, liveChannels, liveEPG := cached.restore()
	if root == nil {
		return
	}
	a.browser.importOpen = false
	a.applyLibraryConfig(raw, root, docs, liveChannels, liveEPG)
}

func (a *app) applyLibraryConfig(raw string, doc *config.Config, cached map[string]*config.Config, liveChannels map[string][]iptv.Channel, liveEPG map[string]string) {
	a.applyLibraryConfigMode(raw, doc, cached, liveChannels, liveEPG, false)
}

func (a *app) applyLibraryConfigMode(raw string, doc *config.Config, cached map[string]*config.Config, liveChannels map[string][]iptv.Channel, liveEPG map[string]string, keepRoute bool) {
	if !keepRoute {
		a.cancelJob(jobSite)
		a.closeSite()
		a.clearTitle()
		a.nav = nil
	} else {
		// Background refresh must not dismantle an active playback session,
		// open title detail, or site browsing screen that the user entered
		// while the refresh request was still in flight.
		if a.route() != routeSite {
			a.cancelJob(jobSite)
			a.closeSite()
		}
		if a.player == nil && a.route() != routeDetail && !a.running(jobPlay) && !a.running(jobTitle) {
			a.clearTitle()
		}
	}
	a.browser.configURL = raw
	if a.live.url == "" {
		a.cancelJob(jobLive)
		a.setLiveSources(doc.Lives)
	}
	a.resetLibraryWithCache(doc, cached, liveChannels, liveEPG)
}

// removeConfig forgets the imported configuration. Play history stays, shown
// as unavailable until its source is imported again.
func (a *app) removeConfig() {
	a.pendingLibrary = nil
	a.cancelJob(jobConfig)
	a.cancelJob(jobSite)
	a.closeSite()
	a.clearTitle()
	a.clearSiteHealth()
	a.showFailedSites = false
	a.resetLibrary(nil)
	a.browser.configURL = ""
	if a.live.url == "" {
		a.cancelJob(jobLive)
		a.setLiveSources(nil)
	}
	a.setNotice("已移除片源")
	if a.persist {
		if err := a.saveImportedSource(""); err != nil {
			a.status = "移除片源失败：" + err.Error()
		}
	}
}

func (a *app) openSite(entry library.Entry) {
	if entry.Site.Kind.Unsupported() {
		a.status = unsupported(entry.Site.Kind)
		return
	}
	assets := strings.TrimSpace(a.browser.assets)
	a.run(jobSite, "打开站点", func(ctx context.Context) (jobResult, error) {
		session, categories, err := source.Open(ctx, entry.Site, assets)
		if err != nil {
			return jobResult{}, err
		}
		var first catalog.Page
		var firstErr error
		if len(categories) > 0 {
			first, firstErr = session.Category(ctx, string(categories[0].ID), 1)
		}
		return jobResult{discard: session.Close, apply: func() {
			a.markSiteHealthy(entry.Key())
			a.closeSite()
			b := &a.browser
			b.session, b.categories, b.site = session, categories, entry
			b.results = first
			if len(categories) > 0 {
				b.category = string(categories[0].ID)
				if firstErr == nil {
					b.page = 1
				}
			}
			a.push(routeSite)
			if firstErr != nil {
				a.status = "站点已打开，读取首个分类失败：" + firstErr.Error()
			}
		}}, nil
	}, func(err error) {
		a.markSiteFailed(entry.Key())
		a.status = jobError("打开站点", err)
	})
}

// closeSite releases the site opened from the sources page.
func (a *app) closeSite() {
	b := &a.browser
	a.closeSession(b.session)
	b.session, b.categories, b.site = nil, nil, library.Entry{}
	b.category, b.page, b.results, b.selected = "", 0, catalog.Page{}, -1
}

func (a *app) loadPage(category string, page int) {
	session := a.browser.session
	if session == nil {
		return
	}
	a.run(jobSite, "读取影片", func(ctx context.Context) (jobResult, error) {
		result, err := session.Category(ctx, category, page)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			b := &a.browser
			b.category, b.page, b.results, b.selected = category, page, result, -1
			if len(result.Videos) == 0 {
				a.setNotice(fmt.Sprintf("第 %d 页 · 0 部影片，可换分类或换站点", page))
			}
		}}, nil
	}, nil)
}

func unsupported(kind config.Kind) string {
	if kind == config.KindCSP {
		return "该站点依赖安卓专属插件，桌面端暂不支持"
	}
	return fmt.Sprintf("当前版本暂不支持 %s 站点，请选择其他站点", kind)
}

// chip is a compact matte selectable control used for toggle pills.
func chip(c *ui.Context, label string, selected bool) ui.Element {
	p := paletteFor(c)
	btn := ui.ButtonBase(c).Padding(6, 12).Radius(9).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	bg := p.controlBg
	border := p.border
	fg := p.textMuted
	if selected {
		bg = p.accentSoft
		border = p.accentBorder
		fg = p.accentLightText
	} else if btn.Hovered() {
		bg = p.controlHover
		border = p.borderControl
		fg = p.text
	}
	animFg := animateButtonChrome(btn, "chip", bg, border, fg)
	btn.Children(func() {
		t := ui.Text(c, label).FontSize(12.5).TextColor(animFg).SingleLine().PassThrough()
		if selected {
			t.Bold()
		}
	})
	return applyPressPose(btn)
}

// underlineTab renders a clean Apple TV style borderless text tab with a
// spring-gliding coral underline shared across tabs in the same group.
func underlineTab(c *ui.Context, group, key, label string, selected bool) ui.Element {
	p := paletteFor(c)
	btn := ui.ButtonBase(c.Key(key)).Padding(4, 2, 0, 2).Cursor(ui.CursorPointer).Label(label)
	fgTarget := p.textMuted
	if selected || btn.Hovered() {
		fgTarget = p.text
	}
	fg := btn.AnimateColor("utab-fg", fgTarget, animColor)
	btn.Children(func() {
		ui.Column(c).Gap(6).AlignItems(ui.Center).PassThrough().Children(func() {
			t := ui.Text(c, label).FontSize(13).TextColor(fg).SingleLine().PassThrough()
			if selected {
				t.Bold()
			}
			ui.Box(c).FillWidth().Height(2.5).PassThrough().Children(func() {
				if selected {
					ui.Box(c.Key("utab-bar-"+group+"-"+key)).Fill().Radius(1.5).Background(p.accent).
						Transition(transMatchIndicator(tabUnderlineMatch{group: group}, false)).PassThrough()
				}
			})
		})
	})
	return applyPressPose(btn)
}

// specMicroBadge renders a crisp player-HUD-style 5px hairline metadata badge (e.g. 网盘).
func specMicroBadge(c *ui.Context, label string, accent bool) {
	p := paletteFor(c)
	bg := ui.RGBA(255, 255, 255, 0.05)
	border := ui.RGBA(255, 255, 255, 0.22)
	fg := ui.RGBA(236, 240, 248, 0.90)
	if accent {
		bg = p.accentSoft
		border = p.accentBorder
		fg = p.accentLightText
	}
	ui.Text(c, label).Padding(2, 6).Radius(5).FontSize(10.5).Bold().
		Background(bg).Border(1, border).TextColor(fg).SingleLine().PassThrough()
}

type sourceChipState int

const (
	sourceChipNormal sourceChipState = iota
	sourceChipSelected
	sourceChipFailed
	sourceChipSelectedFailed
)

// latencyTier classifies a measured source latency into visual tiers:
// 1 = fast (<300ms), 2 = medium (<1200ms), 3 = slow (>=1200ms).
func latencyTier(d int64) int {
	switch {
	case d <= 0:
		return 0
	case d < 300:
		return 1
	case d < 1200:
		return 2
	default:
		return 3
	}
}

// sourceStatusChip renders the unified player-matched source latency chip shared
// by the Detail page source strip and the Sources S2 site browser.
func sourceStatusChip(c *ui.Context, key, name string, isPan bool, latencyText string, tier int, state sourceChipState) ui.Element {
	p := paletteFor(c)
	btn := ui.ButtonBase(c.Key(key)).Height(32).Padding(0, 10).Gap(6).Radius(9).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(name)
	bg := p.controlBg
	border := p.border
	dot := p.success
	nameColor := p.text
	boldName := false
	switch state {
	case sourceChipSelected:
		bg = p.accentSoft
		border = p.accentBorder
		dot = p.accent
		nameColor = p.text
		boldName = true
	case sourceChipFailed:
		bg = p.dangerSoft
		border = p.dangerBorder
		dot = p.danger
		nameColor = p.dangerLightText
	case sourceChipSelectedFailed:
		bg = p.dangerSoft
		border = p.danger
		dot = p.danger
		nameColor = p.dangerLightText
		boldName = true
	default:
		if btn.Hovered() {
			bg = p.controlHover
			border = p.borderControl
		}
	}
	animName := animateButtonChrome(btn, "src-chip", bg, border, nameColor)
	animDot := btn.AnimateColor("src-chip-dot", dot, animColor)
	btn.Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Background(animDot).PassThrough()
		nt := ui.Text(c, name).FontSize(12).TextColor(animName).SingleLine().PassThrough()
		if boldName {
			nt.Bold()
		}
		if isPan {
			ui.Text(c, "网盘").FontSize(9.5).Bold().TextColor(p.accentLightText).SingleLine().PassThrough()
		}
		if latencyText != "" {
			lc := p.textMuted
			switch {
			case state == sourceChipFailed || state == sourceChipSelectedFailed:
				lc = p.dangerLightText
			case tier == 1:
				lc = p.successText
			case tier == 3:
				lc = p.warning
			}
			ui.Text(c, latencyText).FontSize(11).FontFeatures("tnum").
				TextColor(btn.AnimateColor("src-chip-lat", lc, animColor)).
				SingleLine().ContentTransition(ctNumeric).PassThrough()
		}
	})
	return applyPressPose(btn)
}

// siteView browses the categories of one site.
func (a *app) siteView(c *ui.Context) {
	b := &a.browser
	p := paletteFor(c)
	ui.Column(c).Fill().Gap(12).Children(func() {
		if len(b.categories) > 0 {
			ui.ScrollHorizontal(c).FillWidth().Padding(0, scrollGutter, 0, 0).Gap(18).Scrollbars(false).Children(func() {
				for i, category := range b.categories {
					if underlineTab(c, "site-cat", fmt.Sprintf("category-%d", i), category.Name, b.category == string(category.ID)).Clicked() {
						a.loadPage(string(category.ID), 1)
					}
				}
			})
		}
		ui.Row(c).FillWidth().Padding(0, scrollGutter, 0, 0).Gap(10).AlignItems(ui.Center).Children(func() {
			state := "选择分类，浏览这个站点的影片。"
			if a.running(jobSite) {
				state = "正在读取…"
			} else if b.page > 0 {
				state = fmt.Sprintf("第 %d 页", b.page)
			}
			ui.Text(c, state).FontSize(12.5).FontFeatures("tnum").TextColor(p.textMuted).Grow(1).ContentTransition(ctNumeric)
			if b.page > 0 {
				if glassButton(c, "上一页").Padding(5, 12).Disabled(b.page <= 1).Clicked() {
					a.loadPage(b.category, b.page-1)
				}
				if glassButton(c, "下一页").Padding(5, 12).Disabled(!b.results.HasNext).Clicked() {
					a.loadPage(b.category, b.page+1)
				}
			}
		})
		if len(b.results.Videos) == 0 {
			emptyState(c, "这里还没有影片", "可以选择其他分类，或回到片源换一个站点。")
			return
		}
		b.grid.Selected = &b.selected
		b.grid.Label = func(i int) string { return videoTitle(b.results.Videos[i]) }
		b.grid.Key = func(i int) any { return string(b.results.Videos[i].ID) }
		grid := ui.GridView(c, &b.grid, len(b.results.Videos), 152, 252, func(i int) {
			card := a.videoCard(c, b.results.Videos[i], "", false)
			if card.Clicked() {
				video := b.results.Videos[i]
				a.openTitle(videoTitle(video), video.Pic, []library.Item{{Entry: b.site, Video: video}})
			}
		}).Gap(14).Padding(0, scrollGutter, 0, 0).ScrollbarStyle(appScrollbar).Grow(1).FillWidth()
		// Arrows move the choice without opening it; a click or Enter opens it.
		if grid.Submitted() && b.selected >= 0 && b.selected < len(b.results.Videos) {
			video := b.results.Videos[b.selected]
			b.selected = -1
			a.openTitle(videoTitle(video), video.Pic, []library.Item{{Entry: b.site, Video: video}})
		}
	})
}

func emptyState(c *ui.Context, title, hint string) {
	p := paletteFor(c)
	ui.Column(c).Grow(1).FillWidth().Center().Gap(10).Children(func() {
		ui.Text(c, title).FontSize(22).Bold()
		ui.Text(c, hint).TextColor(p.textMuted)
	})
}

func emptyStateWithAction(c *ui.Context, title, hint, actionLabel string, onAction func()) {
	p := paletteFor(c)
	ui.Column(c).Grow(1).FillWidth().Center().Gap(12).Children(func() {
		ui.Text(c, title).FontSize(22).Bold()
		ui.Text(c, hint).TextColor(p.textMuted)
		if actionLabel != "" && onAction != nil && primaryButton(c, actionLabel).Clicked() {
			onAction()
		}
	})
}

// welcomeView is the home page before any source was imported.
func (a *app) welcomeView(c *ui.Context) {
	b := &a.browser
	p := paletteFor(c)
	ui.Column(c).Fill().Center().Children(func() {
		box := ui.Column(c).Width(540).MaxWidth(540).Padding(30).Gap(18).Radius(30)
		applyPane(box, p, p.paneMat)
		box.Children(func() {
			ui.Text(c, "添加片源，开始观看").FontSize(30).Bold()
			ui.Text(c, "小幕不内置任何片源。粘贴你自己的配置地址，影片和直播就会出现在这里。地址只保存在本机。").
				TextColor(p.textMuted)
			if ui.TextInput(c, &b.configURL).Label("配置地址").Placeholder("https://example.com/config.json").FillWidth().Submitted() {
				a.loadConfig(b.configURL)
			}
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "自动识别影视配置与直播频道表，也可直接拖入本地视频。").FontSize(13).TextColor(p.textMuted).Grow(1)
				if glassButton(c, "打开本地视频").Clicked() {
					a.openLocalFileDialog()
				}
				importLabel := "导入"
				if a.running(jobConfig) {
					importLabel = "导入中…"
				}
				if primaryButton(c, importLabel).Disabled(a.running(jobConfig)).Clicked() {
					a.loadConfig(b.configURL)
				}
			})
		})
	})
}

func (a *app) importDialog(c *ui.Context) {
	b := &a.browser
	if b.importOpen && !b.importWasOpen {
		if b.importURL == "" && b.configURL != "" {
			b.importURL = b.configURL
		}
		if b.importAssets == "" && b.assets != "" {
			b.importAssets = b.assets
		}
		b.importWasOpen = true
	}
	p := paletteFor(c)
	_, winH := c.Size()
	keyedDialog(c, "dlg-import", &b.importOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(p.dialogScrim).Transition(transDialogScrim)
		panel.Width(520).MaxWidth(520).MaxHeight(max(float32(260), winH-32)).Padding(24).Gap(12).Radius(26).TextColor(p.text)
		applyPane(panel, p, p.dialogMat)
		applyDialogPanelPose(c, panel)
		title := "添加片源"
		hint := "粘贴影视配置或直播频道表地址（自动识别），导入后即可全库搜索与观看。"
		if a.library.root != nil {
			title = "更换片源"
			hint = "粘贴新地址将替换当前片源，你的观看历史与追剧都会保留。"
		}
		ui.Text(c, title).FontSize(24).Bold()
		ui.Scroll(c).FillWidth().MaxHeight(max(float32(120), winH-176)).Gap(12).ScrollbarStyle(appScrollbar).Children(func() {
			ui.Text(c, hint).TextColor(p.textMuted)
			if ui.TextInput(c, &b.importURL).Label("配置地址").Placeholder("https://example.com/config.json").FillWidth().AutoFocus().Submitted() {
				if !a.running(jobConfig) {
					a.loadConfigFromDialog(b.importURL, b.importAssets)
				}
			}
			ui.Collapsible(c.Key("import-advanced"), "高级选项", &b.importAdvanced, func() {
				ui.Column(c).FillWidth().Padding(6, 0, 2, 0).Gap(6).Children(func() {
					ui.TextInput(c, &b.importAssets).Label("脚本依赖目录（可选）").Placeholder("仅个别脚本源需要，通常留空即可").FillWidth()
				})
			})
			if b.importErr != "" {
				ui.Text(c.Key("import-err"), b.importErr).FontSize(13).TextColor(p.warning).Transition(transInlineBanner)
			}
		})
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if glassButton(c, "取消").Clicked() {
				b.importOpen = false
			}
			submitLabel := "导入"
			if a.running(jobConfig) {
				submitLabel = "导入中…"
			}
			if primaryButton(c, submitLabel).Disabled(a.running(jobConfig)).Clicked() {
				a.loadConfigFromDialog(b.importURL, b.importAssets)
			}
		})
	})
	if !b.importOpen && b.importWasOpen {
		b.importWasOpen = false
		b.importErr = ""
		if b.importJobGen != 0 {
			a.cancelJobIfGen(jobConfig, b.importJobGen)
			b.importJobGen = 0
		}
	}
}

// videoCard is a poster with its title over it. badge, when set, says where
// the video comes from; a strong badge marks an exact match.
func (a *app) videoCard(c *ui.Context, video catalog.Video, badge string, strong bool) ui.Element {
	p := paletteFor(c)
	card := ui.Box(c).Fill().Radius(11).Clip().Background(p.cardPlaceholder).Border(1, p.border).Cursor(ui.CursorPointer)
	card.Children(func() {
		applyPosterImage(c, card, a.poster(c, video.Pic))
		if badge != "" {
			if strong {
				ui.Text(c, badge).Absolute().Left(7).Top(7).Padding(2, 7).Radius(6).
					FontSize(11).Bold().Background(p.accent).TextColor(p.accentText).SingleLine().PassThrough()
			} else if strings.Contains(badge, "豆瓣 ") {
				score := strings.Replace(badge, "豆瓣 ", "", 1)
				ui.Row(c).Absolute().Left(7).Top(7).Padding(2, 7).Gap(4).Radius(6).AlignItems(ui.Center).
					Label(badge).Background(ui.RGBA(12, 15, 24, 0.86)).Border(1, p.border).PassThrough().Children(func() {
					ui.Icon(c, iconStarFilled).Size(10, 10).TextColor(p.gold).PassThrough()
					ui.Text(c, score).FontSize(11).Bold().TextColor(p.gold).SingleLine().PassThrough()
				})
			} else {
				ui.Text(c, badge).Absolute().Left(7).Top(7).Padding(2, 7).Radius(6).
					FontSize(11).Background(ui.RGBA(12, 15, 24, 0.86)).Border(1, p.border).
					TextColor(p.text).SingleLine().PassThrough()
			}
		}
		ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Padding(28, 10, 10, 10).Gap(3).
			Gradient(ui.RGBA(10, 12, 20, 0), ui.RGBA(10, 12, 20, 0.93), 180).PassThrough().Children(func() {
			ui.Text(c, videoTitle(video)).FontSize(13).Bold().TextColor(p.text).SingleLine().PassThrough()
			if meta := videoMeta(video.Year, video.Type, video.Remarks); meta != "" {
				ui.Text(c, meta).FontSize(11).TextColor(p.textMuted).SingleLine().PassThrough()
			}
		})
	})
	return applyCardPose(card)
}

func videoTitle(video catalog.Video) string {
	if video.Name != "" {
		return video.Name
	}
	return string(video.ID)
}

func videoMeta(parts ...string) string {
	var shown []string
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			shown = append(shown, part)
		}
	}
	return strings.Join(shown, " · ")
}
