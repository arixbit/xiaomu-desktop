package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mpvplayer "github.com/arixbit/mygo-player"
	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo/ui"
)

func TestImportM3UShowsLiveChannels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1 group-title=\"示例分组\",演示频道\nhttps://example.com/live.m3u8\n")
	}))
	defer srv.Close()
	a := &app{}
	a.live.url = "https://example.com/previous-list.m3u"
	defer a.close()
	a.loadConfig(srv.URL)
	tt := ui.NewTester(a.view, 960, 640)
	if !tt.HasText("演示频道") {
		t.Fatalf("导入 M3U 后没有频道：status=%s texts=%v", a.status, tt.Texts())
	}
}

func TestLiveImportPlaybackAndChannelControls(t *testing.T) {
	var mediaRequests []string
	var requestsMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channels.txt" {
			fmt.Fprint(w, "新闻,#genre#\n演示频道,./one.m3u8|User-Agent=FixtureUA&Referer=https%3A%2F%2Fexample.com%2F#./backup.m3u8|User-Agent=FixtureUA\n体育,#genre#\n体育频道,./two.m3u8|User-Agent=FixtureUA\n")
			return
		}
		if r.Header.Get("User-Agent") != "FixtureUA" {
			t.Errorf("missing UA: %v", r.Header)
		}
		if r.URL.Path == "/one.m3u8" && r.Header.Get("Referer") != "https://example.com/" {
			t.Errorf("missing Referer: %v", r.Header)
		}
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			requestsMu.Lock()
			mediaRequests = append(mediaRequests, r.URL.Path)
			requestsMu.Unlock()
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nsegment.ts\n")
		} else if r.URL.Path == "/segment.ts" {
			fmt.Fprint(w, "local media bytes")
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := &app{autoNext: true}
	defer a.close()
	// Live playback must not expose the previously selected VOD's episodes.
	a.title = &titlePage{name: "旧点播", failed: map[string]string{}}
	a.title.detail = &catalog.Detail{Name: "旧点播", Sources: []catalog.Source{{Name: "旧线路", Episodes: []catalog.Episode{{Name: "第1集"}}}}}
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		response, err := http.Get(raw)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.HasPrefix(line, "http://127.0.0.1:") {
				continue
			}
			segment, err := http.Get(line)
			if err != nil {
				return nil, err
			}
			data, _ := io.ReadAll(segment.Body)
			segment.Body.Close()
			if string(data) != "local media bytes" {
				t.Fatalf("segment=%q", data)
			}
		}
		return ui.NewVideoPlayer(&stillVideo{})
	}
	tt := ui.NewTester(a.view, 1120, 760)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q: %v; status=%s texts=%v", label, err, a.status, tt.Texts())
		}
	}
	click("直播")
	click("添加直播源")
	click("直播地址")
	tt.Type(srv.URL + "/channels.txt")
	click("导入")
	click("播放 演示频道")
	if !a.playingLive || !tt.HasText("直播 · 演示频道") || tt.HasText("下一集") || tt.HasText("播完自动下一集") {
		t.Fatalf("wrong live controls: %v", tt.Texts())
	}
	click("切换线路 1/2")
	click("下一频道")
	if !tt.HasText("直播 · 体育频道") {
		t.Fatal(tt.Texts())
	}
	click("上一频道")
	// The panel starts collapsed by default; opening it changes the channel without leaving the player.
	click("频道")
	click("切换到 体育频道")
	if !tt.HasText("直播 · 体育频道") {
		t.Fatal(tt.Texts())
	}
	click("切换到 演示频道")
	click("返回")
	if a.player != nil || a.route() != routeLive || !tt.HasText("体育频道") {
		t.Fatalf("did not return to the live page: %v", tt.Texts())
	}
	click("体育")
	if tt.HasText("演示频道") || !tt.HasText("体育频道") {
		t.Fatalf("group filter: %v", tt.Texts())
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	if strings.Join(mediaRequests, ",") != "/one.m3u8,/backup.m3u8,/two.m3u8,/one.m3u8,/two.m3u8,/one.m3u8" {
		t.Fatalf("media requests=%v", mediaRequests)
	}
}

type failedLiveVideo struct{}

func (*failedLiveVideo) Next() (ui.VideoFrame, error) {
	return ui.VideoFrame{}, fmt.Errorf("fixture stream failed")
}
func (*failedLiveVideo) Rewind() error { return nil }

func TestLiveFailureTriesAlternativesOnce(t *testing.T) {
	a := &app{}
	defer a.close()
	a.live.channels = []iptv.Channel{{Name: "演示", Streams: []iptv.Stream{{URL: "https://example.com/first.m3u8"}, {URL: "https://example.com/second.m3u8"}}}}
	opened := 0
	a.open = func(string) (*ui.VideoPlayer, error) { opened++; return ui.NewVideoPlayer(&failedLiveVideo{}) }
	a.playLiveChannel(0, 0)
	tt := ui.NewTester(a.view, 640, 420)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if opened == 2 && a.status != "正在播放" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if opened != 2 || a.live.playingStream != 1 || a.live.attempts != 2 {
		t.Fatalf("opened=%d state=%+v", opened, a.live)
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if opened != 2 {
		t.Fatal("alternatives looped indefinitely")
	}
}

func TestLiveChannelListsVirtualizeLargePlaylists(t *testing.T) {
	const total = 2000
	channels := make([]iptv.Channel, total)
	for i := range channels {
		channels[i] = iptv.Channel{
			Name:    fmt.Sprintf("频道 %04d", i),
			Group:   "全部测试",
			Streams: []iptv.Stream{{URL: fmt.Sprintf("https://example.com/live/%d.m3u8", i)}},
		}
	}
	a := &app{nav: []routeKind{routeLive}}
	defer a.close()
	a.live.sources = []iptv.Source{{Name: "测试直播"}}
	a.live.selectedSource = 0
	a.live.channels = channels
	a.open = func(string) (*ui.VideoPlayer, error) {
		return ui.NewVideoPlayer(&stillVideo{})
	}
	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("频道 0000") {
		t.Fatalf("first live channel missing: %v", tt.Texts())
	}
	if tt.HasText("频道 1999") {
		t.Fatal("live view built off-screen rows instead of virtualizing with ui.List")
	}
	if err := tt.Click("频道搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Type("1999")
	if !tt.HasText("频道 1999") || tt.HasText("频道 0000") {
		t.Fatalf("search did not filter virtualized live list: %v", tt.Texts())
	}
	if err := tt.Click("播放 频道 1999"); err != nil {
		t.Fatal(err)
	}
	if !a.playingLive || a.live.playingChannel != 1999 {
		t.Fatalf("clicking filtered channel did not start playback: playingLive=%v channel=%d", a.playingLive, a.live.playingChannel)
	}
	// Player panel starts collapsed; opening it virtualizes the channel list while the header shows the current channel.
	if !tt.HasText("直播 · 频道 1999") {
		t.Fatalf("player header did not show current channel: %v", tt.Texts())
	}
	if err := tt.Click("频道"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("切换到 频道 0000"); !ok {
		t.Fatalf("player channel panel did not render visible rows: %v", tt.Texts())
	}
	if _, ok := tt.Find("切换到 频道 1998"); ok {
		t.Fatal("player channel panel built off-screen rows instead of virtualizing with ui.List")
	}
	if _, ok := tt.Find("切换到 频道 1999"); ok {
		t.Fatal("player channel panel built tail row instead of virtualizing with ui.List")
	}
	if err := tt.Click("切换到 频道 0001"); err != nil {
		t.Fatal(err)
	}
	if a.live.playingChannel != 1 {
		t.Fatalf("clicking virtualized panel channel switched to %d", a.live.playingChannel)
	}
}

func TestLiveSupportsRTSPAndRTMPWithMPVHeaders(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"rtsp", "rtsp://example.com:554/live/ch1"},
		{"rtmp", "rtmp://example.com/live/ch2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotTarget string
			var gotOpts mpvplayer.OpenOptions
			a := &app{
				openMPV: func(target string, opts mpvplayer.OpenOptions) (mediaPlayer, error) {
					gotTarget, gotOpts = target, opts
					vp, err := ui.NewVideoPlayer(&stillVideo{})
					if err != nil {
						return nil, err
					}
					return wrapSystemPlayer(vp), nil
				},
			}
			t.Cleanup(a.close)
			a.live.channels = []iptv.Channel{{
				Name: "协议频道",
				Streams: []iptv.Stream{{
					URL: tc.url,
					Headers: map[string]string{
						"User-Agent": "LivePlayer/1.0",
						"Referer":    "https://example.com/live",
						"X-Token":    "abc123",
					},
				}},
			}}
			a.playLiveChannel(0, 0)
			if a.player == nil || !a.playingLive {
				t.Fatalf("%s live channel did not start: player=%v live=%v status=%q", tc.name, a.player != nil, a.playingLive, a.status)
			}
			if gotTarget != tc.url || gotOpts.UserAgent != "LivePlayer/1.0" || gotOpts.Referrer != "https://example.com/live" || gotOpts.Headers["X-Token"] != "abc123" {
				t.Fatalf("openMPV(%q, %+v) did not preserve stream target or headers", gotTarget, gotOpts)
			}
		})
	}

	// Unsupported protocols (e.g. udp://) are still rejected cleanly.
	a := &app{
		openMPV: func(string, mpvplayer.OpenOptions) (mediaPlayer, error) {
			t.Fatal("unsupported udp:// stream should not invoke openMPV")
			return nil, nil
		},
	}
	t.Cleanup(a.close)
	a.live.channels = []iptv.Channel{{
		Name:    "组播频道",
		Streams: []iptv.Stream{{URL: "udp://239.0.0.1:1234"}},
	}}
	a.playLiveChannel(0, 0)
	if a.player != nil || a.status == "" {
		t.Fatalf("unsupported protocol should fail with clear status: player=%v status=%q", a.player != nil, a.status)
	}
}

func TestLiveHeaderConditionalSearchAndTwoColumnCards(t *testing.T) {
	a := &app{nav: []routeKind{routeLive}}
	defer a.close()
	a.live.sources = []iptv.Source{
		{Name: "高清主源"},
		{Name: "卫视精选"},
	}
	a.live.selectedSource = 0
	a.live.channels = []iptv.Channel{
		{Name: "新闻综合", Group: "综合", Streams: []iptv.Stream{{URL: "https://example.com/1.m3u8"}, {URL: "https://example.com/1b.m3u8"}}},
		{Name: "体育高清", Group: "体育", Streams: []iptv.Stream{{URL: "https://example.com/2.m3u8"}}},
		{Name: "电影频道", Group: "影视", Streams: []iptv.Stream{{URL: "https://example.com/3.m3u8"}}},
	}

	tt := ui.NewTester(a.view, 1120, 760)
	if _, ok := tt.Find("频道搜索"); ok {
		t.Fatalf("channel search field should stay hidden when <=20 channels and empty query")
	}
	if !tt.HasText("共 3 个频道 · 2 个直播来源") || !tt.HasText("高清主源") || !tt.HasText("卫视精选") {
		t.Fatalf("header tabs/count missing: %v", tt.Texts())
	}
	if !tt.HasText("综合 · 2 条线路") || !tt.HasText("体育 · 1 条线路") {
		t.Fatalf("channel card meta missing: %v", tt.Texts())
	}
	if a.live.channelListCols != 2 {
		t.Fatalf("1120x760 live list cols = %d, want 2", a.live.channelListCols)
	}
	// Star button toggles favorite using icon button while keeping accessibility labels.
	if err := tt.Click("收藏 体育高清"); err != nil {
		t.Fatalf("star button missing: %v", err)
	}
	if _, ok := tt.Find("取消收藏 体育高清"); !ok {
		t.Fatalf("unstar label missing after toggling favorite")
	}
	if _, ok := tt.Find("收藏频道"); !ok {
		t.Fatalf("favorites group missing after starring a channel")
	}
	if err := tt.Click("取消收藏 体育高清"); err != nil {
		t.Fatalf("unstar click failed: %v", err)
	}
	if _, ok := tt.Find("收藏频道"); ok {
		t.Fatalf("favorites group should disappear when no favorites remain")
	}
	// Narrow window falls back to 1 column cleanly.
	ttNarrow := ui.NewTester(a.view, 860, 640)
	if !ttNarrow.HasText("新闻综合") || a.live.channelListCols != 1 {
		t.Fatalf("narrow window live list cols = %d, want 1", a.live.channelListCols)
	}
	// Adding >20 channels reveals the local channel search box.
	more := make([]iptv.Channel, 21)
	for i := range more {
		more[i] = iptv.Channel{
			Name:    fmt.Sprintf("频道 %02d", i),
			Group:   "测试组",
			Streams: []iptv.Stream{{URL: fmt.Sprintf("https://example.com/ch/%d.m3u8", i)}},
		}
	}
	a.live.channels = more
	tt.Frame()
	if _, ok := tt.Find("频道搜索"); !ok {
		t.Fatalf("channel search field did not appear for 21 channels: %v", tt.Texts())
	}
}
