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
	p := paletteFor(c)
	followed := a.favorites.contains(a.title.name)
	label := "加入追剧"
	tip := "加入追剧，自动跟踪更新"
	icon := navIconFavorites
	bg := p.controlBg
	border := p.borderControl
	fg := p.text
	iconColor := p.text
	a11yLabel := label
	if followed {
		label = "已追剧"
		a11yLabel = "已追剧 · 取消"
		tip = "已追剧 · 点击取消"
		icon = navIconFavoritesFilled
		bg = p.accentSoft
		border = p.accentBorder
		fg = p.accentLightText
		iconColor = p.accent
	}
	btn := ui.ButtonBase(c.Key("detail-favorite")).Height(38).Padding(0, 15).Gap(7).Radius(9).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(a11yLabel).Tooltip(tip)
	if !followed && btn.Hovered() {
		bg = p.controlHover
		border = ui.RGBA(255, 255, 255, 0.20)
	}
	animFg := animateButtonChrome(btn, "fav-btn", bg, border, fg)
	animIconColor := btn.AnimateColor("fav-btn-ico", iconColor, animColor)
	applyPressPose(btn)
	btn.Children(func() {
		ui.Icon(c, icon).Size(15, 15).TextColor(animIconColor).ContentTransition(ctIcon).PassThrough()
		t := ui.Text(c, label).FontSize(13).TextColor(animFg).SingleLine().ContentTransition(ctReplace).PassThrough()
		if followed {
			t.Bold()
		}
	})
	if btn.Clicked() {
		a.toggleFavorite()
	}
}

// Open the saved site when it is still imported. Otherwise search the current
// library; removing or replacing a configuration never removes favorites.
func (a *app) openFavorite(e favoriteEntry) {
	if a.library.root == nil {
		a.setNotice("请先添加片源，再打开追剧影片")
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

func (a *app) findFavorite(name string) (favoriteEntry, bool) {
	key := titleKey(name)
	if key == "" {
		return favoriteEntry{}, false
	}
	for _, e := range a.favorites.entries {
		if titleKey(e.Name) == key {
			return e, true
		}
	}
	return favoriteEntry{}, false
}

// homeFavoriteEntries orders followed titles with new episodes first while
// preserving relative order within each group. When deferKey matches the title
// currently spotlighted in the left continue-watching card, it is demoted to
// avoid showing the exact same show side-by-side when other followed titles exist.
func (a *app) homeFavoriteEntries(limit int, deferKey string) []favoriteEntry {
	entries := a.favorites.entries
	if len(entries) == 0 {
		return nil
	}
	ordered := make([]favoriteEntry, 0, len(entries))
	var deferred []favoriteEntry
	for _, e := range entries {
		if a.followingHasNew(e) {
			if deferKey != "" && titleKey(e.Name) == deferKey {
				deferred = append(deferred, e)
				continue
			}
			ordered = append(ordered, e)
		}
	}
	for _, e := range entries {
		if !a.followingHasNew(e) {
			if deferKey != "" && titleKey(e.Name) == deferKey {
				deferred = append(deferred, e)
				continue
			}
			ordered = append(ordered, e)
		}
	}
	ordered = append(ordered, deferred...)
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

func (a *app) homeFavoritesHeader(c *ui.Context) {
	p := paletteFor(c)
	ui.Row(c).FillWidth().Height(28).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, navIconFavoritesFilled).Size(13.5, 13.5).TextColor(p.accent).Shrink(0).PassThrough()
		ui.Text(c, "我的追剧").FontSize(15).Bold().TextColor(p.text).SingleLine().Shrink(0)
		ui.Spacer(c)
		if a.library.root != nil {
			refreshing := a.favorites.refreshing
			refA11y, refText := "刷新追剧", "刷新"
			refFg := p.textFaint
			if refreshing {
				refA11y, refText = "取消检查更新", "取消检查"
				refFg = p.accentLightText
			}
			refBtn := ui.ButtonBase(c.Key("home-fav-refresh")).Padding(3, 6).Radius(6).
				Cursor(ui.CursorPointer).Label(refA11y).Shrink(0)
			if refBtn.Hovered() {
				if refreshing {
					refFg = p.accent
				} else {
					refFg = p.text
				}
			}
			animRefFg := refBtn.AnimateColor("hfav-ref-fg", refFg, animColor)
			applyPressPose(refBtn)
			refBtn.Children(func() {
				ui.Text(c, refText).FontSize(11.5).TextColor(animRefFg).SingleLine().ContentTransition(ctReplace).PassThrough()
			})
			if refBtn.Clicked() {
				if refreshing {
					a.cancelFollowingRefresh()
					a.setNotice("已取消检查更新")
				} else {
					a.refreshFollowingMode(true)
				}
			}
		}
		allBtn := ui.ButtonBase(c.Key("home-fav-all")).Padding(3, 6).Radius(6).
			Cursor(ui.CursorPointer).Label("全部追剧").Shrink(0)
		allFg := p.textMuted
		if allBtn.Hovered() {
			allFg = p.text
		}
		animAllFg := allBtn.AnimateColor("hfav-all-fg", allFg, animColor)
		applyPressPose(allBtn)
		allBtn.Children(func() {
			ui.Text(c, fmt.Sprintf("全部 %d ›", len(a.favorites.entries))).FontSize(12).FontFeatures("tnum").
				TextColor(animAllFg).SingleLine().ContentTransition(ctNumeric).PassThrough()
		})
		if allBtn.Clicked() {
			a.switchTab(routeFavorites)
		}
	})
}

func (a *app) homeFavoriteCard(c *ui.Context, e favoriteEntry, fixedWidth float32) {
	p := paletteFor(c)
	hasNew := a.followingHasNew(e)
	_, hasHist := a.followingHistory(e.Name)
	action := a.followingAction(e)

	card := ui.Row(c).Key("favcard-"+titleKey(e.Name)).Height(92).Padding(8, 10).Gap(10).AlignItems(ui.Center)
	if fixedWidth > 0 {
		card.Width(fixedWidth).Shrink(0)
	} else {
		card.FillWidth()
	}
	applyInteractiveSurface(card, p, 12, card.Hovered())
	card.Children(func() {
		posterBox := ui.Box(c).Size(52, 74).Shrink(0).Radius(7).Clip().
			Background(p.cardPlaceholder).Border(1, p.border).Cursor(ui.CursorPointer)
		posterBox.Children(func() {
			applyPosterImage(c, posterBox, a.poster(c, e.Pic))
		})
		if posterBox.Clicked() {
			a.openFavorite(e)
		}

		ui.Column(c).Grow(1).MinWidth(0).FillHeight().Justify(ui.SpaceBetween).Children(func() {
			ui.Column(c).FillWidth().Gap(3).Children(func() {
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, e.Name).FontSize(13.5).Bold().TextColor(p.text).SingleLine()
					if hasNew {
						ui.Text(c, "有新集").Shrink(0).Padding(1.5, 6).Radius(5).FontSize(10).Bold().
							Background(p.accent).TextColor(p.accentText).SingleLine()
					} else if !hasHist {
						ui.Text(c, "未开始").Shrink(0).Padding(1.5, 6).Radius(5).FontSize(10).
							Background(p.controlBg).Border(1, p.border).TextColor(p.textMuted).SingleLine()
					}
				})
				ui.Text(c, a.followingProgress(e)).FontSize(11.5).TextColor(p.textMuted).SingleLine()
			})

			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				actBtn := ui.ButtonBase(c.Key("hfav-act-"+titleKey(e.Name))).Height(24).Padding(0, 10).Radius(7).
					Center().Cursor(ui.CursorPointer).Label("打开追剧 " + e.Name)
				actBg := p.controlBg
				actBorder := p.borderControl
				actFg := p.text
				if actBtn.Hovered() {
					actBg = p.accent
					actBorder = p.accent
					actFg = p.accentText
				}
				animActFg := animateButtonChrome(actBtn, "hfav-act", actBg, actBorder, actFg)
				applyPressPose(actBtn)
				actBtn.Children(func() {
					ui.Text(c, action).FontSize(11.5).TextColor(animActFg).SingleLine().PassThrough()
				})
				if actBtn.Clicked() {
					a.openFollowing(e)
				}

				detailLink := ui.ButtonBase(c.Key("hfav-detail-"+titleKey(e.Name))).Padding(2, 4).Radius(5).
					Cursor(ui.CursorPointer).Label("追剧详情 " + e.Name).Shrink(0)
				detailFg := p.textFaint
				if detailLink.Hovered() {
					detailFg = p.text
				}
				animDetailFg := detailLink.AnimateColor("hfav-det-fg", detailFg, animColor)
				applyPressPose(detailLink)
				detailLink.Children(func() {
					ui.Text(c, "详情 ›").FontSize(11.5).TextColor(animDetailFg).SingleLine().PassThrough()
				})
				if detailLink.Clicked() {
					a.openFavorite(e)
				}
			})
		})
	})
}

// homeFavoritesColumn renders the right-column compact followed list in the
// wide home H1 split focus area (up to 2 stacked matte cards).
func (a *app) homeFavoritesColumn(c *ui.Context) {
	deferKey := ""
	if cw := a.continueWatching(); len(cw) > 0 {
		idx := a.homeResumeIdx
		if idx < 0 || idx >= len(cw) {
			idx = 0
		}
		deferKey = titleKey(cw[idx].Name)
	}
	shown := a.homeFavoriteEntries(2, deferKey)
	if len(shown) == 0 {
		return
	}
	ui.Column(c).Key("home-fav-column").FillWidth().Gap(8).Children(func() {
		a.homeFavoritesHeader(c)
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			for _, e := range shown {
				a.homeFavoriteCard(c, e, 0)
			}
		})
	})
}

// favoritesStrip is the home page's horizontal shortcut strip when favorites
// appear standalone or stacked on narrow windows.
func (a *app) favoritesStrip(c *ui.Context) {
	ordered := a.homeFavoriteEntries(0, "")
	if len(ordered) == 0 {
		return
	}
	ui.Column(c).Key("home-fav-strip").FillWidth().Gap(8).Children(func() {
		a.homeFavoritesHeader(c)
		ui.ScrollHorizontal(c).FillWidth().Gap(10).Scrollbars(false).Children(func() {
			for _, e := range ordered {
				a.homeFavoriteCard(c, e, 276)
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
		if a.library.root == nil {
			emptyStateWithAction(c, "还没有追剧", "在影片详情页点「加入追剧」，这里会显示观看进度和更新。", "添加片源", func() {
				a.openImportDialog()
			})
		} else {
			emptyStateWithAction(c, "还没有追剧", "在影片详情页点「加入追剧」，这里会显示观看进度和更新。", "去首页看看", func() {
				a.switchTab(routeHome)
			})
		}
		return
	}
	winW, _ := c.Size()
	twoCols := winW >= 760
	hasLib := a.library.root != nil
	ui.Column(c).Fill().Gap(12).Children(func() {
		ui.Row(c).FillWidth().Padding(0, scrollGutter, 0, 0).Gap(10).AlignItems(ui.Center).Children(func() {
			note := fmt.Sprintf("共追 %d 部剧 · 进度与更新仅保存在本机", len(a.favorites.entries))
			if !hasLib {
				note = fmt.Sprintf("当前未添加片源 · 共追 %d 部剧仍保存在本机", len(a.favorites.entries))
			}
			ui.Text(c, note).FontSize(13).FontFeatures("tnum").TextColor(p.textMuted).Grow(1).ContentTransition(ctNumeric)
			if !hasLib {
				if primaryButton(c, "添加片源").Clicked() {
					a.openImportDialog()
				}
			} else {
				a.followingRefreshButton(c)
			}
		})
		ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Padding(0, scrollGutter, 0, 0).Gap(12).ScrollbarStyle(appScrollbar).Children(func() {
			entries := a.favorites.entries
			if twoCols {
				for i := 0; i < len(entries); i += 2 {
					ui.Row(c).Key(fmt.Sprintf("fav-row-%d", i/2)).FillWidth().Gap(12).AlignItems(ui.Stretch).Children(func() {
						a.favoriteGridCard(c, entries[i], hasLib).Grow(1).MinWidth(0)
						if i+1 < len(entries) {
							a.favoriteGridCard(c, entries[i+1], hasLib).Grow(1).MinWidth(0)
						} else {
							ui.Box(c).Grow(1).MinWidth(0)
						}
					})
				}
				return
			}
			for _, e := range entries {
				a.favoriteGridCard(c, e, hasLib).FillWidth()
			}
		})
	})
}

func (a *app) favoriteGridCard(c *ui.Context, e favoriteEntry, hasLib bool) ui.Element {
	p := paletteFor(c)
	key := titleKey(e.Name)
	action := a.followingAction(e)
	hasNew := a.followingHasNew(e)
	hist, hasHist := a.followingHistory(e.Name)

	card := ui.Box(c).Key("fav-" + key)
	applyInteractiveSurface(card, p, 12, card.Hovered())
	card.Children(func() {
		ui.Row(c).FillWidth().Padding(12).Gap(14).AlignItems(ui.Center).Children(func() {
			posterBox := ui.Box(c).Size(76, 110).Shrink(0).Radius(9).Clip().
				Background(p.cardPlaceholder).Border(1, p.border)
			if hasLib {
				posterBox.Cursor(ui.CursorPointer)
			}
			posterBox.Children(func() {
				applyPosterImage(c, posterBox, a.poster(c, e.Pic))
			})
			if hasLib && posterBox.Clicked() {
				a.openFavorite(e)
			}

			ui.Column(c).Grow(1).MinWidth(0).Gap(7).Padding(0, 24, 0, 0).Children(func() {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, e.Name).FontSize(15).Bold().TextColor(p.text).SingleLine()
					if hasNew {
						ui.Text(c, "有新集").Shrink(0).Padding(2, 7).Radius(5).FontSize(10.5).Bold().
							Background(p.accent).TextColor(p.accentText).SingleLine()
					}
				})

				ui.Text(c, a.followingProgress(e)).FontSize(12).TextColor(p.textMuted).SingleLine()

				if hasHist && hist.Duration > 0 {
					progress := max(0, min(hist.Position/hist.Duration, 1))
					ui.Progress(c, progress).Height(3).FillWidth().Label("追剧进度 " + e.Name)
				} else {
					ui.Box(c).FillWidth().Height(3).Radius(1.5).Background(ui.RGBA(255, 255, 255, 0.07)).PassThrough()
				}

				if message := a.favorites.refreshErrors[key]; message != "" {
					ui.Text(c, message).FontSize(11).TextColor(p.warning).SingleLine()
				}
				if !hasLib {
					ui.Text(c, "添加片源后可选集播放").FontSize(12).TextColor(p.textFaint).SingleLine()
				}

				if hasLib {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Padding(2, 0, 0, 0).Children(func() {
						actBtn := ui.ButtonBase(c.Key("fav-act-"+key)).Height(32).Padding(0, 14).Gap(6).Radius(8).
							AlignItems(ui.Center).Cursor(ui.CursorPointer).Label("打开追剧 " + e.Name)
						bg := p.accent
						if actBtn.Hovered() {
							bg = p.accentHover
						}
						actBtn.Background(actBtn.AnimateColor("fact-bg", bg, animColor))
						applyPressPose(actBtn)
						actBtn.Children(func() {
							if action != "查看详情" {
								ui.Icon(c, iconPlay).Size(10, 10).TextColor(p.accentText).PassThrough()
							}
							ui.Text(c, action).FontSize(12.5).Bold().TextColor(p.accentText).SingleLine().PassThrough()
						})
						if actBtn.Clicked() {
							a.openFollowing(e)
						}

						if action != "查看详情" {
							if glassButton(c.Key("fav-detail-"+key), "详情").Height(32).Padding(0, 13).FontSize(12.5).
								Label("追剧详情 " + e.Name).Clicked() {
								a.openFavorite(e)
							}
						}
					})
				}
			})
		})

		removeBtn := ui.ButtonBase(c.Key("fav-remove-"+key)).
			Absolute().Top(8).Right(8).Size(24, 24).Radius(7).Center().
			Cursor(ui.CursorPointer).Label("移出追剧 " + e.Name).Tooltip("移出追剧")
		rmBg := ui.Transparent
		rmFg := p.textFaint
		if removeBtn.Hovered() {
			rmBg = p.surfaceHover
			rmFg = p.text
		}
		removeBtn.Background(removeBtn.AnimateColor("frm-bg", rmBg, animColor))
		applyPressPose(removeBtn)
		removeBtn.Children(func() {
			ui.Icon(c, iconClose).Size(12, 12).TextColor(removeBtn.AnimateColor("frm-fg", rmFg, animColor)).PassThrough()
		})
		if removeBtn.Clicked() {
			a.removeFavorite(e.Name)
		}
	})
	return card
}
