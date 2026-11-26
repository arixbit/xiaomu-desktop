package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	mpvplayer "github.com/arixbit/mygo-player"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/netimage"
)

func TestLiveFavoritesPinningAndLastChannelResume(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "live_state.json")
	a := &app{nav: []routeKind{routeLive}}
	a.live.state.path = statePath
	a.live.sources = []iptv.Source{{Name: "测试直播"}}
	a.live.selectedSource = 0
	a.live.channels = []iptv.Channel{
		{Name: "频道甲", Group: "新闻", Streams: []iptv.Stream{{URL: "https://example.com/a.m3u8"}}},
		{Name: "频道乙", Group: "体育", Streams: []iptv.Stream{{URL: "https://example.com/b.m3u8"}}},
	}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	t.Cleanup(a.close)

	tt := ui.NewTester(a.view, 1120, 760)

	// Star 频道乙: pins it ahead of 频道甲 and reveals 收藏频道 group.
	if err := tt.Click("收藏 频道乙"); err != nil {
		t.Fatalf("favorite button missing: %v (texts=%v)", err, tt.Texts())
	}
	ordered := a.live.filteredChannels(true)
	if len(ordered) != 2 || ordered[0] != 1 {
		t.Fatalf("favorited channel not pinned to top: order=%v", ordered)
	}
	if err := tt.Click("收藏频道"); err != nil {
		t.Fatalf("favorites sidebar group missing: %v (texts=%v)", err, tt.Texts())
	}
	if !tt.HasText("频道乙") || tt.HasText("频道甲") {
		t.Fatalf("favorites filter mismatch: %v", tt.Texts())
	}

	// Play 频道乙, leave player, verify quick-resume bar appears and resumes playback.
	if err := tt.Click("播放 频道乙"); err != nil {
		t.Fatalf("play favorite failed: %v", err)
	}
	if !a.playingLive || a.live.playingChannel != 1 {
		t.Fatalf("did not play 频道乙: playing=%v ch=%d", a.playingLive, a.live.playingChannel)
	}
	a.leavePlayer()
	tt.Frame()
	if !tt.HasText("上次观看 · 频道乙") {
		t.Fatalf("last-channel resume banner missing: %v", tt.Texts())
	}
	if err := tt.Click("恢复上次频道"); err != nil {
		t.Fatalf("resume button missing: %v", err)
	}
	if !a.playingLive || a.live.playingChannel != 1 {
		t.Fatalf("resume did not restart 频道乙: playing=%v ch=%d", a.playingLive, a.live.playingChannel)
	}

	// Reload persisted state into a fresh app instance and verify favorites + last channel survive.
	st, err := readLiveState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Favorites) != 1 || st.Last == nil || st.Last.ChannelName != "频道乙" {
		t.Fatalf("persisted live state mismatch: %+v", st)
	}
}

func TestLiveLogoEPGScheduleAndCatchupReplay(t *testing.T) {
	logoPNG := fixPNG(t, 32, 32)
	var playedURLs []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels.m3u":
			fmt.Fprintf(w, `#EXTM3U x-tvg-url="%s/epg.xml" catchup="append" catchup-days="7"
#EXTINF:-1 tvg-id="cctv1" tvg-logo="%s/logo.png" catchup-source="?playseek=${(b)yyyyMMddHHmmss}-${(e)yyyyMMddHHmmss}" group-title="央视",CCTV-1 综合
%s/live/cctv1.m3u8
`, srv.URL, srv.URL, srv.URL)
		case "/logo.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(logoPNG)
		case "/epg.xml":
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="cctv1"><display-name>CCTV-1</display-name></channel>
  <programme start="20261124180000 +0800" stop="20261124190000 +0800" channel="cctv1"><title>晚间新闻</title></programme>
  <programme start="20261124190000 +0800" stop="20261124193000 +0800" channel="cctv1"><title>新闻联播</title></programme>
  <programme start="20261124193000 +0800" stop="20261124200000 +0800" channel="cctv1"><title>焦点访谈</title></programme>
</tv>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	defer netimage.Forget(srv.URL + "/logo.png")

	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 24, 19, 15, 0, 0, loc)

	a := &app{nav: []routeKind{routeLive}}
	a.live.epgClient = srv.Client()
	a.live.nowFn = func() time.Time { return now }
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		playedURLs = append(playedURLs, raw)
		return ui.NewVideoPlayer(&timedVideo{})
	}
	t.Cleanup(a.close)

	// Import M3U directly (synchronous in headless test when dispatch == nil).
	a.importLive(srv.URL + "/channels.m3u")
	if a.activeEPGURL() != srv.URL+"/epg.xml" {
		t.Fatalf("x-tvg-url not captured: %q", a.activeEPGURL())
	}
	a.loadLiveSource(0)

	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("正在播出：新闻联播") {
		t.Fatalf("live channel list did not display current EPG program: %v", tt.Texts())
	}

	// Play channel and verify player header shows current EPG program.
	if err := tt.Click("播放 CCTV-1 综合"); err != nil {
		t.Fatalf("play channel failed: %v", err)
	}
	if !tt.HasText("直播 · CCTV-1 综合 · 新闻联播") {
		t.Fatalf("player header missing current EPG title: %v", tt.Texts())
	}

	// Open EPG tab inside player panel and trigger catchup replay on past program 晚间新闻.
	a.panelOpen = true
	a.live.panelTab = 1
	tt.Frame()
	if !tt.HasText("晚间新闻") || !tt.HasText("播出中") || !tt.HasText("焦点访谈") {
		t.Fatalf("EPG schedule missing entries: %v", tt.Texts())
	}
	if err := tt.Click("回看 晚间新闻"); err != nil {
		t.Fatalf("catchup button missing: %v (texts=%v)", err, tt.Texts())
	}
	if a.live.catchupProgram == nil || a.live.catchupProgram.Title != "晚间新闻" {
		t.Fatalf("catchup program not active: %+v", a.live.catchupProgram)
	}
	if !tt.HasText("回看 · CCTV-1 综合 · 晚间新闻") || !tt.HasText("返回直播") {
		t.Fatalf("catchup UI state mismatch: %v", tt.Texts())
	}
	if len(playedURLs) < 2 || !strings.Contains(playedURLs[len(playedURLs)-1], "playseek%3D") {
		t.Fatalf("catchup proxy URL missing playseek param: %v", playedURLs)
	}

	// Clicking 返回直播 returns to live stream and clears catchup state.
	if err := tt.Click("返回直播"); err != nil {
		t.Fatalf("return to live failed: %v", err)
	}
	if a.live.catchupProgram != nil || !tt.HasText("直播 · CCTV-1 综合 · 新闻联播") {
		t.Fatalf("did not return to live: catchup=%+v texts=%v", a.live.catchupProgram, tt.Texts())
	}
}

func TestLiveDIYPTemplateEPGLoadsPerChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/diyp" && r.URL.Query().Get("ch") == "CCTV-1" {
			fmt.Fprint(w, `{"channel_name":"CCTV-1","date":"2026-11-24","epg_data":[{"start":"19:00","end":"19:30","title":"新闻联播"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 24, 19, 10, 0, 0, time.Local)
	a := &app{nav: []routeKind{routeLive}}
	a.live.epgClient = srv.Client()
	a.live.nowFn = func() time.Time { return now }
	a.live.sources = []iptv.Source{{Name: "TVBox直播", EPG: srv.URL + "/diyp?ch={name}&date={date}"}}
	a.live.selectedSource = 0
	a.live.channels = []iptv.Channel{{Name: "CCTV-1", Group: "央视", Streams: []iptv.Stream{{URL: "https://example.com/c1.m3u8"}}}}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	t.Cleanup(a.close)

	a.playLiveChannel(0, 0)
	cur, _ := a.channelEPGCurrent(a.live.channels[0])
	if cur == nil || cur.Title != "新闻联播" {
		t.Fatalf("DIYP per-channel EPG did not load: cur=%+v err=%q", cur, a.live.epgErr)
	}
}

func TestLiveDIYPEPGFastChannelSwitchDoesNotDeadlock(t *testing.T) {
	firstAStarted := make(chan struct{})
	firstCStarted := make(chan struct{})
	var reqA, reqC atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("ch") {
		case "CCTV-1":
			if reqA.Add(1) == 1 {
				close(firstAStarted)
				<-r.Context().Done()
				return
			}
			fmt.Fprint(w, `{"channel_name":"CCTV-1","date":"2026-11-24","epg_data":[{"start":"19:00","end":"19:30","title":"新闻联播"}]}`)
		case "CCTV-2":
			fmt.Fprint(w, `{"channel_name":"CCTV-2","date":"2026-11-24","epg_data":[{"start":"19:00","end":"19:30","title":"经济信息联播"}]}`)
		case "CCTV-3":
			if reqC.Add(1) == 1 {
				close(firstCStarted)
				<-r.Context().Done()
				return
			}
			fmt.Fprint(w, `{"channel_name":"CCTV-3","date":"2026-11-24","epg_data":[{"start":"19:00","end":"19:30","title":"综艺盛典"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	updates := make(chan func(), 8)
	drain := func(n int) {
		t.Helper()
		for range n {
			select {
			case fn := <-updates:
				fn()
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for async EPG job")
			}
		}
	}

	now := time.Date(2026, 11, 24, 19, 10, 0, 0, time.Local)
	a := &app{
		nav:      []routeKind{routeLive},
		dispatch: func(fn func()) { updates <- fn },
	}
	a.live.epgClient = srv.Client()
	a.live.nowFn = func() time.Time { return now }
	a.live.sources = []iptv.Source{{Name: "TVBox直播", EPG: srv.URL + "/diyp?ch={name}&date={date}"}}
	a.live.selectedSource = 0
	a.live.channels = []iptv.Channel{
		{Name: "CCTV-1", Group: "央视", Streams: []iptv.Stream{{URL: "https://example.com/c1.m3u8"}}},
		{Name: "CCTV-2", Group: "央视", Streams: []iptv.Stream{{URL: "https://example.com/c2.m3u8"}}},
		{Name: "CCTV-3", Group: "央视", Streams: []iptv.Stream{{URL: "https://example.com/c3.m3u8"}}},
	}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	t.Cleanup(a.close)

	// Start CCTV-1, wait until its HTTP request is in flight, then quickly switch to CCTV-2.
	a.playLiveChannel(0, 0)
	select {
	case <-firstAStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first CCTV-1 EPG request did not start")
	}
	a.playLiveChannel(1, 0)
	drain(2)

	if cur, _ := a.channelEPGCurrent(a.live.channels[1]); cur == nil || cur.Title != "经济信息联播" {
		t.Fatalf("CCTV-2 EPG did not load after superseding CCTV-1: cur=%+v err=%q", cur, a.live.epgErr)
	}
	if cur, _ := a.channelEPGCurrent(a.live.channels[0]); cur != nil {
		t.Fatalf("superseded CCTV-1 unexpectedly populated cache: %+v", cur)
	}

	// Switching back to CCTV-1 must not be stuck in a stale loading flag.
	a.playLiveChannel(0, 0)
	drain(1)
	if cur, _ := a.channelEPGCurrent(a.live.channels[0]); cur == nil || cur.Title != "新闻联播" {
		t.Fatalf("CCTV-1 EPG deadlocked after fast channel switch: cur=%+v reqA=%d err=%q", cur, reqA.Load(), a.live.epgErr)
	}
	if reqA.Load() != 2 {
		t.Fatalf("CCTV-1 request count = %d, want 2", reqA.Load())
	}

	// Explicitly canceling all jobs while a DIYP request is in flight must also allow retrying.
	a.playLiveChannel(2, 0)
	select {
	case <-firstCStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first CCTV-3 EPG request did not start")
	}
	a.cancelJobs()
	drain(1)
	a.playLiveChannel(2, 0)
	drain(1)
	if cur, _ := a.channelEPGCurrent(a.live.channels[2]); cur == nil || cur.Title != "综艺盛典" {
		t.Fatalf("CCTV-3 EPG deadlocked after cancelJobs: cur=%+v reqC=%d err=%q", cur, reqC.Load(), a.live.epgErr)
	}
}

type stubStateMediaPlayer struct {
	mediaPlayer
	ready bool
	err   error
}

func (s *stubStateMediaPlayer) Ready() bool { return s.ready }
func (s *stubStateMediaPlayer) Err() error  { return s.err }

func TestLiveCatchupDoesNotJumpBackToLiveAndSupportsSeekSpeedAndMiniBar(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 24, 19, 15, 0, 0, loc)
	pastProg := iptv.Program{
		Title: "晚间新闻",
		Start: time.Date(2026, 11, 24, 18, 0, 0, 0, loc),
		End:   time.Date(2026, 11, 24, 19, 0, 0, 0, loc),
	}
	ch := iptv.Channel{
		Name:          "CCTV-1",
		Group:         "央视",
		Catchup:       "append",
		CatchupSource: "?playseek=${(b)yyyyMMddHHmmss}-${(e)yyyyMMddHHmmss}",
		CatchupDays:   7,
		Streams: []iptv.Stream{
			{URL: "https://example.com/live/s1.m3u8"},
			{URL: "https://example.com/live/s2.m3u8"},
		},
	}

	// 1. Failing catchup stream must stay in catchup mode and never call startLiveChannel.
	t.Run("failure does not jump back to live", func(t *testing.T) {
		opened := 0
		a := &app{nav: []routeKind{routeLive}}
		a.live.nowFn = func() time.Time { return now }
		a.live.channels = []iptv.Channel{ch}
		a.openMPV = func(string, mpvplayer.OpenOptions) (mediaPlayer, error) {
			opened++
			vp, err := ui.NewVideoPlayer(&stillVideo{})
			if err != nil {
				return nil, err
			}
			return &stubStateMediaPlayer{
				mediaPlayer: wrapSystemPlayer(vp),
				ready:       true,
				err:         errors.New("fixture stream failed"),
			}, nil
		}
		t.Cleanup(a.close)

		if !a.playCatchupProgram(0, 0, pastProg) {
			t.Fatalf("playCatchupProgram failed: status=%q", a.status)
		}
		tt := ui.NewTester(a.view, 1120, 760)
		for range 5 {
			tt.Frame()
		}
		if opened != 1 || a.live.catchupProgram == nil {
			t.Fatalf("failed catchup jumped back to live: opened=%d catchup=%+v", opened, a.live.catchupProgram)
		}
		if !tt.HasText("播放失败：fixture stream failed") {
			t.Fatalf("failed catchup did not show error message: %v", tt.Texts())
		}
	})

	// 2. Slow (>15s unready) catchup stream must not time out into live playback.
	t.Run("slow load over 15s does not jump back to live", func(t *testing.T) {
		opened := 0
		a := &app{nav: []routeKind{routeLive}}
		a.live.nowFn = func() time.Time { return now }
		a.live.channels = []iptv.Channel{ch}
		a.openMPV = func(string, mpvplayer.OpenOptions) (mediaPlayer, error) {
			opened++
			vp, err := ui.NewVideoPlayer(&stillVideo{})
			if err != nil {
				return nil, err
			}
			return &stubStateMediaPlayer{mediaPlayer: wrapSystemPlayer(vp), ready: false}, nil
		}
		t.Cleanup(a.close)

		if !a.playCatchupProgram(0, 0, pastProg) {
			t.Fatalf("playCatchupProgram failed: status=%q", a.status)
		}
		a.live.started = time.Now().Add(-20 * time.Second)
		tt := ui.NewTester(a.view, 1120, 760)
		tt.Frame()
		if opened != 1 || a.live.catchupProgram == nil {
			t.Fatalf("slow catchup jumped back to live: opened=%d catchup=%+v", opened, a.live.catchupProgram)
		}
		if !tt.HasText("正在加载视频…") {
			t.Fatalf("slow catchup did not keep loading indicator: %v", tt.Texts())
		}
	})

	// 3. Catchup supports keyboard Left/Right ±10s, long-press 2x speed, rate button, and mini slider.
	t.Run("seek long-press speed rate button and mini slider", func(t *testing.T) {
		a := &app{nav: []routeKind{routeLive}}
		a.live.nowFn = func() time.Time { return now }
		a.live.channels = []iptv.Channel{ch}
		a.open = func(string) (*ui.VideoPlayer, error) {
			return ui.NewVideoPlayer(&timedVideo{})
		}
		t.Cleanup(a.close)

		if !a.playCatchupProgram(0, 0, pastProg) {
			t.Fatalf("playCatchupProgram failed: status=%q", a.status)
		}
		p := a.player
		p.Pause()
		p.Seek(25 * time.Second)

		tt := ui.NewTester(a.view, 1120, 760)
		if _, ok := tt.Find("播放速度"); !ok {
			t.Fatalf("catchup main controls missing speed button: %v", tt.Texts())
		}

		// Left / short Right seek ±10s.
		tt.Key(0, ui.KeyLeft)
		if got := p.Position(); got != 15*time.Second {
			t.Fatalf("catchup KeyLeft position = %s, want 15s", got)
		}
		tt.Key(0, ui.KeyRight)
		if got := p.Position(); got != 25*time.Second {
			t.Fatalf("catchup short KeyRight position = %s, want 25s", got)
		}

		// Long-press Right activates 2x speed and releases cleanly.
		p.Play()
		tt.Frame()
		tt.KeyDown(ui.KeyRight)
		tt.Advance(350 * time.Millisecond)
		if p.Rate() != 2.0 || !tt.HasText("▶▶ 2× 倍速中") {
			t.Fatalf("catchup long-press Right did not boost to 2x: rate=%v texts=%v", p.Rate(), tt.Texts())
		}
		tt.KeyUp(ui.KeyRight)
		if p.Rate() != 1.0 {
			t.Fatalf("catchup long-press Right did not restore 1x: rate=%v", p.Rate())
		}

		// Mini-window bottom bar shows seek slider during catchup.
		p.Pause()
		a.enterMiniMode()
		ttMini := ui.NewTester(a.view, miniWindowWidth, miniWindowHeight)
		if _, ok := ttMini.Find("小窗播放进度"); !ok {
			t.Fatalf("catchup mini controls missing seek slider: %v", ttMini.Texts())
		}

		// Returning to real-time live hides mini seek slider and disables keyboard seeking again.
		a.playLiveChannel(0, 0)
		a.player.Pause()
		a.player.Seek(20 * time.Second)
		ttMini.Frame()
		if _, ok := ttMini.Find("小窗播放进度"); ok {
			t.Fatal("live mini controls unexpectedly showed seek slider")
		}
		ttMini.Key(0, ui.KeyLeft)
		ttMini.Key(0, ui.KeyRight)
		if got := a.player.Position(); got != 20*time.Second {
			t.Fatalf("real-time live playback unexpectedly seeked to %s", got)
		}
	})
}

func TestSingleChannelLiveSourceOpensEPGPanel(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 24, 19, 15, 0, 0, loc)
	epg, err := iptv.ParseXMLTV([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="cctv1"><display-name>CCTV-1</display-name></channel>
  <programme start="20261124180000 +0800" stop="20261124190000 +0800" channel="cctv1"><title>晚间新闻</title></programme>
  <programme start="20261124190000 +0800" stop="20261124193000 +0800" channel="cctv1"><title>新闻联播</title></programme>
</tv>`))
	if err != nil {
		t.Fatal(err)
	}

	a := &app{nav: []routeKind{routeLive}}
	a.live.nowFn = func() time.Time { return now }
	a.live.sources = []iptv.Source{{Name: "单频道源", EPG: "https://example.com/epg.xml"}}
	a.live.selectedSource = 0
	a.live.epgURL = "https://example.com/epg.xml"
	a.live.xmltvCache = map[string]*iptv.EPG{"https://example.com/epg.xml": epg}
	a.live.channels = []iptv.Channel{{
		Name:    "CCTV-1",
		TVGID:   "cctv1",
		Streams: []iptv.Stream{{URL: "https://example.com/c1.m3u8"}},
	}}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	t.Cleanup(a.close)

	a.playLiveChannel(0, 0)
	tt := ui.NewTester(a.view, 1120, 760)
	if a.panelOpen || tt.HasText("晚间新闻") || !tt.HasText("节目单") {
		t.Fatalf("single-channel live source with EPG should start collapsed with 节目单 toggle: open=%v texts=%v", a.panelOpen, tt.Texts())
	}
	if err := tt.Click("节目单"); err != nil {
		t.Fatalf("open EPG panel failed: %v", err)
	}
	if !a.panelOpen || !tt.HasText("节目单") || !tt.HasText("晚间新闻") || !tt.HasText("新闻联播") {
		t.Fatalf("single-channel live source with EPG did not open EPG panel: open=%v texts=%v", a.panelOpen, tt.Texts())
	}
	if err := tt.Click("节目单"); err != nil {
		t.Fatalf("collapse EPG panel failed: %v", err)
	}
	if a.panelOpen || tt.HasText("晚间新闻") || !tt.HasText("节目单") {
		t.Fatalf("EPG panel did not collapse cleanly: open=%v texts=%v", a.panelOpen, tt.Texts())
	}
	if err := tt.Click("节目单"); err != nil {
		t.Fatalf("reopen EPG panel failed: %v", err)
	}
	if !a.panelOpen || !tt.HasText("晚间新闻") {
		t.Fatalf("EPG panel did not reopen: open=%v texts=%v", a.panelOpen, tt.Texts())
	}

	// Single-channel live source without EPG must not show the panel toggle.
	a.live.epgURL = ""
	a.live.sources[0].EPG = ""
	tt.Frame()
	if _, ok := tt.Find("节目单"); ok {
		t.Fatalf("single-channel live without EPG unexpectedly showed EPG toggle: %v", tt.Texts())
	}
	if _, ok := tt.Find("频道"); ok {
		t.Fatalf("single-channel live without EPG unexpectedly showed channel toggle: %v", tt.Texts())
	}
}
