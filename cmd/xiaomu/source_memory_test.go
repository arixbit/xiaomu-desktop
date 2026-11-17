package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

func TestSourceAttemptRequiresPlaybackProgressAndCountsFailureOnce(t *testing.T) {
	var attempt sourcePlaybackAttempt
	for _, sample := range []struct {
		ready, buffering, playing bool
		position                  time.Duration
	}{
		{false, true, true, 0},
		{true, true, true, 0},
		{true, false, false, 0},
		{true, false, true, 0},
		{true, false, true, 90 * time.Millisecond},
	} {
		if success, failure := attempt.observe(sample.ready, sample.buffering, sample.playing, sample.position, nil); success || failure {
			t.Fatal("ready, buffering, pause or insignificant progress counted as successful playback")
		}
	}
	if success, failure := attempt.observe(true, false, true, 150*time.Millisecond, nil); !success || failure {
		t.Fatal("actual progress was not recorded")
	}
	if success, _ := attempt.observe(true, false, true, time.Second, nil); success {
		t.Fatal("success counted twice")
	}
	for i := range 2 {
		if success, failure := attempt.observe(false, true, true, time.Second, errors.New("playback failed")); success || failure != (i == 0) {
			t.Fatal("failure was missed or counted repeatedly")
		}
	}
}

func TestSourceMemoryPersistsSuccessfulPreferenceAndFailureHistory(t *testing.T) {
	a, seeds := newDetailTestApp(t, 3)
	path := filepath.Join(t.TempDir(), "source-playback.json")
	a.sourceMemory.path = path
	now := time.Now()
	if err := a.sourceMemory.record(a.title.name, seeds[1], "备用线路", 2*time.Second, true, now); err != nil {
		t.Fatal(err)
	}
	if err := a.sourceMemory.record(a.title.name, seeds[0], "主线路", 0, false, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := a.sourceMemory.record(a.title.name, seeds[1], "备用线路", 0, false, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	entries, err := readSourceMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	a.sourceMemory.entries = entries
	_ = a.latency.record(seeds[0].Entry.Key(), time.Millisecond)
	_ = a.latency.record(seeds[1].Entry.Key(), 10*time.Second)
	if got := a.candidates(); !entries[titleKey(a.title.name)].Preferred.matches(got[0]) || got[0].Video.ID != seeds[1].Video.ID {
		t.Fatalf("last successful source did not take priority: %+v", got)
	}
	r := entries[titleKey(a.title.name)].Records[itemKey(seeds[1])]
	if r.Successes != 1 || r.Failures != 1 || r.StartMS != 2000 || r.ConsecutiveFailures != 1 {
		t.Fatalf("playback observations = %+v", r)
	}
	detail := catalog.Detail{Sources: []catalog.Source{{Name: "主线路"}, {Name: "备用线路"}}}
	if a.preferredLine(a.title.name, seeds[1], detail) != 1 || a.preferredLine(a.title.name, seeds[0], detail) != 0 {
		t.Fatal("play line was lost or applied to a different source")
	}
	// A failure reading the remembered source can still fall back this visit.
	a.title.failed[itemKey(seeds[1])] = "gone"
	if got := a.candidates(); itemKey(got[len(got)-1]) != itemKey(seeds[1]) {
		t.Fatal("unavailable remembered source was not placed last")
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	a.sourceMemory.entries, a.sourceMemory.readErr = readSourceMemory(path)
	if a.sourceMemory.record(a.title.name, seeds[2], "线路", 0, true, now) == nil {
		t.Fatal("corrupt source record was overwritten")
	}
}

func TestAutomaticSourceSelectionWaitsForRememberedSearchResult(t *testing.T) {
	a, seeds := newDetailTestApp(t, 2)
	a.title.auto = true
	a.title.detail = nil
	a.title.seeds = seeds[:1]
	_ = a.sourceMemory.record(a.title.name, seeds[1], "线路", time.Second, true, time.Now())
	a.library.root = seeds[1].Entry.Config
	a.library.search.query, a.library.search.running = a.title.name, true
	a.autoOpenTitle()
	if a.title.detail != nil {
		t.Fatal("a faster response bypassed the remembered source")
	}
	a.library.search.items = []library.Item{seeds[1]}
	a.autoOpenTitle()
	if a.title.detail == nil || itemKey(a.title.item) != itemKey(seeds[1]) {
		t.Fatalf("remembered source did not open: %+v status=%s", a.title.item, a.status)
	}
}

func TestResumePrefersSuccessfulSourceAndFallsBackWithoutChangingEpisode(t *testing.T) {
	for _, tc := range []struct {
		name, preferredName, episodes, wantSite, wantEpisode string
		finished                                             bool
	}{
		{"matching numbered episode", "示例剧", "01$https://example.com/1.mp4#02$https://example.com/2.mp4#03$https://example.com/3.mp4", "preferred", "02", false},
		{"next episode", "示例剧", "01$https://example.com/1.mp4#02$https://example.com/2.mp4#03$https://example.com/3.mp4", "preferred", "03", true},
		{"missing episode", "示例剧", "01$https://example.com/1.mp4", "site", "第2集", false},
		{"different title", "另一部剧", "02$https://example.com/2.mp4", "site", "第2集", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := followingFixture(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"list":[{"vod_id":8,"vod_name":%q,"vod_play_from":"好用线路","vod_play_url":%q}]}`, tc.preferredName, tc.episodes)
			}))
			defer srv.Close()
			doc := a.library.root
			doc.Sites = append(doc.Sites, config.Site{Key: "preferred", Type: 1, Kind: config.KindJSON, API: srv.URL})
			item := library.Item{Entry: library.Entry{Config: doc, Site: doc.Sites[1]}, Video: catalog.Video{ID: "8", Name: "示例剧"}}
			_ = a.sourceMemory.record("示例剧", item, "好用线路", time.Second, true, time.Now())
			e := a.favorites.entries[0]
			h := historyEntry{Name: e.Name, ConfigURL: e.ConfigURL, EntryKey: e.EntryKey, SiteKey: e.SiteKey, VideoID: e.VideoID, Line: "线路", Episode: "第2集", Position: 25, Duration: 60}
			if tc.finished {
				h.Position = h.Duration
			}
			a.resume(h)
			if a.player == nil || a.title.item.Entry.Site.Key != tc.wantSite || a.playingEpisode != tc.wantEpisode {
				t.Fatalf("resume target site=%v episode=%s status=%s", a.title, a.playingEpisode, a.status)
			}
			if !tc.finished && a.resumeAt != 25*time.Second {
				t.Fatal("preferred fallback lost resume time")
			}
		})
	}
}

func TestPendingFollowingPlaybackCanceledOnTabChange(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	doc := &config.Config{URL: "https://example.com/config", Sites: []config.Site{{Key: "a", Type: 1, Kind: config.KindJSON, API: srv.URL}}}
	a := &app{}
	defer a.close()
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}
	a.favorites.entries = []favoriteEntry{{Name: "示例剧", ConfigURL: doc.URL, SiteKey: "a", VideoID: "7"}}
	queued := make(chan func(), 1)
	a.dispatch = func(fn func()) { queued <- fn }
	a.open = func(string) (*ui.VideoPlayer, error) { t.Error("canceled playback opened player"); return nil, nil }
	a.openFollowing(a.favorites.entries[0])
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("playback detail did not start")
	}
	a.switchTab(routeHistory)
	select {
	case fn := <-queued:
		fn()
	case <-time.After(3 * time.Second):
		t.Fatal("tab change did not cancel playback")
	}
	if a.title != nil || a.route() != routeHistory {
		t.Fatal("stale playback result replaced the selected page")
	}
}
