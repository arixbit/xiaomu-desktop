package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

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

func (a *app) openHistoryDetail(e historyEntry) {
	if e.isLocal() {
		return
	}
	a.openFavorite(favoriteEntry{
		Name:      e.Name,
		Pic:       e.Pic,
		ConfigURL: e.ConfigURL,
		EntryKey:  e.EntryKey,
		SiteKey:   e.SiteKey,
		VideoID:   e.VideoID,
	})
}

func continueWatchingSubline(e historyEntry, fav favoriteEntry, followed bool) string {
	var parts []string
	if e.Duration > 0 && e.Position < e.Duration {
		rem := time.Duration((e.Duration - e.Position) * float64(time.Second))
		if mins := int(rem.Minutes()); mins >= 1 {
			parts = append(parts, fmt.Sprintf("剩余 %d 分钟", mins))
		} else {
			parts = append(parts, "即将看完")
		}
	}
	if followed && fav.LatestEpisode != "" && fav.EpisodeCount > 0 {
		parts = append(parts, "更新至 "+fav.LatestEpisode)
	}
	return strings.Join(parts, " · ")
}

func (a *app) continueWatchingStrip(c *ui.Context) {
	entries := continueWatchingEntries(a.history.entries)
	if len(entries) == 0 {
		return
	}
	a.homeContinueWatchingSection(c, entries)
}

func (a *app) homeContinueWatchingSection(c *ui.Context, entries []historyEntry) {
	if len(entries) == 0 {
		return
	}
	if a.homeResumeIdx < 0 || a.homeResumeIdx >= len(entries) {
		a.homeResumeIdx = 0
	}
	e := entries[a.homeResumeIdx]
	available := a.historyAvailable(e)
	fav, followed := a.findFavorite(e.Name)
	p := paletteFor(c)

	ui.Column(c).Key("home-cw-section").FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Height(28).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "继续观看").FontSize(15).Bold().TextColor(p.text).SingleLine().Shrink(0)
			if len(entries) > 1 {
				switcher := ui.Row(c).Key("cw-switcher").Height(24).Padding(0, 4).Gap(2).Radius(12).
					Background(p.surface).Border(1, p.border).AlignItems(ui.Center).Shrink(0)
				switcher.Children(func() {
					prevBtn := ui.ButtonBase(c.Key("cw-prev")).Size(20, 20).Radius(10).Center().
						Cursor(ui.CursorPointer).Label("上一部继续观看").Tooltip("上一部")
					prevBg, prevFg := ui.Transparent, p.textMuted
					if prevBtn.Hovered() {
						prevBg, prevFg = p.surfaceHover, p.text
					}
					prevBtn.Background(prevBtn.AnimateColor("cw-prev-bg", prevBg, animColor))
					animPrevFg := prevBtn.AnimateColor("cw-prev-fg", prevFg, animColor)
					applyPressPose(prevBtn)
					prevBtn.Children(func() {
						ui.Icon(c, iconBack).Size(11, 11).TextColor(animPrevFg).PassThrough()
					})
					if prevBtn.Clicked() {
						a.homeResumeDir = -1
						a.homeResumeIdx = (a.homeResumeIdx - 1 + len(entries)) % len(entries)
					}

					ui.Text(c, fmt.Sprintf("%d/%d", a.homeResumeIdx+1, len(entries))).
						FontSize(11.5).TextColor(p.textMuted).FontFeatures("tnum").
						Padding(0, 3).SingleLine().ContentTransition(ctNumericDir(a.homeResumeDir < 0))

					nextBtn := ui.ButtonBase(c.Key("cw-next")).Size(20, 20).Radius(10).Center().
						Cursor(ui.CursorPointer).Label("下一部继续观看").Tooltip("下一部")
					nextBg, nextFg := ui.Transparent, p.textMuted
					if nextBtn.Hovered() {
						nextBg, nextFg = p.surfaceHover, p.text
					}
					nextBtn.Background(nextBtn.AnimateColor("cw-next-bg", nextBg, animColor))
					animNextFg := nextBtn.AnimateColor("cw-next-fg", nextFg, animColor)
					applyPressPose(nextBtn)
					nextBtn.Children(func() {
						ui.Icon(c, iconChevronRight).Size(11, 11).TextColor(animNextFg).PassThrough()
					})
					if nextBtn.Clicked() {
						a.homeResumeDir = 1
						a.homeResumeIdx = (a.homeResumeIdx + 1) % len(entries)
					}
				})
			}
			ui.Spacer(c)
			histLink := ui.ButtonBase(c.Key("cw-history-link")).Padding(3, 6).Radius(6).
				Cursor(ui.CursorPointer).Label("播放历史").Shrink(0)
			histFg := p.textMuted
			if histLink.Hovered() {
				histFg = p.text
			}
			animHistFg := histLink.AnimateColor("cw-hist-fg", histFg, animColor)
			applyPressPose(histLink)
			histLink.Children(func() {
				ui.Text(c, "播放历史 ›").FontSize(12).TextColor(animHistFg).SingleLine().PassThrough()
			})
			if histLink.Clicked() {
				a.switchTab(routeHistory)
			}
		})

		posterSrc := a.poster(c, e.Pic)
		cwTrans := transContinueCard(a.homeResumeDir)
		cardHost := ui.Box(c.Key("home-cw-card-host")).FillWidth().Clip()
		if cwTrans.Exit != nil {
			cardHost.ChildExit(*cwTrans.Exit)
		}
		cardHost.Children(func() {
			card := ui.Box(c).Key("continue-" + e.dedupKey()).FillWidth().MinHeight(192).Clip().Transition(cwTrans)
			applySurface(card, p, 13).Children(func() {
				if !p.reduceTransparency {
					ambient := ui.Box(c.Key("cw-ambient")).Absolute().Fill().ZIndex(-1).PassThrough()
					ambient.Children(func() {
						applyAmbientPoster(c, ambient, posterSrc, 0.16, 26, ui.LinearGradient{
							Angle: 90,
							Stops: []ui.GradientStop{
								{Color: ui.RGBA(0, 0, 0, 0.85), At: 0},
								{Color: ui.RGBA(0, 0, 0, 0.35), At: 0.45},
								{Color: ui.Transparent, At: 0.85},
							},
						})
					})
				}

				ui.Row(c).FillWidth().Padding(14).Gap(16).AlignItems(ui.Center).Children(func() {
					poster := ui.Box(c).Size(108, 162).Shrink(0).Radius(10).Clip().
						Background(p.cardPlaceholder).Border(1, p.border).
						Cursor(ui.CursorPointer).Label("继续观看 " + e.Name).Disabled(!available)
					poster.Children(func() {
						applyPosterImage(c, poster, posterSrc)
					})
					if poster.Clicked() {
						a.resume(e)
					}

					ui.Column(c).Grow(1).MinWidth(0).Gap(7).Padding(0, 26, 0, 0).Children(func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, e.Name).FontSize(18).Bold().TextColor(p.text).SingleLine()
							if followed && a.followingHasNew(fav) {
								ui.Text(c, "已追 · 有新集").Shrink(0).Padding(2, 8).Radius(6).
									FontSize(10.5).Bold().Background(p.accentSoft).Border(1, p.accentBorder).
									TextColor(p.accentLightText).SingleLine()
							}
						})

						progressMeta := e.progress()
						if available && e.Origin != "" {
							progressMeta += " · " + e.Origin
						}
						ui.Text(c, progressMeta).FontSize(12.5).TextColor(p.textMuted).SingleLine()

						if e.Duration > 0 {
							progress := max(0, min(e.Position/e.Duration, 1))
							ui.Progress(c, progress).Height(3.5).FillWidth().Label("观看进度 " + e.Name)
						}

						if !available {
							missingLabel := "片源已移除"
							if e.isLocal() {
								missingLabel = "文件已移除"
							}
							ui.Text(c, missingLabel).FontSize(12).TextColor(p.warning).SingleLine()
						} else if sub := continueWatchingSubline(e, fav, followed); sub != "" {
							ui.Text(c, sub).FontSize(11.5).TextColor(p.textFaint).SingleLine()
						}

						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Padding(2, 0, 0, 0).Children(func() {
							ctaText := "继续播放"
							if ep := strings.TrimSpace(e.Episode); ep != "" && len([]rune(ep)) <= 8 {
								ctaText = "继续播放 · " + ep
							}
							resumeBtn := ui.ButtonBase(c.Key("cw-resume-"+e.dedupKey())).Height(36).Padding(0, 16).Gap(7).Radius(9).
								AlignItems(ui.Center).Cursor(ui.CursorPointer).Label("续播 " + e.Name).Disabled(!available)
							bg := ui.RGBA(255, 255, 255, 0.14)
							border := ui.RGBA(255, 255, 255, 0.20)
							fg := p.text
							if p.reduceTransparency {
								bg = ui.Hex("#2c3246")
								border = p.borderControl
							}
							if resumeBtn.Hovered() && available {
								bg = p.accent
								border = p.accent
								fg = p.accentText
							}
							animFg := animateButtonChrome(resumeBtn, "cw-res", bg, border, fg)
							applyPressPose(resumeBtn)
							resumeBtn.Children(func() {
								ui.Icon(c, iconPlay).Size(11, 11).TextColor(animFg).PassThrough()
								ui.Text(c, ctaText).FontSize(13).Bold().TextColor(animFg).SingleLine().PassThrough()
							})
							if resumeBtn.Clicked() {
								a.resume(e)
							}

							if !e.isLocal() && a.library.root != nil {
								if ghostButton(c.Key("cw-detail-"+e.dedupKey()), "详情").Height(36).Padding(0, 14).FontSize(13).
									Label("继续观看详情 " + e.Name).Clicked() {
									a.openHistoryDetail(e)
								}
							}
						})

						if len(entries) > 1 {
							ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "其他在看：").FontSize(11.5).TextColor(p.textFaint).SingleLine().Shrink(0)
								shown := 0
								for idx, other := range entries {
									if idx == a.homeResumeIdx {
										continue
									}
									if shown > 0 {
										ui.Text(c, "·").FontSize(11.5).TextColor(p.textFaint).Shrink(0)
									}
									snippet := other.Name
									if ep := strings.TrimSpace(other.Episode); ep != "" && len([]rune(ep)) <= 6 {
										snippet += " " + ep
									} else if other.Duration > 0 {
										pct := int(max(0, min(other.Position/other.Duration, 1)) * 100)
										snippet += fmt.Sprintf(" %d%%", pct)
									}
									pick := ui.ButtonBase(c.Key("cw-pick-"+other.dedupKey())).Padding(2, 4).Radius(5).
										Cursor(ui.CursorPointer).Label("切换继续观看 " + other.Name)
									pickBg, pickFg := ui.Transparent, p.textMuted
									if pick.Hovered() {
										pickBg, pickFg = p.surfaceSubtle, p.text
									}
									pick.Background(pick.AnimateColor("cw-pick-bg", pickBg, animColor))
									animPickFg := pick.AnimateColor("cw-pick-fg", pickFg, animColor)
									applyPressPose(pick)
									pick.Children(func() {
										ui.Text(c, snippet).FontSize(11.5).TextColor(animPickFg).SingleLine().PassThrough()
									})
									if pick.Clicked() {
										if idx > a.homeResumeIdx {
											a.homeResumeDir = 1
										} else {
											a.homeResumeDir = -1
										}
										a.homeResumeIdx = idx
									}
									shown++
									if shown == 2 {
										break
									}
								}
							})
						}
					})
				})

				dismissBtn := ui.ButtonBase(c.Key("dismiss-"+e.dedupKey())).
					Absolute().Top(10).Right(10).ZIndex(1).Size(24, 24).Radius(12).Center().
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
					a.homeResumeDir = 0
					a.setContinueWatchingHidden(key, true)
					if a.homeResumeIdx >= len(entries)-1 && a.homeResumeIdx > 0 {
						a.homeResumeIdx--
					}
					a.setUndoNotice("已隐藏「"+name+"」", "撤销", func() {
						a.setContinueWatchingHidden(key, false)
					})
				}
			})
		})
	})
}
