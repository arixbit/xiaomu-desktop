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
	if err := tt.Click("加入想看"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("已收藏 · 取消") || len(a.history.entries) != 0 {
		t.Fatal("saving an unwatched title should not add history")
	}
	if err := tt.Click("收藏"); err != nil {
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
	if err := bt.Click("打开收藏 示例片"); err != nil {
		t.Fatal(err)
	}
	if b.title == nil || b.title.detail == nil || b.route() != routeDetail || searches.Load() != 0 {
		t.Fatal("saved site was not reopened directly")
	}
	if !bt.HasText("已收藏 · 取消") {
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
	if b.title != nil || b.status != "请先添加片源，再打开收藏的影片" {
		t.Fatal("favorite opened a source that is no longer imported")
	}
	b.switchTab(routeFavorites)
	bt.Frame()
	if err := bt.Click("移除收藏 示例片"); err != nil {
		t.Fatal(err)
	}
	if !bt.HasText("还没有收藏") {
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
	if err := tt.Click("加入想看"); err != nil {
		t.Fatal(err)
	}
	a.switchTab(routeHome)
	tt.Frame()
	if !tt.HasText("我的收藏") || !tt.HasText("示例片") {
		t.Fatalf("favorites strip missing on home: %v", tt.Texts())
	}
	if err := tt.Click("打开收藏 示例片"); err != nil {
		t.Fatal(err)
	}
	if a.route() != routeDetail || a.title == nil || a.title.detail == nil {
		t.Fatalf("strip did not open the saved title: route=%v title=%+v", a.route(), a.title)
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("全部收藏"); err != nil {
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
	if tt.HasText("我的收藏") {
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
	if len(a.favorites.entries) != 0 || !strings.Contains(a.status, "保存收藏失败") {
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
