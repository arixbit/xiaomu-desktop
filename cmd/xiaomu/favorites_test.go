package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestFavoritesUIRestartAndSourceChanges(t *testing.T) {
	var oldGone atomic.Bool
	var searches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("ids") == "7" && oldGone.Load():
			http.Error(w, "gone", http.StatusGone)
		case q.Get("ids") != "":
			fmt.Fprint(w, `{"list":[{"vod_id":7,"vod_name":"示例片","vod_play_from":"线路","vod_play_url":"第1集$https://example.com/one.mp4"}]}`)
		case q.Get("wd") != "":
			searches.Add(1)
			fmt.Fprint(w, `{"list":[{"vod_id":8,"vod_name":"示例片"}]}`)
		default:
			fmt.Fprint(w, `{"list":[]}`)
		}
	}))
	defer srv.Close()
	doc := &config.Config{URL: srv.URL + "/config", Sites: []config.Site{{Key: "site", Name: "示例来源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/api"}}}
	path := filepath.Join(t.TempDir(), "favorites.json")
	a := &app{favorites: favoriteStore{path: path}}
	defer a.close()
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{}}
	a.openTitle("示例片", "", []library.Item{{Entry: library.Entry{Config: doc, Site: doc.Sites[0]}, Video: catalog.Video{ID: "7", Name: "示例片"}}})
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("加入追剧"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("已追剧 · 取消") || len(a.history.entries) != 0 {
		t.Fatal("saving an unwatched title should not add history")
	}
	if err := tt.Click("追剧"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("示例片") || a.route() != routeFavorites {
		t.Fatalf("favorite not listed: %v", tt.Texts())
	}
	entries, err := readFavorites(path)
	if err != nil || len(entries) != 1 || entries[0].VideoID != "7" {
		t.Fatalf("saved entry: %+v, %v", entries, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "one.mp4") {
		t.Fatal("favorite persisted resolved media")
	}
	b := &app{favorites: favoriteStore{path: path, entries: entries}, nav: []routeKind{routeFavorites}}
	defer b.close()
	b.library = libraryBrowser{root: doc, cached: map[string]*config.Config{}}
	bt := ui.NewTester(b.view, 640, 420)
	if err := bt.Click("追剧详情 示例片"); err != nil {
		t.Fatal(err)
	}
	if b.title == nil || b.title.detail == nil || b.route() != routeDetail || searches.Load() != 0 {
		t.Fatal("saved site was not reopened directly")
	}
	if !bt.HasText("已追剧 · 取消") {
		t.Fatal("restart lost favorite membership")
	}
	// If the saved video disappeared, find it in the current library.
	oldGone.Store(true)
	b.switchTab(routeFavorites)
	b.openFavorite(entries[0])
	if b.title == nil || b.title.detail == nil || b.title.item.Video.ID != "8" || searches.Load() == 0 {
		t.Fatalf("did not recover a stale favorite via search: title=%+v status=%s", b.title, b.status)
	}
	b.removeConfig()
	if len(b.favorites.entries) != 1 {
		t.Fatal("removing the source removed the favorite")
	}
	b.openFavorite(entries[0])
	if b.title != nil || b.status != "请先添加片源，再打开追剧影片" {
		t.Fatal("favorite opened a source that is no longer imported")
	}
	b.switchTab(routeFavorites)
	bt.Frame()
	if err := bt.Click("移出追剧 示例片"); err != nil {
		t.Fatal(err)
	}
	if !bt.HasText("还没有追剧") {
		t.Fatal("favorite removal did not reach the empty state")
	}
	entries, err = readFavorites(path)
	if err != nil || len(entries) != 0 {
		t.Fatal("removal did not persist")
	}
}

func TestFavoritesStripOnHomeOpensSavedTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("ids") != "" {
			fmt.Fprint(w, `{"list":[{"vod_id":7,"vod_name":"示例片","vod_play_from":"线路","vod_play_url":"第1集$https://example.com/one.mp4"}]}`)
			return
		}
		fmt.Fprint(w, `{"list":[]}`)
	}))
	defer srv.Close()
	doc := &config.Config{URL: srv.URL + "/config", Sites: []config.Site{{Key: "site", Name: "示例来源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/api"}}}
	path := filepath.Join(t.TempDir(), "favorites.json")
	a := &app{favorites: favoriteStore{path: path}}
	defer a.close()
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{}}
	a.openTitle("示例片", "", []library.Item{{Entry: library.Entry{Config: doc, Site: doc.Sites[0]}, Video: catalog.Video{ID: "7", Name: "示例片"}}})
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("加入追剧"); err != nil {
		t.Fatal(err)
	}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&timedVideo{}) }
	a.switchTab(routeHome)
	tt.Frame()
	if !tt.HasText("我的追剧") || !tt.HasText("示例片") {
		t.Fatalf("favorites strip missing on home: %v", tt.Texts())
	}
	if err := tt.Click("打开追剧 示例片"); err != nil {
		t.Fatal(err)
	}
	if a.player == nil || a.title == nil || a.playingEpisode != "第1集" {
		t.Fatalf("strip did not play the saved title: episode=%s title=%+v", a.playingEpisode, a.title)
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("全部追剧"); err != nil {
		t.Fatal(err)
	}
	if a.route() != routeFavorites {
		t.Fatalf("strip did not lead to the favorites page: route=%v", a.route())
	}
}

func TestFavoritesStripHiddenWithoutFavorites(t *testing.T) {
	a := &app{}
	defer a.close()
	a.library = libraryBrowser{root: &config.Config{URL: "https://example.com/config"}}
	tt := ui.NewTester(a.view, 1120, 760)
	if tt.HasText("我的追剧") {
		t.Fatalf("empty favorites showed a strip: %v", tt.Texts())
	}
}

func TestFavoritesAllowSavingBeforeDetailAndPreserveFileOnError(t *testing.T) {
	a := &app{title: &titlePage{name: "示例片"}}
	a.toggleFavorite()
	if !a.favorites.contains("《示例片》") {
		t.Fatal("could not save a title before detail loaded")
	}
	a.title.name = " 《示例片》 "
	a.toggleFavorite()
	if len(a.favorites.entries) != 0 {
		t.Fatal("same title was duplicated instead of removed")
	}
	a.favorites.path = t.TempDir() // a directory cannot be replaced by a file
	a.toggleFavorite()
	if len(a.favorites.entries) != 0 || !strings.Contains(a.status, "保存追剧失败") {
		t.Fatal("failed save appeared successful")
	}
	path := filepath.Join(t.TempDir(), "favorites.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	a.favorites.path = path
	a.favorites.entries, a.favorites.readErr = readFavorites(path)
	a.toggleFavorite()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{" || a.favorites.readErr == nil {
		t.Fatal("a corrupt favorites file was overwritten")
	}
}

func TestOpeningFavoriteIsCanceledWhenChangingTabs(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	a := &app{}
	defer a.close()
	e := favoriteEntry{Name: "示例片", ConfigURL: srv.URL, VideoID: "7"}
	a.library.root = &config.Config{URL: "https://example.com/config", Lines: []config.Line{{URL: srv.URL}}}
	finished := make(chan func(), 1)
	a.dispatch = func(f func()) { finished <- f }
	a.openFavorite(e)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("favorite load did not start")
	}
	a.switchTab(routeHistory)
	select {
	case finish := <-finished:
		finish()
	case <-ctx.Done():
		t.Fatal("favorite load did not cancel")
	}
	if a.title != nil || a.route() != routeHistory {
		t.Fatal("late favorite result reopened the detail page")
	}
}

func TestFavoritesViewTwoColumnCardsNarrowFallbackAndUndoRemoval(t *testing.T) {
	now := time.Now()
	doc := &config.Config{URL: "https://example.com/config"}
	a := &app{nav: []routeKind{routeFavorites}}
	defer a.close()
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}
	a.history.note(historyEntry{Name: "剧一", ConfigURL: doc.URL, Episode: "第4集", Position: 600, Duration: 2400, Updated: now})
	a.favorites.entries = []favoriteEntry{
		{Name: "剧一", ConfigURL: doc.URL, EpisodeCount: 12, LatestEpisode: "第12集", Checked: now, Added: now},
		{Name: "剧二", ConfigURL: doc.URL, EpisodeCount: 24, LatestEpisode: "第24集", Checked: now, Added: now.Add(-time.Minute)},
		{Name: "剧三", ConfigURL: doc.URL, EpisodeCount: 16, LatestEpisode: "第16集", Checked: now, Added: now.Add(-2 * time.Minute)},
	}

	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("共追 3 部剧 · 进度与更新仅保存在本机") || !tt.HasText("有新集") {
		t.Fatalf("favorites header or badge missing: %v", tt.Texts())
	}
	if _, ok := tt.Find("追剧进度 剧一"); !ok {
		t.Fatal("watched favorite missing real progress bar")
	}
	if _, ok := tt.Find("追剧进度 剧二"); ok {
		t.Fatal("unwatched favorite must not fabricate a progress bar")
	}
	act1, ok1 := tt.Find("打开追剧 剧一")
	act2, ok2 := tt.Find("打开追剧 剧二")
	act3, ok3 := tt.Find("打开追剧 剧三")
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("missing action buttons: ok=(%v,%v,%v) texts=%v", ok1, ok2, ok3, tt.Texts())
	}
	// Wide layout arranges cards in 2 columns: 剧一 and 剧二 share row 1; 剧三 starts row 2 on the left.
	if act2.X <= act1.X+act1.W || act2.Y > act1.Y+20 || act1.Y > act2.Y+20 {
		t.Fatalf("wide favorites did not pair row 1 into two columns: act1=%+v act2=%+v", act1, act2)
	}
	if act3.Y <= act1.Y+act1.H || act3.X > act1.X+20 {
		t.Fatalf("third favorite card did not align to row 2 left column: act1=%+v act3=%+v", act1, act3)
	}

	// Narrow window (<760px) automatically falls back to a single column.
	tt.SetSize(640, 480)
	narrow1, _ := tt.Find("打开追剧 剧一")
	narrow2, _ := tt.Find("打开追剧 剧二")
	if narrow2.Y <= narrow1.Y+narrow1.H {
		t.Fatalf("narrow favorites did not collapse to single column: n1=%+v n2=%+v", narrow1, narrow2)
	}

	// Top-right × button removes the entry and exposes an undo toast that restores it.
	if err := tt.Click("移出追剧 剧二"); err != nil {
		t.Fatalf("click remove favorite: %v", err)
	}
	if len(a.favorites.entries) != 2 || a.favorites.contains("剧二") {
		t.Fatalf("remove favorite failed: %+v", a.favorites.entries)
	}
	if err := tt.Click("撤销"); err != nil {
		t.Fatalf("click undo toast: %v", err)
	}
	if len(a.favorites.entries) != 3 || !a.favorites.contains("剧二") {
		t.Fatalf("undo did not restore favorite: %+v", a.favorites.entries)
	}
}
