package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/egoist/mygo/ui"
)

func TestSavedSourceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "source.json")
	if got, err := readSavedSource(path); err != nil || got.URL != "" {
		t.Fatalf("missing source: %+v, %v", got, err)
	}
	want := savedSource{URL: "https://example.com/config.json", Assets: "/tmp/example-assets"}
	if err := writeSavedSource(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readSavedSource(path)
	if err != nil || got != want {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("saved source is readable by others: %v", info.Mode())
	}
	if err := writeSavedSource(path, savedSource{URL: "https://example.com/other.json"}); err != nil {
		t.Fatal(err)
	}
	got, err = readSavedSource(path)
	if err != nil || got.URL != "https://example.com/other.json" || got.Assets != "" {
		t.Fatalf("replacement: %+v, %v", got, err)
	}
}

func TestRestoreImportedVODAndLiveAfterTempLinkExpires(t *testing.T) {
	var expired atomic.Bool
	var configHits, livePlaylistHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/temp-vod-live.json":
			configHits.Add(1)
			if expired.Load() {
				w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"sites":[{"key":"cms","name":"示例站点","type":1,"api":"./api"}],"lives":[{"name":"测试直播","url":"./channels.m3u"}]}`)
		case "/channels.m3u":
			livePlaylistHits.Add(1)
			if expired.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1 group-title=\"新闻\",示例频道\nhttps://example.com/live.m3u8\n")
		case "/api":
			if r.URL.Query().Get("ac") == "list" {
				fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
				return
			}
			fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"1","vod_name":"示例电影"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sourcePath := filepath.Join(t.TempDir(), "source.json")

	// First run: import VOD+Live config and open the live tab while the temp link works.
	first := &app{persist: true, sourcePath: sourcePath}
	defer first.close()
	first.loadConfig(srv.URL + "/temp-vod-live.json")
	if first.library.root == nil || len(first.library.feed.items) != 1 {
		t.Fatalf("initial import failed: status=%q items=%d", first.status, len(first.library.feed.items))
	}
	first.switchTab(routeLive)
	if len(first.live.channels) != 1 || first.live.channels[0].Name != "示例频道" {
		t.Fatalf("initial live load failed: status=%q channels=%+v", first.status, first.live.channels)
	}
	info, err := os.Stat(configCachePath(sourcePath))
	if err != nil {
		t.Fatalf("config cache file missing: %v", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("config cache is readable by others: %v", info.Mode())
	}

	// Expire the temporary config and playlist links (HTTP 404).
	expired.Store(true)
	configHits.Store(0)
	livePlaylistHits.Store(0)

	// Second run: restart restores VOD and Live completely from local cache without hitting the expired links.
	restarted := &app{persist: true, sourcePath: sourcePath}
	defer restarted.close()
	restarted.restoreSavedSource()
	if restarted.library.root == nil || len(restarted.library.feed.items) != 1 {
		t.Fatalf("cached VOD restore failed: status=%q root=%v items=%d", restarted.status, restarted.library.root, len(restarted.library.feed.items))
	}
	restarted.switchTab(routeLive)
	if len(restarted.live.channels) != 1 || restarted.live.channels[0].Name != "示例频道" {
		t.Fatalf("cached live restore failed: status=%q channels=%+v", restarted.status, restarted.live.channels)
	}
	if configHits.Load() != 0 || livePlaylistHits.Load() != 0 {
		t.Fatalf("startup unexpectedly fetched expired URLs: configHits=%d livePlaylistHits=%d", configHits.Load(), livePlaylistHits.Load())
	}

	// Explicit refresh attempts the network, reports the error, and preserves the cached VOD and Live data.
	restarted.loadConfig(restarted.browser.configURL)
	if !strings.Contains(restarted.status, "加载配置失败") || !strings.Contains(restarted.status, "HTTP 404") {
		t.Fatalf("expected refresh error status, got %q", restarted.status)
	}
	if restarted.library.root == nil || len(restarted.live.channels) != 1 {
		t.Fatalf("failed refresh wiped cached data: root=%v channels=%d", restarted.library.root, len(restarted.live.channels))
	}
	if configHits.Load() != 1 {
		t.Fatalf("explicit refresh should hit network once, got %d", configHits.Load())
	}
}

func TestRestoreStandaloneLiveAfterTempLinkExpires(t *testing.T) {
	var expired atomic.Bool
	var liveConfigHits, playlistHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/temp-live.json":
			liveConfigHits.Add(1)
			if expired.Load() {
				w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"lives":[{"name":"测试直播","url":"./list.txt"}]}`)
		case "/list.txt":
			playlistHits.Add(1)
			if expired.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, "新闻,#genre#\n演示频道,https://example.com/live.m3u8\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sourcePath := filepath.Join(t.TempDir(), "source.json")

	first := &app{persist: true, sourcePath: sourcePath}
	defer first.close()
	first.importLive(srv.URL + "/temp-live.json")
	if len(first.live.channels) != 1 || first.live.channels[0].Name != "演示频道" {
		t.Fatalf("initial live import failed: status=%q channels=%+v", first.status, first.live.channels)
	}
	info, err := os.Stat(liveCachePath(sourcePath))
	if err != nil {
		t.Fatalf("live cache file missing: %v", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("live cache is readable by others: %v", info.Mode())
	}

	// Expire both the live config URL and the referenced playlist URL.
	expired.Store(true)
	liveConfigHits.Store(0)
	playlistHits.Store(0)

	restarted := &app{persist: true, sourcePath: sourcePath}
	defer restarted.close()
	restarted.restoreSavedSource()
	if restarted.route() != routeLive {
		t.Fatalf("standalone live restore did not open live tab: route=%v status=%q", restarted.route(), restarted.status)
	}
	if restarted.status != "" {
		t.Fatalf("unexpected error status on cached live restore: %q", restarted.status)
	}
	if len(restarted.live.channels) != 1 || restarted.live.channels[0].Name != "演示频道" {
		t.Fatalf("cached standalone live channels missing: %+v", restarted.live.channels)
	}
	if liveConfigHits.Load() != 0 || playlistHits.Load() != 0 {
		t.Fatalf("startup unexpectedly fetched expired live URLs: configHits=%d playlistHits=%d", liveConfigHits.Load(), playlistHits.Load())
	}

	// Clicking "刷新直播" on the sources page attempts the network, reports 404, and preserves cached channels.
	restarted.switchTab(routeSources)
	tt := ui.NewTester(restarted.view, 960, 640)
	if err := tt.Click("刷新直播"); err != nil {
		t.Fatalf("click 刷新直播: %v; texts=%v", err, tt.Texts())
	}
	if !strings.Contains(restarted.status, "导入直播失败") || !strings.Contains(restarted.status, "HTTP 404") {
		t.Fatalf("expected live refresh 404 status, got %q", restarted.status)
	}
	if liveConfigHits.Load() != 1 || len(restarted.live.channels) != 1 {
		t.Fatalf("live refresh hits=%d channels=%d", liveConfigHits.Load(), len(restarted.live.channels))
	}
}

func TestRestoreDepotAfterTempLinksExpire(t *testing.T) {
	var expired atomic.Bool
	var depotHits, lineHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/depot.json":
			depotHits.Add(1)
			if expired.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"urls":[{"name":"测试线路","url":"./line.json"}]}`)
		case "/line.json":
			lineHits.Add(1)
			if expired.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"sites":[{"key":"cms","name":"线路站点","type":1,"api":"./api"}],"lives":[{"name":"线路直播","channels":[{"name":"频道甲","urls":["https://example.com/a.m3u8"]}]}]}`)
		case "/api":
			if r.URL.Query().Get("ac") == "list" {
				fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
				return
			}
			fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"9","vod_name":"仓库影片"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sourcePath := filepath.Join(t.TempDir(), "source.json")

	first := &app{persist: true, sourcePath: sourcePath}
	defer first.close()
	first.loadConfig(srv.URL + "/depot.json")
	if len(first.library.documents) != 1 || len(first.library.feed.items) != 1 {
		t.Fatalf("depot import failed: docs=%d items=%d status=%q", len(first.library.documents), len(first.library.feed.items), first.status)
	}

	expired.Store(true)
	depotHits.Store(0)
	lineHits.Store(0)

	restarted := &app{persist: true, sourcePath: sourcePath}
	defer restarted.close()
	restarted.restoreSavedSource()
	if restarted.library.root == nil || !restarted.library.root.FromDepot || len(restarted.library.documents) != 1 {
		t.Fatalf("depot restore failed: root=%+v docs=%d status=%q", restarted.library.root, len(restarted.library.documents), restarted.status)
	}
	if len(restarted.library.feed.items) != 1 || restarted.library.feed.items[0].Video.Name != "仓库影片" {
		t.Fatalf("depot feed items missing after cached restore: %+v", restarted.library.feed.items)
	}
	restarted.switchTab(routeLive)
	if len(restarted.live.channels) != 1 || restarted.live.channels[0].Name != "频道甲" {
		t.Fatalf("depot live channels missing after cached restore: status=%q channels=%+v", restarted.status, restarted.live.channels)
	}
	if depotHits.Load() != 0 || lineHits.Load() != 0 {
		t.Fatalf("depot restore hit expired URLs: depotHits=%d lineHits=%d", depotHits.Load(), lineHits.Load())
	}
}

func TestRemoveSourcesCleansUpOnlyRemovedCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vod.json":
			fmt.Fprint(w, `{"sites":[{"key":"cms","name":"点播站","type":1,"api":"./api"}],"lives":[{"name":"点播附带直播","channels":[{"name":"附带频道","urls":["https://example.com/vod-live.m3u8"]}]}]}`)
		case "/standalone.m3u":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,独立频道\nhttps://example.com/standalone.m3u8\n")
		case "/api":
			fmt.Fprint(w, `{"pagecount":1,"list":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sourcePath := filepath.Join(t.TempDir(), "source.json")
	a := &app{persist: true, sourcePath: sourcePath}
	defer a.close()
	a.loadConfig(srv.URL + "/vod.json")
	a.importLive(srv.URL + "/standalone.m3u")

	if _, err := os.Stat(configCachePath(sourcePath)); err != nil {
		t.Fatalf("expected VOD cache file: %v", err)
	}
	if _, err := os.Stat(liveCachePath(sourcePath)); err != nil {
		t.Fatalf("expected live cache file: %v", err)
	}

	// Removing standalone live removes live cache, keeps VOD cache, and falls back to VOD lives.
	a.removeLive()
	if _, err := os.Stat(liveCachePath(sourcePath)); !os.IsNotExist(err) {
		t.Fatalf("live cache was not deleted: %v", err)
	}
	if _, err := os.Stat(configCachePath(sourcePath)); err != nil {
		t.Fatalf("VOD cache should remain after removeLive: %v", err)
	}
	a.switchTab(routeLive)
	if len(a.live.channels) != 1 || a.live.channels[0].Name != "附带频道" {
		t.Fatalf("expected fallback to VOD live channels: %+v", a.live.channels)
	}

	// Re-import standalone live, then remove VOD: VOD cache goes away while standalone live cache survives.
	a.importLive(srv.URL + "/standalone.m3u")
	a.removeConfig()
	if _, err := os.Stat(configCachePath(sourcePath)); !os.IsNotExist(err) {
		t.Fatalf("VOD cache was not deleted: %v", err)
	}
	if _, err := os.Stat(liveCachePath(sourcePath)); err != nil {
		t.Fatalf("live cache should remain after removeConfig: %v", err)
	}
}

func TestRestoreFallsBackToNetworkWhenLegacySourceHasNoCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/legacy-vod.json":
			fmt.Fprint(w, `{"sites":[{"key":"cms","name":"旧站点","type":1,"api":"./api"}]}`)
		case "/legacy-live.m3u":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,旧频道\nhttps://example.com/old.m3u8\n")
		case "/api":
			fmt.Fprint(w, `{"pagecount":1,"list":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sourcePath := filepath.Join(t.TempDir(), "source.json")
	if err := writeSavedSource(sourcePath, savedSource{
		URL:     srv.URL + "/legacy-vod.json",
		LiveURL: srv.URL + "/legacy-live.m3u",
	}); err != nil {
		t.Fatal(err)
	}
	// Corrupted cache files should be treated as cache misses and repaired on fetch.
	if err := os.WriteFile(configCachePath(sourcePath), []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(liveCachePath(sourcePath), []byte(`{"url":"https://example.com/other.m3u","sources":[]}`), 0600); err != nil {
		t.Fatal(err)
	}

	a := &app{persist: true, sourcePath: sourcePath}
	defer a.close()
	a.restoreSavedSource()
	a.switchTab(routeLive)
	if a.library.root == nil || len(a.live.channels) != 1 || a.live.channels[0].Name != "旧频道" {
		t.Fatalf("legacy fallback failed: root=%v channels=%+v status=%q", a.library.root, a.live.channels, a.status)
	}
	if hits.Load() == 0 {
		t.Fatal("expected network fallback for uncached legacy source")
	}
	if _, ok := readCachedSourceConfig(configCachePath(sourcePath), srv.URL+"/legacy-vod.json"); !ok {
		t.Fatal("legacy VOD restore did not populate local cache")
	}
	if _, ok := readCachedLiveSource(liveCachePath(sourcePath), srv.URL+"/legacy-live.m3u"); !ok {
		t.Fatal("legacy live restore did not populate local cache")
	}
}

func TestRefreshLiveUpdatesAllPlaylistsAndFallsBackOnError(t *testing.T) {
	var version atomic.Int32
	var secondFails atomic.Bool
	version.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.json":
			fmt.Fprint(w, `{"lives":[{"name":"线路甲","url":"./a.txt"},{"name":"线路乙","url":"./b.txt"}]}`)
		case "/a.txt":
			fmt.Fprintf(w, "分组,#genre#\n频道甲v%d,https://example.com/a.m3u8\n", version.Load())
		case "/b.txt":
			if secondFails.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, "分组,#genre#\n频道乙v%d,https://example.com/b.m3u8\n", version.Load())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sourcePath := filepath.Join(t.TempDir(), "source.json")
	a := &app{persist: true, sourcePath: sourcePath}
	defer a.close()
	a.importLive(srv.URL + "/live.json")
	a.loadLiveSource(1)
	if a.live.channels[0].Name != "频道乙v1" {
		t.Fatalf("unexpected initial second source: %+v", a.live.channels)
	}

	// Update playlist A, fail playlist B, and trigger explicit live refresh.
	version.Store(2)
	secondFails.Store(true)
	a.importLive(srv.URL + "/live.json")
	if len(a.live.channels) != 1 || a.live.channels[0].Name != "频道甲v2" {
		t.Fatalf("first playlist did not refresh: %+v status=%q", a.live.channels, a.status)
	}
	a.loadLiveSource(1)
	if len(a.live.channels) != 1 || a.live.channels[0].Name != "频道乙v1" {
		t.Fatalf("second playlist did not fall back to cached channels on 404: %+v", a.live.channels)
	}
	if !strings.Contains(a.status, "加载频道失败") {
		t.Fatalf("expected error status on failed playlist refresh, got %q", a.status)
	}
}

func TestCachedConfigReclassifiesLegacyCSPSites(t *testing.T) {
	legacy := &cachedConfig{
		Config: config.Config{
			URL: "https://example.com/line.json",
			Sites: []config.Site{
				{Key: "wogg", Name: "玩偶示例", Type: 3, API: "csp_Wogg", Ext: "https://example.com/t.json$$$https://wogg.example.com", Kind: config.KindCSP},
				{Key: "share", Name: "夸克分享", Type: 3, API: "csp_QuarkShare", Ext: "https://example.com/t.json$$$https://example.com/list.txt", Kind: config.KindCSP},
				{Key: "alist", Name: "AList", Type: 3, API: "csp_Alist", Ext: `{"盘":"https://alist.example.com"}`, Kind: config.KindCSP},
			},
		},
	}
	restored := legacy.toConfig()
	if restored.Sites[0].Kind != config.KindPanWeb {
		t.Fatalf("wogg kind = %s, want %s", restored.Sites[0].Kind, config.KindPanWeb)
	}
	if restored.Sites[1].Kind != config.KindPanShare {
		t.Fatalf("share kind = %s, want %s", restored.Sites[1].Kind, config.KindPanShare)
	}
	if restored.Sites[2].Kind != config.KindAList {
		t.Fatalf("alist kind = %s, want %s", restored.Sites[2].Kind, config.KindAList)
	}
}
