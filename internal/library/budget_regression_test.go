package library

import (
	"context"
	"fmt"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFixScanSharedBudget(t *testing.T) {
	var active, peak, requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(40 * time.Millisecond):
		}
		fmt.Fprint(w, `{"list":[],"pagecount":1}`)
	}))
	defer srv.Close()
	root := &config.Config{URL: srv.URL + "/config"}
	for i := range 12 {
		root.Sites = append(root.Sites, config.Site{Key: fmt.Sprint(i), API: srv.URL, Kind: config.KindJSON, Type: 1})
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := Scan(context.Background(), root, Options{Query: "normal"}, func(Event) {}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	t.Logf("four simultaneous scans: actual peak=%d requests=%d active=%d", peak.Load(), requests.Load(), active.Load())
	if peak.Load() > 4 {
		t.Errorf("shared limit exceeded: %d", peak.Load())
	}
	if requests.Load() != 48 {
		t.Errorf("normal work starved or lost: %d", requests.Load())
	}
	for range 50 {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Scan(ctx, root, Options{Query: "replacement"}, func(Event) {}) }()
		timer := time.NewTimer(5 * time.Millisecond)
		<-timer.C
		cancel()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("cancelled scan retained workers")
		}
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatalf("old requests remain: %d", active.Load())
	}
	t.Log("50 replacements: old requests=0")
}
