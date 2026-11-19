package main

import (
	"maps"
	"sort"
	"sync"
	"time"
)

// Only the writer goroutine touches its private store. Published maps are
// immutable; the UI never waits for a disk write while browsing results.
type latencyWriter struct {
	mu      sync.Mutex
	pending map[string]latencyEntry
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	store   latencyStore
	err     error
}

func keepLatency(entries map[string]latencyEntry, key string, e latencyEntry) {
	entries[key] = e
	if len(entries) <= latencyStoreLimit {
		return
	}
	oldest := ""
	var at time.Time
	for k, v := range entries {
		if at.IsZero() || v.At.Before(at) {
			oldest, at = k, v.At
		}
	}
	delete(entries, oldest)
}

func (s *latencyStore) recordBatch(batch map[string]latencyEntry) error {
	if len(batch) == 0 {
		return nil
	}
	kept := maps.Clone(s.entries)
	if kept == nil {
		kept = map[string]latencyEntry{}
	}
	keys := make([]string, 0, len(batch))
	for key := range batch {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return batch[keys[i]].At.Before(batch[keys[j]].At) })
	for _, key := range keys {
		keepLatency(kept, key, batch[key])
	}
	if s.path != "" {
		if err := writeJSONFile(s.path, kept); err != nil {
			return err
		}
	}
	s.entries = kept
	return nil
}

func (a *app) recordLatency(key string, d time.Duration) {
	if d <= 0 || a.closed {
		return
	}
	if a.dispatch == nil {
		_ = a.latency.record(key, d)
		return
	}
	s := &a.latency
	if s.writer == nil {
		writer := &latencyWriter{pending: map[string]latencyEntry{}, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), store: latencyStore{entries: maps.Clone(s.entries), path: s.path}}
		s.writer = writer
		dispatch := a.dispatch
		go writer.run(func(entries map[string]latencyEntry) {
			dispatch(func() {
				if !a.closed {
					s.entries = entries
				}
			})
		})
	}
	w := s.writer
	w.mu.Lock()
	keepLatency(w.pending, key, latencyEntry{Ms: d.Milliseconds(), At: time.Now()})
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *latencyWriter) run(publish func(map[string]latencyEntry)) {
	defer close(w.done)
	for {
		closing := false
		select {
		case <-w.stop:
			closing = true
		case <-w.wake:
		}
		if !closing {
			timer := time.NewTimer(16 * time.Millisecond)
			select {
			case <-timer.C:
			case <-w.stop:
				closing = true
			}
			timer.Stop()
		}
		w.mu.Lock()
		batch := w.pending
		w.pending = map[string]latencyEntry{}
		w.mu.Unlock()
		w.err = w.store.recordBatch(batch)
		if w.err == nil {
			if len(batch) > 0 && !closing {
				publish(w.store.entries)
			}
		} else if !closing {
			// Retain unsaved values for the next write or the close flush; a failed
			// write never changes what the UI reports as saved.
			w.mu.Lock()
			for k, e := range batch {
				if newer, ok := w.pending[k]; !ok || newer.At.Before(e.At) {
					keepLatency(w.pending, k, e)
				}
			}
			w.mu.Unlock()
		}
		if closing {
			return
		}
	}
}

func (s *latencyStore) flush() error {
	w := s.writer
	if w == nil {
		return nil
	}
	close(w.stop)
	<-w.done
	s.entries = w.store.entries
	s.writer = nil
	return w.err
}
