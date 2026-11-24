package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/iptv"
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
