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

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func scanSearch(t *testing.T, root *config.Config, options Options) ([]Result, Event) {
	t.Helper()
	var results []Result
	var last Event
	if err := Scan(context.Background(), root, options, func(e Event) {
		last = e
		if e.Result != nil && e.Err == nil {
			results = append(results, *e.Result)
		}
	}); err != nil {
		t.Fatal(err)
	}
	return results, last
}

func TestSearchCacheSeparatesSourcesQueriesPagesAndAssets(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		q := r.URL.Query()
		fmt.Fprintf(w, `{"pagecount":2,"list":[{"vod_id":"7","vod_name":%q}]}`, q.Get("wd")+r.URL.Path+q.Get("pg"))
	}))
	defer srv.Close()
	root := &config.Config{URL: srv.URL + "/config", Sites: []config.Site{
		{Key: "a", Kind: config.KindJSON, API: srv.URL + "/a"},
		{Key: "b", Kind: config.KindJSON, API: srv.URL + "/b"},
	}}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	cache := &SearchCache{clock: func() time.Time { return now }}
	options := Options{Query: "示例", SearchCache: cache}
	first, last := scanSearch(t, root, options)
	if len(first) != 2 || last.Failed != 0 || requests.Load() != 2 {
		t.Fatalf("first search: results=%v progress=%+v requests=%d", first, last, requests.Load())
	}
	first[0].Page.Videos[0].Name = "caller mutation"
	again, _ := scanSearch(t, root, options)
	if len(again) != 2 || requests.Load() != 2 {
		t.Fatalf("search was fetched again: requests=%d", requests.Load())
	}
	for _, result := range again {
		if result.Page.Videos[0].Name != "示例/"+result.Target.Entry.Site.Key+"1" || !result.Page.HasNext {
			t.Fatalf("cached data lost its source or was mutated: %+v", result)
		}
	}
	options.Query = "另一部"
	scanSearch(t, root, options)
	if requests.Load() != 4 {
		t.Fatal("different query reused cached results")
	}
	options.Query = "示例"
	options.More = []Target{{Entry: first[0].Target.Entry, Page: 2}}
	more, _ := scanSearch(t, root, options)
	scanSearch(t, root, options)
	if len(more) != 1 || more[0].Page.HasNext || !strings.HasSuffix(more[0].Page.Videos[0].Name, "2") || requests.Load() != 5 {
		t.Fatalf("cached pagination: results=%+v requests=%d", more, requests.Load())
	}
	options.More, options.Assets = nil, "different-assets"
	scanSearch(t, root, options)
	if requests.Load() != 7 {
		t.Fatal("different script environment reused cached results")
	}
	options.Assets = ""
	other := *root
	other.URL += "-other"
	scanSearch(t, &other, options)
	if requests.Load() != 9 {
		t.Fatal("different imported configuration reused cached results")
	}
	now = now.Add(searchCacheTTL)
	scanSearch(t, root, options)
	if requests.Load() != 11 {
		t.Fatal("expired results were not fetched again")
	}
}

func TestSearchCacheTemporarilyCoolsFailedSources(t *testing.T) {
	for _, failure := range []string{"forbidden", "timeout"} {
		t.Run(failure, func(t *testing.T) {
			var requests atomic.Int32
			var unavailable atomic.Bool
			unavailable.Store(true)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if unavailable.Load() {
					if failure == "timeout" {
						<-r.Context().Done()
					} else {
						http.Error(w, "blocked", http.StatusForbidden)
					}
					return
				}
				fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"7","vod_name":"已恢复"}]}`)
			}))
			defer srv.Close()
			root := &config.Config{URL: srv.URL, Sites: []config.Site{{Key: "demo", Kind: config.KindJSON, API: srv.URL + "/api"}}}
			now := time.Now()
			cache := &SearchCache{clock: func() time.Time { return now }}
			options := Options{Query: "示例", SearchCache: cache, Timeout: 200 * time.Millisecond}
			_, last := scanSearch(t, root, options)
			if last.Failed != 1 || last.Cooling != 0 || requests.Load() != 1 {
				t.Fatalf("first failure: progress=%+v requests=%d", last, requests.Load())
			}
			options.Query = "另一部"
			_, last = scanSearch(t, root, options)
			if last.Failed != 0 || last.Cooling != 1 || requests.Load() != 1 {
				t.Fatalf("cooldown did not suppress requests: progress=%+v requests=%d", last, requests.Load())
			}
			unavailable.Store(false)
			now = now.Add(searchCooldown)
			results, last := scanSearch(t, root, options)
			if len(results) != 1 || last.Failed != 0 || last.Cooling != 0 || requests.Load() != 2 {
				t.Fatalf("source did not recover: progress=%+v requests=%d", last, requests.Load())
			}
		})
	}
}

func TestCancelledSearchDoesNotCoolSource(t *testing.T) {
	started := make(chan struct{})
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"7","vod_name":"示例"}]}`)
	}))
	defer srv.Close()
	root := &config.Config{URL: srv.URL, Sites: []config.Site{{Key: "demo", Kind: config.KindJSON, API: srv.URL}}}
	options := Options{Query: "示例", SearchCache: &SearchCache{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Scan(ctx, root, options, func(Event) {}) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("search did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled search did not stop")
	}
	results, last := scanSearch(t, root, options)
	if len(results) != 1 || last.Cooling != 0 || requests.Load() != 2 {
		t.Fatalf("cancellation poisoned cache: progress=%+v requests=%d", last, requests.Load())
	}
}

func TestSearchCacheRetainsGoodPagesAndBoundsMemory(t *testing.T) {
	cache := &SearchCache{}
	key := searchKey{origin: "demo", query: "示例", page: 1}
	page := catalog.Page{Videos: []catalog.Video{{ID: "7", Name: "示例"}}, HasNext: true}
	cache.put(key, page, nil)
	failed := key
	failed.page = 2
	cache.put(failed, catalog.Page{}, context.DeadlineExceeded)
	got, err, ok := cache.get(key)
	if !ok || err != nil || len(got.Videos) != 1 || !got.HasNext {
		t.Fatal("a failed later page hid valid cached results")
	}
	for i := range searchCacheLimit + 1 {
		next := key
		next.query = fmt.Sprint(i)
		cache.put(next, page, nil)
	}
	if len(cache.entries) > searchCacheLimit {
		t.Fatal("cache entry limit exceeded")
	}
	page.Videos[0].Content = strings.Repeat("x", searchCacheBudget/2)
	cache.put(key, page, nil)
	key.query = "另一部"
	cache.put(key, page, nil)
	if cache.bytes > searchCacheBudget {
		t.Fatal("cache byte budget exceeded")
	}
	page.Videos[0].Content = strings.Repeat("x", searchCacheBudget)
	key.query = "超大响应"
	cache.put(key, page, nil)
	if _, _, ok := cache.get(key); ok {
		t.Fatal("oversized page was cached")
	}
}
