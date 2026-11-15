package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/source"
	"github.com/egoist/mygo/ui"
)

func TestPlaybackWaitOnlyTimesOutContinuousWaiting(t *testing.T) {
	now := time.Now()
	for _, ready := range []bool{false, true} {
		var w playbackWait
		if w.check(now, ready, true, true, 0) != nil || w.check(now.Add(29*time.Second), ready, true, true, 0) != nil {
			t.Fatal("timed out before 30 seconds")
		}
		if w.check(now.Add(30*time.Second), ready, true, true, 0) == nil {
			t.Fatal("an indefinitely waiting player had no recovery")
		}
		// Progress, a pause, and successful playback each break the wait.
		if w.check(now.Add(31*time.Second), ready, true, true, time.Second) != nil {
			t.Fatal("progress did not reset the buffer timer")
		}
		if w.check(now.Add(time.Minute), ready, true, false, time.Second) != nil {
			t.Fatal("a deliberate pause was treated as a failure")
		}
		if w.check(now.Add(2*time.Minute), true, false, true, time.Second) != nil {
			t.Fatal("playing video was treated as a failure")
		}
	}
}

type recoverySession struct {
	source.Session
	play func(context.Context, string, catalog.Episode) (catalog.Playback, error)
}

func (s *recoverySession) Play(ctx context.Context, line string, e catalog.Episode) (catalog.Playback, error) {
	return s.play(ctx, line, e)
}
func (*recoverySession) Close() {}

func TestFailedEpisodeCanRetryOrSwitchLineBeforePlayerOpens(t *testing.T) {
	a := newPlayerTestApp(t)
	a.player.Close()
	a.player = nil
	a.playingEpisode, a.playingSource = "", ""
	a.nav = []routeKind{routeHome, routeDetail}
	a.title.detail.Sources = append(a.title.detail.Sources, catalog.Source{Name: "备用", Episodes: []catalog.Episode{
		{Name: "01", ID: "https://example.com/one.mp4"},
		{Name: "02", ID: "https://example.com/two.mp4"},
	}})
	var requests []string
	a.title.session = &recoverySession{play: func(_ context.Context, line string, e catalog.Episode) (catalog.Playback, error) {
		requests = append(requests, line+"/"+e.Name)
		if line == "线路" {
			return catalog.Playback{}, errors.New("测试解析失败")
		}
		return catalog.Playback{URL: e.ID}, nil
	}}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&timedVideo{}) }
	a.playEpisodeAt("线路", a.title.detail.Sources[0].Episodes[1], 20*time.Second)
	tt := ui.NewTester(a.view, 1120, 760)
	if a.playFailure == nil || !tt.HasText("解析分集失败：测试解析失败") {
		t.Fatalf("failed request missing from detail: %v", tt.Texts())
	}
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[1] != "线路/第2集" {
		t.Fatalf("retried wrong episode: %v", requests)
	}
	if err := tt.Click("换下一条线路"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || requests[2] != "备用/02" || a.playFailure != nil || a.player == nil {
		t.Fatalf("did not switch to matching episode: %v failure=%v", requests, a.playFailure)
	}
	if a.playbackPosition() < 20*time.Second {
		t.Fatalf("lost resume position: %s", a.playbackPosition())
	}
}

func TestOpenFailureKeepsRecoveryTargetAndUnappliedResumePosition(t *testing.T) {
	a := newPlayerTestApp(t)
	a.resumeAt, a.lastPosition = 35*time.Second, 0
	a.title.session = &recoverySession{play: func(_ context.Context, _ string, e catalog.Episode) (catalog.Playback, error) {
		return catalog.Playback{URL: "https://example.com/video.mp4"}, nil
	}}
	a.open = func(string) (*ui.VideoPlayer, error) { return nil, errors.New("测试解码失败") }
	a.retryEpisode()
	if a.playFailure == nil || a.playFailure.at != 35*time.Second || a.playFailure.episode.Name != "第1集" {
		t.Fatalf("lost failed target: %+v", a.playFailure)
	}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&timedVideo{}) }
	a.retryEpisode()
	if a.playFailure != nil || a.resumeAt != 35*time.Second || a.player == nil {
		t.Fatal("a successful retry did not restore the pending seek")
	}
}

func TestCanceledEpisodeCannotRestoreFailureAfterLeaving(t *testing.T) {
	a := newPlayerTestApp(t)
	finished := make(chan func(), 1)
	a.dispatch = func(f func()) { finished <- f }
	a.title.session = &recoverySession{play: func(ctx context.Context, _ string, _ catalog.Episode) (catalog.Playback, error) {
		<-ctx.Done()
		return catalog.Playback{}, ctx.Err()
	}}
	a.playEpisode("线路", a.title.detail.Sources[0].Episodes[0])
	a.leavePlayer()
	select {
	case finish := <-finished:
		finish()
	case <-time.After(time.Second):
		t.Fatal("canceled playback did not finish")
	}
	if a.player != nil || a.playFailure != nil || a.title != nil {
		t.Fatal("a canceled request returned to the player")
	}
}

func TestLinePanelRecoversRequestedEpisodeInsteadOfPreviousVideo(t *testing.T) {
	a := newPlayerTestApp(t)
	a.panelOpen = true
	a.title.detail.Sources = append(a.title.detail.Sources, catalog.Source{Name: "备用", Episodes: []catalog.Episode{
		{Name: "01", ID: "https://example.com/one.mp4"},
		{Name: "02", ID: "https://example.com/two.mp4"},
	}})
	a.playFailure = &episodeFailure{line: "线路", episode: a.title.detail.Sources[0].Episodes[1], at: 25 * time.Second, err: errors.New("测试失败")}
	requested := ""
	a.title.session = &recoverySession{play: func(_ context.Context, line string, e catalog.Episode) (catalog.Playback, error) {
		requested = line + "/" + e.Name
		return catalog.Playback{URL: e.ID}, nil
	}}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&timedVideo{}) }
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("备用"); err != nil {
		t.Fatal(err)
	}
	if requested != "备用/02" || a.playbackPosition() < 25*time.Second || a.playFailure != nil {
		t.Fatalf("panel played %q at %s, failure=%v", requested, a.playbackPosition(), a.playFailure)
	}
}
