package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/egoist/mygo/ui"
)

// Opt in with a generated, silent h264.mp4 fixture. This checks actual AVPlayer
// pixels underneath the overlay without contacting any external media service.
func TestNativeDanmakuOverVideo(t *testing.T) {
	dir := os.Getenv("XIAOMU_NATIVE_MEDIA_DIR")
	if dir == "" {
		t.Skip("requires a generated h264.mp4 fixture")
	}
	if _, err := os.Stat(filepath.Join(dir, "h264.mp4")); err != nil {
		t.Fatal(err)
	}
	media := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(media.Close)
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.autoNext = false
	a.startEpisode(catalog.Playback{URL: media.URL + "/h264.mp4"}, "线路", a.title.detail.Sources[0].Episodes[0], 0)
	a.preferences.Danmaku.Enabled = true
	tt := ui.NewTester(a.view, 1120, 760)
	deadline := time.Now().Add(10 * time.Second)
	for !a.player.Ready() && a.player.Err() == nil && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	if !a.player.Ready() || a.player.Err() != nil {
		t.Fatalf("native video is not ready: %v", a.player.Err())
	}
	a.player.Pause()
	tt.Frame()
	before := tt.Image()
	beforePixels := append([]byte(nil), before.Pix...)
	a.applyDanmaku(danmaku.Episode{ID: "local"}, []danmaku.Comment{{Time: a.player.Position(), Mode: 5, Color: 0xffffff, Text: "本地弹幕验证"}})
	tt.Frame()
	after := tt.Image()
	picture, ok := tt.Find("视频画面")
	if !ok {
		t.Fatal("video element missing")
	}
	w, h := a.player.Size()
	picture = playerPictureRect(picture, w, h)
	top := int(max(picture.Y+12, float32(72)))
	changed := 0
	for y := top; y < min(top+50, after.Bounds().Dy()); y++ {
		for x := int(picture.X); x < int(picture.X+picture.W); x++ {
			i := after.PixOffset(x, y)
			if after.Pix[i] > 200 && after.Pix[i+1] > 200 && after.Pix[i+2] > 200 && (beforePixels[i] < 150 || beforePixels[i+1] < 150 || beforePixels[i+2] < 150) {
				changed++
			}
		}
	}
	if changed < 40 {
		t.Fatalf("comments did not paint above native video: %d new bright pixels", changed)
	}
	t.Logf("AVPlayer %dx%d: overlay added %d bright text pixels above decoded video", w, h, changed)
}
