package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/douban"
	"github.com/egoist/mygo/ui"
)

type discoveryList struct {
	feed           douban.Feed
	loading        bool
	loadingMore    bool
	moreFailed     bool
	waitScrollGrow bool
	lastMaxY       float32
	message        string
	selected       int
	grid           ui.GridState
	scroll         ui.ScrollState
	cancel         context.CancelFunc
	gen            uint64
}

const discoveryAutoLoadMargin = 280

type discoveryBrowser struct {
	client      *douban.Client
	tab         string
	hot         discoveryList
	movie       discoveryList
	tv          discoveryList
	show        discoveryList
	animation   discoveryList
	short       discoveryList
	documentary discoveryList
	top250      discoveryList
}

func (d *discoveryBrowser) listFor(kind douban.Kind) *discoveryList {
	switch kind {
	case douban.Movie:
		return &d.movie
	case douban.TV:
		return &d.tv
	case douban.Show:
		return &d.show
	case douban.Animation:
		return &d.animation
	case douban.ShortDrama:
		return &d.short
	case douban.Documentary:
		return &d.documentary
	case douban.Top250:
		return &d.top250
	default:
		return &d.hot
	}
}

func (d *discoveryBrowser) allLists() []*discoveryList {
	return []*discoveryList{
		&d.hot,
		&d.movie,
		&d.tv,
		&d.show,
		&d.animation,
		&d.short,
		&d.documentary,
		&d.top250,
	}
}

func (d *discoveryBrowser) currentKind() douban.Kind {
	switch douban.Kind(d.tab) {
	case douban.Movie, douban.TV, douban.Show, douban.Animation, douban.ShortDrama, douban.Documentary, douban.Top250:
		return douban.Kind(d.tab)
	default:
		return douban.Hot
	}
}

func (d *discoveryBrowser) current() *discoveryList {
	return d.listFor(d.currentKind())
}

func (a *app) initDiscovery() {
	cacheDir := ""
	if sourcePath, err := savedSourcePath(); err == nil {
		cacheDir = filepath.Join(filepath.Dir(sourcePath), "cache", "discovery")
	}
	a.discovery.client = douban.New(cacheDir)
}

func (a *app) stopDiscovery() {
	for _, list := range a.discovery.allLists() {
		list.gen++
		if list.cancel != nil {
			list.cancel()
			list.cancel = nil
		}
		list.loading = false
		list.loadingMore = false
	}
}

func (a *app) loadDiscoveryTab(force bool) {
	d := &a.discovery
	if d.client == nil || d.tab == "sites" || a.closed {
		return
	}
	kind := d.currentKind()
	list := d.listFor(kind)
	if list.loading || (!force && (list.loadingMore || list.feed.Fresh(time.Now()))) {
		return
	}
	if list.cancel != nil {
		list.cancel()
		list.cancel = nil
	}
	list.gen++
	gen := list.gen
	list.loading, list.loadingMore, list.moreFailed, list.waitScrollGrow, list.message, list.selected = true, false, false, false, "", -1
	if force {
		list.scroll.Y = 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	list.cancel = cancel
	client, dispatch, previous := d.client, a.dispatch, list.feed
	update := func(fn func()) {
		guarded := func() {
			if !a.closed && list.gen == gen {
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
		cached := client.Cached(kind)
		if previous.Updated.After(cached.Updated) {
			cached = previous
		}
		if len(cached.Subjects) > 0 {
			update(func() { list.feed = cached })
			if !force && cached.Fresh(time.Now()) {
				update(func() { list.loading, list.cancel = false, nil })
				return
			}
		}
		feed, err := client.Fetch(ctx, kind)
		var cacheErr error
		if err == nil {
			cacheErr = client.Save(feed)
		}
		update(func() {
			list.loading, list.cancel = false, nil
			if err != nil {
				if len(list.feed.Subjects) > 0 {
					list.message = "豆瓣暂时无法更新，正在显示上次的榜单。"
				} else {
					list.message = "豆瓣榜单暂时无法加载，可重试或浏览最近更新。"
				}
				return
			}
			list.feed = feed
			list.waitScrollGrow = false
			if cacheErr != nil {
				list.message = "榜单已更新，本地缓存保存失败；重启后可重新加载。"
			} else if force {
				a.setNotice("豆瓣榜单已更新")
			}
		})
	}
	if dispatch == nil {
		work()
	} else {
		go work()
	}
}

func (a *app) loadMoreDiscovery() {
	d := &a.discovery
	if d.client == nil || d.tab == "sites" || a.closed {
		return
	}
	kind := d.currentKind()
	list := d.listFor(kind)
	if list.loading || list.loadingMore || !list.feed.HasMore() {
		return
	}
	if list.cancel != nil {
		list.cancel()
		list.cancel = nil
	}
	list.gen++
	gen := list.gen
	list.loadingMore, list.moreFailed, list.message = true, false, ""
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	list.cancel = cancel
	client, dispatch, previous := d.client, a.dispatch, list.feed
	start := previous.Next
	update := func(fn func()) {
		guarded := func() {
			if !a.closed && list.gen == gen {
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
		page, err := client.FetchPage(ctx, kind, start)
		var merged douban.Feed
		var cacheErr error
		if err == nil {
			merged = douban.MergePage(previous, page)
			if len(merged.Subjects) > 0 {
				cacheErr = client.Save(merged)
			}
		}
		update(func() {
			list.loadingMore, list.cancel = false, nil
			if err != nil {
				list.moreFailed = true
				list.message = "加载更多失败，可重试。"
				return
			}
			prevCount := len(list.feed.Subjects)
			list.feed = merged
			if len(merged.Subjects) > prevCount && list.scroll.MaxY > 0 {
				list.waitScrollGrow = true
				list.lastMaxY = list.scroll.MaxY
			}
			if cacheErr != nil {
				list.message = "已加载更多，本地缓存保存失败。"
			}
		})
	}
	if dispatch == nil {
		work()
	} else {
		go work()
	}
}

// doubanScore finds a title's score on the cached Douban lists, by exact title.
// Scores are validated when read, so a match is a real score and a miss simply
// means this app does not know one.
func (a *app) doubanScore(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	for _, list := range a.discovery.allLists() {
		for _, subject := range list.feed.Subjects {
			if subject.Title == title && subject.Rating != "" {
				return subject.Rating
			}
		}
	}
	return ""
}

type homeTabSpec struct {
	key   string
	label string
}

// homeTabs are the home page's lists, in the order they show.
var homeTabs = []homeTabSpec{
	{string(douban.Hot), "实时热门"},
	{string(douban.Movie), "热门电影"},
	{string(douban.TV), "热门剧集"},
	{string(douban.Show), "热门综艺"},
	{string(douban.Animation), "热门动漫"},
	{string(douban.ShortDrama), "热门短剧"},
	{string(douban.Documentary), "热门纪录片"},
	{string(douban.Top250), "Top 250"},
	{"sites", "最近更新"},
}

// homeView is the first page: the welcome before a source is imported, then
// what is popular on Douban and the sites' latest updates.
func (a *app) homeView(c *ui.Context) {
	if a.library.root == nil && len(continueWatchingEntries(a.history.entries)) == 0 && len(a.favorites.entries) == 0 {
		a.welcomeView(c)
		return
	}
	// Shortcut rows keep their natural height. The page can scroll even in a
	// small window, while the recommendation grid retains a bounded viewport
	// for its own virtual scrolling.
	width, height := c.Size()
	d := &a.discovery
	list := d.current()
	hasMeta := d.tab != "sites" && !list.feed.Updated.IsZero()
	// Wrap tabs below 1180px so wider CJK fallback/scaled fonts never crowd
	// or clip the inline refresh icon button alongside all 9 tabs.
	wrapTabs := width < 1180
	separateMetaRow := hasMeta && width < 1380
	headerReserve := float32(180)
	if separateMetaRow {
		headerReserve = 224
	}
	if wrapTabs {
		headerReserve += 44
	}
	recommendationHeight := max(float32(300), height-headerReserve)
	tab := 0
	for i, spec := range homeTabs {
		if d.tab == spec.key {
			tab = i
		}
	}
	p := paletteFor(c)
	renderRefreshButton := func() {
		if d.tab == "sites" || list.message != "" {
			return
		}
		tip := "刷新榜单"
		iconColor := p.text
		if list.loading {
			tip = "刷新中…"
			iconColor = p.textMuted
		}
		btn := glassButton(c.Key("discovery-refresh"), "").Size(34, 34).Shrink(0).Radius(17).Padding(0).Center().
			Label("刷新榜单").Tooltip(tip).Disabled(list.loading)
		btn.Children(func() {
			ui.Icon(c, iconRefresh).Size(16, 16).TextColor(iconColor).PassThrough()
		})
		if btn.Clicked() {
			a.loadDiscoveryTab(true)
		}
	}
	renderDiscoveryMeta := func(separateRow bool) {
		if d.tab == "sites" {
			return
		}
		if hasMeta {
			ui.Text(c, "豆瓣榜单 · 更新于 "+list.feed.Updated.Local().Format("01-02 15:04")).FontSize(13).TextColor(p.textMuted)
		}
		if separateRow {
			ui.Spacer(c)
		}
		renderRefreshButton()
	}
	ui.Scroll(c).Key("home-scroll").Label("首页内容").Fill().Gap(14).Children(func() {
		a.continueWatchingStrip(c)
		a.favoritesStrip(c)
		if a.library.root == nil {
			card := ui.Column(c).FillWidth().Padding(20).Gap(12).Radius(22)
			applyPane(card, p, p.paneMat)
			card.Children(func() {
				ui.Text(c, "添加片源，继续观看").FontSize(22).Bold()
				ui.Text(c, "观看记录和追剧仍保存在本机。重新添加片源后即可继续播放，也可直接打开或拖入本地视频文件。").TextColor(p.textMuted)
				ui.Row(c).Gap(10).Children(func() {
					if primaryButton(c, "添加片源").Clicked() {
						a.openImportDialog()
					}
					if glassButton(c, "打开本地视频").Clicked() {
						a.openLocalFileDialog()
					}
				})
			})
			return
		}
		if wrapTabs {
			ui.Row(c).Label("榜单").FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				for i, spec := range homeTabs {
					if chip(c.Key("home-tab-"+spec.key), spec.label, tab == i).Clicked() {
						if d.tab != spec.key {
							d.tab = spec.key
							a.loadDiscoveryTab(false)
						}
					}
				}
				if !separateMetaRow {
					renderRefreshButton()
				}
			})
		} else {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				if ui.Segmented(c, &tab, "实时热门", "热门电影", "热门剧集", "热门综艺", "热门动漫", "热门短剧", "热门纪录片", "Top 250", "最近更新").Label("榜单").Changed() {
					d.tab = homeTabs[tab].key
					a.loadDiscoveryTab(false)
				}
				if !separateMetaRow && d.tab != "sites" {
					ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
						renderDiscoveryMeta(false)
					})
				}
			})
		}
		if separateMetaRow {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				renderDiscoveryMeta(true)
			})
		}
		ui.Column(c).Label("首页推荐").FillWidth().Height(recommendationHeight).MinHeight(recommendationHeight).Gap(14).Children(func() {
			if d.tab == "sites" {
				a.feedView(c)
			} else {
				a.discoveryView(c)
			}
		})
	})
}

func discoveryBadge(kind douban.Kind, subject douban.Subject) string {
	if kind == douban.Top250 && subject.Rank > 0 {
		if subject.Rating != "" {
			return fmt.Sprintf("#%d · 豆瓣 %s", subject.Rank, subject.Rating)
		}
		return fmt.Sprintf("#%d", subject.Rank)
	}
	if subject.Rating != "" {
		return fmt.Sprintf("豆瓣 %s", subject.Rating)
	}
	return "暂无评分"
}

// discoveryView is a Douban list. Douban names what is popular; choosing a
// poster looks for the title in the user's own library.
func (a *app) discoveryView(c *ui.Context) {
	kind := a.discovery.currentKind()
	list := a.discovery.listFor(kind)
	p := paletteFor(c)
	if list.message != "" {
		ui.Row(c).Key("discovery-message").FillWidth().Gap(10).AlignItems(ui.Center).Transition(transInlineBanner).Children(func() {
			ui.Text(c, list.message).FontSize(13).Grow(1)
			if glassButton(c, "重试").Disabled(list.loading || list.loadingMore).Clicked() {
				if list.moreFailed && list.feed.HasMore() {
					a.loadMoreDiscovery()
				} else {
					a.loadDiscoveryTab(true)
				}
			}
		})
	}
	if len(list.feed.Subjects) == 0 {
		if list.loading {
			emptyState(c, "正在加载豆瓣榜单…", "也可以直接搜索片名。")
		} else {
			emptyStateWithAction(c, "暂无豆瓣榜单", "可查看片库最近更新，或直接搜索片名。", "看最近更新", func() {
				a.discovery.tab = "sites"
			})
		}
		return
	}
	list.grid.Selected = &list.selected
	list.grid.Key = func(i int) any { return list.feed.Subjects[i].ID }
	list.grid.Label = func(i int) string { return "查找影片 " + list.feed.Subjects[i].Title }
	grid := ui.GridView(c.Key("discovery-grid-"+string(kind)), &list.grid, len(list.feed.Subjects), 152, 252, func(i int) {
		subject := list.feed.Subjects[i]
		badge := discoveryBadge(kind, subject)
		card := a.videoCard(c, catalog.Video{ID: catalog.ID(subject.ID), Name: subject.Title, Pic: subject.Cover, Remarks: subject.Episodes}, badge, false)
		if card.Clicked() {
			a.openTitleWithScore(subject.Title, subject.Cover, nil, subject.Rating)
		}
	}).Gap(14).Grow(1).FillWidth().TrackScroll(&list.scroll)
	// Arrows move the choice without opening it; a click or Enter opens it.
	if grid.Submitted() && list.selected >= 0 && list.selected < len(list.feed.Subjects) {
		subject := list.feed.Subjects[list.selected]
		list.selected = -1
		a.openTitleWithScore(subject.Title, subject.Cover, nil, subject.Rating)
	}
	// After a page appends, wait until layout actually expands MaxY for the
	// new rows so the still-stale MaxY cannot immediately trigger another page.
	if list.waitScrollGrow && list.scroll.MaxY > list.lastMaxY+1 {
		list.waitScrollGrow = false
	}
	if shouldAutoLoadMore(list) {
		a.loadMoreDiscovery()
	}
	if list.loadingMore {
		ui.Row(c).Key("discovery-load-more").FillWidth().Gap(10).AlignItems(ui.Center).Padding(2, 0).Children(func() {
			ui.Spacer(c)
			status := "正在加载更多…"
			if list.feed.Total > 0 {
				status = fmt.Sprintf("正在加载更多…（已显示 %d / %d）", len(list.feed.Subjects), list.feed.Total)
			}
			ui.Text(c, status).FontSize(13).TextColor(p.textMuted)
			ui.Spacer(c)
		})
	} else if list.feed.HasMore() && !list.loading && list.scroll.MaxY <= 0 {
		// Fallback button only when the viewport is tall enough to fit all
		// loaded items without a scrollbar.
		ui.Row(c).Key("discovery-load-more").FillWidth().Gap(12).AlignItems(ui.Center).Padding(2, 0).Children(func() {
			ui.Spacer(c)
			if list.feed.Total > 0 {
				ui.Text(c, fmt.Sprintf("已显示 %d / %d", len(list.feed.Subjects), list.feed.Total)).FontSize(13).TextColor(p.textMuted)
			}
			if glassButton(c, "加载更多").Clicked() {
				a.loadMoreDiscovery()
			}
			ui.Spacer(c)
		})
	}
}

func shouldAutoLoadMore(list *discoveryList) bool {
	if list.loading || list.loadingMore || list.moreFailed || list.waitScrollGrow || !list.feed.HasMore() {
		return false
	}
	return list.scroll.MaxY > 0 && list.scroll.Y > 0 && list.scroll.MaxY-list.scroll.Y <= discoveryAutoLoadMargin
}
