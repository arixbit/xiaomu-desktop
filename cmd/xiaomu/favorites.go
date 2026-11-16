package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

// Favorites store a title and an optional site reference, never resolved media.
// A title can be saved before its sources finish loading.
type favoriteEntry struct {
	Name      string    `json:"name"`
	Pic       string    `json:"pic,omitempty"`
	ConfigURL string    `json:"config_url,omitempty"`
	EntryKey  string    `json:"entry_key,omitempty"`
	SiteKey   string    `json:"site_key,omitempty"`
	VideoID   string    `json:"video_id,omitempty"`
	Added     time.Time `json:"added"`
}

type favoriteStore struct {
	entries []favoriteEntry
	path    string
	readErr error
}

func (f *favoriteStore) contains(name string) bool {
	for _, e := range f.entries {
		if titleKey(e.Name) == titleKey(name) {
			return true
		}
	}
	return false
}

// replace writes before updating the UI so a failed save cannot appear saved.
func (f *favoriteStore) replace(entries []favoriteEntry) error {
	if f.readErr != nil {
		return f.readErr
	}
	if f.path != "" {
		if err := writeJSONFile(f.path, entries); err != nil {
			return err
		}
	}
	f.entries = entries
	return nil
}

func readFavorites(path string) ([]favoriteEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []favoriteEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("读取收藏：%w", err)
	}
	return entries, nil
}

func (a *app) restoreFavorites() {
	sourcePath, err := savedSourcePath()
	if err != nil {
		a.favorites.readErr = err
		return
	}
	a.favorites.path = filepath.Join(filepath.Dir(sourcePath), "favorites.json")
	a.favorites.entries, a.favorites.readErr = readFavorites(a.favorites.path)
	if a.favorites.readErr != nil {
		a.status = a.favorites.readErr.Error()
	}
}

func (a *app) removeFavorite(name string) {
	kept := make([]favoriteEntry, 0, len(a.favorites.entries))
	for _, e := range a.favorites.entries {
		if titleKey(e.Name) != titleKey(name) {
			kept = append(kept, e)
		}
	}
	if err := a.favorites.replace(kept); err != nil {
		a.status = "保存收藏失败：" + err.Error()
	}
}

func (a *app) toggleFavorite() {
	t := a.title
	if t == nil || titleKey(t.name) == "" {
		return
	}
	if a.favorites.contains(t.name) {
		a.removeFavorite(t.name)
		return
	}
	e := favoriteEntry{Name: t.name, Pic: t.pic, Added: time.Now()}
	item := t.item
	if t.detail != nil {
		e.Pic = t.detail.Pic
	} else if len(t.seeds) > 0 {
		item = t.seeds[0]
	}
	if item.Entry.Config != nil {
		e.ConfigURL, e.EntryKey, e.SiteKey, e.VideoID = configURL(item.Entry), entryKey(item.Entry), item.Entry.Site.Key, string(item.Video.ID)
	}
	entries := append([]favoriteEntry{e}, a.favorites.entries...)
	if err := a.favorites.replace(entries); err != nil {
		a.status = "保存收藏失败：" + err.Error()
	}
}

func (a *app) favoriteButton(c *ui.Context) {
	label := "加入想看"
	if a.favorites.contains(a.title.name) {
		label = "已收藏 · 取消"
	}
	if glassButton(c, label).AlignSelf(ui.Start).Clicked() {
		a.toggleFavorite()
	}
}

// Open the saved site when it is still imported. Otherwise search the current
// library; removing or replacing a configuration never removes favorites.
func (a *app) openFavorite(e favoriteEntry) {
	if a.library.root == nil {
		a.status = "请先添加片源，再打开收藏的影片"
		return
	}
	if !a.configAvailable(e.ConfigURL) || e.VideoID == "" {
		a.openTitle(e.Name, e.Pic, nil)
		return
	}
	cached := a.library.cached[e.ConfigURL]
	if a.library.root.URL == e.ConfigURL {
		cached = a.library.root
	}
	a.run(jobTitle, "打开收藏", func(ctx context.Context) (jobResult, error) {
		doc := cached
		if doc == nil {
			var err error
			doc, err = config.Read(ctx, e.ConfigURL, nil)
			if err != nil {
				return jobResult{}, err
			}
		}
		var seeds []library.Item
		if entry, ok := findSite(doc, e.EntryKey, e.SiteKey); ok {
			seeds = []library.Item{{Entry: entry, Video: catalog.Video{ID: catalog.ID(e.VideoID), Name: e.Name, Pic: e.Pic}}}
		}
		return jobResult{apply: func() { a.showTitle(e.Name, e.Pic, seeds, true) }}, nil
	}, func(error) { a.openTitle(e.Name, e.Pic, nil) })
}

func (a *app) favoritesView(c *ui.Context) {
	if a.favorites.readErr != nil {
		emptyState(c, "收藏暂时无法读取", a.favorites.readErr.Error())
		return
	}
	if len(a.favorites.entries) == 0 {
		emptyState(c, "还没有收藏", "在影片详情页点“加入想看”，下次从这里打开。")
		return
	}
	ui.Column(c).Fill().Gap(14).Children(func() {
		ui.Text(c, "想看的影片 · 只保存在这台 Mac 上").FontSize(13).TextColor(textMuted)
		ui.Scroll(c).Grow(1).FillWidth().Gap(8).Children(func() {
			for _, e := range a.favorites.entries {
				ui.Row(c).Key(titleKey(e.Name)).FillWidth().Padding(10).Gap(12).Radius(18).AlignItems(ui.Center).Background(surface).Children(func() {
					ui.Box(c).Size(60, 90).Radius(10).Clip().Background(ui.Hex("#24242c")).Children(func() {
						if poster := a.poster(e.Pic); poster != nil {
							ui.Image(c, poster).Fill().Fit(ui.Cover)
						}
					})
					ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
						ui.Text(c, e.Name).Bold().SingleLine()
						ui.Text(c, e.Added.Local().Format("2006-01-02")+" 加入想看").FontSize(12).TextColor(textMuted)
						if a.library.root == nil {
							ui.Text(c, "添加片源后可选集播放").FontSize(12).TextColor(textMuted)
						}
					})
					if primaryButton(c, "打开").Label("打开收藏 " + e.Name).Clicked() {
						a.openFavorite(e)
					}
					if glassButton(c, "移除").Label("移除收藏 " + e.Name).Clicked() {
						a.removeFavorite(e.Name)
					}
				})
			}
		})
	})
}
