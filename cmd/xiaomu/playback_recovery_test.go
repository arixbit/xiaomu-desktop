package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
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

func TestEpisodeRetryRefreshesExpiredCMSURL(t *testing.T) {
	var detailReads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			if r.URL.Query().Get("ids") != "7" {
				t.Errorf("requested wrong title: %s", r.URL.RawQuery)
			}
			if detailReads.Add(1) == 1 {
				fmt.Fprint(w, `{"list":[{"vod_name":"示例片","vod_play_from":"线路","vod_play_url":"第15集$/expired.m3u8"}]}`)
				return
			}
			fmt.Fprint(w, `{"list":[{"vod_name":"示例片","vod_play_from":"备用$$$线路","vod_play_url":"第15集$/other.m3u8$$$第16集$/wrong.m3u8#15$/fresh.m3u8"}]}`)
		case "/fresh.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\nsegment.ts\n#EXT-X-ENDLIST\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	item := library.Item{
		Entry: library.Entry{Site: config.Site{Kind: config.KindJSON, API: upstream.URL + "/api"}},
		Video: catalog.Video{ID: "7", Name: "示例片"},
	}
	session, detail, err := openItem(t.Context(), item, "")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{nav: []routeKind{routeHome, routeDetail}, title: &titlePage{name: "示例片", item: item, session: session, detail: &detail}}
	defer a.close()
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		resp, err := http.Get(raw)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, errors.New(resp.Status)
		}
		return ui.NewVideoPlayer(&timedVideo{})
	}
	at := 25 * time.Second
	a.playEpisodeAt("线路", detail.Sources[0].Episodes[0], at)
	tt := ui.NewTester(a.view, 1120, 760)
	if a.playFailure == nil || !tt.HasText("无法打开: 404 Not Found") {
		t.Fatalf("expected an expired playback address: %v", tt.Texts())
	}
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	if detailReads.Load() != 2 || a.playFailure != nil || a.player == nil {
		t.Fatalf("retry did not open a refreshed address: detail reads=%d failure=%+v", detailReads.Load(), a.playFailure)
	}
	line, episode, ok := a.currentEpisode()
	if !ok || line != 1 || a.title.detail.Sources[line].Episodes[episode].Name != "15" || a.playbackPosition() < at {
		t.Fatalf("retry lost the current episode or progress: line=%d episode=%d position=%s", line, episode, a.playbackPosition())
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
