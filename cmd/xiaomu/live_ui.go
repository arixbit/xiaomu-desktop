package main

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo/ui"
)

type liveFilterCache struct {
	ptr        *iptv.Channel
	length     int
	group      string
	query      string
	favVersion uint64
	indices    []int
	valid      bool
}

type liveBrowser struct {
	url, input, query, group string
	importOpen, restore      bool
	importWasOpen            bool
	importErr                string
	importJobGen             uint64
	sources                  []iptv.Source
	cachedChannels           map[string][]iptv.Channel
	cachedEPG                map[string]string
	staleChannels            map[string]bool
	selectedSource           int
	channels                 []iptv.Channel
	epgURL                   string
	xmltvCache               map[string]*iptv.EPG
	diypCache                map[string][]iptv.Program
	diypLoadingKey           string
	epgErr                   string
	epgClient                *http.Client
	nowFn                    func() time.Time
	state                    liveStateStore
	pendingResume            string
	pendingResumeStream      int
	panelTab                 int // 0=频道, 1=节目单
	catchupProgram           *iptv.Program
	groupsCache              []string
	groupsCachePtr           *iptv.Channel
	groupsCacheLen           int
	groupsCacheValid         bool
	filteredAllCache         liveFilterCache
	filteredQueryCache       liveFilterCache
	channelList              ui.ListState
	panelChannelList         ui.ListState
	resetLists               bool
	playingChannel           int
	playingStream            int
	attempts                 int
	started                  time.Time
}

func channelsPtr(channels []iptv.Channel) *iptv.Channel {
	if len(channels) == 0 {
		return nil
	}
	return &channels[0]
}

func (l *liveBrowser) groups() []string {
	ptr, n := channelsPtr(l.channels), len(l.channels)
	if l.groupsCacheValid && l.groupsCachePtr == ptr && l.groupsCacheLen == n {
		return l.groupsCache
	}
	seen := make(map[string]struct{}, 16)
	groups := make([]string, 0, 16)
	for _, channel := range l.channels {
		if channel.Group == "" {
			continue
		}
		if _, dup := seen[channel.Group]; dup {
			continue
		}
		seen[channel.Group] = struct{}{}
		groups = append(groups, channel.Group)
	}
	l.groupsCache = groups
	l.groupsCachePtr = ptr
	l.groupsCacheLen = n
	l.groupsCacheValid = true
	return groups
}

func (l *liveBrowser) filteredChannels(withQuery bool) []int {
	query := ""
	cache := &l.filteredAllCache
	if withQuery {
		query = strings.ToLower(strings.TrimSpace(l.query))
		cache = &l.filteredQueryCache
	}
	ptr, n := channelsPtr(l.channels), len(l.channels)
	favVer := l.state.version
	if cache.valid && cache.ptr == ptr && cache.length == n && cache.group == l.group && cache.query == query && cache.favVersion == favVer {
		return cache.indices
	}
	indices := make([]int, 0, n)
	for i, channel := range l.channels {
		if l.group == liveGroupFavorites {
			if !l.state.isFavorite(channel.Name) {
				continue
			}
		} else if l.group != "" && channel.Group != l.group {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(channel.Name), query) {
			continue
		}
		indices = append(indices, i)
	}
	// Float favorited/pinned channels to the top while preserving relative order.
	if len(l.state.favSet) > 0 && len(indices) > 1 {
		sort.SliceStable(indices, func(a, b int) bool {
			ra, oka := l.state.favoriteRank(l.channels[indices[a]].Name)
			rb, okb := l.state.favoriteRank(l.channels[indices[b]].Name)
			if oka != okb {
				return oka
			}
			if oka && okb {
				return ra < rb
			}
			return false
		})
	}
	*cache = liveFilterCache{
		ptr:        ptr,
		length:     n,
		group:      l.group,
		query:      query,
		favVersion: favVer,
		indices:    indices,
		valid:      true,
	}
	return indices
}

func liveSourceCacheKey(source iptv.Source) string {
	if source.URL == "" {
		return ""
	}
	if len(source.Headers) == 0 {
		return source.URL
	}
	keys := make([]string, 0, len(source.Headers))
	for k := range source.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(source.URL)
	for _, k := range keys {
		b.WriteByte('\x00')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(source.Headers[k])
	}
	return b.String()
}

func cloneLiveChannels(channels []iptv.Channel) []iptv.Channel {
	if len(channels) == 0 {
		return nil
	}
	out := make([]iptv.Channel, len(channels))
	for i, ch := range channels {
		ch.Streams = make([]iptv.Stream, len(ch.Streams))
		for j, st := range channels[i].Streams {
			ch.Streams[j] = iptv.Stream{URL: st.URL, Headers: maps.Clone(st.Headers)}
		}
		out[i] = ch
	}
	return out
}

func cloneLiveChannelsMap(m map[string][]iptv.Channel) map[string][]iptv.Channel {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string][]iptv.Channel, len(m))
	for k, v := range m {
		if len(v) > 0 {
			out[k] = cloneLiveChannels(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v = strings.TrimSpace(v); v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *app) activeLiveChannelCache() map[string][]iptv.Channel {
	if a.live.url != "" {
		return a.live.cachedChannels
	}
	return a.library.liveChannels
}

func (a *app) setActiveLiveChannelCache(key string, channels []iptv.Channel) {
	copied := cloneLiveChannels(channels)
	if a.live.url != "" {
		if a.live.cachedChannels == nil {
			a.live.cachedChannels = map[string][]iptv.Channel{}
		}
		a.live.cachedChannels[key] = copied
		return
	}
	if a.library.liveChannels == nil {
		a.library.liveChannels = map[string][]iptv.Channel{}
	}
	a.library.liveChannels[key] = copied
}

func (a *app) activeLiveEPGCache() map[string]string {
	if a.live.url != "" {
		return a.live.cachedEPG
	}
	return a.library.liveEPG
}

func (a *app) setActiveLiveEPGCache(key, epgURL string) {
	epgURL = strings.TrimSpace(epgURL)
	if a.live.url != "" {
		if epgURL == "" {
			delete(a.live.cachedEPG, key)
			return
		}
		if a.live.cachedEPG == nil {
			a.live.cachedEPG = map[string]string{}
		}
		a.live.cachedEPG[key] = epgURL
		return
	}
	if epgURL == "" {
		delete(a.library.liveEPG, key)
		return
	}
	if a.library.liveEPG == nil {
		a.library.liveEPG = map[string]string{}
	}
	a.library.liveEPG[key] = epgURL
}

func (a *app) applyLiveChannels(index int, channels []iptv.Channel) {
	a.applyLivePlaylist(index, channels, "")
}

func (a *app) applyLivePlaylist(index int, channels []iptv.Channel, epgURL string) {
	a.live.channels, a.live.selectedSource = channels, index
	a.live.group, a.live.query = "", ""
	a.live.channelList, a.live.panelChannelList = ui.ListState{}, ui.ListState{}
	if epgURL == "" && index >= 0 && index < len(a.live.sources) {
		epgURL = a.live.sources[index].EPG
	}
	a.live.epgURL = strings.TrimSpace(epgURL)
	a.live.epgErr = ""
	a.live.diypLoadingKey = ""
	a.cancelJob(jobEPG)
	a.ensureEPGLoaded()
	a.tryPendingLiveResume()
}

func (a *app) setLiveSources(sources []iptv.Source) {
	a.cancelJob(jobEPG)
	a.live.sources = sources
	a.live.channels = nil
	a.live.selectedSource = -1
	a.live.epgURL = ""
	a.live.epgErr = ""
	a.live.diypLoadingKey = ""
	a.live.group, a.live.query = "", ""
	a.live.channelList, a.live.panelChannelList = ui.ListState{}, ui.ListState{}
}

// showLive loads what the live tab needs when it comes on show.
func (a *app) showLive() {
	if a.live.restore {
		a.importLive(a.live.url)
	} else if a.live.selectedSource < 0 && len(a.live.sources) > 0 {
		a.loadLiveSource(0)
	}
}

func (a *app) importLive(raw string) {
	a.loadLiveImport(raw, true)
}

func (a *app) refreshLive(raw string) {
	a.loadLiveImport(raw, false)
}

func (a *app) openLiveDialog() {
	a.live.input = a.live.url
	a.live.importErr = ""
	a.live.importJobGen = 0
	a.live.importOpen = true
}

func (a *app) loadLiveImport(raw string, switchToLive bool) {
	a.loadLiveImportMode(raw, switchToLive, false)
}

func (a *app) loadLiveImportFromDialog(raw string) {
	a.loadLiveImportMode(raw, true, true)
}

func (a *app) loadLiveImportMode(raw string, switchToLive, fromDialog bool) {
	raw = cleanUserURL(raw)
	if raw == "" {
		msg := "请先粘贴直播频道表地址"
		if a.live.importOpen {
			a.live.importErr = msg
		} else {
			a.setNotice(msg)
		}
		return
	}
	a.live.importErr = ""
	a.run(jobLive, "导入直播", func(ctx context.Context) (jobResult, error) {
		doc, err := config.Load(ctx, raw, "", nil)
		if err != nil {
			return jobResult{}, err
		}
		if len(doc.Lives) == 0 {
			return jobResult{}, fmt.Errorf("这个地址里没有直播频道，请粘贴 M3U、TXT 频道表或含直播的影视配置")
		}
		return jobResult{apply: func() {
			if fromDialog {
				a.live.importJobGen = 0
			}
			a.applyLiveImport(raw, doc.Lives, switchToLive)
		}}, nil
	}, func(err error) {
		if fromDialog {
			a.live.importJobGen = 0
		}
		msg := jobError("导入直播", err)
		if a.live.importOpen {
			a.live.importErr = msg
		} else if !switchToLive {
			a.setBackgroundError(msg)
		} else {
			a.status = msg
		}
	})
	if fromDialog {
		a.live.importJobGen = a.jobGen(jobLive)
	}
}

// applyLiveImport makes a channel list the live source. A list imported on its
// own takes priority over the lives of the film library.
func (a *app) applyLiveImport(raw string, sources []iptv.Source, switchToLive bool) {
	prevURL := a.live.url
	if prevURL != raw {
		a.live.cachedChannels = nil
		a.live.cachedEPG = nil
		a.live.staleChannels = nil
	} else if len(a.live.cachedChannels) > 0 {
		a.live.staleChannels = make(map[string]bool, len(a.live.cachedChannels))
		for k := range a.live.cachedChannels {
			a.live.staleChannels[k] = true
		}
	}
	a.setLiveSources(sources)
	a.live.url, a.live.input, a.live.restore, a.live.importOpen, a.live.importErr = raw, raw, false, false, ""
	if switchToLive {
		a.switchTab(routeLive)
	} else {
		a.setNotice("直播源已刷新")
	}
	if a.persist {
		if err := a.saveLiveSource(raw); err != nil {
			msg := "保存直播地址失败：" + err.Error()
			if !switchToLive {
				a.setBackgroundError(msg)
			} else {
				a.status = msg
			}
		}
	}
}

// removeLive forgets the separately imported list; the lives of the film
// library, if any, show again.
func (a *app) removeLive() {
	a.cancelJob(jobLive)
	a.live.url, a.live.input, a.live.restore = "", "", false
	a.live.cachedChannels = nil
	a.live.cachedEPG = nil
	a.live.staleChannels = nil
	var lives []iptv.Source
	if root := a.library.root; root != nil {
		lives = append(lives, root.Lives...)
	}
	for _, document := range a.library.documents {
		if document.doc != a.library.root {
			lives = append(lives, document.doc.Lives...)
		}
	}
	a.setLiveSources(lives)
	a.setNotice("已移除直播源")
	if a.persist {
		if err := a.saveLiveSource(""); err != nil {
			a.status = "移除直播源失败：" + err.Error()
		}
	}
}

func (a *app) loadLiveSource(index int) {
	if index < 0 || index >= len(a.live.sources) {
		return
	}
	source := a.live.sources[index]
	key := liveSourceCacheKey(source)
	var cached []iptv.Channel
	stale := false
	if key != "" {
		cached = a.activeLiveChannelCache()[key]
		if a.live.url != "" && a.live.staleChannels[key] {
			stale = true
		}
	}
	if !stale && len(cached) > 0 {
		a.cancelJob(jobLive)
		cachedEPG := source.EPG
		if cachedEPG == "" {
			cachedEPG = a.activeLiveEPGCache()[key]
		}
		a.applyLivePlaylist(index, cloneLiveChannels(cached), cachedEPG)
		return
	}
	expectedLiveURL := a.live.url
	expectedConfigURL := a.browser.configURL
	var fail func(error)
	if stale && len(cached) > 0 {
		fallback := cloneLiveChannels(cached)
		fallbackEPG := source.EPG
		if fallbackEPG == "" {
			fallbackEPG = a.activeLiveEPGCache()[key]
		}
		fail = func(err error) {
			if a.live.url == expectedLiveURL && a.browser.configURL == expectedConfigURL &&
				index < len(a.live.sources) && liveSourceCacheKey(a.live.sources[index]) == key {
				delete(a.live.staleChannels, key)
				a.applyLivePlaylist(index, fallback, fallbackEPG)
			}
			a.status = jobError("加载频道", err)
		}
	}
	a.run(jobLive, "加载频道", func(ctx context.Context) (jobResult, error) {
		pl, err := config.LivePlaylist(ctx, source, nil)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if a.live.url != expectedLiveURL || a.browser.configURL != expectedConfigURL ||
				index >= len(a.live.sources) || liveSourceCacheKey(a.live.sources[index]) != key {
				return
			}
			a.applyLivePlaylist(index, pl.Channels, pl.EPG)
			if key != "" {
				delete(a.live.staleChannels, key)
				a.setActiveLiveChannelCache(key, pl.Channels)
				a.setActiveLiveEPGCache(key, pl.EPG)
				if a.persist {
					if a.live.url != "" {
						_ = a.saveLiveCache()
					} else if a.library.root != nil && a.browser.configURL != "" {
						_ = a.saveConfigCache()
					}
				}
			}
		}}, nil
	}, fail)
}

func (a *app) playLiveChannel(index, streamIndex int) {
	a.live.attempts = 0
	a.startLiveChannel(index, streamIndex)
}

func (a *app) startLiveChannel(index, streamIndex int) {
	if index < 0 || index >= len(a.live.channels) {
		return
	}
	channel := a.live.channels[index]
	if streamIndex < 0 || streamIndex >= len(channel.Streams) {
		return
	}

	for a.live.attempts < len(channel.Streams) {
		stream := channel.Streams[streamIndex]
		a.live.attempts++
		a.live.playingChannel, a.live.playingStream = index, streamIndex
		u, err := url.Parse(strings.TrimSpace(stream.URL))
		scheme := ""
		if err == nil {
			scheme = strings.ToLower(u.Scheme)
		}
		switch {
		case err == nil && u.Host != "" && (scheme == "http" || scheme == "https"):
			a.live.started = time.Now()
			if a.playURL(stream.URL, stream.Headers) {
				a.playingLive = true
				a.live.catchupProgram = nil
				if a.player != nil {
					a.player.SetRate(1)
				}
				a.recordLastLiveChannel(index, streamIndex)
				a.ensureChannelDIYPEPG(channel)
				a.syncNowPlaying()
				return
			}
		case err == nil && u.Host != "" && (scheme == "rtsp" || scheme == "rtmp") && (mpvBackendSupported() || a.openMPV != nil):
			a.live.started = time.Now()
			if a.playDirectMPVURL(stream.URL, stream.Headers) {
				a.playingLive = true
				a.live.catchupProgram = nil
				if a.player != nil {
					a.player.SetRate(1)
				}
				a.recordLastLiveChannel(index, streamIndex)
				a.ensureChannelDIYPEPG(channel)
				a.syncNowPlaying()
				return
			}
		default:
			if mpvBackendSupported() {
				a.status = "该频道使用暂不支持的直播协议（当前支持 HTTP/HTTPS、RTSP、RTMP）"
			} else {
				a.status = "该频道使用非 HTTP 直播协议，桌面端暂不支持直接播放"
			}
		}
		streamIndex = (streamIndex + 1) % len(channel.Streams)
	}

}

// listRow is a row of a plain list: a tab of the sidebar, a channel group.
func listRow(c *ui.Context, label string, selected bool) ui.Element {
	p := paletteFor(c)
	row := ui.Row(c.Key(label)).FillWidth().Height(38).Padding(0, 12).Radius(11).
		AlignItems(ui.Center).Cursor(ui.CursorPointer)
	targetBg := ui.Transparent
	targetFg := p.textMuted
	if selected {
		targetBg = p.surfaceSelected
		targetFg = p.text
	} else if row.Hovered() {
		targetBg = p.surfaceHover
		targetFg = p.text
	}
	row.Background(row.AnimateColor("row-bg", targetBg, animColor))
	applyPressPose(row)
	fg := row.AnimateColor("row-fg", targetFg, animColor)
	row.Children(func() {
		text := ui.Text(c, label).SingleLine().TextColor(fg)
		if selected {
			text.Bold()
		}
	})
	return row
}

func (a *app) liveView(c *ui.Context) {
	live := &a.live
	p := paletteFor(c)
	if len(live.sources) == 0 {
		emptyStateWithAction(c, "添加直播源，开始看电视", "支持 M3U、TXT 频道表，以及影视配置中附带的直播。", "添加直播源", func() {
			a.openLiveDialog()
		})
		return
	}
	ui.Column(c).Fill().Gap(14).Children(func() {
		showSources := len(live.sources) > 1
		showSearch := len(live.channels) > 0
		if showSources || showSearch {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				if showSources {
					ui.Row(c).Grow(1).MinWidth(0).Wrap().Gap(8).Children(func() {
						for i, source := range live.sources {
							if chip(c.Key(fmt.Sprintf("live-source-%d", i)), source.Name, i == live.selectedSource).Clicked() {
								a.loadLiveSource(i)
							}
						}
					})
				} else {
					ui.Spacer(c)
				}
				if showSearch {
					sf := ui.SearchField(c, &live.query).Label("频道搜索").Placeholder("搜索频道").Width(240).Radius(20)
					if p.reduceTransparency {
						sf.Background(p.surface).Border(1, p.border)
					} else {
						sf.Material(p.controlMat)
						if !p.dark || p.highContrast {
							sf.Border(1, p.border)
						}
					}
					if sf.Changed() {
						live.channelList = ui.ListState{}
					}
				}
			})
		}
		if len(live.channels) == 0 {
			if a.running(jobLive) {
				emptyState(c, "正在加载频道…", "频道读取后会显示在这里。")
			} else {
				emptyStateWithAction(c, "这个直播来源没有频道", "可切换上方其他直播来源，或更换直播源地址。", "更换直播源", func() {
					a.openLiveDialog()
				})
			}
			return
		}
		a.liveLastResumeBar(c)
		groups := live.groups()
		hasFavs := len(live.state.favorites) > 0
		if !hasFavs && live.group == liveGroupFavorites {
			live.group = ""
			live.resetLists = true
		}
		if live.resetLists {
			live.channelList, live.panelChannelList = ui.ListState{}, ui.ListState{}
			live.resetLists = false
		}
		ui.Row(c).Grow(1).FillWidth().MinHeight(0).Gap(16).Children(func() {
			if len(groups) > 0 || hasFavs {
				ui.Scroll(c).Width(168).FillHeight().Gap(2).Children(func() {
					if listRow(c, "全部频道", live.group == "").Clicked() {
						if live.group != "" {
							live.group = ""
							live.channelList, live.panelChannelList = ui.ListState{}, ui.ListState{}
						}
					}
					if hasFavs {
						favLabel := fmt.Sprintf("★ 收藏 (%d)", len(live.state.favorites))
						if listRow(c, favLabel, live.group == liveGroupFavorites).Label("收藏频道").Clicked() {
							if live.group != liveGroupFavorites {
								live.group = liveGroupFavorites
								live.channelList, live.panelChannelList = ui.ListState{}, ui.ListState{}
							}
						}
					}
					for _, group := range groups {
						if listRow(c, group, live.group == group).Clicked() {
							if live.group != group {
								live.group = group
								live.channelList, live.panelChannelList = ui.ListState{}, ui.ListState{}
							}
						}
					}
				})
			}
			indices := live.filteredChannels(true)
			if len(indices) == 0 {
				emptyStateWithAction(c, "没有找到匹配的频道", "可以清空当前筛选条件，查看全部频道。", "清空筛选", func() {
					live.query = ""
					live.group = ""
					live.channelList, live.panelChannelList = ui.ListState{}, ui.ListState{}
				})
				return
			}
			live.channelList.Key = func(row int) any { return indices[row] }
			live.channelList.Label = func(row int) string { return live.channels[indices[row]].Name }
			list := ui.List(c, &live.channelList, len(indices), func(rowIndex int) {
				i := indices[rowIndex]
				channel := live.channels[i]
				fav := live.state.isFavorite(channel.Name)
				row := ui.Row(c).Key(fmt.Sprintf("live-channel-%d", i)).FillWidth().Padding(10, 14).Gap(10).
					AlignItems(ui.Center)
				targetBg := p.surface
				if row.Hovered() {
					targetBg = p.surfaceHover
					if !p.dark {
						targetBg = ui.RGB(255, 255, 255)
					}
				}
				row.Radius(16).Background(row.AnimateColor("ch-bg", targetBg, animColor))
				if !p.dark || p.highContrast || p.reduceTransparency {
					row.Border(1, p.border)
				}
				applyPressPose(row)
				row.Children(func() {
					mainArea := ui.Row(c).Grow(1).MinWidth(0).Gap(12).AlignItems(ui.Center).
						Cursor(ui.CursorPointer).Label("播放 " + channel.Name)
					mainArea.Children(func() {
						a.renderChannelLogo(c, channel, 36, p.dark)
						ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
							ui.Text(c, channel.Name).Bold().SingleLine()
							meta := channel.Group
							if len(channel.Streams) > 1 {
								streamNote := fmt.Sprintf("%d 条线路", len(channel.Streams))
								if meta != "" {
									meta += " · " + streamNote
								} else {
									meta = streamNote
								}
							}
							if cur, _ := a.channelEPGCurrent(channel); cur != nil {
								nowPlaying := "正在播出：" + cur.Title
								if meta != "" {
									meta = nowPlaying + " · " + meta
								} else {
									meta = nowPlaying
								}
							}
							if meta != "" {
								ui.Text(c, meta).FontSize(12).TextColor(p.textMuted).SingleLine()
							}
						})
					})
					if mainArea.Clicked() {
						a.playLiveChannel(i, 0)
					}
					starText, starLabel := "☆", "收藏 "+channel.Name
					if fav {
						starText, starLabel = "★", "取消收藏 "+channel.Name
					}
					starBtn := ui.Button(c, starText).Label(starLabel).Tooltip(starLabel).Padding(6, 10).Radius(12)
					if fav {
						starBtn.Background(p.surfaceSelected).TextColor(p.accent)
					} else {
						starBtn.Background(ui.Transparent).TextColor(p.textMuted)
					}
					applyPressPose(starBtn)
					if starBtn.Clicked() {
						a.toggleLiveChannelFavorite(channel)
					}
				})
			}).Grow(1).MinWidth(0).FillHeight().Gap(8)
			_ = list
		})
	})
}

func (a *app) liveDialog(c *ui.Context) {
	live := &a.live
	if live.importOpen && !live.importWasOpen {
		if live.input == "" && live.url != "" {
			live.input = live.url
		}
		live.importWasOpen = true
	}
	p := paletteFor(c)
	_, winH := c.Size()
	ui.DialogBase(c, &live.importOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(p.dialogScrim).Transition(transDialogScrim)
		panel.Width(520).MaxWidth(520).MaxHeight(max(float32(240), winH-32)).Padding(24).Gap(12).Radius(26).TextColor(p.text).Transition(transDialogPanel)
		applyPane(panel, p, p.dialogMat)
		title := "添加直播源"
		hint := "粘贴直播频道表地址（支持 M3U、TXT 或含直播的影视配置）。"
		if live.url != "" {
			title = "更换直播源"
			hint = "粘贴新地址将替换当前直播源，你的频道收藏都会保留。"
		}
		ui.Text(c, title).FontSize(24).Bold()
		ui.Scroll(c).FillWidth().MaxHeight(max(float32(120), winH-176)).Gap(12).Children(func() {
			ui.Text(c, hint).TextColor(p.textMuted)
			if ui.TextInput(c, &live.input).Label("直播地址").Placeholder("https://example.com/channels.m3u").FillWidth().AutoFocus().Submitted() {
				if !a.running(jobLive) {
					a.loadLiveImportFromDialog(live.input)
				}
			}
			if live.importErr != "" {
				ui.Text(c, live.importErr).FontSize(13).TextColor(p.warning)
			}
		})
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if glassButton(c, "取消").Clicked() {
				live.importOpen = false
			}
			submitLabel := "导入"
			if a.running(jobLive) {
				submitLabel = "导入中…"
			}
			if primaryButton(c, submitLabel).Disabled(a.running(jobLive)).Clicked() {
				a.loadLiveImportFromDialog(live.input)
			}
		})
	})
	if !live.importOpen && live.importWasOpen {
		live.importWasOpen = false
		live.importErr = ""
		if live.importJobGen != 0 {
			a.cancelJobIfGen(jobLive, live.importJobGen)
			live.importJobGen = 0
		}
	}
}

// advanceLive tries each alternative once when the player fails or never starts.
func (a *app) advanceLive(p mediaPlayer) {
	if !a.playingLive || a.live.catchupProgram != nil || p == nil || a.live.playingChannel < 0 || a.live.playingChannel >= len(a.live.channels) {
		return
	}
	if p.Err() == nil && (p.Ready() || time.Since(a.live.started) < 15*time.Second) {
		return
	}
	count := len(a.live.channels[a.live.playingChannel].Streams)
	if a.live.attempts >= count {
		a.status = fmt.Sprintf("该频道的 %d 条线路均未能启动，请稍后重试", count)
		return
	}
	a.startLiveChannel(a.live.playingChannel, (a.live.playingStream+1)%count)
}
