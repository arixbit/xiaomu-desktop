package main

import (
	"fmt"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
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
	a.loadConfig(srv.URL, "")
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
	a.browser.detail = &catalog.Detail{Name: "旧点播", Sources: []catalog.Source{{Name: "旧线路", Episodes: []catalog.Episode{{Name: "第1集"}}}}}
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
	click("导入直播")
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
	click("返回直播")
	if a.player != nil || !tt.HasText("IPTV 直播") || a.browser.detail.Name != "旧点播" {
		t.Fatalf("live import lost library state: %v", tt.Texts())
	}
	click("体育")
	if tt.HasText("演示频道") || !tt.HasText("体育频道") {
		t.Fatalf("group filter: %v", tt.Texts())
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	if strings.Join(mediaRequests, ",") != "/one.m3u8,/backup.m3u8,/two.m3u8,/one.m3u8" {
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
