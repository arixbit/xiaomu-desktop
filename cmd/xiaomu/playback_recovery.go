package main

import (
	"errors"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
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
	line    string
	episode catalog.Episode
	at      time.Duration
	err     error
}

func (a *app) playbackPosition() time.Duration {
	if a.resumeAt > 0 {
		return a.resumeAt
	}
	return a.lastPosition
}

func (a *app) checkPlayback(c *ui.Context, p *ui.VideoPlayer) {
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
			a.playFailure = &episodeFailure{line: source.Name, episode: source.Episodes[episode], at: a.playbackPosition(), err: err}
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
	if line, _, episode, at, ok := a.recoveryEpisode(); ok {
		// Some sites only return a fresh media URL in the title detail.
		a.loadEpisode(a.title.detail.Sources[line].Name, episode, at, true)
	}
}

func (a *app) recoveryActions(c *ui.Context) {
	ui.Row(c).Wrap().Gap(8).Children(func() {
		if len(a.title.detail.Sources) > 1 && primaryButton(c, "换下一条线路").Clicked() {
			a.playNextLine()
		}
		if a.player != nil && (a.library.root != nil || len(a.candidates()) > 1) && glassButton(c, "换来源").TextColor(playerText).Clicked() {
			a.panelTab, a.panelOpen, a.panelTouched = 0, true, true
			a.playerOverlay = playerOverlayState{}
		}
		if glassButton(c, "重试").TextColor(playerText).Clicked() {
			a.retryEpisode()
		}
	})
}
