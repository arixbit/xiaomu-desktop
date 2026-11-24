package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

func TestSourcesPageFiltersInvalidSitesAndAllowsRecovery(t *testing.T) {
	var deadFixed atomic.Bool
	var deadSearchHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alive":
			if r.URL.Query().Get("ac") == "list" {
				fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
				return
			}
			fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"1","vod_name":"示例电影"}]}`)
		case "/dead":
			if r.URL.Query().Get("wd") != "" {
				deadSearchHits.Add(1)
			}
			if !deadFixed.Load() {
				http.Error(w, "gone", http.StatusNotFound)
				return
			}
			if r.URL.Query().Get("ac") == "list" {
				fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
				return
			}
			fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"2","vod_name":"复活影片"}]}`)
		case "/dead-pan/":
			// Parked domain placeholder page with no categories or cards.
			_, _ = w.Write([]byte(`<html><head><title>welcome</title></head><body><h2>domain parked</h2></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	doc := &config.Config{
		URL: srv.URL + "/line.json",
		Sites: []config.Site{
			{Key: "alive", Name: "正常站点", Type: 1, API: srv.URL + "/alive", Kind: config.KindJSON},
			{Key: "dead", Name: "故障站点A", Type: 1, API: srv.URL + "/dead", Kind: config.KindJSON},
			{Key: "dead-pan", Name: "停靠网盘B", Type: 3, API: "csp_Wogg", Ext: srv.URL + "/dead-pan", Kind: config.KindPanWeb},
			{Key: "jar", Name: "不兼容站点C", Type: 3, API: "csp_Demo", Kind: config.KindCSP},
		},
	}
	deadOnlyDoc := &config.Config{
		URL: srv.URL + "/dead-line.json",
		Sites: []config.Site{
			{Key: "dead-only", Name: "独苗失效站", Type: 1, API: srv.URL + "/dead", Kind: config.KindJSON},
		},
	}

	healthPath := filepath.Join(t.TempDir(), "source-health.json")
	a := &app{}
	defer a.close()
	a.siteHealth.path = healthPath
	a.library.root = doc
	a.library.cached = map[string]*config.Config{}
	a.library.documents = []libraryDocument{
		{doc: doc, line: "主线路", open: true},
		{doc: deadOnlyDoc, line: "全挂线路", open: true},
	}

	// Speed test probes all supported sites and automatically marks dead / parked sites failed.
	a.startSourceSpeedTest()

	deadEntry := library.Entry{Site: doc.Sites[1], Config: doc, Line: "主线路"}
	deadPanEntry := library.Entry{Site: doc.Sites[2], Config: doc, Line: "主线路"}
	if !a.isSiteFailed(deadEntry.Key()) || !a.isSiteFailed(deadPanEntry.Key()) {
		t.Fatalf("expected dead and parked sites marked failed: %+v", a.siteHealth.failed)
	}

	// Verify persistence round-trip.
	saved, err := readSiteHealth(healthPath)
	if err != nil || len(saved) != 3 {
		t.Fatalf("persisted health = %+v err=%v, want 3 failed sites", saved, err)
	}

	// On the sources page, failed sites, parked pan sites, and dead-only lines are filtered out by default.
	a.nav = []routeKind{routeSources}
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	if !tt.HasText("正常站点") {
		t.Fatalf("alive site missing: %v", tt.Texts())
	}
	if tt.HasText("故障站点A") || tt.HasText("停靠网盘B") || tt.HasText("全挂线路") {
		t.Fatalf("invalid sites or dead-only line were not filtered out: %v", tt.Texts())
	}
	if !tt.HasText("已过滤 3 个失效站点、1 个不兼容站点") {
		t.Fatalf("filtered summary missing: %v", tt.Texts())
	}

	// Normal search skips known-failed sites completely.
	a.browser.query = "示例"
	a.searchLibrary()
	if deadSearchHits.Load() != 0 {
		t.Fatalf("search queried known-failed site %d times", deadSearchHits.Load())
	}

	// Clicking "查看失效站点" reveals hidden sites on demand.
	a.nav = []routeKind{routeSources}
	if err := tt.Click("查看失效站点"); err != nil {
		t.Fatalf("click 查看失效站点: %v (texts=%v)", err, tt.Texts())
	}
	if !tt.HasText("故障站点A") || !tt.HasText("停靠网盘B") || !tt.HasText("隐藏失效站点") {
		t.Fatalf("failed sites not shown after toggle: %v", tt.Texts())
	}

	// Once a previously dead site recovers, clicking speed test restores it automatically.
	a.showFailedSites = false
	deadFixed.Store(true)
	a.startSourceSpeedTest()
	tt.Frame()
	if a.isSiteFailed(deadEntry.Key()) {
		t.Fatal("recovered site remained marked failed after speed test")
	}
	if !tt.HasText("故障站点A") {
		t.Fatalf("recovered site did not reappear on sources page: %v", tt.Texts())
	}
}
