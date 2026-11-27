package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/arixbit/xiaomu-desktop/internal/httpx"
	"github.com/arixbit/xiaomu-desktop/internal/localart"
	"github.com/arixbit/xiaomu-desktop/internal/tmdb"
	"github.com/egoist/mygo/ui"
)

type localPosterSearcher interface {
	Search(context.Context, string, string, tmdb.Kind) ([]tmdb.Match, error)
}

const posterWorkerURL = "https://xiaomu-poster.asynbit.workers.dev"

type localPosterState struct {
	settings localPosterSettingsState

	workerURL       string
	newSearcher     func(string) localPosterSearcher
	autoPath        string
	pending         string // artwork found before the first history update for autoPath
	pendingSeriesID int
	images          map[string]localPosterImage
	imageUse        uint64

	open    bool
	path    string
	query   string
	err     string
	matches []tmdb.Match
}

type localPosterImage struct {
	src  ui.ImageSource
	used uint64
}

func (a *app) localPosterCacheDir() string {
	if a.history.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(a.history.path), "cache", "local-posters")
}

func (a *app) localPosterOnlineAvailable() bool {
	return a.localPoster.workerURL != "" || a.preferences.TMDBToken != ""
}

func (s *localPosterState) searcher(token string) (localPosterSearcher, error) {
	if s.newSearcher != nil {
		return s.newSearcher(token), nil
	}
	if s.workerURL != "" {
		return tmdb.NewWorker(s.workerURL)
	}
	return tmdb.New(token), nil
}

var (
	localPosterSeason      = regexp.MustCompile(`\s*第\s*[0-9]+\s*季\s*$`)
	localPosterBareEpisode = regexp.MustCompile(`^(.+)\s+[0-9]{1,3}$`)
)

func localPosterFileQuery(path string) (title, year string, kind tmdb.Kind) {
	file := filepath.Base(path)
	title, year, _ = parseLocalDanmakuMeta(file)
	stem := strings.TrimSuffix(file, filepath.Ext(file))
	stem = strings.TrimSpace(localLeadingBracketTag.ReplaceAllString(stem, ""))
	if danmakuNamedEpisode.MatchString(stem) || danmakuTaggedEpisode.MatchString(stem) {
		kind = tmdb.TV
		if loc := localTitleCutoff.FindStringIndex(stem); loc != nil && loc[0] == 0 {
			return "", "", kind
		}
	}
	normalized := danmaku.NormalizeSeasonTitle(title)
	if loc := localPosterSeason.FindStringIndex(normalized); loc != nil {
		title, kind = normalized[:loc[0]], tmdb.TV
	}
	// A matching series folder distinguishes a bare episode suffix from
	// movies whose titles end in a number, such as "Rocky 2".
	if m := localPosterBareEpisode.FindStringSubmatch(title); kind == "" && len(m) == 2 {
		folder := filepath.Base(filepath.Dir(path))
		folder = strings.Join(strings.Fields(strings.NewReplacer(".", " ", "_", " ").Replace(folder)), " ")
		if titleKey(m[1]) == titleKey(folder) {
			title, kind = m[1], tmdb.TV
		}
	}
	title = strings.TrimSpace(strings.Trim(strings.TrimSpace(title), "《》"))
	if title == year { // A title such as "1917" needs a title search, not a year filter.
		year = ""
	}
	return title, year, kind
}

// A same-name result without artwork still counts as a competing identity.
func exactLocalPoster(title, year string, matches []tmdb.Match) (tmdb.Match, bool) {
	key := titleKey(title)
	var chosen tmdb.Match
	for _, m := range matches {
		if year != "" && m.Year != year {
			continue
		}
		if titleKey(m.Title) != key && titleKey(m.OriginalTitle) != key {
			continue
		}
		if chosen.ID != 0 {
			return tmdb.Match{}, false
		}
		chosen = m
	}
	return chosen, chosen.ID != 0 && chosen.PosterURL != ""
}

func cachedLocalPoster(dir, name string) (ui.ImageSource, error) {
	data, err := localart.ReadCached(dir, name)
	if err != nil {
		return nil, err
	}
	img, err := ui.DecodeBitmapScaled(data, posterMaxSide)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func saveLocalPoster(ctx context.Context, dir string, data []byte) (string, ui.ImageSource, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	img, err := ui.DecodeBitmapScaled(data, posterMaxSide)
	if err != nil {
		return "", nil, err
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	name, err := localart.Save(dir, data)
	return name, img, err
}

func downloadLocalPoster(ctx context.Context, dir, url string) (string, ui.ImageSource, error) {
	// Shared poster transport applies byte/pixel budgets. This new request
	// carries no TMDB API credential.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, err
	}
	resp, err := posterNetClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer httpx.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("封面下载 HTTP %d", resp.StatusCode)
	}
	data, exceeded, err := httpx.ReadLimit(resp.Body, resp.ContentLength, maxPosterBytes)
	if err != nil {
		return "", nil, err
	}
	if exceeded {
		return "", nil, fmt.Errorf("封面超过大小限制")
	}
	return saveLocalPoster(ctx, dir, data)
}

func (a *app) startLocalPoster(path string) {
	s := &a.localPoster
	s.autoPath, s.pending, s.pendingSeriesID = path, "", 0
	dir := a.localPosterCacheDir()
	if dir == "" {
		return
	}
	entry, _ := a.history.findLocalByPath(path)
	title, year, kind := localPosterFileQuery(path)
	var series historyEntry
	if kind == tmdb.TV {
		series, _ = a.history.findLocalSeriesPoster(path, title, year)
	}
	var client localPosterSearcher
	var clientErr error
	if a.localPosterOnlineAvailable() && !a.preferences.DisableAutoLocalPoster {
		client, clientErr = s.searcher(a.preferences.TMDBToken)
	}
	a.run(jobLocalPoster, "", func(ctx context.Context) (jobResult, error) {
		result := func(name string, img ui.ImageSource, manual bool, seriesID int) jobResult {
			return jobResult{apply: func() {
				a.applyLocalPoster(path, name, img, manual, seriesID)
			}}
		}

		if !entry.PosterManual {
			if data, err := localart.Sidecar(path); err == nil {
				if name, img, err := saveLocalPoster(ctx, dir, data); err == nil {
					return result(name, img, false, 0), nil
				}
			}
		}
		if entry.LocalPoster != "" {
			if img, err := cachedLocalPoster(dir, entry.LocalPoster); err == nil {
				return result(entry.LocalPoster, img, entry.PosterManual, entry.PosterSeriesID), nil
			}
		}
		if entry.PosterManual {
			return jobResult{}, nil
		}
		if series.LocalPoster != "" {
			if img, err := cachedLocalPoster(dir, series.LocalPoster); err == nil {
				return result(series.LocalPoster, img, false, series.PosterSeriesID), nil
			}
		}
		if title == "" || client == nil {
			return jobResult{}, clientErr
		}
		matches, err := client.Search(ctx, title, year, kind)
		if err != nil {
			return jobResult{}, err
		}
		match, ok := exactLocalPoster(title, year, matches)
		if !ok {
			return jobResult{}, nil
		}
		name, img, err := downloadLocalPoster(ctx, dir, match.PosterURL)
		if err != nil {
			return jobResult{}, err
		}
		return result(name, img, false, localPosterSeriesID(path, match)), nil
	}, func(error) {})
}

func (a *app) applyLocalPoster(path, name string, img ui.ImageSource, manual bool, seriesID int) bool {
	key := localHistoryKey(path)
	for i := range a.history.entries {
		e := &a.history.entries[i]
		if !e.isLocal() || e.dedupKey() != key {
			continue
		}
		a.localPoster.rememberImage(name, img)
		if e.LocalPoster != name || e.PosterManual != manual || e.PosterSeriesID != seriesID {
			e.LocalPoster, e.PosterManual, e.PosterSeriesID = name, manual, seriesID
			a.history.dirty = true
			a.flushHistory()
		}
		return true
	}
	if !manual && a.playingFile && a.playingFilePath == path {
		a.localPoster.rememberImage(name, img)
		a.localPoster.pending, a.localPoster.pendingSeriesID = name, seriesID
		return true
	}
	return false
}

func (a *app) takePendingLocalPoster() (string, int) {
	s := &a.localPoster
	if s.autoPath != a.playingFilePath {
		return "", 0
	}
	name, seriesID := s.pending, s.pendingSeriesID
	s.pending, s.pendingSeriesID = "", 0
	return name, seriesID
}

// Keep only visible/recently used decoded artwork in memory.
func (s *localPosterState) rememberImage(name string, src ui.ImageSource) {
	const maxImages = 28
	if s.images == nil {
		s.images = make(map[string]localPosterImage)
	}
	if _, exists := s.images[name]; !exists && len(s.images) >= maxImages {
		var oldest string
		var used uint64
		for k, img := range s.images {
			if oldest == "" || img.used < used {
				oldest, used = k, img.used
			}
		}
		delete(s.images, oldest)
	}
	s.imageUse++
	s.images[name] = localPosterImage{src: src, used: s.imageUse}
}

func (a *app) historyPoster(c *ui.Context, e historyEntry) ui.ImageSource {
	if !e.isLocal() {
		return a.poster(c, e.Pic)
	}
	name, dir := e.LocalPoster, a.localPosterCacheDir()
	if a.closed || name == "" || dir == "" {
		return nil
	}
	s := &a.localPoster
	if img, ok := s.images[name]; ok {
		s.rememberImage(name, img.src)
		return img.src
	}
	if !a.running(jobLocalPosterRead) {
		a.run(jobLocalPosterRead, "", func(context.Context) (jobResult, error) {
			img, _ := cachedLocalPoster(dir, name)
			return jobResult{apply: func() { s.rememberImage(name, img) }}, nil
		}, func(error) { s.rememberImage(name, nil) })
	}
	return s.images[name].src
}

func (a *app) openLocalPosterDialog(e historyEntry) {
	a.closeLocalPosterDialog()
	title, year, _ := localPosterFileQuery(e.VideoID)
	if title != "" && year != "" {
		title += " (" + year + ")"
	}
	s := &a.localPoster
	s.open, s.path, s.query = true, e.VideoID, title
	if img, loaded := s.images[e.LocalPoster]; e.PosterManual && loaded && img.src == nil {
		s.err = "已选封面的本地缓存不可用，请重新匹配。"
	}
}

func (a *app) closeLocalPosterDialog() {
	s := &a.localPoster
	if s.path != "" {
		a.cancelJob(jobLocalPosterSearch)
	}
	s.open, s.path, s.query, s.err, s.matches = false, "", "", "", nil
}

func (a *app) searchLocalPoster() {
	s := &a.localPoster
	token := a.preferences.TMDBToken
	if !s.open || !a.localPosterOnlineAvailable() {
		return
	}
	query, year := strings.TrimSpace(s.query), ""
	if suffix := danmakuYearSuffix.FindStringSubmatchIndex(query); len(suffix) == 4 {
		query, year = strings.TrimSpace(query[:suffix[0]]), query[suffix[2]:suffix[3]]
	}
	if query == "" {
		s.err = "请输入片名"
		return
	}
	s.matches, s.err = nil, ""
	client, err := s.searcher(token)
	if err != nil {
		s.err = err.Error()
		return
	}
	a.run(jobLocalPosterSearch, "", func(ctx context.Context) (jobResult, error) {
		matches, err := client.Search(ctx, query, year, "")
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			for _, m := range matches {
				if m.PosterURL != "" {
					s.matches = append(s.matches, m)
				}
			}
			if len(s.matches) == 0 {
				s.err = "没有找到带海报的作品，试试原名或调整年份"
			}
		}}, nil
	}, func(err error) { s.err = err.Error() })
}

func (a *app) selectLocalPoster(m tmdb.Match) {
	s := &a.localPoster
	path, dir := s.path, a.localPosterCacheDir()
	if !s.open || a.running(jobLocalPosterSearch) {
		return
	}
	if dir == "" {
		s.err = "本地封面缓存不可用"
		return
	}
	if s.autoPath == path {
		a.cancelJob(jobLocalPoster)
	}
	s.err = ""
	a.run(jobLocalPosterSearch, "", func(ctx context.Context) (jobResult, error) {
		name, img, err := downloadLocalPoster(ctx, dir, m.PosterURL)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if !a.applyLocalPoster(path, name, img, true, localPosterSeriesID(path, m)) {
				s.err = "这条播放记录已移除"
				return
			}
			a.closeLocalPosterDialog()
		}}, nil
	}, func(error) { s.err = "下载封面失败，请检查网络后重试" })
}

func (a *app) localPosterDialog(c *ui.Context) {
	s := &a.localPoster
	p := paletteFor(c)
	_, height := c.Size()
	keyedDialog(c, "dlg-local-poster", &s.open, func(backdrop, panel ui.Element) {
		backdrop.Background(p.dialogScrim).Transition(transDialogScrim)
		panel.Width(480).MaxHeight(max(float32(240), height-36)).Padding(20).Gap(12).Radius(22).TextColor(p.text)
		applyPane(panel, p, p.dialogMat)
		applyDialogPanelPose(c, panel)
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "匹配封面").FontSize(21).Bold().Grow(1)
			if glassButton(c, "关闭").Clicked() {
				a.closeLocalPosterDialog()
			}
		})
		ui.Text(c, filepath.Base(s.path)).SingleLine().FontSize(12).TextColor(p.textMuted)
		if !a.localPosterOnlineAvailable() {
			ui.Text(c, "在线匹配需要 TMDB Read Access Token。").TextColor(p.textMuted)
			if primaryButton(c, "封面设置").Clicked() {
				a.openLocalPosterSettingsFor(s.path)
				a.closeLocalPosterDialog()
			}
			return
		}
		busy := a.running(jobLocalPosterSearch)
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			input := ui.TextInput(c.Key("poster-query"), &s.query).Label("片名").Placeholder("片名或原名（年份可选）").Grow(1).MinWidth(0).AutoFocus()
			if input.Submitted() && !busy {
				a.searchLocalPoster()
			}
			if primaryButton(c, "搜索").Disabled(busy).Clicked() {
				a.searchLocalPoster()
			}
		})
		if busy {
			ui.Text(c, "正在处理封面…").TextColor(p.textMuted)
		}
		if s.err != "" {
			ui.Text(c, s.err).TextColor(p.warning)
		}
		if len(s.matches) != 0 {
			ui.Scroll(c).FillWidth().MaxHeight(max(float32(80), height-240)).Gap(4).ScrollbarStyle(appScrollbar).Children(func() {
				for _, m := range s.matches {
					a.localPosterMatchRow(c, m, busy)
				}
			})
		}
	})
	if !s.open && s.path != "" {
		a.closeLocalPosterDialog()
	}
}

func (a *app) localPosterMatchRow(c *ui.Context, m tmdb.Match, busy bool) {
	p := paletteFor(c)
	kind := "电影"
	if m.Kind == tmdb.TV {
		kind = "剧集"
	}
	key := fmt.Sprintf("poster-%s-%d", m.Kind, m.ID)
	label := strings.TrimSpace(strings.Join([]string{m.Title, m.Year, kind}, " "))
	row := ui.ButtonBase(c.Key(key)).FillWidth().MinHeight(60).Padding(5, 8).Gap(10).Radius(8).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label).Disabled(busy)
	applyInteractiveSurface(row, p, 8, row.Hovered())
	row.Children(func() {
		box := ui.Box(c).Size(32, 48).Radius(4).Clip().Background(p.cardPlaceholder).PassThrough()
		box.Children(func() { applyPosterImage(c, box, a.poster(c, m.PosterURL)) })
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).PassThrough().Children(func() {
			ui.Text(c, m.Title).SingleLine().Bold().TextColor(p.text).PassThrough()
			details := kind
			if m.Year != "" {
				details = m.Year + " · " + details
			}
			if m.OriginalTitle != "" && m.OriginalTitle != m.Title {
				details += " · " + m.OriginalTitle
			}
			ui.Text(c, details).SingleLine().FontSize(11.5).TextColor(p.textMuted).PassThrough()
		})
	})
	if row.Clicked() {
		a.selectLocalPoster(m)
	}
}
