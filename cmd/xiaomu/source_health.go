package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	siteHealthStoreLimit = 2048
	siteHealthTTL        = 30 * time.Minute
)

// siteHealthStore remembers site entry keys that failed live checks (home feed,
// speed test, or opening the site). Broken sites are filtered out on the
// sources page and skipped during search until a speed test, config refresh,
// or successful load recovers them.
type siteHealthStore struct {
	failed map[string]time.Time
	path   string
	writer *siteHealthWriter
}

type siteHealthWriter struct {
	mu      sync.Mutex
	pending map[string]time.Time
	dirty   bool
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	path    string
	err     error
}

func readSiteHealth(path string) (map[string]time.Time, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries map[string]time.Time
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("读取站点状态记录：%w", err)
	}
	return entries, nil
}

func (a *app) restoreSiteHealth() {
	sourcePath, err := savedSourcePath()
	if err != nil {
		return
	}
	a.siteHealth.path = filepath.Join(filepath.Dir(sourcePath), "source-health.json")
	entries, err := readSiteHealth(a.siteHealth.path)
	if err != nil {
		a.status = err.Error()
		return
	}
	a.siteHealth.failed = entries
}

func (a *app) isSiteFailed(key string) bool {
	if key == "" || a.siteHealth.failed == nil {
		return false
	}
	at, ok := a.siteHealth.failed[key]
	if !ok {
		return false
	}
	// Automatically allow sites to recover after a cool-down window so
	// transient network blips never banish a source forever.
	if !at.IsZero() && time.Since(at) >= siteHealthTTL {
		return false
	}
	return true
}

func (a *app) markSiteFailed(key string) {
	if key == "" || a.closed {
		return
	}
	if a.siteHealth.failed == nil {
		a.siteHealth.failed = map[string]time.Time{}
	}
	now := time.Now()
	if at, already := a.siteHealth.failed[key]; already {
		if at.IsZero() || now.Sub(at) < siteHealthTTL {
			return
		}
	} else if len(a.siteHealth.failed) >= siteHealthStoreLimit {
		oldest, at := "", time.Time{}
		for k, v := range a.siteHealth.failed {
			if at.IsZero() || v.Before(at) {
				oldest, at = k, v
			}
		}
		delete(a.siteHealth.failed, oldest)
	}
	a.siteHealth.failed[key] = now
	a.scheduleSiteHealthSave()
}

func (a *app) markSiteHealthy(key string) {
	if key == "" || a.closed || a.siteHealth.failed == nil {
		return
	}
	if _, exists := a.siteHealth.failed[key]; !exists {
		return
	}
	delete(a.siteHealth.failed, key)
	a.scheduleSiteHealthSave()
}

func (a *app) clearSiteHealth() {
	if len(a.siteHealth.failed) == 0 {
		return
	}
	a.siteHealth.failed = nil
	a.scheduleSiteHealthSave()
}

func (s *siteHealthStore) snapshotKeys() map[string]bool {
	if len(s.failed) == 0 {
		return nil
	}
	now := time.Now()
	out := make(map[string]bool, len(s.failed))
	for k, at := range s.failed {
		if !at.IsZero() && now.Sub(at) >= siteHealthTTL {
			continue
		}
		out[k] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *app) scheduleSiteHealthSave() {
	s := &a.siteHealth
	if s.path == "" {
		return
	}
	snap := maps.Clone(s.failed)
	if snap == nil {
		snap = map[string]time.Time{}
	}
	if a.dispatch == nil {
		_ = writeJSONFile(s.path, snap)
		return
	}
	if s.writer == nil {
		w := &siteHealthWriter{
			wake: make(chan struct{}, 1),
			stop: make(chan struct{}),
			done: make(chan struct{}),
			path: s.path,
		}
		s.writer = w
		go w.run()
	}
	w := s.writer
	w.mu.Lock()
	w.pending = snap
	w.dirty = true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *siteHealthWriter) run() {
	defer close(w.done)
	for {
		closing := false
		select {
		case <-w.stop:
			closing = true
		case <-w.wake:
		}
		if !closing {
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-timer.C:
			case <-w.stop:
				closing = true
			}
			timer.Stop()
		}
		w.mu.Lock()
		dirty := w.dirty
		snap := w.pending
		w.dirty = false
		w.mu.Unlock()
		if dirty {
			w.err = writeJSONFile(w.path, snap)
		}
		if closing {
			return
		}
	}
}

func (s *siteHealthStore) flush() error {
	w := s.writer
	if w == nil {
		return nil
	}
	close(w.stop)
	<-w.done
	s.writer = nil
	return w.err
}
