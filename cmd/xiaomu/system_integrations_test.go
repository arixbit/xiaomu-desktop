package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func openIntegrationVideo(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&timedVideo{}) }

func TestSystemIntegrationDeepLinkMustNotOverwriteEpisodeHistory(t *testing.T) {
	a := newPlayerTestApp(t)
	a.open = openIntegrationVideo
	a.history.note(historyEntry{Name: "示例片", Line: "线路", Episode: "第1集", Position: 35, Duration: 60})
	a.handleDeepLink("xiaomu://play?url=" + url.QueryEscape("https://example.com/unrelated.mp4"))
	a.recordHistory(a.player, time.Now())
	if !a.playingDirect || a.hasEpisodes() || len(a.history.entries) != 1 || a.history.entries[0].Position != 35 {
		t.Fatalf("unrelated URL treated as episode: direct=%v hasEpisodes=%v history=%+v", a.playingDirect, a.hasEpisodes(), a.history.entries)
	}
}

func TestSystemIntegrationNowPlayingMustFollowNaturalEnd(t *testing.T) {
	a := newKeyboardTestApp(t)
	fake := &recordingNowPlaying{}
	a.nowPlaying = fake
	a.syncNowPlaying()
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Advance(61 * time.Second)
	tt.Frame()
	if !a.player.Ended() {
		t.Fatal("fixture did not end")
	}
	if fake.info.State != mygo.PlaybackStopped || !fake.showing {
		t.Fatal("video ended but system state is still playing; toggle is routed as pause")
	}
	a.handleMediaCommand(mygo.MediaPlay, 0)
	if !a.player.Playing() || fake.info.State != mygo.PlaybackPlaying {
		t.Fatal("media play did not restart the ended video")
	}
}

func TestSystemIntegrationNowPlayingMustFollowSpeedButton(t *testing.T) {
	a := newKeyboardTestApp(t)
	fake := &recordingNowPlaying{}
	a.nowPlaying = fake
	a.syncNowPlaying()
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("播放速度"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("1.5×"); err != nil {
		t.Fatal(err)
	}
	published := fake.info.Rate
	if a.player.Rate() != 1.5 || published != a.player.Rate() {
		t.Fatalf("actual rate=%v, published rate=%v", a.player.Rate(), published)
	}
}

func TestSystemIntegrationNowPlayingMustPublishSelectedEpisode(t *testing.T) {
	a := newPlayerTestApp(t)
	a.open = openIntegrationVideo
	fake := &recordingNowPlaying{}
	a.nowPlaying = fake
	a.startEpisode(catalog.Playback{URL: "https://example.com/two.mp4"}, "线路", catalog.Episode{Name: "第2集", ID: "two"}, 0)
	if len(fake.sets) == 0 {
		t.Fatal("no publication")
	}
	last := fake.sets[len(fake.sets)-1]
	if last.Album != a.playingEpisode {
		t.Fatalf("playing episode=%q, published episode=%q", a.playingEpisode, last.Album)
	}
}

func TestSystemIntegrationNowPlayingMustPublishLiveChannel(t *testing.T) {
	fake := &recordingNowPlaying{}
	a := &app{open: openIntegrationVideo, nowPlaying: fake}
	t.Cleanup(a.close)
	a.live.channels = []iptv.Channel{{Name: "测试频道", Streams: []iptv.Stream{{URL: "https://example.com/live.m3u8"}}}}
	a.playLiveChannel(0, 0)
	if len(fake.sets) == 0 {
		t.Fatal("no publication")
	}
	if got := fake.sets[len(fake.sets)-1].Title; got != "测试频道" {
		t.Fatalf("live channel published as %q", got)
	}
}

func TestSystemIntegrationNotificationMustRevealDetailDuringPlayback(t *testing.T) {
	a, _, _ := followingFixture(t)
	vp, err := openIntegrationVideo("")
	if err != nil {
		t.Fatal(err)
	}
	a.player = wrapSystemPlayer(vp)
	a.handleNotificationClick(followNotificationID("示例剧"))
	if a.player != nil || a.route() != routeDetail {
		t.Fatalf("notification target still covered: player=%v route=%v title=%v", a.player != nil, a.route(), a.title.name)
	}
}

func TestSystemIntegrationColdStartSearchMustSurviveConfigRestore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"sites": []any{map[string]any{"key": "unsupported", "name": "示例", "type": 3, "api": "csp_Example"}}})
	}))
	t.Cleanup(srv.Close)
	a := &app{}
	t.Cleanup(a.close)
	queued := make(chan func(), 8)
	a.deferUntilReady(func() { a.handleDeepLink("xiaomu://search?wd=" + url.QueryEscape("示例剧")) })
	a.window = &mygo.Window{}
	a.dispatch = func(fn func()) { queued <- fn }
	a.readConfig(srv.URL, true)
	var finish func()
	select {
	case finish = <-queued:
	case <-time.After(3 * time.Second):
		t.Fatal("config did not return")
	}
	a.dispatch = nil
	a.drainPending()
	before := a.status
	finish()
	if a.route() != routeSearch || a.library.search.query != "示例剧" {
		t.Fatalf("launch search lost: before=%q rootLoaded=%v route=%v search=%q", before, a.library.root != nil, a.route(), a.library.search.query)
	}
}

func TestSystemIntegrationColdStartNotificationAndCanceledActions(t *testing.T) {
	for _, action := range []string{"notification", "file", "failure", "cancel"} {
		t.Run(action, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if action == "failure" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"sites": []any{map[string]any{"key": "unsupported", "name": "示例", "type": 3, "api": "csp_Example"}}})
			}))
			t.Cleanup(srv.Close)
			a := &app{openLocal: openIntegrationVideo}
			t.Cleanup(a.close)
			a.favorites.entries = []favoriteEntry{{Name: "示例剧"}}
			queued := make(chan func(), 8)
			a.dispatch = func(fn func()) { queued <- fn }
			a.readConfig(srv.URL, true)
			var finish func()
			select {
			case finish = <-queued:
			case <-time.After(3 * time.Second):
				t.Fatal("config did not return")
			}
			a.dispatch = nil
			a.handleNotificationClick(followNotificationID("示例剧"))
			if a.title != nil {
				t.Fatal("notification navigated before config restore")
			}
			switch action {
			case "file":
				if !a.openLocalFile("/tmp/example.mp4") {
					t.Fatal(a.status)
				}
			case "cancel":
				a.cancelJobs()
			}
			finish()
			if a.pendingLibrary != nil {
				t.Fatal("pending navigation was not consumed")
			}
			if action == "notification" {
				if a.title == nil || a.title.name != "示例剧" || a.route() != routeDetail {
					t.Fatalf("cold notification lost: title=%v route=%v", a.title, a.route())
				}
			} else if a.title != nil {
				t.Fatal("obsolete notification navigated after another action")
			}
			if action == "file" && (a.player == nil || !a.playingFile) {
				t.Fatal("config restore interrupted local playback")
			}
			if action == "failure" && !strings.Contains(a.status, "加载配置失败") {
				t.Fatalf("lost config failure: %q", a.status)
			}
		})
	}
}

func TestSystemIntegrationFailedOpenClearsNowPlaying(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "link", true: "file"}[local], func(t *testing.T) {
			a := newPlayerTestApp(t)
			fake := &recordingNowPlaying{}
			a.nowPlaying = fake
			a.syncNowPlaying()
			fail := func(string) (*ui.VideoPlayer, error) { return nil, errors.New("open failed") }
			a.open, a.openLocal = fail, fail
			if local {
				a.openLocalFile("/tmp/example.mp4")
			} else {
				a.handleDeepLink("xiaomu://play?url=" + url.QueryEscape("https://example.com/direct.mp4"))
			}
			if a.player != nil || fake.showing {
				t.Fatal("failed open left stale system playback")
			}
		})
	}
}

func TestSystemIntegrationNowPlayingTracksSeekWithoutPublishingEveryFrame(t *testing.T) {
	a := newKeyboardTestApp(t)
	fake := &recordingNowPlaying{}
	a.nowPlaying = fake
	a.player.Pause()
	a.syncNowPlaying()
	tt := ui.NewTester(a.view, 1120, 760)
	for range 5 {
		tt.Frame()
	}
	if len(fake.sets) != 1 || len(fake.states) != 0 {
		t.Fatal("unchanged frames repeatedly published system playback")
	}
	// A seek can finish after the caller already published its old position.
	a.player.Seek(25 * time.Second)
	tt.Frame()
	if fake.info.Position != 25*time.Second || fake.info.State != mygo.PlaybackPaused {
		t.Fatalf("seek did not reach system playback: %+v", fake.info)
	}
}

func TestSystemIntegrationSystemEventsRequestWindowUpdate(t *testing.T) {
	var queued []func()
	a := &app{window: &mygo.Window{}, dispatch: func(fn func()) { queued = append(queued, fn) }}
	called := false
	a.deferUntilReady(func() { called = true })
	if called || len(queued) != 1 {
		t.Fatal("system event bypassed the window update")
	}
	queued[0]()
	if !called {
		t.Fatal("window update did not run the event")
	}
	called = false
	a.deferUntilReady(func() { called = true })
	a.close()
	queued[1]()
	if called {
		t.Fatal("system event ran after closing the window")
	}
}

func TestSystemIntegrationExternalPlaybackMustCancelPendingEpisode(t *testing.T) {
	for _, kind := range []string{"file", "link"} {
		t.Run(kind, func(t *testing.T) {
			a := newPlayerTestApp(t)
			a.open, a.openLocal = openIntegrationVideo, openIntegrationVideo
			queued := make(chan func(), 2)
			a.dispatch = func(fn func()) { queued <- fn }
			a.run(jobPlay, "解析分集", func(context.Context) (jobResult, error) {
				return jobResult{apply: func() {
					a.startEpisode(catalog.Playback{URL: "https://example.com/old-episode.mp4"}, "线路", catalog.Episode{Name: "第2集", ID: "two"}, 0)
				}}, nil
			}, nil)
			var finish func()
			select {
			case finish = <-queued:
			case <-time.After(3 * time.Second):
				t.Fatal("episode did not resolve")
			}
			if kind == "file" {
				a.openLocalFile("/tmp/example.mp4")
			} else {
				a.handleDeepLink("xiaomu://play?url=" + url.QueryEscape("https://example.com/direct.mp4"))
			}
			intended := a.player
			finish()
			if a.player != intended {
				t.Fatalf("late episode replaced requested %s: episode=%q file=%v", kind, a.playingEpisode, a.playingFile)
			}
		})
	}
}
