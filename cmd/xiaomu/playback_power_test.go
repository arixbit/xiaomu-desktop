package main

import (
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type powerRecorder struct {
	active, acquired, released int
}

func recordPlaybackPower(t *testing.T, a *app) *powerRecorder {
	t.Helper()
	r := &powerRecorder{}
	a.playbackPower.keepAwake = func(reason string, display bool) func() {
		if !display || reason == "" {
			t.Error("video playback must inhibit both display and system idle sleep with a reason")
		}
		r.active++
		r.acquired++
		return func() {
			r.active--
			r.released++
			if r.active < 0 {
				t.Error("released playback power more than once")
			}
		}
	}
	return r
}

func TestPlaybackPowerFollowsPlaybackAndMediaKeys(t *testing.T) {
	a := newKeyboardTestApp(t)
	r := recordPlaybackPower(t, a)
	tt := ui.NewTester(a.view, 1120, 760)
	if r.active != 1 {
		t.Fatal("playing video did not inhibit idle display and system sleep")
	}
	for range 5 {
		tt.Frame()
	}
	if r.acquired != 1 {
		t.Fatal("each frame acquired another power assertion")
	}
	a.handleMediaCommand(mygo.MediaPause, 0)
	if r.active != 0 || r.released != 1 {
		t.Fatal("media-key pause did not immediately allow sleep")
	}
	a.handleMediaCommand(mygo.MediaPlay, 0)
	if r.active != 1 || r.acquired != 2 {
		t.Fatal("media-key resume did not restore keep-awake")
	}
	a.setFullScreen(true)
	tt.Move(500, 300)
	tt.Advance(4 * time.Second)
	if tt.HasText("音量") || r.active != 1 {
		t.Fatal("hiding fullscreen controls released playback keep-awake")
	}
	if err := tt.Click("视频画面"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeySpace)
	if r.active != 0 {
		t.Fatal("keyboard pause did not allow sleep")
	}
}

func TestPlaybackPowerReleasesWhenPlaybackStops(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*app)
	}{
		{"natural end", func(a *app) { a.player.Seek(a.player.Duration()) }},
		{"pause", func(a *app) { a.player.Pause() }},
		{"failure", func(a *app) { a.playFailure = &episodeFailure{err: errors.New("fixture failure")} }},
		{"leave player", func(a *app) { a.leavePlayer() }},
		{"close window", func(a *app) { a.close() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newKeyboardTestApp(t)
			r := recordPlaybackPower(t, a)
			tt := ui.NewTester(a.view, 1120, 760)
			if r.active != 1 {
				t.Fatal("no keep-awake during playback")
			}
			tc.stop(a)
			tt.Frame()
			tt.Frame()
			if r.active != 0 || r.released != 1 {
				t.Fatalf("stopped playback leaked an assertion: %+v", r)
			}
		})
	}
}

func TestPlaybackPowerSupportsAllMediaAndUnknownDuration(t *testing.T) {
	for _, mode := range []string{"episode", "live", "direct", "file"} {
		t.Run(mode, func(t *testing.T) {
			a := newPlayerTestApp(t) // Duration is unknown, as for a live stream.
			a.playingLive = mode == "live"
			a.playingDirect = mode == "direct" || mode == "file"
			a.playingFile = mode == "file"
			r := recordPlaybackPower(t, a)
			a.syncNowPlaying() // Power works even without a Now Playing sink.
			if r.active != 1 {
				t.Fatal("playing media did not keep the computer awake")
			}
			a.close()
			if r.active != 0 {
				t.Fatal("closing the app did not release keep-awake")
			}
		})
	}
}

func TestPlaybackPowerSurvivesSwitchAndReleasesOnOpenFailure(t *testing.T) {
	a := newKeyboardTestApp(t)
	r := recordPlaybackPower(t, a)
	a.syncNowPlaying()
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&timedVideo{}) }
	if !a.playDirectURL("https://example.com/video.mp4", nil) {
		t.Fatal(a.status)
	}
	if r.active != 1 || r.acquired != 1 {
		t.Fatal("switching active media duplicated or dropped keep-awake")
	}
	a.open = func(string) (*ui.VideoPlayer, error) { return nil, errors.New("fixture open failure") }
	if a.playDirectURL("https://example.com/broken.mp4", nil) || r.active != 0 {
		t.Fatal("failed media replacement leaked keep-awake")
	}
}

func TestPlaybackPowerReleasesAtHLSClockGapAndResetsAfterSeek(t *testing.T) {
	a := newKeyboardTestApp(t) // Auto-next is off.
	a.playingEpisode = "第2集"   // The last episode must release too.
	a.player.Seek(time.Minute - 100*time.Millisecond)
	r := recordPlaybackPower(t, a)
	now := time.Now()
	a.syncPlaybackPower(now)
	if r.active != 1 {
		t.Fatal("near the end is still playing until its clock stops")
	}
	a.syncPlaybackPower(now.Add(3 * time.Second))
	a.syncPlaybackPower(now.Add(4 * time.Second))
	if r.active != 0 || r.acquired != 1 || r.released != 1 {
		t.Fatal("missing native end event left the computer awake or reacquired the assertion")
	}
	a.player.Seek(20 * time.Second)
	a.syncPlaybackPower(now.Add(5 * time.Second))
	if r.active != 1 || r.acquired != 2 {
		t.Fatal("seeking back into the video did not restore keep-awake")
	}
}
