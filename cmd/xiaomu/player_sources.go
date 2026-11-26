package main

import (
	"context"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
	"github.com/arixbit/xiaomu-desktop/internal/source"
)

// A saved entry opens its recorded episode first. Discover the other sites
// independently so the viewer can switch without leaving playback.
func (a *app) savedEpisodeResult(t *titlePage, play catalog.Playback, episode catalog.Episode, at time.Duration) jobResult {
	return jobResult{discard: t.session.Close, apply: func() {
		a.clearTitle()
		a.title = t
		t.failed = map[string]string{}
		t.seeds = a.candidates() // keep already known sources available while the others respond
		a.startEpisode(play, t.detail.Sources[t.line].Name, episode, at)
		if a.player == nil && a.route() != routeDetail {
			a.clearTitle()
			return
		}
		if key := titleKey(t.name); a.playFailure == nil && key != "" && a.library.root != nil {
			t.sourceScan = &libraryScan{exactTitle: key}
			a.startScan(t.sourceScan, t.name, false)
		}
	}}
}

func (a *app) titleSearch() *libraryScan {
	if t := a.title; t != nil && t.sourceScan != nil {
		return t.sourceScan
	}
	return &a.library.search
}

// The chooser owns an alternate site's session while the current player,
// history and recovery controls still belong to the original site.
type playerSourceChoice struct {
	item    library.Item
	session source.Session
	detail  *catalog.Detail
	line    string
	episode catalog.Episode
	message string
	failed  bool
	retried bool
}

func (a *app) clearPanelChoice() {
	if choice := a.panelChoice; choice != nil {
		a.closeSession(choice.session)
		a.panelChoice = nil
		a.panelSource = -1
	}
}

// singleMovieMatch allows switching directly between single-episode movie lines
// whose labels differ across providers (e.g. cloud-drive "电影.2026.2160p.4K"
// vs CMS "HD1080P" / "正片"), while never guessing for multi-episode series or
// numbered episodes.
func (a *app) singleMovieMatch(currentLine int, requested catalog.Episode, target catalog.Source) (catalog.Episode, bool) {
	if a.title == nil || a.title.detail == nil || currentLine < 0 || currentLine >= len(a.title.detail.Sources) {
		return catalog.Episode{}, false
	}
	if len(a.title.detail.Sources[currentLine].Episodes) != 1 || len(target.Episodes) != 1 {
		return catalog.Episode{}, false
	}
	candidate := target.Episodes[0]
	if episodeMatchNumber(requested.Name) != 0 || episodeMatchNumber(candidate.Name) != 0 {
		return catalog.Episode{}, false
	}
	return candidate, true
}

func (a *app) switchSource(item library.Item) {
	a.switchSourceInternal(item, false)
}

func (a *app) switchSourceInternal(item library.Item, isRetry bool) {
	t := a.title
	if t == nil {
		return
	}
	prevMessage := ""
	if isRetry && a.panelChoice != nil && sourceMemoryItemKey(a.panelChoice.item) == sourceMemoryItemKey(item) {
		prevMessage = a.panelChoice.message
	}
	a.cancelJob(jobPlay)
	a.clearPanelChoice()
	if t.isCurrent(item) {
		a.status = ""
		return
	}
	a.cancelJob(jobTitle)
	msg := "正在读取线路…"
	if prevMessage != "" {
		msg = prevMessage
	}
	choice := &playerSourceChoice{item: item, message: msg}
	a.panelChoice = choice
	assets := strings.TrimSpace(a.browser.assets)
	a.run(jobPlay, "读取来源", func(ctx context.Context) (jobResult, error) {
		start := time.Now()
		session, detail, err := openItem(ctx, item, assets)
		elapsed := time.Since(start)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{discard: session.Close, apply: func() {
			if a.title != t || a.panelChoice != choice {
				session.Close()
				return
			}
			choice.session, choice.detail = session, &detail
			choice.failed = false
			a.recordLatency(item.Entry.Key(), elapsed)
			a.panelSource = a.preferredLine(t.name, item, detail)
			if len(detail.Sources) == 0 {
				a.failureUI.retrying = false
				a.failureUI.retryJobGen = 0
				a.failureUI.copySeq++
				a.failureUI.copied = false
				a.failureUI.copyFailed = false
				choice.failed = true
				choice.retried = isRetry
				choice.message = "这个来源没有提供可播放线路"
				if a.playFailure != nil {
					a.showPlayerOSD("切换失败：" + item.Entry.Label())
				} else {
					a.showPlayerOSD("该来源无可播放线路")
				}
				return
			}
			curLine, _, requested, at, ok := a.recoveryEpisode()
			if ok {
				line, ep, found := rememberedEpisode(detail, detail.Sources[a.panelSource].Name, requested.Name)
				if !found {
					if singleEp, matched := a.singleMovieMatch(curLine, requested, detail.Sources[a.panelSource]); matched {
						line, ep, found = a.panelSource, singleEp, true
					}
				}
				if found {
					a.panelSource = line
					a.playPanelEpisodeInternal(line, ep, at, isRetry)
					return
				}
			}
			a.failureUI.retrying = false
			a.failureUI.retryJobGen = 0
			choice.failed = false
			choice.message = "请选择要播放的分集"
			a.showPlayerOSD("未匹配当前分集，请在右侧选集")
		}}, nil
	}, func(err error) {
		if a.panelChoice != choice {
			return
		}
		a.failureUI.retrying = false
		a.failureUI.retryJobGen = 0
		a.failureUI.copySeq++
		a.failureUI.copied = false
		a.failureUI.copyFailed = false
		choice.failed = true
		choice.retried = isRetry
		choice.message = jobError("读取来源", err)
		a.status = ""
		if a.playFailure != nil {
			a.showPlayerOSD("切换失败：" + item.Entry.Label())
		} else {
			a.showPlayerOSD("切换来源失败，继续播放原视频")
		}
		_ = a.sourceMemory.record(t.name, item, "", 0, false, time.Now())
	})
}

func (a *app) selectPanelLine(detail *catalog.Detail, line int) {
	a.panelSource = line
	curLine, index, requested, at, ok := a.recoveryEpisode()
	if !ok {
		return
	}
	if a.panelChoice == nil && a.playFailure == nil && curLine == line {
		// Clicking back to the line already playing should cancel any pending
		// switch attempt without restarting the active stream.
		a.cancelJob(jobPlay)
		return
	}
	if a.panelChoice != nil {
		index = -1 // episode order can differ between sites
	}
	ep, found := catalog.Episode{}, false
	if _, matchedEp, ok := matchEpisode(catalog.Detail{Sources: detail.Sources[line : line+1]}, requested.Name, index); ok {
		ep, found = matchedEp, true
	} else if singleEp, ok := a.singleMovieMatch(curLine, requested, detail.Sources[line]); ok {
		ep, found = singleEp, true
	}
	if found {
		a.playPanelEpisode(line, ep, at)
		return
	}
	a.cancelJob(jobPlay)
	if a.panelChoice != nil {
		a.status = ""
		a.panelChoice.failed = false
		a.panelChoice.message = "请选择要播放的分集"
		a.showPlayerOSD("该线路无对应分集，请在右侧选集")
	} else {
		a.showPlayerOSD("该线路无对应分集，请在右侧选集")
	}
}

func (a *app) playPanelEpisode(line int, episode catalog.Episode, at time.Duration) {
	a.playPanelEpisodeInternal(line, episode, at, false)
}

func (a *app) playPanelEpisodeInternal(line int, episode catalog.Episode, at time.Duration, isRetry bool) {
	choice := a.panelChoice
	if choice == nil {
		a.playEpisodeAt(a.title.detail.Sources[line].Name, episode, at)
		return
	}
	name := choice.detail.Sources[line].Name
	resolver := resolve.Resolver{Sniff: a.sniffPage, Parsers: entryParsers(choice.item.Entry)}
	choice.line = name
	choice.episode = episode
	if !isRetry {
		choice.failed = false
		choice.retried = false
		choice.message = "正在解析分集…"
	}
	start := time.Now()
	fail := func(message string) {
		if a.panelChoice != choice {
			return
		}
		a.failureUI.retrying = false
		a.failureUI.retryJobGen = 0
		a.failureUI.copySeq++
		a.failureUI.copied = false
		a.failureUI.copyFailed = false
		choice.failed = true
		choice.retried = isRetry
		choice.message, a.status = message, ""
		if a.playFailure != nil {
			route := formatRouteLabel(choice.item.Entry, name, choice.detail != nil && len(choice.detail.Sources) > 1)
			a.showPlayerOSD("切换失败：" + route)
		} else {
			a.showPlayerOSD("切换失败，继续播放原视频")
		}
		if a.title != nil {
			_ = a.sourceMemory.record(a.title.name, choice.item, name, time.Since(start), false, time.Now())
		}
	}
	a.run(jobPlay, "切换来源", func(ctx context.Context) (jobResult, error) {
		play, err := resolvePlay(ctx, choice.session, resolver, name, episode)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if a.title == nil || a.panelChoice != choice {
				return
			}
			a.flushHistory()
			if !a.openEpisodeMedia(play) {
				fail(a.status)
				return
			}
			t := a.title
			a.closeSession(t.session)
			t.session, t.item, t.auto = choice.session, choice.item, false
			t.applyDetail(*choice.detail)
			t.line, t.chunk = line, 0
			delete(t.failed, itemKey(t.item))
			a.panelChoice = nil // session ownership moved to the title
			a.panelSource = line
			a.beginSourceAttempt(t.item, name, episode.Name)
			a.sourceAttempt.started = start
			a.notePlayingEpisode(name, episode, at)
			switched := t.item.Entry.Label()
			if name != "" {
				if switched != "" {
					switched += " · " + name
				} else {
					switched = name
				}
			}
			if switched != "" {
				a.showPlayerOSD("已切换：" + switched)
			}
		}}, nil
	}, func(err error) {
		fail(jobError("解析分集", err))
	})
}
