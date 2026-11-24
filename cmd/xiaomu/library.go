package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/douban"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

type libraryDocument struct {
	doc  *config.Config
	line string
	open bool // expanded on the sources page
}

// titleGroup is one title with every site that has it.
type titleGroup struct {
	name  string
	items []library.Item
}

// itemID identifies one video within one configured site without copying the
// site's full Entry.Key() string for every video in the same page.
type itemID struct {
	entryKey string
	videoID  catalog.ID
}

// libraryScan is one pass over the library: the home feed, or a search.
// A scan owns its sessions; a generation rejects replies of an older pass.
type libraryScan struct {
	items            []library.Item
	groups           []titleGroup
	groupIndex       map[string]int
	groupIndexLen    int
	groupIndexHead   *library.Item
	seen             map[itemID]struct{}
	sortBuf          []library.Item
	keysBuf          []string
	keysOutBuf       []string
	ranksBuf         []uint8
	hot              map[string]bool // Douban hot titles, for feed ranking
	more             []library.Target
	query            string
	selected         int
	grid             ui.GridState
	cancel           context.CancelFunc
	generation       uint64
	running, stopped bool
	progress         library.Event
}

// UI state is updated only through dispatch. The feed and the search run
// side by side, so going back from a search never reloads the home page.
type libraryBrowser struct {
	root         *config.Config
	cached       map[string]*config.Config
	liveChannels map[string][]iptv.Channel
	searchCache  *library.SearchCache
	documents    []libraryDocument
	feed, search libraryScan
}

func (a *app) stopScan(s *libraryScan) {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.generation++
	s.running = false
}

// resetLibrary replaces the imported configuration; nil removes it.
func (a *app) resetLibrary(doc *config.Config) {
	a.resetLibraryWithCache(doc, nil, nil)
}

func (a *app) resetLibraryWithCache(doc *config.Config, cached map[string]*config.Config, liveChannels map[string][]iptv.Channel) {
	a.cancelSourceSpeedTest()
	a.cancelFollowingRefresh()
	l := &a.library
	a.stopScan(&l.feed)
	a.stopScan(&l.search)
	docs := maps.Clone(cached)
	if docs == nil {
		docs = map[string]*config.Config{}
	}
	*l = libraryBrowser{
		root:         doc,
		cached:       docs,
		liveChannels: cloneLiveChannelsMap(liveChannels),
		feed:         libraryScan{generation: l.feed.generation, selected: -1},
		search:       libraryScan{generation: l.search.generation, selected: -1},
	}
	if doc != nil {
		a.startScan(&l.feed, "", false)
		a.loadDiscoveryTab(false)
		a.refreshFollowing()
	}
}

// searchLibrary shows the results for the toolbar's text above the tab.
func (a *app) searchLibrary() {
	if a.library.root == nil {
		a.status = "请先添加片源，再搜索片名"
		return
	}
	query := strings.TrimSpace(a.browser.query)
	if query == "" {
		a.status = "请输入片名"
		return
	}
	if err := a.searchHistory.record(query); err != nil {
		a.status = "搜索记录保存失败：" + err.Error()
	}
	a.focusSearchGrid = true
	a.cancelJob(jobTitle)
	for len(a.nav) > 1 {
		a.pop()
	}
	a.push(routeSearch)
	a.startScan(&a.library.search, query, false)
}

func (a *app) startScan(s *libraryScan, query string, more bool) {
	a.startScanWithSkip(s, query, more, true)
}

func (a *app) startScanWithSkip(s *libraryScan, query string, more, skipFailed bool) {
	l := &a.library
	if l.root == nil || a.closed {
		return
	}
	a.stopScan(s)
	if l.searchCache == nil {
		l.searchCache = &library.SearchCache{}
	}
	var skip map[string]bool
	if skipFailed && strings.TrimSpace(query) != "" {
		skip = a.siteHealth.snapshotKeys()
	}
	options := library.Options{Assets: strings.TrimSpace(a.browser.assets), Query: query, Cached: maps.Clone(l.cached), SearchCache: l.searchCache, Skip: skip}
	if more {
		options.More = append([]library.Target(nil), s.more...)
		if len(options.More) == 0 {
			return
		}
	} else {
		// A refresh owns this buffer; group/detail snapshots own their copies.
		// Clear references before reuse, and release capacity on a new query.
		if query == s.query {
			clear(s.items)
			s.items = s.items[:0]
			clear(s.seen)
			clear(s.sortBuf)
			s.sortBuf = s.sortBuf[:0]
			clear(s.keysBuf)
			s.keysBuf = s.keysBuf[:0]
			clear(s.keysOutBuf)
			s.keysOutBuf = s.keysOutBuf[:0]
			s.ranksBuf = s.ranksBuf[:0]
		} else {
			s.items, s.seen = nil, nil
			s.sortBuf, s.keysBuf, s.keysOutBuf, s.ranksBuf = nil, nil, nil, nil
		}
		s.groups, s.groupIndex, s.groupIndexLen, s.groupIndexHead = nil, nil, 0, nil
		if s.seen == nil {
			s.seen = make(map[itemID]struct{})
		}
		s.selected = -1
		s.hot = nil
		if query == "" {
			s.hot = a.hotTitles()
		}
	}
	s.more, s.progress = nil, library.Event{}
	s.query, s.running, s.stopped = query, true, false
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	generation, root, dispatch := s.generation, l.root, a.dispatch
	update := func(fn func()) {
		guarded := func() {
			if !a.closed && s.generation == generation {
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
		// At most one update is queued per scan. Keep arrivals together until
		// the UI takes them, so a burst cannot fill the main queue with sorts.
		var mu sync.Mutex
		var pending []library.Event
		var scheduled, finished bool
		var scanErr error
		publish := func() {
			update(func() {
				mu.Lock()
				events, done, err := pending, finished, scanErr
				pending, scheduled = nil, false
				mu.Unlock()
				a.applyLibraryEvents(s, events)
				if done {
					s.running, s.cancel, s.stopped = false, nil, err != nil
					a.autoOpenTitle()
				}
			})
		}
		enqueue := func(event *library.Event, done bool, err error) {
			mu.Lock()
			if event != nil {
				pending = append(pending, *event)
			}
			if done {
				finished, scanErr = true, err
			}
			schedule := !scheduled
			scheduled = true
			mu.Unlock()
			if schedule {
				if dispatch == nil {
					publish()
				} else {
					time.AfterFunc(16*time.Millisecond, publish)
				}
			}
		}
		err := library.Scan(ctx, root, options, func(event library.Event) { enqueue(&event, false, nil) })
		cancel()
		enqueue(nil, true, err)
	}
	if dispatch == nil {
		work()
	} else {
		go work()
	}
}

// reloadScan repeats a pass, asking again the sites that failed before.
func (a *app) reloadScan(s *libraryScan) {
	a.library.searchCache = &library.SearchCache{}
	a.startScanWithSkip(s, s.query, false, false)
}

func (a *app) applyLibraryEvent(s *libraryScan, event library.Event) {
	a.applyLibraryEvents(s, []library.Event{event})
}

func (a *app) applyLibraryEvents(s *libraryScan, events []library.Event) {
	// Reserve once for the batch rather than repeatedly copying every item
	// as individual sites arrive. Duplicate/empty IDs still get filtered.
	additional := 0
	for _, event := range events {
		if event.Result != nil && event.Err == nil {
			additional += len(event.Result.Page.Videos)
		}
	}
	s.items = slices.Grow(s.items, additional)
	changed := false
	docsChanged := false
	for _, event := range events {
		if a.appendLibraryEvent(s, event) {
			docsChanged = true
		}
		changed = changed || (event.Result != nil && event.Err == nil)
	}
	if docsChanged && a.persist && a.library.root != nil && a.browser.configURL != "" {
		_ = a.saveConfigCache()
	}
	if changed {
		keys := a.rankLibraryItems(s)
		s.groups, s.groupIndex = groupTitlesWithKeys(s.items, keys)
		s.groupIndexLen = len(s.items)
		if len(s.items) > 0 {
			s.groupIndexHead = &s.items[0]
		} else {
			s.groupIndexHead = nil
		}
		// Release scratch string references while retaining slice capacity.
		clear(s.keysBuf)
		s.keysBuf = s.keysBuf[:0]
		clear(s.keysOutBuf)
		s.keysOutBuf = s.keysOutBuf[:0]
		if s == &a.library.search {
			a.autoOpenTitle()
		}
	}
}

// rankLibraryItems stably partitions s.items into the three ranking buckets
// (0, 1, 2) in O(N) time and returns each ranked item's normalized titleKey
// so groupTitlesWithKeys does not recompute it.
func (a *app) rankLibraryItems(s *libraryScan) []string {
	n := len(s.items)
	if n == 0 {
		return nil
	}
	if cap(s.keysBuf) < n {
		s.keysBuf = make([]string, n)
	} else {
		s.keysBuf = s.keysBuf[:n]
	}
	if cap(s.ranksBuf) < n {
		s.ranksBuf = make([]uint8, n)
	} else {
		s.ranksBuf = s.ranksBuf[:n]
	}
	queryKey := ""
	if s.query != "" {
		queryKey = titleKey(s.query)
	}
	var counts [3]int
	ordered := true
	var prevRank uint8
	for i := range n {
		key := titleKey(s.items[i].Video.Name)
		s.keysBuf[i] = key
		r := uint8(libraryItemRankWithKey(s.items[i], key, queryKey, s.hot))
		s.ranksBuf[i] = r
		counts[r]++
		if r < prevRank {
			ordered = false
		}
		prevRank = r
	}
	if ordered {
		return s.keysBuf
	}
	if cap(s.sortBuf) < n {
		s.sortBuf = make([]library.Item, n)
	} else {
		s.sortBuf = s.sortBuf[:n]
	}
	if cap(s.keysOutBuf) < n {
		s.keysOutBuf = make([]string, n)
	} else {
		s.keysOutBuf = s.keysOutBuf[:n]
	}
	pos := [3]int{0, counts[0], counts[0] + counts[1]}
	for i := range n {
		b := s.ranksBuf[i]
		dst := pos[b]
		pos[b] = dst + 1
		s.sortBuf[dst] = s.items[i]
		s.keysOutBuf[dst] = s.keysBuf[i]
	}
	copy(s.items, s.sortBuf)
	clear(s.sortBuf)
	s.sortBuf = s.sortBuf[:0]
	return s.keysOutBuf
}

func (a *app) appendLibraryEvent(s *libraryScan, event library.Event) bool {
	l := &a.library
	s.progress = event
	docAdded := false
	if doc := event.Document; doc != nil {
		if l.cached[event.URL] != doc || l.cached[doc.URL] != doc {
			l.cached[event.URL], l.cached[doc.URL] = doc, doc
			if doc != l.root {
				docAdded = true
			}
		}
		known := false
		for _, existing := range l.documents {
			if existing.doc.URL == doc.URL {
				known = true
				break
			}
		}
		if !known && len(doc.Lines) == 0 {
			l.documents = append(l.documents, libraryDocument{doc: doc, line: event.Line, open: len(l.documents) == 0})
			// A separately imported IPTV list retains priority over depot lives.
			if a.live.url == "" && doc != l.root {
				a.live.sources = append(a.live.sources, doc.Lives...)
			}
		}
	}
	result := event.Result
	if result == nil {
		return docAdded
	}
	entryKey := result.Target.Entry.Key()
	// A site just answered: record latency on success, and update live health
	// from home scans (or heal a site that returns search results).
	if event.Duration > 0 && event.Err == nil {
		// The store is best-effort: a failed save must not disturb
		// the scan, and the next measurement overwrites it anyway.
		a.recordLatency(entryKey, event.Duration)
	}
	if s.query == "" {
		if event.Err != nil || len(result.Page.Videos) == 0 {
			a.markSiteFailed(entryKey)
		} else {
			a.markSiteHealthy(entryKey)
		}
	} else if event.Err == nil && len(result.Page.Videos) > 0 {
		a.markSiteHealthy(entryKey)
	}
	if event.Err != nil {
		// A failed later page can be retried without repeating successful pages.
		if result.Target.Page > 1 {
			s.more = append(s.more, result.Target)
		}
		return docAdded
	}
	if s.seen == nil {
		s.seen = make(map[itemID]struct{})
	}
	for _, video := range result.Page.Videos {
		if video.ID == "" {
			continue
		}
		id := itemID{entryKey: entryKey, videoID: video.ID}
		if _, dup := s.seen[id]; dup {
			continue
		}
		s.seen[id] = struct{}{}
		s.items = append(s.items, library.Item{Entry: result.Target.Entry, Video: video})
	}
	if s.query != "" && result.Page.HasNext {
		s.more = append(s.more, library.Target{Entry: result.Target.Entry, Page: result.Target.Page + 1})
	}
	return docAdded
}

func itemKey(item library.Item) string {
	return item.Entry.Key() + "\x00" + string(item.Video.ID)
}

// titleKey is what makes two sites' videos the same title: the name without
// case, surrounding space or title marks.
func titleKey(name string) string {
	s := strings.Trim(strings.TrimSpace(name), "《》")
	if isLowerNoAlloc(s) {
		return s
	}
	return strings.ToLower(s)
}

// isLowerNoAlloc reports whether s is guaranteed unchanged by strings.ToLower.
// CJK Unified Ideographs and CJK symbols have no case folding in Unicode, so
// Chinese titles take the zero-allocation fast path instead of falling through
// strings.ToLower's non-ASCII builder allocation.
func isLowerNoAlloc(s string) bool {
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			if b >= 'A' && b <= 'Z' {
				return false
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if !((r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3000 && r <= 0x303F)) {
			return false
		}
		i += size
	}
	return true
}

// groupTitles merges videos of the same title, in the order they first show.
// A video without a name stays on its own.
func groupTitles(items []library.Item) []titleGroup {
	groups, _ := groupTitlesWithKeys(items, nil)
	return groups
}

func groupTitlesWithKeys(items []library.Item, precomputedKeys []string) ([]titleGroup, map[string]int) {
	groups := make([]titleGroup, 0, len(items))
	index := make(map[string]int, len(items))
	counts := make([]int, 0, len(items))
	membership := make([]int, len(items))
	for n, item := range items {
		var key string
		if n < len(precomputedKeys) {
			key = precomputedKeys[n]
		} else {
			key = titleKey(item.Video.Name)
		}
		if key == "" {
			key = "\x00" + itemKey(item)
		}
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, titleGroup{name: videoTitle(item.Video)})
			counts = append(counts, 0)
		}
		counts[i]++
		membership[n] = i
	}
	// One independent snapshot avoids thousands of small allocations and
	// remains valid when a later page sorts or refreshes the scan's items.
	storage := make([]library.Item, len(items))
	offset := 0
	for i, count := range counts {
		groups[i].items = storage[offset : offset : offset+count]
		offset += count
	}
	for n, item := range items {
		i := membership[n]
		groups[i].items = append(groups[i].items, item)
	}
	return groups, index
}

// origin says where a title comes from: its site, or how many have it.
func (g titleGroup) origin() string {
	if len(g.items) == 1 {
		return g.items[0].Entry.Label()
	}
	return fmt.Sprintf("%d 个来源", len(g.items))
}

func libraryItemRank(item library.Item, query string, hot map[string]bool) int {
	queryKey := ""
	if query != "" {
		queryKey = titleKey(query)
	}
	return libraryItemRankWithKey(item, titleKey(item.Video.Name), queryKey, hot)
}

func libraryItemRankWithKey(item library.Item, name, query string, hot map[string]bool) int {
	if query == "" {
		// The feed is the sites' latest updates. A title the Douban lists
		// already call hot comes first, so the feed surfaces what is
		// popular now instead of whichever site answered first; CMS sites
		// follow, then the rest.
		if hot[name] {
			return 0
		}
		if item.Entry.Site.Kind == config.KindJSON || item.Entry.Site.Kind == config.KindXML {
			return 1
		}
		return 2
	}
	if name == query {
		return 0
	}
	if strings.Contains(name, query) {
		return 1
	}
	return 2
}

func (s *libraryScan) summary() string {
	verb := "推荐"
	if s.query != "" {
		verb = "搜索"
	}
	state := verb + "完成"
	if s.running {
		state = fmt.Sprintf("正在%s · %d/%d 个站点", verb, s.progress.Completed, s.progress.Total)
	}
	if s.stopped {
		state = "已停止，保留已找到的影片"
	}
	text := fmt.Sprintf("%s · %d 部影片", state, len(s.groups))
	if s.progress.Failed > 0 {
		text += fmt.Sprintf(" · %d 个请求未成功", s.progress.Failed)
	}
	if s.progress.Cooling > 0 {
		text += fmt.Sprintf(" · %d 个站点稍后重试", s.progress.Cooling)
	}
	if s.progress.Skipped > 0 {
		text += fmt.Sprintf(" · 已跳过 %d 个站点", s.progress.Skipped)
	}
	return text
}

// scanBar is a pass's progress with the buttons to stop or repeat it.
func (a *app) scanBar(c *ui.Context, s *libraryScan) {
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, s.summary()).FontSize(13).TextColor(textMuted).Grow(1)
		if s.running {
			if glassButton(c, "停止").Clicked() {
				a.stopScan(s)
				s.stopped = true
			}
		} else if glassButton(c, "重新加载").Clicked() {
			a.reloadScan(s)
		}
	})
	if s.running && s.progress.Total > 0 {
		ui.Progress(c, float64(s.progress.Completed)/float64(s.progress.Total)).FillWidth().Label("进度")
	}
}

// hotTitles are the Douban hot titles already cached for the home page, by
// normalized title: the disk cache from the last run plus the lists loaded
// in this run. The feed scan ranks them first; search keeps its own
// relevance order and ignores them.
func (a *app) hotTitles() map[string]bool {
	hot := map[string]bool{}
	add := func(feed douban.Feed) {
		for _, subject := range feed.Subjects {
			if key := titleKey(subject.Title); key != "" {
				hot[key] = true
			}
		}
	}
	if a.discovery.client != nil {
		add(a.discovery.client.Cached(douban.Movie))
		add(a.discovery.client.Cached(douban.TV))
	}
	add(a.discovery.movie.feed)
	add(a.discovery.tv.feed)
	return hot
}

// titleGrid shows a pass's titles; choosing one opens it with its sources.
func (a *app) titleGrid(c *ui.Context, s *libraryScan) {
	groups := s.groups
	query := titleKey(s.query)
	s.grid.Selected = &s.selected
	s.grid.Label = func(i int) string { return groups[i].name + " · " + groups[i].origin() }
	s.grid.Key = func(i int) any { return itemKey(groups[i].items[0]) }
	// A search just ran from the toolbar: ask for the focus on its results,
	// so the arrows move through them and Enter opens one without a click.
	// The request is applied after the build, which is what keeps it.
	if a.focusSearchGrid && s == &a.library.search {
		s.grid.Handle.Focus()
		a.focusSearchGrid = false
	}
	grid := ui.GridView(c, &s.grid, len(groups), 152, 252, func(i int) {
		group := groups[i]
		card := a.videoCard(c, group.items[0].Video, group.origin(), query != "" && titleKey(group.name) == query)
		if card.Clicked() {
			a.openTitle(group.name, group.items[0].Video.Pic, group.items)
		}
	}).Gap(14).Grow(1).FillWidth()
	// Arrows move the choice without opening it; a click or Enter opens it.
	if grid.Submitted() && s.selected >= 0 && s.selected < len(groups) {
		group := groups[s.selected]
		s.selected = -1
		a.openTitle(group.name, group.items[0].Video.Pic, group.items)
	}
}

// feedView is the home page's latest updates, gathered from every site.
func (a *app) feedView(c *ui.Context) {
	s := &a.library.feed
	a.scanBar(c, s)
	if len(s.groups) == 0 {
		if s.running {
			emptyState(c, "正在汇集影片", "可直接输入片名搜索，影片返回后会立即显示。")
		} else {
			emptyState(c, "暂时没有影片", "试试搜索片名，或重新加载。")
		}
		return
	}
	a.titleGrid(c, s)
}

func (a *app) searchView(c *ui.Context) {
	s := &a.library.search
	ui.Column(c).Fill().Gap(14).Children(func() {
		ui.Text(c, "“"+s.query+"”").FontSize(16).Bold().SingleLine()
		a.scanBar(c, s)
		if len(s.groups) == 0 {
			if s.running {
				emptyState(c, "正在搜索", "找到的影片会立即显示。")
			} else {
				emptyState(c, "没有找到匹配的影片", "换个关键词再搜；未响应的站点可以重新加载。")
			}
			return
		}
		a.titleGrid(c, s)
		if len(s.more) > 0 && !s.running {
			if glassButton(c, "加载更多结果").Clicked() {
				a.startScan(s, s.query, true)
			}
		}
	})
}
