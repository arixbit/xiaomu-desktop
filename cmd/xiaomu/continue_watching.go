package main

import "github.com/egoist/mygo/ui"

const continueWatchingLimit = 6

// Keep the history's newest-first order while hiding dismissed and finished items.
// The full history remains available even when a shortcut is dismissed.
func continueWatchingEntries(history []historyEntry) []historyEntry {
	entries := make([]historyEntry, 0, continueWatchingLimit)
	for _, e := range history {
		if e.isNAS() || e.HomeHidden || e.finished() || e.dedupKey() == "" {
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
	if key == "" {
		return
	}
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
	a.revealContinueWatchingKey(titleKey(name))
}

func (a *app) revealContinueWatchingKey(key string) {
	a.setContinueWatchingHidden(key, false)
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
				card := ui.Box(c).Key("continue-" + e.dedupKey()).Width(268)
				applySurface(card, p, 14).Children(func() {
					ui.Row(c).FillWidth().Padding(10).Gap(10).AlignItems(ui.Center).Children(func() {
						poster := ui.Box(c).Size(60, 90).Radius(8).Clip().Background(p.cardPlaceholder).
							Cursor(ui.CursorPointer).Label("继续观看 " + e.Name).Disabled(!available)
						poster.Children(func() {
							applyPosterImage(c, poster, a.poster(c, e.Pic))
						})
						info := ui.Column(c).Grow(1).MinWidth(0).Gap(5).Padding(0, 22, 0, 0).Cursor(ui.CursorPointer).
							Label("续播 " + e.Name).Disabled(!available)
						info.Children(func() {
							ui.Text(c, e.Name).FontSize(13).Bold().SingleLine()
							ui.Text(c, e.progress()).FontSize(11).TextColor(p.textMuted).SingleLine()
							if e.Duration > 0 {
								progress := max(0, min(e.Position/e.Duration, 1))
								ui.Progress(c, progress).FillWidth().Label("观看进度 " + e.Name)
							}
							if !available {
								missingLabel := "片源已移除"
								if e.isLocal() {
									missingLabel = "文件已移除"
								}
								ui.Text(c, missingLabel).FontSize(11).TextColor(p.textMuted)
							}
						})
						if info.Clicked() {
							a.resume(e)
						}
						if poster.Clicked() {
							a.resume(e)
						}
					})
					dismissBtn := ui.ButtonBase(c.Key("dismiss-"+e.dedupKey())).
						Absolute().Top(6).Right(6).Size(24, 24).Radius(12).Center().
						Cursor(ui.CursorPointer).
						Label("从继续观看移除 " + e.Name).Tooltip("从继续观看隐藏")
					dismissBg := ui.Transparent
					dismissFg := p.textMuted
					if dismissBtn.Hovered() {
						dismissBg = p.surfaceHover
						dismissFg = p.text
					}
					dismissBtn.Background(dismissBtn.AnimateColor("dismiss-bg", dismissBg, animColor))
					applyPressPose(dismissBtn)
					dismissBtn.Children(func() {
						ui.Icon(c, iconClose).Size(13, 13).TextColor(dismissBtn.AnimateColor("dismiss-fg", dismissFg, animColor)).PassThrough()
					})
					if dismissBtn.Clicked() {
						key := e.dedupKey()
						name := e.Name
						a.setContinueWatchingHidden(key, true)
						a.setUndoNotice("已隐藏「"+name+"」", "撤销", func() {
							a.setContinueWatchingHidden(key, false)
						})
					}
				})
				applyCardPose(card)
			}
		})
	})
}
