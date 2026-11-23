package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// Register keys outside the auto-hiding controls. Focused controls and dialogs
// get first use of shortcuts; single-key commands also leave text editing alone.
func (a *app) handleKeyboard(c *ui.Context) {
	if a.player == nil && a.library.root != nil && c.Shortcut(ui.Cmd, ui.KeyK) && !c.ShortcutRepeated() {
		a.searchFocused = true
		a.searchFocus.SelectAll(c)
		return
	}
	if c.FocusTakesText() {
		// Autocomplete consumes Escape while its suggestions are open.
		// The next press leaves the field; only a later press goes back.
		if ui.FocusedValue(c, &a.searchFocused) && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
			a.searchFocused = false
		}
		return
	}
	if a.fullScreen && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
		a.setFullScreen(false)
		if a.window != nil {
			a.window.SetFullScreen(false)
		}
		// One key, one step: leaving fullscreen does not also go back.
	} else if a.player == nil && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() && len(a.nav) > 1 {
		// Outside the player, Escape goes back a page: from a search or a
		// title to the page it came from. A dialog or a suggestions popup
		// takes the key first, and a text field keeps it, as before.
		a.pop()
	}
	p := a.player
	if p == nil {
		return
	}
	if c.Shortcut(0, ui.KeySpace) {
		if !c.ShortcutRepeated() {
			togglePlayback(p)
			a.syncNowPlayingState()
		}
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if a.hasEpisodes() && c.Shortcut(0, ui.KeyD) && !c.ShortcutRepeated() {
		a.toggleDanmaku()
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
	a.syncNowPlayingState()
	a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
}

func togglePlayback(p *ui.VideoPlayer) {
	if p.Playing() {
		p.Pause()
	} else {
		p.Play()
	}
}
