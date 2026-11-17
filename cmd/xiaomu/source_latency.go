package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// latencyStoreLimit is how many sites keep a measured latency. A depot
// holds at most a few hundred sites, so the file stays small and the
// oldest measurements go away instead of piling up.
const latencyStoreLimit = 256

// latencyEntry is one site's last measured wait, and when it was measured.
// The key is the site's Entry.Key(): the configuration it came from plus
// its own key, API and extension — stable across restarts and titles for
// the same site, and the same site measured while searching another title
// still counts.
type latencyEntry struct {
	Ms int64     `json:"ms"`
	At time.Time `json:"at"`
}

// latencyStore keeps per-site latencies, only on this Mac. It never holds
// a config URL or a media address — the key is a site identity, and the
// file lives beside the saved source, like the search history.
type latencyStore struct {
	entries map[string]latencyEntry
	path    string
}

// record saves a fresh measurement: the write goes first, so a failed
// save cannot appear saved, and the oldest entry goes when the store
// is full. A measurement of zero or less is not a measurement — a
// cached reply, for example, never reaches here.
func (s *latencyStore) record(key string, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	kept := map[string]latencyEntry{}
	for k, e := range s.entries {
		kept[k] = e
	}
	if _, ok := kept[key]; !ok && len(kept) >= latencyStoreLimit {
		oldest, at := "", time.Time{}
		for k, e := range kept {
			if at.IsZero() || e.At.Before(at) {
				oldest, at = k, e.At
			}
		}
		delete(kept, oldest)
	}
	kept[key] = latencyEntry{Ms: d.Milliseconds(), At: time.Now()}
	if s.path != "" {
		if err := writeJSONFile(s.path, kept); err != nil {
			return err
		}
	}
	s.entries = kept
	return nil
}

// get returns a site's last measured latency, if one was ever recorded.
func (s *latencyStore) get(key string) (time.Duration, bool) {
	if s.entries == nil {
		return 0, false
	}
	e, ok := s.entries[key]
	if !ok || e.Ms <= 0 {
		return 0, false
	}
	return time.Duration(e.Ms) * time.Millisecond, true
}

func readLatency(path string) (map[string]latencyEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries map[string]latencyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("读取测速记录：%w", err)
	}
	return entries, nil
}

func (a *app) restoreLatency() {
	sourcePath, err := savedSourcePath()
	if err != nil {
		return
	}
	a.latency.path = filepath.Join(filepath.Dir(sourcePath), "source-latency.json")
	entries, err := readLatency(a.latency.path)
	if err != nil {
		a.status = err.Error()
		return
	}
	a.latency.entries = entries
}

// formatLatency is the latency shown on a source card: milliseconds
// below a second, one decimal above it. Nothing is shown for a source
// that was never measured.
func formatLatency(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}
