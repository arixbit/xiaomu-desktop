package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/nas"
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
// marks one recorded from a library site. Kind is the only type check:
// a site whose key happens to be "nas" must never be mistaken for one.
const (
	historyKindNAS  = "nas"
	historyKindSite = "site"
)

// isNAS reports whether the entry is a NAS file. Entries written before
// Kind existed are recognised by their smb:// address, so an old site
// record is never misread either.
func (e historyEntry) isNAS() bool {
	switch e.Kind {
	case historyKindNAS:
		return true
	case historyKindSite:
		return false
	default:
		return strings.HasPrefix(e.ConfigURL, "smb://")
	}
}

// nasLocation reads the saved server and share. Older records stored a
// literal smb:// address with no URL encoding, so preserve the original
// share characters when recovering their identity.
func (e historyEntry) nasLocation() (server, share string) {
	if e.NASShare != "" {
		return e.Line, e.NASShare
	}
	addr, ok := strings.CutPrefix(e.ConfigURL, "smb://")
	if !ok {
		return "", ""
	}
	server, share, _ = strings.Cut(addr, "/")
	return server, share
}

func matchesNASHistory(e historyEntry, srv nas.Server) bool {
	server, share := e.nasLocation()
	return e.isNAS() && server != "" && share != "" && server == srv.Address && share == srv.Share
}

func nasHistoryKey(server, share, path string) string {
	return "nas\x00" + server + "\x00" + share + "\x00" + path
}

// dedupKey identifies a site title by name and a NAS file by its server,
// share and relative path.
func (e historyEntry) dedupKey() string {
	if e.isNAS() {
		server, share := e.nasLocation()
		return nasHistoryKey(server, share, e.VideoID)
	}
	return titleKey(e.Name)
}

func (e historyEntry) finished() bool {
	return e.Duration > 0 && e.Position >= e.Duration-historyEndSlack
}

// progress is the row's second line: the episode and how far it was watched.
func (e historyEntry) progress() string {
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

// historyStore keeps the newest entry of each title first. An empty path
// keeps the history in memory only.
type historyStore struct {
	entries []historyEntry
	path    string
	dirty   bool
	noted   time.Time
	saved   time.Time
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
	h.dirty = true
}

func (h *historyStore) remove(i int) {
	if i < 0 || i >= len(h.entries) {
		return
	}
	h.entries = append(h.entries[:i:i], h.entries[i+1:]...)
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
	return entries, nil
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

// recordHistory notes once a second where the episode on show is, and writes
// it to disk now and then; leaving the player writes it too. A NAS file is
// recorded the same way, with the server and share as its marker instead
// of a library site.
func (a *app) recordHistory(p *ui.VideoPlayer, now time.Time) {
	if a.playingNAS != nil {
		if now.Sub(a.history.noted) < time.Second {
			return
		}
		position, duration := p.Position().Seconds(), p.Duration().Seconds()
		if p.Ended() && duration > 0 {
			position = duration
		}
		a.history.noted = now
		np := a.playingNAS
		shareURL := url.URL{Scheme: "smb", Host: np.Server, Path: "/" + np.Share}
		a.history.note(historyEntry{
			Name:      np.Name,
			ConfigURL: shareURL.String(),
			SiteKey:   historyKindNAS,
			Kind:      historyKindNAS,
			NASShare:  np.Share,
			Origin:    "内网盘",
			VideoID:   np.Path,
			Line:      np.Server,
			Episode:   "正片",
			Position:  position,
			Duration:  duration,
			Updated:   now,
		})
		if now.Sub(a.history.saved) >= historySaveGap {
			a.flushHistory()
		}
		return
	}
	t := a.title
	if !a.hasEpisodes() || a.playingEpisode == "" || now.Sub(a.history.noted) < time.Second {
		return
	}
	position, duration := p.Position().Seconds(), p.Duration().Seconds()
	if p.Ended() && duration > 0 {
		position = duration
	}
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
	if now.Sub(a.history.saved) >= historySaveGap {
		a.flushHistory()
	}
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

// historyAvailable reports whether the entry's source is still imported.
// A NAS entry is available while the same server and share are saved.
func (a *app) historyAvailable(e historyEntry) bool {
	if e.isNAS() {
		return a.nasMatches(e)
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
// A NAS entry never goes through the library: it reopens its own file.
func (a *app) resume(e historyEntry) {
	if e.isNAS() {
		a.resumeNAS(e)
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
	if len(h.entries) == 0 {
		emptyState(c, "还没有播放记录", "看过的影片会出现在这里，点一下从上次的进度继续。")
		return
	}
	ui.Column(c).Fill().Gap(14).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "只保存在本机").FontSize(13).TextColor(textMuted).Grow(1)
			if glassButton(c, "清空历史").Clicked() {
				h.entries, h.dirty = nil, true
				a.flushHistory()
			}
		})
		today := c.Now().Local().Format("2006-01-02")
		ui.Scroll(c).Grow(1).FillWidth().Gap(8).Children(func() {
			heading := ""
			for i, e := range h.entries {
				group := "更早"
				if e.Updated.Local().Format("2006-01-02") == today {
					group = "今天"
				}
				if group != heading {
					heading = group
					ui.Text(c, heading).Key("heading-"+heading).FontSize(16).Bold().Padding(6, 0, 0, 0)
				}
				a.historyRow(c, i, e)
			}
		})
	})
}

func (a *app) historyRow(c *ui.Context, i int, e historyEntry) {
	available := a.historyAvailable(e)
	ui.Row(c).Key(e.dedupKey()).FillWidth().Padding(10, 14, 10, 10).Gap(14).Radius(18).
		AlignItems(ui.Center).Background(surface).Children(func() {
		ui.Box(c).Size(60, 90).Radius(10).Clip().Background(ui.Hex("#24242c")).Children(func() {
			if poster := a.poster(e.Pic); poster != nil {
				ui.Image(c, poster).Fill().Fit(ui.Cover)
			}
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
			ui.Text(c, e.Name).Bold().SingleLine()
			ui.Text(c, e.progress()).FontSize(13).TextColor(textMuted).SingleLine()
			if e.Duration > 0 {
				ui.Progress(c, min(e.Position/e.Duration, 1)).FillWidth().MaxWidth(360).Label("观看进度")
			}
			origin := e.Origin
			if !available {
				origin = "片源已移除"
				if e.isNAS() {
					origin = "内网盘已移除或更换"
				}
			}
			ui.Text(c, origin).FontSize(12).TextColor(textMuted).SingleLine()
		})
		if primaryButton(c, "继续播放").Label("继续播放 " + e.Name).Disabled(!available).Clicked() {
			a.resume(e)
		}
		if glassButton(c, "移除").Label("移除 " + e.Name).Clicked() {
			a.history.remove(i)
			a.flushHistory()
		}
	})
}
