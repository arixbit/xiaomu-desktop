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
	Name          string    `json:"name"`
	Pic           string    `json:"pic,omitempty"`
	ConfigURL     string    `json:"config_url,omitempty"`
	EntryKey      string    `json:"entry_key,omitempty"`
	SiteKey       string    `json:"site_key,omitempty"`
	VideoID       string    `json:"video_id,omitempty"`
	Added         time.Time `json:"added"`
	EpisodeCount  int       `json:"episode_count,omitempty"`
	LatestEpisode string    `json:"latest_episode,omitempty"`
	Checked       time.Time `json:"checked,omitempty"`
}

type favoriteStore struct {
	entries           []favoriteEntry
	path              string
	readErr           error
	refreshCancel     context.CancelFunc
	refreshGeneration uint64
	refreshing        bool
	refreshErrors     map[string]string
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
		return nil, fmt.Errorf("读取追剧列表：%w", err)
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
	var removed *favoriteEntry
	removedIdx := -1
	for i, e := range a.favorites.entries {
		if titleKey(e.Name) != titleKey(name) {
			kept = append(kept, e)
		} else if removed == nil {
			cp := e
			removed = &cp
			removedIdx = i
		}
	}
	if err := a.favorites.replace(kept); err != nil {
		a.status = "保存追剧失败：" + err.Error()
		return
	}
	if removed != nil {
		entry := *removed
		idx := removedIdx
		a.setUndoNotice("已取消追剧「"+entry.Name+"」", "撤销", func() {
			if a.favorites.contains(entry.Name) {
				return
			}
			pos := min(max(idx, 0), len(a.favorites.entries))
			restored := make([]favoriteEntry, 0, len(a.favorites.entries)+1)
			restored = append(restored, a.favorites.entries[:pos]...)
			restored = append(restored, entry)
			restored = append(restored, a.favorites.entries[pos:]...)
			if err := a.favorites.replace(restored); err != nil {
				a.status = "保存追剧失败：" + err.Error()
			}
		})
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
	if t.detail != nil {
		e.EpisodeCount, e.LatestEpisode = followingEpisodes(*t.detail)
		e.Checked = time.Now()
	}
	entries := append([]favoriteEntry{e}, a.favorites.entries...)
	if err := a.favorites.replace(entries); err != nil {
		a.status = "保存追剧失败：" + err.Error()
	} else {
		a.setNotice("已加入追剧")
	}
}

func (a *app) favoriteButton(c *ui.Context) {
	label := "加入追剧"
	if a.favorites.contains(a.title.name) {
		label = "已追剧 · 取消"
	}
	if glassButton(c, label).AlignSelf(ui.Start).Clicked() {
		a.toggleFavorite()
	}
}

// Open the saved site when it is still imported. Otherwise search the current
// library; removing or replacing a configuration never removes favorites.
func (a *app) openFavorite(e favoriteEntry) {
	if a.library.root == nil {
		a.status = "请先添加片源，再打开追剧影片"
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
	a.run(jobTitle, "打开追剧", func(ctx context.Context) (jobResult, error) {
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
		return jobResult{apply: func() { a.showTitle(e.Name, e.Pic, seeds, true, "") }}, nil
	}, func(error) { a.openTitle(e.Name, e.Pic, nil) })
}

// favoritesStrip is the home page's shortcut to saved titles: one row above
// the hot lists, so a common title opens in two steps instead of a trip to
// the sidebar. It shows only when there is something saved.
func (a *app) favoritesStrip(c *ui.Context) {
	if len(a.favorites.entries) == 0 {
		return
	}
	p := paletteFor(c)
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "我的追剧").FontSize(16).Bold()
			ui.Spacer(c)
			a.followingRefreshButton(c)
			if glassButton(c, "全部追剧").Clicked() {
				a.switchTab(routeFavorites)
			}
		})
		ui.ScrollHorizontal(c).FillWidth().Gap(12).Children(func() {
			for _, e := range a.favorites.entries {
				card := ui.Column(c).Key("favstrip-" + titleKey(e.Name)).Width(104).Gap(6).
					Cursor(ui.CursorPointer).Label("打开追剧 " + e.Name)
				card.Children(func() {
					posterBox := ui.Box(c).Size(104, 152).Radius(12).Clip().Background(p.cardPlaceholder)
					posterBox.Children(func() {
						applyPosterImage(c, posterBox, a.poster(c, e.Pic))
						if a.followingHasNew(e) {
							ui.Text(c, "有新集").Absolute().Left(6).Top(6).Padding(2, 7).Radius(8).
								FontSize(11).Bold().Background(p.accent).TextColor(p.accentText).SingleLine()
						}
					})
					ui.Text(c, e.Name).FontSize(13).Bold().SingleLine()
					ui.Text(c, a.followingProgress(e)).FontSize(11).TextColor(p.textMuted).SingleLine()
				})
				applyCardPose(card)
				if card.Clicked() {
					a.openFollowing(e)
				}
			}
		})
	})
}

func (a *app) favoritesView(c *ui.Context) {
	p := paletteFor(c)
	if a.favorites.readErr != nil {
		emptyState(c, "追剧列表暂时无法读取", "本地追剧文件格式异常，可检查后重试。")
		return
	}
	if len(a.favorites.entries) == 0 {
		emptyState(c, "还没有追剧", "在影片详情页点「加入追剧」，这里会显示观看进度和更新。")
		return
	}
	ui.Column(c).Fill().Gap(14).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "追剧和进度 · 只保存在本机").FontSize(13).TextColor(p.textMuted).Grow(1)
			a.followingRefreshButton(c)
		})
		ui.Scroll(c).Grow(1).FillWidth().Gap(8).Children(func() {
			for _, e := range a.favorites.entries {
				action := a.followingAction(e)
				row := ui.Row(c).Key(titleKey(e.Name)).FillWidth().Padding(10).Gap(12).AlignItems(ui.Center)
				applySurface(row, p, 18).Children(func() {
					mainArea := ui.Row(c).Grow(1).MinWidth(0).Gap(12).AlignItems(ui.Center).Cursor(ui.CursorPointer)
					mainArea.Children(func() {
						posterBox := ui.Box(c).Size(60, 90).Radius(10).Clip().Background(p.cardPlaceholder)
						posterBox.Children(func() {
							applyPosterImage(c, posterBox, a.poster(c, e.Pic))
						})
						ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
							ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
								ui.Text(c, e.Name).Bold().SingleLine()
								if a.followingHasNew(e) {
									ui.Text(c, "有新集").Padding(2, 8).Radius(9).FontSize(11).Bold().
										Background(p.accent).TextColor(p.accentText).SingleLine()
								}
							})
							ui.Text(c, a.followingProgress(e)).FontSize(12).TextColor(p.textMuted).SingleLine()
							if message := a.favorites.refreshErrors[titleKey(e.Name)]; message != "" {
								ui.Text(c, message).FontSize(11).TextColor(p.warning).SingleLine()
							}
							if a.library.root == nil {
								ui.Text(c, "添加片源后可选集播放").FontSize(12).TextColor(p.textMuted)
							}
						})
					})
					if mainArea.Clicked() {
						a.openFollowing(e)
					}
					ui.Column(c).Width(108).Gap(6).Children(func() {
						if primaryButton(c, action).FillWidth().Label("打开追剧 " + e.Name).Clicked() {
							a.openFollowing(e)
						}
						if action != "查看详情" {
							if glassButton(c, "详情").FillWidth().Label("追剧详情 " + e.Name).Clicked() {
								a.openFavorite(e)
							}
						}
						if glassButton(c, "移除").FillWidth().Label("移除追剧 " + e.Name).Clicked() {
							a.removeFavorite(e.Name)
						}
					})
				})
			}
		})
	})
}
