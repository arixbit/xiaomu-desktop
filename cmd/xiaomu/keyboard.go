package main

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// Register keys outside the auto-hiding controls. Focused controls and dialogs
// get first use of shortcuts; single-key commands also leave text editing alone.
func (a *app) handleKeyboard(c *ui.Context) {
	if c.Shortcut(ui.Cmd, ui.KeyO) && !c.ShortcutRepeated() {
		a.releaseRightKeyHold(a.player)
		a.openLocalFileDialog()
		return
	}
	if a.player == nil && a.library.root != nil && c.Shortcut(ui.Cmd, ui.KeyK) && !c.ShortcutRepeated() {
		a.searchFocused = true
		a.searchFocus.SelectAll(c)
		return
	}
	if c.FocusTakesText() {
		a.releaseRightKeyHold(a.player)
		// Autocomplete consumes Escape while its suggestions are open.
		// The next press leaves the field; only a later press goes back.
		if ui.FocusedValue(c, &a.searchFocused) && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
			a.searchFocused = false
		}
		return
	}
	if a.armed.key != "" && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
		a.clearArmed()
		return
	}
	if a.player != nil && a.playerPopovers.anyOpen() && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
		a.closePlayerPopovers()
		return
	}
	if a.player != nil && a.hasSeekPreview() && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
		a.dismissSeekPreview()
		return
	}
	if a.fullScreen && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
		a.setFullScreen(false)
		if a.window != nil {
			a.window.SetFullScreen(false)
		}
		// One key, one step: leaving fullscreen does not also go back.
		return
	}
	if a.player != nil && a.panelOpen && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
		a.closeEpisodePanel()
		return
	}
	if a.player != nil && a.hasPlayerFailureOverlay(a.player) && a.failureUI.detailsOpen && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() {
		a.failureUI.detailsOpen = false
		a.failureUI.copySeq++
		a.failureUI.copied = false
		a.failureUI.copyFailed = false
		return
	}
	if a.player == nil && c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() && len(a.nav) > 1 {
		// Outside the player, Escape goes back a page: from a search or a
		// title to the page it came from. A dialog or a suggestions popup
		// takes the key first, and a text field keeps it, as before.
		a.pop()
		return
	}
	p := a.player
	if p == nil {
		return
	}
	if a.playerExtra.rightHold.down && !c.KeyDown(ui.KeyRight) {
		a.handleRightKeyHold(c, p, false, false)
	}
	if c.Shortcut(0, ui.KeySpace) {
		if !c.ShortcutRepeated() {
			togglePlayback(p)
			a.syncNowPlayingState()
			if p.Playing() {
				a.showPlayerOSD("继续播放")
			} else {
				a.showPlayerOSD("已暂停")
			}
		}
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if a.canUseDanmaku() && c.Shortcut(0, ui.KeyD) && !c.ShortcutRepeated() {
		a.toggleDanmaku()
		if a.preferences.Danmaku.Enabled {
			a.showPlayerOSD("弹幕已开")
		} else {
			a.showPlayerOSD("弹幕已关")
		}
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if c.Shortcut(0, ui.KeyM) && !c.ShortcutRepeated() {
		a.toggleMute(p)
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if c.Shortcut(0, ui.KeyF) && !c.ShortcutRepeated() {
		a.togglePlayerFullScreen()
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if !a.fullScreen && c.Shortcut(0, ui.KeyP) && !c.ShortcutRepeated() {
		a.toggleAlwaysOnTop()
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if c.Shortcut(0, ui.KeyT) && !c.ShortcutRepeated() {
		a.toggleMiniMode()
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if c.Shortcut(0, ui.KeyI) && !c.ShortcutRepeated() {
		a.toggleStatsOverlay()
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if c.Shortcut(0, ui.KeyS) && !c.ShortcutRepeated() {
		a.capturePlayerScreenshot()
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if c.Shortcut(0, ui.KeyR) && !c.ShortcutRepeated() {
		if _, ok := p.(advancedMediaPlayer); ok {
			a.rotateVideo(p)
			a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
		}
	}
	if !a.isLiveStream() && c.Shortcut(0, ui.KeyL) && !c.ShortcutRepeated() {
		a.toggleLoop(p)
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if !a.isLiveStream() && c.Shortcut(0, ui.KeyBracketLeft) {
		a.adjustSubtitleDelay(p, -500*time.Millisecond)
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if !a.isLiveStream() && c.Shortcut(0, ui.KeyBracketRight) {
		a.adjustSubtitleDelay(p, 500*time.Millisecond)
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if c.Shortcut(0, ui.KeyUp) {
		a.adjustVolume(p, 0.05)
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	} else if c.Shortcut(0, ui.KeyDown) {
		a.adjustVolume(p, -0.05)
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
	}
	if !a.canSeekPlayer(p) {
		return
	}
	if c.Shortcut(0, ui.KeyLeft) {
		p.Seek(min(max(p.Position()-10*time.Second, 0), p.Duration()))
		a.syncNowPlayingState()
		a.showPlayerOSD("快退 10 秒")
		a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
		return
	}
	if pressed := c.Shortcut(0, ui.KeyRight); pressed || a.playerExtra.rightHold.down {
		a.handleRightKeyHold(c, p, pressed, pressed && c.ShortcutRepeated())
	}
}

func (a *app) toggleMute(p mediaPlayer) {
	if p == nil {
		return
	}
	if a.muted {
		a.muted = false
		p.SetMuted(false)
		vol := a.savedVolume
		if vol <= 0 {
			vol = p.Volume()
		}
		if vol <= 0 {
			vol = 1
		}
		p.SetVolume(vol)
		a.showPlayerOSD(fmt.Sprintf("音量 %d%%", int(math.Round(vol*100))))
		return
	}
	if v := p.Volume(); v > 0 {
		a.savedVolume = v
	}
	a.muted = true
	p.SetMuted(true)
	a.showPlayerOSD("已静音")
}

func (a *app) adjustVolume(p mediaPlayer, delta float64) {
	if p == nil {
		return
	}
	if a.muted && delta < 0 {
		p.SetMuted(true)
		a.showPlayerOSD("已静音")
		return
	}
	vol := p.Volume()
	if a.muted && delta > 0 {
		a.muted = false
		p.SetMuted(false)
		if a.savedVolume > 0 {
			vol = a.savedVolume
		}
	}
	vol = math.Round(min(max(vol+delta, 0), 1)*100) / 100
	p.SetVolume(vol)
	if vol <= 0 {
		a.muted = true
		p.SetMuted(true)
		a.showPlayerOSD("已静音")
	} else {
		a.muted = false
		p.SetMuted(false)
		a.savedVolume = vol
		a.showPlayerOSD(fmt.Sprintf("音量 %d%%", int(math.Round(vol*100))))
	}
	a.ensurePreferences()
	a.preferences.Volume = vol
	a.savePreferences()
}

func togglePlayback(p mediaPlayer) {
	if p.Playing() {
		p.Pause()
	} else {
		p.Play()
	}
}
