package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// Register keys outside the auto-hiding controls. Focused controls and dialogs
// get first use of shortcuts; single-key commands also leave text editing alone.
func (a *app) handleKeyboard(c *ui.Context) {
	if c.FocusTakesText() {
		return
	}
	if a.fullScreen && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
		a.setFullScreen(false)
		if a.window != nil {
			a.window.SetFullScreen(false)
		}
	}
	p := a.player
	if p == nil {
		return
	}
	if c.Shortcut(0, ui.KeySpace) {
		if !c.ShortcutRepeated() {
			togglePlayback(p)
		}
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if a.playingLive || p.Duration() <= 0 {
		return
	}
	var step time.Duration
	switch {
	case c.Shortcut(0, ui.KeyLeft):
		step = -10 * time.Second
	case c.Shortcut(0, ui.KeyRight):
		step = 10 * time.Second
	default:
		return
	}
	p.Seek(min(max(p.Position()+step, 0), p.Duration()))
	a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
}

func togglePlayback(p *ui.VideoPlayer) {
	if p.Playing() {
		p.Pause()
	} else {
		p.Play()
	}
}
