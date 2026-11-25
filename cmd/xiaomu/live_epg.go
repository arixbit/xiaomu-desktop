package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

const liveGroupFavorites = "\x00favorites"

type liveLastChannel struct {
	SourceName  string    `json:"source_name,omitempty"`
	SourceURL   string    `json:"source_url,omitempty"`
	ChannelName string    `json:"channel_name"`
	Group       string    `json:"group,omitempty"`
	Logo        string    `json:"logo,omitempty"`
	StreamIndex int       `json:"stream_index,omitempty"`
	Updated     time.Time `json:"updated"`
}

type livePersistedState struct {
	Favorites []string         `json:"favorites,omitempty"`
	Last      *liveLastChannel `json:"last,omitempty"`
}

type liveStateStore struct {
	path      string
	favorites []string
	favSet    map[string]int // key -> order index for stable pinned sorting
	version   uint64
	last      *liveLastChannel
}

func liveFavoriteKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (s *liveStateStore) rebuildIndex() {
	if len(s.favorites) == 0 {
		s.favSet = nil
		return
	}
	s.favSet = make(map[string]int, len(s.favorites))
	clean := s.favorites[:0]
	for _, raw := range s.favorites {
		k := liveFavoriteKey(raw)
		if k == "" {
			continue
		}
		if _, dup := s.favSet[k]; dup {
			continue
		}
		s.favSet[k] = len(clean)
		clean = append(clean, k)
	}
	s.favorites = clean
}

func (s *liveStateStore) isFavorite(name string) bool {
	if len(s.favSet) == 0 {
		return false
	}
	_, ok := s.favSet[liveFavoriteKey(name)]
	return ok
}

func (s *liveStateStore) favoriteRank(name string) (int, bool) {
	if len(s.favSet) == 0 {
		return 0, false
	}
	rank, ok := s.favSet[liveFavoriteKey(name)]
	return rank, ok
}

func (s *liveStateStore) toggleFavorite(name string) bool {
	k := liveFavoriteKey(name)
	if k == "" {
		return false
	}
	s.version++
	if _, exists := s.favSet[k]; exists {
		s.favorites = slices.DeleteFunc(s.favorites, func(v string) bool { return liveFavoriteKey(v) == k })
		s.rebuildIndex()
		return false
	}
	s.favorites = append([]string{k}, s.favorites...)
	s.rebuildIndex()
	return true
}

func readLiveState(path string) (livePersistedState, error) {
	var st livePersistedState
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return livePersistedState{}, fmt.Errorf("读取直播收藏与历史：%w", err)
	}
	return st, nil
}

func (a *app) restoreLiveState() {
	sourcePath, err := savedSourcePath()
	if err != nil {
		return
	}
	path := filepath.Join(filepath.Dir(sourcePath), "live_state.json")
	a.live.state.path = path
	st, err := readLiveState(path)
	if err != nil {
		a.status = err.Error()
		return
	}
	a.live.state.favorites = st.Favorites
	a.live.state.last = st.Last
	a.live.state.rebuildIndex()
	a.live.state.version++
}

func (a *app) saveLiveState() {
	s := &a.live.state
	if s.path == "" {
		return
	}
	payload := livePersistedState{
		Favorites: s.favorites,
		Last:      s.last,
	}
	if err := writeJSONFile(s.path, payload); err != nil {
		a.status = "保存直播状态失败：" + err.Error()
	}
}

func (a *app) toggleLiveChannelFavorite(ch iptv.Channel) {
	added := a.live.state.toggleFavorite(ch.Name)
	a.saveLiveState()
	if added {
		a.setNotice("已收藏并置顶频道：" + ch.Name)
		if a.playingLive {
			a.showPlayerOSD("已收藏频道")
		}
	} else {
		if len(a.live.state.favorites) == 0 && a.live.group == liveGroupFavorites {
			a.live.group = ""
			a.live.resetLists = true
		}
		a.setNotice("已取消收藏：" + ch.Name)
		if a.playingLive {
			a.showPlayerOSD("已取消收藏")
		}
	}
}

func (a *app) recordLastLiveChannel(index, streamIndex int) {
	if index < 0 || index >= len(a.live.channels) {
		return
	}
	ch := a.live.channels[index]
	var srcName, srcURL string
	if a.live.selectedSource >= 0 && a.live.selectedSource < len(a.live.sources) {
		srcName = a.live.sources[a.live.selectedSource].Name
		srcURL = a.live.sources[a.live.selectedSource].URL
	}
	a.live.state.last = &liveLastChannel{
		SourceName:  srcName,
		SourceURL:   srcURL,
		ChannelName: ch.Name,
		Group:       ch.Group,
		Logo:        ch.Logo,
		StreamIndex: streamIndex,
		Updated:     a.liveNow(),
	}
	a.saveLiveState()
}

func (a *app) findLiveChannelByName(name string) int {
	key := liveFavoriteKey(name)
	if key == "" {
		return -1
	}
	for i, ch := range a.live.channels {
		if liveFavoriteKey(ch.Name) == key {
			return i
		}
	}
	return -1
}

func liveSourceMatches(src iptv.Source, last *liveLastChannel) bool {
	if last == nil {
		return false
	}
	if last.SourceURL != "" {
		return src.URL == last.SourceURL
	}
	return src.URL == "" && last.SourceName != "" && src.Name == last.SourceName
}

func (a *app) canResumeLastLiveChannel() bool {
	last := a.live.state.last
	if last == nil || strings.TrimSpace(last.ChannelName) == "" {
		return false
	}
	if a.findLiveChannelByName(last.ChannelName) >= 0 {
		return true
	}
	currentLoaded := len(a.live.channels) > 0 && !a.running(jobLive)
	for i, src := range a.live.sources {
		// If this source is already loaded and the channel wasn't found above,
		// the channel has been removed from this playlist.
		if currentLoaded && i == a.live.selectedSource {
			continue
		}
		if liveSourceMatches(src, last) {
			return true
		}
	}
	return false
}

func (a *app) resumeLastLiveChannel() {
	last := a.live.state.last
	if last == nil || strings.TrimSpace(last.ChannelName) == "" {
		return
	}
	if idx := a.findLiveChannelByName(last.ChannelName); idx >= 0 {
		streamIdx := last.StreamIndex
		if streamIdx < 0 || streamIdx >= len(a.live.channels[idx].Streams) {
			streamIdx = 0
		}
		a.playLiveChannel(idx, streamIdx)
		return
	}
	// If the channel lives in another source, load that source and resume on arrival.
	for i, src := range a.live.sources {
		if liveSourceMatches(src, last) {
			a.live.pendingResume = last.ChannelName
			a.live.pendingResumeStream = last.StreamIndex
			a.loadLiveSource(i)
			return
		}
	}
	a.setNotice("当前直播源中未找到上次观看的频道：" + last.ChannelName)
}

func (a *app) tryPendingLiveResume() {
	name := a.live.pendingResume
	if name == "" {
		return
	}
	streamIdx := a.live.pendingResumeStream
	a.live.pendingResume = ""
	a.live.pendingResumeStream = 0
	if idx := a.findLiveChannelByName(name); idx >= 0 {
		if streamIdx < 0 || streamIdx >= len(a.live.channels[idx].Streams) {
			streamIdx = 0
		}
		a.playLiveChannel(idx, streamIdx)
	} else {
		a.setNotice("该直播源中已找不到频道：" + name)
	}
}

// --- Channel Logo Rendering ---

func channelMonogram(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return "TV"
	}
	// Keep short ASCII prefixes like "CCTV", "BTV", or first 2 CJK runes.
	var ascii []byte
	for i := 0; i < len(s) && len(ascii) < 4; i++ {
		ch := s[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			ascii = append(ascii, ch)
		} else if len(ascii) > 0 {
			break
		} else {
			break
		}
	}
	if len(ascii) >= 2 {
		return strings.ToUpper(string(ascii))
	}
	var b strings.Builder
	count := 0
	for len(s) > 0 && count < 2 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if r == ' ' || r == '-' || r == '_' {
			continue
		}
		b.WriteRune(r)
		count++
	}
	if b.Len() == 0 {
		return "TV"
	}
	return b.String()
}

func (a *app) renderChannelLogo(c *ui.Context, ch iptv.Channel, size float32, dark bool) {
	bg := ui.RGBA(255, 255, 255, 0.10)
	fg := ui.RGBA(240, 244, 255, 0.85)
	if !dark {
		bg = ui.RGBA(18, 24, 38, 0.08)
		fg = ui.RGBA(32, 42, 64, 0.78)
	}
	box := ui.Box(c).Size(size, size).Radius(8).Clip().Background(bg).Center()
	box.Children(func() {
		if src := a.poster(c, ch.Logo); src != nil {
			ui.Image(c, src).Fill().Fit(ui.Contain).Padding(3)
			return
		}
		fontSize := float32(11)
		if size < 30 {
			fontSize = 9
		}
		ui.Text(c, channelMonogram(ch.Name)).FontSize(fontSize).Bold().TextColor(fg).SingleLine()
	})
}

// --- EPG & Catchup ---

// isLiveStream reports whether playback is a real-time live broadcast
// rather than a seekable catchup replay or on-demand video.
func (a *app) isLiveStream() bool {
	if a.playingLive {
		return a.live.catchupProgram == nil
	}
	return a.playingDirectLive
}

func (a *app) liveNow() time.Time {
	if a.live.nowFn != nil {
		return a.live.nowFn()
	}
	return time.Now()
}

func (a *app) activeEPGURL() string {
	if a.live.epgURL != "" {
		return a.live.epgURL
	}
	if a.live.selectedSource >= 0 && a.live.selectedSource < len(a.live.sources) {
		return strings.TrimSpace(a.live.sources[a.live.selectedSource].EPG)
	}
	return ""
}

func (a *app) ensureEPGLoaded() {
	epgURL := a.activeEPGURL()
	if epgURL == "" || iptv.IsTemplateEPG(epgURL) {
		return
	}
	if a.live.xmltvCache != nil && a.live.xmltvCache[epgURL] != nil {
		return
	}
	var headers map[string]string
	if a.live.selectedSource >= 0 && a.live.selectedSource < len(a.live.sources) {
		headers = a.live.sources[a.live.selectedSource].Headers
	}
	client := a.live.epgClient
	a.live.diypLoadingKey = ""
	a.live.epgErr = ""
	a.run(jobEPG, "", func(ctx context.Context) (jobResult, error) {
		epg, err := iptv.FetchXMLTV(ctx, client, epgURL, headers)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if a.live.xmltvCache == nil {
				a.live.xmltvCache = map[string]*iptv.EPG{}
			}
			a.live.xmltvCache[epgURL] = epg
			a.live.epgErr = ""
		}}, nil
	}, func(err error) {
		a.live.epgErr = err.Error()
	})
}

func (a *app) ensureChannelDIYPEPG(ch iptv.Channel) {
	epgURL := a.activeEPGURL()
	if epgURL == "" || !iptv.IsTemplateEPG(epgURL) {
		return
	}
	now := a.liveNow()
	key := diypCacheKey(epgURL, ch, now)
	if a.live.diypCache != nil {
		if _, ok := a.live.diypCache[key]; ok {
			a.live.epgErr = ""
			return
		}
	}
	if a.live.diypLoadingKey == key && a.running(jobEPG) {
		return
	}
	a.live.diypLoadingKey = key
	a.live.epgErr = ""
	var headers map[string]string
	if a.live.selectedSource >= 0 && a.live.selectedSource < len(a.live.sources) {
		headers = a.live.sources[a.live.selectedSource].Headers
	}
	client := a.live.epgClient
	a.run(jobEPG, "", func(ctx context.Context) (jobResult, error) {
		progs, err := iptv.FetchDIYPChannel(ctx, client, epgURL, ch, now, headers)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if a.live.diypLoadingKey == key {
				a.live.diypLoadingKey = ""
			}
			if a.live.diypCache == nil {
				a.live.diypCache = map[string][]iptv.Program{}
			}
			a.live.diypCache[key] = progs
			a.live.epgErr = ""
		}}, nil
	}, func(err error) {
		if a.live.diypLoadingKey == key {
			a.live.diypLoadingKey = ""
		}
		a.live.epgErr = err.Error()
	})
}

func diypCacheKey(epgURL string, ch iptv.Channel, day time.Time) string {
	return epgURL + "\x00" + liveFavoriteKey(ch.Name) + "\x00" + day.Local().Format("2006-01-02")
}

func (a *app) channelEPGPrograms(ch iptv.Channel) []iptv.Program {
	epgURL := a.activeEPGURL()
	if epgURL == "" {
		return nil
	}
	now := a.liveNow()
	if iptv.IsTemplateEPG(epgURL) {
		if a.live.diypCache != nil {
			return a.live.diypCache[diypCacheKey(epgURL, ch, now)]
		}
		return nil
	}
	if a.live.xmltvCache != nil {
		if guide := a.live.xmltvCache[epgURL]; guide != nil {
			return guide.ProgramsForDay(ch, now)
		}
	}
	return nil
}

func (a *app) channelEPGCurrent(ch iptv.Channel) (current, next *iptv.Program) {
	epgURL := a.activeEPGURL()
	if epgURL == "" {
		return nil, nil
	}
	now := a.liveNow()
	if iptv.IsTemplateEPG(epgURL) {
		if a.live.diypCache != nil {
			return iptv.CurrentAndNextPrograms(a.live.diypCache[diypCacheKey(epgURL, ch, now)], now)
		}
		return nil, nil
	}
	if a.live.xmltvCache != nil {
		if guide := a.live.xmltvCache[epgURL]; guide != nil {
			return guide.CurrentAndNext(ch, now)
		}
	}
	return nil, nil
}

func (a *app) playCatchupProgram(channelIndex, streamIndex int, prog iptv.Program) bool {
	if channelIndex < 0 || channelIndex >= len(a.live.channels) {
		return false
	}
	ch := a.live.channels[channelIndex]
	if streamIndex < 0 || streamIndex >= len(ch.Streams) {
		streamIndex = 0
	}
	replayURL, ok := iptv.CatchupURL(ch, streamIndex, prog, a.liveNow())
	if !ok {
		a.setNotice("该频道暂不支持回看或节目已超出回看天数")
		return false
	}
	headers := ch.Streams[streamIndex].Headers
	a.live.playingChannel, a.live.playingStream = channelIndex, streamIndex
	a.live.started = time.Now()
	var started bool
	lower := strings.ToLower(replayURL)
	if (strings.HasPrefix(lower, "rtsp://") || strings.HasPrefix(lower, "rtmp://")) && (mpvBackendSupported() || a.openMPV != nil) {
		started = a.playDirectMPVURL(replayURL, headers)
	} else {
		started = a.playURL(replayURL, headers)
	}
	if !started {
		return false
	}
	a.playingLive = true
	cp := prog
	a.live.catchupProgram = &cp
	a.syncNowPlaying()
	a.showPlayerOSD("开始回看：" + prog.Title)
	return true
}

// panelChannelEPG renders the current channel's daily program schedule and
// clickable catchup replay buttons for past broadcasts.
func (a *app) panelChannelEPG(c *ui.Context) {
	live := &a.live
	if live.playingChannel < 0 || live.playingChannel >= len(live.channels) {
		return
	}
	ch := live.channels[live.playingChannel]
	epgURL := a.activeEPGURL()
	if epgURL == "" {
		ui.Text(c, "当前直播源未提供 EPG 节目单地址。").FontSize(13).TextColor(ui.RGBA(248, 249, 255, 0.72))
		return
	}
	progs := a.channelEPGPrograms(ch)
	if len(progs) == 0 {
		if a.running(jobEPG) {
			ui.Text(c, "正在加载节目单…").FontSize(13).TextColor(ui.RGBA(248, 249, 255, 0.75))
		} else if live.epgErr != "" {
			ui.Text(c, "节目单加载失败："+live.epgErr).FontSize(12).TextColor(ui.RGB(255, 166, 130))
		} else {
			ui.Text(c, "暂无该频道今日节目单").FontSize(13).TextColor(ui.RGBA(248, 249, 255, 0.72))
		}
		return
	}
	now := a.liveNow()
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Gap(6).Children(func() {
		for i, prog := range progs {
			isLive := prog.LiveAt(now)
			isCatchup := live.catchupProgram != nil && live.catchupProgram.Start.Equal(prog.Start) && live.catchupProgram.Title == prog.Title
			canReplay := prog.EndedAt(now) && iptv.SupportsCatchup(ch)
			if canReplay {
				_, canReplay = iptv.CatchupURL(ch, live.playingStream, prog, now)
			}
			tint := ui.RGBA(55, 50, 92, 0.32)
			if isLive || isCatchup {
				tint = ui.RGBA(255, 90, 64, 0.35)
			}
			ui.Row(c.Key(fmt.Sprintf("epg-%d", i))).FillWidth().Padding(8, 10).Gap(8).Radius(12).
				AlignItems(ui.Center).
				Material(glass.Glass{Style: glass.Clear, Tint: tint}).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, prog.TimeRange()).FontSize(11).TextColor(ui.RGBA(248, 249, 255, 0.72)).SingleLine()
						if isCatchup {
							ui.Text(c, "回看中").FontSize(10).Bold().TextColor(ui.RGB(255, 198, 130)).SingleLine()
						} else if isLive {
							ui.Text(c, "播出中").FontSize(10).Bold().TextColor(ui.RGB(142, 230, 178)).SingleLine()
						}
					})
					title := ui.Text(c, prog.Title).FontSize(13).SingleLine()
					if isLive || isCatchup {
						title.Bold()
					}
				})
				if canReplay {
					btn := ui.Button(c, "回看").Label("回看 "+prog.Title).Padding(4, 9).Radius(10).
						Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(255, 255, 255, 0.14), Interactive: true}).
						TextColor(playerText)
					applyPressPose(btn)
					if btn.Clicked() {
						a.playCatchupProgram(live.playingChannel, live.playingStream, prog)
					}
				}
			})
		}
	})
}

func (a *app) liveLastResumeBar(c *ui.Context) {
	if (a.live.group != "" && a.live.group != liveGroupFavorites) || strings.TrimSpace(a.live.query) != "" {
		return
	}
	if !a.canResumeLastLiveChannel() {
		return
	}
	last := a.live.state.last
	p := paletteFor(c)
	bar := ui.Row(c).Key("live-last-resume").FillWidth().Padding(10, 14).Gap(12).AlignItems(ui.Center)
	applySurface(bar, p, 16).Children(func() {
		a.renderChannelLogo(c, iptv.Channel{Name: last.ChannelName, Logo: last.Logo}, 32, p.dark)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, "上次观看 · "+last.ChannelName).Bold().SingleLine()
			sub := last.Group
			if idx := a.findLiveChannelByName(last.ChannelName); idx >= 0 {
				if cur, _ := a.channelEPGCurrent(a.live.channels[idx]); cur != nil {
					sub = "正在播出：" + cur.Title
				}
			}
			if sub != "" {
				ui.Text(c, sub).FontSize(12).TextColor(p.textMuted).SingleLine()
			}
		})
		if primaryButton(c, "继续观看").Label("恢复上次频道").Padding(6, 14).Clicked() {
			a.resumeLastLiveChannel()
		}
	})
}
