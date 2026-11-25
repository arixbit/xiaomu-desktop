package main

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
	"github.com/arixbit/xiaomu-desktop/internal/source"
	"github.com/egoist/mygo/ui"
)

// Only episode metadata is saved. Playback addresses are always resolved anew.
func followingEpisodes(detail catalog.Detail) (count int, latest string) {
	for _, line := range detail.Sources {
		for _, episode := range line.Episodes {
			if n, err := strconv.Atoi(episodeButtonLabel(episode.Name)); err == nil && n > count {
				count, latest = n, episode.Name
			}
		}
	}
	if count > 0 {
		return count, fmt.Sprintf("第%d集", count)
	}
	for _, line := range detail.Sources {
		if len(line.Episodes) > count {
			count = len(line.Episodes)
			latest = line.Episodes[count-1].Name
		}
	}
	return
}

func (a *app) followingHistory(name string) (historyEntry, bool) {
	return a.history.findByTitle(name)
}

func (a *app) hasAnyFollowingNew() bool {
	for _, e := range a.favorites.entries {
		if a.followingHasNew(e) {
			return true
		}
	}
	return false
}

func (a *app) followingHasNew(e favoriteEntry) bool {
	if e.EpisodeCount <= 0 || e.Checked.IsZero() {
		return false
	}
	h, ok := a.followingHistory(e.Name)
	if !ok {
		return false
	}
	n, err := strconv.Atoi(episodeButtonLabel(h.Episode))
	latest, latestErr := strconv.Atoi(episodeButtonLabel(e.LatestEpisode))
	return err == nil && latestErr == nil && latest > n
}

func (a *app) followingProgress(e favoriteEntry) string {
	progress := "未开始"
	if h, ok := a.followingHistory(e.Name); ok {
		progress = "看到" + h.Episode
		if h.Episode == "" {
			progress = "已有观看记录"
		}
	}
	if e.LatestEpisode != "" && e.EpisodeCount > 0 {
		return progress + "／更新到" + e.LatestEpisode
	}
	return progress + "／待检查更新"
}

func (a *app) followingAction(e favoriteEntry) string {
	if h, ok := a.followingHistory(e.Name); ok {
		if !h.finished() {
			return "继续观看"
		}
		if a.followingHasNew(e) {
			return "看下一集"
		}
		return "查看详情"
	}
	return "开始观看"
}

// A freshly opened detail also updates a followed title, including a new site
// chosen by the viewer. A background reply cannot overwrite this newer choice.
func (a *app) updateFollowedTitle() {
	t := a.title
	if t == nil || t.detail == nil || t.item.Entry.Config == nil {
		return
	}
	entries := append([]favoriteEntry(nil), a.favorites.entries...)
	for i, e := range entries {
		if titleKey(e.Name) != titleKey(t.name) {
			continue
		}
		e.ConfigURL, e.EntryKey, e.SiteKey, e.VideoID = configURL(t.item.Entry), entryKey(t.item.Entry), t.item.Entry.Site.Key, string(t.item.Video.ID)
		if count, latest := followingEpisodes(*t.detail); count > 0 {
			e.EpisodeCount, e.LatestEpisode = count, latest
			e.Checked = time.Now()
		}
		if t.detail.Pic != "" {
			e.Pic = t.detail.Pic
		}
		entries[i] = e
		if err := a.favorites.replace(entries); err != nil {
			a.status = "保存追剧更新失败：" + err.Error()
		}
		return
	}
}

func (a *app) cancelFollowingRefresh() {
	f := &a.favorites
	if f.refreshCancel != nil {
		f.refreshCancel()
		f.refreshCancel = nil
	}
	f.refreshGeneration++
	f.refreshing = false
}

func (a *app) followingRefreshButton(c *ui.Context) {
	if a.library.root == nil {
		return
	}
	if a.favorites.refreshing {
		if glassButton(c, "取消检查").Label("取消检查更新").Clicked() {
			a.cancelFollowingRefresh()
			a.setNotice("已取消检查更新")
		}
		return
	}
	if glassButton(c, "刷新追剧").Clicked() {
		a.refreshFollowingMode(true)
	}
}

func (a *app) refreshFollowing() {
	a.refreshFollowingMode(false)
}

func (a *app) refreshFollowingMode(manual bool) {
	if a.closed || a.library.root == nil || a.favorites.readErr != nil || len(a.favorites.entries) == 0 {
		return
	}
	a.cancelFollowingRefresh()
	f := &a.favorites
	ctx, cancel := context.WithCancel(context.Background())
	f.refreshCancel, f.refreshing = cancel, true
	f.refreshErrors = map[string]string{}
	generation, started := f.refreshGeneration, time.Now()
	entries := append([]favoriteEntry(nil), f.entries...)
	root, cached, assets, dispatch := a.library.root, maps.Clone(a.library.cached), strings.TrimSpace(a.browser.assets), a.dispatch
	available := make(map[string]bool, len(entries))
	for _, e := range entries {
		available[e.ConfigURL] = a.configAvailable(e.ConfigURL)
	}
	update := func(fn func()) {
		guarded := func() {
			if !a.closed && f.refreshGeneration == generation {
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
		checkedNames := map[string]bool{}
		skippedCount := 0
		failedCount := 0
		emptyCount := 0
		// One title at a time bounds script runtimes and detail requests. A title
		// saved before a site was known uses the library's four-worker search.
		for _, e := range entries {
			if ctx.Err() != nil {
				break
			}
			if e.ConfigURL != "" && !available[e.ConfigURL] {
				skippedCount++
				continue
			}
			itemCtx, itemCancel := context.WithTimeout(ctx, 15*time.Second)
			item, session, detail, err := followingDetail(itemCtx, e, root, cached, assets)
			if session != nil {
				session.Close()
			}
			itemCancel()
			if ctx.Err() != nil {
				break
			}
			entry := e
			update(func() {
				if a.applyFollowingRefresh(entry, item, detail, err, started) {
					checkedNames[titleKey(entry.Name)] = true
				} else if err != nil {
					failedCount++
				} else {
					emptyCount++
				}
			})
		}
		update(func() {
			f.refreshCancel, f.refreshing = nil, false
			if manual {
				newCount := 0
				for _, e := range a.favorites.entries {
					if checkedNames[titleKey(e.Name)] && a.followingHasNew(e) {
						newCount++
					}
				}
				checkedCount := len(checkedNames)
				switch {
				case checkedCount == 0 && failedCount == 0 && emptyCount == 0 && skippedCount > 0:
					a.setNotice("当前片源无可检查的追剧（片源已移除）")
				case checkedCount == 0 && failedCount == 0 && emptyCount > 0:
					a.setNotice("追剧检查完成，暂未获取到最新分集")
				case newCount > 0 && failedCount > 0:
					a.setNotice(fmt.Sprintf("追剧检查完成：%d 部有新集，%d 部检查失败", newCount, failedCount))
				case newCount > 0:
					a.setNotice(fmt.Sprintf("追剧检查完成，%d 部有新集", newCount))
				case failedCount > 0:
					a.setNotice(fmt.Sprintf("追剧检查完成：%d 部检查失败，可稍后重试", failedCount))
				default:
					a.setNotice("追剧已检查，暂无新集")
				}
			}
		})
	}
	if dispatch == nil {
		work()
	} else {
		go work()
	}
}

func (a *app) applyFollowingRefresh(old favoriteEntry, item library.Item, detail catalog.Detail, err error, started time.Time) bool {
	entries := append([]favoriteEntry(nil), a.favorites.entries...)
	for i, e := range entries {
		if titleKey(e.Name) != titleKey(old.Name) || !e.Added.Equal(old.Added) || e.Checked.After(started) || e.EntryKey != old.EntryKey || e.VideoID != old.VideoID {
			continue
		}
		if err != nil {
			if a.favorites.refreshErrors == nil {
				a.favorites.refreshErrors = map[string]string{}
			}
			a.favorites.refreshErrors[titleKey(e.Name)] = "更新检查失败，可稍后重试"
			return false
		}
		count, latest := followingEpisodes(detail)
		if count == 0 {
			return false // An empty or temporary response must not erase known episodes.
		}
		e.ConfigURL, e.EntryKey, e.SiteKey, e.VideoID = configURL(item.Entry), entryKey(item.Entry), item.Entry.Site.Key, string(item.Video.ID)
		e.EpisodeCount, e.LatestEpisode, e.Checked = count, latest, time.Now()
		if detail.Pic != "" {
			e.Pic = detail.Pic
		}
		entries[i] = e
		if saveErr := a.favorites.replace(entries); saveErr != nil {
			if a.favorites.refreshErrors == nil {
				a.favorites.refreshErrors = map[string]string{}
			}
			a.favorites.refreshErrors[titleKey(e.Name)] = "追剧更新保存失败"
			return false
		}
		// Only a saved refresh with more episodes than the last
		// known count notifies; the first check has no baseline.
		a.notifyFollow(old, count, latest)
		return true
	}
	return false
}

func followingDetail(ctx context.Context, e favoriteEntry, root *config.Config, cached map[string]*config.Config, assets string) (library.Item, source.Session, catalog.Detail, error) {
	var item library.Item
	if e.ConfigURL != "" && e.VideoID != "" {
		doc := cached[e.ConfigURL]
		if root.URL == e.ConfigURL {
			doc = root
		}
		if doc == nil {
			release, err := source.AcquireBackground(ctx)
			if err != nil {
				return item, nil, catalog.Detail{}, err
			}
			doc, err = config.Read(ctx, e.ConfigURL, nil)
			release()
			if err != nil {
				return item, nil, catalog.Detail{}, err
			}
		}
		entry, ok := findSite(doc, e.EntryKey, e.SiteKey)
		if !ok {
			return item, nil, catalog.Detail{}, fmt.Errorf("片源中已没有这个站点")
		}
		item = library.Item{Entry: entry, Video: catalog.Video{ID: catalog.ID(e.VideoID), Name: e.Name, Pic: e.Pic}}
	} else {
		searchCtx, cancel := context.WithCancel(ctx)
		_ = library.Scan(searchCtx, root, library.Options{Assets: assets, Query: e.Name, Cached: cached}, func(event library.Event) {
			if item.Entry.Config != nil || event.Err != nil || event.Result == nil {
				return
			}
			for _, video := range event.Result.Page.Videos {
				if video.ID != "" && titleKey(video.Name) == titleKey(e.Name) {
					item = library.Item{Entry: event.Result.Target.Entry, Video: video}
					cancel()
					return
				}
			}
		})
		cancel()
		if item.Entry.Config == nil {
			return item, nil, catalog.Detail{}, fmt.Errorf("当前片库暂未找到这部剧")
		}
	}
	release, err := source.AcquireBackground(ctx)
	if err != nil {
		return item, nil, catalog.Detail{}, err
	}
	defer release()
	session, detail, err := openItem(ctx, item, assets)
	return item, session, detail, err
}

// The main follow action starts playback immediately; details remain available
// through a separate button.
func (a *app) openFollowing(e favoriteEntry) {
	if a.library.root == nil || e.ConfigURL != "" && !a.configAvailable(e.ConfigURL) || a.followingAction(e) == "查看详情" {
		a.openFavorite(e)
		return
	}
	if history, ok := a.followingHistory(e.Name); ok {
		a.resume(history)
		return
	}
	if item, ok := a.rememberedCandidate(e.Name, e.Pic); ok {
		e.ConfigURL, e.EntryKey, e.SiteKey, e.VideoID = configURL(item.Entry), entryKey(item.Entry), item.Entry.Site.Key, string(item.Video.ID)
	}
	root, cached, assets, sniff := a.library.root, maps.Clone(a.library.cached), strings.TrimSpace(a.browser.assets), a.sniffPage
	a.cancelJob(jobTitle)
	a.run(jobPlay, "打开追剧", func(ctx context.Context) (jobResult, error) {
		item, session, detail, err := followingDetail(ctx, e, root, cached, assets)
		if err != nil {
			return jobResult{}, err
		}
		line, episode, at, ok := followingResumePoint(detail, historyEntry{}, false)
		if !ok {
			session.Close()
			return jobResult{}, fmt.Errorf("暂时没有可播放的分集")
		}
		play, err := resolvePlay(ctx, session, resolve.Resolver{Sniff: sniff, Parsers: entryParsers(item.Entry)}, detail.Sources[line].Name, episode)
		if err != nil {
			session.Close()
			return jobResult{}, err
		}
		return jobResult{discard: session.Close, apply: func() {
			a.clearTitle()
			a.title = &titlePage{name: e.Name, pic: e.Pic, seeds: []library.Item{item}, failed: map[string]string{}, item: item, session: session, detail: &detail, line: line}
			a.startEpisode(play, detail.Sources[line].Name, episode, at)
		}}, nil
	}, nil)
}

func followingResumePoint(detail catalog.Detail, h historyEntry, hasHistory bool) (line int, episode catalog.Episode, at time.Duration, ok bool) {
	if hasHistory {
		// Prefer the recorded line, then permit only an exact/numbered match.
		for pass := range 2 {
			for i, source := range detail.Sources {
				if pass == 0 && source.Name != h.Line || pass == 1 && source.Name == h.Line {
					continue
				}
				for j, candidate := range source.Episodes {
					if candidate.Name != h.Episode && episodeButtonLabel(candidate.Name) != episodeButtonLabel(h.Episode) {
						continue
					}
					if h.finished() && j+1 < len(source.Episodes) {
						return i, source.Episodes[j+1], 0, true
					}
					if h.finished() {
						continue
					}
					return i, candidate, time.Duration(max(0, h.Position) * float64(time.Second)), true
				}
			}
		}
		return // A missing episode or a caught-up series has no resume target.
	}
	for i, source := range detail.Sources {
		if len(source.Episodes) > 0 {
			return i, source.Episodes[0], 0, true
		}
	}
	return
}
