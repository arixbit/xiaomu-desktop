package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type timedVideo struct{ stillVideo }

func (*timedVideo) Duration() time.Duration { return time.Minute }

func newKeyboardTestApp(t *testing.T) *app {
	t.Helper()
	a := newPlayerTestApp(t)
	a.player.Close()
	p, err := ui.NewVideoPlayer(&timedVideo{})
	if err != nil {
		t.Fatal(err)
	}
	a.player, a.autoNext = p, false
	return a
}

func TestPlayerKeyboardPauseAndRevealControls(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.setFullScreen(true)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Move(500, 300)
	tt.Advance(4 * time.Second)
	if tt.HasText("音量") {
		t.Fatal("controls did not hide before the keyboard check")
	}
	tt.Key(0, ui.KeySpace)
	if a.player.Playing() || !tt.HasText("音量") || tt.Cursor() == ui.CursorNone {
		t.Fatal("Space did not pause and reveal the controls and cursor")
	}
	tt.KeyRepeat(0, ui.KeySpace)
	if a.player.Playing() {
		t.Fatal("holding Space toggled playback again")
	}
	tt.Key(0, ui.KeySpace)
	if !a.player.Playing() {
		t.Fatal("a second Space press did not resume playback")
	}
}

func TestPlayerKeyboardSeek(t *testing.T) {
	a := newKeyboardTestApp(t)
	p := a.player
	p.Pause()
	p.Seek(25 * time.Second)
	tt := ui.NewTester(a.view, 1120, 760)
	for _, step := range []struct {
		key    ui.Key
		repeat bool
		want   time.Duration
	}{
		{ui.KeyLeft, false, 15 * time.Second},
		{ui.KeyLeft, true, 5 * time.Second},
		{ui.KeyLeft, true, 0},
		{ui.KeyRight, false, 10 * time.Second},
	} {
		if step.repeat {
			tt.KeyRepeat(0, step.key)
		} else {
			tt.Key(0, step.key)
		}
		if got := p.Position(); got != step.want || p.Playing() {
			t.Fatalf("seek: position=%s want=%s playing=%v", got, step.want, p.Playing())
		}
	}
	p.Seek(55 * time.Second)
	tt.Key(0, ui.KeyRight)
	if got := p.Position(); got != time.Minute {
		t.Fatalf("forward seek did not stop at the duration: %s", got)
	}
	tt.Key(0, ui.KeyLeft)
	if got := p.Position(); got != 50*time.Second {
		t.Fatalf("could not seek back from the end: %s", got)
	}
}

func TestPlayerKeyboardDoesNotSeekLiveOrUnknownDuration(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown duration", true: "live"}[live], func(t *testing.T) {
			a := newPlayerTestApp(t)
			if live {
				a.player.Close()
				var err error
				a.player, err = ui.NewVideoPlayer(&timedVideo{})
				if err != nil {
					t.Fatal(err)
				}
			}
			a.playingLive = live
			a.player.Pause()
			a.player.Seek(20 * time.Second)
			tt := ui.NewTester(a.view, 1120, 760)
			for _, key := range []ui.Key{ui.KeyLeft, ui.KeyRight} {
				tt.Key(0, key)
				if got := a.player.Position(); got != 20*time.Second {
					t.Fatalf("non-seekable playback moved to %s", got)
				}
			}
			tt.Key(0, ui.KeySpace)
			if !a.player.Playing() {
				t.Fatal("Space should still resume non-seekable playback")
			}
		})
	}
}

func TestKeyboardEscapeLeavesFullscreenWithoutStoppingPlayback(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.setFullScreen(true)
	p := a.player
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Key(0, ui.KeyEscape)
	if a.fullScreen || a.player != p || !p.Playing() {
		t.Fatal("Escape should leave fullscreen and keep the current video playing")
	}
	tt.Key(0, ui.KeyEscape)
	if a.fullScreen || a.player != p {
		t.Fatal("Escape in a normal window changed the player")
	}
}

func TestKeyboardEscapeFromFullscreenHome(t *testing.T) {
	a := &app{}
	a.setFullScreen(true)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Key(0, ui.KeyEscape)
	if a.fullScreen || !tt.HasText("首页") {
		t.Fatal("Escape should also leave fullscreen outside the player")
	}
}

func TestPlayerKeyboardRespectsDialog(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.player.Pause()
	a.player.Seek(20 * time.Second)
	a.setFullScreen(true)
	open := true
	tt := ui.NewTester(func(c *ui.Context) {
		a.view(c)
		ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
			panel.Size(240, 120)
			ui.Text(c, "对话框")
		})
	}, 1120, 760)
	for _, key := range []ui.Key{ui.KeySpace, ui.KeyLeft, ui.KeyRight} {
		tt.Key(0, key)
	}
	if a.player.Playing() || a.player.Position() != 20*time.Second {
		t.Fatal("keys in a dialog changed the player behind it")
	}
	tt.Key(0, ui.KeyEscape)
	if open || !a.fullScreen {
		t.Fatal("Escape should dismiss the dialog before leaving fullscreen")
	}
	tt.Key(0, ui.KeyEscape)
	if a.fullScreen {
		t.Fatal("Escape did not leave fullscreen after the dialog closed")
	}
}

func TestPlayerKeyboardLeavesTextInputAndModifiersAlone(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.player.Pause()
	a.player.Seek(20 * time.Second)
	a.setFullScreen(true)
	text := ""
	tt := ui.NewTester(func(c *ui.Context) {
		a.view(c)
		ui.TextInput(c, &text).Label("输入内容").Absolute().Top(100).Left(100).Width(200)
	}, 1120, 760)
	if err := tt.Click("输入内容"); err != nil {
		t.Fatal(err)
	}
	tt.Type("ab")
	tt.Key(0, ui.KeyLeft)
	tt.TypeKey(0, ui.KeySpace, " ")
	tt.Key(0, ui.KeyRight)
	if text != "a b" || a.player.Playing() || a.player.Position() != 20*time.Second {
		t.Fatalf("text input triggered playback: text=%q playing=%v position=%s", text, a.player.Playing(), a.player.Position())
	}
	tt.ClickAt(500, 300)
	for _, mods := range []ui.Modifiers{ui.Cmd, ui.Ctrl, ui.Alt, ui.Shift} {
		for _, key := range []ui.Key{ui.KeySpace, ui.KeyLeft, ui.KeyRight, ui.KeyEscape} {
			tt.Key(mods, key)
		}
	}
	if a.player.Playing() || a.player.Position() != 20*time.Second || !a.fullScreen {
		t.Fatal("modified keys triggered player shortcuts")
	}
}
