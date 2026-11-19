package main

import (
	"fmt"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFixSpeedLifecycle(t *testing.T) {
	for _, action := range []string{"replace", "remove", "close"} {
		t.Run(action, func(t *testing.T) {
			var active, peak, requests atomic.Int64
			var fast atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				n := active.Add(1)
				defer active.Add(-1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				if !fast.Load() {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(150 * time.Millisecond):
					}
				}
				fmt.Fprint(w, `{"class":[],"list":[],"pagecount":1}`)
			}))
			defer srv.Close()
			root := &config.Config{URL: srv.URL + "/old"}
			for i := range 100 {
				root.Sites = append(root.Sites, config.Site{Key: fmt.Sprint(i), API: fmt.Sprintf("%s/site/%d", srv.URL, i), Kind: config.KindJSON, Type: 1})
			}
			q := make(chan func(), 10000)
			a := &app{dispatch: func(f func()) { q <- f }}
			defer a.close()
			a.discovery.tab = "sites"
			a.library = libraryBrowser{root: root, cached: map[string]*config.Config{}, documents: []libraryDocument{{doc: root}}}
			a.favorites.entries = []favoriteEntry{{Name: "合成追剧"}}
			a.startScan(&a.library.feed, "", false)
			a.startScan(&a.library.search, "合成", false)
			a.refreshFollowing()
			a.startSourceSpeedTest()
			time.Sleep(35 * time.Millisecond)
			t.Logf("combined feed/search/follow/speed peak=%d active=%d", peak.Load(), active.Load())
			if peak.Load() > 4 {
				t.Errorf("shared budget exceeded: %d", peak.Load())
			}
			before := requests.Load()
			start := time.Now()
			switch action {
			case "close":
				a.close()
			case "remove":
				a.resetLibrary(nil)
			case "replace":
				a.favorites.entries = nil
				a.resetLibrary(&config.Config{URL: srv.URL + "/new"})
			}
			deadline := time.Now().Add(100 * time.Millisecond)
			for active.Load() != 0 && time.Now().Before(deadline) {
				select {
				case f := <-q:
					f()
				case <-time.After(time.Millisecond):
				}
			}
			elapsed := time.Since(start)
			if active.Load() != 0 {
				t.Errorf("old requests retained after 100ms: %d", active.Load())
			}
			time.Sleep(180 * time.Millisecond)
			for len(q) > 0 {
				(<-q)()
			}
			t.Logf("%s: zero-request wait=%v active=%d new old-config requests=%d", action, elapsed, active.Load(), requests.Load()-before)
			if requests.Load() != before {
				t.Error("cancelled speed test dispatched more work")
			}
			for key := range a.latency.entries {
				if strings.HasPrefix(key, root.URL) {
					t.Error("old configuration wrote a measurement")
				}
			}
			fast.Store(true)
			// Old-version red runs still own a finite speed test. Let that bounded
			// server finish it before leaving, preserving the failed assertions.
			time.Sleep(300 * time.Millisecond)
			for len(q) > 0 {
				(<-q)()
			}
		})
	}
}
