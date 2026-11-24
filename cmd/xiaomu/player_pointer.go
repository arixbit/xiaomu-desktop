package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// Wait briefly before committing a single click so a double click never
// pauses and restarts the decoder. All state and timers stay on the UI thread.
const playerClickDelay = 260 * time.Millisecond

type playerPointerState struct {
	player              mediaPlayer
	down, moved, double bool
	x, y                float32
	clickAt             time.Time
	wasPlaying          bool
}

func (a *app) handlePlayerPointer(p mediaPlayer, ev ui.InputEvent) bool {
	if p != a.player || a.closed || ev.Button != 0 {
		return false
	}
	s := &a.playerPointer
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Mods != 0 {
			return false
		}
		s.down, s.moved = true, false
		s.x, s.y = ev.X, ev.Y
		s.double = ev.Clicks == 2 && !s.clickAt.IsZero()
		s.clickAt = time.Time{}
		return true
	case ui.InputPointerMove, ui.InputPointerUp:
		if !s.down {
			return false
		}
		dx, dy := ev.X-s.x, ev.Y-s.y
		s.moved = s.moved || dx*dx+dy*dy > 25
		if ev.Kind == ui.InputPointerUp {
			s.down = false
			if !ev.Canceled && !s.moved && ev.Mods == 0 {
				if s.double {
					a.togglePlayerFullScreen()
				} else if ev.Clicks <= 2 {
					s.clickAt, s.wasPlaying = ev.Time.Add(playerClickDelay), p.Playing()
				}
			}
		}
		return true
	}
	return false
}

func (a *app) flushPlayerClick(c *ui.Context, p mediaPlayer) {
	s := &a.playerPointer
	if s.player != p {
		*s = playerPointerState{player: p}
	}
	if s.clickAt.IsZero() {
		return
	}
	if delay := s.clickAt.Sub(c.Now()); delay > 0 {
		c.After(delay)
		return
	}
	s.clickAt = time.Time{}
	if p.Playing() == s.wasPlaying {
		togglePlayback(p)
		a.syncNowPlayingState()
		if p.Playing() {
			a.showPlayerOSD("继续播放")
		} else {
			a.showPlayerOSD("已暂停")
		}
	}
}

func (a *app) togglePlayerFullScreen() {
	a.setFullScreen(!a.fullScreen)
	if a.window != nil {
		a.window.SetFullScreen(a.fullScreen)
	}
}

// Match ui.Contain without changing the video layout: the letterbox area
// passes through to the page's native window drag handle.
func playerPictureRect(r ui.Rect, width, height int) ui.Rect {
	if width <= 0 || height <= 0 {
		return r
	}
	scale := min(r.W/float32(width), r.H/float32(height))
	w, h := float32(width)*scale, float32(height)*scale
	return ui.Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
}
