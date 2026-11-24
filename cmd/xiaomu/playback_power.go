package main

import (
	"time"
)

// The release function belongs to active playback, not to the window or to
// an individual episode. keepAwake is wired to MyGo in main and faked in tests.
type playbackPower struct {
	keepAwake func(reason string, display bool) func()
	release   func()
	player    mediaPlayer
	end       playbackEnd
}

// syncPlaybackPower is independent of Now Playing being available. Frames
// catch native state changes; explicit play/pause/close paths also call it
// so a media key releases the assertion even without another UI frame.
func (a *app) syncPlaybackPower(now time.Time) {
	s := &a.playbackPower
	if s.keepAwake == nil && s.release == nil {
		return
	}
	p := a.player
	if s.player != p {
		s.player, s.end = p, playbackEnd{}
	}
	active := !a.closed && a.playFailure == nil && p != nil && p.Ready() && p.Playing() && p.Err() == nil
	if active {
		// HLS can stop just short of its declared duration without Ended.
		// Release even for the final episode or with auto-next disabled.
		active = !s.end.check(now, a.resumeAt == 0, p.Buffering(), p.Playing(), p.Position(), p.Duration())
	} else {
		s.end = playbackEnd{}
	}
	if active {
		if s.release == nil && s.keepAwake != nil {
			s.release = s.keepAwake("Xiaomu video playback", true)
		}
	} else if release := s.release; release != nil {
		s.release = nil
		release()
	}
}
