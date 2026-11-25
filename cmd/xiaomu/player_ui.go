package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

var (
	playerInk           = ui.RGB(9, 15, 31)
	playerText          = ui.RGB(248, 249, 255)
	playerMutedText     = ui.RGBA(248, 249, 255, 0.70)
	playerCoral         = ui.RGBA(255, 90, 64, 0.94)
	playerCoralSoft     = ui.RGBA(255, 90, 64, 0.18)
	playerCoralBorder   = ui.RGBA(255, 90, 64, 0.55)
	playerDividerColor  = ui.RGBA(255, 255, 255, 0.16)
	playerPopoverBg     = ui.RGBA(18, 22, 34, 0.94)
	playerPopoverBorder = ui.RGBA(255, 255, 255, 0.14)
)

const playerOverlayIdle = 3 * time.Second

type playerOverlayState struct {
	hideAt             time.Time
	pointerX, pointerY float32
	pointerOver        bool
	keepVisible        bool
}

type playerPopoverState struct {
	stream   bool
	speed    bool
	audio    bool
	subtitle bool
	danmaku  bool
	more     bool
}

func (s *playerPopoverState) anyOpen() bool {
	return s.stream || s.speed || s.audio || s.subtitle || s.danmaku || s.more
}

func (s *playerPopoverState) closeAll() {
	*s = playerPopoverState{}
}

func (a *app) closePlayerPopovers() {
	a.playerPopovers.closeAll()
}

func (a *app) togglePlayerPopover(target *bool) {
	was := *target
	a.playerPopovers.closeAll()
	*target = !was
}

func (a *app) setFullScreen(full bool) {
	a.fullScreen = full
	a.playerOverlay = playerOverlayState{}
	a.closePlayerPopovers()
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
	if s.hideAt.IsZero() || s.keepVisible || a.playerPopovers.anyOpen() || over && (!s.pointerOver || x != s.pointerX || y != s.pointerY) {
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
	ui.Themed(c.Key("app-player"), appDarkTheme(c)).Fill().Background(playerInk).Children(func() {
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

func playerBarDivider(c *ui.Context) {
	ui.Box(c).Size(1, 16).Margin(0, 3).Background(playerDividerColor).PassThrough()
}

func playerIconButton(c *ui.Context, key string, icon *ui.SVG, size float32, label, tooltip string, active, disabled bool) ui.Element {
	btn := ui.ButtonBase(c.Key(key)).Size(34, 34).Radius(10).Center().
		Cursor(ui.CursorPointer).Label(label).Tooltip(tooltip).Disabled(disabled)
	bg := ui.Transparent
	fg := playerText
	if disabled {
		fg = ui.RGBA(248, 249, 255, 0.32)
	} else if active {
		bg = ui.RGBA(255, 255, 255, 0.14)
	} else if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.12)
	}
	btn.Background(btn.AnimateColor("picon-bg", bg, animColor))
	applyPressPose(btn)
	btn.Children(func() {
		ui.Icon(c, icon).Size(size, size).TextColor(fg).PassThrough()
	})
	return btn
}

func stylePlayerPopoverPanel(panel ui.Element, width float32) ui.Element {
	return panel.Width(width).Padding(14).Gap(10).Radius(16).
		Background(playerPopoverBg).
		Border(1, playerPopoverBorder).
		Shadow(0, 14, 36, 0, ui.RGBA(0, 0, 0, 0.48)).
		TextColor(playerText)
}

func playerPopoverMenuOpts(open *bool) ui.PopoverMenuButtonOptions {
	return ui.PopoverMenuButtonOptions{
		At:   ui.AnchorTopEnd,
		Self: ui.AnchorBottomEnd,
		Open: open,
		Panel: func(panel ui.Element) {
			panel.Background(playerPopoverBg).
				Border(1, playerPopoverBorder).
				Radius(14).
				Padding(6).
				NoShadow().
				Shadow(0, 14, 36, 0, ui.RGBA(0, 0, 0, 0.48))
		},
		Item: func(item ui.Element) {
			item.Padding(6, 10).Radius(8)
		},
	}
}

func (a *app) syncMenuPopoverOpen(open bool) {
	if !open {
		return
	}
	a.playerPopovers.speed = false
	a.playerPopovers.subtitle = false
	a.playerPopovers.danmaku = false
	a.playerOverlay.keepVisible = true
}

func playerPopoverSmallChip(c *ui.Context, text, label, tooltip string) ui.Element {
	btn := ui.ButtonBase(c.Key("pop-chip-"+label)).Padding(4, 7).Radius(8).Center().
		Cursor(ui.CursorPointer).Label(label).Tooltip(tooltip).
		Border(1, ui.RGBA(255, 255, 255, 0.12))
	bg := ui.RGBA(255, 255, 255, 0.08)
	if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.16)
	}
	btn.Background(btn.AnimateColor("pchip-bg", bg, animColor))
	applyPressPose(btn)
	btn.Children(func() {
		ui.Text(c, text).FontSize(11).TextColor(playerText).SingleLine().PassThrough()
	})
	return btn
}

func playerPopoverFooterLink(c *ui.Context, text, tooltip string) ui.Element {
	btn := ui.ButtonBase(c.Key("pop-footer-"+text)).FillWidth().Padding(7, 8).Gap(6).Radius(9).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(text).Tooltip(tooltip)
	bg := ui.Transparent
	if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.09)
	}
	btn.Background(btn.AnimateColor("pfoot-bg", bg, animColor))
	applyPressPose(btn)
	btn.Children(func() {
		ui.Text(c, text).FontSize(12).TextColor(ui.RGBA(248, 249, 255, 0.84)).SingleLine().Grow(1).PassThrough()
		ui.Icon(c, iconChevronRight).Size(14, 14).TextColor(playerMutedText).PassThrough()
	})
	return btn
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
		backBtn := ui.ButtonBase(c.Key("player-back")).Size(36, 36).Radius(18).Center().
			Material(glass.Glass{Style: glass.Regular, Interactive: true}).
			Border(1, ui.RGBA(255, 255, 255, 0.14)).
			Cursor(ui.CursorPointer).Label("返回").Tooltip("返回上一页")
		applyPressPose(backBtn)
		backBtn.Children(func() {
			ui.Icon(c, iconBack).Size(18, 18).TextColor(playerText).PassThrough()
		})
		if backBtn.Clicked() {
			a.leavePlayer()
		}
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
		ui.Row(c).Padding(3).Gap(2).Radius(18).Material(glass.Glass{Style: glass.Regular}).
			Border(1, ui.RGBA(255, 255, 255, 0.12)).Children(func() {
			if !a.fullScreen {
				pinIcon := iconPin
				pinFg := playerText
				pinBg := ui.Transparent
				pinTip := "切换窗口置顶 (P)"
				pinBtn := ui.ButtonBase(c.Key("player-pin")).Size(30, 30).Radius(14).Center().
					Cursor(ui.CursorPointer).Label("窗口置顶")
				if a.playerExtra.pinned {
					pinIcon = iconPinFilled
					pinFg = playerCoral
					pinBg = ui.RGBA(255, 90, 64, 0.22)
					pinTip = "窗口已置顶 · 点击取消 (P)"
					pinBtn.Border(1, playerCoralBorder)
				} else if pinBtn.Hovered() {
					pinBg = ui.RGBA(255, 255, 255, 0.14)
				}
				pinBtn.Tooltip(pinTip).Background(pinBtn.AnimateColor("pin-bg", pinBg, animColor))
				applyPressPose(pinBtn)
				pinBtn.Children(func() {
					ui.Icon(c, pinIcon).Size(16, 16).TextColor(pinFg).PassThrough()
				})
				if pinBtn.Clicked() {
					a.toggleAlwaysOnTop()
				}
			}
			pipBtn := ui.ButtonBase(c.Key("player-pip")).Size(30, 30).Radius(14).Center().
				Cursor(ui.CursorPointer).Label("迷你小窗").Tooltip("置顶迷你小窗 (T)")
			pipBg := ui.Transparent
			if pipBtn.Hovered() {
				pipBg = ui.RGBA(255, 255, 255, 0.14)
			}
			pipBtn.Background(pipBtn.AnimateColor("pip-bg", pipBg, animColor))
			applyPressPose(pipBtn)
			pipBtn.Children(func() {
				ui.Icon(c, iconMiniPiP).Size(16, 16).TextColor(playerText).PassThrough()
			})
			if pipBtn.Clicked() {
				a.toggleMiniMode()
			}
		})
		if hasEpisodes {
			shortLabel := "选集"
			if a.playingLive {
				if len(a.live.channels) <= 1 && a.activeEPGURL() != "" {
					shortLabel = "节目单"
				} else {
					shortLabel = "频道"
				}
			} else if a.hasLocalPlaylist() {
				shortLabel = "列表"
			}
			panelShown := a.panelOpen && (!compact || a.panelTouched)
			tip := "展开" + shortLabel + " (Esc 收起)"
			if panelShown {
				tip = "收起" + shortLabel + " (Esc 收起)"
			}
			panelBtn := ui.ButtonBase(c.Key("player-panel-toggle")).Height(36).Padding(0, 12).Gap(6).Radius(18).
				Center().Cursor(ui.CursorPointer).Label(shortLabel).Tooltip(tip).
				Material(glass.Glass{Style: glass.Regular, Interactive: true})
			if panelShown {
				panelBtn.Background(ui.RGBA(255, 255, 255, 0.18)).Border(1, ui.RGBA(255, 255, 255, 0.28))
			} else {
				panelBtn.Border(1, ui.RGBA(255, 255, 255, 0.12))
			}
			applyPressPose(panelBtn)
			panelBtn.Children(func() {
				ui.Icon(c, iconPanelList).Size(15, 15).TextColor(playerText).PassThrough()
				ui.Text(c, shortLabel).FontSize(13).TextColor(playerText).SingleLine().PassThrough()
			})
			if panelBtn.Clicked() {
				if compact && !a.panelTouched {
					a.panelOpen = true
				} else {
					a.panelOpen = !a.panelOpen
				}
				a.panelTouched = true
			}
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

type resolutionTier int

const (
	resTierNone resolutionTier = iota
	resTierSD
	resTier360P
	resTier480P
	resTier720P
	resTier1080P
	resTier2K
	resTier4K
	resTier8K
)

// formatResolutionBadge classifies decoded video dimensions into a concise
// resolution tier using both long and short edges so scope-cropped movies
// (e.g. 3840x1600, 1920x800) and portrait videos keep their true tier.
func formatResolutionBadge(w, h int) (string, resolutionTier) {
	if w <= 0 || h <= 0 {
		return "", resTierNone
	}
	long, short := max(w, h), min(w, h)
	switch {
	case long >= 7600 || short >= 4000:
		return "8K", resTier8K
	case long >= 3800 || short >= 2000:
		return "4K", resTier4K
	case long >= 2500 || short >= 1400:
		return "2K", resTier2K
	case long >= 1860 || short >= 1040:
		return "1080P", resTier1080P
	case long >= 1240 || short >= 700:
		return "720P", resTier720P
	case long >= 700 || short >= 460:
		return "480P", resTier480P
	case long >= 520 || short >= 340:
		return "360P", resTier360P
	default:
		return "SD", resTierSD
	}
}

func (a *app) resolutionBadgeButton(c *ui.Context, w, h int) {
	label, tier := formatResolutionBadge(w, h)
	if label == "" {
		return
	}
	var fg, bg, border ui.Color
	bold := false
	switch tier {
	case resTier8K:
		fg = ui.RGB(252, 211, 107)
		bg = ui.RGBA(252, 211, 107, 0.14)
		border = ui.RGBA(252, 211, 107, 0.55)
		bold = true
	case resTier4K:
		fg = ui.RGB(248, 250, 255)
		bg = ui.RGBA(255, 255, 255, 0.12)
		border = ui.RGBA(255, 255, 255, 0.36)
		bold = true
	case resTier2K:
		fg = ui.RGBA(248, 249, 255, 0.90)
		bg = ui.RGBA(255, 255, 255, 0.08)
		border = ui.RGBA(255, 255, 255, 0.24)
		bold = true
	case resTier1080P:
		fg = ui.RGBA(248, 249, 255, 0.80)
		bg = ui.RGBA(255, 255, 255, 0.05)
		border = ui.RGBA(255, 255, 255, 0.20)
	case resTier720P:
		fg = ui.RGBA(248, 249, 255, 0.64)
		bg = ui.Transparent
		border = ui.RGBA(255, 255, 255, 0.14)
	case resTier480P:
		fg = ui.RGBA(248, 249, 255, 0.52)
		bg = ui.Transparent
		border = ui.RGBA(255, 255, 255, 0.10)
	default:
		fg = ui.RGBA(248, 249, 255, 0.44)
		bg = ui.Transparent
		border = ui.RGBA(255, 255, 255, 0.08)
	}
	if a.playerExtra.statsOpen {
		border = playerCoralBorder
	}
	tip := fmt.Sprintf("真实画面 %d×%d · 点击查看播放信息 (I)", w, h)
	btn := ui.ButtonBase(c.Key("res-badge")).Padding(2, 6).Radius(5).Center().
		Cursor(ui.CursorPointer).Label("视频分辨率").Tooltip(tip).Border(1, border)
	if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.16)
	}
	btn.Background(btn.AnimateColor("res-bg", bg, animColor))
	applyPressPose(btn)
	btn.Children(func() {
		t := ui.Text(c, label).FontSize(10).TextColor(fg).SingleLine().PassThrough()
		if bold {
			t.Bold()
		}
	})
	if btn.Clicked() {
		a.toggleStatsOverlay()
	}
}

func (a *app) playerControls(c *ui.Context, p mediaPlayer, hasEpisodes, compact bool) {
	ui.Row(c).Key("hud-bottom").Absolute().Left(18).Right(18).Bottom(18).Justify(ui.Center).
		Transition(transHUDBottom).Children(func() {
		ui.Column(c).FillWidth().MaxWidth(920).Padding(10, 14).Gap(6).
			Radius(22).Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(9, 15, 31, 0.66)}).
			Border(1, ui.RGBA(255, 255, 255, 0.10)).
			TextColor(playerText).Children(func() {
			duration := p.Duration()
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				switch {
				case a.isLiveStream():
					ui.Row(c).Grow(1).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Size(7, 7).Radius(4).Background(ui.RGB(239, 68, 68)).PassThrough()
						ui.Text(c, "直播").FontSize(12).SingleLine()
					})
				case duration > 0:
					ui.Text(c, playerClock(p.Position())).FontSize(12).TextColor(ui.RGBA(248, 249, 255, 0.88)).ContentTransition(ctNumeric)
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
					ui.Text(c, playerClock(duration)).FontSize(12).TextColor(ui.RGBA(248, 249, 255, 0.78)).ContentTransition(ctNumeric)
				default:
					ui.Text(c, "加载中…").FontSize(12).TextColor(ui.RGBA(248, 249, 255, 0.72)).Grow(1)
				}
				w, h := p.Size()
				if w > 0 && h > 0 {
					a.resolutionBadgeButton(c, w, h)
				}
				if !a.playingFile && !a.playingDirectMPV {
					if speed := a.playbackSpeed(c); speed != "" && speed != "—" {
						ui.Text(c, "↓ "+speed).FontSize(11).TextColor(ui.RGBA(248, 249, 255, 0.68)).SingleLine().ContentTransition(ctNumeric)
					}
				}
			})
			ui.Row(c).FillWidth().Wrap().Gap(4).AlignItems(ui.Center).Children(func() {
				// Left zone: transport + divider + volume
				if a.playingLive {
					live := &a.live
					if live.catchupProgram != nil && live.playingChannel >= 0 && live.playingChannel < len(live.channels) {
						retBtn := ui.ButtonBase(c.Key("live-return")).Height(28).Padding(0, 10).Radius(14).Center().
							Cursor(ui.CursorPointer).Label("返回直播").Tooltip("返回实时直播").
							Background(playerCoralSoft).Border(1, playerCoralBorder)
						applyPressPose(retBtn)
						retBtn.Children(func() {
							ui.Text(c, "返回直播").FontSize(12).TextColor(ui.RGB(255, 168, 146)).SingleLine().PassThrough()
						})
						if retBtn.Clicked() {
							a.playLiveChannel(live.playingChannel, live.playingStream)
						}
					}
					if len(live.channels) > 1 {
						if playerIconButton(c, "live-prev", iconPrev, 17, "上一频道", "上一频道", false, live.playingChannel <= 0).Clicked() {
							a.playLiveChannel(live.playingChannel-1, 0)
						}
					}
				}
				multiEp := a.hasMultipleEpisodes()
				localMulti := a.hasLocalPlaylist()
				source, episode, current := a.currentEpisode()
				if !a.playingLive {
					if multiEp {
						if playerIconButton(c, "ep-prev", iconPrev, 17, "上一集", "播放上一集", false, !current || episode == 0).Clicked() {
							a.playEpisode(a.title.detail.Sources[source].Name, a.title.detail.Sources[source].Episodes[episode-1])
						}
					} else if localMulti {
						if playerIconButton(c, "local-prev", iconPrev, 17, "上一集", "播放上一集", false, a.localPlaylistIdx <= 0).Clicked() {
							a.playLocalPlaylistIndex(a.localPlaylistIdx - 1)
						}
					}
				}
				playLabel := "暂停"
				playIcon := iconPause
				if !p.Playing() {
					playLabel = "播放"
					playIcon = iconPlay
				}
				playBtn := ui.ButtonBase(c.Key("player-play-toggle")).Size(36, 36).Radius(18).Center().
					Cursor(ui.CursorPointer).Label(playLabel).Tooltip("播放 / 暂停 (Space)").
					Border(1, ui.RGBA(255, 255, 255, 0.14))
				playBg := ui.RGBA(255, 255, 255, 0.18)
				if playBtn.Hovered() {
					playBg = ui.RGBA(255, 255, 255, 0.28)
				}
				playBtn.Background(playBtn.AnimateColor("play-bg", playBg, animColor))
				applyPressPose(playBtn)
				playBtn.Children(func() {
					ui.Icon(c, playIcon).Size(17, 17).TextColor(playerText).PassThrough()
				})
				if playBtn.Clicked() {
					togglePlayback(p)
					a.syncNowPlayingState()
				}
				if a.playingLive {
					live := &a.live
					if len(live.channels) > 1 {
						if playerIconButton(c, "live-next", iconNext, 17, "下一频道", "下一频道", false, live.playingChannel+1 >= len(live.channels)).Clicked() {
							a.playLiveChannel(live.playingChannel+1, 0)
						}
					}
				} else {
					if multiEp {
						if playerIconButton(c, "ep-next", iconNext, 17, "下一集", "播放下一集", false, !current || episode+1 >= len(a.title.detail.Sources[source].Episodes)).Clicked() {
							a.playEpisode(a.title.detail.Sources[source].Name, a.title.detail.Sources[source].Episodes[episode+1])
						}
					} else if localMulti {
						if playerIconButton(c, "local-next", iconNext, 17, "下一集", "播放下一集", false, a.localPlaylistIdx+1 >= len(a.localPlaylist)).Clicked() {
							a.playLocalPlaylistIndex(a.localPlaylistIdx + 1)
						}
					}
				}
				playerBarDivider(c)
				volIcon := iconVolumeHigh
				if a.muted || p.Volume() <= 0 {
					volIcon = iconVolumeMute
				}
				if playerIconButton(c, "player-mute", volIcon, 17, "静音切换", "静音 / 取消静音 (M)", false, false).Clicked() {
					a.toggleMute(p)
				}
				volume := p.Volume()
				if a.muted {
					volume = 0
				}
				volumeControl := ui.Slider(c, &volume, 0, 1).Label("调节音量").Width(78)
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
				// Right zone: Plan B unified popover icon flow + fullscreen
				if a.playingLive {
					a.livePlayerRightControls(c)
				}
				a.streamControls(c)
				a.speedControls(c, p)
				a.audioTrackControls(c, p)
				a.subtitleTrackControls(c, p)
				a.danmakuButton(c)
				a.morePlayerControls(c, p)
				if a.window != nil {
					playerBarDivider(c)
					fullLabel := "全屏"
					fullIcon := iconFullscreenEnter
					if a.fullScreen {
						fullLabel = "退出全屏"
						fullIcon = iconFullscreenExit
					}
					if playerIconButton(c, "player-fullscreen", fullIcon, 17, fullLabel, "全屏切换 (F)", false, false).Clicked() {
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
	panel := ui.Themed(c, appDarkTheme(c)).Key(key).Padding(14).Gap(12).Radius(20).
		Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(13, 17, 28, 0.84)}).
		Border(1, ui.RGBA(255, 255, 255, 0.10)).
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
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Grow(1).Children(func() {
					ui.Segmented(c, &a.panelTab, "选集", "跳过").FillEqually().Label("面板内容")
				})
				if playerIconButton(c, "panel-close", iconClose, 15, "收起面板", "收起面板 (Esc)", false, false).Size(28, 28).Radius(9).Clicked() {
					a.panelOpen = false
					a.panelTouched = true
				}
			})
		}
		if a.panelTab == 1 {
			ui.Scroll(c).FillWidth().Grow(1).Gap(10).ScrollbarStyle(danmakuScrollbar).Children(func() {
				a.panelSeriesSkips(c)
			})
			return
		}
		a.panelEpisodes(c)
	})
	return panel
}

func panelSectionHeader(c *ui.Context, title, count string) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(12).Bold().TextColor(playerMutedText).Grow(1).SingleLine()
		if count != "" {
			ui.Text(c, count).FontSize(11).TextColor(ui.RGBA(248, 249, 255, 0.48)).SingleLine().ContentTransition(ctNumeric)
		}
	})
}

func panelListRowButton(c *ui.Context, key, label, a11yLabel string, selected bool) ui.Element {
	if a11yLabel == "" {
		a11yLabel = label
	}
	btn := ui.ButtonBase(c.Key(key)).FillWidth().Padding(9, 12).Gap(8).Radius(10).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(a11yLabel)
	bg := ui.RGBA(255, 255, 255, 0.05)
	border := ui.RGBA(255, 255, 255, 0.08)
	fg := ui.RGBA(248, 249, 255, 0.88)
	if selected {
		bg = playerCoralSoft
		border = playerCoralBorder
		fg = playerText
	} else if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.10)
		fg = playerText
	}
	btn.Background(btn.AnimateColor("prow-bg", bg, animColor)).Border(1, border)
	applyPressPose(btn)
	btn.Children(func() {
		if selected {
			ui.Box(c).Size(6, 6).Radius(3).Background(playerCoral).PassThrough()
		}
		t := ui.Text(c, label).FontSize(12.5).TextColor(fg).SingleLine().Grow(1).PassThrough()
		if selected {
			t.Bold()
		}
	})
	return btn
}

func panelLineChip(c *ui.Context, key, label string, selected bool) ui.Element {
	btn := ui.ButtonBase(c.Key(key)).Padding(6, 10).Gap(6).Radius(10).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	bg := ui.RGBA(255, 255, 255, 0.06)
	border := ui.RGBA(255, 255, 255, 0.10)
	fg := ui.RGBA(248, 249, 255, 0.84)
	if selected {
		bg = playerCoralSoft
		border = playerCoralBorder
		fg = ui.RGB(255, 178, 158)
	} else if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.13)
		fg = playerText
	}
	btn.Background(btn.AnimateColor("pline-bg", bg, animColor)).Border(1, border)
	applyPressPose(btn)
	btn.Children(func() {
		if selected {
			ui.Box(c).Size(5, 5).Radius(3).Background(playerCoral).PassThrough()
		}
		t := ui.Text(c, label).FontSize(12).TextColor(fg).SingleLine().PassThrough()
		if selected {
			t.Bold()
		}
	})
	return btn
}

func (a *app) playingSourceSummary() string {
	var parts []string
	if a.title != nil {
		if label := a.title.item.Entry.Label(); label != "" {
			parts = append(parts, label)
		}
	}
	if a.playingSource != "" {
		parts = append(parts, a.playingSource)
	}
	return strings.Join(parts, " · ")
}

func (a *app) panelEpisodes(c *ui.Context) {
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Gap(12).ScrollbarStyle(danmakuScrollbar).Children(func() {
		a.panelSources(c, a.candidates())
		detail := a.title.detail
		if choice := a.panelChoice; choice != nil {
			if choice.message != "" {
				bg := ui.RGBA(255, 255, 255, 0.06)
				border := ui.RGBA(255, 255, 255, 0.14)
				fg := ui.RGBA(248, 249, 255, 0.88)
				if choice.failed {
					bg = ui.RGBA(239, 68, 68, 0.15)
					border = ui.RGBA(248, 113, 113, 0.45)
					fg = ui.RGB(254, 202, 202)
				}
				ui.Column(c).FillWidth().Padding(8, 10).Gap(4).Radius(10).Background(bg).Border(1, border).Children(func() {
					ui.Text(c, choice.message).FontSize(12).TextColor(fg)
					if playing := a.playingSourceSummary(); playing != "" {
						ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "仍在播放："+playing).FontSize(11).TextColor(playerMutedText).SingleLine().Grow(1)
							if !a.running(jobPlay) && a.title != nil {
								retBtn := ui.ButtonBase(c.Key("panel-choice-back")).Padding(2, 7).Radius(6).
									Cursor(ui.CursorPointer).Label("回到当前来源").
									Background(ui.RGBA(255, 255, 255, 0.10)).Border(1, ui.RGBA(255, 255, 255, 0.16))
								applyPressPose(retBtn)
								retBtn.Children(func() {
									ui.Text(c, "回到当前").FontSize(11).TextColor(playerText).SingleLine().PassThrough()
								})
								if retBtn.Clicked() {
									a.switchSource(a.title.item)
								}
							}
						})
					}
				})
			}
			if a.panelChoice == nil {
				detail = a.title.detail
			} else {
				detail = choice.detail
			}
		}
		if detail == nil || len(detail.Sources) == 0 {
			return
		}
		playingLine, _, hasPlaying := a.currentEpisode()
		if a.panelSource < 0 || a.panelSource >= len(detail.Sources) {
			a.panelSource = playingLine
		}
		selectedLine := a.panelSource
		if len(detail.Sources) > 1 {
			ui.Column(c).FillWidth().Gap(7).Children(func() {
				panelSectionHeader(c, "线路", fmt.Sprintf("%d 条", len(detail.Sources)))
				ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
					for i, source := range detail.Sources {
						name := source.Name
						if name == "" {
							name = fmt.Sprintf("线路 %d", i+1)
						}
						if panelLineChip(c, fmt.Sprintf("player-source-%d", i), name, i == a.panelSource).Clicked() {
							selectedLine = i
							a.selectPanelLine(detail, i)
						}
					}
				})
				if a.panelChoice == nil && hasPlaying && selectedLine != playingLine && !a.running(jobPlay) && a.playFailure == nil {
					ui.Text(c, "未匹配当前分集 · 仍在播放："+a.playingSource+"，请点击下方分集切换").FontSize(11).TextColor(ui.RGB(255, 198, 130))
				}
			})
		}
		selected := detail.Sources[selectedLine]
		solePlaying := len(selected.Episodes) == 1 && a.panelChoice == nil &&
			selected.Name == a.playingSource && selected.Episodes[0].Name == a.playingEpisode
		if len(selected.Episodes) > 0 && !solePlaying {
			ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.08)).PassThrough()
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				panelSectionHeader(c, "选集", fmt.Sprintf("共 %d 集", len(selected.Episodes)))
				ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
					for i, episode := range selected.Episodes {
						active := a.panelChoice == nil && selected.Name == a.playingSource && episode.Name == a.playingEpisode
						epLabel := episodeButtonLabel(episode.Name)
						btn := ui.ButtonBase(c.Key(fmt.Sprintf("player-episode-%d", i))).
							MinWidth(52).Height(32).Padding(0, 10).Radius(9).Center().
							Cursor(ui.CursorPointer).Label(epLabel)
						bg := ui.RGBA(255, 255, 255, 0.07)
						border := ui.RGBA(255, 255, 255, 0.09)
						fg := ui.RGBA(248, 249, 255, 0.88)
						if active {
							bg = playerCoral
							border = playerCoral
							fg = ui.RGB(255, 255, 255)
						} else if btn.Hovered() {
							bg = ui.RGBA(255, 255, 255, 0.15)
							border = ui.RGBA(255, 255, 255, 0.18)
							fg = playerText
						}
						btn.Background(btn.AnimateColor("ep-bg", bg, animColor)).Border(1, border)
						applyPressPose(btn)
						btn.Children(func() {
							t := ui.Text(c, epLabel).FontSize(12).TextColor(fg).SingleLine().PassThrough()
							if active {
								t.Bold()
							}
						})
						if btn.Clicked() {
							a.playPanelEpisode(selectedLine, episode, 0)
						}
					}
				})
			})
		}
	})
	if a.hasMultipleEpisodes() {
		ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.08)).PassThrough()
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "播完自动下一集").FontSize(12.5).TextColor(ui.RGBA(248, 249, 255, 0.86)).Grow(1)
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
	ui.Column(c).FillWidth().Gap(7).Children(func() {
		count := ""
		if len(candidates) > 1 {
			count = fmt.Sprintf("%d 个来源", len(candidates))
		}
		panelSectionHeader(c, "来源", count)
		if len(candidates) > 1 {
			retry := a.panelChoice != nil && (a.panelChoice.detail == nil || a.panelChoice.failed) && !a.running(jobPlay)
			srcBtn := ui.ButtonBase(c.Key("panel-source-select")).FillWidth().Padding(8, 12).Gap(8).Radius(10).
				AlignItems(ui.Center).Cursor(ui.CursorPointer).Label("选择来源").
				Border(1, ui.RGBA(255, 255, 255, 0.12))
			bg := ui.RGBA(255, 255, 255, 0.06)
			if srcBtn.Hovered() {
				bg = ui.RGBA(255, 255, 255, 0.12)
			}
			srcBtn.Background(srcBtn.AnimateColor("psrc-bg", bg, animColor))
			applyPressPose(srcBtn)
			srcBtn.Children(func() {
				ui.Text(c, label).FontSize(12.5).TextColor(playerText).SingleLine().Grow(1).PassThrough()
				ui.Icon(c, iconChevronDown).Size(15, 15).TextColor(playerMutedText).PassThrough()
			})
			srcBtn.PopoverMenuButton(func(m *ui.Menu) {
				for _, candidate := range candidates {
					key := itemKey(candidate)
					selected := item.Entry.Config != nil && itemKey(item) == key
					if m.Item(candidate.Entry.Label()).ID(key).Radio(selected).Disabled(selected && !retry).Chosen() {
						a.switchSource(candidate)
					}
				}
			}, ui.PopoverMenuButtonOptions{
				At:   ui.AnchorBottomStart,
				Self: ui.AnchorTopStart,
				Panel: func(panel ui.Element) {
					panel.Background(playerPopoverBg).
						Border(1, playerPopoverBorder).
						Radius(12).
						Padding(6).
						NoShadow().
						Shadow(0, 14, 36, 0, ui.RGBA(0, 0, 0, 0.48))
				},
				Item: func(item ui.Element) {
					item.Padding(6, 10).Radius(8)
				},
			})
		} else {
			ui.Row(c).FillWidth().Padding(8, 12).Radius(10).Background(ui.RGBA(255, 255, 255, 0.04)).
				Border(1, ui.RGBA(255, 255, 255, 0.07)).Children(func() {
				ui.Text(c, label).FontSize(12.5).TextColor(ui.RGBA(248, 249, 255, 0.85)).SingleLine()
			})
		}
	})
}

// panelChannels changes the channel or browses today's EPG schedule & catchup without leaving the player.
func (a *app) panelChannels(c *ui.Context) {
	live := &a.live
	hasEPG := a.activeEPGURL() != ""
	if len(live.channels) <= 1 && hasEPG {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "节目单").FontSize(15).Bold().Grow(1)
			if playerIconButton(c, "panel-close", iconClose, 15, "收起面板", "收起面板 (Esc)", false, false).Size(28, 28).Radius(9).Clicked() {
				a.panelOpen = false
				a.panelTouched = true
			}
		})
		a.panelChannelEPG(c)
		return
	}
	if !hasEPG {
		live.panelTab = 0
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "频道").FontSize(15).Bold().Grow(1)
			if playerIconButton(c, "panel-close", iconClose, 15, "收起面板", "收起面板 (Esc)", false, false).Size(28, 28).Radius(9).Clicked() {
				a.panelOpen = false
				a.panelTouched = true
			}
		})
	} else {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Grow(1).Children(func() {
				ui.Segmented(c, &live.panelTab, "频道", "节目单").FillEqually().Label("直播面板")
			})
			if playerIconButton(c, "panel-close", iconClose, 15, "收起面板", "收起面板 (Esc)", false, false).Size(28, 28).Radius(9).Clicked() {
				a.panelOpen = false
				a.panelTouched = true
			}
		})
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
		bg := ui.RGBA(255, 255, 255, 0.05)
		border := ui.RGBA(255, 255, 255, 0.08)
		btn := ui.Row(c.Key(fmt.Sprintf("panel-channel-%d", i))).FillWidth().Padding(8, 10).Gap(8).
			Radius(11).AlignItems(ui.Center).Cursor(ui.CursorPointer).
			TextColor(playerText).Label("切换到 " + channel.Name)
		if current {
			bg = playerCoralSoft
			border = playerCoralBorder
		} else if btn.Hovered() {
			bg = ui.RGBA(255, 255, 255, 0.10)
		}
		btn.Background(btn.AnimateColor("pch-bg", bg, animColor)).Border(1, border)
		applyPressPose(btn)
		btn.Children(func() {
			a.renderChannelLogo(c, channel, 26)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				label := channel.Name
				if fav {
					label = "★ " + label
				}
				t := ui.Text(c, label).FontSize(13).SingleLine()
				if current {
					t.Bold()
				}
				if cur, _ := a.channelEPGCurrent(channel); cur != nil {
					ui.Text(c, cur.Title).FontSize(11).TextColor(playerMutedText).SingleLine()
				}
			})
			if current {
				ui.Box(c).Size(6, 6).Radius(3).Background(playerCoral).PassThrough()
			}
		})
		if btn.Clicked() && !current {
			a.playLiveChannel(i, 0)
		}
	}).FillWidth().Grow(1).MinHeight(0).Gap(6).ScrollbarStyle(danmakuScrollbar)
}

// panelLocalPlaylist lists sibling videos in the same directory so a viewer
// watching a local season can jump between files or toggle auto-next.
func (a *app) panelLocalPlaylist(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "同目录视频").FontSize(15).Bold().Grow(1)
		if playerIconButton(c, "panel-close", iconClose, 15, "收起面板", "收起面板 (Esc)", false, false).Size(28, 28).Radius(9).Clicked() {
			a.panelOpen = false
			a.panelTouched = true
		}
	})
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Gap(6).ScrollbarStyle(danmakuScrollbar).Children(func() {
		for i, path := range a.localPlaylist {
			name := filepath.Base(path)
			current := i == a.localPlaylistIdx
			if panelListRowButton(c, fmt.Sprintf("panel-local-%d", i), name, "播放 "+name, current).Clicked() && !current {
				a.playLocalPlaylistIndex(i)
			}
		}
	})
	ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.08)).PassThrough()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "播完自动下一集").FontSize(12.5).TextColor(ui.RGBA(248, 249, 255, 0.86)).Grow(1)
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
	a.closePlayerPopovers()
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

func formatPlayerRate(rate float64) string {
	return strconv.FormatFloat(rate, 'f', -1, 64) + "×"
}

func (a *app) streamControls(c *ui.Context) {
	if len(a.playingStreams) <= 1 {
		return
	}
	currentLabel := "画质"
	if a.playingStreamIdx >= 0 && a.playingStreamIdx < len(a.playingStreams) {
		if l := a.playingStreams[a.playingStreamIdx].Label; l != "" {
			currentLabel = l
		}
	}
	btn := ui.ButtonBase(c.Key("player-stream-btn")).Height(30).Padding(0, 9).Radius(8).Center().
		Cursor(ui.CursorPointer).Label("切换画质").Tooltip("切换清晰度").
		Border(1, ui.RGBA(255, 255, 255, 0.16))
	bg := ui.RGBA(255, 255, 255, 0.06)
	if a.playerPopovers.stream || btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.14)
	}
	btn.Background(btn.AnimateColor("stream-bg", bg, animColor))
	applyPressPose(btn)
	btn.Children(func() {
		ui.Text(c, currentLabel).FontSize(12).TextColor(playerText).SingleLine().PassThrough()
	})
	btn.PopoverMenuButton(func(m *ui.Menu) {
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
	}, playerPopoverMenuOpts(&a.playerPopovers.stream))
	a.syncMenuPopoverOpen(a.playerPopovers.stream)
}

var playerSpeedChoices = []float64{0.5, 0.75, 1, 1.25, 1.5, 2, 3}

func (a *app) popoverDockOffset(p mediaPlayer) float32 {
	if a.canUseDanmaku() && a.preferences.Danmaku.Enabled && len(a.danmaku.comments) > 0 && p != nil && p.Duration() > 0 && !a.isLiveStream() {
		return 68
	}
	return 48
}

func (a *app) speedControls(c *ui.Context, p mediaPlayer) {
	if a.isLiveStream() {
		return
	}
	rate := p.Rate()
	if rate <= 0 {
		rate = 1
	}
	custom := math.Abs(rate-1) > 0.01
	rateLabel := formatPlayerRate(rate)
	btn := ui.ButtonBase(c.Key("player-speed-btn")).Height(30).MinWidth(38).Padding(0, 8).Radius(8).Center().
		Cursor(ui.CursorPointer).Label("播放速度").Tooltip("播放倍速 · 长按 → 键临时 2× 加速")
	bg := ui.Transparent
	fg := playerText
	if custom {
		bg = playerCoralSoft
		fg = ui.RGB(255, 168, 146)
		btn.Border(1, playerCoralBorder)
	} else if a.playerPopovers.speed {
		bg = ui.RGBA(255, 255, 255, 0.14)
	} else if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.12)
	}
	btn.Background(btn.AnimateColor("speed-bg", bg, animColor))
	applyPressPose(btn)
	btn.Children(func() {
		t := ui.Text(c, rateLabel).FontSize(12).TextColor(fg).SingleLine().ContentTransition(ctNumeric).PassThrough()
		if custom {
			t.Bold()
		}
	})
	if btn.Clicked() {
		a.togglePlayerPopover(&a.playerPopovers.speed)
	}
	if a.playerPopovers.speed {
		a.playerOverlay.keepVisible = true
	}
	ui.PopoverBase(c, btn, &a.playerPopovers.speed, func(panel ui.Element) {
		stylePlayerPopoverPanel(panel, 240).
			AttachTo(btn, ui.AnchorTopEnd, ui.AnchorBottomEnd).Margin(0, 0, a.popoverDockOffset(p), 0).AttachEdgeMargin(12)
		ui.Text(c, "播放倍速").FontSize(13).Bold()
		ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
			for _, choice := range playerSpeedChoices {
				active := math.Abs(rate-choice) < 0.01
				label := formatPlayerRate(choice)
				item := ui.ButtonBase(c.Key("speed-opt-"+label)).Width(64).Height(32).Radius(9).Center().
					Cursor(ui.CursorPointer).Label(label)
				itemBg := ui.RGBA(255, 255, 255, 0.08)
				itemFg := playerText
				if active {
					itemBg = playerCoral
					itemFg = ui.RGB(255, 255, 255)
				} else if item.Hovered() {
					itemBg = ui.RGBA(255, 255, 255, 0.16)
				}
				item.Background(item.AnimateColor("spd-opt-bg", itemBg, animColor))
				applyPressPose(item)
				item.Children(func() {
					t := ui.Text(c, label).FontSize(12).TextColor(itemFg).SingleLine().PassThrough()
					if active {
						t.Bold()
					}
				})
				if item.Clicked() {
					a.ensurePreferences()
					a.preferences.Rate = choice
					p.SetRate(choice)
					a.savePreferences()
					a.syncNowPlayingState()
					a.showPlayerOSD(label + " 倍速")
					a.playerPopovers.speed = false
				}
			}
		})
		ui.Text(c, "长按 → 键可临时 2× 加速").FontSize(11).TextColor(playerMutedText)
	})
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
	btn := playerIconButton(c, "player-audio-btn", iconAudioWave, 18, "切换音轨", "切换音轨", a.playerPopovers.audio, false)
	btn.PopoverMenuButton(func(m *ui.Menu) {
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
	}, playerPopoverMenuOpts(&a.playerPopovers.audio))
	a.syncMenuPopoverOpen(a.playerPopovers.audio)
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
	if hasActive {
		for _, tr := range tracks {
			if tr.Selected {
				a.playerExtra.lastSubID = tr.ID
				break
			}
		}
	}
	btn := ui.ButtonBase(c.Key("player-sub-btn")).Size(34, 34).Radius(10).Center().
		Cursor(ui.CursorPointer).Label("切换字幕").Tooltip("字幕与在线匹配 ([ / ] 调偏移)")
	bg := ui.Transparent
	if a.playerPopovers.subtitle {
		bg = ui.RGBA(255, 255, 255, 0.14)
	} else if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.12)
	}
	btn.Background(btn.AnimateColor("sub-btn-bg", bg, animColor))
	applyPressPose(btn)
	btn.Children(func() {
		badge := ui.Box(c).Size(22, 16).Radius(4).Center().PassThrough()
		if hasActive {
			badge.Background(playerText).Children(func() {
				ui.Icon(c, iconCCLetters).Size(15, 9).TextColor(playerInk).PassThrough()
			})
			ui.Box(c).Size(4, 4).Radius(2).Absolute().Bottom(2).Left(15).Background(playerCoral).PassThrough()
		} else {
			badge.Border(1.4, ui.RGBA(248, 249, 255, 0.65)).Children(func() {
				ui.Icon(c, iconCCLetters).Size(15, 9).TextColor(ui.RGBA(248, 249, 255, 0.78)).PassThrough()
			})
		}
	})
	if btn.Clicked() {
		a.togglePlayerPopover(&a.playerPopovers.subtitle)
	}
	if a.playerPopovers.subtitle {
		a.playerOverlay.keepVisible = true
	}
	ui.PopoverBase(c, btn, &a.playerPopovers.subtitle, func(panel ui.Element) {
		stylePlayerPopoverPanel(panel, 288).
			AttachTo(btn, ui.AnchorTopEnd, ui.AnchorBottomEnd).Margin(0, 0, a.popoverDockOffset(p), 0).AttachEdgeMargin(12)
		ui.Text(c, "字幕").FontSize(14).Bold()
		subOn := hasActive
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "显示字幕").FontSize(13).Grow(1)
			if ui.Switch(c, &subOn).Label("字幕开关").Disabled(len(tracks) == 0).Changed() {
				a.setSubtitleEnabled(p, subOn)
			}
		})
		ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.08)).PassThrough()
		if len(tracks) == 0 {
			ui.Text(c, "当前视频无可用字幕轨").FontSize(12).TextColor(playerMutedText)
		} else {
			ui.Scroll(c).FillWidth().MaxHeight(168).Gap(4).ScrollbarStyle(danmakuScrollbar).Children(func() {
				for i, tr := range tracks {
					selected := tr.Selected && visible
					label := formatSubtitleTrackLabel(tr, i)
					row := ui.ButtonBase(c.Key(fmt.Sprintf("sub-pop-%d", tr.ID))).FillWidth().Padding(7, 10).Gap(8).
						Radius(9).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
					rowBg := ui.Transparent
					if selected {
						rowBg = playerCoralSoft
						row.Border(1, playerCoralBorder)
					} else if row.Hovered() {
						rowBg = ui.RGBA(255, 255, 255, 0.08)
					}
					row.Background(row.AnimateColor("sub-row-bg", rowBg, animColor))
					applyPressPose(row)
					row.Children(func() {
						dotColor := ui.Transparent
						if selected {
							dotColor = playerCoral
						}
						ui.Box(c).Size(6, 6).Radius(3).Background(dotColor).PassThrough()
						t := ui.Text(c, label).FontSize(12).TextColor(playerText).SingleLine().Grow(1).PassThrough()
						if selected {
							t.Bold()
						}
					})
					if row.Clicked() {
						if err := tp.SelectSubtitleTrack(tr.ID); err != nil {
							a.status = "切换字幕失败：" + err.Error()
						} else {
							a.playerExtra.lastSubID = tr.ID
							a.showPlayerOSD("字幕：" + label)
						}
					}
				}
			})
			if hasActive {
				ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.08)).PassThrough()
				ui.Row(c).FillWidth().Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "偏移").FontSize(12).TextColor(playerMutedText)
					if playerPopoverSmallChip(c, "−0.5s", "字幕提前 0.5s", "字幕提前 0.5 秒 ([)").Clicked() {
						a.adjustSubtitleDelay(p, -500*time.Millisecond)
					}
					ui.Text(c, formatSubtitleDelay(a.playerExtra.subDelay)).FontSize(11).TextColor(playerText).
						TextAlign(ui.Center).Grow(1).SingleLine().ContentTransition(ctNumeric)
					if playerPopoverSmallChip(c, "+0.5s", "字幕推迟 0.5s", "字幕推迟 0.5 秒 (])").Clicked() {
						a.adjustSubtitleDelay(p, 500*time.Millisecond)
					}
					if a.playerExtra.subDelay != 0 {
						if playerPopoverSmallChip(c, "重置", "重置字幕偏移", "重置字幕偏移").Clicked() {
							a.resetSubtitleDelay(p)
						}
					}
					ui.Text(c, "[ / ]").FontSize(10).TextColor(ui.RGBA(248, 249, 255, 0.45)).SingleLine()
				})
			}
		}
		ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.08)).PassThrough()
		if playerPopoverFooterLink(c, "加载本地外挂字幕…", "选择本地字幕文件").Clicked() {
			a.closePlayerPopovers()
			a.openSubtitleFileDialog()
		}
		if playerPopoverFooterLink(c, "字号样式与在线匹配…", "调整字幕样式或在线搜索匹配").Clicked() {
			a.openSubtitleDialog()
		}
	})
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

func (a *app) livePlayerRightControls(c *ui.Context) {
	live := &a.live
	if live.playingChannel < 0 || live.playingChannel >= len(live.channels) {
		return
	}
	ch := live.channels[live.playingChannel]
	if count := len(ch.Streams); count > 1 {
		chipText := fmt.Sprintf("线路 %d/%d", live.playingStream+1, count)
		chipLabel := fmt.Sprintf("切换线路 %d/%d", live.playingStream+1, count)
		lineBtn := ui.ButtonBase(c.Key("live-stream-switch")).Height(30).Padding(0, 10).Radius(8).Center().
			Cursor(ui.CursorPointer).Label(chipLabel).Tooltip("切换直播线路").
			Border(1, ui.RGBA(255, 255, 255, 0.16))
		bg := ui.RGBA(255, 255, 255, 0.06)
		if lineBtn.Hovered() {
			bg = ui.RGBA(255, 255, 255, 0.14)
		}
		lineBtn.Background(lineBtn.AnimateColor("line-bg", bg, animColor))
		applyPressPose(lineBtn)
		lineBtn.Children(func() {
			ui.Text(c, chipText).FontSize(12).TextColor(playerText).SingleLine().PassThrough()
		})
		if lineBtn.Clicked() {
			a.playLiveChannel(live.playingChannel, (live.playingStream+1)%count)
		}
	}
	fav := live.state.isFavorite(ch.Name)
	starIcon := iconStarOutline
	starColor := playerMutedText
	favTip := "收藏当前频道"
	if fav {
		starIcon = iconStarFilled
		starColor = ui.RGB(251, 191, 36)
		favTip = "已收藏 · 点击取消收藏"
	}
	favBtn := ui.ButtonBase(c.Key("live-fav-btn")).Size(34, 34).Radius(10).Center().
		Cursor(ui.CursorPointer).Label("收藏当前频道").Tooltip(favTip)
	favBg := ui.Transparent
	if favBtn.Hovered() {
		favBg = ui.RGBA(255, 255, 255, 0.12)
	}
	favBtn.Background(favBtn.AnimateColor("fav-bg", favBg, animColor))
	applyPressPose(favBtn)
	favBtn.Children(func() {
		ui.Icon(c, starIcon).Size(17, 17).TextColor(starColor).PassThrough()
	})
	if favBtn.Clicked() {
		a.toggleLiveChannelFavorite(ch)
	}
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
