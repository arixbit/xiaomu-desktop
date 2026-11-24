package main

import "github.com/egoist/mygo/ui"

const continueWatchingLimit = 6

// Keep the history's newest-first order while hiding dismissed and finished items.
// The full history remains available even when a shortcut is dismissed.
func continueWatchingEntries(history []historyEntry) []historyEntry {
	entries := make([]historyEntry, 0, continueWatchingLimit)
	for _, e := range history {
		if e.isNAS() || e.HomeHidden || e.finished() || titleKey(e.Name) == "" {
			continue
		}
		entries = append(entries, e)
		if len(entries) == continueWatchingLimit {
			break
		}
	}
	return entries
}

func (a *app) setContinueWatchingHidden(key string, hidden bool) {
	for i := range a.history.entries {
		entry := &a.history.entries[i]
		if entry.dedupKey() == key && entry.HomeHidden != hidden {
			entry.HomeHidden = hidden
			a.history.dirty = true
			a.flushHistory()
			return
		}
	}
}

// Starting this title again restores its home shortcut. Periodic history
// recording alone does not undo a dismissal.
func (a *app) revealContinueWatching(name string) {
	a.setContinueWatchingHidden(titleKey(name), false)
}

func (a *app) continueWatchingStrip(c *ui.Context) {
	entries := continueWatchingEntries(a.history.entries)
	if len(entries) == 0 {
		return
	}
	p := paletteFor(c)
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "继续观看").FontSize(16).Bold()
			ui.Spacer(c)
			if glassButton(c, "播放历史").Clicked() {
				a.switchTab(routeHistory)
			}
		})
		ui.ScrollHorizontal(c).FillWidth().Gap(12).Children(func() {
			for _, e := range entries {
				available := a.historyAvailable(e)
				card := ui.Row(c).Key("continue-" + e.dedupKey()).Width(268).Padding(10).Gap(10)
				applySurface(card, p, 14).Children(func() {
					poster := ui.Box(c).Size(60, 90).Radius(8).Clip().Background(p.cardPlaceholder).
						Cursor(ui.CursorPointer).Label("继续观看 " + e.Name).Disabled(!available)
					poster.Children(func() {
						applyPosterImage(c, poster, a.poster(e.Pic))
					})
					ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
						info := ui.Column(c).FillWidth().Gap(5).Cursor(ui.CursorPointer).
							Label("续播 " + e.Name).Disabled(!available)
						info.Children(func() {
							ui.Text(c, e.Name).FontSize(13).Bold().SingleLine()
							ui.Text(c, e.progress()).FontSize(11).TextColor(p.textMuted).SingleLine()
							if e.Duration > 0 {
								progress := max(0, min(e.Position/e.Duration, 1))
								ui.Progress(c, progress).FillWidth().Label("观看进度 " + e.Name)
							}
							if !available {
								ui.Text(c, "片源已移除").FontSize(11).TextColor(p.textMuted)
							}
						})
						if info.Clicked() {
							a.resume(e)
						}
						if glassButton(c, "隐藏").Padding(4, 10).AlignSelf(ui.Start).Label("从继续观看移除 " + e.Name).Clicked() {
							key := e.dedupKey()
							name := e.Name
							a.setContinueWatchingHidden(key, true)
							a.setUndoNotice("已隐藏「"+name+"」", "撤销", func() {
								a.setContinueWatchingHidden(key, false)
							})
						}
					})
					if poster.Clicked() {
						a.resume(e)
					}
				})
				applyCardPose(card)
			}
		})
	})
}
