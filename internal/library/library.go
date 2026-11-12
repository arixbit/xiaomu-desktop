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

const workers = 4

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

// Options controls a home/search scan. Cached documents are read-only snapshots.
type Options struct {
	Assets      string
	Query       string
	Cached      map[string]*config.Config
	More        []Target
	Fetch       config.Fetcher
	Timeout     time.Duration
	SearchCache *SearchCache
}

// Result is one site's completed page; failures do not discard other results.
type Result struct {
	Target Target
	Page   catalog.Page
}

// Event is delivered serially as configurations and site requests complete.
type Event struct {
	Document                          *config.Config
	URL, Line                         string
	Result                            *Result
	Err                               error
	Completed, Total, Skipped, Failed int
	Cooling                           int
}

type task struct {
	url, line string
	depth     int
	target    *Target
}

type completion struct {
	task    task
	doc     *config.Config
	page    catalog.Page
	err     error
	cooling bool
}

// Scan loads all lines in an imported depot and streams recommendations (empty
// Query) or search results. Four workers bound network and script work. Every
// task has a deadline; cancellation waits for workers and releases all runtimes.
func Scan(ctx context.Context, root *config.Config, options Options, emit func(Event)) error {
	if root == nil {
		return fmt.Errorf("请先添加片库")
	}
	if options.Timeout <= 0 {
		options.Timeout = 12 * time.Second
	}
	options.Query = strings.TrimSpace(options.Query)
	jobs := make(chan task)
	completed := make(chan completion, workers)
	var wg sync.WaitGroup
	for range workers {
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
	running, runningDocs := 0, 0
	served := map[string]int{}
	for len(queue) > 0 || running > 0 {
		var send chan task
		var next task
		nextIndex := 0
		if len(queue) > 0 {
			// Keep room for videos while slow depot lines are still loading.
			// Prefer CMS feeds, sharing turns among configurations of that kind.
			docIndex, siteIndex := -1, -1
			for i, candidate := range queue {
				if candidate.target == nil {
					if docIndex < 0 {
						docIndex = i
					}
					continue
				}
				if siteIndex < 0 || taskPriority(candidate) < taskPriority(queue[siteIndex]) ||
					(taskPriority(candidate) == taskPriority(queue[siteIndex]) && served[candidate.target.Entry.Config.URL] < served[queue[siteIndex].target.Entry.Config.URL]) {
					siteIndex = i
				}
			}
			nextIndex = siteIndex
			if docIndex >= 0 && (siteIndex < 0 || runningDocs < workers/2) {
				nextIndex = docIndex
			}
			send, next = jobs, queue[nextIndex]
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case send <- next:
			queue = append(queue[:nextIndex], queue[nextIndex+1:]...)
			if next.target != nil {
				served[next.target.Entry.Config.URL]++
			} else {
				runningDocs++
			}
			running++
		case done := <-completed:
			running--
			if done.task.target == nil {
				runningDocs--
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
			}
			emit(event)
		}
	}
	return ctx.Err()
}

func taskPriority(work task) int {
	if work.target.Entry.Site.Kind == config.KindJSON || work.target.Entry.Site.Kind == config.KindXML {
		return 0
	}
	return 1
}

func execute(parent context.Context, work task, options Options) completion {
	ctx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()
	out := completion{task: work}
	if work.target == nil {
		if work.depth > 4 {
			out.err = fmt.Errorf("配置仓库嵌套超过 4 层")
			return out
		}
		if cached := options.Cached[work.url]; cached != nil {
			out.doc = cached
			return out
		}
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
			out.page, out.err, out.cooling = page, err, err != nil
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
