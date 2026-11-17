package main

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/douban"
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

// libraryScan is one pass over the library: the home feed, or a search.
// A scan owns its sessions; a generation rejects replies of an older pass.
type libraryScan struct {
	items            []library.Item
	groups           []titleGroup
	seen             map[string]bool
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
	l := &a.library
	a.stopScan(&l.feed)
	a.stopScan(&l.search)
	*l = libraryBrowser{
		root:   doc,
		cached: map[string]*config.Config{},
		feed:   libraryScan{generation: l.feed.generation, selected: -1},
		search: libraryScan{generation: l.search.generation, selected: -1},
	}
	if doc != nil {
		a.startScan(&l.feed, "", false)
		a.loadDiscoveryTab(false)
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
	l := &a.library
	if l.root == nil || a.closed {
		return
	}
	a.stopScan(s)
	if l.searchCache == nil {
		l.searchCache = &library.SearchCache{}
	}
	options := library.Options{Assets: strings.TrimSpace(a.browser.assets), Query: query, Cached: maps.Clone(l.cached), SearchCache: l.searchCache}
	if more {
		options.More = append([]library.Target(nil), s.more...)
		if len(options.More) == 0 {
			return
		}
	} else {
		s.items, s.groups, s.seen = nil, nil, map[string]bool{}
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
		err := library.Scan(ctx, root, options, func(event library.Event) {
			update(func() { a.applyLibraryEvent(s, event) })
		})
		cancel()
		update(func() {
			s.running, s.cancel, s.stopped = false, nil, err != nil
			a.autoOpenTitle()
		})
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
	a.startScan(s, s.query, false)
}

func (a *app) applyLibraryEvent(s *libraryScan, event library.Event) {
	l := &a.library
	s.progress = event
	if doc := event.Document; doc != nil {
		l.cached[event.URL], l.cached[doc.URL] = doc, doc
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
		return
	}
	// A site just answered: how long it took is its measured speed,
	// whether the page came back or the request failed. A cached reply
	// carries no duration, because no network happened.
	if event.Duration > 0 {
		// The store is best-effort: a failed save must not disturb
		// the scan, and the next measurement overwrites it anyway.
		_ = a.latency.record(result.Target.Entry.Key(), event.Duration)
	}
	if event.Err != nil {
		// A failed later page can be retried without repeating successful pages.
		if result.Target.Page > 1 {
			s.more = append(s.more, result.Target)
		}
		return
	}
	for _, video := range result.Page.Videos {
		item := library.Item{Entry: result.Target.Entry, Video: video}
		if video.ID == "" || s.seen[itemKey(item)] {
			continue
		}
		s.seen[itemKey(item)] = true
		s.items = append(s.items, item)
	}
	sort.SliceStable(s.items, func(i, j int) bool {
		return libraryItemRank(s.items[i], s.query, s.hot) < libraryItemRank(s.items[j], s.query, s.hot)
	})
	s.groups = groupTitles(s.items)
	if s.query != "" && result.Page.HasNext {
		s.more = append(s.more, library.Target{Entry: result.Target.Entry, Page: result.Target.Page + 1})
	}
	if s == &l.search {
		a.autoOpenTitle()
	}
}

func itemKey(item library.Item) string {
	return item.Entry.Key() + "\x00" + string(item.Video.ID)
}

// titleKey is what makes two sites' videos the same title: the name without
// case, surrounding space or title marks.
func titleKey(name string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(name), "《》"))
}

// groupTitles merges videos of the same title, in the order they first show.
// A video without a name stays on its own.
func groupTitles(items []library.Item) []titleGroup {
	var groups []titleGroup
	index := map[string]int{}
	for _, item := range items {
		key := titleKey(item.Video.Name)
		if key == "" {
			key = "\x00" + itemKey(item)
		}
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, titleGroup{name: videoTitle(item.Video)})
		}
		groups[i].items = append(groups[i].items, item)
	}
	return groups
}

// origin says where a title comes from: its site, or how many have it.
func (g titleGroup) origin() string {
	if len(g.items) == 1 {
		return g.items[0].Entry.Label()
	}
	return fmt.Sprintf("%d 个来源", len(g.items))
}

func libraryItemRank(item library.Item, query string, hot map[string]bool) int {
	if query == "" {
		// The feed is the sites' latest updates. A title the Douban lists
		// already call hot comes first, so the feed surfaces what is
		// popular now instead of whichever site answered first; CMS sites
		// follow, then the rest.
		if hot[titleKey(item.Video.Name)] {
			return 0
		}
		if item.Entry.Site.Kind == config.KindJSON || item.Entry.Site.Kind == config.KindXML {
			return 1
		}
		return 2
	}
	name, query := titleKey(item.Video.Name), titleKey(query)
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
