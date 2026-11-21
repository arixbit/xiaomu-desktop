package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
	"github.com/arixbit/xiaomu-desktop/internal/source"
	"github.com/egoist/mygo/ui"
)

// episodeChunk is how many episodes show at once; longer series page.
const episodeChunk = 50

// titlePage is one title and the sites that have it. Its session belongs to
// the source on show, and serves the player while an episode plays.
type titlePage struct {
	name, pic      string
	score          string            // Douban score, when the opener knew one
	seeds          []library.Item    // sources known when the page opened
	failed         map[string]string // source → why it could not be read
	auto           bool              // open the first source that reads
	searchFallback bool              // a saved source may have disappeared
	pending        library.Item      // the source being read
	item           library.Item      // the source on show
	session        source.Session
	detail         *catalog.Detail
	line           int // chosen play line
	chunk          int // chosen range of episodes
}

// openTitle shows a title. Without seeds, its sources are searched for in
// the library, and the first one that reads opens by itself.
func (a *app) openTitle(name, pic string, seeds []library.Item) {
	a.showTitle(name, pic, seeds, false, "")
}

// openTitleWithScore is openTitle for a caller that already knows the
// title's Douban score, such as a discovery card. The score is shown
// as-is; it is never looked up, guessed or invented here.
func (a *app) openTitleWithScore(name, pic string, seeds []library.Item, score string) {
	a.showTitle(name, pic, seeds, false, score)
}

func (a *app) showTitle(name, pic string, seeds []library.Item, searchFallback bool, score string) {
	// Keep this title's sources independent of subsequent scan refreshes.
	seeds = append([]library.Item(nil), seeds...)
	a.clearTitle()
	if score == "" {
		// A title opened from search or the library may still be on
		// a cached Douban list; an exact title match there is the
		// only other score this app knows. No match means no score.
		score = a.doubanScore(name)
	}
	if saved, ok := a.rememberedCandidate(name, pic); ok {
		known := false
		for _, item := range seeds {
			known = known || sourceMemoryItemKey(item) == sourceMemoryItemKey(saved)
		}
		if !known {
			seeds = append([]library.Item{saved}, seeds...)
			searchFallback = true
		}
	}
	a.title = &titlePage{name: name, pic: pic, score: score, seeds: seeds, auto: true, searchFallback: searchFallback, failed: map[string]string{}}
	a.push(routeDetail)
	preferred := a.sourceMemory.entries[titleKey(name)].Preferred
	knownPreferred := false
	for _, item := range seeds {
		knownPreferred = knownPreferred || preferred.matches(item)
	}
	if len(seeds) == 0 || preferred.VideoID != "" && a.configAvailable(preferred.ConfigURL) && !knownPreferred {
		a.title.searchFallback = false
		a.startScan(&a.library.search, name, false)
		return
	}
	a.autoOpenTitle()
}

// clearTitle releases the title on show and whatever was loading for it.
func (a *app) clearTitle() {
	a.cancelJob(jobTitle)
	a.cancelJob(jobPlay)
	if a.title != nil {
		a.clearPanelChoice()
		a.closeSession(a.title.session)
		a.title = nil
	}
	a.playingSource, a.playingEpisode = "", ""
	a.playFailure = nil
	a.playbackWait = playbackWait{}
	a.sourceAttempt = sourcePlaybackAttempt{}
}

// candidates prefer the last source that actually played this title. Failed
// reads stay last; playback observations and interface timing order the rest.
func (a *app) candidates() []library.Item {
	t := a.title
	if t == nil {
		return nil
	}
	out := append([]library.Item(nil), t.seeds...)
	seen := map[string]bool{}
	for _, item := range out {
		seen[itemKey(item)] = true
	}
	key := titleKey(t.name)
	if t.item.Entry.Config != nil && !seen[itemKey(t.item)] {
		seen[itemKey(t.item)] = true
		out = append(out, t.item)
	}
	for _, item := range a.library.search.items {
		if titleKey(item.Video.Name) == key && !seen[itemKey(item)] {
			seen[itemKey(item)] = true
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return a.candidateLess(t, out[i], out[j])
	})
	return out
}

// Equal sources keep their original order, as guaranteed by the stable sort.
func (a *app) candidateLess(t *titlePage, x, y library.Item) bool {
	fx, fy := t.failed[itemKey(x)] != "", t.failed[itemKey(y)] != ""
	if fx != fy {
		return fy
	}
	memory := a.sourceMemory.entries[titleKey(t.name)]
	px, py := memory.Preferred.matches(x), memory.Preferred.matches(y)
	if px != py {
		return px
	}
	rx, ry := memory.Records[sourceMemoryItemKey(x)], memory.Records[sourceMemoryItemKey(y)]
	if rx.ConsecutiveFailures != ry.ConsecutiveFailures {
		return rx.ConsecutiveFailures < ry.ConsecutiveFailures
	}
	if (rx.Successes > 0) != (ry.Successes > 0) {
		return rx.Successes > 0
	}
	if rx.Successes > 0 && ry.Successes > 0 && rx.StartMS != ry.StartMS {
		return rx.StartMS < ry.StartMS
	}
	dx, okx := a.latency.get(x.Entry.Key())
	dy, oky := a.latency.get(y.Entry.Key())
	if okx != oky {
		return okx
	}
	if okx && oky && dx != dy {
		return dx < dy
	}
	return false
}

func (a *app) autoOpenTitle() {
	t := a.title
	if t == nil || !t.auto || t.detail != nil || a.running(jobTitle) {
		return
	}
	candidates := a.candidates()
	preferred := a.sourceMemory.entries[titleKey(t.name)].Preferred
	preferredSeen := false
	for _, item := range candidates {
		if preferred.matches(item) {
			preferredSeen = true
			if t.failed[itemKey(item)] == "" {
				a.openCandidate(item)
				return
			}
		}
	}
	// Library replies arrive out of order. Give the saved source its chance
	// before auto-opening a quicker search response from another site.
	if !preferredSeen && preferred.VideoID != "" && a.configAvailable(preferred.ConfigURL) && a.library.search.running && titleKey(a.library.search.query) == titleKey(t.name) {
		return
	}
	for _, item := range candidates {
		if _, bad := t.failed[itemKey(item)]; !bad {
			a.openCandidate(item)
			return
		}
	}
	if t.searchFallback {
		t.searchFallback = false
		a.startScan(&a.library.search, t.name, false)
	}
}

// openItem reads a video's detail from its own site.
func openItem(ctx context.Context, item library.Item, assets string) (source.Session, catalog.Detail, error) {
	session, err := source.OpenContent(ctx, item.Entry.Site, assets)
	if err != nil {
		return nil, catalog.Detail{}, err
	}
	detail, err := session.Detail(ctx, string(item.Video.ID))
	if err != nil {
		session.Close()
		return nil, catalog.Detail{}, err
	}
	if detail.Name == "" {
		detail.Name = item.Video.Name
	}
	if detail.Pic == "" {
		detail.Pic = item.Video.Pic
	}
	detail.Content = stripHTML(detail.Content)
	return session, detail, nil
}

// stripHTML takes the tags and entities out of a synopsis: CMS sites
// return the description wrapped in <p>…</p> and the like, which the
// detail page would otherwise show literally.
func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := b.String()
	// &amp; last, so "&amp;lt;" becomes "&lt;", not "<".
	for _, e := range [][2]string{
		{"&nbsp;", " "}, {"&quot;", `"`}, {"&#39;", "'"}, {"&apos;", "'"},
		{"&lt;", "<"}, {"&gt;", ">"}, {"&amp;", "&"},
	} {
		out = strings.ReplaceAll(out, e[0], e[1])
	}
	return strings.Join(strings.Fields(out), " ")
}

// openCandidate shows the title as one of its sources has it.
func (a *app) openCandidate(item library.Item) {
	t := a.title
	if t == nil {
		return
	}
	a.cancelJob(jobPlay)
	a.playFailure = nil
	t.pending = item
	key := itemKey(item)
	isRemembered := a.sourceMemory.entries[titleKey(t.name)].Preferred.matches(item)
	assets := strings.TrimSpace(a.browser.assets)
	a.run(jobTitle, "读取影片详情", func(ctx context.Context) (jobResult, error) {
		start := time.Now()
		session, detail, err := openItem(ctx, item, assets)
		elapsed := time.Since(start)
		if err != nil {
			return jobResult{}, err
		}
		if isRemembered && titleKey(detail.Name) != titleKey(t.name) {
			session.Close()
			return jobResult{}, fmt.Errorf("上次的影片编号已变更，重新查找来源")
		}
		return jobResult{discard: session.Close, apply: func() {
			a.closeSession(t.session)
			t.session, t.detail, t.item = session, &detail, item
			t.line, t.chunk = a.preferredLine(t.name, item, detail), 0
			delete(t.failed, key)
			// The detail read is the freshest measurement of this
			// site's speed; a failed read records nothing.
			a.recordLatency(item.Entry.Key(), elapsed)
			a.updateFollowedTitle()
		}}, nil
	}, func(err error) {
		// The page shows the failure beside its source and tries the next one.
		t.failed[key] = jobError("读取", err)
		a.status = ""
		a.autoOpenTitle()
	})
}

// showRelated replaces a title nobody has with the search that looked for it.
func (a *app) showRelated() {
	a.browser.query = a.library.search.query
	a.clearTitle()
	a.nav[len(a.nav)-1] = routeSearch
}

func entryParsers(entry library.Entry) []config.Parser {
	if entry.Config == nil {
		return nil
	}
	return entry.Config.Parsers
}

// resolvePlay turns an episode into media, through the line's parsers or a
// web page when the site does not return a direct address.
func resolvePlay(ctx context.Context, session source.Session, resolver resolve.Resolver, line string, episode catalog.Episode) (catalog.Playback, error) {
	play, err := session.Play(ctx, line, episode)
	if err != nil {
		return catalog.Playback{}, err
	}
	return resolver.Resolve(ctx, play)
}

func (a *app) playEpisode(line string, episode catalog.Episode) {
	a.playEpisodeAt(line, episode, 0)
}

// playEpisodeAt plays an episode of the title on show from a position.
func (a *app) playEpisodeAt(line string, episode catalog.Episode, at time.Duration) {
	a.loadEpisode(line, episode, at, false)
}

func (a *app) loadEpisode(line string, episode catalog.Episode, at time.Duration, refresh bool) {
	t := a.title
	if t == nil || t.session == nil {
		return
	}
	a.clearPanelChoice()
	session := t.session
	videoID := string(t.item.Video.ID)
	refresh = refresh && videoID != ""
	label := "解析分集"
	if refresh {
		label = "重新获取播放地址"
	}
	a.playFailure = nil
	a.playbackWait = playbackWait{}
	a.beginSourceAttempt(t.item, line, episode.Name)
	resolver := resolve.Resolver{Sniff: a.sniffPage, Parsers: entryParsers(t.item.Entry)}
	a.run(jobPlay, label, func(ctx context.Context) (jobResult, error) {
		selected := episode
		var refreshed []catalog.Source
		var lineIndex int
		if refresh {
			detail, err := session.Detail(ctx, videoID)
			if err != nil {
				return jobResult{}, err
			}
			for i, candidate := range detail.Sources {
				if candidate.Name != line {
					continue
				}
				only := catalog.Detail{Sources: detail.Sources[i : i+1]}
				if _, fresh, ok := matchEpisode(only, episode.Name, -1); ok {
					selected, refreshed, lineIndex = fresh, detail.Sources, i
					break
				}
			}
			if refreshed == nil {
				return jobResult{}, fmt.Errorf("这条线路已找不到对应的分集")
			}
		}
		play, err := resolvePlay(ctx, session, resolver, line, selected)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if refreshed != nil {
				t.detail.Sources = refreshed
				t.line, t.chunk = lineIndex, 0
			}
			a.startEpisode(play, line, selected, at)
		}}, nil
	}, func(err error) {
		a.failSourceAttempt()
		a.playFailure = &episodeFailure{line: line, episode: episode, at: at, err: fmt.Errorf("%s", jobError(label, err))}
		a.status = ""
		if a.player != nil {
			a.player.Pause()
		}
	})
}

// startEpisode opens resolved media as an episode of the title on show.
func (a *app) startEpisode(play catalog.Playback, line string, episode catalog.Episode, at time.Duration) {
	t := a.title
	if t == nil {
		return
	}
	if a.sourceAttempt.title != t || sourceMemoryItemKey(a.sourceAttempt.item) != sourceMemoryItemKey(t.item) || a.sourceAttempt.player != nil {
		a.beginSourceAttempt(t.item, line, episode.Name)
	}
	a.sourceAttempt.line, a.sourceAttempt.episode = line, episode.Name
	a.flushHistory()
	if !a.openEpisodeMedia(play) {
		a.failSourceAttempt()
		a.playFailure = &episodeFailure{line: line, episode: episode, at: at, err: fmt.Errorf("%s", a.status)}
		if a.player != nil {
			a.player.Pause()
		}
		return
	}
	a.notePlayingEpisode(line, episode, at)
}

// An open episode panel stays in place across line and source changes,
// including in compact windows and fullscreen.
func (a *app) openEpisodeMedia(play catalog.Playback) bool {
	replacing := a.player != nil
	open, touched := a.panelOpen, a.panelTouched
	if !a.openPlaybackURL(play.URL, play.Headers, replacing) {
		return false
	}
	if replacing {
		a.panelOpen, a.panelTouched = open, touched
	}
	return true
}

func (a *app) notePlayingEpisode(line string, episode catalog.Episode, at time.Duration) {
	t := a.title
	a.playingSource, a.playingEpisode, a.resumeAt = line, episode.Name, at
	a.syncNowPlaying()
	a.sourceAttempt.player = a.player
	a.revealContinueWatching(t.name)
	a.updateFollowedTitle()
}

// matchEpisode finds in detail the episode a viewer was watching elsewhere:
// by name, then by number, then by place in the first line.
func matchEpisode(detail catalog.Detail, name string, index int) (line int, episode catalog.Episode, ok bool) {
	for i, source := range detail.Sources {
		for _, candidate := range source.Episodes {
			if candidate.Name == name {
				return i, candidate, true
			}
		}
	}
	label := episodeButtonLabel(name)
	for i, source := range detail.Sources {
		for _, candidate := range source.Episodes {
			if episodeButtonLabel(candidate.Name) == label {
				return i, candidate, true
			}
		}
	}
	if len(detail.Sources) > 0 && index >= 0 && index < len(detail.Sources[0].Episodes) {
		return 0, detail.Sources[0].Episodes[index], true
	}
	return 0, catalog.Episode{}, false
}

// playNextLine retries the episode on the title's next play line.
func (a *app) playNextLine() {
	t := a.title
	if t == nil || t.detail == nil {
		return
	}
	current, index, requested, at, ok := a.recoveryEpisode()
	if !ok {
		return
	}
	lines := t.detail.Sources
	for step := 1; step < len(lines); step++ {
		i := (current + step) % len(lines)
		only := catalog.Detail{Sources: lines[i : i+1]}
		if _, episode, found := matchEpisode(only, requested.Name, index); found {
			t.line = i
			a.playEpisodeAt(lines[i].Name, episode, at)
			return
		}
	}
	a.status = "没有其他线路可换，试试换来源"
}

func (a *app) detailView(c *ui.Context) {
	t := a.title
	if t == nil {
		emptyState(c, "没有打开的影片", "返回上一页重新选择。")
		return
	}
	candidates := a.candidates()
	// The header stays put while the rest scrolls, so the play button
	// never leaves the screen however far down the sources go.
	ui.Column(c).Fill().Gap(20).Children(func() {
		a.detailHeader(c, t)
		ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Gap(20).Children(func() {
			if f := a.playFailure; f != nil && t.detail != nil && !a.running(jobPlay) {
				ui.Column(c).FillWidth().Padding(16).Gap(10).Radius(18).Background(surface).Children(func() {
					ui.Text(c, f.line+" · "+f.episode.Name).Bold()
					ui.Text(c, f.err.Error()).Selectable()
					a.recoveryActions(c)
				})
			}
			a.sourceAccordion(c, candidates)
		})
	})
}

// detailHeader is the title's poster and information. It is fixed above
// the scrolling body, so the play button is always visible. The header
// is compact and its height is driven by the poster: the synopsis just
// sits in the column as plain text, so a short synopsis leaves no hole
// and the buttons never float up.
func (a *app) detailHeader(c *ui.Context, t *titlePage) {
	ui.Row(c).FillWidth().Gap(22).AlignItems(ui.Start).Children(func() {
		pic := t.pic
		if t.detail != nil && t.detail.Pic != "" {
			pic = t.detail.Pic
		}
		ui.Box(c).Size(170, 240).Radius(16).Clip().Background(ui.Hex("#24242c")).Children(func() {
			if poster := a.poster(pic); poster != nil {
				ui.Image(c, poster).Fill().Fit(ui.Cover)
			}
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(10).Children(func() {
			ui.Text(c, t.name).FontSize(27).Bold()
			if t.detail == nil {
				a.favoriteButton(c)
				return
			}
			// The score is the one the opener knew, or one found on a
			// cached Douban list; a title with no known score shows
			// no score at all, never a made-up one.
			score := ""
			if t.score != "" {
				score = "豆瓣 " + t.score
			}
			if meta := videoMeta(t.detail.Year, t.detail.Type, score); meta != "" {
				ui.Text(c, meta).TextColor(textMuted)
			}
			if t.detail.Content != "" {
				ui.Text(c, t.detail.Content).TextColor(textMuted).MaxLines(3)
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				if line, ok := t.currentLine(); ok && len(line.Episodes) > 0 {
					if primaryButton(c, "立即播放").Clicked() {
						a.playEpisode(line.Name, line.Episodes[0])
					}
				}
				a.favoriteButton(c)
			})
		})
	})
}

func (t *titlePage) currentLine() (catalog.Source, bool) {
	if t.detail == nil || len(t.detail.Sources) == 0 {
		return catalog.Source{}, false
	}
	if t.line < 0 || t.line >= len(t.detail.Sources) {
		t.line = 0
	}
	return t.detail.Sources[t.line], true
}

// isCurrent reports whether item is the source on show. A title with no
// source on show yet — its item is empty — has no current source.
func (t *titlePage) isCurrent(item library.Item) bool {
	return t.detail != nil && t.item.Entry.Config != nil && itemKey(t.item) == itemKey(item)
}

// currentFailed reports whether the source on show failed to load.
func (t *titlePage) currentFailed() bool {
	return t.detail != nil && t.item.Entry.Config != nil && t.failed[itemKey(t.item)] != ""
}

// sourceAccordion is every site that has the title, as one list where
// the source on show is expanded and the rest are one-liners. A source,
// its lines and the line's episodes are one hierarchy, so they live in
// one place instead of three stacked sections: the expanded row holds
// the lines and the episodes, and the rows are compact enough that no
// separate scroll box is needed.
func (a *app) sourceAccordion(c *ui.Context, candidates []library.Item) {
	t, search := a.title, &a.library.search
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "来源").FontSize(19).Bold()
		if len(candidates) > 0 {
			ui.Text(c, fmt.Sprintf("共 %d 个", len(candidates))).FontSize(13).TextColor(textMuted)
		}
		// The "switch source" hint only means something when the source
		// on show actually failed; showing it while it plays fine reads
		// as if the current source were broken.
		if t.currentFailed() || a.playFailure != nil {
			ui.Text(c, "当前来源播不了，换一个即可").FontSize(13).TextColor(textMuted).Grow(1)
		} else {
			ui.Spacer(c)
		}
		if !a.inStack(routeSearch) && a.library.root != nil && titleKey(search.query) != titleKey(t.name) {
			if glassButton(c, "查找其他来源").Clicked() {
				a.startScan(search, t.name, false)
			}
		}
	})
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		for _, item := range candidates {
			a.sourceRow(c, item)
		}
	})
	searching := search.running && titleKey(search.query) == titleKey(t.name)
	switch {
	case searching:
		ui.Text(c, fmt.Sprintf("正在片库中查找 · %d/%d 个站点", search.progress.Completed, search.progress.Total)).TextColor(textMuted)
	case len(candidates) == 0:
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "片库里没有找到《"+t.name+"》。").TextColor(textMuted).Grow(1)
			if len(search.groups) > 0 && glassButton(c, "查看相近结果").Clicked() {
				a.showRelated()
			}
		})
	}
}

// sourceRow is one site that has the title. The source on show is
// expanded: its lines and episodes live inside it. The rest are
// one-liners; choosing one loads its detail, and once it is on show
// it expands in turn. The dot says which one is on show (green) and
// which one failed (grey, with the reason).
func (a *app) sourceRow(c *ui.Context, item library.Item) {
	t := a.title
	key := itemKey(item)
	current := t.isCurrent(item)
	state := ""
	dot := ""
	switch {
	case a.running(jobTitle) && itemKey(t.pending) == key:
		state = "正在读取…"
	case current:
		state = "当前来源"
		dot = "#3fb96a"
	case t.failed[key] != "":
		state = t.failed[key]
		dot = "#8a8f9e"
	}
	if !current {
		row := ui.Row(c).Key(key).FillWidth().Padding(12, 16).Gap(12).Radius(16).AlignItems(ui.Center).
			Background(surface).Cursor(ui.CursorPointer).Label("来源 " + item.Entry.Label())
		row.Children(func() {
			a.sourceRowContent(c, item, dot, state)
		})
		if row.Clicked() {
			t.auto = false
			a.openCandidate(item)
		}
		return
	}
	ui.Column(c).Key(key).FillWidth().Padding(12, 16).Gap(10).Radius(16).
		Background(surface).Label("来源 " + item.Entry.Label()).Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			a.sourceRowContent(c, item, dot, state)
		})
		if t.detail == nil {
			return
		}
		if len(t.detail.Sources) == 0 {
			ui.Text(c, "这个站点没有提供可播放分集，换一个来源试试。").TextColor(textMuted)
			return
		}
		a.lineChips(c)
		a.episodeList(c)
	})
}

// sourceRowContent is the one line every source row shows: its name,
// its remarks, its measured latency if one was ever recorded, and its
// state. A measured latency shows here; an unmeasured source shows
// nothing, never a made-up number.
func (a *app) sourceRowContent(c *ui.Context, item library.Item, dot, state string) {
	if dot != "" {
		ui.Box(c).Size(8, 8).Radius(4).Background(ui.Hex(dot))
	}
	ui.Text(c, item.Entry.Label()).Bold().SingleLine()
	if item.Video.Remarks != "" {
		ui.Text(c, item.Video.Remarks).FontSize(13).TextColor(textMuted).SingleLine()
	}
	ui.Spacer(c)
	if d, ok := a.latency.get(item.Entry.Key()); ok {
		ui.Text(c, formatLatency(d)).FontSize(13).TextColor(textMuted).SingleLine()
	}
	if state != "" {
		ui.Text(c, state).FontSize(13).TextColor(textMuted).SingleLine()
	}
}

// lineChips is the current source's play lines, shown inside its
// expanded row. With one line there is nothing to choose.
func (a *app) lineChips(c *ui.Context) {
	t := a.title
	if len(t.detail.Sources) <= 1 {
		return
	}
	ui.Text(c, "播放线路").FontSize(19).Bold()
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		for i, line := range t.detail.Sources {
			name := line.Name
			if name == "" {
				name = fmt.Sprintf("线路 %d", i+1)
			}
			if chip(c, name, i == t.line).Key(fmt.Sprintf("line-%d", i)).Clicked() {
				t.line, t.chunk = i, 0
			}
		}
	})
}

func (a *app) episodeList(c *ui.Context) {
	t := a.title
	line, ok := t.currentLine()
	if !ok {
		return
	}
	count := len(line.Episodes)
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "选集").FontSize(19).Bold()
		ui.Text(c, fmt.Sprintf("共 %d 集", count)).FontSize(13).TextColor(textMuted)
	})
	if t.chunk*episodeChunk >= count {
		t.chunk = 0
	}
	if count > episodeChunk {
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			for i := 0; i*episodeChunk < count; i++ {
				label := fmt.Sprintf("%d–%d", i*episodeChunk+1, min((i+1)*episodeChunk, count))
				if chip(c, label, i == t.chunk).Key(fmt.Sprintf("chunk-%d", i)).Clicked() {
					t.chunk = i
				}
			}
		})
	}
	start := t.chunk * episodeChunk
	ui.Row(c).Key(fmt.Sprintf("episodes-%d-%d", t.line, t.chunk)).FillWidth().Wrap().Gap(8).Children(func() {
		for j, episode := range line.Episodes[start:min(start+episodeChunk, count)] {
			playing := line.Name == a.playingSource && episode.Name == a.playingEpisode
			if chip(c, episodeButtonLabel(episode.Name), playing).Key(j).MinWidth(52).Clicked() {
				a.playEpisode(line.Name, episode)
			}
		}
	})
}
