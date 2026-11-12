package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/egoist/mygo/ui"
)

func TestCMSSearchAndDetailIgnoreCategoryEndpoint(t *testing.T) {
	for _, kind := range []config.Kind{config.KindJSON, config.KindXML} {
		t.Run(string(kind), func(t *testing.T) {
			var categories atomic.Int32
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("wd") == "" && q.Get("ids") == "" {
					categories.Add(1)
					http.Error(w, "categories unavailable", http.StatusForbidden)
					return
				}
				if q.Get("token") != "fixture" {
					t.Error("API query parameters lost")
				}
				if q.Get("ids") == "7" {
					if kind == config.KindJSON {
						fmt.Fprintf(w, `{"list":[{"vod_name":"示例","vod_play_from":"HLS$$$MP4","vod_play_url":"正片$%s/clip.m3u8?token=fixture$$$正片$%s/clip.mp4"}]}`, srv.URL, srv.URL)
					} else {
						fmt.Fprint(w, `<rss><list><video><id>7</id><name>示例</name><dl><dd flag="HLS">正片$/clip.m3u8?token=fixture</dd><dd flag="MP4">正片$/clip.mp4</dd></dl></video></list></rss>`)
					}
					return
				}
				if q.Get("wd") != "示例" || q.Get("pg") != "1" {
					t.Errorf("unexpected search query: %s", r.URL.RawQuery)
				}
				if kind == config.KindJSON {
					fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"7","vod_name":"示例"}]}`)
				} else {
					fmt.Fprint(w, `<rss><list pagecount="1"><video><id>7</id><name>示例</name></video></list></rss>`)
				}
			}))
			defer srv.Close()
			a := &app{}
			defer a.close()
			a.library.root = &config.Config{URL: srv.URL, Sites: []config.Site{{Key: "demo", Kind: kind, API: srv.URL + "/api?token=fixture"}}}
			a.library.cached = map[string]*config.Config{}
			a.browser.query = "示例"
			a.searchLibrary()
			if len(a.library.items) != 1 {
				t.Fatalf("search blocked by categories: results=%d progress=%+v", len(a.library.items), a.library.progress)
			}
			a.openLibraryItem(a.library.items[0])
			if a.browser.detail == nil || len(a.browser.detail.Sources) != 2 {
				t.Fatalf("detail failed or playback sources lost: %s", a.status)
			}
			for i, suffix := range []string{"/clip.m3u8?token=fixture", "/clip.mp4"} {
				group := a.browser.detail.Sources[i]
				if len(group.Episodes) != 1 {
					t.Fatalf("episode lost: %+v", group)
				}
				play, err := a.browser.session.Play(context.Background(), group.Name, group.Episodes[0])
				if err != nil || play.URL != srv.URL+suffix {
					t.Fatalf("media URL changed: %+v, %v", play, err)
				}
			}
			if categories.Load() != 0 {
				t.Fatalf("unexpected category requests: %d", categories.Load())
			}
		})
	}
}

func TestLibraryResultUsesItsOwnSiteAndParser(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/config" {
			fmt.Fprint(w, `{"sites":[{"key":"a","name":"来源甲","type":1,"api":"/a"},{"key":"b","name":"来源乙","type":1,"api":"/b"}],"parses":[{"type":1,"url":"/parse?url="}]}`)
			return
		}
		if r.URL.Path == "/parse" {
			fmt.Fprintf(w, `{"url":%q}`, srv.URL+"/clip.mp4")
			return
		}
		if r.URL.Path == "/clip.mp4" {
			w.Header().Set("Content-Type", "video/mp4")
			fmt.Fprint(w, "fixture")
			return
		}
		if r.URL.Path == "/watch" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html></html>")
			return
		}
		query := r.URL.Query()
		if query.Get("ac") == "list" {
			fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
			return
		}
		if query.Get("ids") != "" {
			fmt.Fprintf(w, `{"list":[{"vod_name":%q,"vod_play_from":"解析播放","vod_play_url":%q}]}`, "详情"+r.URL.Path, "第1集$"+srv.URL+"/watch")
			return
		}
		id := "same"
		if query.Get("pg") == "2" {
			id = "more"
		}
		name := "简介匹配但片名不同"
		if r.URL.Path == "/b" {
			name = "示例"
		}
		fmt.Fprintf(w, `{"pagecount":2,"list":[{"vod_id":%q,"vod_name":%q}]}`, id, name)
	}))
	defer srv.Close()
	a := &app{}
	defer a.close()
	a.loadConfig(srv.URL+"/config", "")
	a.browser.query = "示例"
	a.searchLibrary()
	if len(a.library.items) != 2 {
		t.Fatalf("search results=%d status=%s", len(a.library.items), a.status)
	}
	if a.library.items[0].Video.Name != "示例" {
		t.Fatal("exact title match was not ranked first")
	}
	tt := ui.NewTester(a.view, 960, 640)
	if err := tt.Click("结果来源"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("来源甲"); err != nil {
		t.Fatal(err)
	}
	if a.library.filter != a.library.items[1].Entry.Key() {
		t.Fatalf("source filter did not update: %q", a.library.filter)
	}
	if err := tt.Click("结果来源"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("全部来源"); err != nil {
		t.Fatal(err)
	}
	if a.library.filter != "" {
		t.Fatal("source filter did not reset")
	}
	for _, item := range a.library.items {
		if item.Entry.Site.Key != "b" {
			continue
		}
		a.openLibraryItem(item)
		if a.browser.detail == nil || a.browser.detail.Name != "详情/b" || a.browser.doc != item.Entry.Config {
			t.Fatalf("wrong result origin: %s", a.status)
		}
		opened := false
		a.open = func(raw string) (*ui.VideoPlayer, error) {
			if !strings.HasPrefix(raw, "http://127.0.0.1:") {
				t.Fatalf("not proxied: %s", raw)
			}
			opened = true
			return nil, nil
		}
		a.playEpisode(a.browser.detail.Sources[0].Name, a.browser.detail.Sources[0].Episodes[0])
		if !opened {
			t.Fatalf("configured parser was lost: %s", a.status)
		}
	}
	a.browser.detail = nil
	a.startLibraryScan("示例", true)
	if len(a.library.items) != 4 || len(a.library.more) != 0 {
		t.Fatalf("pagination results=%d more=%d", len(a.library.items), len(a.library.more))
	}
}

func TestLibraryReloadRetriesFailedSourcesAndRefreshesResults(t *testing.T) {
	var good, flaky atomic.Int32
	var recovered atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var version int32
		if r.URL.Path == "/good" {
			version = good.Add(1)
		} else {
			version = flaky.Add(1)
			if !recovered.Load() {
				http.Error(w, "blocked", http.StatusForbidden)
				return
			}
		}
		fmt.Fprintf(w, `{"pagecount":1,"list":[{"vod_id":"7","vod_name":%q}]}`, fmt.Sprintf("%s-%d", r.URL.Path, version))
	}))
	defer srv.Close()
	a := &app{}
	defer a.close()
	a.library.root = &config.Config{URL: srv.URL, Sites: []config.Site{
		{Key: "good", Kind: config.KindJSON, API: srv.URL + "/good"},
		{Key: "flaky", Kind: config.KindJSON, API: srv.URL + "/flaky"},
	}}
	a.library.cached = map[string]*config.Config{}
	a.browser.query = "示例"
	a.searchLibrary()
	a.searchLibrary()
	if len(a.library.items) != 1 || good.Load() != 1 || flaky.Load() != 1 || a.library.progress.Cooling != 1 {
		t.Fatalf("repeated search: good=%d flaky=%d progress=%+v", good.Load(), flaky.Load(), a.library.progress)
	}
	recovered.Store(true)
	tt := ui.NewTester(a.view, 960, 640)
	if err := tt.Click("重新加载"); err != nil {
		t.Fatal(err)
	}
	if len(a.library.items) != 2 || good.Load() != 2 || flaky.Load() != 2 || a.library.progress.Cooling != 0 {
		t.Fatalf("manual reload: good=%d flaky=%d progress=%+v", good.Load(), flaky.Load(), a.library.progress)
	}
	for _, item := range a.library.items {
		if !strings.HasSuffix(item.Video.Name, "-2") {
			t.Fatalf("reload kept old results: %+v", item.Video)
		}
	}
}

func TestNewLibrarySearchIgnoresQueuedOldReplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ac") == "list" {
			fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
			return
		}
		query := r.URL.Query().Get("wd")
		fmt.Fprintf(w, `{"pagecount":1,"list":[{"vod_id":%q,"vod_name":%q}]}`, query, query)
	}))
	defer srv.Close()
	updates := make(chan func(), 32)
	a := &app{dispatch: func(fn func()) { updates <- fn }}
	defer a.close()
	a.library.root = &config.Config{URL: srv.URL, Sites: []config.Site{{Key: "demo", Kind: config.KindJSON, API: srv.URL + "/api"}}}
	a.library.cached = map[string]*config.Config{}
	a.startLibraryScan("旧关键词", false)
	// Keep the first generation's updates queued until the next search starts.
	select {
	case first := <-updates:
		updates <- first
	case <-time.After(time.Second):
		t.Fatal("no initial update")
	}
	a.startLibraryScan("新关键词", false)
	deadline := time.After(3 * time.Second)
	for a.library.running {
		select {
		case update := <-updates:
			update()
		case <-deadline:
			t.Fatal("new search did not finish")
		}
	}
	if len(a.library.items) != 1 || a.library.items[0].Video.Name != "新关键词" {
		t.Fatalf("stale search overwrote current results: %+v", a.library.items)
	}
}
