package main

import (
	"context"
	"fmt"
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
	seeds          []library.Item    // sources known when the page opened
	failed         map[string]string // source → why it could not be read
	auto           bool              // open the first source that reads
	searchFallback bool              // a saved source may have disappeared
	pending        library.Item      // the source being read
	item           library.Item      // the source on show
	session        source.Session
	detail         *catalog.Detail
	line           int            // chosen play line
	chunk          int            // chosen range of episodes
	sourcesScroll  ui.ScrollState // where the sources box is scrolled
}

// openTitle shows a title. Without seeds, its sources are searched for in
// the library, and the first one that reads opens by itself.
func (a *app) openTitle(name, pic string, seeds []library.Item) {
	a.showTitle(name, pic, seeds, false)
}

func (a *app) showTitle(name, pic string, seeds []library.Item, searchFallback bool) {
	a.clearTitle()
	a.title = &titlePage{name: name, pic: pic, seeds: seeds, auto: true, searchFallback: searchFallback, failed: map[string]string{}}
	a.push(routeDetail)
	if len(seeds) == 0 {
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
		a.closeSession(a.title.session)
		a.title = nil
	}
	a.playingSource, a.playingEpisode = "", ""
	a.playFailure = nil
	a.playbackWait = playbackWait{}
}

// candidates are the title's sources: its seeds, then the library search's
// videos of the same title.
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
	for _, item := range a.library.search.items {
		if titleKey(item.Video.Name) == key && !seen[itemKey(item)] {
			seen[itemKey(item)] = true
			out = append(out, item)
		}
	}
	return out
}

func (a *app) autoOpenTitle() {
	t := a.title
	if t == nil || !t.auto || t.detail != nil || a.running(jobTitle) {
		return
	}
	for _, item := range a.candidates() {
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
	assets := strings.TrimSpace(a.browser.assets)
	a.run(jobTitle, "读取影片详情", func(ctx context.Context) (jobResult, error) {
		session, detail, err := openItem(ctx, item, assets)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{discard: session.Close, apply: func() {
			a.closeSession(t.session)
			t.session, t.detail, t.item = session, &detail, item
			t.line, t.chunk = 0, 0
			delete(t.failed, key)
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
	t := a.title
	if t == nil || t.session == nil {
		return
	}
	session := t.session
	a.playFailure = nil
	a.playbackWait = playbackWait{}
	resolver := resolve.Resolver{Sniff: a.sniffPage, Parsers: entryParsers(t.item.Entry)}
	a.run(jobPlay, "解析分集", func(ctx context.Context) (jobResult, error) {
		play, err := resolvePlay(ctx, session, resolver, line, episode)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() { a.startEpisode(play, line, episode, at) }}, nil
	}, func(err error) {
		a.playFailure = &episodeFailure{line: line, episode: episode, at: at, err: fmt.Errorf("%s", jobError("解析分集", err))}
		a.status = ""
		if a.player != nil {
			a.player.Pause()
		}
	})
}

// startEpisode opens resolved media as an episode of the title on show.
func (a *app) startEpisode(play catalog.Playback, line string, episode catalog.Episode, at time.Duration) {
	a.flushHistory()
	if !a.playURL(play.URL, play.Headers) {
		a.playFailure = &episodeFailure{line: line, episode: episode, at: at, err: fmt.Errorf("%s", a.status)}
		return
	}
	a.playingSource, a.playingEpisode, a.resumeAt = line, episode.Name, at
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

// switchSource keeps the episode playing, from another site that has the title.
func (a *app) switchSource(item library.Item) {
	t := a.title
	if t == nil {
		return
	}
	_, index, episode, at, ok := a.recoveryEpisode()
	if !ok {
		return
	}
	name := episode.Name
	assets := strings.TrimSpace(a.browser.assets)
	resolver := resolve.Resolver{Sniff: a.sniffPage, Parsers: entryParsers(item.Entry)}
	a.cancelJob(jobTitle)
	a.run(jobPlay, "切换来源", func(ctx context.Context) (jobResult, error) {
		session, detail, err := openItem(ctx, item, assets)
		if err != nil {
			return jobResult{}, err
		}
		line, episode, ok := matchEpisode(detail, name, index)
		if !ok {
			session.Close()
			return jobResult{}, fmt.Errorf("这个来源没有对应的分集")
		}
		play, err := resolvePlay(ctx, session, resolver, detail.Sources[line].Name, episode)
		if err != nil {
			session.Close()
			return jobResult{}, err
		}
		return jobResult{discard: session.Close, apply: func() {
			a.closeSession(t.session)
			t.session, t.detail, t.item, t.auto = session, &detail, item, false
			t.line, t.chunk = line, 0
			a.startEpisode(play, detail.Sources[line].Name, episode, at)
		}}, nil
	}, nil)
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
	// The header stays put while the rest scrolls: the sources box
	// scrolls inside itself, so the play button never leaves the
	// screen however far down the sources go.
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
			a.sourceList(c, candidates)
			if t.detail == nil {
				return
			}
			if len(t.detail.Sources) == 0 {
				ui.Text(c, "这个站点没有提供可播放分集，换一个来源试试。").TextColor(textMuted)
				return
			}
			if len(t.detail.Sources) > 1 {
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
			a.episodeList(c)
		})
	})
}

// detailHeader is the title's poster and information. It is fixed above
// the scrolling body, so the play button is always visible.
func (a *app) detailHeader(c *ui.Context, t *titlePage) {
	ui.Row(c).FillWidth().Gap(22).AlignItems(ui.Start).Children(func() {
		pic := t.pic
		if t.detail != nil && t.detail.Pic != "" {
			pic = t.detail.Pic
		}
		ui.Box(c).Size(220, 310).Radius(16).Clip().Background(ui.Hex("#24242c")).Children(func() {
			if poster := a.poster(pic); poster != nil {
				ui.Image(c, poster).Fill().Fit(ui.Cover)
			}
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(10).Children(func() {
			ui.Text(c, t.name).FontSize(27).Bold()
			a.favoriteButton(c)
			if t.detail == nil {
				return
			}
			if meta := videoMeta(t.detail.Year, t.detail.Type, ""); meta != "" {
				ui.Text(c, meta).TextColor(textMuted)
			}
			if t.detail.Content != "" {
				ui.Column(c).FillWidth().Padding(12, 14).Radius(14).Background(surface).Children(func() {
					ui.Text(c, t.detail.Content).MaxHeight(132).Clip()
				})
			}
			if line, ok := t.currentLine(); ok && len(line.Episodes) > 0 {
				if primaryButton(c, "立即播放").FillWidth().Clicked() {
					a.playEpisode(line.Name, line.Episodes[0])
				}
			}
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

// sourceCardHeight is the height of one source card; sourceBoxHeight
// is the most the sources box grows to: three rows of cards (nine
// cards) and a peek of the next row, so the box reads as scrollable
// when there are more sources below. With fewer sources the box stays
// as tall as its cards, so the episodes below it stay on screen.
const sourceCardHeight = 56
const sourceGridRows = 3
const sourceBoxHeight = sourceGridRows*sourceCardHeight + (sourceGridRows-1)*8 + 16

// sourceList is every site that has the title; choosing one shows it there.
func (a *app) sourceList(c *ui.Context, candidates []library.Item) {
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
	if len(candidates) > 0 {
		// The grid scrolls inside its own box, so the header — and its
		// play button — never scroll away with it.
		ui.Scroll(c).FillWidth().MaxHeight(sourceBoxHeight).TrackScroll(&t.sourcesScroll).
			Label("来源列表").Children(func() {
			ui.Grid(c).Columns(3).Gap(8).Children(func() {
				for _, item := range candidates {
					a.sourceCard(c, item)
				}
			})
		})
		if len(candidates) > sourceGridRows*3 {
			if t.sourcesScroll.MaxY > 0 && t.sourcesScroll.Y >= t.sourcesScroll.MaxY {
				ui.Text(c, fmt.Sprintf("已显示全部 %d 个来源", len(candidates))).FontSize(13).TextColor(textMuted)
			} else {
				ui.Text(c, "下滑查看更多").FontSize(13).TextColor(textMuted)
			}
		}
	}
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

// sourceCard is one site that has the title, in the sources grid. The
// dot says which one is on show (green), which one is still being read
// (no dot, "正在读取…") and which one failed (grey, with the reason).
func (a *app) sourceCard(c *ui.Context, item library.Item) {
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
	card := ui.Column(c).Key(key).Height(sourceCardHeight).Padding(8, 14).Gap(2).Radius(14).
		Background(surface).Cursor(ui.CursorPointer).Label("来源 " + item.Entry.Label())
	card.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if dot != "" {
				ui.Box(c).Size(8, 8).Radius(4).Background(ui.Hex(dot))
			}
			ui.Text(c, item.Entry.Label()).Bold().SingleLine().Grow(1).MinWidth(0)
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if item.Video.Remarks != "" {
				ui.Text(c, item.Video.Remarks).FontSize(13).TextColor(textMuted).SingleLine().Grow(1).MinWidth(0)
			} else {
				ui.Spacer(c)
			}
			if state != "" {
				ui.Text(c, state).FontSize(13).TextColor(textMuted).SingleLine()
			}
		})
	})
	if card.Clicked() && !current {
		t.auto = false
		a.openCandidate(item)
	}
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
