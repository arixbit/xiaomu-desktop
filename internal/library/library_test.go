package library

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
)

func TestSearchAcrossDepotPreservesOriginsAndSkipsDisabledSites(t *testing.T) {
	var configReads, forbidden atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/depot":
			fmt.Fprint(w, `{"urls":[{"name":"线路甲","url":"/a.json"},{"name":"线路乙","url":"/b.json"},{"name":"重复","url":"/a.json"},{"name":"失效","url":"/bad.json"}]}`)
		case "/a.json", "/b.json":
			configReads.Add(1)
			key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".json")
			fmt.Fprintf(w, `{"sites":[{"key":"%s","name":"站点%s","type":1,"api":"/%s"},{"key":"jar","type":3,"api":"csp_Demo"},{"key":"disabled","type":1,"api":"/forbidden","searchable":"0"}],"parses":[{"name":"解析%s","type":1,"url":"/parse-%s"}]}`, key, key, key, key, key)
		case "/bad.json":
			http.Error(w, "unavailable", http.StatusNotFound)
		case "/forbidden":
			forbidden.Add(1)
			http.Error(w, "must not query", http.StatusBadRequest)
		case "/a", "/b":
			if r.URL.Query().Get("ac") == "list" {
				fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
				return
			}
			if r.URL.Query().Get("wd") != "示例" {
				t.Errorf("query=%s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"pagecount":2,"list":[{"vod_id":"7","vod_name":"示例"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	root, err := config.Read(context.Background(), srv.URL+"/depot", nil)
	if err != nil {
		t.Fatal(err)
	}
	cache := map[string]*config.Config{}
	var results []Result
	var last Event
	if err := Scan(context.Background(), root, Options{Query: "示例"}, func(e Event) {
		last = e
		if e.Document != nil {
			cache[e.URL] = e.Document
		}
		if e.Result != nil && e.Err == nil {
			results = append(results, *e.Result)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || configReads.Load() != 2 || forbidden.Load() != 0 || last.Completed != 2 || last.Skipped != 4 || last.Failed != 1 {
		t.Fatalf("results=%d reads=%d forbidden=%d progress=%+v", len(results), configReads.Load(), forbidden.Load(), last)
	}
	if results[0].Target.Entry.Key() == results[1].Target.Entry.Key() {
		t.Fatal("origins collapsed")
	}
	for _, result := range results {
		entry := result.Target.Entry
		if len(result.Page.Videos) != 1 || !result.Page.HasNext || entry.Config.Parsers[0].Name != "解析"+entry.Site.Key || entry.Line == "" {
			t.Fatalf("lost origin or pagination: %+v", result)
		}
	}
	if err := Scan(context.Background(), root, Options{Query: "示例", Cached: cache}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if configReads.Load() != 2 {
		t.Fatal("cached line configurations were fetched again")
	}
}

func TestSearchStreamsBeforeSlowSiteTimesOut(t *testing.T) {
	var slowStarted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ac") == "list" {
			fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
			return
		}
		if r.URL.Path == "/slow" {
			slowStarted.Store(true)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"fast","vod_name":"先返回的影片"}]}`)
	}))
	defer srv.Close()
	root := &config.Config{URL: srv.URL, Sites: []config.Site{{Key: "slow", Kind: config.KindJSON, API: srv.URL + "/slow"}, {Key: "fast", Kind: config.KindJSON, API: srv.URL + "/fast"}}}
	var fastBeforeTimeout bool
	var last Event
	err := Scan(context.Background(), root, Options{Query: "示例", Timeout: 250 * time.Millisecond}, func(e Event) {
		last = e
		if e.Result != nil && e.Err == nil && e.Result.Target.Entry.Site.Key == "fast" {
			fastBeforeTimeout = e.Completed == 1
		}
	})
	if err != nil || !slowStarted.Load() || !fastBeforeTimeout || last.Completed != 2 || last.Failed != 1 {
		t.Fatalf("err=%v streaming=%v progress=%+v", err, fastBeforeTimeout, last)
	}
}

func TestCancelSearchReleasesBoundedWorkers(t *testing.T) {
	started := make(chan struct{}, 20)
	var active, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()
	root := &config.Config{URL: srv.URL}
	for i := range 20 {
		root.Sites = append(root.Sites, config.Site{Key: fmt.Sprint(i), API: fmt.Sprintf("%s/%d", srv.URL, i), Kind: config.KindJSON})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Scan(ctx, root, Options{Query: "示例"}, func(Event) {}) }()
	for range workers {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not stop")
	}
	srv.Close()
	if peak.Load() > workers || active.Load() != 0 {
		t.Fatalf("peak=%d active=%d", peak.Load(), active.Load())
	}
}
