package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/egoist/mygo/ui"
)

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
