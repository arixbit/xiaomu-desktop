package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/douban"
	"github.com/egoist/mygo/plugins/glass"
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
					list.message = "豆瓣榜单暂时无法加载，可重试或浏览站点推荐。"
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

func (a *app) discoveryTabs(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		for _, tab := range []struct{ key, label string }{{"movie", "热播电影"}, {"tv", "热播剧集"}, {"sites", "站点推荐"}} {
			selected := a.discovery.tab == tab.key || (a.discovery.tab == "" && tab.key == "movie")
			if ui.Button(c, tab.label).Key(tab.key).Selected(selected).Radius(18).
				Material(glass.Glass{Style: glass.Clear, Interactive: true}).Clicked() {
				a.discovery.tab = tab.key
				a.loadDiscoveryTab(false)
			}
		}
	})
}

func (a *app) discoveryTitle() string {
	if a.discovery.tab == string(douban.TV) {
		return "豆瓣热播剧集"
	}
	return "豆瓣热播电影"
}

func (a *app) discoveryView(c *ui.Context) {
	list := a.discovery.current()
	ui.Row(c).FillWidth().Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Text(c, "点击影片，全库找片").FontSize(13).Grow(1).TextColor(c.Theme().TextMuted)
		if !list.feed.Updated.IsZero() {
			ui.Text(c, "更新于 "+list.feed.Updated.Local().Format("01-02 15:04")).FontSize(12).TextColor(c.Theme().TextMuted)
		}
	})
	if list.message != "" {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, list.message).FontSize(13).Grow(1)
			if ui.Button(c, "重试").Disabled(list.loading).Clicked() {
				a.loadDiscoveryTab(true)
			}
		})
	}
	if len(list.feed.Subjects) == 0 {
		ui.Column(c).Grow(1).FillWidth().Center().Gap(12).Children(func() {
			text := "暂无豆瓣榜单"
			if list.loading {
				text = "正在加载豆瓣热播…"
			}
			ui.Text(c, text).FontSize(24).Bold()
			if ui.Button(c, "浏览站点推荐").Clicked() {
				a.discovery.tab = "sites"
			}
		})
		return
	}
	if list.loading {
		ui.Text(c, "正在更新榜单…").FontSize(12).TextColor(c.Theme().TextMuted)
	}
	list.grid.Selected = &list.selected
	list.grid.Key = func(i int) any { return list.feed.Subjects[i].ID }
	list.grid.Label = func(i int) string { return "搜索影片 " + list.feed.Subjects[i].Title }
	if ui.GridView(c, &list.grid, len(list.feed.Subjects), 148, 238, func(i int) {
		subject := list.feed.Subjects[i]
		ui.Box(c).Fill().Children(func() {
			a.videoCard(c, catalog.Video{ID: catalog.ID(subject.ID), Name: subject.Title, Pic: subject.Cover, Remarks: subject.Episodes})
			score := "暂无评分"
			if subject.Rating != "" {
				score = fmt.Sprintf("豆瓣 %s", subject.Rating)
			}
			ui.Text(c, score).Absolute().Right(8).Top(8).Padding(4, 7).Radius(7).
				Background(ui.RGBA(10, 13, 24, 220)).TextColor(ui.RGB(255, 255, 255)).FontSize(12).Bold()
		})
	}).Grow(1).FillWidth().Changed() && list.selected >= 0 && list.selected < len(list.feed.Subjects) && !a.busy {
		a.browser.query = list.feed.Subjects[list.selected].Title
		list.selected = -1
		a.searchLibrary()
	}
}
