package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
	"github.com/arixbit/xiaomu-desktop/internal/source"
	"github.com/egoist/mygo/ui"
)

// episodeChunk is how many episodes show at once; longer series page.
const episodeChunk = 50

// detailHedgeDelay is how long automatic detail loading waits on the primary
// candidate before launching a backup candidate in parallel to avoid hanging
// on a single slow source.
var detailHedgeDelay = 1500 * time.Millisecond

// titlePage is one title and the sites that have it. Its session belongs to
// the source on show, and serves the player while an episode plays.
type titlePage struct {
	name, pic        string
	score            string            // Douban score, when the opener knew one
	seeds            []library.Item    // sources known when the page opened
	sourceScan       *libraryScan      // private title search for playback from history or following
	failed           map[string]string // source → why it could not be read
	auto             bool              // open the first source that reads
	searchFallback   bool              // a saved source may have disappeared
	pending          library.Item      // the source being read
	item             library.Item      // the source on show
	session          source.Session
	detail           *catalog.Detail
	line             int  // chosen play line
	chunk            int  // chosen range of episodes
	synopsisExpanded bool // whether full description is expanded
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
		if s := a.title.sourceScan; s != nil {
			a.stopScan(s)
		}
		a.clearPanelChoice()
		a.closeSession(a.title.session)
		a.title = nil
	}
	a.playingSource, a.playingEpisode = "", ""
	a.playFailure = nil
	a.resetFailureUI()
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
	key := titleKey(t.name)
	s := a.titleSearch()
	var searchMatches []library.Item
	useIndex := false
	if key != "" && len(s.items) > 0 && s.groupIndex != nil && s.groupIndexLen == len(s.items) && s.groupIndexHead == &s.items[0] {
		useIndex = true
		if idx, ok := s.groupIndex[key]; ok && idx < len(s.groups) {
			searchMatches = s.groups[idx].items
		}
	}
	capHint := len(t.seeds) + len(searchMatches) + 1
	out := make([]library.Item, 0, capHint)
	entryKeys := make([]string, 0, capHint)
	seen := make(map[itemID]struct{}, capHint)
	add := func(item library.Item) {
		ek := entryKey(item.Entry)
		id := itemID{entryKey: ek, videoID: item.Video.ID}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, item)
		entryKeys = append(entryKeys, ek)
	}
	for _, item := range t.seeds {
		add(item)
	}
	if t.item.Entry.Config != nil {
		add(t.item)
	}
	if useIndex {
		for _, item := range searchMatches {
			add(item)
		}
	} else {
		for _, item := range s.items {
			if titleKey(item.Video.Name) == key {
				add(item)
			}
		}
	}
	if len(out) <= 1 {
		return out
	}
	memory := a.sourceMemory.entries[key]
	type rankedCandidate struct {
		item                library.Item
		failed              bool
		preferred           bool
		consecutiveFailures int
		hasSuccess          bool
		startMS             int64
		hasLatency          bool
		latency             time.Duration
	}
	ranked := make([]rankedCandidate, len(out))
	for i, item := range out {
		ek := entryKeys[i]
		ik := ""
		if len(t.failed) > 0 || len(memory.Records) > 0 {
			ik = ek + "\x00" + string(item.Video.ID)
		}
		var rec sourcePlaybackRecord
		if len(memory.Records) > 0 {
			rec = memory.Records[ik]
		}
		d, ok := a.latency.get(ek)
		ranked[i] = rankedCandidate{
			item:                item,
			failed:              len(t.failed) > 0 && t.failed[ik] != "",
			preferred:           memory.Preferred.matches(item),
			consecutiveFailures: rec.ConsecutiveFailures,
			hasSuccess:          rec.Successes > 0,
			startMS:             rec.StartMS,
			hasLatency:          ok,
			latency:             d,
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		x, y := &ranked[i], &ranked[j]
		if x.failed != y.failed {
			return y.failed
		}
		if x.preferred != y.preferred {
			return x.preferred
		}
		if x.consecutiveFailures != y.consecutiveFailures {
			return x.consecutiveFailures < y.consecutiveFailures
		}
		if x.hasSuccess != y.hasSuccess {
			return x.hasSuccess
		}
		if x.hasSuccess && x.startMS != y.startMS {
			return x.startMS < y.startMS
		}
		if x.hasLatency != y.hasLatency {
			return x.hasLatency
		}
		if x.hasLatency && x.latency != y.latency {
			return x.latency < y.latency
		}
		return false
	})
	for i := range ranked {
		out[i] = ranked[i].item
	}
	return out
}

func (a *app) autoOpenTitle() {
	t := a.title
	if t == nil || !t.auto || t.detail != nil || a.running(jobTitle) {
		return
	}
	candidates := a.candidates()
	preferred := a.sourceMemory.entries[titleKey(t.name)].Preferred
	preferredSeen := false
	var untried []library.Item
	for _, item := range candidates {
		isPref := preferred.matches(item)
		if isPref {
			preferredSeen = true
		}
		if t.failed[itemKey(item)] != "" {
			continue
		}
		if isPref {
			untried = append([]library.Item{item}, untried...)
		} else {
			untried = append(untried, item)
		}
	}
	// Library replies arrive out of order. Give the saved source its chance
	// before auto-opening a quicker search response from another site.
	if !preferredSeen && preferred.VideoID != "" && a.configAvailable(preferred.ConfigURL) && a.library.search.running && titleKey(a.library.search.query) == titleKey(t.name) {
		return
	}
	if len(untried) >= 2 && detailHedgeDelay > 0 {
		a.openCandidatesHedged(untried[0], untried[1])
		return
	}
	if len(untried) == 1 {
		a.openCandidate(untried[0])
		return
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
	if expanded, handled, expErr := pan.ExpandDetailShares(ctx, detail); handled {
		if expErr != nil {
			session.Close()
			return nil, catalog.Detail{}, expErr
		}
		detail = expanded
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

// applyDetail updates the active source's lines and episodes while keeping
// established title header metadata (poster, year, type and synopsis) stable
// across source switches. Missing header fields are filled when a later source
// provides them.
func (t *titlePage) applyDetail(detail catalog.Detail) {
	if t.pic != "" {
		detail.Pic = t.pic
	} else if detail.Pic != "" {
		t.pic = detail.Pic
	}
	if prev := t.detail; prev != nil {
		if prev.Year != "" {
			detail.Year = prev.Year
		}
		if prev.Type != "" {
			detail.Type = prev.Type
		}
		if prev.Content != "" {
			detail.Content = prev.Content
		}
	}
	t.detail = &detail
}

// openCandidate shows the title as one of its sources has it.
func (a *app) openCandidate(item library.Item) {
	t := a.title
	if t == nil {
		return
	}
	a.cancelJob(jobPlay)
	a.playFailure = nil
	a.resetFailureUI()
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
			t.session, t.item = session, item
			t.applyDetail(detail)
			t.line = a.preferredLine(t.name, item, *t.detail)
			t.chunk = a.initialEpisodeChunk(t.name, *t.detail, t.line)
			delete(t.failed, key)
			// The detail read is the freshest measurement of this
			// site's speed; a failed read records nothing.
			a.recordLatency(item.Entry.Key(), elapsed)
			a.updateFollowedTitle()
		}}, nil
	}, func(err error) {
		if provider, needAuth := pan.AuthRequiredProvider(err); needAuth && !t.auto {
			t.failed[key] = err.Error()
			a.clearStatus()
			originTitle := t
			a.promptPanLogin(provider, err.Error(), func() {
				if a.title != originTitle {
					return
				}
				a.openCandidate(item)
			})
			return
		}
		// The page shows the failure beside its source and tries the next one.
		t.failed[key] = jobError("读取", err)
		a.status = ""
		a.autoOpenTitle()
	})
}

type candidateOutcome struct {
	item    library.Item
	session source.Session
	detail  catalog.Detail
	elapsed time.Duration
	err     error
}

type hedgedDetailError struct {
	failed map[string]string
	last   error
}

func (e hedgedDetailError) Error() string {
	if e.last != nil {
		return e.last.Error()
	}
	return "候选来源均读取失败"
}

// openCandidatesHedged starts reading primary immediately and, if it neither
// succeeds nor fails within detailHedgeDelay, launches backup in parallel.
// The first candidate that succeeds wins; the slower request is canceled and
// any late session is closed without leaking.
func (a *app) openCandidatesHedged(primary, backup library.Item) {
	t := a.title
	if t == nil {
		return
	}
	a.cancelJob(jobPlay)
	a.playFailure = nil
	a.resetFailureUI()
	t.pending = primary
	titleName := t.name
	preferred := a.sourceMemory.entries[titleKey(titleName)].Preferred
	primaryRemembered := preferred.matches(primary)
	backupRemembered := preferred.matches(backup)
	assets := strings.TrimSpace(a.browser.assets)
	delay := detailHedgeDelay
	a.run(jobTitle, "读取影片详情", func(ctx context.Context) (jobResult, error) {
		raceCtx, raceCancel := context.WithCancel(ctx)
		outcomes := make(chan candidateOutcome, 2)
		launch := func(item library.Item, isRemembered bool) {
			go func() {
				start := time.Now()
				session, detail, err := openItem(raceCtx, item, assets)
				elapsed := time.Since(start)
				if err == nil && isRemembered && titleKey(detail.Name) != titleKey(titleName) {
					session.Close()
					session = nil
					err = fmt.Errorf("上次的影片编号已变更，重新查找来源")
				}
				outcomes <- candidateOutcome{item: item, session: session, detail: detail, elapsed: elapsed, err: err}
			}()
		}

		launch(primary, primaryRemembered)
		inFlight := 1
		backupStarted := false
		timer := time.NewTimer(delay)
		defer timer.Stop()
		hedgeCh := timer.C
		failed := map[string]string{}
		var lastErr error

		for inFlight > 0 || !backupStarted {
			select {
			case <-hedgeCh:
				hedgeCh = nil
				if !backupStarted && ctx.Err() == nil {
					backupStarted = true
					inFlight++
					launch(backup, backupRemembered)
				} else if !backupStarted {
					backupStarted = true
				}
			case res := <-outcomes:
				inFlight--
				if res.err == nil {
					raceCancel()
					if remaining := inFlight; remaining > 0 {
						go func() {
							for range remaining {
								late := <-outcomes
								if late.session != nil {
									late.session.Close()
								}
							}
						}()
					}
					winner := res
					return jobResult{discard: winner.session.Close, apply: func() {
						a.closeSession(t.session)
						t.session, t.item = winner.session, winner.item
						t.applyDetail(winner.detail)
						t.line = a.preferredLine(t.name, winner.item, *t.detail)
						t.chunk = a.initialEpisodeChunk(t.name, *t.detail, t.line)
						for fk, msg := range failed {
							t.failed[fk] = msg
						}
						delete(t.failed, itemKey(winner.item))
						a.recordLatency(winner.item.Entry.Key(), winner.elapsed)
						a.updateFollowedTitle()
					}}, nil
				}
				if ctx.Err() != nil {
					raceCancel()
					if remaining := inFlight; remaining > 0 {
						go func() {
							for range remaining {
								late := <-outcomes
								if late.session != nil {
									late.session.Close()
								}
							}
						}()
					}
					return jobResult{}, ctx.Err()
				}
				failed[itemKey(res.item)] = jobError("读取", res.err)
				lastErr = res.err
				if !backupStarted {
					timer.Stop()
					hedgeCh = nil
					backupStarted = true
					inFlight++
					launch(backup, backupRemembered)
				}
			}
		}
		raceCancel()
		return jobResult{}, hedgedDetailError{failed: failed, last: lastErr}
	}, func(err error) {
		if he, ok := err.(hedgedDetailError); ok {
			for fk, msg := range he.failed {
				t.failed[fk] = msg
			}
		} else {
			t.failed[itemKey(primary)] = jobError("读取", err)
		}
		a.status = ""
		a.autoOpenTitle()
	})
}

// initialEpisodeChunk picks the episode range page containing the viewer's
// watched/next episode so long series open straight to where they left off.
func (a *app) initialEpisodeChunk(title string, detail catalog.Detail, lineIdx int) int {
	if lineIdx < 0 || lineIdx >= len(detail.Sources) {
		return 0
	}
	episodes := detail.Sources[lineIdx].Episodes
	if len(episodes) <= episodeChunk {
		return 0
	}
	targetName := ""
	if a.playingEpisode != "" && a.title != nil && titleKey(a.title.name) == titleKey(title) {
		targetName = a.playingEpisode
	} else if h, found := a.history.findByTitle(title); found {
		if _, target, _, ok := resumePoint(detail, h); ok {
			targetName = target.Name
		} else {
			targetName = h.Episode
		}
	}
	if targetName == "" {
		return 0
	}
	for i, ep := range episodes {
		if ep.Name == targetName || episodeButtonLabel(ep.Name) == episodeButtonLabel(targetName) {
			return i / episodeChunk
		}
	}
	return 0
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

// resolvePlay turns an episode into media, through cloud-drive resolution,
// the line's parsers or a web page when the site does not return a direct address.
func resolvePlay(ctx context.Context, session source.Session, resolver resolve.Resolver, line string, episode catalog.Episode) (catalog.Playback, error) {
	if pan.IsPanLink(episode.ID) {
		return pan.ResolveEpisode(ctx, episode.ID)
	}
	play, err := session.Play(ctx, line, episode)
	if err != nil {
		return catalog.Playback{}, err
	}
	if play.PanShare != "" {
		resolved, panErr := pan.ResolveEpisode(ctx, play.PanShare)
		if panErr != nil {
			return catalog.Playback{}, panErr
		}
		if resolved.Danmaku == "" {
			resolved.Danmaku = play.Danmaku
		}
		return resolved, nil
	}
	if len(play.Streams) > 0 {
		return play, nil
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
	isRetry := refresh
	hadFailure := isRetry && a.playFailure != nil
	inPlaceRetry := hadFailure && a.player != nil
	refresh = refresh && videoID != ""
	label := "解析分集"
	if refresh {
		label = "重新获取播放地址"
	}
	if inPlaceRetry {
		a.failureUI.retrying = true
		a.failureUI.retryJobGen = 0
		a.failureUI.copySeq++
		a.failureUI.copied = false
		a.failureUI.copyFailed = false
	} else {
		a.playFailure = nil
		a.resetFailureUIForNewRequest()
	}
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
			if a.title != t {
				return
			}
			if refreshed != nil {
				t.detail.Sources = refreshed
				t.line, t.chunk = lineIndex, 0
			}
			a.startEpisodeInternal(play, line, selected, at, hadFailure)
		}}, nil
	}, func(err error) {
		if a.title != t {
			return
		}
		a.failSourceAttempt()
		a.failureUI.retrying = false
		a.failureUI.retryJobGen = 0
		a.failureUI.copySeq++
		a.failureUI.copied = false
		a.failureUI.copyFailed = false
		if provider, needAuth := pan.AuthRequiredProvider(err); needAuth {
			a.playFailure = &episodeFailure{
				line:         line,
				episode:      episode,
				at:           at,
				err:          err,
				authProvider: provider,
				retried:      hadFailure,
			}
			a.status = ""
			if a.player != nil {
				a.player.Pause()
			}
			originTitle := t
			originPlayGen := a.jobGen(jobPlay)
			originPlayer := a.player
			a.promptPanLogin(provider, err.Error(), func() {
				if a.title != originTitle || a.jobGen(jobPlay) != originPlayGen || a.player != originPlayer {
					return
				}
				a.resumeAfterPanLogin(line, episode, at)
			})
			return
		}
		a.playFailure = &episodeFailure{
			line:    line,
			episode: episode,
			at:      at,
			err:     formatFailureErrorMessage(label, err),
			retried: hadFailure,
		}
		a.status = ""
		if a.player != nil {
			a.player.Pause()
		}
	})
	if a.failureUI.retrying {
		if a.running(jobPlay) {
			a.failureUI.retryJobGen = a.jobGen(jobPlay)
		} else {
			a.failureUI.retrying = false
			a.failureUI.retryJobGen = 0
		}
	}
}

// resumeAfterPanLogin refreshes the episode list if the clicked item was an
// unexpanded share link placeholder, then immediately starts playback.
func (a *app) resumeAfterPanLogin(line string, episode catalog.Episode, at time.Duration) {
	t := a.title
	if t == nil || t.session == nil {
		return
	}
	if _, isRawShare := pan.ParseShareURL(episode.ID); isRawShare {
		videoID := string(t.item.Video.ID)
		session := t.session
		a.run(jobPlay, "加载网盘剧集", func(ctx context.Context) (jobResult, error) {
			detail, err := session.Detail(ctx, videoID)
			if err != nil {
				return jobResult{}, err
			}
			if expanded, handled, expErr := pan.ExpandDetailShares(ctx, detail); handled && expErr == nil {
				detail = expanded
			}
			return jobResult{apply: func() {
				if a.title != t {
					return
				}
				t.applyDetail(detail)
				for i, src := range t.detail.Sources {
					if src.Name == line && len(src.Episodes) > 0 {
						t.line, t.chunk = i, 0
						a.playEpisodeAt(src.Name, src.Episodes[0], at)
						return
					}
				}
				if len(detail.Sources) > 0 && len(detail.Sources[0].Episodes) > 0 {
					t.line, t.chunk = 0, 0
					a.playEpisodeAt(detail.Sources[0].Name, detail.Sources[0].Episodes[0], at)
				}
			}}, nil
		}, func(err error) {
			a.playFailure = &episodeFailure{line: line, episode: episode, at: at, err: fmt.Errorf("%s", jobError("加载网盘剧集", err))}
			a.status = ""
		})
		return
	}
	a.playEpisodeAt(line, episode, at)
}

// startEpisode opens resolved media as an episode of the title on show.
func (a *app) startEpisode(play catalog.Playback, line string, episode catalog.Episode, at time.Duration) {
	a.startEpisodeInternal(play, line, episode, at, false)
}

func (a *app) startEpisodeInternal(play catalog.Playback, line string, episode catalog.Episode, at time.Duration, retried bool) {
	t := a.title
	if t == nil {
		return
	}
	wasReplacing := a.player != nil
	prevLine := a.playingSource
	if a.sourceAttempt.title != t || sourceMemoryItemKey(a.sourceAttempt.item) != sourceMemoryItemKey(t.item) || a.sourceAttempt.player != nil {
		a.beginSourceAttempt(t.item, line, episode.Name)
	}
	a.sourceAttempt.line, a.sourceAttempt.episode = line, episode.Name
	a.flushHistory()
	if !a.openEpisodeMedia(play) {
		a.failSourceAttempt()
		a.failureUI.retrying = false
		a.failureUI.retryJobGen = 0
		a.failureUI.copySeq++
		a.failureUI.copied = false
		a.failureUI.copyFailed = false
		a.playFailure = &episodeFailure{line: line, episode: episode, at: at, err: fmt.Errorf("%s", a.status), retried: retried}
		if a.player != nil {
			a.player.Pause()
		}
		return
	}
	a.notePlayingEpisode(line, episode, at)
	if wasReplacing && prevLine != "" && prevLine != line {
		a.showPlayerOSD("已切换：" + line)
	}
}

func selectPlaybackStream(play catalog.Playback) (catalog.Stream, int, bool) {
	if len(play.Streams) == 0 {
		return catalog.Stream{}, -1, false
	}
	for i, s := range play.Streams {
		if s.URL == play.URL {
			return s, i, true
		}
	}
	idx := pan.PickPreferredStream(play.Streams, pan.DefaultCredentials().EffectiveQuality())
	if idx < 0 || idx >= len(play.Streams) {
		idx = 0
	}
	return play.Streams[idx], idx, true
}

// An open episode panel stays in place across line and source changes,
// including in compact windows and fullscreen.
func (a *app) openEpisodeMedia(play catalog.Playback) bool {
	replacing := a.player != nil
	open, touched := a.panelOpen, a.panelTouched
	skipOpen, wasShown := a.panelSkipOpen, a.panelWasShown
	closeRecoveryPanel := a.failureUI.panelFromRecovery
	if stream, idx, ok := selectPlaybackStream(play); ok {
		if !a.openPlaybackStream(stream, replacing) {
			return false
		}
		a.playingStreams = append([]catalog.Stream(nil), play.Streams...)
		a.playingStreamIdx = idx
	} else {
		if !a.openPlaybackURL(play.URL, play.Headers, replacing) {
			return false
		}
	}
	if replacing {
		if closeRecoveryPanel {
			a.panelOpen, a.panelTouched = false, true
			a.panelSkipOpen, a.panelWasShown = true, false
		} else {
			a.panelOpen, a.panelTouched = open, touched
			a.panelSkipOpen, a.panelWasShown = skipOpen, wasShown
		}
	}
	a.danmaku.sourceURL = play.Danmaku
	return true
}

func (a *app) notePlayingEpisode(line string, episode catalog.Episode, at time.Duration) {
	t := a.title
	a.playingSource, a.playingEpisode, a.resumeAt = line, episode.Name, at
	a.playingSourceIdx, a.playingEpisodeIdx = -1, -1
	a.panelRevealEpisode = true
	a.syncNowPlaying()
	a.sourceAttempt.player = a.player
	a.revealContinueWatching(t.name)
	a.updateFollowedTitle()
	a.startDanmaku()
}

var spacedDashEpisode = regexp.MustCompile(`(?:\s+[-–—]\s*)0*([0-9]{1,3})(?:\s*[\[\(【（]|$)`)

// episodeMatchNumber extracts a normalized episode number from either standard
// CMS labels ("第05集", "05") or cloud-drive/release filenames ("Show.S01E05.2160p",
// "片名 EP05 4K", "[字幕组] 番剧 - 05 [1080p]").
func episodeMatchNumber(name string) int {
	if n := danmakuNumber(name); n > 0 && (n < 1900 || n > 2099) {
		return n
	}
	if m := spacedDashEpisode.FindStringSubmatch(name); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// matchEpisode finds in detail the episode a viewer was watching elsewhere:
// by name, then by label, then by normalized episode number (for cloud-drive
// filenames like S01E05 / EP05 vs 第05集), then by place in the first line.
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
	if num := episodeMatchNumber(name); num > 0 {
		for i, source := range detail.Sources {
			for _, candidate := range source.Episodes {
				if episodeMatchNumber(candidate.Name) == num {
					return i, candidate, true
				}
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
	if choice := a.panelChoice; choice != nil && choice.failed && choice.detail != nil && len(choice.detail.Sources) > 1 {
		lines := choice.detail.Sources
		current := a.panelSource
		if current < 0 || current >= len(lines) {
			current = 0
		}
		curLine, _, requested, at, hasRecovery := a.recoveryEpisode()
		if !hasRecovery {
			requested = choice.episode
		}
		for step := 1; step < len(lines); step++ {
			i := (current + step) % len(lines)
			only := catalog.Detail{Sources: lines[i : i+1]}
			if _, episode, found := matchEpisode(only, requested.Name, -1); found {
				a.panelSource = i
				a.playPanelEpisode(i, episode, at)
				return
			}
			if hasRecovery {
				if singleEp, matched := a.singleMovieMatch(curLine, requested, lines[i]); matched {
					a.panelSource = i
					a.playPanelEpisode(i, singleEp, at)
					return
				}
			}
		}
		if len(a.candidates()) > 1 {
			a.setNotice("没有其他线路可换，试试换来源")
		} else {
			a.setNotice("没有其他线路可换")
		}
		return
	}
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
	if len(a.candidates()) > 1 {
		a.setNotice("没有其他线路可换，试试换来源")
	} else {
		a.setNotice("没有其他线路可换")
	}
}

func (a *app) detailView(c *ui.Context) {
	t := a.title
	if t == nil {
		emptyState(c, "没有打开的影片", "返回上一页重新选择。")
		return
	}
	p := paletteFor(c)
	candidates := a.candidates()
	pic := t.pic
	if pic == "" && t.detail != nil && t.detail.Pic != "" {
		pic = t.detail.Pic
	}
	posterSrc := a.poster(c, pic)

	ui.Box(c).Fill().Children(func() {
		if !p.reduceTransparency {
			ambient := ui.Box(c.Key("detail-ambient")).Absolute().Top(0).Left(0).Right(0).Height(310).ZIndex(-1).Clip().PassThrough()
			ambient.Children(func() {
				applyAmbientPoster(c, ambient, posterSrc, 0.28, 46, ui.LinearGradient{
					Angle: 180,
					Stops: []ui.GradientStop{
						{Color: ui.RGBA(0, 0, 0, 1), At: 0},
						{Color: ui.RGBA(0, 0, 0, 0.52), At: 0.55},
						{Color: ui.Transparent, At: 1},
					},
				})
			})
		}
		ui.Scroll(c.Key("detail-scroll")).Fill().Padding(62, 24, 24, 24).Gap(18).ScrollbarStyle(appScrollbar).Children(func() {
			a.detailHero(c, t, posterSrc)
			if a.title != t {
				return
			}
			a.detailSourceStrip(c, candidates)
			if a.title != t {
				return
			}
			if f := a.playFailure; f != nil && t.detail != nil && !a.running(jobPlay) {
				a.detailFailureBanner(c, f)
				if a.title != t {
					return
				}
			}
			a.detailPlaybackSection(c)
		})
	})
}

// detailResumeCTA checks whether the viewer already has watch progress on this
// title and resolves the right resume or next-episode action on the loaded detail.
func (a *app) detailResumeCTA(t *titlePage) (label, lineName string, ep catalog.Episode, at time.Duration, ok bool) {
	if t == nil || t.detail == nil {
		return "", "", catalog.Episode{}, 0, false
	}
	if h, found := a.history.findByTitle(t.name); found {
		if lineIdx, target, pos, canResume := resumePoint(*t.detail, h); canResume {
			if h.finished() {
				return "播放下一集 · " + target.Name, t.detail.Sources[lineIdx].Name, target, 0, true
			}
			return "继续播放 · " + target.Name, t.detail.Sources[lineIdx].Name, target, pos, true
		}
	}
	if line, hasLine := t.currentLine(); hasLine && len(line.Episodes) > 0 {
		return "立即播放", line.Name, line.Episodes[0], 0, true
	}
	return "", "", catalog.Episode{}, 0, false
}

// detailHero renders the poster and editorial metadata at the top of the detail page.
func (a *app) detailHero(c *ui.Context, t *titlePage, posterSrc ui.ImageSource) {
	p := paletteFor(c)

	ui.Row(c).FillWidth().Gap(22).AlignItems(ui.Start).Children(func() {
		posterBox := ui.Box(c).Size(152, 224).Radius(12).Clip().Shrink(0).
			Background(p.cardPlaceholder).Border(1, p.borderControl).
			Shadow(0, 12, 28, 0, ui.RGBA(0, 0, 0, 0.45))
		posterBox.Children(func() {
			applyPosterImage(c, posterBox, posterSrc)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(10).Children(func() {
			ui.Text(c, t.name).FontSize(30).Bold()
			if t.detail == nil {
				ui.Row(c).Wrap().Gap(10).AlignItems(ui.Center).Children(func() {
					a.favoriteButton(c)
				})
				return
			}
			a.detailHeroMeta(c, t)
			if t.detail.Content != "" {
				a.detailHeroSynopsis(c, t)
			}
			ui.Row(c).FillWidth().Wrap().Gap(10).AlignItems(ui.Center).Padding(2, 0, 0, 0).Children(func() {
				if ctaLabel, lineName, ep, at, ok := a.detailResumeCTA(t); ok {
					playingNow := a.running(jobPlay)
					shown := ctaLabel
					if playingNow {
						shown = "正在打开…"
					}
					cta := ui.ButtonBase(c.Key("detail-play-cta")).Height(38).Padding(0, 18).Gap(8).Radius(9).
						AlignItems(ui.Center).Cursor(ui.CursorPointer).Disabled(playingNow).Label(shown)
					bg := p.accent
					if playingNow {
						bg = ui.RGBA(255, 90, 64, 0.55)
					} else if cta.Hovered() {
						bg = p.accentHover
					}
					cta.Background(cta.AnimateColor("detail-cta-bg", bg, animColor))
					cta.Children(func() {
						if playingNow {
							loadingIndicator(c, 12, p.accentText)
						} else {
							ui.Icon(c, iconPlay).Size(13, 13).TextColor(p.accentText).PassThrough()
						}
						ui.Text(c, shown).FontSize(13.5).Bold().TextColor(p.accentText).SingleLine().ContentTransition(ctReplace).PassThrough()
					})
					if applyPressPose(cta).Clicked() {
						a.playEpisodeAt(lineName, ep, at)
					}
				}
				a.favoriteButton(c)
			})
			if a.title != t {
				return
			}
			a.detailResumeProgress(c, t)
		})
	})
}

func (a *app) detailHeroMeta(c *ui.Context, t *titlePage) {
	p := paletteFor(c)
	var parts []string
	appendPart := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, existing := range parts {
			if existing == s {
				return
			}
		}
		parts = append(parts, s)
	}
	appendPart(t.detail.Year)
	appendPart(t.detail.Type)
	appendPart(t.item.Video.Remarks)
	isPan := t.item.Entry.Config != nil && isPanSiteKind(t.item.Entry.Site.Kind)
	if t.score == "" && len(parts) == 0 && !isPan {
		return
	}
	ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
		if t.score != "" {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconStarFilled).Size(12, 12).TextColor(p.gold).PassThrough()
				ui.Text(c, "豆瓣 "+t.score).FontSize(13).Bold().TextColor(p.gold).SingleLine()
			})
		}
		if len(parts) > 0 {
			if t.score != "" {
				ui.Text(c, "·").FontSize(13).TextColor(p.textFaint).SingleLine()
			}
			ui.Text(c, strings.Join(parts, " · ")).FontSize(13).TextColor(p.textMuted)
		}
		if isPan {
			specMicroBadge(c, "网盘", true)
		}
	})
}

func (a *app) detailHeroSynopsis(c *ui.Context, t *titlePage) {
	p := paletteFor(c)
	longSynopsis := len([]rune(t.detail.Content)) > 96
	if !longSynopsis {
		ui.Text(c, t.detail.Content).FontSize(13).LineHeight(1.55).TextColor(p.textMuted)
		return
	}
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		ui.Box(c.Key("detail-synopsis-wrap")).FillWidth().Clip().
			Transition(ui.ElementTransition{Animation: &animSurface, Height: true}).Children(func() {
			synopsis := ui.Text(c, t.detail.Content).FontSize(13).LineHeight(1.55).TextColor(p.textMuted)
			if !t.synopsisExpanded {
				synopsis.MaxLines(2)
			}
		})
		shown, a11y := "更多", "展开简介"
		if t.synopsisExpanded {
			shown, a11y = "收起", "收起简介"
		}
		toggle := ui.ButtonBase(c.Key("detail-synopsis-toggle")).Padding(2, 0).
			Cursor(ui.CursorPointer).AlignSelf(ui.Start).Label(a11y)
		fg := p.accentLightText
		if toggle.Hovered() {
			fg = p.accent
		}
		animFg := toggle.AnimateColor("syn-fg", fg, animColor)
		toggle.Children(func() {
			ui.Text(c, shown).FontSize(12.5).Bold().TextColor(animFg).SingleLine().ContentTransition(ctReplace).PassThrough()
		})
		if applyPressPose(toggle).Clicked() {
			t.synopsisExpanded = !t.synopsisExpanded
		}
	})
}

func (a *app) detailRelatedButton(c *ui.Context, count int) {
	p := paletteFor(c)
	label := fmt.Sprintf("相近结果 (%d)", count)
	btn := ui.ButtonBase(c.Key("detail-related")).Height(28).Padding(0, 10).Radius(8).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Shrink(0).Label(label)
	bg := p.surfaceSubtle
	border := p.border
	fg := p.textMuted
	if btn.Hovered() {
		bg = p.surfaceHover
		border = p.borderControl
		fg = p.text
	}
	animFg := animateButtonChrome(btn, "detail-related", bg, border, fg)
	btn.Children(func() {
		ui.Text(c, label+" ›").FontSize(12).FontFeatures("tnum").TextColor(animFg).SingleLine().ContentTransition(ctNumeric).PassThrough()
	})
	if applyPressPose(btn).Clicked() {
		a.showRelated()
	}
}

func (a *app) detailResumeProgress(c *ui.Context, t *titlePage) {
	h, found := a.history.findByTitle(t.name)
	if !found || (h.Position <= 0 && h.Duration <= 0) {
		return
	}
	p := paletteFor(c)
	pos := time.Duration(h.Position * float64(time.Second))
	dur := time.Duration(h.Duration * float64(time.Second))
	var parts []string
	switch {
	case h.finished():
		parts = append(parts, "已看完")
	case h.Duration > 0:
		parts = append(parts, fmt.Sprintf("已看 %s / %s", playerClock(pos), playerClock(dur)))
	default:
		parts = append(parts, "已看 "+playerClock(pos))
	}
	lineName := strings.TrimSpace(h.Line)
	if line, ok := t.currentLine(); ok && strings.TrimSpace(line.Name) != "" {
		lineName = strings.TrimSpace(line.Name)
	}
	if lineName != "" {
		parts = append(parts, lineName)
	}
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Padding(2, 0, 0, 0).Children(func() {
		if h.Duration > 0 {
			frac := min(max(h.Position/h.Duration, 0), 1)
			ui.Progress(c, frac).Width(200).Height(3).Radius(1.5).
				Background(p.divider).TextColor(p.accent).Label("观看进度")
		}
		ui.Text(c, strings.Join(parts, " · ")).FontSize(12).TextColor(p.textMuted).SingleLine()
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
	return t != nil && t.detail != nil && t.item.Entry.Config != nil && itemKey(t.item) == itemKey(item)
}

// currentFailed reports whether the source on show failed to load.
func (t *titlePage) currentFailed() bool {
	return t != nil && t.detail != nil && t.item.Entry.Config != nil && t.failed[itemKey(t.item)] != ""
}

// detailSourceChipState resolves the visual status of a candidate source chip.
// A current source that failed to load or failed playback uses
// sourceChipSelectedFailed so it never falsely reports a healthy dot.
func detailSourceChipState(t *titlePage, item library.Item, playFailure *episodeFailure) sourceChipState {
	key := itemKey(item)
	current := t.isCurrent(item)
	failed := t != nil && t.failed[key] != ""
	if current {
		if failed || playFailure != nil {
			return sourceChipSelectedFailed
		}
		return sourceChipSelected
	}
	if failed {
		return sourceChipFailed
	}
	return sourceChipNormal
}

func detailSourceFailBadge(msg string) string {
	if strings.Contains(msg, "超时") || strings.Contains(strings.ToLower(msg), "timeout") {
		return "超时"
	}
	return "失败"
}

// detailSourceStrip renders the left-clustered source header ("片源", count,
// background search / find-more-sources, related results) above the full-width
// horizontally scrollable strip of candidate sources.
func (a *app) detailSourceStrip(c *ui.Context, candidates []library.Item) {
	t, search := a.title, &a.library.search
	p := paletteFor(c)
	searching := search.running && titleKey(search.query) == titleKey(t.name)
	showRelated := titleKey(search.query) == titleKey(t.name) && len(search.groups) > 1 && len(candidates) > 0
	ui.Column(c).FillWidth().Gap(9).Children(func() {
		ui.Row(c).FillWidth().MinHeight(28).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "片源").FontSize(14).Bold().TextColor(p.text).SingleLine().Shrink(0)
			if len(candidates) > 0 {
				ui.Text(c, fmt.Sprintf("共 %d 个来源", len(candidates))).
					FontSize(12).FontFeatures("tnum").TextColor(p.textFaint).SingleLine().Shrink(0).ContentTransition(ctNumeric)
			}
			switch {
			case searching:
				stopBtn := ui.ButtonBase(c.Key("detail-search-status")).Height(28).Padding(0, 10).Gap(6).Radius(8).
					AlignItems(ui.Center).Cursor(ui.CursorPointer).Shrink(0).
					Tooltip("点击停止后台找源").Label("停止后台找源")
				bg := p.accentSoft
				if stopBtn.Hovered() {
					bg = ui.RGBA(255, 90, 64, 0.26)
				}
				stopBtn.Background(stopBtn.AnimateColor("detail-search-bg", bg, animColor)).Border(1, p.accentBorder)
				stopBtn.Children(func() {
					loadingIndicator(c, 11, p.accent)
					ui.Text(c, fmt.Sprintf("后台找源 %d/%d", search.progress.Completed, search.progress.Total)).
						FontSize(12).FontFeatures("tnum").TextColor(p.accentLightText).SingleLine().ContentTransition(ctNumeric).PassThrough()
					ui.Text(c, "· 停止").FontSize(11.5).TextColor(p.textMuted).SingleLine().PassThrough()
				})
				if applyPressPose(stopBtn).Clicked() {
					a.stopScan(search)
				}
			case !a.inStack(routeSearch) && a.library.root != nil && titleKey(search.query) != titleKey(t.name):
				findBtn := ui.ButtonBase(c.Key("detail-find-sources")).Height(28).Padding(0, 10).Gap(5).Radius(8).
					AlignItems(ui.Center).Cursor(ui.CursorPointer).Shrink(0).Label("查找其他来源")
				bg := p.surfaceSubtle
				border := p.border
				fg := p.textMuted
				if findBtn.Hovered() {
					bg = p.surfaceHover
					border = p.borderControl
					fg = p.text
				}
				animFg := animateButtonChrome(findBtn, "detail-find", bg, border, fg)
				findBtn.Children(func() {
					ui.Icon(c, librarySearchIcon).Size(12, 12).TextColor(animFg).PassThrough()
					ui.Text(c, "查找其他来源").FontSize(12).TextColor(animFg).SingleLine().PassThrough()
				})
				if applyPressPose(findBtn).Clicked() {
					a.startScan(search, t.name, false)
				}
			}
			if showRelated {
				a.detailRelatedButton(c, len(search.groups))
			}
		})
		if len(candidates) > 0 {
			ui.ScrollHorizontal(c.Key("detail-source-chips")).Label("片源列表").FillWidth().
				Padding(0, 0, 12, 0).Gap(8).AlignItems(ui.Center).ScrollbarStyle(appScrollbar).Children(func() {
				for _, item := range candidates {
					if a.title != t {
						return
					}
					key := itemKey(item)
					loading := a.running(jobTitle) && itemKey(t.pending) == key
					state := detailSourceChipState(t, item, a.playFailure)
					isPan := isPanSiteKind(item.Entry.Site.Kind)
					latencyText := ""
					tier := 0
					tip := item.Entry.Label()
					switch {
					case loading:
						latencyText = "读取中…"
					case state == sourceChipFailed || state == sourceChipSelectedFailed:
						msg := t.failed[key]
						if msg == "" && a.playFailure != nil && a.playFailure.err != nil {
							msg = a.playFailure.err.Error()
						}
						latencyText = detailSourceFailBadge(msg)
						if msg != "" {
							tip = msg
						}
					default:
						if d, ok := a.latency.get(item.Entry.Key()); ok {
							latencyText = formatLatency(d)
							tier = latencyTier(d.Milliseconds())
						}
					}
					chipEl := sourceStatusChip(c, "src-"+key, item.Entry.Label(), isPan, latencyText, tier, state).
						Label("来源 " + item.Entry.Label()).Tooltip(tip)
					if chipEl.Clicked() {
						t.auto = false
						a.openCandidate(item)
					}
				}
			})
		}
		if a.title != t {
			return
		}
		if t.currentFailed() && a.playFailure == nil {
			failHint := "当前来源播不了，换一个试试"
			if len(candidates) <= 1 {
				failHint = "当前来源播不了，可查找其他来源"
			}
			if reason := t.failed[itemKey(t.item)]; reason != "" {
				failHint += " · " + reason
			}
			ui.Text(c.Key("detail-cur-failed"), failHint).FontSize(12.5).TextColor(p.dangerLightText).Transition(transInlineBanner)
		}
		if len(candidates) == 0 && !searching {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "片库里没有找到《"+t.name+"》。").FontSize(13).TextColor(p.textMuted).Grow(1)
				if len(search.groups) > 0 && primaryButton(c, "查看相近结果").Clicked() {
					a.showRelated()
				}
			})
		}
	})
}

func (a *app) detailFailureBanner(c *ui.Context, f *episodeFailure) {
	p := paletteFor(c)
	ui.Row(c.Key("detail-failure")).FillWidth().Padding(10, 14).Gap(12).AlignItems(ui.Center).Wrap().
		Radius(11).Background(p.dangerSoft).Border(1, p.dangerBorder).Transition(transInlineReveal).Children(func() {
		ui.Column(c).Grow(1).MinWidth(220).Gap(3).Children(func() {
			ui.Text(c, f.line+" · "+f.episode.Name).FontSize(13).Bold().TextColor(p.dangerLightText)
			ui.Text(c, f.err.Error()).FontSize(12.5).Selectable().TextColor(p.textMuted)
		})
		a.recoveryActions(c)
	})
}

func isGenericLineName(name string) bool {
	s := strings.TrimSpace(name)
	return s == "" || s == "线路" || s == "默认" || s == "默认线路"
}

func detailEpisodeColumns(c *ui.Context) int {
	winW, _ := c.Size()
	if winW <= 0 {
		return 10
	}
	sideW, _ := activeSidebarWidth(winW)
	contentW := winW - sideW - 78
	switch {
	case contentW >= 760:
		return 10
	case contentW >= 560:
		return 8
	default:
		return 5
	}
}

func useEpisodeGrid(episodes []catalog.Episode) bool {
	if len(episodes) == 0 {
		return false
	}
	longCount := 0
	for _, ep := range episodes {
		if len([]rune(episodeButtonLabel(ep.Name))) > 6 {
			longCount++
		}
	}
	return longCount*3 < len(episodes)
}

func (a *app) detailPlaybackSection(c *ui.Context) {
	t := a.title
	if t == nil || t.detail == nil {
		return
	}
	p := paletteFor(c)
	if len(t.detail.Sources) == 0 {
		ui.Text(c, "这个站点没有提供可播放分集，换一个来源试试。").FontSize(13).TextColor(p.textMuted)
		return
	}
	line, ok := t.currentLine()
	if !ok {
		return
	}
	count := len(line.Episodes)
	if t.chunk*episodeChunk >= count {
		t.chunk = 0
	}
	showLines := len(t.detail.Sources) > 1 || !isGenericLineName(line.Name)
	showChunks := count > episodeChunk
	if showLines || showChunks {
		ui.Column(c).FillWidth().Gap(0).Children(func() {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				if showLines {
					ui.ScrollHorizontal(c.Key("detail-line-tabs")).Grow(1).MinWidth(0).Gap(18).Scrollbars(false).Children(func() {
						for i, src := range t.detail.Sources {
							name := strings.TrimSpace(src.Name)
							if name == "" {
								name = fmt.Sprintf("线路 %d", i+1)
							}
							if underlineTab(c, "detail-line", fmt.Sprintf("line-%d", i), name, i == t.line).Clicked() {
								t.line, t.chunk = i, 0
								a.playFailure = nil
							}
						}
					})
				} else {
					ui.Spacer(c)
				}
				if showChunks {
					ui.Row(c).Gap(14).AlignItems(ui.Center).Shrink(0).Children(func() {
						for i := 0; i*episodeChunk < count; i++ {
							label := fmt.Sprintf("%d–%d", i*episodeChunk+1, min((i+1)*episodeChunk, count))
							if underlineTab(c, "detail-chunk", fmt.Sprintf("chunk-%d", i), label, i == t.chunk).Clicked() {
								t.chunk = i
							}
						}
					})
				} else if count > 1 {
					ui.Text(c, fmt.Sprintf("共 %d 集", count)).FontSize(12).FontFeatures("tnum").TextColor(p.textFaint).
						SingleLine().Shrink(0).ContentTransition(ctNumeric)
				}
			})
		})
	}
	// Re-fetch current line in case clicking a line tab changed t.line.
	line, ok = t.currentLine()
	if !ok {
		return
	}
	count = len(line.Episodes)
	if count == 0 {
		ui.Text(c, "这条线路暂无可播放分集，换一条线路试试。").FontSize(13).TextColor(p.textMuted)
		return
	}
	if t.chunk*episodeChunk >= count {
		t.chunk = 0
	}
	activeIdx, activeFrac := a.detailActiveEpisode(t, line)
	start := t.chunk * episodeChunk
	page := line.Episodes[start:min(start+episodeChunk, count)]
	gridKey := fmt.Sprintf("episodes-%d-%d", t.line, t.chunk)
	if count > 1 && useEpisodeGrid(page) {
		cols := detailEpisodeColumns(c)
		ui.Grid(c.Key(gridKey)).FillWidth().Columns(cols).Gap(8).Children(func() {
			for j, episode := range page {
				globalIdx := start + j
				active := globalIdx == activeIdx
				watched := activeIdx > 0 && globalIdx < activeIdx
				frac := 0.0
				if active {
					frac = activeFrac
				}
				if a.detailEpisodeButton(c, j, episode, active, watched, frac, true, false).Clicked() {
					a.playEpisode(line.Name, episode)
				}
			}
		})
		return
	}
	ui.Row(c.Key(gridKey)).FillWidth().Wrap().Gap(8).Children(func() {
		for j, episode := range page {
			globalIdx := start + j
			active := globalIdx == activeIdx
			watched := activeIdx > 0 && globalIdx < activeIdx
			frac := 0.0
			if active {
				frac = activeFrac
			}
			if a.detailEpisodeButton(c, j, episode, active, watched, frac, false, count == 1).Clicked() {
				a.playEpisode(line.Name, episode)
			}
		}
	})
}

func (a *app) detailActiveEpisode(t *titlePage, line catalog.Source) (int, float64) {
	targetName := ""
	var frac float64
	if line.Name == a.playingSource && a.playingEpisode != "" {
		targetName = a.playingEpisode
	} else if h, found := a.history.findByTitle(t.name); found {
		if _, target, _, ok := resumePoint(*t.detail, h); ok {
			targetName = target.Name
			if !h.finished() && h.Duration > 0 && h.Position > 0 && (target.Name == h.Episode || episodeButtonLabel(target.Name) == episodeButtonLabel(h.Episode)) {
				frac = min(max(h.Position/h.Duration, 0), 1)
			}
		} else {
			targetName = h.Episode
			if !h.finished() && h.Duration > 0 && h.Position > 0 {
				frac = min(max(h.Position/h.Duration, 0), 1)
			}
		}
	}
	if targetName == "" {
		return -1, 0
	}
	targetLabel := episodeButtonLabel(targetName)
	for i, ep := range line.Episodes {
		if ep.Name == targetName || episodeButtonLabel(ep.Name) == targetLabel {
			return i, frac
		}
	}
	return -1, 0
}

func (a *app) detailEpisodeButton(c *ui.Context, key int, episode catalog.Episode, active, watched bool, progress float64, gridCell, single bool) ui.Element {
	p := paletteFor(c)
	label := episodeButtonLabel(episode.Name)
	if single {
		raw := strings.TrimSpace(episode.Name)
		if raw == "" || raw == "第1集" || raw == "1" {
			label = "正片"
		} else {
			label = raw
		}
	}
	btn := ui.ButtonBase(c.Key(key)).Height(38).Radius(8).Center().Cursor(ui.CursorPointer).Label(label)
	padX := float32(0)
	if gridCell {
		padX = 4
		btn.FillWidth().Padding(0, padX)
	} else if single {
		padX = 16
		btn.MinWidth(88).Padding(0, padX)
	} else {
		padX = 12
		btn.MinWidth(64).Padding(0, padX)
	}
	if episode.Name != "" && episode.Name != label {
		btn.Tooltip(episode.Name)
	}
	hovered := btn.Hovered()
	bg := p.surfaceInner
	border := p.border
	fg := p.text
	if active {
		bg = p.accent
		border = p.accent
		fg = p.accentText
		if hovered {
			bg = p.accentHover
			border = p.accentHover
		}
	} else {
		if watched {
			fg = p.textFaint
		}
		if hovered {
			bg = p.controlHover
			border = p.borderControl
			fg = p.text
		}
	}
	animFg := animateButtonChrome(btn, "ep", bg, border, fg)
	innerW := max(float32(0), btn.Bounds().W-2*padX-2)
	btn.Children(func() {
		a.hoverMarqueeText(c, marqueeTextOptions{
			key:        fmt.Sprintf("detail-episode-%d-%s", key, label),
			text:       label,
			fontSize:   13,
			bold:       active,
			color:      animFg,
			hovered:    hovered,
			centered:   true,
			fillHeight: true,
			fixedWidth: func() float32 {
				if !gridCell {
					return innerW
				}
				return 0
			}(),
			knownWidth: innerW,
		})
		if active {
			if progress > 0 {
				ui.Progress(c, progress).Absolute().Left(5).Right(5).Bottom(3).Height(2).Radius(1).
					Background(ui.RGBA(255, 255, 255, 0.30)).TextColor(p.accentText).PassThrough()
			} else {
				ui.Box(c).Absolute().Left(8).Right(8).Bottom(3).Height(2).Radius(1).
					Background(ui.RGBA(255, 255, 255, 0.65)).PassThrough()
			}
		}
	})
	return applyPressPose(btn)
}
