package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

func TestLatencyStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source-latency.json")
	s := latencyStore{path: path}
	if _, ok := s.get("site-a"); ok {
		t.Fatal("unmeasured site has a latency")
	}
	if err := s.record("site-a", 850*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := s.record("site-b", 2300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if d, ok := s.get("site-a"); !ok || d != 850*time.Millisecond {
		t.Fatalf("site-a latency = %v, %v", d, ok)
	}
	if d, ok := s.get("site-b"); !ok || d != 2300*time.Millisecond {
		t.Fatalf("site-b latency = %v, %v", d, ok)
	}
	// A fresh store on the same file reads the measurements back.
	again, err := readLatency(path)
	if err != nil {
		t.Fatal(err)
	}
	if again["site-a"].Ms != 850 || again["site-b"].Ms != 2300 {
		t.Fatalf("stored latencies = %+v", again)
	}
	// Zero and negative durations are not measurements.
	if err := s.record("site-c", 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.get("site-c"); ok {
		t.Fatal("zero duration recorded as a measurement")
	}
}

func TestLatencyStoreDropsOldestWhenFull(t *testing.T) {
	now := time.Now()
	entries := map[string]latencyEntry{}
	for i := 0; i < latencyStoreLimit; i++ {
		entries["site-"+strconv.Itoa(i)] = latencyEntry{Ms: 100, At: now.Add(-time.Duration(i) * time.Minute)}
	}
	s := latencyStore{entries: entries}
	if err := s.record("site-new", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(s.entries) != latencyStoreLimit {
		t.Fatalf("store size = %d, want %d", len(s.entries), latencyStoreLimit)
	}
	if _, ok := s.get("site-new"); !ok {
		t.Fatal("new measurement did not survive")
	}
	// site-255 is the oldest measurement and must be the one dropped.
	if _, ok := s.get("site-255"); ok {
		t.Fatal("oldest measurement was not dropped")
	}
}

func TestFormatLatency(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0ms"},
		{850 * time.Millisecond, "850ms"},
		{999 * time.Millisecond, "999ms"},
		{2300 * time.Millisecond, "2.3s"},
		{10 * time.Second, "10.0s"},
	} {
		if got := formatLatency(tc.in); got != tc.want {
			t.Fatalf("formatLatency(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMeasureSite(t *testing.T) {
	_, doc := suggestionTestServer(t)
	entry := library.Entry{Site: doc.Sites[0], Config: doc}
	if d := measureSite(context.Background(), entry, ""); d <= 0 {
		t.Fatal("a live CMS site measured nothing")
	}
	// A site that does not answer records nothing.
	dead := library.Entry{
		Site:   config.Site{Key: "dead", Name: "死了", Type: 1, Kind: config.KindJSON, API: "http://127.0.0.1:1/api"},
		Config: doc,
	}
	if d := measureSite(context.Background(), dead, ""); d != 0 {
		t.Fatalf("a dead site measured %v", d)
	}
}

func TestSourcePageShowsLatency(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	a.library.documents = []libraryDocument{{doc: doc, open: true}}
	entry := library.Entry{Site: doc.Sites[0], Config: doc}
	if err := a.latency.record(entry.Key(), 850*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	if err := tt.Click("片源管理"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("850ms") || !tt.HasText("示例来源") {
		t.Fatalf("latency label missing: %v", tt.Texts())
	}
}

func TestSourceSpeedTestUpdatesStore(t *testing.T) {
	// The store records whole milliseconds and treats 0ms as
	// unmeasured, so the site answers with a small delay: a
	// loopback server alone can answer in under a millisecond.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		fmt.Fprint(w, `{"list":[]}`)
	}))
	t.Cleanup(srv.Close)
	doc := &config.Config{URL: srv.URL + "/config", Sites: []config.Site{{Key: "site", Name: "示例来源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/api"}}}
	a := newSuggestionTestApp(t, doc)
	a.library.documents = []libraryDocument{{doc: doc, open: true}}
	a.startSourceSpeedTest()
	if a.speedTest.running {
		t.Fatal("the re-test did not finish")
	}
	if a.speedTest.done != 1 || a.speedTest.total != 1 {
		t.Fatalf("progress = %d/%d, want 1/1", a.speedTest.done, a.speedTest.total)
	}
	entry := library.Entry{Site: doc.Sites[0], Config: doc}
	if d, ok := a.latency.get(entry.Key()); !ok || d <= 0 {
		t.Fatalf("the re-test recorded nothing: %v %v", d, ok)
	}
}

func TestCandidatesSortMeasuredFirst(t *testing.T) {
	a, seeds := newDetailTestApp(t, 5)
	key := func(i int) string { return seeds[i].Entry.Key() }
	// seed 2 is fastest, then seed 4, then seed 0; seed 3 was never
	// measured; seed 1 failed for this title.
	if err := a.latency.record(key(0), 900*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := a.latency.record(key(2), 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := a.latency.record(key(4), 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	a.title.failed[itemKey(seeds[1])] = "读取失败"
	got := a.candidates()
	want := []int{2, 4, 0, 3, 1}
	if len(got) != len(want) {
		t.Fatalf("candidates = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Entry.Site.Key != seeds[w].Entry.Site.Key {
			t.Fatalf("position %d = %s, want %s", i, got[i].Entry.Site.Key, seeds[w].Entry.Site.Key)
		}
	}
}

func TestDetailSourceCardShowsLatency(t *testing.T) {
	a, seeds := newDetailTestApp(t, 3)
	if err := a.latency.record(seeds[0].Entry.Key(), 2300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := a.latency.record(seeds[1].Entry.Key(), 850*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// seeds[2] is never measured and must show no latency at all.
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if !tt.HasText("2.3s") || !tt.HasText("850ms") {
		t.Fatalf("latency labels missing: %v", tt.Texts())
	}
	// The horizontal source strip shows the fastest source first on the left,
	// so seed 1 sits before seed 0 even though seed 0 was listed first.
	first, ok := tt.Find("来源 " + seeds[1].Entry.Label())
	if !ok {
		t.Fatal("fastest card is missing")
	}
	second, ok := tt.Find("来源 " + seeds[0].Entry.Label())
	if !ok {
		t.Fatal("slower card is missing")
	}
	if first.X >= second.X {
		t.Fatalf("fastest chip is not first: first=%+v second=%+v", first, second)
	}
}
