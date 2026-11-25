package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	mpvplayer "github.com/arixbit/mygo-player"
	"github.com/egoist/mygo/ui"
)

func TestDepotFailedDocsCooldownAndReloadRecovery(t *testing.T) {
	var failLine atomic.Bool
	failLine.Store(true)
	var badHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad.json":
			badHits.Add(1)
			if failLine.Load() {
				http.Error(w, "gone", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"sites":[{"key":"ok","name":"恢复站","type":1,"api":"/vod"}]}`))
		case "/vod":
			_, _ = w.Write([]byte(`{"pagecount":1,"list":[{"vod_id":"1","vod_name":"测试片"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &app{}
	root := &config.Config{
		URL:   srv.URL + "/depot.json",
		Lines: []config.Line{{Name: "坏线路", URL: srv.URL + "/bad.json"}},
	}
	a.library.root = root
	a.library.cached = map[string]*config.Config{}

	// First scan hits /bad.json once and records it in failedDocs.
	a.startScan(&a.library.search, "测试片", false)
	if badHits.Load() != 1 {
		t.Fatalf("first scan badHits = %d, want 1", badHits.Load())
	}
	if _, cooled := a.library.failedDocs[srv.URL+"/bad.json"]; !cooled {
		t.Fatal("failed depot URL was not recorded in failedDocs")
	}

	// Second normal search skips the failed depot line without hitting network.
	a.startScan(&a.library.search, "另一部片", false)
	if badHits.Load() != 1 {
		t.Fatalf("second scan re-fetched cooling depot line: badHits = %d, want 1", badHits.Load())
	}

	// Canceled errors must not poison failedDocs.
	a.appendLibraryEvent(&a.library.search, library.Event{
		URL: srv.URL + "/canceled.json",
		Err: context.Canceled,
	})
	if _, poisoned := a.library.failedDocs[srv.URL+"/canceled.json"]; poisoned {
		t.Fatal("canceled depot event poisoned failedDocs")
	}

	// Explicit reloadScan (skipFailed=false) retries and clears cooldown on recovery.
	failLine.Store(false)
	a.reloadScan(&a.library.search)
	if badHits.Load() != 2 {
		t.Fatalf("reloadScan badHits = %d, want 2", badHits.Load())
	}
	if _, stillFailed := a.library.failedDocs[srv.URL+"/bad.json"]; stillFailed {
		t.Fatal("recovered depot line remained in failedDocs")
	}
}

func TestOpenPlaybackURLReusesProxyAndConnectionPoolAcrossSwitches(t *testing.T) {
	a := &app{}
	var openedURLs []string
	var failNext bool
	a.openMPV = func(target string, _ mpvplayer.OpenOptions) (mediaPlayer, error) {
		if failNext {
			return nil, errors.New("open failed")
		}
		openedURLs = append(openedURLs, target)
		return newFakeTrackPlayer(t), nil
	}
	defer func() {
		if a.proxy != nil {
			_ = a.proxy.Close()
		}
	}()

	if !a.openPlaybackURL("https://cdn.example.com/ep1.m3u8", map[string]string{"Referer": "https://a.example.com/"}, false) {
		t.Fatalf("first open failed: %s", a.status)
	}
	firstProxy := a.proxy
	if firstProxy == nil || firstProxy.Header("Referer") != "https://a.example.com/" {
		t.Fatalf("expected proxy with initial referer, got proxy=%v ref=%q", firstProxy, firstProxy.Header("Referer"))
	}

	// Failed replacement with keepCurrent=true keeps the existing proxy and headers intact.
	failNext = true
	if a.openPlaybackURL("https://cdn.example.com/bad.m3u8", map[string]string{"Referer": "https://bad.example.com/"}, true) {
		t.Fatal("expected replacement failure")
	}
	if a.proxy != firstProxy || a.proxy.Header("Referer") != "https://a.example.com/" {
		t.Fatalf("failed replacement disturbed active proxy/headers: proxySame=%v ref=%q", a.proxy == firstProxy, a.proxy.Header("Referer"))
	}

	// Successful episode switch with keepCurrent=true reuses the same Proxy instance
	// (preserving its http.Transport pool) and stages headers via an isolated sid session.
	failNext = false
	if !a.openPlaybackURL("https://cdn.example.com/ep2.m3u8", map[string]string{"Referer": "https://b.example.com/"}, true) {
		t.Fatalf("second open failed: %s", a.status)
	}
	if a.proxy != firstProxy {
		t.Fatal("episode switch created a new Proxy instead of reusing the existing connection pool")
	}
	if a.proxySession == "" || a.proxy.Header("Referer") != "https://b.example.com/" {
		t.Fatalf("expected active sid session and updated referer, got sess=%q ref=%q", a.proxySession, a.proxy.Header("Referer"))
	}
	if len(openedURLs) != 2 || !strings.Contains(openedURLs[0], "/u?url=") || !strings.Contains(openedURLs[1], "/u?sid=") {
		t.Fatalf("expected /u?url= then /u?sid= URLs, got %v", openedURLs)
	}
}

func TestDetailShowRelatedButtonDoesNotPanic(t *testing.T) {
	doc := &config.Config{URL: "https://example.com/cfg.json"}
	entry := library.Entry{
		Site:   config.Site{Key: "s1", Name: "示例站", Kind: config.KindJSON, API: "https://example.com/api"},
		Config: doc,
		Line:   "线路1",
	}
	item1 := library.Item{Entry: entry, Video: catalog.Video{ID: "1", Name: "示例剧"}}
	item2 := library.Item{Entry: entry, Video: catalog.Video{ID: "2", Name: "示例剧 第二季"}}

	a := &app{nav: []routeKind{routeHome, routeDetail}}
	a.library.search.query = "示例剧"
	a.library.search.items = []library.Item{item1, item2}
	a.library.search.groups = []titleGroup{
		{name: "示例剧", items: []library.Item{item1}},
		{name: "示例剧 第二季", items: []library.Item{item2}},
	}
	a.title = &titlePage{
		name:   "示例剧",
		seeds:  []library.Item{item1},
		item:   item1,
		detail: &catalog.Detail{Name: "示例剧"},
		failed: map[string]string{},
	}

	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("相近结果 (2)"); err != nil {
		t.Fatalf("clicking 相近结果 failed: %v (texts=%v)", err, tt.Texts())
	}
	if a.title != nil {
		t.Fatal("expected a.title to be cleared after showRelated")
	}
	if a.route() != routeSearch {
		t.Fatalf("route = %v, want routeSearch", a.route())
	}
}

func TestFollowingRefreshSharesFailedDocsAcrossTitles(t *testing.T) {
	var badHits atomic.Int32
	var okHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad.json":
			badHits.Add(1)
			http.Error(w, "dead", http.StatusNotFound)
		case "/good.json":
			okHits.Add(1)
			_, _ = w.Write([]byte(`{"sites":[{"key":"s1","name":"示例站","type":1,"api":"/vod"}]}`))
		case "/vod":
			if r.URL.Query().Get("ids") != "" {
				_, _ = w.Write([]byte(`{"list":[{"vod_id":"1","vod_name":"追剧甲","vod_play_from":"主线","vod_play_url":"第1集$https://example.com/1.mp4"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"pagecount":1,"list":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &app{}
	a.library.root = &config.Config{
		URL: srv.URL + "/depot.json",
		Lines: []config.Line{
			{Name: "坏线路", URL: srv.URL + "/bad.json"},
			{Name: "好线路", URL: srv.URL + "/good.json"},
		},
	}
	a.library.cached = map[string]*config.Config{}
	a.favorites.entries = []favoriteEntry{
		{Name: "追剧甲"},
		{Name: "追剧乙"},
		{Name: "追剧丙"},
	}

	a.refreshFollowingMode(false)

	if badHits.Load() != 1 {
		t.Fatalf("expected dead depot line to be fetched once across 3 followed titles, got %d", badHits.Load())
	}
	if okHits.Load() != 1 {
		t.Fatalf("expected good depot line to be cached after first title, got %d fetches", okHits.Load())
	}
	if _, cooled := a.library.failedDocs[srv.URL+"/bad.json"]; !cooled {
		t.Fatal("failed depot line from following refresh was not propagated to a.library.failedDocs")
	}
	if a.library.cached[srv.URL+"/good.json"] == nil {
		t.Fatal("loaded depot line from following refresh was not propagated to a.library.cached")
	}
}
