package main

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/source"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

type libraryDocument struct {
	doc  *config.Config
	line string
}

// UI state is updated only through dispatch. Scans own their sessions and never
// share the active detail/playback session. A generation rejects stale replies.
type libraryBrowser struct {
	root             *config.Config
	cached           map[string]*config.Config
	documents        []libraryDocument
	items            []library.Item
	seen             map[string]bool
	more             []library.Target
	query, filter    string
	selected         int
	grid             ui.GridState
	cancel           context.CancelFunc
	generation       uint64
	running, stopped bool
	progress         library.Event
}

func (a *app) stopLibraryScan() {
	l := &a.library
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	l.generation++
	l.running = false
}

func (a *app) resetLibrary(doc *config.Config) {
	a.stopLibraryScan()
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{}, generation: a.library.generation, selected: -1}
	a.browser.sidebar = "library"
	if len(doc.Sites) > 0 || len(doc.Lines) > 0 {
		a.startLibraryScan("", false)
		a.loadDiscoveryTab(false)
	} else {
		a.status = fmt.Sprintf("已导入 %d 个直播来源", len(doc.Lives))
	}
}

func (a *app) searchLibrary() {
	if a.library.root == nil {
		a.status = "请先添加片库，再搜索片名"
		return
	}
	query := strings.TrimSpace(a.browser.query)
	if query == "" {
		a.status = "请输入片名"
		return
	}
	if a.busy {
		return
	}
	a.browser.detail = nil
	a.browser.sidebar = "search"
	a.startLibraryScan(query, false)
}

func (a *app) startLibraryScan(query string, more bool) {
	l := &a.library
	if l.root == nil || a.closed {
		return
	}
	a.stopLibraryScan()
	options := library.Options{Assets: strings.TrimSpace(a.browser.assets), Query: query, Cached: maps.Clone(l.cached)}
	if more {
		options.More = append([]library.Target(nil), l.more...)
		if len(options.More) == 0 {
			return
		}
	} else {
		l.items, l.seen, l.filter = nil, map[string]bool{}, ""
		l.selected = -1
	}
	l.more, l.progress = nil, library.Event{}
	l.query, l.running, l.stopped = query, true, false
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	generation, root, dispatch := l.generation, l.root, a.dispatch
	a.status = ""
	update := func(fn func()) {
		guarded := func() {
			if !a.closed && a.library.generation == generation {
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
			update(func() { a.applyLibraryEvent(event) })
		})
		cancel()
		update(func() {
			l.running, l.cancel, l.stopped = false, nil, err != nil
			if !a.busy && a.browser.detail == nil && a.browser.sidebar == "library" {
				a.status = ""
			}
		})
	}
	if dispatch == nil {
		work()
	} else {
		go work()
	}
}

func (a *app) applyLibraryEvent(event library.Event) {
	l := &a.library
	l.progress = event
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
			l.documents = append(l.documents, libraryDocument{doc: doc, line: event.Line})
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
	if event.Err != nil {
		// A failed later page can be retried without repeating successful pages.
		if result.Target.Page > 1 {
			l.more = append(l.more, result.Target)
		}
		return
	}
	for _, video := range result.Page.Videos {
		key := result.Target.Entry.Key() + "\x00" + string(video.ID)
		if video.ID == "" || l.seen[key] {
			continue
		}
		l.seen[key] = true
		l.items = append(l.items, library.Item{Entry: result.Target.Entry, Video: video})
	}
	sort.SliceStable(l.items, func(i, j int) bool {
		return libraryItemRank(l.items[i], l.query) < libraryItemRank(l.items[j], l.query)
	})
	if l.query != "" && result.Page.HasNext {
		l.more = append(l.more, library.Target{Entry: result.Target.Entry, Page: result.Target.Page + 1})
	}
}

func libraryItemRank(item library.Item, query string) int {
	if query == "" {
		if item.Entry.Site.Kind == config.KindJSON || item.Entry.Site.Kind == config.KindXML {
			return 0
		}
		return 1
	}
	name := strings.ToLower(strings.Trim(strings.TrimSpace(item.Video.Name), "《》"))
	query = strings.ToLower(strings.Trim(query, "《》"))
	if name == query {
		return 0
	}
	if strings.Contains(name, query) {
		return 1
	}
	return 2
}

func (a *app) librarySummary() string {
	l := &a.library
	verb := "推荐"
	if l.query != "" {
		verb = "搜索"
	}
	state := verb + "完成"
	if l.running {
		state = fmt.Sprintf("正在%s · %d/%d 个站点", verb, l.progress.Completed, l.progress.Total)
	}
	if l.stopped {
		state = "已停止，保留已找到的影片"
	}
	text := fmt.Sprintf("%s · %d 个结果", state, len(l.items))
	if l.progress.Failed > 0 {
		text += fmt.Sprintf(" · %d 个请求未成功", l.progress.Failed)
	}
	if l.progress.Skipped > 0 {
		text += fmt.Sprintf(" · 已跳过 %d 个站点", l.progress.Skipped)
	}
	return text
}

func (a *app) openLibraryItem(item library.Item) {
	assets := strings.TrimSpace(a.browser.assets)
	a.run("读取影片详情", func(ctx context.Context) (jobResult, error) {
		session, categories, err := source.Open(ctx, item.Entry.Site, assets)
		if err != nil {
			return jobResult{}, err
		}
		detail, err := session.Detail(ctx, string(item.Video.ID))
		if err != nil {
			session.Close()
			return jobResult{}, err
		}
		if detail.Name == "" {
			detail.Name = item.Video.Name
		}
		if detail.Pic == "" {
			detail.Pic = item.Video.Pic
		}
		return jobResult{discard: session.Close, apply: func() {
			b := &a.browser
			if b.session != nil {
				b.session.Close()
			}
			b.session, b.categories = session, categories
			b.doc, b.site, b.line = item.Entry.Config, item.Entry.Site.Name, item.Entry.Line
			b.detail, b.sidebar = &detail, "library"
			if a.library.query != "" {
				b.sidebar = "search"
			}
			a.status = "请选择分集播放"
		}}, nil
	})
}

func (a *app) catalogView(c *ui.Context) {
	if a.browser.sidebar == "sources" {
		a.librarySourcesView(c)
		return
	}
	if a.library.root == nil || a.browser.sidebar == "site" {
		a.siteCatalogView(c)
		return
	}
	a.libraryView(c)
}

func (a *app) libraryView(c *ui.Context) {
	l := &a.library
	discovering := l.query == "" && a.browser.detail == nil
	bottom, gap := float32(50), float32(18)
	if discovering {
		bottom, gap = 24, 14
	}
	ui.Column(c).Fill().Padding(28, 30, bottom, 30).Gap(gap).Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			title := "首页推荐"
			if discovering && a.discovery.tab != "sites" {
				title = a.discoveryTitle()
			}
			if l.query != "" {
				title = "搜索：" + l.query
			}
			fontSize := float32(28)
			if discovering {
				fontSize = 24
			}
			ui.Text(c, title).FontSize(fontSize).Bold().SingleLine().Grow(1)
			if ui.Button(c, "搜索").Disabled(a.busy).Clicked() {
				a.searchLibrary()
			}
			if discovering && a.discovery.tab != "sites" {
				if ui.Button(c, "刷新榜单").Disabled(a.discovery.current().loading).Clicked() {
					a.loadDiscoveryTab(true)
				}
			} else if l.running {
				if ui.Button(c, "停止").Clicked() {
					a.stopLibraryScan()
					l.stopped = true
				}
			} else if ui.Button(c, "重新加载").Disabled(a.busy).Clicked() {
				a.startLibraryScan(l.query, false)
			}
		})
		if a.browser.detail != nil {
			ui.Text(c, videoMeta(a.browser.line, a.browser.site)).TextColor(c.Theme().TextMuted)
			a.detailView(c)
			return
		}
		if discovering {
			a.discoveryTabs(c)
			if a.discovery.tab != "sites" {
				a.discoveryView(c)
				return
			}
		}
		ui.Text(c, a.librarySummary()).FontSize(13).TextColor(c.Theme().TextMuted)
		if len(l.items) == 0 {
			ui.Column(c).Grow(1).FillWidth().Center().Gap(12).Children(func() {
				title, hint := "正在汇集影片", "可直接输入片名搜索，影片返回后会立即显示。"
				if !l.running {
					title, hint = "暂时没有影片", "试试搜索片名，或重新加载片库。"
					if l.query != "" {
						title, hint = "没有找到匹配的影片", "换个关键词再搜；未响应的站点可以重新加载。"
					}
				}
				ui.Text(c, title).FontSize(24).Bold()
				ui.Text(c, hint).TextColor(c.Theme().TextMuted)
			})
			return
		}
		// A compact source selector keeps the result grid stable as more sites arrive.
		keys, labels := []string{""}, []string{"全部来源"}
		seen := map[string]bool{}
		for _, item := range l.items {
			key := item.Entry.Key()
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
				labels = append(labels, item.Entry.Label())
			}
		}
		a.librarySourcePicker(c, labels, keys)
		var visible []library.Item
		for _, item := range l.items {
			if l.filter == "" || item.Entry.Key() == l.filter {
				visible = append(visible, item)
			}
		}
		l.grid.Selected = &l.selected
		l.grid.Label = func(i int) string { return videoTitle(visible[i].Video) + " · " + visible[i].Entry.Label() }
		l.grid.Key = func(i int) any { return visible[i].Entry.Key() + "\x00" + string(visible[i].Video.ID) }
		if ui.GridView(c, &l.grid, len(visible), 152, 252, func(i int) {
			item := visible[i]
			ui.Column(c).Fill().Gap(8).Children(func() {
				ui.Box(c).FillWidth().Grow(1).Children(func() { a.videoCard(c, item.Video) })
				ui.Text(c, item.Entry.Label()).FontSize(11).SingleLine().TextColor(c.Theme().TextMuted)
			})
		}).Grow(1).FillWidth().Changed() && l.selected >= 0 && l.selected < len(visible) && !a.busy {
			a.openLibraryItem(visible[l.selected])
		}
		if l.query != "" && len(l.more) > 0 && !l.running {
			if ui.Button(c, "加载更多结果").Disabled(a.busy).Clicked() {
				a.startLibraryScan(l.query, true)
			}
		}
	})
}

func (a *app) librarySourcePicker(c *ui.Context, labels, keys []string) {
	// Menu avoids dozens of source chips squeezing the posters off the screen.
	selected := labels[0]
	for i, key := range keys {
		if a.library.filter == key {
			selected = labels[i]
			break
		}
	}
	if ui.Select(c, &selected, labels).Label("结果来源").Width(360).Changed() {
		for i, label := range labels {
			if label == selected {
				a.library.filter, a.library.selected = keys[i], -1
				break
			}
		}
	}
}

func (a *app) librarySourcesView(c *ui.Context) {
	l := &a.library
	ui.Scroll(c).Fill().Padding(30, 34, 64, 34).Gap(18).Children(func() {
		ui.Text(c, "管理片库").FontSize(28).Bold()
		ui.Text(c, "首页与搜索自动汇集这些站点。也可以单独打开站点浏览分类。").TextColor(c.Theme().TextMuted)
		ui.Row(c).FillWidth().Gap(10).Children(func() {
			if ui.Button(c, "添加片源").Disabled(a.busy).Clicked() {
				a.browser.importOpen = true
			}
			if l.root != nil && ui.Button(c, "刷新片库配置").Disabled(a.busy).Clicked() {
				a.loadConfig(a.browser.configURL, "")
			}
		})
		for _, document := range l.documents {
			name := document.line
			if name == "" {
				name = "已导入片库"
			}
			ui.Column(c).Key(document.doc.URL).FillWidth().Padding(18).Gap(12).Radius(18).Material(glass.Glass{Style: glass.Clear}).Children(func() {
				ui.Text(c, name).FontSize(18).Bold()
				skipped := 0
				ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
					for i, site := range document.doc.Sites {
						if site.Kind.Unsupported() {
							skipped++
							continue
						}
						if ui.Button(c, site.Name).Key(i).Disabled(a.busy).Clicked() {
							a.openSiteInConfig(site, document.doc, document.line)
						}
					}
				})
				if skipped > 0 {
					ui.Text(c, fmt.Sprintf("%d 个站点暂不兼容，已从推荐和搜索中跳过", skipped)).FontSize(12).TextColor(c.Theme().TextMuted)
				}
			})
		}
	})
}
