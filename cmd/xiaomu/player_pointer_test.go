package main

import (
	"image"
	"io"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type widePointerVideo struct{ sent bool }

func (d *widePointerVideo) Next() (ui.VideoFrame, error) {
	if d.sent {
		return ui.VideoFrame{}, io.EOF
	}
	d.sent = true
	return ui.VideoFrame{Image: image.NewRGBA(image.Rect(0, 0, 16, 9)), Duration: time.Minute}, nil
}

func (d *widePointerVideo) Rewind() error         { d.sent = false; return nil }
func (*widePointerVideo) Duration() time.Duration { return time.Minute }

func TestPlayerPictureClickTogglesPlayback(t *testing.T) {
	a := newKeyboardTestApp(t)
	tt := ui.NewTester(a.view, 1120, 760)
	for _, playing := range []bool{false, true} {
		tt.ClickAt(500, 300)
		tt.Advance(600 * time.Millisecond)
		if a.player.Playing() != playing {
			t.Fatalf("picture click: playing=%v, want %v", a.player.Playing(), playing)
		}
	}
}

func TestPlayerPictureDoubleClickOnlyTogglesFullscreen(t *testing.T) {
	for _, playing := range []bool{true, false} {
		t.Run(map[bool]string{true: "playing", false: "paused"}[playing], func(t *testing.T) {
			a := newKeyboardTestApp(t)
			if !playing {
				a.player.Pause()
			}
			tt := ui.NewTester(a.view, 1120, 760)
			for _, full := range []bool{true, false} {
				tt.ClickAt(500, 300)
				tt.Advance(100 * time.Millisecond)
				if a.player.Playing() != playing {
					t.Fatal("first half of double click changed playback")
				}
				tt.ClickAt(500, 300)
				tt.Advance(600 * time.Millisecond)
				if a.fullScreen != full || a.player.Playing() != playing {
					t.Fatalf("double click: fullscreen=%v want %v, playing=%v want %v", a.fullScreen, full, a.player.Playing(), playing)
				}
				tt.Advance(4 * time.Second) // also exercise hidden fullscreen controls
			}
		})
	}
}

func TestPlayerPictureIgnoresDragCancelAndOtherButtons(t *testing.T) {
	for _, gesture := range []string{"drag", "cancel", "right", "middle"} {
		t.Run(gesture, func(t *testing.T) {
			a := newKeyboardTestApp(t)
			tt := ui.NewTester(a.view, 1120, 760)
			switch gesture {
			case "drag":
				tt.Press(500, 300)
				tt.Move(560, 340)
				tt.Release(500, 300)
			case "cancel":
				tt.Press(500, 300)
				tt.CancelPress()
			case "right", "middle":
				b := map[string]int{"right": 1, "middle": 2}[gesture]
				tt.PressButton(b, 500, 300)
				tt.ReleaseButton(b, 500, 300)
			}
			tt.Advance(time.Second)
			if !a.player.Playing() || a.fullScreen {
				t.Fatal("non-click changed playback or fullscreen")
			}
		})
	}
}

func TestPlayerLetterboxDoesNotTogglePlayback(t *testing.T) {
	for _, size := range []struct{ w, h int }{{1120, 760}, {1600, 700}} {
		a := newKeyboardTestApp(t)
		p, err := ui.NewVideoPlayer(&widePointerVideo{})
		if err != nil {
			t.Fatal(err)
		}
		a.adoptPlayer(p)
		tt := ui.NewTester(a.view, size.w, size.h)
		tt.SetTitleBar(dragBar)
		// Exercise both horizontal and vertical black bars, away from controls.
		x, y := float32(8), float32(size.h)/2
		if size.w == 1120 {
			x, y = 500, 8
		}
		for range 2 {
			tt.ClickAt(x, y)
		}
		tt.Advance(time.Second)
		if !p.Playing() || a.fullScreen || tt.Focused("视频画面") {
			t.Fatalf("%dx%d: letterbox consumed as a picture gesture", size.w, size.h)
		}
		tt.ClickAt(float32(size.w)/2, float32(size.h)/2)
		tt.Advance(600 * time.Millisecond)
		if p.Playing() {
			t.Fatal("visible picture no longer takes clicks")
		}
	}
}

func TestPlayerPointerLeavesControlsAndDialogsAlone(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.setFullScreen(true)
	open := false
	tt := ui.NewTester(func(c *ui.Context) {
		a.view(c)
		ui.DialogBase(c, &open, func(_, panel ui.Element) {
			panel.Size(240, 120)
			ui.Text(c, "对话框")
		})
	}, 1120, 760)
	if err := tt.Click("暂停"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if a.player.Playing() {
		t.Fatal("pause button also clicked the picture underneath")
	}
	for _, control := range []string{"调节音量", "播放进度", "线路与选集"} {
		if err := tt.Click(control); err != nil {
			t.Fatal(err)
		}
		tt.Advance(time.Second)
		if a.player.Playing() || !a.fullScreen {
			t.Fatalf("%s triggered a picture gesture", control)
		}
	}
	open = true
	tt.Frame()
	tt.ClickAt(500, 350)
	tt.ClickAt(500, 350)
	tt.Advance(time.Second)
	if a.player.Playing() || !a.fullScreen {
		t.Fatal("dialog click reached the picture")
	}
}

func TestPlayerPendingClickDoesNotAffectNewEpisode(t *testing.T) {
	a := newKeyboardTestApp(t)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.ClickAt(500, 300)
	p, err := ui.NewVideoPlayer(&timedVideo{})
	if err != nil {
		t.Fatal(err)
	}
	a.adoptPlayer(p)
	tt.Advance(time.Second)
	if !p.Playing() {
		t.Fatal("previous episode's pending click paused the new player")
	}
	tt.ClickAt(500, 300)
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if a.player != nil {
		t.Fatal("pending click brought back a closed player")
	}
}
