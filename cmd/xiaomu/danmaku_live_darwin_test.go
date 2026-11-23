package main

import (
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/ui"
)

// This opt-in check uses the public default service and a generated, silent
// h264.mp4 of at least three seconds. No viewing title or source is a fixture.
func TestNativeDanmakuLiveDefaultService(t *testing.T) {
	title := strings.TrimSpace(os.Getenv("XIAOMU_DANMAKU_LIVE_TITLE"))
	dir := os.Getenv("XIAOMU_NATIVE_MEDIA_DIR")
	if title == "" || dir == "" {
		t.Skip("requires an explicit live-search title and a generated h264.mp4")
	}
	number, err := strconv.Atoi(os.Getenv("XIAOMU_DANMAKU_LIVE_EPISODE"))
	if err != nil || number <= 0 {
		t.Fatal("live episode must be a positive integer")
	}
	if _, err := os.Stat(filepath.Join(dir, "h264.mp4")); err != nil {
		t.Fatal(err)
	}
	media := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(media.Close)
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferences.Danmaku.Enabled = true
	a.autoNext = false
	a.title.name, a.title.detail.Name = title, title
	episode := catalog.Episode{Name: fmt.Sprintf("第%d集", number), ID: "generated-media"}
	a.title.detail.Sources[0].Episodes = []catalog.Episode{episode}
	a.startEpisode(catalog.Playback{URL: media.URL + "/h264.mp4"}, "线路", episode, 0)
	if a.preferences.Danmaku.API != "" || a.danmaku.selected == nil || len(a.danmaku.comments) == 0 {
		t.Fatalf("automatic default service did not load comments: %s", a.danmaku.message)
	}
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
	a.player.Seek(2 * time.Second)
	for a.player.Position() < 1500*time.Millisecond && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	if a.player.Position() < 1500*time.Millisecond {
		t.Fatal("generated video did not seek into the live comment track")
	}
	a.preferences.Danmaku.Enabled = false
	tt.Frame()
	before := tt.Image()
	beforePixels := append([]byte(nil), before.Pix...)
	a.preferences.Danmaku.Enabled = true
	a.danmaku.screen.Reset(a.danmaku.comments)
	tt.Frame()
	after := tt.Image()
	picture, ok := tt.Find("视频画面")
	if !ok {
		t.Fatal("video element missing")
	}
	w, h := a.player.Size()
	picture = playerPictureRect(picture, w, h)
	top := int(max(picture.Y+12, float32(72)))
	bottom := int(min(picture.Y+picture.H-12, float32(after.Bounds().Dy()-132)))
	changed := 0
	for y := top; y < bottom; y++ {
		for x := int(picture.X); x < int(picture.X+picture.W); x++ {
			i := after.PixOffset(x, y)
			if after.Pix[i] > 200 && after.Pix[i+1] > 200 && after.Pix[i+2] > 200 && (beforePixels[i] < 150 || beforePixels[i+1] < 150 || beforePixels[i+2] < 150) {
				changed++
			}
		}
	}
	if changed < 40 {
		t.Fatalf("downloaded comments did not paint above native video: %d new bright pixels", changed)
	}
	if path := os.Getenv("XIAOMU_DANMAKU_LIVE_IMAGE"); path != "" {
		saveImage := func(path string) {
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			encodeErr, closeErr := png.Encode(file, tt.Image()), file.Close()
			if encodeErr != nil || closeErr != nil {
				t.Fatalf("save live rendering: %v, %v", encodeErr, closeErr)
			}
		}
		saveImage(path)
		if err := tt.Click("弹幕"); err != nil {
			t.Fatal(err)
		}
		for _, label := range []string{"弹幕滚动速度", "弹幕显示区域"} {
			if _, ok := tt.Find(label); !ok {
				t.Fatalf("display setting missing: %s", label)
			}
		}
		saveImage(strings.TrimSuffix(path, filepath.Ext(path)) + "-settings.png")
	}
	t.Logf("Default service: %d candidates, %d comments; AVPlayer %dx%d, %d new bright text pixels", len(a.danmaku.episodes), len(a.danmaku.comments), w, h, changed)
}
