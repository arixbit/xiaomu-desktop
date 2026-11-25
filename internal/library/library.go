// Package library discovers imported depots and streams videos from their sites.
// Each result keeps its originating configuration for detail and playback.
package library

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/source"
)

const (
	workers                 = source.BackgroundLimit
	interactiveWorkers      = source.InteractiveLimit
	defaultBackgroundTime   = 12 * time.Second
	defaultInteractiveTime  = 8 * time.Second
	unknownLatencyThreshold = 2 * time.Second
)

// Entry identifies a site and the playback parsers belonging to its line.
type Entry struct {
	Site   config.Site
	Config *config.Config
	Line   string
}

// Key distinguishes identical video IDs in different sites or configurations.
func (e Entry) Key() string {
	return e.Config.URL + "\x00" + e.Site.Key + "\x00" + e.Site.API + "\x00" + e.Site.Ext
}

// Label is the human-readable origin of a video.
func (e Entry) Label() string {
	name := e.Site.Name
	if name == "" {
		name = e.Site.Key
	}
	if e.Line != "" {
		return e.Line + " · " + name
	}
	return name
}

// Item is a video with enough information to open it without selecting a site.
type Item struct {
	Entry Entry
	Video catalog.Video
}

// Target requests another page from one particular site.
type Target struct {
	Entry Entry
	Page  int
}

// Options controls a home/search scan. Cached documents, Latency and
// PreferredKeys are read-only snapshots.
type Options struct {
	Assets        string
	Query         string
	Cached        map[string]*config.Config
	More          []Target
	Fetch         config.Fetcher
	Timeout       time.Duration
	SearchCache   *SearchCache
	Skip          map[string]bool
	Interactive   bool
	Latency       map[string]time.Duration
	PreferredKeys map[string]bool
}

// Result is one site's completed page; failures do not discard other results.
type Result struct {
	Target Target
	Page   catalog.Page
}

// Event is delivered serially as configurations and site requests complete.
// Duration is how long the site's own request took, when the event carries
// a site result; a reply served from the search cache has no duration,
// because no network happened and there is nothing to measure.
type Event struct {
	Document                          *config.Config
	URL, Line                         string
	Result                            *Result
	Err                               error
	Duration                          time.Duration
	Completed, Total, Skipped, Failed int
	Cooling, SkippedHealth            int
}

type task struct {
	url, line string
	depth     int
	target    *Target
}

type completion struct {
	task     task
	doc      *config.Config
	page     catalog.Page
	err      error
	cooling  bool
	duration time.Duration
	cached   bool
}

// Scan loads all lines in an imported depot and streams recommendations (empty
// Query) or search results. Background scans stay bounded to 4 workers;
// interactive foreground searches scale up lightweight HTTP sites while still
// capping heavy script engines. Every task has a deadline; cancellation waits
// for workers and releases all runtimes.
func Scan(ctx context.Context, root *config.Config, options Options, emit func(Event)) error {
	if root == nil {
		return fmt.Errorf("请先添加片库")
	}
	workerCount := workers
	if options.Interactive {
		workerCount = interactiveWorkers
	}
	if options.Timeout <= 0 {
		if options.Interactive {
			options.Timeout = defaultInteractiveTime
		} else {
			options.Timeout = defaultBackgroundTime
		}
	}
	options.Query = strings.TrimSpace(options.Query)
	jobs := make(chan task)
	completed := make(chan completion, workerCount)
	var wg sync.WaitGroup
	for range workerCount {
		wg.Go(func() {
			for work := range jobs {
				if ctx.Err() != nil {
					return
				}
				result := execute(ctx, work, options)
				select {
				case completed <- result:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	defer func() { close(jobs); wg.Wait() }()

	var queue []task
	seenDocs, seenSites := map[string]bool{root.URL: true}, map[string]bool{}
	progress := Event{}
	enqueue := func(doc *config.Config, line string, depth int) {
		for _, child := range doc.Lines {
			if seenDocs[child.URL] {
				continue
			}
			seenDocs[child.URL] = true
			queue = append(queue, task{url: child.URL, line: child.Name, depth: depth + 1})
		}
		for _, site := range doc.Sites {
			entry := Entry{Site: site, Config: doc, Line: line}
			if seenSites[entry.Key()] {
				continue
			}
			seenSites[entry.Key()] = true
			if site.Kind.Unsupported() || (options.Query != "" && !site.SearchEnabled()) {
				progress.Skipped++
				continue
			}
			if options.Query != "" && options.Skip[entry.Key()] {
				progress.Skipped++
				progress.SkippedHealth++
				continue
			}
			target := Target{Entry: entry, Page: 1}
			queue = append(queue, task{target: &target})
			progress.Total++
		}
	}
	if len(options.More) > 0 {
		for _, target := range options.More {
			queue = append(queue, task{target: &target})
			progress.Total++
		}
	} else {
		enqueue(root, root.Line, 0)
	}
	initial := progress
	initial.Document, initial.URL, initial.Line = root, root.URL, root.Line
	emit(initial)
	running, runningDocs, runningScripts := 0, 0, 0
	docLimit := max(2, workerCount/2)
	served := map[string]int{}
	for len(queue) > 0 || running > 0 {
		var send chan task
		var next task
		nextIndex := -1
		if len(queue) > 0 {
			// Keep room for videos while slow depot lines are still loading,
			// prefer preferred/fast lightweight sites, and avoid dispatching
			// extra script sites while the script pool is already saturated.
			docIndex, siteIndex := -1, -1
			for i, candidate := range queue {
				if candidate.target == nil {
					if docIndex < 0 {
						docIndex = i
					}
					continue
				}
				if runningScripts >= source.ScriptLimit && isScriptSite(candidate.target.Entry.Site.Kind) {
					continue
				}
				if siteIndex < 0 || betterSiteTask(candidate, queue[siteIndex], options, served) {
					siteIndex = i
				}
			}
			nextIndex = siteIndex
			if docIndex >= 0 && (siteIndex < 0 || runningDocs < docLimit) {
				nextIndex = docIndex
			}
			if nextIndex >= 0 {
				send, next = jobs, queue[nextIndex]
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case send <- next:
			queue = append(queue[:nextIndex], queue[nextIndex+1:]...)
			if next.target != nil {
				served[next.target.Entry.Config.URL]++
				if isScriptSite(next.target.Entry.Site.Kind) {
					runningScripts++
				}
			} else {
				runningDocs++
			}
			running++
		case done := <-completed:
			running--
			if done.task.target == nil {
				runningDocs--
			} else if isScriptSite(done.task.target.Entry.Site.Kind) {
				runningScripts--
			}
			if done.cooling {
				progress.Cooling++
			} else if done.err != nil {
				progress.Failed++
			}
			if done.task.target != nil {
				progress.Completed++
			} else if done.doc != nil {
				enqueue(done.doc, done.task.line, done.task.depth)
			}
			event := progress
			event.Err = done.err
			event.Document, event.URL, event.Line = done.doc, done.task.url, done.task.line
			if done.task.target != nil {
				event.Result = &Result{Target: *done.task.target, Page: done.page}
				if !done.cached {
					event.Duration = done.duration
				}
			}
			emit(event)
		}
	}
	return ctx.Err()
}

func taskPriority(work task) int {
	switch work.target.Entry.Site.Kind {
	case config.KindJSON, config.KindXML, config.KindPanWeb, config.KindPanShare, config.KindAList:
		return 0
	default:
		return 1
	}
}

func isScriptSite(kind config.Kind) bool {
	return taskPriority(task{target: &Target{Entry: Entry{Site: config.Site{Kind: kind}}}}) != 0
}

func betterSiteTask(a, b task, options Options, served map[string]int) bool {
	keyA, keyB := a.target.Entry.Key(), b.target.Entry.Key()
	prefA, prefB := options.PreferredKeys[keyA], options.PreferredKeys[keyB]
	if prefA != prefB {
		return prefA
	}
	priA, priB := taskPriority(a), taskPriority(b)
	if priA != priB {
		return priA < priB
	}
	latA, hasLatA := options.Latency[keyA]
	latB, hasLatB := options.Latency[keyB]
	if hasLatA || hasLatB {
		effA := unknownLatencyThreshold
		if hasLatA && latA > 0 {
			effA = latA
		}
		effB := unknownLatencyThreshold
		if hasLatB && latB > 0 {
			effB = latB
		}
		if effA != effB {
			return effA < effB
		}
	}
	return served[a.target.Entry.Config.URL] < served[b.target.Entry.Config.URL]
}

func acquireScanSlot(ctx context.Context, interactive bool) (func(), error) {
	if interactive {
		return source.AcquireInteractive(ctx)
	}
	return source.AcquireBackground(ctx)
}

func execute(parent context.Context, work task, options Options) (out completion) {
	ctx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()
	out = completion{task: work}
	start := time.Now()
	// Windows' clock can read zero for a quick local reply; a request
	// that ran still took time, and zero means "nothing measured".
	defer func() { out.duration = max(time.Since(start), time.Nanosecond) }()
	if work.target == nil {
		if work.depth > 4 {
			out.err = fmt.Errorf("配置仓库嵌套超过 4 层")
			return out
		}
		if cached := options.Cached[work.url]; cached != nil {
			out.doc = cached
			return out
		}
		release, err := acquireScanSlot(ctx, options.Interactive)
		if err != nil {
			out.err = err
			return out
		}
		defer release()
		start = time.Now()
		out.doc, out.err = config.Read(ctx, work.url, options.Fetch)
		return out
	}
	target := work.target
	if err := ctx.Err(); err != nil {
		out.err = err
		return out
	}
	if options.Query != "" && options.SearchCache != nil {
		key := searchKey{origin: target.Entry.Key(), assets: options.Assets, kind: target.Entry.Site.Kind, query: options.Query, page: max(target.Page, 1)}
		if page, err, ok := options.SearchCache.get(key); ok {
			out.page, out.err, out.cooling, out.cached = page, err, err != nil, true
			return out
		}
		defer func() {
			// Stopping or replacing a search must not mark its sources as failed.
			if parent.Err() == nil {
				options.SearchCache.put(key, out.page, out.err)
			}
		}()
	}
	var session source.Session
	release, acquireErr := acquireScanSlot(ctx, options.Interactive)
	if acquireErr != nil {
		out.err = acquireErr
		return out
	}
	defer release()
	if isScriptSite(target.Entry.Site.Kind) {
		releaseScript, scriptErr := source.AcquireScript(ctx)
		if scriptErr != nil {
			out.err = scriptErr
			return out
		}
		defer releaseScript()
	}
	// Queueing for the shared budget is not the source's measured latency.
	start = time.Now()
	var categories []catalog.Category
	var err error
	if options.Query == "" {
		session, categories, err = source.Open(ctx, target.Entry.Site, options.Assets)
	} else {
		session, err = source.OpenContent(ctx, target.Entry.Site, options.Assets)
	}
	if err != nil {
		out.err = err
		return out
	}
	defer session.Close()
	if options.Query == "" {
		out.page, out.err = source.Home(ctx, session, categories)
		// Keep the home feed balanced across sites. Full lists remain browseable.
		out.page.Videos = out.page.Videos[:min(12, len(out.page.Videos))]
	} else {
		out.page, out.err = session.Search(ctx, options.Query, target.Page)
	}
	if ctx.Err() != nil {
		out.err = ctx.Err()
	}
	return out
}
