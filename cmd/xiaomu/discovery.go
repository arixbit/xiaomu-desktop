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
	feed     douban.Feed
	loading  bool
	message  string
	selected int
	grid     ui.GridState
	cancel   context.CancelFunc
}

type discoveryBrowser struct {
	client    *douban.Client
	tab       string
	movie, tv discoveryList
}

func (d *discoveryBrowser) current() *discoveryList {
	if d.tab == string(douban.TV) {
		return &d.tv
	}
	return &d.movie
}

func (a *app) initDiscovery() {
	cacheDir := ""
	if sourcePath, err := savedSourcePath(); err == nil {
		cacheDir = filepath.Join(filepath.Dir(sourcePath), "cache", "discovery")
	}
	a.discovery.client = douban.New(cacheDir)
}

func (a *app) stopDiscovery() {
	for _, list := range []*discoveryList{&a.discovery.movie, &a.discovery.tv} {
		if list.cancel != nil {
			list.cancel()
			list.cancel = nil
		}
	}
}

func (a *app) loadDiscoveryTab(force bool) {
	d := &a.discovery
	if d.client == nil || d.tab == "sites" || a.closed {
		return
	}
	list, kind := d.current(), douban.Movie
	if d.tab == string(douban.TV) {
		kind = douban.TV
	}
	if list.loading || (!force && list.feed.Fresh(time.Now())) {
		return
	}
	list.loading, list.message, list.selected = true, "", -1
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	list.cancel = cancel
	client, dispatch, previous := d.client, a.dispatch, list.feed
	update := func(fn func()) {
		guarded := func() {
			if !a.closed {
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
			if cacheErr != nil {
				list.message = "榜单已更新，本地缓存保存失败；重启后可重新加载。"
			}
		})
	}
	if dispatch == nil {
		work()
	} else {
		go work()
	}
}

// doubanScore finds a title's score on the cached Douban lists, by
// exact title. The lists hold at most 24 subjects each, and the score
// was validated when the list was read, so a match is a real score and
// a miss simply means this app does not know one.
func (a *app) doubanScore(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	for _, list := range []*discoveryList{&a.discovery.movie, &a.discovery.tv} {
		for _, subject := range list.feed.Subjects {
			if subject.Title == title && subject.Rating != "" {
				return subject.Rating
			}
		}
	}
	return ""
}

// homeTabs are the home page's lists, in the order they show.
var homeTabs = []string{string(douban.Movie), string(douban.TV), "sites"}

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
	_, height := c.Size()
	recommendationHeight := max(float32(300), height-180)
	d := &a.discovery
	tab := 0
	for i, key := range homeTabs {
		if d.tab == key {
			tab = i
		}
	}
	p := paletteFor(c)
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
						a.browser.importErr = ""
						a.browser.importOpen = true
					}
					if glassButton(c, "打开本地视频").Clicked() {
						a.openLocalFileDialog()
					}
				})
			})
			return
		}
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.Segmented(c, &tab, "热播电影", "热播剧集", "最近更新").Label("榜单").Changed() {
				d.tab = homeTabs[tab]
				a.loadDiscoveryTab(false)
			}
			ui.Spacer(c)
			if d.tab == "sites" {
				return
			}
			list := d.current()
			if !list.feed.Updated.IsZero() {
				ui.Text(c, "豆瓣榜单 · 更新于 "+list.feed.Updated.Local().Format("01-02 15:04")).FontSize(13).TextColor(p.textMuted)
			}
			if list.message == "" {
				if glassButton(c, "刷新榜单").Disabled(list.loading).Clicked() {
					a.loadDiscoveryTab(true)
				}
			}
		})
		ui.Column(c).Label("首页推荐").FillWidth().Height(recommendationHeight).MinHeight(recommendationHeight).Gap(14).Children(func() {
			if d.tab == "sites" {
				a.feedView(c)
			} else {
				a.discoveryView(c)
			}
		})
	})
}

// discoveryView is a Douban list. Douban names what is popular; choosing a
// poster looks for the title in the user's own library.
func (a *app) discoveryView(c *ui.Context) {
	list := a.discovery.current()
	if list.message != "" {
		ui.Row(c).Key("discovery-message").FillWidth().Gap(10).AlignItems(ui.Center).Transition(transInlineBanner).Children(func() {
			ui.Text(c, list.message).FontSize(13).Grow(1)
			if glassButton(c, "重试").Disabled(list.loading).Clicked() {
				a.loadDiscoveryTab(true)
			}
		})
	}
	if len(list.feed.Subjects) == 0 {
		if list.loading {
			emptyState(c, "正在加载豆瓣热播…", "也可以直接搜索片名。")
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
	grid := ui.GridView(c, &list.grid, len(list.feed.Subjects), 152, 252, func(i int) {
		subject := list.feed.Subjects[i]
		score := "暂无评分"
		if subject.Rating != "" {
			score = fmt.Sprintf("豆瓣 %s", subject.Rating)
		}
		card := a.videoCard(c, catalog.Video{ID: catalog.ID(subject.ID), Name: subject.Title, Pic: subject.Cover, Remarks: subject.Episodes}, score, false)
		if card.Clicked() {
			a.openTitleWithScore(subject.Title, subject.Cover, nil, subject.Rating)
		}
	}).Gap(14).Grow(1).FillWidth()
	// Arrows move the choice without opening it; a click or Enter opens it.
	if grid.Submitted() && list.selected >= 0 && list.selected < len(list.feed.Subjects) {
		subject := list.feed.Subjects[list.selected]
		list.selected = -1
		a.openTitleWithScore(subject.Title, subject.Cover, nil, subject.Rating)
	}
}
