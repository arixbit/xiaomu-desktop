package main

import (
	"errors"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/egoist/mygo/ui"
)

const playbackWaitLimit = 30 * time.Second

// playbackWait measures a continuous wait, not time spent paused or playing.
type playbackWait struct {
	since    time.Time
	position time.Duration
}

func (w *playbackWait) check(now time.Time, ready, buffering, playing bool, position time.Duration) error {
	if !playing || ready && !buffering {
		*w = playbackWait{}
		return nil
	}
	if w.since.IsZero() || position != w.position {
		w.since, w.position = now, position
	}
	if now.Sub(w.since) < playbackWaitLimit {
		return nil
	}
	if !ready {
		return errors.New("等待画面超过 30 秒，可重试或换一条线路")
	}
	return errors.New("缓冲超过 30 秒且播放进度没有推进，可重试或换一条线路")
}

// A failure keeps the requested episode even when resolving it never opened
// a player, or a previous episode is still on screen. It holds no media URL.
type episodeFailure struct {
	line         string
	episode      catalog.Episode
	at           time.Duration
	err          error
	authProvider pan.Provider
	retried      bool
}

func (a *app) playbackPosition() time.Duration {
	if a.resumeAt > 0 {
		return a.resumeAt
	}
	return a.lastPosition
}

func (a *app) checkPlayback(c *ui.Context, p mediaPlayer) {
	if !a.hasEpisodes() || a.playFailure != nil || a.running(jobPlay) {
		return
	}
	err := p.Err()
	if err == nil {
		err = a.playbackWait.check(c.Now(), p.Ready(), p.Buffering(), p.Playing(), p.Position())
	}
	a.observeSourcePlayback(c.Now(), p, err)
	if err != nil {
		if line, episode, ok := a.currentEpisode(); ok {
			source := a.title.detail.Sources[line]
			pos := a.playbackPosition()
			if pos <= 0 {
				pos = p.Position()
			}
			a.playFailure = &episodeFailure{line: source.Name, episode: source.Episodes[episode], at: pos, err: err}
			p.Pause()
			a.playerOverlay = playerOverlayState{}
		}
	} else if !a.playbackWait.since.IsZero() {
		c.After(time.Second)
	}
}

// recoveryEpisode chooses the failed request, or the currently playing one.
func (a *app) recoveryEpisode() (line, index int, episode catalog.Episode, at time.Duration, ok bool) {
	if a.title == nil || a.title.detail == nil {
		return
	}
	if f := a.playFailure; f != nil {
		for i, source := range a.title.detail.Sources {
			if source.Name == f.line {
				for j, e := range source.Episodes {
					if e == f.episode {
						return i, j, e, f.at, true
					}
				}
			}
		}
		return
	}
	line, index, ok = a.currentEpisode()
	if ok {
		episode = a.title.detail.Sources[line].Episodes[index]
		at = a.playbackPosition()
	}
	return
}

func (a *app) retryEpisode() {
	if a.isRetryingFailure() {
		return
	}
	if choice := a.panelChoice; choice != nil && choice.failed {
		a.failureUI.retrying = true
		a.failureUI.retryJobGen = 0
		a.failureUI.copySeq++
		a.failureUI.copied = false
		a.failureUI.copyFailed = false
		if choice.detail != nil && len(choice.detail.Sources) > 0 && a.panelSource >= 0 && a.panelSource < len(choice.detail.Sources) && (choice.episode.Name != "" || choice.episode.ID != "") {
			_, _, _, at, _ := a.recoveryEpisode()
			a.playPanelEpisodeInternal(a.panelSource, choice.episode, at, true)
		} else {
			a.switchSourceInternal(choice.item, true)
		}
		if a.failureUI.retrying {
			if a.running(jobPlay) {
				a.failureUI.retryJobGen = a.jobGen(jobPlay)
			} else {
				a.failureUI.retrying = false
				a.failureUI.retryJobGen = 0
			}
		}
		return
	}
	if line, _, episode, at, ok := a.recoveryEpisode(); ok {
		// Some sites only return a fresh media URL in the title detail.
		a.loadEpisode(a.title.detail.Sources[line].Name, episode, at, true)
	}
}

// switchToNextCandidate switches the detail page to the next available
// candidate source in priority order, skipping the current source and any
// source already marked as failed.
func (a *app) switchToNextCandidate() bool {
	t := a.title
	if t == nil {
		return false
	}
	candidates := a.candidates()
	if len(candidates) <= 1 {
		a.setNotice("没有其他可用来源")
		return false
	}
	curIdx := -1
	for i, item := range candidates {
		if t.isCurrent(item) {
			curIdx = i
			break
		}
	}
	// First pass: prefer a non-current candidate that has not failed.
	for step := 1; step <= len(candidates); step++ {
		idx := (curIdx + step) % len(candidates)
		if curIdx < 0 {
			idx = step - 1
		}
		item := candidates[idx]
		if t.isCurrent(item) || t.failed[itemKey(item)] != "" {
			continue
		}
		t.auto = false
		a.openCandidate(item)
		return true
	}
	// Fallback: all other candidates have previously failed; still cycle to the
	// next non-current source when the user explicitly requests switching.
	for step := 1; step <= len(candidates); step++ {
		idx := (curIdx + step) % len(candidates)
		if curIdx < 0 {
			idx = step - 1
		}
		item := candidates[idx]
		if t.isCurrent(item) {
			continue
		}
		t.auto = false
		a.openCandidate(item)
		return true
	}
	a.setNotice("没有其他可用来源")
	return false
}

func (a *app) recoveryActions(c *ui.Context) {
	ui.Row(c).Wrap().Gap(8).Children(func() {
		if f := a.playFailure; f != nil && f.authProvider != "" {
			provider := f.authProvider
			line, ep, at := f.line, f.episode, f.at
			if primaryButton(c, "登录"+provider.Label()).Clicked() {
				originTitle := a.title
				originPlayGen := a.jobGen(jobPlay)
				originPlayer := a.player
				a.beginPanWebLogin(provider, func() {
					if a.title != originTitle || a.jobGen(jobPlay) != originPlayGen || a.player != originPlayer {
						return
					}
					a.resumeAfterPanLogin(line, ep, at)
				})
			}
		}
		if len(a.title.detail.Sources) > 1 && primaryButton(c, "换下一条线路").Clicked() {
			a.playNextLine()
		}
		if len(a.candidates()) > 1 && glassButton(c, "换来源").Clicked() {
			if a.player != nil {
				a.openSourcesFromFailure()
			} else {
				a.switchToNextCandidate()
			}
		}
		if a.title != nil && !a.inStack(routeSearch) && len(a.candidates()) <= 1 && a.library.root != nil {
			search := a.titleSearch()
			searchingThis := search.running && titleKey(search.query) == titleKey(a.title.name)
			if searchingThis {
				glassButton(c, "查找来源中…").Disabled(true)
			} else if titleKey(search.query) != titleKey(a.title.name) {
				if glassButton(c, "查找其他来源").Clicked() {
					a.startScan(search, a.title.name, false)
					if a.player != nil {
						a.openEpisodePanel()
						a.playerOverlay = playerOverlayState{}
					}
				}
			}
		}
		if glassButton(c, "重试").Clicked() {
			a.retryEpisode()
		}
	})
}
