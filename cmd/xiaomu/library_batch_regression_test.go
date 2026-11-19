package main

import (
	"encoding/json"
	"fmt"
	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestFixBurstSearch(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var videos []catalog.Video
		for i := range 100 {
			videos = append(videos, catalog.Video{ID: catalog.ID(fmt.Sprint(i)), Name: fmt.Sprintf("合成 %s %d", r.URL.Path, i)})
		}
		json.NewEncoder(w).Encode(map[string]any{"list": videos, "pagecount": 1})
	}))
	defer srv.Close()
	root := &config.Config{URL: srv.URL + "/config"}
	for i := range 100 {
		root.Sites = append(root.Sites, config.Site{Key: fmt.Sprint(i), API: fmt.Sprintf("%s/%d", srv.URL, i), Kind: config.KindJSON, Type: 1})
	}
	q := make(chan func(), 10000)
	a := &app{dispatch: func(f func()) { q <- f }}
	a.library = libraryBrowser{root: root, cached: map[string]*config.Config{}}
	a.latency.path = filepath.Join(t.TempDir(), "latency.json")
	defer a.close()
	start := time.Now()
	a.startScan(&a.library.search, "合成", false)
	deadline := time.Now().Add(3 * time.Second)
	for requests.Load() < 100 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(25 * time.Millisecond) // one display frame with all replies ready
	queued := len(q)
	var total, maxCall time.Duration
	count := 0
	fixPump(t, q, func() bool {
		for len(q) > 0 {
			fn := <-q
			now := time.Now()
			fn()
			d := time.Since(now)
			total += d
			if d > maxCall {
				maxCall = d
			}
			count++
		}
		return !a.library.search.running
	}, 3*time.Second)
	t.Logf("100 sites / 10000 results: queued=%d callbacks=%d UI total=%v max=%v full completion=%v", queued, count, total, maxCall, time.Since(start))
	if queued > 12 {
		t.Errorf("burst not coalesced: %d queued callbacks", queued)
	}
	if total > 100*time.Millisecond || maxCall > 100*time.Millisecond {
		t.Errorf("UI work over 100ms: total=%v max=%v", total, maxCall)
	}
	if len(a.library.search.items) != 10000 || len(a.library.search.groups) != 10000 {
		t.Fatal("full results lost")
	}
	a.close()
	saved, err := readLatency(a.latency.path)
	if err != nil || len(saved) != 100 {
		t.Fatalf("close lost valid measurements: %d %v", len(saved), err)
	}
}
