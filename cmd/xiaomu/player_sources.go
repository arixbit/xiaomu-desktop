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

// The chooser owns an alternate site's session while the current player,
// history and recovery controls still belong to the original site.
type playerSourceChoice struct {
	item    library.Item
	session source.Session
	detail  *catalog.Detail
	message string
}

func (a *app) clearPanelChoice() {
	if choice := a.panelChoice; choice != nil {
		a.closeSession(choice.session)
		a.panelChoice = nil
		a.panelSource = -1
	}
}

func (a *app) switchSource(item library.Item) {
	t := a.title
	if t == nil {
		return
	}
	a.cancelJob(jobPlay)
	a.clearPanelChoice()
	if t.isCurrent(item) {
		a.status = ""
		return
	}
	a.cancelJob(jobTitle)
	choice := &playerSourceChoice{item: item, message: "正在读取线路…"}
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
			choice.session, choice.detail = session, &detail
			a.recordLatency(item.Entry.Key(), elapsed)
			a.panelSource = a.preferredLine(t.name, item, detail)
			if len(detail.Sources) == 0 {
				choice.message = "这个来源没有提供可播放线路"
				return
			}
			_, _, requested, at, ok := a.recoveryEpisode()
			if ok {
				line, ep, found := rememberedEpisode(detail, detail.Sources[a.panelSource].Name, requested.Name)
				if found {
					a.panelSource = line
					a.playPanelEpisode(line, ep, at)
					return
				}
			}
			choice.message = "请选择要播放的分集"
		}}, nil
	}, func(err error) {
		choice.message = jobError("读取来源", err)
		a.status = ""
		_ = a.sourceMemory.record(t.name, item, "", 0, false, time.Now())
	})
}

func (a *app) selectPanelLine(detail *catalog.Detail, line int) {
	a.panelSource = line
	_, index, requested, at, ok := a.recoveryEpisode()
	if !ok {
		return
	}
	if a.panelChoice != nil {
		index = -1 // episode order can differ between sites
	}
	if _, ep, found := matchEpisode(catalog.Detail{Sources: detail.Sources[line : line+1]}, requested.Name, index); found {
		a.playPanelEpisode(line, ep, at)
	} else if a.panelChoice != nil {
		a.cancelJob(jobPlay)
		a.status = ""
		a.panelChoice.message = "请选择要播放的分集"
	}
}

func (a *app) playPanelEpisode(line int, episode catalog.Episode, at time.Duration) {
	choice := a.panelChoice
	if choice == nil {
		a.playEpisodeAt(a.title.detail.Sources[line].Name, episode, at)
		return
	}
	name := choice.detail.Sources[line].Name
	resolver := resolve.Resolver{Sniff: a.sniffPage, Parsers: entryParsers(choice.item.Entry)}
	choice.message = "正在解析分集…"
	start := time.Now()
	fail := func(message string) {
		choice.message, a.status = message, ""
		_ = a.sourceMemory.record(a.title.name, choice.item, name, time.Since(start), false, time.Now())
	}
	a.run(jobPlay, "切换来源", func(ctx context.Context) (jobResult, error) {
		play, err := resolvePlay(ctx, choice.session, resolver, name, episode)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
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
		}}, nil
	}, func(err error) {
		fail(jobError("解析分集", err))
	})
}
