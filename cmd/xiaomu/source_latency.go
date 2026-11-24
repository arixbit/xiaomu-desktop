package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/source"
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
	writer  *latencyWriter
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

// sourceSpeedTest is one manual re-measurement of every site on the
// sources page. It runs beside the UI, like the discovery refresh:
// workers measure, and the UI thread records, so a slow site never
// blocks the page and the labels fill in as results arrive.
type sourceSpeedTest struct {
	cancel     context.CancelFunc
	generation uint64
	running    bool
	done       int
	total      int
}

// speedTestEntries is every supported site the sources page lists, each
// once by its latency key. Unsupported sites are skipped, as the page
// skips them.
func (a *app) speedTestEntries() []library.Entry {
	seen := map[string]bool{}
	var entries []library.Entry
	for _, document := range a.library.documents {
		for _, site := range document.doc.Sites {
			if site.Kind.Unsupported() {
				continue
			}
			entry := library.Entry{Site: site, Config: document.doc, Line: document.line}
			if seen[entry.Key()] {
				continue
			}
			seen[entry.Key()] = true
			entries = append(entries, entry)
		}
	}
	return entries
}

// measureSite is one site answering: open it and close it again. For a
// CMS site that is one list request; for a script site it is the
// engine load and home call a search would do anyway. A site that does
// not answer within the timeout measures nothing, and a failed open
// records nothing — the same rule as a failed detail read.
func measureSite(ctx context.Context, entry library.Entry, assets string) time.Duration {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	release, err := source.AcquireBackground(ctx)
	if err != nil {
		return 0
	}
	defer release()
	start := time.Now()
	session, _, err := source.Open(ctx, entry.Site, assets)
	if err != nil {
		return 0
	}
	session.Close()
	return time.Since(start)
}

type speedTestResult struct {
	entry library.Entry
	d     time.Duration
}

// startSourceSpeedTest re-measures every site on the sources page, one
// measurement per site per press. Four workers bound the network work,
// as the library scan does; the button stays disabled until the last
// site answers or times out.
func (a *app) cancelSourceSpeedTest() {
	s := &a.speedTest
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.generation++
	s.running = false
}

func (a *app) startSourceSpeedTest() {
	if a.closed || a.speedTest.running || a.library.root == nil {
		return
	}
	entries := a.speedTestEntries()
	if len(entries) == 0 {
		a.setNotice("当前还没有可测速的站点")
		return
	}
	a.cancelSourceSpeedTest()
	ctx, cancel := context.WithCancel(context.Background())
	a.speedTest.cancel = cancel
	generation := a.speedTest.generation
	a.speedTest.running, a.speedTest.done, a.speedTest.total = true, 0, len(entries)
	assets := strings.TrimSpace(a.browser.assets)
	dispatch := a.dispatch
	update := func(fn func()) {
		guarded := func() {
			if !a.closed && a.speedTest.generation == generation {
				fn()
			}
		}
		if dispatch == nil {
			guarded()
		} else {
			dispatch(guarded)
		}
	}
	work := func() {
		defer cancel()
		jobs := make(chan library.Entry)
		results := make(chan speedTestResult, 4)
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				for entry := range jobs {
					if ctx.Err() != nil {
						return
					}
					result := speedTestResult{entry: entry, d: measureSite(ctx, entry, assets)}
					select {
					case <-ctx.Done():
						return
					case results <- result:
					}
				}
			})
		}
		go func() {
			defer close(jobs)
			for _, entry := range entries {
				if ctx.Err() != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case jobs <- entry:
				}
			}
		}()
		go func() {
			wg.Wait()
			close(results)
		}()
		for r := range results {
			if ctx.Err() != nil {
				continue
			}
			r := r
			update(func() {
				key := r.entry.Key()
				if r.d > 0 {
					a.recordLatency(key, r.d)
					a.markSiteHealthy(key)
				} else {
					a.markSiteFailed(key)
				}
				a.speedTest.done++
				if a.speedTest.done >= a.speedTest.total {
					a.speedTest.running = false
					a.speedTest.cancel = nil
				}
			})
		}
	}
	if dispatch == nil {
		work()
	} else {
		go work()
	}
}
