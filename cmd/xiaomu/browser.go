package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/source"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// browser holds the import dialog, the toolbar's search text and the one
// site opened from the sources page.
type browser struct {
	configURL, assets, query string
	importOpen               bool

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
	jobConfig jobSlot = iota // importing or refreshing a configuration
	jobLive                  // importing a live list or reading its channels
	jobSite                  // browsing one site
	jobTitle                 // reading a title from one of its sources
	jobPlay                  // resolving an episode into media
	jobSlots
)

type job struct {
	cancel context.CancelFunc
	gen    uint64
}

// A job returns an update to apply on the UI thread and optional cleanup for
// a result that arrives after the job was replaced, canceled or failed.
type jobResult struct {
	apply   func()
	discard func()
}

func (a *app) running(slot jobSlot) bool { return a.jobs[slot].cancel != nil }

func (a *app) busy() bool {
	for i := range a.jobs {
		if a.jobs[i].cancel != nil {
			return true
		}
	}
	return false
}

// cancelJob abandons the slot's job; its result is discarded when it returns.
func (a *app) cancelJob(slot jobSlot) {
	j := &a.jobs[slot]
	if j.cancel != nil {
		j.cancel()
		j.cancel = nil
	}
	j.gen++
}

func (a *app) cancelJobs() {
	for slot := range a.jobs {
		a.cancelJob(jobSlot(slot))
	}
	a.status = "已取消，可以重试或选择其他站点"
}

// run starts work in slot, replacing the job already there. fail, when not
// nil, receives the error on the UI thread instead of the status line, for
// pages that show it in place.
func (a *app) run(slot jobSlot, label string, work func(context.Context) (jobResult, error), fail func(error)) {
	if a.closed {
		return
	}
	a.cancelJob(slot)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	j := &a.jobs[slot]
	j.cancel = cancel
	gen := j.gen
	a.status = label + "…"
	dispatch := a.dispatch
	execute := func() {
		result, err := work(ctx)
		finish := func() {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			cancel()
			current := j.gen == gen
			if current {
				j.cancel = nil
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
			if a.status == label+"…" {
				a.status = ""
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
	a.stopScan(&a.library.feed)
	a.stopScan(&a.library.search)
	a.stopDiscovery()
	for i := range a.jobs {
		if cancel := a.jobs[i].cancel; cancel != nil {
			cancel()
		}
	}
	a.flushHistory()
	a.closeSite()
	a.clearTitle()
	if a.player != nil {
		a.player.Close()
		a.player = nil
	}
	if a.proxy != nil {
		_ = a.proxy.Close()
		a.proxy = nil
	}
}

func (a *app) loadConfig(raw string) {
	a.readConfig(raw, false)
}

func (a *app) readConfig(raw string, restoring bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		a.status = "请先粘贴配置地址"
		return
	}
	a.run(jobConfig, "加载配置", func(ctx context.Context) (jobResult, error) {
		doc, err := config.Read(ctx, raw, nil)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() { a.applyConfig(raw, doc, restoring) }}, nil
	}, nil)
}

func (a *app) applyConfig(raw string, doc *config.Config, restoring bool) {
	b := &a.browser
	b.importOpen = false
	if len(doc.Sites) == 0 && len(doc.Lines) == 0 {
		if len(doc.Lives) == 0 {
			a.status = "这个地址里没有站点，也没有直播频道"
			return
		}
		// A plain channel list is a live source; the film library stays.
		if !restoring || a.live.url == "" {
			a.applyLiveImport(raw, doc.Lives)
		}
		return
	}
	a.cancelJob(jobSite)
	a.closeSite()
	a.clearTitle()
	a.nav = nil
	b.configURL = raw
	if a.live.url == "" {
		a.setLiveSources(doc.Lives)
	}
	a.resetLibrary(doc)
	if a.persist {
		if err := a.saveImportedSource(raw); err != nil {
			a.status = "保存片源地址失败：" + err.Error()
		}
	}
}

// removeConfig forgets the imported configuration. Play history stays, shown
// as unavailable until its source is imported again.
func (a *app) removeConfig() {
	a.cancelJob(jobConfig)
	a.cancelJob(jobSite)
	a.closeSite()
	a.clearTitle()
	a.resetLibrary(nil)
	a.browser.configURL = ""
	if a.live.url == "" {
		a.setLiveSources(nil)
	}
	a.status = "已移除片源"
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
	}, nil)
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
			a.status = fmt.Sprintf("第 %d 页 · %d 部影片", page, len(result.Videos))
			if len(result.Videos) == 0 {
				a.status += "，可换分类或换站点"
			}
		}}, nil
	}, nil)
}

func unsupported(kind config.Kind) string {
	if kind == config.KindCSP {
		return "此站点依赖专用 Android 爬虫，当前尚未适配；通用 XBPQ 规则已支持"
	}
	return fmt.Sprintf("当前版本暂不支持 %s 站点，请选择其他站点", kind)
}

// chip is one choice among several: a category, a play line or an episode.
func chip(c *ui.Context, label string, selected bool) ui.Element {
	button := ui.Button(c, label).Radius(18).Padding(7, 14)
	if selected {
		return button.Background(ui.RGB(243, 239, 231)).TextColor(playerInk)
	}
	return button.Material(glassControl)
}

// siteView browses the categories of one site.
func (a *app) siteView(c *ui.Context) {
	b := &a.browser
	ui.Column(c).Fill().Gap(14).Children(func() {
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			for i, category := range b.categories {
				if chip(c, category.Name, b.category == string(category.ID)).Key(fmt.Sprintf("category-%d", i)).Clicked() {
					a.loadPage(string(category.ID), 1)
				}
			}
		})
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			state := "选择分类，浏览这个站点的影片。"
			if a.running(jobSite) {
				state = "正在读取…"
			} else if b.page > 0 {
				state = fmt.Sprintf("第 %d 页", b.page)
			}
			ui.Text(c, state).TextColor(textMuted).Grow(1)
			if b.page > 0 {
				if glassButton(c, "上一页").Disabled(b.page <= 1).Clicked() {
					a.loadPage(b.category, b.page-1)
				}
				if glassButton(c, "下一页").Disabled(!b.results.HasNext).Clicked() {
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
		if ui.GridView(c, &b.grid, len(b.results.Videos), 152, 252, func(i int) {
			a.videoCard(c, b.results.Videos[i], "", false)
		}).Gap(14).Grow(1).FillWidth().Changed() && b.selected >= 0 && b.selected < len(b.results.Videos) {
			video := b.results.Videos[b.selected]
			b.selected = -1
			a.openTitle(videoTitle(video), video.Pic, []library.Item{{Entry: b.site, Video: video}})
		}
	})
}

func emptyState(c *ui.Context, title, hint string) {
	ui.Column(c).Grow(1).FillWidth().Center().Gap(10).Children(func() {
		ui.Text(c, title).FontSize(22).Bold()
		ui.Text(c, hint).TextColor(textMuted)
	})
}

// welcomeView is the home page before any source was imported.
func (a *app) welcomeView(c *ui.Context) {
	b := &a.browser
	ui.Column(c).Fill().Center().Children(func() {
		ui.Column(c).Width(540).MaxWidth(540).Padding(30).Gap(18).Radius(30).Material(glassPane).Children(func() {
			ui.Text(c, "添加片源，开始观看").FontSize(30).Bold()
			ui.Text(c, "小幕不内置任何片源。粘贴你自己的配置地址，影片和直播就会出现在这里。地址只保存在这台 Mac 上。").
				TextColor(textMuted)
			ui.TextInput(c, &b.configURL).Label("配置地址").Placeholder("https://example.com/config.json").FillWidth()
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "支持 TVBox 配置、M3U 和 TXT 直播列表。").FontSize(13).TextColor(textMuted).Grow(1)
				if primaryButton(c, "导入").Clicked() {
					a.loadConfig(b.configURL)
				}
			})
		})
	})
}

func (a *app) importDialog(c *ui.Context) {
	b := &a.browser
	ui.DialogBase(c, &b.importOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(2, 8, 22, 0.67))
		panel.Width(520).MaxWidth(520).Padding(26).Gap(14).Radius(26).Material(glass.Glass{})
		ui.Text(c, "添加片源").FontSize(24).Bold()
		ui.Text(c, "导入后自动汇集影片推荐，可直接搜索整个片库。也支持 M3U 和 TXT 直播列表。").TextColor(textMuted)
		ui.TextInput(c, &b.configURL).Label("配置地址").Placeholder("https://example.com/config.json").FillWidth()
		ui.TextInput(c, &b.assets).Label("脚本目录（可选）").Placeholder("本地依赖目录").FillWidth()
		if a.status != "" {
			ui.Text(c, a.status).TextColor(textMuted)
		}
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if glassButton(c, "取消").Clicked() {
				b.importOpen = false
			}
			if primaryButton(c, "加载配置").Clicked() {
				a.loadConfig(b.configURL)
			}
		})
	})
}

// videoCard is a poster with its title over it. badge, when set, says where
// the video comes from; a strong badge marks an exact match.
func (a *app) videoCard(c *ui.Context, video catalog.Video, badge string, strong bool) {
	ui.Box(c).Fill().Radius(14).Clip().Background(ui.Hex("#24242c")).Children(func() {
		if poster := a.poster(video.Pic); poster != nil {
			ui.Image(c, poster).Fill().Fit(ui.Cover)
		}
		if badge != "" {
			text := ui.Text(c, badge).Absolute().Left(8).Top(8).Padding(3, 8).Radius(10).FontSize(12).SingleLine()
			if strong {
				text.Background(ui.RGBA(255, 90, 64, 0.92)).TextColor(accentInk)
			} else {
				text.Background(ui.RGBA(10, 13, 24, 0.78)).TextColor(ui.RGB(255, 255, 255))
			}
		}
		ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Padding(26, 12, 12, 12).Gap(4).
			Gradient(ui.RGBA(10, 13, 24, 0), ui.RGBA(10, 13, 24, 0.9), 180).Children(func() {
			ui.Text(c, videoTitle(video)).Bold().TextColor(ui.RGB(255, 255, 255)).SingleLine()
			if meta := videoMeta(video.Year, video.Type, video.Remarks); meta != "" {
				ui.Text(c, meta).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.86)).SingleLine()
			}
		})
	})
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
