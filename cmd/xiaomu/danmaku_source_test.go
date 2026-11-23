package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
	"github.com/egoist/mygo/ui"
)

func TestDanmakuLoadsFromSourceWithoutServiceConfiguration(t *testing.T) {
	var requests atomic.Int32
	comments := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("media credentials leaked to the comment service")
		}
		fmt.Fprintf(w, `<i><d p="0,5,25,16777215">%s</d></i>`, r.URL.Path)
	}))
	defer comments.Close()
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	start := func(index int) {
		t.Helper()
		raw := fmt.Sprintf(`{"url":"https://example.com/video.mp4","danmaku":%q,"header":{"Cookie":"example-only","Authorization":"example-only"}}`, fmt.Sprintf("%s/episode-%d.xml", comments.URL, index+1))
		play, err := catalog.ReadPlayback(raw)
		if err != nil {
			t.Fatal(err)
		}
		play, err = (resolve.Resolver{}).Resolve(context.Background(), play)
		if err != nil {
			t.Fatal(err)
		}
		a.startEpisode(play, "线路", a.title.detail.Sources[0].Episodes[index], 0)
	}
	start(0)
	if requests.Load() != 0 {
		t.Fatal("disabled comments contacted the source")
	}
	tt := ui.NewTester(a.view, 1120, 760)
	for _, label := range []string{"弹幕", "弹幕开关"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if a.preferences.Danmaku.API != "" || len(a.danmaku.comments) != 1 || a.danmaku.comments[0].Text != "/episode-1.xml" || requests.Load() != 1 {
		t.Fatalf("source comments were not loaded: %+v, requests=%d", a.danmaku.comments, requests.Load())
	}
	if _, ok := tt.Find("弹幕服务地址"); ok {
		t.Fatal("normal comment playback asked for an API address")
	}
	start(1)
	if len(a.danmaku.comments) != 1 || a.danmaku.comments[0].Text != "/episode-2.xml" || requests.Load() != 2 {
		t.Fatalf("episode change retained the old comment track: %+v", a.danmaku.comments)
	}
	if a.danmaku.selected != nil {
		t.Fatal("a source track was treated as a manual API match")
	}
}

func TestDanmakuSourceFailureDoesNotBlockPlayback(t *testing.T) {
	comments := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "denied")
	}))
	defer comments.Close()
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferences.Danmaku.Enabled = true
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	a.startEpisode(catalog.Playback{URL: "https://example.com/video.mp4", Danmaku: comments.URL}, "线路", a.title.detail.Sources[0].Episodes[0], 0)
	if a.player == nil || !a.player.Playing() || a.playFailure != nil || a.status != "" || !strings.Contains(a.danmaku.message, "403") {
		t.Fatalf("comment failure interfered with media: status=%q, message=%q", a.status, a.danmaku.message)
	}
}
