package main

import "time"

const (
	episodeEndTolerance = 250 * time.Millisecond
	episodeEndWait      = 2 * time.Second
)

// playbackEnd covers AVPlayer stopping just short of an HLS duration while
// MyGo still reports Playing instead of Ended. A near-end timestamp alone
// is not completion: it must remain still, without a pause or buffering.
type playbackEnd struct {
	since              time.Time
	position, duration time.Duration
}

func (e *playbackEnd) check(now time.Time, ready, buffering, playing bool, position, duration time.Duration) bool {
	if !ready || buffering || !playing || duration <= 0 || position <= 0 || duration-position > episodeEndTolerance {
		*e = playbackEnd{}
		return false
	}
	if e.since.IsZero() || position != e.position || duration != e.duration {
		e.since, e.position, e.duration = now, position, duration
		return false
	}
	return now.Sub(e.since) >= episodeEndWait
}
