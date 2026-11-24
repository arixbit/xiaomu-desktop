package main

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

type liveFilterCache struct {
	ptr     *iptv.Channel
	length  int
	group   string
	query   string
	indices []int
	valid   bool
}

type liveBrowser struct {
	url, input, query, group string
	importOpen, restore      bool
	sources                  []iptv.Source
	cachedChannels           map[string][]iptv.Channel
	staleChannels            map[string]bool
	selectedSource           int
	channels                 []iptv.Channel
	groupsCache              []string
	groupsCachePtr           *iptv.Channel
	groupsCacheLen           int
	groupsCacheValid         bool
	filteredAllCache         liveFilterCache
	filteredQueryCache       liveFilterCache
	channelList              ui.ListState
	panelChannelList         ui.ListState
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
	if cache.valid && cache.ptr == ptr && cache.length == n && cache.group == l.group && cache.query == query {
		return cache.indices
	}
	indices := make([]int, 0, n)
	for i, channel := range l.channels {
		if l.group != "" && channel.Group != l.group {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(channel.Name), query) {
			continue
		}
		indices = append(indices, i)
	}
	*cache = liveFilterCache{
		ptr:     ptr,
		length:  n,
		group:   l.group,
		query:   query,
		indices: indices,
		valid:   true,
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

func (a *app) applyLiveChannels(index int, channels []iptv.Channel) {
	a.live.channels, a.live.selectedSource = channels, index
	a.live.group, a.live.query = "", ""
	a.live.channelList, a.live.panelChannelList = ui.ListState{}, ui.ListState{}
}

func (a *app) setLiveSources(sources []iptv.Source) {
	a.live.sources = sources
	a.live.channels = nil
	a.live.selectedSource = -1
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
	raw = strings.TrimSpace(raw)
	if raw == "" {
		a.status = "请先粘贴直播列表地址"
		return
	}
	a.run(jobLive, "导入直播", func(ctx context.Context) (jobResult, error) {
		doc, err := config.Load(ctx, raw, "", nil)
		if err != nil {
			return jobResult{}, err
		}
		if len(doc.Lives) == 0 {
			return jobResult{}, fmt.Errorf("此配置没有 IPTV 直播，请导入 M3U、TXT 或含 lives 的配置")
		}
		return jobResult{apply: func() { a.applyLiveImport(raw, doc.Lives) }}, nil
	}, nil)
}

// applyLiveImport makes a channel list the live source and shows it. A list
// imported on its own takes priority over the lives of the film library.
func (a *app) applyLiveImport(raw string, sources []iptv.Source) {
	prevURL := a.live.url
	if prevURL != raw {
		a.live.cachedChannels = nil
		a.live.staleChannels = nil
	} else if len(a.live.cachedChannels) > 0 {
		a.live.staleChannels = make(map[string]bool, len(a.live.cachedChannels))
		for k := range a.live.cachedChannels {
			a.live.staleChannels[k] = true
		}
	}
	a.setLiveSources(sources)
	a.live.url, a.live.input, a.live.restore, a.live.importOpen = raw, raw, false, false
	a.switchTab(routeLive)
	if a.persist {
		if err := a.saveLiveSource(raw); err != nil {
			a.status = "保存直播地址失败：" + err.Error()
		}
	}
}

// removeLive forgets the separately imported list; the lives of the film
// library, if any, show again.
func (a *app) removeLive() {
	a.cancelJob(jobLive)
	a.live.url, a.live.input, a.live.restore = "", "", false
	a.live.cachedChannels = nil
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
	a.status = "已移除直播源"
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
		a.applyLiveChannels(index, cloneLiveChannels(cached))
		return
	}
	expectedLiveURL := a.live.url
	expectedConfigURL := a.browser.configURL
	var fail func(error)
	if stale && len(cached) > 0 {
		fallback := cloneLiveChannels(cached)
		fail = func(err error) {
			if a.live.url == expectedLiveURL && a.browser.configURL == expectedConfigURL &&
				index < len(a.live.sources) && liveSourceCacheKey(a.live.sources[index]) == key {
				delete(a.live.staleChannels, key)
				a.applyLiveChannels(index, fallback)
			}
			a.status = jobError("加载频道", err)
		}
	}
	a.run(jobLive, "加载频道", func(ctx context.Context) (jobResult, error) {
		channels, err := config.LiveChannels(ctx, source, nil)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if a.live.url != expectedLiveURL || a.browser.configURL != expectedConfigURL ||
				index >= len(a.live.sources) || liveSourceCacheKey(a.live.sources[index]) != key {
				return
			}
			a.applyLiveChannels(index, channels)
			if key != "" {
				delete(a.live.staleChannels, key)
				a.setActiveLiveChannelCache(key, channels)
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
		u, err := url.Parse(stream.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			a.status = "此频道使用非 HTTP 直播协议；当前支持 HTTP/HTTPS 视频流，组播频道请使用 udpxy 转换后的地址"
		} else {
			a.live.started = time.Now()
			if a.playURL(stream.URL, stream.Headers) {
				a.playingLive = true
				if a.player != nil {
					a.player.SetRate(1)
				}
				a.syncNowPlaying()
				return
			}
		}
		streamIndex = (streamIndex + 1) % len(channel.Streams)
	}

}

// listRow is a row of a plain list: a tab of the sidebar, a channel group.
func listRow(c *ui.Context, label string, selected bool) ui.Element {
	row := ui.Row(c).Key(label).FillWidth().Height(38).Padding(0, 12).Radius(11).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	if selected {
		row.Background(ui.RGBA(255, 255, 255, 0.16))
	}
	row.Children(func() {
		text := ui.Text(c, label).SingleLine()
		if !selected {
			text.TextColor(textMuted)
		}
	})
	return row
}

func (a *app) liveView(c *ui.Context) {
	live := &a.live
	if len(live.sources) == 0 {
		ui.Column(c).Fill().Center().Gap(12).Children(func() {
			ui.Text(c, "添加直播源，开始看电视").FontSize(24).Bold()
			ui.Text(c, "支持 M3U、TXT 频道列表，以及 TVBox 配置中的直播。").TextColor(textMuted)
			if primaryButton(c, "添加直播源").Clicked() {
				live.importOpen = true
			}
		})
		return
	}
	ui.Column(c).Fill().Gap(14).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Wrap().Gap(8).Children(func() {
				for i, source := range live.sources {
					if chip(c, source.Name, i == live.selectedSource).Key(fmt.Sprintf("live-source-%d", i)).Clicked() {
						a.loadLiveSource(i)
					}
				}
			})
			if ui.SearchField(c, &live.query).Label("频道搜索").Placeholder("搜索频道").Width(240).Radius(20).Material(glassControl).Changed() {
				live.channelList = ui.ListState{}
			}
		})
		if len(live.channels) == 0 {
			if a.running(jobLive) {
				emptyState(c, "正在加载频道…", "频道读取后会显示在这里。")
			} else {
				emptyState(c, "这个直播来源没有频道", "换一个直播来源，或到片源页更换直播源。")
			}
			return
		}
		ui.Row(c).Grow(1).FillWidth().MinHeight(0).Gap(16).Children(func() {
			ui.Scroll(c).Width(168).FillHeight().Gap(2).Children(func() {
				if listRow(c, "全部频道", live.group == "").Clicked() {
					if live.group != "" {
						live.group = ""
						live.channelList, live.panelChannelList = ui.ListState{}, ui.ListState{}
					}
				}
				for _, group := range live.groups() {
					if listRow(c, group, live.group == group).Clicked() {
						if live.group != group {
							live.group = group
							live.channelList, live.panelChannelList = ui.ListState{}, ui.ListState{}
						}
					}
				}
			})
			indices := live.filteredChannels(true)
			live.channelList.Key = func(row int) any { return indices[row] }
			live.channelList.Label = func(row int) string { return live.channels[indices[row]].Name }
			ui.List(c, &live.channelList, len(indices), func(rowIndex int) {
				i := indices[rowIndex]
				channel := live.channels[i]
				row := ui.Row(c).Key(fmt.Sprintf("live-channel-%d", i)).FillWidth().Padding(12, 16).Gap(12).Radius(16).
					AlignItems(ui.Center).Background(surface).Cursor(ui.CursorPointer).Label("播放 " + channel.Name)
				row.Children(func() {
					ui.Text(c, channel.Name).Bold().SingleLine()
					ui.Text(c, fmt.Sprintf("%s · %d 条线路", channel.Group, len(channel.Streams))).FontSize(12).TextColor(textMuted).SingleLine()
				})
				if row.Clicked() {
					a.playLiveChannel(i, 0)
				}
			}).Grow(1).MinWidth(0).FillHeight().Gap(8).Children(func() {
				if len(indices) == 0 {
					ui.Text(c, "没有找到频道，可换个关键词或重新选择分组。").TextColor(textMuted)
				}
			})
		})
	})
}

func (a *app) liveDialog(c *ui.Context) {
	live := &a.live
	ui.DialogBase(c, &live.importOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(2, 8, 22, 0.67))
		panel.Width(520).MaxWidth(520).Padding(26).Gap(14).Radius(26).Material(glass.Glass{})
		ui.Text(c, "添加直播源").FontSize(24).Bold()
		ui.Text(c, "粘贴 M3U、TXT 或 TVBox 直播配置地址。").TextColor(textMuted)
		ui.TextInput(c, &live.input).Label("直播地址").Placeholder("https://example.com/channels.m3u").FillWidth()
		if a.status != "" {
			ui.Text(c, a.status).TextColor(textMuted)
		}
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if glassButton(c, "取消").Clicked() {
				live.importOpen = false
			}
			if primaryButton(c, "导入直播").Clicked() {
				a.importLive(live.input)
			}
		})
	})
}

// advanceLive tries each alternative once when the player fails or never starts.
func (a *app) advanceLive(p *ui.VideoPlayer) {
	if !a.playingLive || p == nil || a.live.playingChannel < 0 || a.live.playingChannel >= len(a.live.channels) {
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
