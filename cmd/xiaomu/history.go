package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
	"github.com/egoist/mygo/ui"
)

const (
	historyLimit    = 200
	historySaveGap  = 15 * time.Second
	historyEndSlack = 5 // seconds from the end that count as watched
)

// historyEntry is where the viewer stopped in a title. It names the site and
// the episode, never the media address, which expires: resuming asks the
// site again.
type historyEntry struct {
	Name       string    `json:"name"`
	Pic        string    `json:"pic,omitempty"`
	ConfigURL  string    `json:"config_url"`
	EntryKey   string    `json:"entry_key"`
	SiteKey    string    `json:"site_key"`
	Kind       string    `json:"kind,omitempty"`
	NASShare   string    `json:"nas_share,omitempty"` // share name, with the server address in Line
	Origin     string    `json:"origin,omitempty"`
	VideoID    string    `json:"video_id"`
	Line       string    `json:"line"`
	Episode    string    `json:"episode"`
	Position   float64   `json:"position"` // seconds
	Duration   float64   `json:"duration"`
	Updated    time.Time `json:"updated"`
	HomeHidden bool      `json:"home_hidden,omitempty"`
}

// historyKindNAS marks an entry recorded from a NAS file; historyKindSite
// marks one recorded from a library site; historyKindLocal marks one recorded
// from a local file on disk. Kind is the only type check: a site whose key
// happens to be "nas" or "local" must never be mistaken for one.
const (
	historyKindNAS   = "nas"
	historyKindSite  = "site"
	historyKindLocal = "local"
)

// isNAS reports whether the entry is a legacy NAS record from an older release.
// Such records are ignored on load so upgrading from v0.8.7 never errors or
// treats them as site entries.
func (e historyEntry) isNAS() bool {
	switch e.Kind {
	case historyKindNAS:
		return true
	case historyKindSite, historyKindLocal:
		return false
	default:
		return strings.HasPrefix(e.ConfigURL, "smb://")
	}
}

func (e historyEntry) isLocal() bool {
	return e.Kind == historyKindLocal
}

func localHistoryKey(path string) string {
	clean := filepath.Clean(path)
	if clean == "" || clean == "." {
		return ""
	}
	return "local:" + clean
}

func localHistoryOrigin(path string) string {
	dir := filepath.Base(filepath.Dir(filepath.Clean(path)))
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return "本地视频"
	}
	return "本地 · " + dir
}

// dedupKey identifies a site title by normalized name, or a local file by its
// cleaned path so distinct local files never collide with each other or with
// online titles.
func (e historyEntry) dedupKey() string {
	if e.isLocal() && e.VideoID != "" {
		return localHistoryKey(e.VideoID)
	}
	return titleKey(e.Name)
}

func (e historyEntry) finished() bool {
	return e.Duration > 0 && e.Position >= e.Duration-historyEndSlack
}

// progress is the row's second line: the episode and how far it was watched.
func (e historyEntry) progress() string {
	if e.isLocal() && e.Episode == "" {
		if e.finished() {
			return "已看完"
		}
		at := playerClock(time.Duration(e.Position * float64(time.Second)))
		if e.Duration <= 0 {
			return "看到 " + at
		}
		return fmt.Sprintf("看到 %s / %s", at, playerClock(time.Duration(e.Duration*float64(time.Second))))
	}
	episode := e.Episode
	if episode == "" {
		episode = "正片"
	}
	if e.finished() {
		return episode + " · 已看完"
	}
	at := playerClock(time.Duration(e.Position * float64(time.Second)))
	if e.Duration <= 0 {
		return episode + " · 看到 " + at
	}
	return fmt.Sprintf("%s · 看到 %s / %s", episode, at, playerClock(time.Duration(e.Duration*float64(time.Second))))
}

type historyListRow struct {
	heading string
	key     string
	label   string
	entry   int
}

// historyStore keeps the newest entry of each title first. An empty path
// keeps the history in memory only.
type historyStore struct {
	entries      []historyEntry
	path         string
	dirty        bool
	noted        time.Time
	saved        time.Time
	list         ui.ListState
	byTitle      map[string]int
	byTitleHead  *historyEntry
	byTitleLen   int
	rowsCache    []historyListRow
	rowsHead     *historyEntry
	rowsLen      int
	rowsDayStart time.Time
}

func (h *historyStore) findByTitle(name string) (historyEntry, bool) {
	key := titleKey(name)
	if key == "" || len(h.entries) == 0 {
		return historyEntry{}, false
	}
	head := &h.entries[0]
	if h.byTitle == nil || h.byTitleLen != len(h.entries) || h.byTitleHead != head {
		h.rebuildTitleIndex()
	}
	if idx, ok := h.byTitle[key]; ok && idx < len(h.entries) {
		e := h.entries[idx]
		if !e.isNAS() && !e.isLocal() && titleKey(e.Name) == key {
			return e, true
		}
		h.rebuildTitleIndex()
		if idx, ok := h.byTitle[key]; ok && idx < len(h.entries) {
			return h.entries[idx], true
		}
	}
	return historyEntry{}, false
}

func (h *historyStore) findLocalByPath(path string) (historyEntry, bool) {
	key := localHistoryKey(path)
	if key == "" {
		return historyEntry{}, false
	}
	for _, e := range h.entries {
		if e.isLocal() && e.dedupKey() == key {
			return e, true
		}
	}
	return historyEntry{}, false
}

func (h *historyStore) rebuildTitleIndex() {
	idx := make(map[string]int, len(h.entries))
	for i, e := range h.entries {
		if e.isNAS() || e.isLocal() {
			continue
		}
		k := titleKey(e.Name)
		if _, exists := idx[k]; !exists {
			idx[k] = i
		}
	}
	h.byTitle = idx
	h.byTitleLen = len(h.entries)
	if len(h.entries) > 0 {
		h.byTitleHead = &h.entries[0]
	} else {
		h.byTitleHead = nil
	}
}

func (h *historyStore) listRows(now time.Time) []historyListRow {
	localNow := now.Local()
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	var head *historyEntry
	if len(h.entries) > 0 {
		head = &h.entries[0]
	}
	if h.rowsHead == head && h.rowsLen == len(h.entries) && h.rowsDayStart.Equal(dayStart) && len(h.rowsCache) > 0 {
		return h.rowsCache
	}
	dayEnd := dayStart.AddDate(0, 0, 1)
	rows := make([]historyListRow, 0, len(h.entries)+2)
	heading := ""
	for i, e := range h.entries {
		group := "更早"
		if !e.Updated.Before(dayStart) && e.Updated.Before(dayEnd) {
			group = "今天"
		}
		if group != heading {
			heading = group
			rows = append(rows, historyListRow{heading: heading, key: "heading-" + heading, label: heading, entry: -1})
		}
		rows = append(rows, historyListRow{key: e.dedupKey(), label: e.Name, entry: i})
	}
	h.rowsCache = rows
	h.rowsHead = head
	h.rowsLen = len(h.entries)
	h.rowsDayStart = dayStart
	return rows
}

// note records an entry, replacing the one with the same dedup key.
func (h *historyStore) note(e historyEntry) {
	key := e.dedupKey()
	kept := h.entries[:0:0]
	for _, old := range h.entries {
		if old.dedupKey() != key {
			kept = append(kept, old)
		} else {
			e.HomeHidden = old.HomeHidden
		}
	}
	h.entries = append([]historyEntry{e}, kept...)
	if len(h.entries) > historyLimit {
		h.entries = h.entries[:historyLimit]
	}
	h.byTitle, h.rowsCache = nil, nil
	h.dirty = true
}

func (h *historyStore) remove(i int) (historyEntry, bool) {
	if i < 0 || i >= len(h.entries) {
		return historyEntry{}, false
	}
	removed := h.entries[i]
	h.entries = append(h.entries[:i:i], h.entries[i+1:]...)
	h.byTitle, h.rowsCache = nil, nil
	h.dirty = true
	return removed, true
}

func (h *historyStore) restore(i int, e historyEntry) {
	key := e.dedupKey()
	for _, existing := range h.entries {
		if existing.dedupKey() == key {
			return
		}
	}
	if i < 0 {
		i = 0
	}
	if i > len(h.entries) {
		i = len(h.entries)
	}
	out := make([]historyEntry, 0, len(h.entries)+1)
	out = append(out, h.entries[:i]...)
	out = append(out, e)
	out = append(out, h.entries[i:]...)
	h.entries = out
	h.byTitle, h.rowsCache = nil, nil
	h.dirty = true
}

func (h *historyStore) flush() error {
	if !h.dirty || h.path == "" {
		return nil
	}
	if err := writeJSONFile(h.path, h.entries); err != nil {
		return err
	}
	h.dirty = false
	return nil
}

func readHistory(path string) ([]historyEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []historyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("读取播放历史: %w", err)
	}
	kept := entries[:0]
	for _, entry := range entries {
		if entry.isNAS() {
			continue
		}
		kept = append(kept, entry)
	}
	return kept, nil
}

// restoreHistory reads the history kept beside the saved source.
func (a *app) restoreHistory() {
	sourcePath, err := savedSourcePath()
	if err != nil {
		return
	}
	a.history.path = filepath.Join(filepath.Dir(sourcePath), "history.json")
	entries, err := readHistory(a.history.path)
	if err != nil {
		a.status = err.Error()
		return
	}
	a.history.entries = entries
}

func (a *app) flushHistory() {
	if err := a.history.flush(); err != nil && !a.closed {
		a.status = "保存播放历史失败：" + err.Error()
	}
	a.history.saved = a.history.noted
}

func (a *app) localResumePosition(path string) time.Duration {
	e, ok := a.history.findLocalByPath(path)
	if !ok || e.finished() || e.Position <= 0 {
		return 0
	}
	return time.Duration(e.Position * float64(time.Second))
}

// recordHistory notes once a second where the episode or local file on show
// is, and writes it to disk now and then; leaving the player writes it too.
func (a *app) recordHistory(p mediaPlayer, now time.Time) {
	if now.Sub(a.history.noted) < time.Second {
		return
	}
	position, duration := p.Position().Seconds(), p.Duration().Seconds()
	if p.Ended() && duration > 0 {
		position = duration
	}
	switch {
	case a.hasEpisodes() && a.playingEpisode != "":
		t := a.title
		a.history.noted = now
		a.history.note(historyEntry{
			Name:      t.name,
			Pic:       t.detail.Pic,
			ConfigURL: configURL(t.item.Entry),
			EntryKey:  entryKey(t.item.Entry),
			SiteKey:   t.item.Entry.Site.Key,
			Kind:      historyKindSite,
			Origin:    t.item.Entry.Label(),
			VideoID:   string(t.item.Video.ID),
			Line:      a.playingSource,
			Episode:   a.playingEpisode,
			Position:  position,
			Duration:  duration,
			Updated:   now,
		})
	case a.playingFile && a.playingFilePath != "":
		a.history.noted = now
		a.history.note(historyEntry{
			Name:     a.playingName,
			Kind:     historyKindLocal,
			Origin:   localHistoryOrigin(a.playingFilePath),
			VideoID:  a.playingFilePath,
			Position: position,
			Duration: duration,
			Updated:  now,
		})
	default:
		return
	}
	if now.Sub(a.history.saved) >= historySaveGap {
		a.flushHistory()
	}
}

func (a *app) recordLocalCompletion(p mediaPlayer) {
	if !a.playingFile || a.playingFilePath == "" || p == nil || p.Duration() <= 0 {
		return
	}
	now := time.Now()
	dur := p.Duration().Seconds()
	a.history.note(historyEntry{
		Name:     a.playingName,
		Kind:     historyKindLocal,
		Origin:   localHistoryOrigin(a.playingFilePath),
		VideoID:  a.playingFilePath,
		Position: dur,
		Duration: dur,
		Updated:  now,
	})
	a.history.noted = now
	a.flushHistory()
}

func configURL(entry library.Entry) string {
	if entry.Config == nil {
		return ""
	}
	return entry.Config.URL
}

func entryKey(entry library.Entry) string {
	if entry.Config == nil {
		return ""
	}
	return entry.Key()
}

// historyAvailable reports whether the entry's source or local file is still available.
func (a *app) historyAvailable(e historyEntry) bool {
	if e.isNAS() {
		return false
	}
	if e.isLocal() {
		if e.VideoID == "" {
			return false
		}
		info, err := os.Stat(e.VideoID)
		return err == nil && !info.IsDir()
	}
	return a.configAvailable(e.ConfigURL)
}

func (a *app) configAvailable(raw string) bool {
	root := a.library.root
	if root == nil || raw == "" {
		return false
	}
	if root.URL == raw || a.library.cached[raw] != nil {
		return true
	}
	for _, line := range root.Lines {
		if line.URL == raw {
			return true
		}
	}
	return false
}

// findEntry locates the entry's site in its configuration: the same site as
// recorded, or after a refresh changed its address, the one with its key.
func findEntry(doc *config.Config, e historyEntry) (library.Entry, bool) {
	return findSite(doc, e.EntryKey, e.SiteKey)
}

func findSite(doc *config.Config, entryKey, siteKey string) (library.Entry, bool) {
	var byKey *library.Entry
	for _, site := range doc.Sites {
		entry := library.Entry{Site: site, Config: doc, Line: doc.Line}
		if entry.Key() == entryKey {
			return entry, true
		}
		if byKey == nil && site.Key == siteKey {
			byKey = &entry
		}
	}
	if byKey != nil {
		return *byKey, true
	}
	return library.Entry{}, false
}

// resumePoint is where to continue: the recorded episode at its position,
// or the one after it when that episode was watched to the end.
func resumePoint(detail catalog.Detail, e historyEntry) (line int, episode catalog.Episode, at time.Duration, ok bool) {
	return followingResumePoint(detail, e, true)
}

// resume plays an entry from where it stopped, straight into the player.
func (a *app) resume(e historyEntry) {
	if e.isNAS() {
		return
	}
	if e.isLocal() {
		if !a.historyAvailable(e) {
			a.status = "本地文件不存在或已移动"
			return
		}
		if e.finished() {
			if files, idx := discoverSiblingVideos(e.VideoID); idx >= 0 && idx+1 < len(files) {
				a.openLocalFileAt(files[idx+1], nil, 0, false)
				return
			}
			a.openLocalFileAt(e.VideoID, nil, 0, false)
			return
		}
		at := time.Duration(e.Position * float64(time.Second))
		a.openLocalFileAt(e.VideoID, nil, at, false)
		return
	}
	preferred := a.sourceMemory.entries[titleKey(e.Name)].Preferred
	originalAvailable := a.historyAvailable(e)
	preferSaved := preferred.VideoID != "" && a.configAvailable(preferred.ConfigURL)
	if preferred.ConfigURL == e.ConfigURL && preferred.VideoID == e.VideoID && preferred.Line == e.Line && (preferred.EntryKey == e.EntryKey || preferred.SiteKey == e.SiteKey) {
		preferSaved = false
	}
	if !originalAvailable && (!preferSaved || e.finished()) {
		a.status = "这条记录的片源已移除，重新添加后可继续播放"
		return
	}
	cached := a.library.cached[e.ConfigURL]
	preferredDoc := a.library.cached[preferred.ConfigURL]
	if root := a.library.root; root != nil {
		if root.URL == e.ConfigURL {
			cached = root
		}
		if root.URL == preferred.ConfigURL {
			preferredDoc = root
		}
	}
	assets := strings.TrimSpace(a.browser.assets)
	sniff := a.sniffPage
	a.cancelJob(jobTitle)
	a.run(jobPlay, "继续播放", func(ctx context.Context) (jobResult, error) {
		// Match the saved episode by name or number, never by another site's
		// list position. A missing episode or failed resolution falls back.
		tryPreferred := func(name string, at time.Duration) (jobResult, bool) {
			if !preferSaved || name == "" {
				return jobResult{}, false
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			doc := preferredDoc
			if doc == nil {
				var err error
				doc, err = config.Read(ctx, preferred.ConfigURL, nil)
				if err != nil {
					return jobResult{}, false
				}
			}
			entry, found := findSite(doc, preferred.EntryKey, preferred.SiteKey)
			if !found {
				return jobResult{}, false
			}
			item := library.Item{Entry: entry, Video: catalog.Video{ID: catalog.ID(preferred.VideoID), Name: e.Name, Pic: e.Pic}}
			session, detail, err := openItem(ctx, item, assets)
			if err != nil {
				return jobResult{}, false
			}
			if titleKey(detail.Name) != titleKey(e.Name) {
				session.Close()
				return jobResult{}, false
			}
			line, episode, ok := rememberedEpisode(detail, preferred.Line, name)
			if !ok {
				session.Close()
				return jobResult{}, false
			}
			play, err := resolvePlay(ctx, session, resolve.Resolver{Sniff: sniff, Parsers: doc.Parsers}, detail.Sources[line].Name, episode)
			if err != nil {
				session.Close()
				return jobResult{}, false
			}
			return jobResult{discard: session.Close, apply: func() {
				a.clearTitle()
				a.title = &titlePage{name: e.Name, pic: e.Pic, seeds: []library.Item{item}, failed: map[string]string{},
					item: item, session: session, detail: &detail, line: line}
				a.startEpisode(play, detail.Sources[line].Name, episode, at)
				if a.player == nil && a.route() != routeDetail {
					a.clearTitle()
				}
			}}, true
		}
		if !e.finished() {
			if result, ok := tryPreferred(e.Episode, time.Duration(e.Position*float64(time.Second))); ok {
				return result, nil
			}
		}
		if !originalAvailable {
			return jobResult{}, fmt.Errorf("上次成功的来源没有对应分集，原播放记录的片源已移除")
		}
		doc := cached
		if doc == nil {
			var err error
			if doc, err = config.Read(ctx, e.ConfigURL, nil); err != nil {
				return jobResult{}, err
			}
		}
		entry, ok := findEntry(doc, e)
		if !ok {
			return jobResult{}, fmt.Errorf("片源里已经没有这个站点")
		}
		item := library.Item{Entry: entry, Video: catalog.Video{ID: catalog.ID(e.VideoID), Name: e.Name, Pic: e.Pic}}
		session, detail, err := openItem(ctx, item, assets)
		if err != nil {
			return jobResult{}, err
		}
		line, episode, at, ok := resumePoint(detail, e)
		if !ok {
			session.Close()
			return jobResult{}, fmt.Errorf("找不到原分集或暂时没有下一集，请打开详情选集")
		}
		if e.finished() {
			if result, ok := tryPreferred(episode.Name, at); ok {
				session.Close()
				return result, nil
			}
		}
		play, err := resolvePlay(ctx, session, resolve.Resolver{Sniff: sniff, Parsers: doc.Parsers}, detail.Sources[line].Name, episode)
		if err != nil {
			session.Close()
			return jobResult{}, err
		}
		return jobResult{discard: session.Close, apply: func() {
			a.clearTitle()
			a.title = &titlePage{name: e.Name, pic: e.Pic, seeds: []library.Item{item}, failed: map[string]string{},
				item: item, session: session, detail: &detail, line: line}
			a.startEpisode(play, detail.Sources[line].Name, episode, at)
			if a.player == nil && a.route() != routeDetail {
				a.clearTitle()
			}
		}}, nil
	}, nil)
}

func (a *app) historyView(c *ui.Context) {
	h := &a.history
	p := paletteFor(c)
	if len(h.entries) == 0 {
		emptyState(c, "还没有播放记录", "看过的影片会出现在这里，点一下从上次的进度继续。")
		return
	}
	ui.Column(c).Fill().Gap(14).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "只保存在本机").FontSize(13).TextColor(p.textMuted).Grow(1)
			if a.armedDangerButton(c, "clear-history", "清空历史", "确认清空？") {
				h.entries, h.dirty = nil, true
				h.byTitle, h.rowsCache, h.list = nil, nil, ui.ListState{}
				a.flushHistory()
				a.setNotice("已清空播放历史")
			}
		})
		rows := h.listRows(c.Now())
		h.list.Key = func(row int) any { return rows[row].key }
		h.list.Label = func(row int) string { return rows[row].label }
		ui.List(c, &h.list, len(rows), func(row int) {
			r := rows[row]
			if r.entry < 0 {
				ui.Text(c, r.heading).Key(r.key).FontSize(16).Bold().Padding(6, 0, 0, 0)
				return
			}
			if r.entry < len(h.entries) {
				a.historyRow(c, r.entry, h.entries[r.entry])
			}
		}).Grow(1).FillWidth().MinHeight(0).Gap(8)
	})
}

func (a *app) historyRow(c *ui.Context, i int, e historyEntry) {
	p := paletteFor(c)
	available := a.historyAvailable(e)
	row := ui.Row(c).Key(e.dedupKey()).FillWidth().Padding(10, 14, 10, 10).Gap(14).
		AlignItems(ui.Center)
	applySurface(row, p, 18).Children(func() {
		mainArea := ui.Row(c).Grow(1).MinWidth(0).Gap(14).AlignItems(ui.Center)
		if available {
			mainArea.Cursor(ui.CursorPointer)
		}
		mainArea.Children(func() {
			posterBox := ui.Box(c).Size(60, 90).Radius(10).Clip().Background(p.cardPlaceholder)
			posterBox.Children(func() {
				applyPosterImage(c, posterBox, a.poster(e.Pic))
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
				ui.Text(c, e.Name).Bold().SingleLine()
				ui.Text(c, e.progress()).FontSize(13).TextColor(p.textMuted).SingleLine()
				if e.Duration > 0 {
					ui.Progress(c, min(e.Position/e.Duration, 1)).FillWidth().MaxWidth(360).Label("观看进度")
				}
				origin := e.Origin
				if !available {
					if e.isLocal() {
						origin = "文件已移除"
					} else {
						origin = "片源已移除"
					}
				}
				ui.Text(c, origin).FontSize(12).TextColor(p.textMuted).SingleLine()
			})
		})
		if available && mainArea.Clicked() {
			a.resume(e)
		}
		if primaryButton(c, "继续播放").Label("继续播放 " + e.Name).Disabled(!available).Clicked() {
			a.resume(e)
		}
		if !available && !e.isLocal() && a.library.root != nil {
			if glassButton(c, "搜片库").Label("搜片库 " + e.Name).Clicked() {
				a.openTitle(e.Name, e.Pic, nil)
			}
		}
		if glassButton(c, "移除").Label("移除 " + e.Name).Clicked() {
			idx := i
			if removed, ok := a.history.remove(idx); ok {
				a.flushHistory()
				a.setUndoNotice("已移除「"+removed.Name+"」", "撤销", func() {
					a.history.restore(idx, removed)
					a.flushHistory()
				})
			}
		}
	})
}
