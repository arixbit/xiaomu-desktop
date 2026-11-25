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

func TestSearchEventsCarrySiteDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ac") == "list" {
			fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
			return
		}
		// A slow site, so its measured duration is unmistakably positive.
		time.Sleep(25 * time.Millisecond)
		fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"9","vod_name":"示例"}]}`)
	}))
	defer srv.Close()
	root := &config.Config{URL: srv.URL, Sites: []config.Site{{Key: "only", Kind: config.KindJSON, API: srv.URL + "/a"}}}
	cache := &SearchCache{}
	var live, cached []time.Duration
	run := func(into *[]time.Duration) {
		if err := Scan(context.Background(), root, Options{Query: "示例", SearchCache: cache}, func(e Event) {
			if e.Result != nil {
				*into = append(*into, e.Duration)
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	run(&live)
	if len(live) != 1 || live[0] <= 0 {
		t.Fatalf("live search durations = %v, want one positive value", live)
	}
	// The same search again is served from the cache: no network
	// happened, so there is no duration to report.
	run(&cached)
	if len(cached) != 1 || cached[0] != 0 {
		t.Fatalf("cached search durations = %v, want one zero value", cached)
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

func TestTaskPriorityPlacesPanAndAListAlongsideCMS(t *testing.T) {
	for _, kind := range []config.Kind{config.KindJSON, config.KindXML, config.KindPanWeb, config.KindPanShare, config.KindAList} {
		got := taskPriority(task{target: &Target{Entry: Entry{Site: config.Site{Kind: kind}}}})
		if got != 0 {
			t.Fatalf("taskPriority(%s) = %d, want 0", kind, got)
		}
	}
	for _, kind := range []config.Kind{config.KindDRPY2, config.KindFongMi, config.KindXBPQ} {
		got := taskPriority(task{target: &Target{Entry: Entry{Site: config.Site{Kind: kind}}}})
		if got != 1 {
			t.Fatalf("taskPriority(%s) = %d, want 1", kind, got)
		}
	}
}

func TestInteractiveScanRunsIndependentlyOfBlockedBackground(t *testing.T) {
	// Occupy every background slot with slow background requests.
	bgStarted := make(chan struct{}, workers)
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	bgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bgStarted <- struct{}{}
		<-r.Context().Done()
	}))
	defer bgSrv.Close()
	bgRoot := &config.Config{URL: bgSrv.URL + "/bg"}
	for i := range workers {
		bgRoot.Sites = append(bgRoot.Sites, config.Site{Key: fmt.Sprintf("bg-%d", i), API: bgSrv.URL, Kind: config.KindJSON})
	}
	bgDone := make(chan struct{})
	go func() {
		defer close(bgDone)
		_ = Scan(bgCtx, bgRoot, Options{Query: "背景"}, func(Event) {})
	}()
	for range workers {
		select {
		case <-bgStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("background slots did not fill")
		}
	}

	// An interactive search scales above BackgroundLimit without waiting on background work.
	var active, peak atomic.Int32
	fgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		time.Sleep(40 * time.Millisecond)
		fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"1","vod_name":"示例"}]}`)
	}))
	defer fgSrv.Close()
	fgRoot := &config.Config{URL: fgSrv.URL + "/fg"}
	for i := range 16 {
		fgRoot.Sites = append(fgRoot.Sites, config.Site{Key: fmt.Sprintf("fg-%d", i), API: fmt.Sprintf("%s/%d", fgSrv.URL, i), Kind: config.KindJSON})
	}
	var results int
	err := Scan(context.Background(), fgRoot, Options{Query: "示例", Interactive: true}, func(e Event) {
		if e.Result != nil && e.Err == nil {
			results++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if results != 16 {
		t.Fatalf("results=%d, want 16", results)
	}
	if peak.Load() <= workers || peak.Load() > interactiveWorkers {
		t.Fatalf("interactive peak concurrency = %d, want in (%d, %d]", peak.Load(), workers, interactiveWorkers)
	}

	bgCancel()
	<-bgDone
}

func TestScanSchedulesPreferredAndFasterSitesFirst(t *testing.T) {
	root := &config.Config{URL: "https://example.com/depot"}
	makeTask := func(key string, kind config.Kind) task {
		target := Target{Entry: Entry{Config: root, Site: config.Site{Key: key, API: "https://example.com/" + key, Kind: kind}}, Page: 1}
		return task{target: &target}
	}
	preferred := makeTask("pref", config.KindDRPY2) // even a script site goes first if preferred
	fast := makeTask("fast", config.KindJSON)
	unknown := makeTask("unknown", config.KindJSON)
	slow := makeTask("slow", config.KindJSON)
	script := makeTask("script", config.KindXBPQ)

	opts := Options{
		PreferredKeys: map[string]bool{preferred.target.Entry.Key(): true},
		Latency: map[string]time.Duration{
			fast.target.Entry.Key(): 180 * time.Millisecond,
			slow.target.Entry.Key(): 5 * time.Second,
		},
	}
	served := map[string]int{}
	if !betterSiteTask(preferred, fast, opts, served) {
		t.Fatal("preferred site did not outrank fast site")
	}
	if !betterSiteTask(fast, unknown, opts, served) {
		t.Fatal("fast site did not outrank unmeasured site")
	}
	if !betterSiteTask(unknown, slow, opts, served) {
		t.Fatal("unmeasured site did not outrank very slow site")
	}
	if !betterSiteTask(slow, script, opts, served) {
		t.Fatal("lightweight CMS site did not outrank heavy script site")
	}
}

func TestInteractiveScanCapsConcurrentScriptSites(t *testing.T) {
	var scriptActive, scriptPeak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := scriptActive.Add(1)
		defer scriptActive.Add(-1)
		for old := scriptPeak.Load(); n > old && !scriptPeak.CompareAndSwap(old, n); old = scriptPeak.Load() {
		}
		time.Sleep(35 * time.Millisecond)
		fmt.Fprint(w, `<html><div class="item"><a href="/v/1" title="示例">示例</a></div></html>`)
	}))
	defer srv.Close()
	root := &config.Config{URL: srv.URL + "/config"}
	ext := fmt.Sprintf(`{"搜索url":"%s/s?wd={wd}","数组":"<div class=\"item\">&&</div>","标题":"title=\"&&\"","链接":"href=\"&&\""}`, srv.URL)
	for i := range 10 {
		root.Sites = append(root.Sites, config.Site{Key: fmt.Sprintf("xbpq-%d", i), Kind: config.KindXBPQ, API: "csp_XBPQ", Ext: ext})
	}
	if err := Scan(context.Background(), root, Options{Query: "示例", Interactive: true}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if scriptPeak.Load() > 4 {
		t.Fatalf("script peak concurrency = %d, want <= 4", scriptPeak.Load())
	}
}
