package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The opt-in AVPlayer test needs macOS's main event loop. Ordinary tests keep
// using the headless UI tester and require no generated media or application.
func TestMain(m *testing.M) {
	if os.Getenv("XIAOMU_NATIVE_MEDIA_DIR") == "" {
		os.Exit(m.Run())
	}
	result := make(chan int, 1)
	mygo.App.WhenReady(func() {
		go func() {
			result <- m.Run()
			mygo.App.Quit()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(<-result)
}

// TestNativeIPTVHLS checks real AVPlayer decoding after an IPTV import and the
// loopback proxy. The generated stream.m3u8 and segments stay outside the repo.
func TestNativeIPTVHLS(t *testing.T) {
	dir := os.Getenv("XIAOMU_NATIVE_MEDIA_DIR")
	if dir == "" {
		t.Skip("set XIAOMU_NATIVE_MEDIA_DIR to a local generated HLS fixture")
	}
	var segments atomic.Int32
	files := http.FileServer(http.Dir(dir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channels.m3u" {
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1 group-title=\"本地测试\",测试画面\n#EXTVLCOPT:http-user-agent=NativeFixture\n#EXTVLCOPT:http-referrer=https://example.com/\n./stream.m3u8\n")
			return
		}
		if r.Header.Get("User-Agent") != "NativeFixture" || r.Header.Get("Referer") != "https://example.com/" {
			t.Errorf("AVPlayer proxy lost stream headers: %v", r.Header)
			http.Error(w, "missing fixture headers", http.StatusForbidden)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".ts") {
			segments.Add(1)
		}
		files.ServeHTTP(w, r)
	}))
	defer srv.Close()
	a := &app{}
	defer a.close()
	a.loadConfig(srv.URL + "/channels.m3u")
	if len(a.live.channels) != 1 {
		t.Fatalf("no live channel: %s", a.status)
	}
	a.playLiveChannel(0, 0)
	if a.player == nil {
		t.Fatalf("no native player: %s", a.status)
	}
	p := a.player
	p.SetMuted(true)
	tt := ui.NewTester(func(c *ui.Context) { ui.Video(c, p).Size(160, 90) }, 160, 90)
	deadline := time.Now().Add(15 * time.Second)
	var first [32]byte
	ready, changed := false, false
	for time.Now().Before(deadline) {
		tt.Frame()
		if err := p.Err(); err != nil {
			t.Fatal(err)
		}
		if p.Ready() {
			w, h := p.Size()
			if w != 160 || h != 90 {
				t.Fatalf("decoded frame size=%dx%d", w, h)
			}
			picture := tt.Image()
			hash := sha256.Sum256(picture.Pix)
			if !ready {
				first, ready = hash, true
			} else if hash != first {
				changed = true
			}
			if changed && p.Position() >= 2*time.Second && segments.Load() >= 2 {
				t.Logf("AVPlayer decoded changing 160x90 frames; position=%s; HLS segment requests=%d; required headers preserved", p.Position(), segments.Load())
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("native playback did not progress: ready=%v changed=%v position=%s segments=%d", ready, changed, p.Position(), segments.Load())
}
