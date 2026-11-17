package main

import "github.com/egoist/mygo/ui"

const continueWatchingLimit = 6

// Keep the history's newest-first order while hiding dismissed shortcuts.
// The full history remains available even when a shortcut is dismissed.
func continueWatchingEntries(history []historyEntry) []historyEntry {
	entries := make([]historyEntry, 0, continueWatchingLimit)
	for _, e := range history {
		if e.HomeHidden || titleKey(e.Name) == "" {
			continue
		}
		entries = append(entries, e)
		if len(entries) == continueWatchingLimit {
			break
		}
	}
	return entries
}

func (h *historyStore) setContinueHidden(name string, hidden bool) bool {
	for i := range h.entries {
		if titleKey(h.entries[i].Name) == titleKey(name) && h.entries[i].HomeHidden != hidden {
			h.entries[i].HomeHidden = hidden
			h.dirty = true
			return true
		}
	}
	return false
}

func (a *app) dismissContinueWatching(name string) {
	if a.history.setContinueHidden(name, true) {
		a.flushHistory()
	}
}

// Starting this title again restores its home shortcut. Periodic history
// recording alone does not undo a dismissal.
func (a *app) revealContinueWatching(name string) {
	if a.history.setContinueHidden(name, false) {
		a.flushHistory()
	}
}

func (a *app) continueWatchingStrip(c *ui.Context) {
	entries := continueWatchingEntries(a.history.entries)
	if len(entries) == 0 {
		return
	}
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
				ui.Row(c).Key("continue-" + titleKey(e.Name)).Width(268).Padding(10).Gap(10).
					Radius(14).Background(surface).Children(func() {
					poster := ui.Box(c).Size(60, 90).Radius(8).Clip().Background(ui.Hex("#24242c")).
						Cursor(ui.CursorPointer).Label("继续观看 " + e.Name).Disabled(!available)
					poster.Children(func() {
						if image := a.poster(e.Pic); image != nil {
							ui.Image(c, image).Fill().Fit(ui.Cover)
						}
					})
					ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
						info := ui.Column(c).FillWidth().Gap(5).Cursor(ui.CursorPointer).
							Label("续播 " + e.Name).Disabled(!available)
						info.Children(func() {
							ui.Text(c, e.Name).FontSize(13).Bold().SingleLine()
							ui.Text(c, e.progress()).FontSize(11).TextColor(textMuted).SingleLine()
							progress := float64(0)
							if e.Duration > 0 {
								progress = max(0, min(e.Position/e.Duration, 1))
							}
							ui.Progress(c, progress).FillWidth().Label("观看进度 " + e.Name)
							if !available {
								ui.Text(c, "片源已移除").FontSize(11).TextColor(textMuted)
							}
						})
						if info.Clicked() {
							a.resume(e)
						}
						if glassButton(c, "从这里移除").AlignSelf(ui.Start).Label("从继续观看移除 " + e.Name).Clicked() {
							a.dismissContinueWatching(e.Name)
						}
					})
					if poster.Clicked() {
						a.resume(e)
					}
				})
			}
		})
	})
}
