package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

var playerInk = ui.RGB(9, 15, 31)
var playerText = ui.RGB(248, 249, 255)

const playerOverlayIdle = 3 * time.Second

type playerOverlayState struct {
	hideAt             time.Time
	pointerX, pointerY float32
	pointerOver        bool
	keepVisible        bool
}

func (a *app) setFullScreen(full bool) {
	a.fullScreen = full
	a.playerOverlay = playerOverlayState{}
	a.playerPointer.clickAt = time.Time{}
	if full {
		a.panelOpen, a.panelTouched = false, false
	}
}

func (a *app) playerOverlaysVisible(c *ui.Context, screen ui.Element) bool {
	// PointerPosition makes the element consume presses, which prevents
	// DragWindow from handing them to the system. Only fullscreen needs pointer
	// tracking: windowed controls are always visible and must remain draggable.
	if !a.fullScreen {
		a.playerOverlay.keepVisible = false
		return true
	}
	s := &a.playerOverlay
	x, y, over := screen.PointerPosition()
	now := c.Now()
	if s.hideAt.IsZero() || s.keepVisible || over && (!s.pointerOver || x != s.pointerX || y != s.pointerY) {
		s.hideAt = now.Add(playerOverlayIdle)
	}
	s.pointerX, s.pointerY, s.pointerOver = x, y, over
	s.keepVisible = false
	if now.Before(s.hideAt) {
		c.After(s.hideAt.Sub(now))
		return true
	}
	return false
}

// playerScreen keeps the system video in one view and paints the controls
// over it. All VideoPlayer calls stay on MyGo's UI thread.
func (a *app) playerScreen(c *ui.Context) {
	a.advanceLive(a.player)
	p := a.player
	if p == nil {
		return
	}
	a.trackPlayback(c, p)
	a.checkPlayback(c, p)
	a.checkSleepTimer(c, p)
	a.advanceEpisode(p, c.Now())
	if !a.episodeEnd.since.IsZero() {
		c.After(episodeEndTolerance)
	}
	// Advancing can synchronously replace (and close) the previous player.
	// Controls must never read the old native engine after that transition.
	p = a.player
	if p == nil {
		a.shell(c)
		return
	}
	a.flushPlayerClick(c, p)
	width, _ := c.Size()
	compact := width < 900
	overlayPanel := compact || a.fullScreen
	// Show the side panel only when there is actually something to choose or configure:
	// multiple episodes/sources/lines, an active source-switch preview, live channels/EPG,
	// or sibling local video files.
	hasEpisodes := a.hasUsefulPlayerPanel()
	ui.Themed(c, ui.DarkTheme()).Fill().Background(playerInk).Children(func() {
		screen := ui.Box(c).Fill().Background(playerInk).ObserveInput(func(ev ui.InputEvent) {
			a.playerOverlay.hideAt = ev.Time.Add(playerOverlayIdle)
			if ev.Kind == ui.InputKeyDown {
				a.playerPointer.clickAt = time.Time{}
			}
		})
		if !a.fullScreen && dragStripHeight(c) > 0 {
			screen.DragWindow().Label("拖动窗口（播放页空白）")
		}
		miniDragH := float32(0)
		miniKept := false
		var showControls bool
		if a.playerExtra.miniMode {
			if !a.fullScreen {
				miniDragH = dragStripHeight(c)
			}
			hovered := screen.Hovered()
			miniKept = a.playerOverlay.keepVisible && !hovered && p.Playing()
			showControls = hovered || !p.Playing() || a.playerOverlay.keepVisible
			a.playerOverlay.keepVisible = false
		} else {
			showControls = a.playerOverlaysVisible(c, screen)
		}
		showPanel := !a.playerExtra.miniMode && hasEpisodes && a.panelOpen && (!compact || a.panelTouched) && showControls
		if !showControls && !a.playerExtra.miniMode {
			screen.Cursor(ui.CursorNone)
		}
		screen.Children(func() {
			ui.Row(c).Fill().Children(func() {
				ui.Box(c).Grow(1).FillHeight().Clip().Children(func() {
					p.View(c).Key(p).Fill().Fit(a.playerVideoFit()).Label("视频画面").Focusable().AutoFocus().Bind(&a.playerFocus).
						HitTest(func(x, y float32, r ui.Rect) bool {
							if a.playerExtra.miniMode {
								return miniDragH <= 0 || y >= r.Y+miniDragH
							}
							if a.playerExtra.aspect == aspectFill {
								return true
							}
							w, h := p.Size()
							return a.fullScreen || playerPictureRect(r, w, h).Contains(x, y)
						}).HandleInput(func(ev ui.InputEvent) bool { return a.handlePlayerPointer(p, ev) })
					a.danmakuOverlay(c, p, showControls)
					if a.playerExtra.miniMode {
						a.playerMiniDragStrip(c, miniDragH, showControls)
					}
					a.playerState(c, p)
					a.playbackStatsOverlay(c, p)
					a.rightHoldBoostPill(c)
					a.playerOSDPill(c)
					a.skipUndoPill(c, p)
					a.upNextPill(c, p, showControls)
					if showControls {
						if a.playerExtra.miniMode {
							a.playerMiniControls(c, p)
							if miniKept && !a.playerOverlay.keepVisible {
								c.Invalidate()
							}
						} else {
							a.playerControls(c, p, hasEpisodes, compact)
							a.playerHeader(c, hasEpisodes, compact)
						}
					}
				})
				if showPanel && !overlayPanel {
					ui.Box(c).Key("player-panel-dock").Width(320).FillHeight().Padding(10, 10, 10, 0).
						Transition(transSideDock).Children(func() {
						a.playerPanel(c, "player-panel-dock-body").Fill()
					})
				}
			})
			if showPanel && overlayPanel {
				bottom := float32(12)
				if a.fullScreen {
					bottom = 140
				}
				a.playerPanel(c, "player-panel-drawer").Absolute().Top(70).Right(12).Bottom(bottom).Width(280).
					Transition(transSideDrawer)
			}
		})
		a.danmakuDialog(c)
		a.subtitleDialog(c, p)
		a.panAuthPromptDialog(c)
	})
}

func stylePlayerGlassControl(el ui.Element) ui.Element {
	return applyPressPose(el.Radius(16).
		Material(glass.Glass{Style: glass.Clear, Interactive: true}).TextColor(playerText))
}

func playerGlassButton(c *ui.Context, label string) ui.Element {
	return stylePlayerGlassControl(ui.Button(c, label))
}

func playerGlassMenuButton(c *ui.Context, label string, build func(m *ui.Menu)) ui.Element {
	return stylePlayerGlassControl(ui.MenuButton(c, label, build))
}

func (a *app) playerHeader(c *ui.Context, hasEpisodes, compact bool) {
	name := "直链播放"
	if !a.playingDirect && a.title != nil {
		name = a.title.name
		if a.playingEpisode != "" {
			name += " · " + a.playingEpisode
		}
	}
	if a.playingLive && a.live.playingChannel >= 0 && a.live.playingChannel < len(a.live.channels) {
		ch := a.live.channels[a.live.playingChannel]
		if a.live.catchupProgram != nil {
			name = "回看 · " + ch.Name + " · " + a.live.catchupProgram.Title
		} else {
			name = "直播 · " + ch.Name
			if cur, _ := a.channelEPGCurrent(ch); cur != nil {
				name += " · " + cur.Title
			}
		}
	}
	if a.playingFile {
		name = a.playingName
	}
	top := float32(12)
	if bar := c.TitleBar(); bar.Bottom+8 > top {
		top = bar.Bottom + 8
	}
	left := max(float32(88), c.TitleBar().Left+14)
	ui.Row(c).Key("hud-top").Absolute().Top(top).Left(left).Right(16).Gap(10).AlignItems(ui.Center).
		Transition(transHUDTop).Children(func() {
		ui.Row(c).Padding(5).Radius(18).Material(glass.Glass{Style: glass.Regular}).Children(func() {
			if playerGlassButton(c, "返回").Tooltip("返回上一页").Clicked() {
				a.leavePlayer()
			}
		})
		title := ui.Row(c).Padding(8, 14).Radius(20).Material(glass.Glass{Style: glass.Clear}).Grow(1)
		if dragStripHeight(c) > 0 {
			title.DragWindow().Label("拖动窗口")
		}
		title.Children(func() {
			ui.Text(c, name).SingleLine().TextColor(playerText).TextShadow(0, 1, 4, playerInk).ContentTransition(ctReplace)
			if badge := a.sleepTimerBadge(c.Now()); badge != "" {
				ui.Text(c, " · "+badge).FontSize(12).SingleLine().TextColor(ui.RGB(255, 198, 130)).ContentTransition(ctNumeric)
			}
			if a.playerExtra.loop {
				ui.Text(c, " · 循环").FontSize(12).SingleLine().TextColor(ui.RGB(142, 230, 178))
			}
		})
		ui.Row(c).Padding(5).Gap(4).Radius(18).Material(glass.Glass{Style: glass.Regular}).Children(func() {
			if !a.fullScreen {
				pinLabel := "置顶"
				if a.playerExtra.pinned {
					pinLabel = "已置顶"
				}
				if playerGlassButton(c, pinLabel).Padding(5, 10).Label("窗口置顶").Tooltip("切换窗口置顶 (P)").Clicked() {
					a.toggleAlwaysOnTop()
				}
			}
			if playerGlassButton(c, "小窗").Padding(5, 10).Label("迷你小窗").Tooltip("置顶迷你小窗 (T)").Clicked() {
				a.toggleMiniMode()
			}
		})
		if hasEpisodes {
			panelLabel, closeLabel := "换源与选集", "收起选集"
			if a.playingLive {
				if len(a.live.channels) <= 1 && a.activeEPGURL() != "" {
					panelLabel, closeLabel = "节目单", "收起节目单"
				} else {
					panelLabel, closeLabel = "频道列表", "收起频道"
				}
			} else if a.hasLocalPlaylist() {
				panelLabel, closeLabel = "目录列表", "收起列表"
			}
			if a.panelOpen && (!compact || a.panelTouched) {
				panelLabel = closeLabel
			}
			ui.Row(c).Padding(5).Radius(18).Material(glass.Glass{Style: glass.Regular}).Children(func() {
				btn := playerGlassButton(c, "").Label(panelLabel).Tooltip(panelLabel + " (Esc 收起)")
				btn.Children(func() {
					ui.Text(c, panelLabel).SingleLine().ContentTransition(ctReplace)
				})
				if btn.Clicked() {
					if compact && !a.panelTouched {
						a.panelOpen = true
					} else {
						a.panelOpen = !a.panelOpen
					}
					a.panelTouched = true
				}
			})
		}
	})
}

func (a *app) playerState(c *ui.Context, p mediaPlayer) {
	a.syncToastState(c, a.busy())
	message := ""
	dismissible := false
	switch {
	case a.running(jobPlay):
		message = a.status
	case a.playFailure != nil && a.hasEpisodes():
		a.playerFailure(c, a.playFailure.err)
		return
	case p.Err() != nil:
		if a.hasEpisodes() {
			a.playerFailure(c, p.Err())
			return
		}
		message = "播放失败：" + p.Err().Error()
	case !p.Ready():
		message = "正在加载视频…"
		if a.isLiveStream() && time.Since(a.live.started) >= 15*time.Second {
			message = a.status
		}
	case p.Buffering():
		message = "正在缓冲…"
	case a.status != "" && a.status != "正在播放":
		message = a.status
		dismissible = a.toast.ttl == 0
	}
	if message == "" {
		return
	}
	ui.Row(c).Key("player-state-pill").Absolute().Top(84).Left(24).Right(24).Center().Transition(transOSD).Children(func() {
		ui.Row(c).Padding(9, 14).Gap(10).Radius(14).MaxWidth(640).AlignItems(ui.Center).
			Material(glass.Glass{Style: glass.Regular}).TextColor(playerText).Children(func() {
			ui.Text(c, message).Selectable().ContentTransition(ctReplace)
			if dismissible {
				if playerGlassButton(c, "关闭").Label("关闭播放提示").Padding(3, 9).Clicked() {
					a.clearStatus()
				}
			}
		})
	})
}

func (a *app) hasUsefulPlayerPanel() bool {
	if a.playingLive {
		return len(a.live.channels) > 1 || a.activeEPGURL() != ""
	}
	if a.hasLocalPlaylist() {
		return true
	}
	if !a.hasEpisodes() {
		return false
	}
	return a.hasMultipleEpisodes() || len(a.candidates()) > 1 || len(a.title.detail.Sources) > 1 || a.panelChoice != nil
}

func (a *app) hasMultipleEpisodes() bool {
	if !a.hasEpisodes() {
		return false
	}
	for _, src := range a.title.detail.Sources {
		if len(src.Episodes) > 1 {
			return true
		}
	}
	return false
}

func (a *app) playerControls(c *ui.Context, p mediaPlayer, hasEpisodes, compact bool) {
	ui.Row(c).Key("hud-bottom").Absolute().Left(18).Right(18).Bottom(18).Justify(ui.Center).
		Transition(transHUDBottom).Children(func() {
		ui.Column(c).FillWidth().MaxWidth(900).Padding(10, 15).Gap(8).
			Radius(24).Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(9, 15, 31, 0.62)}).
			TextColor(playerText).Children(func() {
			duration := p.Duration()
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				switch {
				case a.isLiveStream():
					ui.Text(c, "直播").FontSize(12).Grow(1)
				case duration > 0:
					ui.Text(c, playerClock(p.Position())).FontSize(12).ContentTransition(ctNumeric)
					position := p.Position().Seconds()
					ui.Column(c).Grow(1).Gap(2).Children(func() {
						a.danmakuHighlightCurve(c, p, duration)
						progress := ui.Slider(c, &position, 0, duration.Seconds()).Label("播放进度").FillWidth()
						if progress.Pressed() {
							a.playerOverlay.keepVisible = true
						}
						if progress.Changed() {
							p.Seek(time.Duration(position * float64(time.Second)))
							a.syncNowPlayingState()
						}
					})
					ui.Text(c, playerClock(duration)).FontSize(12).ContentTransition(ctNumeric)
				default:
					ui.Text(c, "加载中…").FontSize(12).TextColor(ui.RGBA(248, 249, 255, 0.72)).Grow(1)
				}
				w, h := p.Size()
				if w > 0 && h > 0 {
					ui.Text(c, fmt.Sprintf("%d×%d", w, h)).FontSize(12).TextColor(ui.RGBA(248, 249, 255, 0.78)).SingleLine()
				}
				if !a.playingFile && !a.playingDirectMPV {
					ui.Text(c, "网速 "+a.playbackSpeed(c)).FontSize(12).TextColor(ui.RGBA(248, 249, 255, 0.78)).SingleLine().ContentTransition(ctNumeric)
				}
			})
			ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				if a.playingLive {
					a.livePlayerControls(c)
				}
				multiEp := a.hasMultipleEpisodes()
				localMulti := a.hasLocalPlaylist()
				source, episode, current := a.currentEpisode()
				if multiEp && playerGlassButton(c, "上一集").Tooltip("播放上一集").
					Disabled(!current || episode == 0).Clicked() {
					a.playEpisode(a.title.detail.Sources[source].Name, a.title.detail.Sources[source].Episodes[episode-1])
				} else if localMulti && playerGlassButton(c, "上一集").Tooltip("播放上一集").
					Disabled(a.localPlaylistIdx <= 0).Clicked() {
					a.playLocalPlaylistIndex(a.localPlaylistIdx - 1)
				}
				playLabel := "暂停"
				if !p.Playing() {
					playLabel = "播放"
				}
				playBtn := playerGlassButton(c, "").Label(playLabel).Tooltip("播放 / 暂停 (Space)")
				playBtn.Children(func() {
					ui.Text(c, playLabel).SingleLine().ContentTransition(ctReplace)
				})
				if playBtn.Clicked() {
					togglePlayback(p)
					a.syncNowPlayingState()
				}
				if multiEp && playerGlassButton(c, "下一集").Tooltip("播放下一集").
					Disabled(!current || episode+1 >= len(a.title.detail.Sources[source].Episodes)).Clicked() {
					a.playEpisode(a.title.detail.Sources[source].Name, a.title.detail.Sources[source].Episodes[episode+1])
				} else if localMulti && playerGlassButton(c, "下一集").Tooltip("播放下一集").
					Disabled(a.localPlaylistIdx+1 >= len(a.localPlaylist)).Clicked() {
					a.playLocalPlaylistIndex(a.localPlaylistIdx + 1)
				}
				volLabel := "音量"
				if a.muted {
					volLabel = "已静音"
				}
				muteBtn := playerGlassButton(c, "").Padding(5, 10).Label("静音切换").Tooltip("静音 / 取消静音 (M)")
				muteBtn.Children(func() {
					ui.Text(c, volLabel).FontSize(12).SingleLine().ContentTransition(ctReplace)
				})
				if muteBtn.Clicked() {
					a.toggleMute(p)
				}
				volume := p.Volume()
				if a.muted {
					volume = 0
				}
				volumeControl := ui.Slider(c, &volume, 0, 1).Label("调节音量").Width(88)
				if volumeControl.Pressed() {
					a.playerOverlay.keepVisible = true
				}
				if volumeControl.Changed() {
					a.muted = volume <= 0
					p.SetMuted(a.muted)
					p.SetVolume(volume)
					if volume > 0 {
						a.savedVolume = volume
					}
					a.ensurePreferences()
					a.preferences.Volume = volume
					a.savePreferences()
				}
				ui.Spacer(c)
				a.streamControls(c)
				a.audioTrackControls(c, p)
				a.subtitleTrackControls(c, p)
				a.danmakuButton(c)
				a.morePlayerControls(c, p)
				rate := p.Rate()
				if rate <= 0 {
					rate = 1
				}
				if !a.isLiveStream() {
					rateLabel := strconv.FormatFloat(rate, 'f', -1, 64) + "×"
					rateBtn := playerGlassButton(c, "").Label("播放速度").Tooltip("点击切换倍速，长按 → 键临时加速")
					rateBtn.Children(func() {
						ui.Text(c, rateLabel).SingleLine().ContentTransition(ctNumeric)
					})
					if rateBtn.Clicked() {
						a.ensurePreferences()
						a.preferences.Rate = nextPlayerRate(rate)
						p.SetRate(a.preferences.Rate)
						a.savePreferences()
						a.showPlayerOSD(strconv.FormatFloat(a.preferences.Rate, 'f', -1, 64) + "× 倍速")
					}
				}
				if a.window != nil {
					fullLabel := "全屏"
					if a.fullScreen {
						fullLabel = "退出全屏"
					}
					fullBtn := playerGlassButton(c, "").Label(fullLabel).Tooltip("全屏切换 (F)")
					fullBtn.Children(func() {
						ui.Text(c, fullLabel).SingleLine().ContentTransition(ctReplace)
					})
					if fullBtn.Clicked() {
						a.togglePlayerFullScreen()
						a.playerFocus.Focus(c)
					}
				}
			})
		})
	})
}

// playerPanel floats beside the picture: a title's episodes and sources, or
// a live list's channels.
func (a *app) playerPanel(c *ui.Context, key string) ui.Element {
	panel := ui.Themed(c, ui.DarkTheme()).Key(key).Padding(16).Gap(12).Radius(24).
		Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(40, 35, 70, 0.67)}).
		TextColor(playerText)
	if panel.Hovered() {
		a.playerOverlay.keepVisible = true
	}
	panel.Children(func() {
		if a.playingLive {
			a.panelChannels(c)
			return
		}
		if a.hasLocalPlaylist() {
			a.panelLocalPlaylist(c)
			return
		}
		if !a.hasEpisodes() { // the header left the player earlier in this frame
			return
		}
		multiEp := a.hasMultipleEpisodes()
		if !multiEp {
			a.panelTab = 0
		} else {
			ui.Segmented(c, &a.panelTab, "选集", "跳过").Label("面板内容")
		}
		if a.panelTab == 1 {
			ui.Scroll(c).FillWidth().Grow(1).Gap(10).Children(func() {
				a.panelSeriesSkips(c)
			})
			return
		}
		a.panelEpisodes(c)
	})
	return panel
}

func stylePanelGlassControl(el ui.Element, selected bool) ui.Element {
	tint := ui.RGBA(55, 50, 92, 0.39)
	if selected {
		tint = ui.RGBA(102, 98, 143, 0.57)
	}
	return applyPressPose(el.FillWidth().Padding(10, 12).Radius(14).
		Material(glass.Glass{Style: glass.Clear, Tint: tint, Interactive: true}).TextColor(playerText))
}

func panelButton(c *ui.Context, label string, selected bool) ui.Element {
	return stylePanelGlassControl(ui.Button(c, label), selected)
}

func (a *app) panelEpisodes(c *ui.Context) {
	ui.Scroll(c).FillWidth().Grow(1).Gap(8).Children(func() {
		a.panelSources(c, a.candidates())
		detail := a.title.detail
		if choice := a.panelChoice; choice != nil {
			if choice.message != "" {
				ui.Text(c, choice.message).FontSize(12)
			}
			detail = choice.detail
		}
		if detail == nil || len(detail.Sources) == 0 {
			return
		}
		if a.panelSource < 0 || a.panelSource >= len(detail.Sources) {
			a.panelSource, _, _ = a.currentEpisode()
		}
		selectedLine := a.panelSource
		if len(detail.Sources) > 1 {
			ui.Text(c, "线路").Bold()
			for i, source := range detail.Sources {
				name := source.Name
				if name == "" {
					name = fmt.Sprintf("线路 %d", i+1)
				}
				if panelButton(c.Key(fmt.Sprintf("player-source-%d", i)), name, i == a.panelSource).Clicked() {
					selectedLine = i
					a.selectPanelLine(detail, i)
				}
			}
		}
		selected := detail.Sources[selectedLine]
		solePlaying := len(selected.Episodes) == 1 && a.panelChoice == nil &&
			selected.Name == a.playingSource && selected.Episodes[0].Name == a.playingEpisode
		if len(selected.Episodes) > 0 && !solePlaying {
			ui.Text(c, "选集").Bold()
			ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
				for i, episode := range selected.Episodes {
					active := a.panelChoice == nil && selected.Name == a.playingSource && episode.Name == a.playingEpisode
					button := ui.Button(c, episodeButtonLabel(episode.Name)).Key(fmt.Sprintf("player-episode-%d", i)).MinWidth(52).Radius(12)
					if active {
						button.Background(ui.RGB(255, 255, 255)).TextColor(playerInk)
					} else {
						button.Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(55, 50, 92, 0.51), Interactive: true}).TextColor(playerText)
					}
					applyPressPose(button)
					if button.Clicked() {
						a.playPanelEpisode(selectedLine, episode, 0)
					}
				}
			})
		}
	})
	if a.hasMultipleEpisodes() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "播完自动下一集").Grow(1)
			if ui.Switch(c, &a.autoNext).Label("自动下一集").Changed() {
				a.savePreferences()
			}
		})
	}
}

// Keep the source visible above its lines even when only one site is known.
func (a *app) panelSources(c *ui.Context, candidates []library.Item) {
	item := a.title.item
	if a.panelChoice != nil {
		item = a.panelChoice.item
	}
	label := item.Entry.Label()
	if len(candidates) <= 1 && label == "" {
		return
	}
	ui.Text(c, "来源").Bold()
	if len(candidates) > 1 {
		retry := a.panelChoice != nil && a.panelChoice.detail == nil && !a.running(jobPlay)
		stylePanelGlassControl(ui.MenuButton(c, label, func(m *ui.Menu) {
			for _, candidate := range candidates {
				key := itemKey(candidate)
				selected := item.Entry.Config != nil && itemKey(item) == key
				if m.Item(candidate.Entry.Label()).ID(key).Radio(selected).Disabled(selected && !retry).Chosen() {
					a.switchSource(candidate)
				}
			}
		}), false).Label("选择来源")
	} else {
		ui.Text(c, label)
	}
}

// panelChannels changes the channel or browses today's EPG schedule & catchup without leaving the player.
func (a *app) panelChannels(c *ui.Context) {
	live := &a.live
	hasEPG := a.activeEPGURL() != ""
	if len(live.channels) <= 1 && hasEPG {
		ui.Text(c, "节目单").FontSize(18).Bold()
		a.panelChannelEPG(c)
		return
	}
	if !hasEPG {
		live.panelTab = 0
		ui.Text(c, "频道").FontSize(18).Bold()
	} else {
		ui.Segmented(c, &live.panelTab, "频道", "节目单").Label("直播面板")
	}
	if live.panelTab == 1 && hasEPG {
		a.panelChannelEPG(c)
		return
	}
	if len(live.state.favorites) == 0 && live.group == liveGroupFavorites {
		live.group = ""
		live.resetLists = true
	}
	if live.resetLists {
		live.channelList, live.panelChannelList = ui.ListState{}, ui.ListState{}
		live.resetLists = false
	}
	indices := live.filteredChannels(false)
	live.panelChannelList.Key = func(row int) any { return indices[row] }
	live.panelChannelList.Label = func(row int) string { return live.channels[indices[row]].Name }
	ui.List(c, &live.panelChannelList, len(indices), func(row int) {
		i := indices[row]
		channel := live.channels[i]
		current := i == live.playingChannel
		fav := live.state.isFavorite(channel.Name)
		tint := ui.RGBA(55, 50, 92, 0.39)
		if current {
			tint = ui.RGBA(102, 98, 143, 0.57)
		}
		btn := ui.Row(c.Key(fmt.Sprintf("panel-channel-%d", i))).FillWidth().Padding(8, 10).Gap(8).
			Radius(14).AlignItems(ui.Center).Cursor(ui.CursorPointer).
			Material(glass.Glass{Style: glass.Clear, Tint: tint, Interactive: true}).
			TextColor(playerText).Label("切换到 " + channel.Name)
		applyPressPose(btn)
		btn.Children(func() {
			a.renderChannelLogo(c, channel, 26, true)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				label := channel.Name
				if fav {
					label = "★ " + label
				}
				ui.Text(c, label).FontSize(13).SingleLine()
				if cur, _ := a.channelEPGCurrent(channel); cur != nil {
					ui.Text(c, cur.Title).FontSize(11).TextColor(ui.RGBA(248, 249, 255, 0.70)).SingleLine()
				}
			})
		})
		if btn.Clicked() && !current {
			a.playLiveChannel(i, 0)
		}
	}).FillWidth().Grow(1).MinHeight(0).Gap(6)
}

// panelLocalPlaylist lists sibling videos in the same directory so a viewer
// watching a local season can jump between files or toggle auto-next.
func (a *app) panelLocalPlaylist(c *ui.Context) {
	ui.Text(c, "同目录视频").FontSize(18).Bold()
	ui.Scroll(c).FillWidth().Grow(1).Gap(6).Children(func() {
		for i, path := range a.localPlaylist {
			name := filepath.Base(path)
			current := i == a.localPlaylistIdx
			if panelButton(c.Key(fmt.Sprintf("panel-local-%d", i)), name, current).Label("播放 "+name).Clicked() && !current {
				a.playLocalPlaylistIndex(i)
			}
		}
	})
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "播完自动下一集").Grow(1)
		if ui.Switch(c, &a.autoNext).Label("自动下一集").Changed() {
			a.savePreferences()
		}
	})
}

// playerFailure offers the ways out of an episode that does not play.
func (a *app) playerFailure(c *ui.Context, err error) {
	heading := "这条线路播不了"
	if f := a.playFailure; f != nil && f.authProvider != "" {
		heading = "需要登录" + f.authProvider.Label()
	}
	ui.Row(c).Key("player-failure").Absolute().Top(96).Left(24).Right(24).Center().Transition(transDialogPanel).Children(func() {
		ui.Column(c).MaxWidth(460).Padding(22).Gap(14).Radius(26).Material(glass.Glass{}).TextColor(playerText).Children(func() {
			ui.Text(c, heading).FontSize(18).Bold()
			if f := a.playFailure; f != nil {
				ui.Text(c, f.line+" · "+f.episode.Name).FontSize(13).TextColor(ui.RGBA(248, 249, 255, 0.78))
			}
			ui.Text(c, err.Error()).Selectable()
			if a.status != "" && a.status != "正在播放" {
				ui.Text(c, a.status)
			}
			a.recoveryActions(c)
		})
	})
}

// hasEpisodes reports whether what plays is an episode of the title on show.
func (a *app) hasEpisodes() bool {
	return !a.playingDirect && !a.playingLive && !a.playingFile && a.title != nil && a.title.detail != nil && len(a.title.detail.Sources) > 0
}

// trackPlayback seeks to where a resumed episode stopped, then keeps the
// position for the play history and for changing line or source.
func (a *app) trackPlayback(c *ui.Context, p mediaPlayer) {
	if !p.Ready() || p.Err() != nil || a.playFailure != nil || a.autoNextPending {
		return
	}
	a.maybeAutoSelectChineseSubtitle(p)
	a.applyOpeningSkip(p)
	if a.resumeAt > 0 {
		if !a.resumeIssued {
			if duration := p.Duration(); duration > 0 {
				a.resumeAt = min(a.resumeAt, max(0, duration-time.Second))
			}
			p.Seek(a.resumeAt)
			a.syncNowPlayingState()
			a.lastPosition, a.resumeIssued = a.resumeAt, true
			return
		}
		// AVPlayer seeks asynchronously. Keep the intended position until it
		// arrives, so a second line switch cannot accidentally resume at zero.
		if p.Position() < a.resumeAt-time.Second {
			return
		}
		a.resumeAt, a.resumeIssued = 0, false
	}
	a.lastPosition = p.Position()
	a.recordHistory(p, c.Now())
}

func (a *app) advanceEpisode(p mediaPlayer, now time.Time) {
	if a.playerExtra.loop {
		a.episodeEnd = playbackEnd{}
		if _, isAdv := p.(advancedMediaPlayer); !isAdv && p.Ended() {
			p.Seek(0)
			p.Play()
		}
		return
	}
	if a.playerExtra.sleepMode == sleepAfterEpisode {
		ended := a.episodeEnd.check(now, p.Ready() && a.resumeAt == 0, p.Buffering(), p.Playing(), p.Position(), p.Duration())
		if p.Ended() || a.skipEndingReady(p) || ended {
			a.episodeEnd = playbackEnd{}
			a.clearSleepTimer()
			p.Pause()
			a.syncNowPlayingState()
			a.setNotice("本集已播完，定时关闭已暂停播放")
			a.showPlayerOSD("定时已到 · 已暂停")
		}
		return
	}
	if !a.autoNext || a.autoNextPending || a.autoNextCanceled || a.running(jobPlay) || a.playFailure != nil || p.Err() != nil {
		a.episodeEnd = playbackEnd{}
		return
	}
	if a.playingFile {
		if _, ok := a.nextLocalFile(); !ok {
			a.episodeEnd = playbackEnd{}
			return
		}
		ended := a.episodeEnd.check(now, p.Ready() && a.resumeAt == 0, p.Buffering(), p.Playing(), p.Position(), p.Duration())
		if !p.Ended() && !ended {
			return
		}
		a.episodeEnd = playbackEnd{}
		a.recordLocalCompletion(p)
		a.autoNextPending = true
		a.playLocalPlaylistIndex(a.localPlaylistIdx + 1)
		return
	}
	if !a.hasEpisodes() || a.panelChoice != nil && a.panelOpen {
		a.episodeEnd = playbackEnd{}
		return
	}
	source, episode, ok := a.currentEpisode()
	if !ok {
		a.episodeEnd = playbackEnd{}
		return
	}
	episodes := a.title.detail.Sources[source].Episodes
	if episode+1 >= len(episodes) {
		a.episodeEnd = playbackEnd{}
		return
	}
	ended := a.episodeEnd.check(now, p.Ready() && a.resumeAt == 0, p.Buffering(), p.Playing(), p.Position(), p.Duration())
	if !p.Ended() && !a.skipEndingReady(p) && !ended {
		return
	}
	a.episodeEnd = playbackEnd{}
	a.recordEpisodeCompletion(p)
	a.autoNextPending = true
	a.playEpisode(a.title.detail.Sources[source].Name, episodes[episode+1])
}

func (a *app) currentEpisode() (sourceIndex, episodeIndex int, ok bool) {
	if a.title == nil || a.title.detail == nil || a.playingEpisode == "" {
		return 0, 0, false
	}
	sources := a.title.detail.Sources
	if i := a.playingSourceIdx; i >= 0 && i < len(sources) && sources[i].Name == a.playingSource {
		eps := sources[i].Episodes
		if j := a.playingEpisodeIdx; j >= 0 && j < len(eps) && eps[j].Name == a.playingEpisode {
			return i, j, true
		}
	}
	for i, source := range sources {
		if source.Name != a.playingSource {
			continue
		}
		for j, episode := range source.Episodes {
			if episode.Name == a.playingEpisode {
				a.playingSourceIdx, a.playingEpisodeIdx = i, j
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func (a *app) leavePlayer() {
	a.leavePlayerExtra()
	a.cancelJob(jobPlay)
	a.clearPanelChoice()
	a.flushHistory()
	a.closeCurrentPlayer()
	if a.proxy != nil && a.proxySession != "" {
		a.proxy.RemoveSession(a.proxySession)
		a.proxySession = ""
	}
	a.playingStreams, a.playingStreamIdx = nil, 0
	a.syncNowPlaying()
	a.playingLive, a.playingDirect, a.playingDirectMPV, a.playingDirectLive = false, false, false, false
	a.live.catchupProgram = nil
	a.playingFile = false
	a.playingFilePath = ""
	a.playingName = ""
	a.localPlaylist = nil
	a.localPlaylistIdx = -1
	a.playerOverlay = playerOverlayState{}
	a.playerPointer = playerPointerState{}
	a.playbackWait = playbackWait{}
	a.playFailure = nil
	a.status = ""
	// A title played from the history has no page to go back to.
	if a.route() != routeDetail {
		a.clearTitle()
	}
}

// Keep the original name for playback and matching between sources.
// Matches ^(?:第\s*)?([0-9]+)\s*集(?:\s*(?:已完结|完结))?$|^([0-9]+)$ without regex allocations.
func episodeButtonLabel(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return name
	}
	// Pure ASCII digits: ^([0-9]+)$
	if isASCIIDigits(s) {
		return trimNumericLabel(s, name)
	}
	rest := s
	if strings.HasPrefix(rest, "第") {
		rest = skipRE2Space(rest[len("第"):])
	}
	digitEnd := 0
	for digitEnd < len(rest) && rest[digitEnd] >= '0' && rest[digitEnd] <= '9' {
		digitEnd++
	}
	if digitEnd == 0 {
		return name
	}
	digits := rest[:digitEnd]
	rest = skipRE2Space(rest[digitEnd:])
	if !strings.HasPrefix(rest, "集") {
		return name
	}
	rest = skipRE2Space(rest[len("集"):])
	if rest != "" && rest != "已完结" && rest != "完结" {
		return name
	}
	return trimNumericLabel(digits, name)
}

func isASCIIDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func skipRE2Space(s string) string {
	i := 0
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\f', '\r':
			i++
		default:
			return s[i:]
		}
	}
	return ""
}

func trimNumericLabel(digits, original string) string {
	i := 0
	for i < len(digits)-1 && digits[i] == '0' {
		i++
	}
	trimmed := digits[i:]
	// Preserve strconv.Atoi overflow fallback to original name.
	if len(trimmed) > 18 {
		if _, err := strconv.Atoi(trimmed); err != nil {
			return original
		}
	}
	return trimmed
}

func (a *app) streamControls(c *ui.Context) {
	if len(a.playingStreams) <= 1 {
		return
	}
	currentLabel := "清晰度"
	if a.playingStreamIdx >= 0 && a.playingStreamIdx < len(a.playingStreams) {
		if l := a.playingStreams[a.playingStreamIdx].Label; l != "" {
			currentLabel = l
		}
	}
	playerGlassMenuButton(c, currentLabel, func(m *ui.Menu) {
		for i, s := range a.playingStreams {
			label := s.Label
			if label == "" {
				label = fmt.Sprintf("流 %d", i+1)
			}
			selected := i == a.playingStreamIdx
			if m.Item(label).ID(fmt.Sprintf("stream-%d", i)).Radio(selected).Chosen() {
				a.switchPlayingStream(i)
			}
		}
	}).Label("切换画质")
}

func (a *app) audioTrackControls(c *ui.Context, p mediaPlayer) {
	tp, ok := p.(trackCapablePlayer)
	if !ok {
		return
	}
	tracks := tp.AudioTracks()
	if len(tracks) <= 1 {
		return
	}
	playerGlassMenuButton(c, "音轨", func(m *ui.Menu) {
		for i, tr := range tracks {
			label := formatAudioTrackLabel(tr, i)
			if m.Item(label).ID(fmt.Sprintf("audio-%d", tr.ID)).Radio(tr.Selected).Chosen() {
				if err := tp.SelectAudioTrack(tr.ID); err != nil {
					a.status = "切换音轨失败：" + err.Error()
				} else {
					a.showPlayerOSD("音轨：" + label)
				}
			}
		}
	}).Label("切换音轨").Tooltip("切换音轨")
}

func (a *app) subtitleTrackControls(c *ui.Context, p mediaPlayer) {
	if a.isLiveStream() {
		return
	}
	tp, ok := p.(trackCapablePlayer)
	if !ok {
		return
	}
	tracks := tp.SubtitleTracks()
	visible := tp.SubtitleVisible()
	hasActive := hasActiveSubtitleTrack(p)
	playerGlassMenuButton(c, "字幕", func(m *ui.Menu) {
		if len(tracks) > 0 {
			for i, tr := range tracks {
				selected := tr.Selected && visible
				label := formatSubtitleTrackLabel(tr, i)
				if m.Item(label).ID(fmt.Sprintf("sub-%d", tr.ID)).Radio(selected).Chosen() {
					if err := tp.SelectSubtitleTrack(tr.ID); err != nil {
						a.status = "切换字幕失败：" + err.Error()
					} else {
						a.showPlayerOSD("字幕：" + label)
					}
				}
			}
			if m.Item("关闭字幕").ID("sub-off").Radio(!hasActive).Chosen() {
				if err := tp.DisableSubtitleTrack(); err != nil {
					a.status = "关闭字幕失败：" + err.Error()
				} else {
					a.showPlayerOSD("字幕已关")
				}
			}
			if hasActive {
				m.Separator()
				if m.Item("字幕提前 0.5s ([)").ID("sub-delay-minus").Chosen() {
					a.adjustSubtitleDelay(p, -500*time.Millisecond)
				}
				if m.Item("字幕推迟 0.5s (])").ID("sub-delay-plus").Chosen() {
					a.adjustSubtitleDelay(p, 500*time.Millisecond)
				}
				if a.playerExtra.subDelay != 0 {
					if m.Item("重置偏移 (" + formatSubtitleDelay(a.playerExtra.subDelay) + ")").ID("sub-delay-reset").Chosen() {
						a.resetSubtitleDelay(p)
					}
				}
			}
			m.Separator()
		}
		if m.Item("加载外挂字幕…").ID("sub-load").Chosen() {
			a.openSubtitleFileDialog()
		}
		if m.Item("字幕样式与在线匹配…").ID("sub-settings").Chosen() {
			a.openSubtitleDialog()
		}
	}).Label("切换字幕").Tooltip("字幕与在线匹配 ([ / ] 调偏移)")
}

func (a *app) switchPlayingStream(idx int) {
	if idx < 0 || idx >= len(a.playingStreams) || idx == a.playingStreamIdx {
		return
	}
	s := a.playingStreams[idx]
	at := a.playbackPosition()
	streams := append([]catalog.Stream(nil), a.playingStreams...)
	line, ep := a.playingSource, a.playingEpisode
	srcIdx, epIdx := a.playingSourceIdx, a.playingEpisodeIdx
	danmakuURL := a.danmaku.sourceURL
	open, touched := a.panelOpen, a.panelTouched
	if !a.openPlaybackStream(s, true) {
		return
	}
	a.playingStreams, a.playingStreamIdx = streams, idx
	a.playingSource, a.playingEpisode = line, ep
	a.playingSourceIdx, a.playingEpisodeIdx = srcIdx, epIdx
	a.resumeAt = at
	a.panelOpen, a.panelTouched = open, touched
	a.danmaku.sourceURL = danmakuURL
	a.syncNowPlaying()
	if s.Label != "" {
		a.showPlayerOSD("画质：" + s.Label)
	}
}

func (a *app) livePlayerControls(c *ui.Context) {
	live := &a.live
	if live.catchupProgram != nil && live.playingChannel >= 0 && live.playingChannel < len(live.channels) {
		if playerGlassButton(c, "返回直播").Clicked() {
			a.playLiveChannel(live.playingChannel, live.playingStream)
		}
	}
	if len(live.channels) > 1 {
		if playerGlassButton(c, "上一频道").Disabled(live.playingChannel <= 0).Clicked() {
			a.playLiveChannel(live.playingChannel-1, 0)
		}
		if playerGlassButton(c, "下一频道").Disabled(live.playingChannel+1 >= len(live.channels)).Clicked() {
			a.playLiveChannel(live.playingChannel+1, 0)
		}
	}
	if live.playingChannel >= 0 && live.playingChannel < len(live.channels) {
		ch := live.channels[live.playingChannel]
		count := len(ch.Streams)
		if count > 1 && playerGlassButton(c, fmt.Sprintf("切换线路 %d/%d", live.playingStream+1, count)).Clicked() {
			a.playLiveChannel(live.playingChannel, (live.playingStream+1)%count)
		}
		favLabel := "☆ 收藏"
		if live.state.isFavorite(ch.Name) {
			favLabel = "★ 已收藏"
		}
		if playerGlassButton(c, favLabel).Label("收藏当前频道").Clicked() {
			a.toggleLiveChannelFavorite(ch)
		}
	}
}

func nextPlayerRate(current float64) float64 {
	for _, rate := range []float64{1, 1.25, 1.5, 2} {
		if rate > current+0.01 {
			return rate
		}
	}
	return 1
}

// playbackSpeed is the proxy's current download rate, sampled about
// once a second. The proxy only ever serves playback, so its bytes are
// the video's bytes: nothing is fetched twice and nothing in the video
// path changes. While nothing flows — before the first segment, while
// buffering or paused — it reads "—".
func (a *app) playbackSpeed(c *ui.Context) string {
	if a.proxy == nil || a.player == nil {
		return "—"
	}
	now := c.Now()
	total := a.proxy.BytesServed()
	if a.speedAt.IsZero() {
		a.speedBytes, a.speedAt, a.speedText = total, now, "—"
	} else if dt := now.Sub(a.speedAt); dt >= time.Second {
		a.speedText = formatSpeed(float64(total-a.speedBytes) / dt.Seconds())
		a.speedBytes, a.speedAt = total, now
	}
	// Wake the view again in a second so the number keeps moving
	// even when nothing else rebuilds the player.
	c.After(time.Second)
	return a.speedText
}

// formatSpeed is a bytes-per-second rate for the player: B/s below a
// kilobyte, one decimal above it.
func formatSpeed(bytesPerSecond float64) string {
	switch {
	case bytesPerSecond <= 0:
		return "—"
	case bytesPerSecond < 1024:
		return fmt.Sprintf("%.0f B/s", bytesPerSecond)
	case bytesPerSecond < 1024*1024:
		return fmt.Sprintf("%.1f KB/s", bytesPerSecond/1024)
	default:
		return fmt.Sprintf("%.1f MB/s", bytesPerSecond/(1024*1024))
	}
}

func playerClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int64(d / time.Second)
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}
