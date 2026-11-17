package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

func followingFixture(t *testing.T) (*app, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	count, fail := &atomic.Int32{}, &atomic.Bool{}
	count.Store(3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var episodes []string
		for i := 1; i <= int(count.Load()); i++ {
			episodes = append(episodes, fmt.Sprintf("第%d集$https://example.com/%d.mp4", i, i))
		}
		fmt.Fprintf(w, `{"list":[{"vod_id":7,"vod_name":"示例剧","vod_play_from":"线路","vod_play_url":%q}]}`, strings.Join(episodes, "#"))
	}))
	t.Cleanup(srv.Close)
	doc := &config.Config{URL: "https://example.com/config", Sites: []config.Site{{Key: "site", Name: "示例来源", Type: 1, Kind: config.KindJSON, API: srv.URL}}}
	item := library.Item{Entry: library.Entry{Config: doc, Site: doc.Sites[0]}, Video: catalog.Video{ID: "7", Name: "示例剧"}}
	a := &app{favorites: favoriteStore{path: filepath.Join(t.TempDir(), "favorites.json")}}
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}
	a.favorites.entries = []favoriteEntry{{Name: "示例剧", ConfigURL: doc.URL, EntryKey: item.Entry.Key(), SiteKey: "site", VideoID: "7", Added: time.Now().Add(-time.Hour)}}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&timedVideo{}) }
	t.Cleanup(a.close)
	return a, count, fail
}

func TestFollowingRefreshPersistsUpdatesAndPreservesKnownEpisodesOnFailure(t *testing.T) {
	a, count, fail := followingFixture(t)
	a.history.note(historyEntry{Name: "示例剧", Episode: "第2集", Position: 60, Duration: 60})
	a.refreshFollowing()
	e := a.favorites.entries[0]
	if e.EpisodeCount != 3 || e.LatestEpisode != "第3集" || !a.followingHasNew(e) || a.followingProgress(e) != "看到第2集／更新到第3集" || a.followingAction(e) != "看下一集" {
		t.Fatalf("following metadata/actions: %+v progress=%s action=%s", e, a.followingProgress(e), a.followingAction(e))
	}
	count.Store(4)
	a.refreshFollowing()
	entries, err := readFavorites(a.favorites.path)
	if err != nil || len(entries) != 1 || entries[0].EpisodeCount != 4 {
		t.Fatalf("refreshed episodes did not persist: %+v, %v", entries, err)
	}
	fail.Store(true)
	a.refreshFollowing()
	if a.favorites.entries[0].EpisodeCount != 4 || a.favorites.refreshErrors[titleKey(e.Name)] == "" || a.favorites.refreshing {
		t.Fatalf("failure erased metadata or stayed loading: %+v", a.favorites)
	}
	fail.Store(false)
	count.Store(0)
	a.refreshFollowing()
	if a.favorites.entries[0].EpisodeCount != 4 {
		t.Fatal("temporary empty episode list erased known updates")
	}
}

func TestFollowingOneClickNextAndResume(t *testing.T) {
	for _, finished := range []bool{false, true} {
		t.Run(fmt.Sprint(finished), func(t *testing.T) {
			a, _, _ := followingFixture(t)
			e := a.favorites.entries[0]
			h := historyEntry{Name: e.Name, ConfigURL: e.ConfigURL, EntryKey: e.EntryKey, SiteKey: e.SiteKey, VideoID: e.VideoID, Line: "线路", Episode: "第2集", Position: 25, Duration: 60}
			if finished {
				h.Position = h.Duration
			}
			a.history.note(h)
			a.refreshFollowing()
			a.nav = []routeKind{routeFavorites}
			tt := ui.NewTester(a.view, 640, 420)
			if err := tt.Click("打开追剧 示例剧"); err != nil {
				t.Fatal(err)
			}
			want := "第2集"
			if finished {
				want = "第3集"
			}
			if a.player == nil || a.playingEpisode != want {
				t.Fatalf("following played %s, want %s; status=%s", a.playingEpisode, want, a.status)
			}
			if !finished && a.playbackPosition() < 25*time.Second {
				t.Fatal("resume position was lost")
			}
		})
	}
}

func TestFollowingPointNeverPlaysAnotherEpisodeWhenMissingOrCaughtUp(t *testing.T) {
	detail := catalog.Detail{Sources: []catalog.Source{{Name: "新线路", Episodes: []catalog.Episode{{Name: "01"}, {Name: "02"}, {Name: "03"}}}}}
	line, e, _, ok := followingResumePoint(detail, historyEntry{Line: "旧线路", Episode: "第2集", Position: 60, Duration: 60}, true)
	if !ok || line != 0 || e.Name != "03" {
		t.Fatalf("renamed line next episode = %+v %v", e, ok)
	}
	for _, name := range []string{"第3集", "第8集"} {
		if _, e, _, ok := followingResumePoint(detail, historyEntry{Episode: name, Position: 60, Duration: 60}, true); ok {
			t.Fatalf("missing/caught-up history played %+v", e)
		}
	}
}

func TestFollowingRefreshCanceledResultCannotRestoreRemovedTitle(t *testing.T) {
	a, _, _ := followingFixture(t)
	queued := make(chan func(), 4)
	a.dispatch = func(fn func()) { queued <- fn }
	a.refreshFollowing()
	var result func()
	select {
	case result = <-queued:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not return")
	}
	a.cancelFollowingRefresh()
	a.removeFavorite("示例剧")
	result()
	if len(a.favorites.entries) != 0 || a.favorites.refreshing {
		t.Fatal("stale refresh restored a removed title")
	}
}

func TestHomeSectionsRemainScrollableInSmallWindowAndAfterSourceRemoval(t *testing.T) {
	a, _, _ := followingFixture(t)
	a.history.note(historyEntry{Name: "示例剧", Episode: "第2集", ConfigURL: a.library.root.URL})
	tt := ui.NewTester(a.view, 640, 420)
	page, ok := tt.Find("首页内容")
	if !ok {
		t.Fatal("home scroll missing")
	}
	recommendation, ok := tt.Find("首页推荐")
	if !ok {
		t.Fatal("recommendation viewport missing")
	}
	tt.Scroll(page.X+page.W/2, page.Y+20, 0, 600)
	revealed, _ := tt.Find("首页推荐")
	if revealed.Y >= recommendation.Y || revealed.H < 100 {
		t.Fatalf("home cannot scroll: before=%+v after=%+v", recommendation, revealed)
	}
	a.library.root = nil
	tt.Frame()
	if !tt.HasText("添加片源，继续观看") || !tt.HasText("我的追剧") || !tt.HasText("继续观看") {
		t.Fatalf("source removal hid shortcuts: %v", tt.Texts())
	}
}
