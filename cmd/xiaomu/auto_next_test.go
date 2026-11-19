package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/ui"
)

type endTestVideo struct {
	timedVideo
	retired bool
}

func (d *endTestVideo) Duration() time.Duration {
	if d.retired {
		panic("read duration from the previous player's released engine")
	}
	return d.timedVideo.Duration()
}

func TestPlaybackEndRequiresStableProgressAndResetsOnInterruptions(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name                      string
		ready, buffering, playing bool
		position, duration        time.Duration
	}{
		{"loading", false, false, true, 59900 * time.Millisecond, time.Minute},
		{"paused", true, false, false, 59900 * time.Millisecond, time.Minute},
		{"buffering", true, true, true, 59900 * time.Millisecond, time.Minute},
		{"unknown duration", true, false, true, 59900 * time.Millisecond, 0},
		{"before end window", true, false, true, 59 * time.Second, time.Minute},
		{"at start", true, false, true, 0, 100 * time.Millisecond},
		{"moving within end window", true, false, true, 59950 * time.Millisecond, time.Minute},
		{"seeking backwards within end window", true, false, true, 59800 * time.Millisecond, time.Minute},
		{"duration changed", true, false, true, 59900 * time.Millisecond, 60050 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var end playbackEnd
			position := time.Minute - 100*time.Millisecond
			if end.check(now, true, false, true, position, time.Minute) {
				t.Fatal("a near-end position alone must not complete an episode")
			}
			if end.check(now.Add(time.Second), tc.ready, tc.buffering, tc.playing, tc.position, tc.duration) {
				t.Fatal("interrupted or moving playback was treated as finished")
			}
			if end.check(now.Add(3*time.Second), true, false, true, position, time.Minute) {
				t.Fatal("completion reused the observation from before the interruption")
			}
			if end.check(now.Add(4900*time.Millisecond), true, false, true, position, time.Minute) {
				t.Fatal("completed before the full settling period")
			}
			if !end.check(now.Add(5*time.Second), true, false, true, position, time.Minute) {
				t.Fatal("stable final timestamp did not complete the episode")
			}
		})
	}
}

func TestAutoNextEndFallbackHonorsPlaybackControls(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*app)
	}{
		{"disabled", func(a *app) { a.autoNext = false }},
		{"paused", func(a *app) { a.player.Pause() }},
		{"last episode", func(a *app) { a.playingEpisode = "第2集" }},
		{"pending resume seek", func(a *app) { a.resumeAt = 59 * time.Second }},
		{"live", func(a *app) { a.playingLive = true }},
		{"direct playback", func(a *app) { a.playingDirect = true }},
		{"playback failed", func(a *app) { a.playFailure = &episodeFailure{err: errors.New("fixture failure")} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newKeyboardTestApp(t)
			a.autoNext = true
			a.player.Seek(time.Minute - 100*time.Millisecond)
			requests := 0
			a.title.session = &recoverySession{play: func(context.Context, string, catalog.Episode) (catalog.Playback, error) {
				requests++
				return catalog.Playback{}, errors.New("must not be requested")
			}}
			now := time.Now()
			a.advanceEpisode(a.player, now)
			tc.stop(a)
			a.advanceEpisode(a.player, now.Add(3*time.Second))
			if requests != 0 || a.autoNextPending || len(a.history.entries) != 0 {
				t.Fatal("playback controls allowed an unwanted episode transition")
			}
		})
	}
}

func TestAutoNextEndFallbackRecordsCompletionOnceWhenNextEpisodeFails(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.autoNext = true
	a.player.Seek(time.Minute - 100*time.Millisecond)
	requests := 0
	a.title.session = &recoverySession{play: func(context.Context, string, catalog.Episode) (catalog.Playback, error) {
		requests++
		return catalog.Playback{}, errors.New("fixture resolution failure")
	}}
	now := time.Now()
	a.advanceEpisode(a.player, now)
	a.advanceEpisode(a.player, now.Add(3*time.Second))
	a.advanceEpisode(a.player, now.Add(6*time.Second))
	if requests != 1 || a.playFailure == nil || a.playFailure.episode.Name != "第2集" {
		t.Fatalf("failed transition was repeated or lost: requests=%d failure=%+v", requests, a.playFailure)
	}
	if len(a.history.entries) != 1 || !a.history.entries[0].finished() {
		t.Fatalf("failed next episode lost previous completion: %+v", a.history.entries)
	}
}

func TestAutoNextNaturalEndDoesNotWaitForFallback(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.autoNext = true
	a.player.Seek(a.player.Duration())
	if !a.player.Ended() {
		t.Fatal("fixture must report a natural end")
	}
	requests := 0
	a.title.session = &recoverySession{play: func(context.Context, string, catalog.Episode) (catalog.Playback, error) {
		requests++
		return catalog.Playback{}, errors.New("fixture resolution failure")
	}}
	a.advanceEpisode(a.player, time.Now())
	if requests != 1 {
		t.Fatal("a real end event did not immediately advance")
	}
}

func TestAutoNextWhenClockStopsJustBeforeDuration(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.autoNext = true
	a.setFullScreen(true)
	a.player.Close()
	decoder := &endTestVideo{}
	p, err := ui.NewVideoPlayer(decoder)
	if err != nil {
		t.Fatal(err)
	}
	a.player = p
	requests := 0
	a.title.session = &recoverySession{play: func(_ context.Context, line string, e catalog.Episode) (catalog.Playback, error) {
		requests++
		if line != "线路" || e.Name != "第2集" {
			t.Errorf("advanced to %s/%s", line, e.Name)
		}
		return catalog.Playback{URL: "https://example.com/next.mp4"}, nil
	}}
	completedBeforeNext := false
	a.open = func(string) (*ui.VideoPlayer, error) {
		completedBeforeNext = len(a.history.entries) == 1 && a.history.entries[0].Episode == "第1集" && a.history.entries[0].finished()
		decoder.retired = true
		return ui.NewVideoPlayer(&timedVideo{})
	}
	// Model AVPlayer's clock stopping 100 ms before the advertised duration
	// while MyGo still reports Playing and never reports Ended.
	tt := ui.NewTester(func(c *ui.Context) {
		if a.player == p {
			p.Seek(p.Duration() - 100*time.Millisecond)
		}
		a.view(c)
	}, 1120, 760)
	if p.Ended() || !p.Playing() {
		t.Fatal("fixture must stay playing without an end event")
	}
	tt.Advance(1900 * time.Millisecond)
	if requests != 0 {
		t.Fatal("advanced before confirming that the clock stopped")
	}
	tt.Advance(1100 * time.Millisecond)
	if requests != 1 || a.playingEpisode != "第2集" || a.player == p {
		t.Fatalf("stuck at the end: requests=%d episode=%s", requests, a.playingEpisode)
	}
	if !completedBeforeNext {
		t.Fatal("previous episode was not completed before opening the next one")
	}
	tt.Frame()
	if requests != 1 {
		t.Fatal("advanced more than once")
	}
}
