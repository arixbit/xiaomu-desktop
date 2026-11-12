package main

import (
	"image"
	"io"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/ui"
)

type stillVideo struct{ sent bool }

func (d *stillVideo) Next() (ui.VideoFrame, error) {
	if d.sent {
		return ui.VideoFrame{}, io.EOF
	}
	d.sent = true
	return ui.VideoFrame{Image: image.NewRGBA(image.Rect(0, 0, 2, 2)), Duration: time.Minute}, nil
}

func (d *stillVideo) Rewind() error { d.sent = false; return nil }

func newPlayerTestApp(t *testing.T) *app {
	t.Helper()
	player, err := ui.NewVideoPlayer(&stillVideo{})
	if err != nil {
		t.Fatal(err)
	}
	a := &app{player: player, autoNext: true, playingSource: "线路", playingEpisode: "第1集"}
	a.browser.detail = &catalog.Detail{
		Name: "示例片",
		Sources: []catalog.Source{{
			Name: "线路",
			Episodes: []catalog.Episode{
				{Name: "第1集", ID: "one"},
				{Name: "第2集", ID: "two"},
			},
		}},
	}
	t.Cleanup(a.close)
	return a
}

func TestPlayerScreenReturnsToLibrary(t *testing.T) {
	a := newPlayerTestApp(t)
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("线路与选集"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("线路与选集") || !tt.HasText("2") || tt.HasText("第2集") || !tt.HasText("播完自动下一集") {
		t.Fatalf("player controls missing: %v", tt.Texts())
	}
	if err := tt.Click("返回片库"); err != nil {
		t.Fatal(err)
	}
	if a.player != nil || !tt.HasText("片库") {
		t.Fatalf("did not return to library: %v", tt.Texts())
	}
}

func TestFullscreenPlayerOverlays(t *testing.T) {
	a := newPlayerTestApp(t)
	a.panelOpen = true
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Advance(4 * time.Second)
	if !tt.HasText("返回片库") || !tt.HasText("播完自动下一集") {
		t.Fatal("windowed controls should stay visible")
	}
	a.setFullScreen(true)
	tt.Frame()
	if a.panelOpen || tt.HasText("播完自动下一集") {
		t.Fatal("entering fullscreen did not collapse the episode panel")
	}
	video, ok := tt.Find("视频画面")
	if !ok || video.W != 1120 {
		t.Fatalf("fullscreen video does not fill the width: %+v", video)
	}
	tt.Move(500, 300)
	tt.Advance(3 * time.Second)
	if tt.HasText("返回片库") || tt.HasText("音量") || tt.Cursor() != ui.CursorNone {
		t.Fatalf("idle fullscreen did not hide controls and cursor: %v", tt.Texts())
	}
	if _, ok := tt.Find("播放进度"); ok {
		t.Fatal("hidden controls remain interactive")
	}
	tt.Move(501, 300)
	if !tt.HasText("返回片库") || !tt.HasText("音量") || tt.Cursor() == ui.CursorNone {
		t.Fatalf("pointer movement did not restore controls: %v", tt.Texts())
	}
	if err := tt.Click("线路与选集"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("2") || tt.HasText("第2集") {
		t.Fatalf("episode buttons are not numeric: %v", tt.Texts())
	}
	if withPanel, _ := tt.Find("视频画面"); withPanel != video {
		t.Fatalf("fullscreen panel resized the video: before=%+v after=%+v", video, withPanel)
	}
	tt.Move(1050, 200)
	tt.Advance(4 * time.Second)
	if !tt.HasText("播完自动下一集") {
		t.Fatal("episode panel hid while being used")
	}
	tt.Move(500, 300)
	tt.Advance(3 * time.Second)
	if tt.HasText("返回片库") || tt.HasText("播完自动下一集") {
		t.Fatal("idle controls and episode panel did not hide")
	}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	a.playURL("https://example.com/video.mp4", nil)
	tt.Frame()
	if a.panelOpen || tt.HasText("播完自动下一集") {
		t.Fatal("starting another episode reopened the fullscreen panel")
	}
	a.setFullScreen(false)
	tt.Frame()
	tt.Advance(4 * time.Second)
	if !tt.HasText("返回片库") || !tt.HasText("音量") || tt.Cursor() == ui.CursorNone {
		t.Fatalf("leaving fullscreen did not restore controls: %v", tt.Texts())
	}
}

func TestFullscreenKeepsControlsWhileDragging(t *testing.T) {
	a := newPlayerTestApp(t)
	a.setFullScreen(true)
	tt := ui.NewTester(a.view, 1120, 760)
	volume, ok := tt.Find("调节音量")
	if !ok {
		t.Fatal("no volume slider")
	}
	x, y := volume.X+volume.W/2, volume.Y+volume.H/2
	tt.Press(x, y)
	tt.Advance(7 * time.Second)
	if !tt.HasText("返回片库") {
		t.Fatal("controls hid during a slider drag")
	}
	tt.Release(x, y)
	tt.Advance(3 * time.Second)
	if tt.HasText("返回片库") {
		t.Fatal("controls did not hide after the slider was released")
	}
}

func TestEpisodeButtonLabel(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"第01集", "1"}, {"第57集", "57"}, {"第60集已完结", "60"},
		{"第 02 集", "2"}, {"003", "3"}, {"10集", "10"},
		{"正片", "正片"}, {"预告", "预告"}, {"花絮", "花絮"},
		{"第1集预告", "第1集预告"}, {"第1-2集", "第1-2集"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := episodeButtonLabel(tc.name); got != tc.want {
				t.Fatalf("label = %q, want %q", got, tc.want)
			}
		})
	}
}
