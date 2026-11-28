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
	playerInk                 = ui.RGB(9, 15, 31)
	playerText                = ui.RGB(248, 249, 255)
	playerMutedText           = ui.RGBA(248, 249, 255, 0.70)
	playerCoral               = ui.RGBA(255, 90, 64, 0.94)
	playerCoralSoft           = ui.RGBA(255, 90, 64, 0.18)
	playerCoralBorder         = ui.RGBA(255, 90, 64, 0.55)
	playerEpisodeActive       = ui.RGB(233, 88, 59)
	playerEpisodeActiveBorder = ui.RGB(255, 118, 91)
	playerDividerColor        = ui.RGBA(255, 255, 255, 0.16)
	playerPopoverBg           = ui.RGBA(18, 22, 34, 0.94)
	playerPopoverBorder       = ui.RGBA(255, 255, 255, 0.14)
	playerSourceMenuBg        = ui.RGB(36, 38, 48)
	playerSourceMenuBorder    = ui.RGBA(255, 255, 255, 0.22)

	// Shared floating chrome baseline derived from the native bottom playback dock
	// so the playback bar and floating episode panel use the exact same Glass,
	// tint, opacity, border, radius, and shadow.
	playerFloatingGlass  = glass.Glass{Style: glass.Clear, Tint: ui.RGBA(9, 15, 31, 0.66)}
	playerFloatingSolid  = ui.RGBA(14, 18, 32, 0.94)
	playerFloatingBorder = ui.RGBA(255, 255, 255, 0.12)
	playerFloatingShadow = ui.RGBA(0, 0, 0, 0.44)

	panelScrollbar = ui.ScrollbarStyle{
		TrackWidth: 6,
		ThumbWidth: 3.5,
		Radius:     2,
		MinLength:  24,
		Thumb:      ui.RGBA(255, 255, 255, 0.28),
		Hover:      ui.RGBA(255, 255, 255, 0.46),
		Active:     ui.RGBA(255, 255, 255, 0.64),
	}
)

const (
	playerOverlayIdle            = 3 * time.Second
	playerFloatingRadius float32 = 22
)

func stylePlayerFloatingSurface(c *ui.Context, el ui.Element) ui.Element {
	prefs := c.Preferences()
	border := playerFloatingBorder
	if prefs.HighContrast {
		border = ui.RGBA(255, 255, 255, 0.28)
	}
	el = el.Radius(playerFloatingRadius).
		Border(1, border).
		Shadow(0, 14, 34, -8, playerFloatingShadow).
		TextColor(playerText)
	if prefs.ReduceTransparency {
		return el.Background(playerFloatingSolid)
	}
	return el.Material(playerFloatingGlass)
}

func stylePlayerFloatingPill(c *ui.Context, el ui.Element, radius float32, interactive bool) ui.Element {
	prefs := c.Preferences()
	border := playerFloatingBorder
	if prefs.HighContrast {
		border = ui.RGBA(255, 255, 255, 0.28)
	}
	el = el.Radius(radius).
		Border(1, border).
		Shadow(0, 10, 26, -8, playerFloatingShadow).
		TextColor(playerText)
	if prefs.ReduceTransparency {
		return el.Background(playerFloatingSolid)
	}
	g := playerFloatingGlass
	g.Interactive = interactive
	return el.Material(g)
}

type playerOverlayState struct {
	hideAt             time.Time
	pointerX, pointerY float32
	pointerOver        bool
	keepVisible        bool
}

type playerPopoverState struct {
	source   bool
	stream   bool
	speed    bool
	audio    bool
	subtitle bool
	danmaku  bool
	more     bool
}

func (s *playerPopoverState) anyOpen() bool {
	return s.source || s.stream || s.speed || s.audio || s.subtitle || s.danmaku || s.more
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

func (a *app) openEpisodePanel() {
	a.panelOpen = true
	a.panelTouched = true
	a.panelSkipOpen = true
	a.panelRevealEpisode = true
	a.playerPopovers.source = false
	a.failureUI.panelFromRecovery = false
	a.failureUI.pendingSourceMenu = false
	a.failureUI.revealSourceTrigger = false
}

func (a *app) closeEpisodePanel() {
	a.panelOpen = false
	a.panelTouched = true
	a.playerPopovers.source = false
	a.failureUI.panelFromRecovery = false
	a.failureUI.pendingSourceMenu = false
	a.failureUI.revealSourceTrigger = false
}

func (a *app) setFullScreen(full bool) {
	a.fullScreen = full
	a.playerOverlay = playerOverlayState{}
	a.clearSeekPreview()
	a.closePlayerPopovers()
	a.playerPointer.clickAt = time.Time{}
	if full {
		a.panelOpen, a.panelTouched, a.panelWasShown = false, false, false
	}
}

// usesFloatingVODStage reports whether the player uses the unified floating
// windowed stage (video aspect box, aligned header/dock, and right-aligned
// floating side panel) across VOD, local video, live IPTV, and direct streams.
func (a *app) usesFloatingVODStage() bool {
	return !a.fullScreen && !a.playerExtra.miniMode
}

func (a *app) hasDanmakuHighlightCurve(p mediaPlayer) bool {
	return p != nil && !a.isLiveStream() && a.canUseDanmaku() &&
		a.preferences.Danmaku.Enabled && len(a.danmaku.comments) > 0 && p.Duration() > 0
}

func responsivePanelMetrics(winW float32) (outer, apart, aside float32) {
	if winW <= 700 {
		return 12, 10, 224
	}
	outer = min(max(winW*0.015, 16), 28)
	apart = min(max(winW*0.010, 12), 18)
	aside = min(max(winW*0.20, 240), 280)
	return outer, apart, aside
}

func effectiveVideoDimensions(videoW, videoH int, aspect videoAspectMode, rotation int) (int, int) {
	switch aspect {
	case aspect16x9:
		return 16, 9
	case aspect4x3:
		return 4, 3
	case aspect21x9:
		return 21, 9
	}
	if videoW <= 0 || videoH <= 0 {
		return 16, 9
	}
	if rotation == 90 || rotation == 270 {
		return videoH, videoW
	}
	return videoW, videoH
}

func effectiveVideoAspectRatio(videoW, videoH int, aspect videoAspectMode, rotation int) float32 {
	w, h := effectiveVideoDimensions(videoW, videoH, aspect, rotation)
	return float32(w) / float32(h)
}

type playerStageLayout struct {
	picture      ui.Rect
	header       ui.Rect
	dock         ui.Rect
	dockBottom   float32
	panel        ui.Rect
	outer        float32
	apart        float32
	aside        float32
	headerGap    float32
	dockGap      float32
	overlayPanel bool
	compactDock  bool
}

func computePlayerStageLayout(
	winW, winH float32,
	titleBarBottom float32,
	videoW, videoH int,
	aspect videoAspectMode,
	rotation int,
	showPanel bool,
	hasDanmakuCurve bool,
) playerStageLayout {
	outer, apart, aside := responsivePanelMetrics(winW)

	tightV := winH <= 500
	headerH := float32(36)
	headerGap := float32(10)
	dockGap := float32(10)
	topOuter := float32(12)
	bottomOuter := float32(18)
	if tightV {
		headerH = 34
		headerGap = 8
		dockGap = 8
		topOuter = 8
		bottomOuter = 10
	}
	topPad := topOuter
	if titleBarBottom > 0 {
		titleGap := float32(8)
		if tightV {
			titleGap = 4
		}
		if t := titleBarBottom + titleGap; t > topPad {
			topPad = t
		}
	}

	// Pin header cleanly to the top of the window and dock to the bottom so the
	// player fills the entire window instead of shrinking into a centered box.
	header := ui.Rect{
		X: outer,
		Y: topPad,
		W: max(float32(160), winW-2*outer),
		H: headerH,
	}

	compactDock := winW <= 700 || tightV
	estDockH := float32(80)
	if compactDock {
		estDockH = 72
	}
	if hasDanmakuCurve {
		estDockH += 18
	}
	dockW := min(max(float32(160), winW-2*outer), float32(920))
	dockX := float32(math.Round(float64((winW - dockW) / 2)))
	dock := ui.Rect{
		X: dockX,
		Y: winH - bottomOuter - estDockH,
		W: float32(math.Round(float64(dockW))),
		H: estDockH,
	}
	if dock.W < 460 {
		compactDock = true
	}

	topSafe := header.Y + header.H + headerGap
	bottomSafe := dock.Y - dockGap

	// Video always fills the full window canvas so opening or closing the
	// floating panel never shrinks, squashes, or jumps the picture.
	fullCanvas := ui.Rect{X: 0, Y: 0, W: winW, H: winH}
	actualPic := fullCanvas
	if aspect != aspectFill {
		effW, effH := effectiveVideoDimensions(videoW, videoH, aspect, rotation)
		if effW > 0 && effH > 0 {
			actualPic = playerPictureRect(fullCanvas, effW, effH)
		}
	}

	// Floating right panel overlays the right side without resizing the video:
	// align with the displayed picture vertically and clamp inside the safe
	// band between top header and bottom playback bar.
	var panel ui.Rect
	if showPanel {
		pw := min(aside, max(float32(160), winW-2*outer))
		panelTop := max(topSafe, actualPic.Y)
		panelBot := min(bottomSafe, actualPic.Y+actualPic.H)
		if panelBot-panelTop < 140 {
			panelTop = topSafe
			panelBot = bottomSafe
		}
		panel = ui.Rect{
			X: winW - outer - pw,
			Y: float32(math.Round(float64(panelTop))),
			W: pw,
			H: float32(math.Round(float64(max(float32(120), panelBot-panelTop)))),
		}
	}

	return playerStageLayout{
		picture:      actualPic,
		header:       header,
		dock:         dock,
		dockBottom:   bottomOuter,
		panel:        panel,
		outer:        outer,
		apart:        apart,
		aside:        aside,
		headerGap:    headerGap,
		dockGap:      dockGap,
		overlayPanel: true,
		compactDock:  compactDock,
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
	width, height := c.Size()
	compact := width < 900
	overlayPanel := compact || a.fullScreen
	// Show the side panel only when there is actually something to choose or configure:
	// multiple episodes/sources/lines, an active source-switch preview, live channels/EPG,
	// or sibling local video files.
	hasEpisodes := a.hasUsefulPlayerPanel()
	floatingVOD := a.usesFloatingVODStage()
	ui.Themed(c.Key("app-player"), appDarkTheme(c)).Fill().Background(ui.RGB(0, 0, 0)).Children(func() {
		screen := ui.Box(c).Fill().Background(ui.RGB(0, 0, 0)).ObserveInput(func(ev ui.InputEvent) {
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
		if a.hasEpisodes() {
			openingSourceMenu := showPanel && a.failureUI.pendingSourceMenu
			if openingSourceMenu {
				a.failureUI.pendingSourceMenu = false
				a.playerPopovers.source = true
			}
			if showPanel && !a.panelWasShown {
				a.panelSkipOpen = true
				if !openingSourceMenu && !a.playerPopovers.source {
					a.panelRevealEpisode = true
				}
			}
			if !showPanel {
				a.playerPopovers.source = false
				if !a.panelOpen {
					a.failureUI.pendingSourceMenu = false
				}
			}
			a.panelWasShown = showPanel
			epKey := a.playingSource + "\x00" + a.playingEpisode
			if epKey != a.panelLastEpisodeKey {
				a.panelLastEpisodeKey = epKey
				if !openingSourceMenu && !a.playerPopovers.source {
					a.panelRevealEpisode = true
				}
			}
		} else if a.hasLocalPlaylist() {
			if showPanel && !a.panelWasShown {
				a.panelSkipOpen = true
				a.panelRevealEpisode = true
			}
			a.panelWasShown = showPanel
			localKey := fmt.Sprintf("local\x00%d\x00%s", a.localPlaylistIdx, a.playingFilePath)
			if localKey != a.panelLastEpisodeKey {
				a.panelLastEpisodeKey = localKey
				a.panelRevealEpisode = true
			}
		}
		if !showControls && !a.playerExtra.miniMode {
			screen.Cursor(ui.CursorNone)
		}
		if (!showControls || a.playerExtra.miniMode) && a.hasSeekPreview() {
			a.clearSeekPreview()
		}
		videoHitTest := func(x, y float32, r ui.Rect) bool {
			if a.playerExtra.miniMode {
				return miniDragH <= 0 || y >= r.Y+miniDragH
			}
			if a.playerExtra.aspect == aspectFill {
				return true
			}
			w, h := p.Size()
			effW, effH := effectiveVideoDimensions(w, h, a.playerExtra.aspect, a.playerExtra.rotation)
			return a.fullScreen || playerPictureRect(r, effW, effH).Contains(x, y)
		}
		screen.Children(func() {
			if floatingVOD {
				vw, vh := p.Size()
				stageAspect := a.playerExtra.aspect
				if (vw <= 0 || vh <= 0) && a.hasPlayerFailureOverlay(p) {
					stageAspect = aspectFill
				}
				stage := computePlayerStageLayout(
					width, height, c.TitleBar().Bottom,
					vw, vh, stageAspect, a.playerExtra.rotation,
					showPanel, a.hasDanmakuHighlightCurve(p),
				)
				ui.Box(c.Key("player-picture-stage")).Fill().Clip().Background(ui.RGB(0, 0, 0)).Children(func() {
					p.View(c).Key(p).Fill().Fit(a.playerVideoFit()).Label("视频画面").Focusable().AutoFocus().Bind(&a.playerFocus).
						HitTest(videoHitTest).HandleInput(func(ev ui.InputEvent) bool { return a.handlePlayerPointer(p, ev) })
					a.danmakuOverlay(c, p, showControls)
					failTop := stage.header.Y + stage.header.H + stage.headerGap
					failBottom := max(float32(16), height-stage.dock.Y+stage.dockGap)
					if sym := max(failTop, failBottom); height-2*sym >= 150 {
						failTop, failBottom = sym, sym
					}
					failLeft := stage.outer
					failRight := stage.outer
					panelRightReserve := float32(0)
					if showPanel {
						panelRightReserve = stage.outer + stage.panel.W + stage.apart
						failRight = panelRightReserve
					}
					failW := max(float32(200), width-failLeft-failRight)
					failH := max(float32(120), height-failTop-failBottom)
					a.playerState(c, p, failW, failH, failTop, failBottom, failLeft, failRight)
					a.playbackStatsOverlay(c, p)
					a.rightHoldBoostPill(c)
					a.playerOSDPill(c)
					a.skipUndoPill(c, p, showControls)
					a.upNextPill(c, p, showControls, panelRightReserve)
				})
				if showControls {
					a.playerHeader(c, hasEpisodes, compact, &stage.header)
					a.playerControls(c, p, hasEpisodes, stage.compactDock, &stage.dock, stage.dockBottom)
				}
				if showPanel {
					a.playerPanel(c, "player-panel-floating", stage.panel.W, stage.panel.H).
						Absolute().Left(stage.panel.X).Top(stage.panel.Y).Size(stage.panel.W, stage.panel.H).
						Transition(transSideDrawer)
				}
				return
			}
			ui.Row(c).Fill().Children(func() {
				ui.Box(c).Grow(1).FillHeight().Clip().Children(func() {
					p.View(c).Key(p).Fill().Fit(a.playerVideoFit()).Label("视频画面").Focusable().AutoFocus().Bind(&a.playerFocus).
						HitTest(videoHitTest).HandleInput(func(ev ui.InputEvent) bool { return a.handlePlayerPointer(p, ev) })
					a.danmakuOverlay(c, p, showControls)
					if a.playerExtra.miniMode {
						a.playerMiniDragStrip(c, miniDragH, showControls)
					}
					failTop, failBottom, failLeft, failRight := float32(60), float32(116), float32(16), float32(16)
					if a.playerExtra.miniMode {
						failTop, failBottom, failLeft, failRight = miniDragH+6, 46, 8, 8
					} else {
						if bar := c.TitleBar(); bar.Bottom+48 > failTop {
							failTop = bar.Bottom + 48
						}
						if a.hasDanmakuHighlightCurve(p) {
							failBottom = 136
						}
						if height <= 500 {
							failTop = max(float32(48), c.TitleBar().Bottom+42)
							failBottom = 88
						}
						if showPanel && overlayPanel {
							outer, apart, aside := responsivePanelMetrics(width)
							failRight = outer + aside + apart
						}
					}
					failW := max(float32(200), width-failLeft-failRight)
					failH := max(float32(120), height-failTop-failBottom)
					a.playerState(c, p, failW, failH, failTop, failBottom, failLeft, failRight)
					a.playbackStatsOverlay(c, p)
					a.rightHoldBoostPill(c)
					a.playerOSDPill(c)
					a.skipUndoPill(c, p, showControls)
					upNextReserve := float32(0)
					if showPanel && overlayPanel {
						outer, apart, aside := responsivePanelMetrics(width)
						upNextReserve = outer + aside + apart
					}
					a.upNextPill(c, p, showControls, upNextReserve)
					if showControls {
						if a.playerExtra.miniMode {
							a.playerMiniControls(c, p)
							if miniKept && !a.playerOverlay.keepVisible {
								c.Invalidate()
							}
						} else {
							a.playerControls(c, p, hasEpisodes, width <= 700, nil, 0)
							a.playerHeader(c, hasEpisodes, compact, nil)
						}
					}
				})
				if showPanel && !overlayPanel {
					ui.Box(c).Key("player-panel-dock").Width(320).FillHeight().Padding(10, 10, 10, 0).
						Transition(transSideDock).Children(func() {
						a.playerPanel(c, "player-panel-dock-body", 310, max(float32(200), height-20)).Fill()
					})
				}
			})
			if showPanel && overlayPanel {
				outer, _, aside := responsivePanelMetrics(width)
				top := float32(60)
				bottom := float32(118)
				if a.hasDanmakuHighlightCurve(p) {
					bottom = 138
				}
				if height <= 500 {
					top = 50
					bottom = 90
				}
				panelH := max(float32(160), height-top-bottom)
				a.playerPanel(c, "player-panel-drawer", aside, panelH).
					Absolute().Top(top).Right(outer).Size(aside, panelH).
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
	animFg := btn.AnimateColor("picon-fg", fg, animColor)
	applyPressPose(btn)
	btn.Children(func() {
		ui.Icon(c, icon).Size(size, size).TextColor(animFg).ContentTransition(ctIcon).PassThrough()
	})
	return btn
}

func stylePlayerPopoverPanel(panel ui.Element, width float32) ui.Element {
	return panel.Width(width).Padding(14).Gap(10).Radius(16).
		Background(playerPopoverBg).
		Border(1, playerPopoverBorder).
		Shadow(0, 14, 36, 0, ui.RGBA(0, 0, 0, 0.48)).
		TextColor(playerText).
		Transition(transPopoverUp)
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
				Shadow(0, 14, 36, 0, ui.RGBA(0, 0, 0, 0.48)).
				Transition(transPopoverUp)
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

// playerHeaderTooltip places top-bar button tooltips below the header so they
// never collide with native macOS traffic lights or Windows caption buttons.
func playerHeaderTooltip(c *ui.Context, anchor ui.Element, text string, at, self ui.Anchor) {
	if text == "" {
		return
	}
	anchor.Description(text)
	prefs := c.Preferences()
	bg := ui.RGBA(20, 24, 36, 0.96)
	border := ui.RGBA(255, 255, 255, 0.16)
	if prefs.ReduceTransparency {
		bg = ui.RGB(20, 24, 36)
	}
	if prefs.HighContrast {
		border = ui.RGBA(255, 255, 255, 0.34)
	}
	t := c.Theme()
	ui.TooltipBase(c, anchor, func(tip ui.Element) {
		tip.Key("tip-player-header-"+text).MaxWidth(t.Space(80)).Padding(5, 9).Radius(8).
			Background(bg).Border(1, border).
			TextColor(playerText).FontSize(max(float32(11), t.FontSize-1)).
			Shadow(0, 6, 18, 0, ui.RGBA(0, 0, 0, 0.45)).
			Transition(transTooltip)
		tip.Children(func() { ui.Text(c, text) })
		tip.AttachTo(anchor, at, self).Margin(6, 0, 0, 0).AttachEdgeMargin(8)
	})
}

func (a *app) playerHeader(c *ui.Context, hasEpisodes, compact bool, floatingRect *ui.Rect) {
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
	if a.playingFile || a.playingMedia {
		name = a.playingName
	}

	barH := float32(36)
	gap := float32(9)
	narrowHeader := false
	row := ui.Row(c).Key("hud-top").Absolute().AlignItems(ui.Center).Transition(transHUDTop)
	if floatingRect != nil {
		barH = floatingRect.H
		narrowHeader = floatingRect.W < 440
		if narrowHeader {
			gap = 6
		}
		row.Left(floatingRect.X).Top(floatingRect.Y).Width(floatingRect.W).Height(barH).Gap(gap)
	} else {
		top := float32(12)
		if bar := c.TitleBar(); bar.Bottom+8 > top {
			top = bar.Bottom + 8
		}
		left := max(float32(88), c.TitleBar().Left+14)
		row.Top(top).Left(left).Right(16).Gap(10)
	}
	pillRadius := barH / 2
	row.Children(func() {
		backBtn := ui.ButtonBase(c.Key("player-back")).Size(barH, barH).Center().
			Cursor(ui.CursorPointer).Label("返回")
		playerHeaderTooltip(c, backBtn, "返回上一页", ui.AnchorBottomStart, ui.AnchorTopStart)
		if floatingRect != nil {
			stylePlayerFloatingPill(c, backBtn, pillRadius, true)
		} else {
			backBtn.Radius(18).Material(glass.Glass{Style: glass.Regular, Interactive: true}).
				Border(1, ui.RGBA(255, 255, 255, 0.14))
		}
		applyPressPose(backBtn)
		backBtn.Children(func() {
			ui.Icon(c, iconBack).Size(17, 17).TextColor(playerText).PassThrough()
		})
		if backBtn.Clicked() {
			a.leavePlayer()
		}

		titlePadX := float32(14)
		if narrowHeader {
			titlePadX = 10
		}
		title := ui.Row(c).Height(barH).Padding(0, titlePadX).AlignItems(ui.Center).Grow(1).MinWidth(0)
		if floatingRect != nil {
			stylePlayerFloatingPill(c, title, pillRadius, false)
		} else {
			title.Radius(20).Material(glass.Glass{Style: glass.Clear})
		}
		if dragStripHeight(c) > 0 {
			title.DragWindow().Label("拖动窗口")
		}
		title.Children(func() {
			ui.Text(c, name).SingleLine().TextColor(playerText).TextShadow(0, 1, 4, playerInk).ContentTransition(ctReplace)
			if badge := a.sleepTimerBadge(c.Now()); badge != "" {
				ui.Text(c.Key("sleep-badge"), " · "+badge).FontSize(12).FontFeatures("tnum").SingleLine().
					TextColor(ui.RGB(255, 198, 130)).ContentTransition(ctCountdown).Transition(transMicroBadge)
			}
			if a.playerExtra.loop {
				ui.Text(c.Key("loop-badge"), " · 循环").FontSize(12).SingleLine().
					TextColor(ui.RGB(142, 230, 178)).Transition(transMicroBadge)
			}
		})

		toolBtnSize := barH - 6
		toolsPill := ui.Row(c).Height(barH).Padding(0, 3).Gap(2).AlignItems(ui.Center).Shrink(0)
		if floatingRect != nil {
			stylePlayerFloatingPill(c, toolsPill, pillRadius, false)
		} else {
			toolsPill.Radius(18).Material(glass.Glass{Style: glass.Regular}).
				Border(1, ui.RGBA(255, 255, 255, 0.12))
		}
		toolsPill.Children(func() {
			if !a.fullScreen {
				pinIcon := iconPin
				pinFg := playerText
				pinBg := ui.Transparent
				pinBorder := ui.Transparent
				pinTip := "切换窗口置顶 (P)"
				pinBtn := ui.ButtonBase(c.Key("player-pin")).Size(toolBtnSize, toolBtnSize).Radius(toolBtnSize/2).Center().
					Cursor(ui.CursorPointer).Label("窗口置顶")
				if a.playerExtra.pinned {
					pinIcon = iconPinFilled
					pinFg = playerCoral
					pinBg = ui.RGBA(255, 90, 64, 0.22)
					pinBorder = playerCoralBorder
					pinTip = "窗口已置顶 · 点击取消 (P)"
				} else if pinBtn.Hovered() {
					pinBg = ui.RGBA(255, 255, 255, 0.14)
				}
				animPinFg := animateButtonChrome(pinBtn, "pin", pinBg, pinBorder, pinFg)
				playerHeaderTooltip(c, pinBtn, pinTip, ui.AnchorBottom, ui.AnchorTop)
				applyPressPose(pinBtn)
				pinBtn.Children(func() {
					ui.Icon(c, pinIcon).Size(15, 15).TextColor(animPinFg).ContentTransition(ctIcon).PassThrough()
				})
				if pinBtn.Clicked() {
					a.toggleAlwaysOnTop()
				}
			}
			pipBtn := ui.ButtonBase(c.Key("player-pip")).Size(toolBtnSize, toolBtnSize).Radius(toolBtnSize/2).Center().
				Cursor(ui.CursorPointer).Label("迷你小窗")
			playerHeaderTooltip(c, pipBtn, "置顶迷你小窗 (T)", ui.AnchorBottom, ui.AnchorTop)
			pipBg := ui.Transparent
			if pipBtn.Hovered() {
				pipBg = ui.RGBA(255, 255, 255, 0.14)
			}
			pipBtn.Background(pipBtn.AnimateColor("pip-bg", pipBg, animColor))
			applyPressPose(pipBtn)
			pipBtn.Children(func() {
				ui.Icon(c, iconMiniPiP).Size(15, 15).TextColor(playerText).PassThrough()
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
			padX := float32(12)
			if narrowHeader {
				padX = 9
			}
			panelBtn := ui.ButtonBase(c.Key("player-panel-toggle")).Height(barH).Padding(0, padX).Gap(6).
				Center().Cursor(ui.CursorPointer).Label(shortLabel).Expanded(panelShown).Shrink(0)
			playerHeaderTooltip(c, panelBtn, tip, ui.AnchorBottomEnd, ui.AnchorTopEnd)
			if floatingRect != nil {
				stylePlayerFloatingPill(c, panelBtn, pillRadius, true)
				if panelShown {
					panelBtn.Background(panelBtn.AnimateColor("ptog-bg", ui.RGBA(255, 255, 255, 0.16), animColor)).
						Border(1, panelBtn.AnimateColor("ptog-bd", ui.RGBA(255, 255, 255, 0.28), animColor))
				}
			} else {
				panelBtn.Radius(18).Material(glass.Glass{Style: glass.Regular, Interactive: true})
				ptogBg, ptogBd := ui.Transparent, ui.RGBA(255, 255, 255, 0.12)
				if panelShown {
					ptogBg, ptogBd = ui.RGBA(255, 255, 255, 0.16), ui.RGBA(255, 255, 255, 0.28)
				}
				panelBtn.Background(panelBtn.AnimateColor("ptog-bg", ptogBg, animColor)).
					Border(1, panelBtn.AnimateColor("ptog-bd", ptogBd, animColor))
			}
			applyPressPose(panelBtn)
			panelBtn.Children(func() {
				ui.Icon(c, iconPanelList).Size(15, 15).TextColor(playerText).PassThrough()
				ui.Text(c, shortLabel).FontSize(13).TextColor(playerText).SingleLine().PassThrough()
			})
			if panelBtn.Clicked() {
				if panelShown {
					a.closeEpisodePanel()
				} else {
					a.openEpisodePanel()
				}
			}
		}
	})
}

func (a *app) playerState(c *ui.Context, p mediaPlayer, availW, availH, safeTop, safeBottom, safeLeft, safeRight float32) {
	a.syncToastState(c, a.busy())
	message := ""
	dismissible := false
	switch {
	case a.hasPlayerFailureOverlay(p):
		var err error
		if a.playFailure != nil {
			err = a.playFailure.err
		} else {
			err = p.Err()
		}
		a.playerFailureOverlay(c, p, err, availW, availH, safeTop, safeBottom, safeLeft, safeRight)
		return
	case a.running(jobPlay):
		message = a.status
	case p.Err() != nil:
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
	top := float32(84)
	loadingState := a.running(jobPlay) || (!p.Ready() && p.Err() == nil) || p.Buffering()
	ui.Row(c).Key("player-state-pill").Absolute().Top(top).Left(24).Right(24).Center().Transition(transOSD).Children(func() {
		ui.Row(c).Padding(9, 14).Gap(10).Radius(14).MaxWidth(640).AlignItems(ui.Center).
			Material(glass.Glass{Style: glass.Regular}).TextColor(playerText).Children(func() {
			if loadingState {
				loadingIndicator(c, 13, playerText)
			}
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
		Cursor(ui.CursorPointer).Label("视频分辨率").Tooltip(tip)
	if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.16)
	}
	animFg := animateButtonChrome(btn, "res", bg, border, fg)
	applyPressPose(btn)
	btn.Children(func() {
		t := ui.Text(c, label).FontSize(10).TextColor(animFg).SingleLine().ContentTransition(ctReplace).PassThrough()
		if bold {
			t.Bold()
		}
	})
	if btn.Clicked() {
		a.toggleStatsOverlay()
	}
}

func (a *app) playerControls(c *ui.Context, p mediaPlayer, hasEpisodes, compact bool, dockRect *ui.Rect, dockBottom float32) {
	padY, padX := float32(9), float32(14)
	colGap := float32(5)
	transportGap := float32(4)
	iconBtnSize := float32(32)
	playBtnSize := float32(34)
	volWidth := float32(72)
	if compact {
		padY, padX = 6, 10
		colGap = 4
		transportGap = 2
		iconBtnSize = 28
		playBtnSize = 30
		volWidth = 34
	}

	buildDockBody := func() {
		duration := p.Duration()
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.End).Children(func() {
			switch {
			case a.isLiveStream():
				ui.Row(c).Grow(1).Height(20).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Box(c.Key("live-onair-dot")).Size(7, 7).Radius(4).Background(ui.RGB(239, 68, 68)).PassThrough()
					ui.Text(c, "直播").FontSize(12).SingleLine()
				})
			case duration > 0:
				clockW := float32(38)
				if duration >= 10*time.Hour {
					clockW = 62
				} else if duration >= time.Hour {
					clockW = 54
				}
				ui.Row(c).Width(clockW).Height(20).Justify(ui.End).AlignItems(ui.Center).Shrink(0).Children(func() {
					ui.Text(c, playerClock(p.Position())).FontSize(12).FontFeatures("tnum").
						TextColor(ui.RGBA(248, 249, 255, 0.88)).SingleLine().ContentTransition(ctNumeric)
				})
				a.playerSeekBar(c, p, duration)
				ui.Row(c).Height(20).AlignItems(ui.Center).Shrink(0).Children(func() {
					ui.Text(c, playerClock(duration)).FontSize(12).FontFeatures("tnum").
						TextColor(ui.RGBA(248, 249, 255, 0.78)).SingleLine().ContentTransition(ctNumeric)
				})
			default:
				ui.Row(c).Grow(1).Height(20).Gap(6).AlignItems(ui.Center).Children(func() {
					loadingIndicator(c, 11, ui.RGBA(248, 249, 255, 0.72))
					ui.Text(c, "加载中…").FontSize(12).TextColor(ui.RGBA(248, 249, 255, 0.72)).SingleLine()
				})
			}
			w, h := p.Size()
			if w > 0 && h > 0 {
				ui.Row(c).Height(20).AlignItems(ui.Center).Children(func() {
					a.resolutionBadgeButton(c, w, h)
				})
			}
			if !a.playingFile && !a.playingDirectMPV {
				if speed := a.playbackSpeed(c); speed != "" && speed != "—" {
					ui.Row(c).Height(20).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "↓ "+speed).FontSize(11).FontFeatures("tnum").
							TextColor(ui.RGBA(248, 249, 255, 0.68)).SingleLine().ContentTransition(ctNumeric)
					})
				}
			}
		})
		ui.Row(c).FillWidth().Wrap().Gap(transportGap).AlignItems(ui.Center).Children(func() {
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
					if playerIconButton(c, "live-prev", iconPrev, 16, "上一频道", "上一频道", false, live.playingChannel <= 0).Size(iconBtnSize, iconBtnSize).Clicked() {
						a.playLiveChannel(live.playingChannel-1, 0)
					}
				}
			}
			multiEp := a.hasMultipleEpisodes()
			localMulti := a.hasLocalPlaylist()
			source, episode, current := a.currentEpisode()
			if !a.playingLive {
				if multiEp {
					if playerIconButton(c, "ep-prev", iconPrev, 16, "上一集", "播放上一集", false, !current || episode == 0).Size(iconBtnSize, iconBtnSize).Clicked() {
						a.playEpisode(a.title.detail.Sources[source].Name, a.title.detail.Sources[source].Episodes[episode-1])
					}
				} else if localMulti {
					if playerIconButton(c, "local-prev", iconPrev, 16, "上一集", "播放上一集", false, a.localPlaylistIdx <= 0).Size(iconBtnSize, iconBtnSize).Clicked() {
						a.playLocalPlaylistIndex(a.localPlaylistIdx - 1)
					}
				}
			}
			playLabel := "暂停"
			playIcon := iconPause
			playTip := "暂停 (Space)"
			if !p.Playing() {
				playLabel = "播放"
				playIcon = iconPlay
				playTip = "播放 (Space)"
			}
			playBtn := ui.ButtonBase(c.Key("player-play-toggle")).Size(playBtnSize, playBtnSize).Radius(playBtnSize/2).Center().
				Cursor(ui.CursorPointer).Label(playLabel).Tooltip(playTip)
			playBg := ui.RGBA(255, 255, 255, 0.16)
			playBd := ui.RGBA(255, 255, 255, 0.14)
			if playBtn.Hovered() {
				playBg = ui.RGBA(255, 255, 255, 0.26)
				playBd = ui.RGBA(255, 255, 255, 0.26)
			}
			playBtn.Background(playBtn.AnimateColor("play-bg", playBg, animColor)).
				Border(1, playBtn.AnimateColor("play-bd", playBd, animColor))
			applyPressPose(playBtn)
			playBtn.Children(func() {
				ui.Icon(c, playIcon).Size(16, 16).TextColor(playerText).ContentTransition(ctIcon).PassThrough()
			})
			if playBtn.Clicked() {
				togglePlayback(p)
				a.syncNowPlayingState()
			}
			if a.playingLive {
				live := &a.live
				if len(live.channels) > 1 {
					if playerIconButton(c, "live-next", iconNext, 16, "下一频道", "下一频道", false, live.playingChannel+1 >= len(live.channels)).Size(iconBtnSize, iconBtnSize).Clicked() {
						a.playLiveChannel(live.playingChannel+1, 0)
					}
				}
			} else {
				if multiEp {
					if playerIconButton(c, "ep-next", iconNext, 16, "下一集", "播放下一集", false, !current || episode+1 >= len(a.title.detail.Sources[source].Episodes)).Size(iconBtnSize, iconBtnSize).Clicked() {
						a.playEpisode(a.title.detail.Sources[source].Name, a.title.detail.Sources[source].Episodes[episode+1])
					}
				} else if localMulti {
					if playerIconButton(c, "local-next", iconNext, 16, "下一集", "播放下一集", false, a.localPlaylistIdx+1 >= len(a.localPlaylist)).Size(iconBtnSize, iconBtnSize).Clicked() {
						a.playLocalPlaylistIndex(a.localPlaylistIdx + 1)
					}
				}
			}
			playerBarDivider(c)
			volIcon := iconVolumeHigh
			volTip := "静音 (M)"
			if a.isEffectivelyMuted(p) {
				volIcon = iconVolumeMute
				volTip = "取消静音 (M)"
			}
			if playerIconButton(c, "player-mute", volIcon, 16, "静音切换", volTip, false, false).Size(iconBtnSize, iconBtnSize).Clicked() {
				a.toggleMute(p)
			}
			volume := p.Volume()
			if a.muted {
				volume = 0
			}
			volumeControl := ui.Slider(c, &volume, 0, 1).Label("调节音量").Width(volWidth)
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
				fullTip := "进入全屏 (F)"
				if a.fullScreen {
					fullLabel = "退出全屏"
					fullIcon = iconFullscreenExit
					fullTip = "退出全屏 (F)"
				}
				if playerIconButton(c, "player-fullscreen", fullIcon, 16, fullLabel, fullTip, false, false).Size(iconBtnSize, iconBtnSize).Clicked() {
					a.togglePlayerFullScreen()
					a.playerFocus.Focus(c)
				}
			}
		})
	}

	if dockRect != nil {
		dock := stylePlayerFloatingSurface(c, ui.Column(c).Key("hud-bottom").Absolute().
			Left(dockRect.X).Bottom(dockBottom).Width(dockRect.W).
			Padding(padY, padX).Gap(colGap).Label("播放控制条").
			Transition(transHUDBottom))
		dock.Children(buildDockBody)
		return
	}

	bottom := float32(18)
	if compact {
		bottom = 10
	}
	ui.Row(c).Key("hud-bottom").Absolute().Left(18).Right(18).Bottom(bottom).Justify(ui.Center).
		Transition(transHUDBottom).Children(func() {
		dock := stylePlayerFloatingSurface(c, ui.Column(c).FillWidth().MaxWidth(920).
			Padding(padY, padX).Gap(colGap).Label("播放控制条"))
		dock.Children(buildDockBody)
	})
}

// playerPanel floats beside the picture: a title's episodes and sources, a
// local directory playlist, or a live list's channels and EPG.
func (a *app) playerPanel(c *ui.Context, key string, panelW, panelH float32) ui.Element {
	pad := float32(16)
	topGap := float32(12)
	if panelH <= 285 {
		pad = 8
		topGap = 4
	} else if panelH <= 420 {
		pad = 11
		topGap = 8
	} else if panelW <= 250 {
		pad = 14
		topGap = 10
	}
	panelContentH := max(float32(120), panelH-2*pad)

	panelLabel := "悬浮选集面板"
	if a.playingLive {
		panelLabel = "悬浮频道面板"
	} else if a.hasLocalPlaylist() {
		panelLabel = "悬浮列表面板"
	}

	panel := stylePlayerFloatingSurface(c, ui.Themed(c, appDarkTheme(c)).Key(key).
		Padding(pad, 0, pad, 0).Gap(topGap).Clip().Label(panelLabel))
	if panel.Hovered() {
		a.playerOverlay.keepVisible = true
	}
	panelBounds := panel.Bounds()
	panel.Children(func() {
		if a.playingLive {
			a.panelChannels(c, pad, panelContentH)
			return
		}
		if a.hasLocalPlaylist() {
			a.panelLocalPlaylist(c, pad, panelContentH)
			return
		}
		if !a.hasEpisodes() { // the header left the player earlier in this frame
			return
		}
		a.panelEpisodes(c, panelBounds, panelW, panelH, pad, topGap, panelContentH)
	})
	return panel
}

func panelScrollInsets(pad float32) (outerRight, contentRight float32) {
	outerRight = 2
	if pad <= 10 {
		outerRight = 1
	}
	contentRight = max(pad-outerRight, panelScrollbar.TrackWidth+3)
	return outerRight, contentRight
}

func panelSectionHeader(c *ui.Context, title, count string) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(12).Bold().TextColor(playerMutedText).Grow(1).SingleLine()
		if count != "" {
			ui.Text(c, count).FontSize(11).FontFeatures("tnum").TextColor(ui.RGBA(248, 249, 255, 0.48)).
				SingleLine().ContentTransition(ctNumeric)
		}
	})
}

func (a *app) panelListRowButton(c *ui.Context, key, label, a11yLabel string, selected, compact bool) ui.Element {
	if a11yLabel == "" {
		a11yLabel = label
	}
	padY, padX := float32(8), float32(11)
	fontSize := float32(12.5)
	if compact {
		padY, padX = 5, 8
		fontSize = 11.5
	}
	btn := ui.ButtonBase(c.Key(key)).FillWidth().Padding(padY, padX).Gap(8).Radius(9).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(a11yLabel)
	hovered := btn.Hovered()
	bg := ui.RGBA(255, 255, 255, 0.05)
	border := ui.RGBA(255, 255, 255, 0.08)
	fg := ui.RGBA(248, 249, 255, 0.88)
	if selected {
		bg = playerCoralSoft
		border = playerCoralBorder
		fg = playerText
	} else if hovered {
		bg = ui.RGBA(255, 255, 255, 0.10)
		border = ui.RGBA(255, 255, 255, 0.16)
		fg = playerText
	}
	animFg := animateButtonChrome(btn, "prow", bg, border, fg)
	applyPressPose(btn)
	dotReserve := float32(0)
	if selected {
		dotReserve = 6 + 8
	}
	innerW := max(float32(0), btn.Bounds().W-2*padX-2-dotReserve)
	btn.Children(func() {
		if selected {
			ui.Box(c.Key("prow-dot-"+key)).Size(6, 6).Radius(3).Background(playerCoral).Shrink(0).
				Transition(transMicroBadge).PassThrough()
		}
		a.hoverMarqueeText(c, marqueeTextOptions{
			key:        key,
			text:       label,
			fontSize:   fontSize,
			bold:       selected,
			color:      animFg,
			hovered:    hovered,
			grow:       true,
			knownWidth: innerW,
		})
	})
	return btn
}

func (a *app) panelLineChip(c *ui.Context, key, label string, selected, medium, compact bool, maxW float32) ui.Element {
	padY, padX := float32(5), float32(9)
	fontSize := float32(13)
	if compact {
		padY, padX = 2, 6
		fontSize = 11.5
	} else if medium {
		padY, padX = 3, 8
		fontSize = 12
	}
	btn := ui.ButtonBase(c.Key(key)).Padding(padY, padX).Gap(5).Radius(8).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	if maxW > 0 {
		btn.MaxWidth(maxW)
	}
	hovered := btn.Hovered()
	bg := ui.RGBA(255, 255, 255, 0.055)
	border := ui.RGBA(255, 255, 255, 0.11)
	fg := ui.RGBA(237, 238, 241, 0.88)
	if selected {
		bg = playerCoralSoft
		border = playerCoralBorder
		fg = ui.RGB(255, 163, 137)
	} else if hovered {
		bg = ui.RGBA(255, 255, 255, 0.13)
		border = ui.RGBA(255, 255, 255, 0.20)
		fg = playerText
	}
	animFg := animateButtonChrome(btn, "pline", bg, border, fg)
	applyPressPose(btn)
	innerW := max(float32(0), btn.Bounds().W-2*padX-2)
	btn.Children(func() {
		a.hoverMarqueeText(c, marqueeTextOptions{
			key:        key,
			text:       label,
			fontSize:   fontSize,
			bold:       selected,
			color:      animFg,
			hovered:    hovered,
			centered:   true,
			fixedWidth: innerW,
			knownWidth: innerW,
		})
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

func episodeGridColumns(episodes []catalog.Episode) int {
	maxRunes := 0
	for _, ep := range episodes {
		if n := len([]rune(episodeButtonLabel(ep.Name))); n > maxRunes {
			maxRunes = n
		}
	}
	switch {
	case maxRunes > 14:
		return 1
	case maxRunes > 6:
		return 2
	default:
		return 5
	}
}

func (a *app) panelEpisodes(c *ui.Context, panelBounds ui.Rect, panelW, panelH, pad, topGap, panelContentH float32) {
	compact := panelContentH <= 285
	medium := !compact && panelContentH <= 420
	inlinePickerRow := panelContentH <= 620

	detail := a.title.detail
	if a.panelChoice != nil {
		detail = a.panelChoice.detail
	}
	selectedLine := a.panelSource
	playingLine, _, hasPlaying := a.currentEpisode()
	if detail != nil && len(detail.Sources) > 0 {
		if selectedLine < 0 || selectedLine >= len(detail.Sources) {
			selectedLine = playingLine
			a.panelSource = selectedLine
		}
	}

	epTotal := 0
	if detail != nil && selectedLine >= 0 && selectedLine < len(detail.Sources) {
		epTotal = len(detail.Sources[selectedLine].Episodes)
	}

	// Top header row: 选集 共 N 集  ×
	headingSize := float32(15.5)
	metaSize := float32(11.5)
	closeSize := float32(28)
	if compact {
		headingSize = 13.5
		metaSize = 10.5
		closeSize = 23
	} else if medium {
		headingSize = 14.5
		metaSize = 11
		closeSize = 26
	}
	ui.Row(c.Key("panel-top")).FillWidth().Padding(0, pad).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Gap(7).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "选集").FontSize(headingSize).Bold().TextColor(playerText).SingleLine()
			if epTotal > 0 {
				ui.Text(c, fmt.Sprintf("共 %d 集", epTotal)).FontSize(metaSize).FontFeatures("tnum").
					TextColor(playerMutedText).SingleLine().ContentTransition(ctNumeric)
			}
		})
		if playerIconButton(c, "panel-close", iconClose, 14, "收起选集", "收起选集 (Esc)", false, false).Size(closeSize, closeSize).Radius(8).Clicked() {
			a.closeEpisodePanel()
		}
	})

	sectionGap := float32(11)
	epGap := float32(6)
	epRowH := float32(38)
	epFontSize := float32(13.5)
	if compact {
		sectionGap = 4
		epGap = 4
		epRowH = 27
		epFontSize = 12
	} else if medium {
		sectionGap = 7
		epGap = 5
		epRowH = 32
		epFontSize = 12.5
	} else if panelContentH <= 540 {
		sectionGap = 9
		epGap = 5.5
		epRowH = 35
		epFontSize = 13
	}

	renderPickersTop := func() {
		a.panelSources(c, a.candidates(), panelBounds, panelW, panelH, pad, topGap, closeSize, inlinePickerRow, medium, compact)
		if choice := a.panelChoice; choice != nil && choice.message != "" {
			bg := ui.RGBA(255, 255, 255, 0.06)
			border := ui.RGBA(255, 255, 255, 0.14)
			fg := ui.RGBA(248, 249, 255, 0.88)
			if choice.failed {
				bg = ui.RGBA(239, 68, 68, 0.15)
				border = ui.RGBA(248, 113, 113, 0.45)
				fg = ui.RGB(254, 202, 202)
			}
			ui.Column(c.Key("panel-choice-msg")).FillWidth().Padding(7, 9).Gap(4).Radius(9).
				Background(bg).Border(1, border).Shrink(0).Transition(transInlineReveal).Children(func() {
				ui.Text(c, choice.message).FontSize(11.5).TextColor(fg)
				if playing := a.playingSourceSummary(); playing != "" {
					statusPrefix := "仍在播放："
					backLabel := "回到当前"
					backA11y := "回到当前来源"
					if a.playFailure != nil {
						statusPrefix = "切换前："
						backLabel = "回到原来源"
						backA11y = "回到原来源"
					}
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, statusPrefix+playing).FontSize(10.5).TextColor(playerMutedText).SingleLine().Grow(1)
						if !a.running(jobPlay) && a.title != nil {
							retBtn := ui.ButtonBase(c.Key("panel-choice-back")).Padding(2, 7).Radius(6).
								Cursor(ui.CursorPointer).Label(backA11y).
								Background(ui.RGBA(255, 255, 255, 0.10)).Border(1, ui.RGBA(255, 255, 255, 0.16))
							applyPressPose(retBtn)
							retBtn.Children(func() {
								ui.Text(c, backLabel).FontSize(10.5).TextColor(playerText).SingleLine().PassThrough()
							})
							if retBtn.Clicked() {
								a.switchSource(a.title.item)
							}
						}
					})
				}
			})
			if a.panelChoice == nil {
				detail = a.title.detail
			} else {
				detail = choice.detail
			}
		}
		if detail == nil || len(detail.Sources) == 0 {
			return
		}
		if a.panelSource < 0 || a.panelSource >= len(detail.Sources) {
			a.panelSource = playingLine
		}
		selectedLine = a.panelSource
		if len(detail.Sources) > 1 {
			lineCount := fmt.Sprintf("%d 条", len(detail.Sources))
			lineMaxW := max(float32(80), panelW-2*pad)
			if inlinePickerRow {
				lineMaxW = max(float32(80), panelW-2*pad-56)
			}
			renderLineChips := func() {
				ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
					for i, source := range detail.Sources {
						name := source.Name
						if name == "" {
							name = fmt.Sprintf("线路 %d", i+1)
						}
						if a.panelLineChip(c, fmt.Sprintf("player-source-%d", i), name, i == a.panelSource, medium, compact, lineMaxW).Clicked() {
							selectedLine = i
							a.selectPanelLine(detail, i)
						}
					}
				})
			}
			ui.Column(c).FillWidth().Gap(4).Shrink(0).Children(func() {
				if inlinePickerRow {
					labelW := float32(48)
					if compact {
						labelW = 46
					}
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Column(c).Width(labelW).Shrink(0).Gap(1).Children(func() {
							lblSize, cntSize := float32(13), float32(11)
							if compact {
								lblSize, cntSize = 11.5, 10
							}
							ui.Text(c, "线路").FontSize(lblSize).Bold().TextColor(ui.RGBA(222, 222, 228, 0.96)).SingleLine()
							ui.Text(c, lineCount).FontSize(cntSize).FontFeatures("tnum").TextColor(playerMutedText).SingleLine().ContentTransition(ctNumeric)
						})
						ui.Box(c).Grow(1).MinWidth(0).Children(renderLineChips)
					})
				} else {
					panelSectionHeader(c, "线路", lineCount)
					renderLineChips()
				}
				if a.panelChoice == nil && hasPlaying && selectedLine != playingLine && !a.running(jobPlay) && a.playFailure == nil {
					ui.Text(c, "未匹配当前分集 · 仍在播放："+a.playingSource+"，请点击下方分集切换").FontSize(11).TextColor(ui.RGB(255, 198, 130))
				}
			})
		}
	}

	renderEpisodeGrid := func(padLeft, padRight float32) {
		if detail == nil || len(detail.Sources) == 0 {
			return
		}
		if selectedLine < 0 || selectedLine >= len(detail.Sources) {
			selectedLine = 0
		}
		selected := detail.Sources[selectedLine]
		solePlaying := len(selected.Episodes) == 1 && a.panelChoice == nil &&
			selected.Name == a.playingSource && selected.Episodes[0].Name == a.playingEpisode
		if len(selected.Episodes) == 0 || solePlaying {
			return
		}
		cols := episodeGridColumns(selected.Episodes)
		epPadX := float32(3)
		if cols < 5 {
			epPadX = 5
			if compact {
				epPadX = 4
			}
		}
		ui.Grid(c.Key("panel-episode-grid")).FillWidth().Columns(cols).Gap(epGap).Padding(2, padRight, 2, padLeft).Children(func() {
			for i, episode := range selected.Episodes {
				active := a.panelChoice == nil && selected.Name == a.playingSource && episode.Name == a.playingEpisode
				epLabel := episodeButtonLabel(episode.Name)
				btnKey := fmt.Sprintf("player-episode-%d", i)
				btn := ui.ButtonBase(c.Key(btnKey)).
					FillWidth().Height(epRowH).Padding(0, epPadX).Radius(8).Center().
					Cursor(ui.CursorPointer).Label(epLabel)
				hovered := btn.Hovered()
				bg := ui.RGBA(255, 255, 255, 0.055)
				border := ui.RGBA(255, 255, 255, 0.10)
				fg := ui.RGBA(237, 238, 241, 0.90)
				if active {
					bg = playerEpisodeActive
					border = playerEpisodeActiveBorder
					fg = ui.RGB(255, 255, 255)
				} else if hovered {
					bg = ui.RGBA(255, 255, 255, 0.13)
					border = ui.RGBA(255, 255, 255, 0.20)
					fg = playerText
				}
				animEpFg := animateButtonChrome(btn, "ep", bg, border, fg)
				applyPressPose(btn)
				innerW := max(float32(0), btn.Bounds().W-2*epPadX-2)
				btn.Children(func() {
					a.hoverMarqueeText(c, marqueeTextOptions{
						key:        btnKey,
						text:       epLabel,
						fontSize:   epFontSize,
						bold:       active,
						color:      animEpFg,
						hovered:    hovered,
						centered:   true,
						fillHeight: true,
						knownWidth: innerW,
					})
				})
				if active && a.panelRevealEpisode {
					align := ui.ScrollNearest
					if compact {
						align = ui.ScrollCenter
					}
					btn.ScrollIntoView(ui.ScrollIntoViewOptions{Block: align})
					a.panelRevealEpisode = false
				}
				if btn.Clicked() {
					a.playPanelEpisode(selectedLine, episode, 0)
				}
			}
		})
	}

	// Compact height with skip editor expanded scrolls the whole picker block so
	// both the active episode and skip inputs remain directly operable at 640×420.
	scrollOuterRight, scrollContentRight := panelScrollInsets(pad)
	scrollWholePickers := compact && a.panelSkipOpen && a.hasMultipleEpisodes()
	if scrollWholePickers {
		ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, scrollOuterRight, 0, 0).Children(func() {
			ui.Scroll(c.Key("panel-pickers-scroll")).FillWidth().FillHeight().Padding(0, scrollContentRight, 0, pad).Gap(sectionGap).
				ScrollbarStyle(panelScrollbar).KeepScrollbars(true).Label("选集列表").Children(func() {
				renderPickersTop()
				if detail != nil && len(detail.Sources) > 0 {
					ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.10)).Shrink(0).PassThrough()
					renderEpisodeGrid(0, 0)
				}
			})
		})
	} else {
		ui.Column(c.Key("panel-pickers")).FillWidth().Grow(1).MinHeight(0).Gap(sectionGap).Children(func() {
			ui.Column(c).FillWidth().Padding(0, pad).Gap(sectionGap).Shrink(0).Children(func() {
				renderPickersTop()
			})
			if detail != nil && len(detail.Sources) > 0 {
				selected := detail.Sources[min(max(selectedLine, 0), len(detail.Sources)-1)]
				solePlaying := len(selected.Episodes) == 1 && a.panelChoice == nil &&
					selected.Name == a.playingSource && selected.Episodes[0].Name == a.playingEpisode
				if len(selected.Episodes) > 0 && !solePlaying {
					ui.Box(c).FillWidth().Padding(0, pad).Shrink(0).Children(func() {
						ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.12)).PassThrough()
					})
					ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, scrollOuterRight, 0, 0).Children(func() {
						ui.Scroll(c.Key("panel-episodes-scroll")).FillWidth().FillHeight().
							ScrollbarStyle(panelScrollbar).KeepScrollbars(true).Label("选集列表").Children(func() {
							renderEpisodeGrid(pad, scrollContentRight)
						})
					})
				}
			}
		})
	}

	if a.hasMultipleEpisodes() {
		footPadTop := float32(6)
		footGap := float32(6)
		autoFont := float32(13)
		if compact {
			footPadTop = 3
			footGap = 3
			autoFont = 11.5
		} else if medium {
			footPadTop = 4
			footGap = 4
			autoFont = 12.5
		}
		ui.Box(c).FillWidth().Padding(0, pad).Shrink(0).Children(func() {
			ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.12)).PassThrough()
		})
		ui.Column(c.Key("panel-footer-deck")).FillWidth().Shrink(0).Padding(footPadTop, pad, 0, pad).Gap(footGap).Children(func() {
			a.panelSeriesSkipsInline(c, panelContentH)
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "播完自动下一集").FontSize(autoFont).TextColor(ui.RGBA(237, 238, 241, 0.90)).SingleLine().Grow(1)
				if ui.Switch(c, &a.autoNext).Label("自动下一集").Changed() {
					a.savePreferences()
				}
			})
		})
	}
}

func sourceMenuMaxHeight(panelBounds, srcBounds ui.Rect, panelH, pad, topGap, closeSize, triggerH, menuGap, winH float32, stackedHeader, justRevealed bool) float32 {
	const (
		defaultCap = float32(244)
		minHeight  = float32(48)
		borderW    = float32(1)
	)
	minSrcBottomOffset := borderW + pad + closeSize + topGap + triggerH
	if stackedHeader {
		minSrcBottomOffset += 16 + 6
	}
	var avail float32
	if panelBounds.H > 0 {
		panelBottomLimit := panelBounds.Y + panelBounds.H - pad - borderW
		srcBottom := panelBounds.Y + minSrcBottomOffset
		if !justRevealed && srcBounds.H > 0 {
			if actualBottom := srcBounds.Y + srcBounds.H; actualBottom >= panelBounds.Y+pad {
				srcBottom = actualBottom
			}
		}
		menuTop := srcBottom + menuGap
		avail = panelBottomLimit - menuTop
		if winH > 0 {
			avail = min(avail, winH-12-menuTop)
		}
	} else {
		panelBottomLimit := panelH - pad - borderW
		avail = panelBottomLimit - (minSrcBottomOffset + menuGap)
	}
	return min(defaultCap, max(minHeight, avail))
}

// Keep the source visible above its lines even when only one site is known.
func (a *app) panelSources(c *ui.Context, candidates []library.Item, panelBounds ui.Rect, panelW, panelH, pad, topGap, closeSize float32, inlineRow, medium, compact bool) {
	item := a.title.item
	if a.panelChoice != nil {
		item = a.panelChoice.item
	}
	label := item.Entry.Label()
	if len(candidates) <= 1 && label == "" {
		return
	}
	count := ""
	longCount := false
	if len(candidates) > 1 {
		count = fmt.Sprintf("%d 个", len(candidates))
	} else if s := a.title.sourceScan; s != nil {
		longCount = true
		if s.running {
			count = "正在查找其他来源…"
		} else {
			count = "暂未找到其他来源"
		}
	}

	triggerH := float32(38)
	triggerFont := float32(13.5)
	triggerPadX := float32(11)
	if compact {
		triggerH = 26
		triggerFont = 12
		triggerPadX = 8
	} else if medium {
		triggerH = 32
		triggerFont = 13
		triggerPadX = 10
	}

	_, winH := c.Size()
	menuGap := c.Theme().Space(1)
	stackedHeader := !inlineRow || longCount
	fallbackMenuW := max(float32(140), panelW-2*pad)
	if !stackedHeader {
		fallbackMenuW = max(float32(130), panelW-2*pad-56)
	}

	renderControl := func() {
		if len(candidates) > 1 {
			srcBtn := ui.ButtonBase(c.Key("panel-source-select")).FillWidth().Height(triggerH).Padding(0, triggerPadX).Gap(8).Radius(9).
				AlignItems(ui.Center).Cursor(ui.CursorPointer).Label("选择来源")
			justRevealed := a.failureUI.revealSourceTrigger
			if justRevealed {
				srcBtn.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollStart})
				a.failureUI.revealSourceTrigger = false
			}
			srcBounds := srcBtn.Bounds()
			menuMaxH := sourceMenuMaxHeight(panelBounds, srcBounds, panelH, pad, topGap, closeSize, triggerH, menuGap, winH, stackedHeader, justRevealed)
			srcHovered := srcBtn.Hovered()
			bg := ui.RGBA(255, 255, 255, 0.055)
			border := ui.RGBA(255, 255, 255, 0.14)
			if a.playerPopovers.source {
				bg = ui.RGBA(255, 255, 255, 0.11)
				border = ui.RGBA(255, 255, 255, 0.36)
			} else if srcHovered {
				bg = ui.RGBA(255, 255, 255, 0.10)
			}
			srcBtn.Background(srcBtn.AnimateColor("psrc-bg", bg, animColor)).
				Border(1, srcBtn.AnimateColor("psrc-bd", border, animColor))
			applyPressPose(srcBtn)
			srcLabelW := max(float32(0), srcBounds.W-2*triggerPadX-2-8-14)
			srcBtn.Children(func() {
				a.hoverMarqueeText(c, marqueeTextOptions{
					key:        "panel-source-select",
					text:       label,
					fontSize:   triggerFont,
					color:      playerText,
					hovered:    srcHovered && !a.playerPopovers.source,
					grow:       true,
					fillHeight: true,
					knownWidth: srcLabelW,
				})
				chev := ui.Icon(c.Key("psrc-chev"), iconChevronDown).Size(14, 14).TextColor(playerMutedText).PassThrough()
				applyChevronPose(chev, a.playerPopovers.source)
			})
			menuW := srcBounds.W
			if menuW <= 0 {
				menuW = fallbackMenuW
			}
			srcBtn.PopoverMenuButton(func(m *ui.Menu) {
				for _, candidate := range candidates {
					cand := candidate
					candLabel := cand.Entry.Label()
					key := itemKey(cand)
					selected := item.Entry.Config != nil && itemKey(item) == key
					mi := m.Item(candLabel).ID(key)
					mi.Content(func(mc ui.MenuItemContent) {
						fg := mc.Text
						if selected && !mc.Highlighted {
							fg = ui.RGB(255, 178, 154)
						}
						ui.Row(mc.Context).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
							a.hoverMarqueeText(mc.Context, marqueeTextOptions{
								key:      "panel-source-item-" + key,
								text:     candLabel,
								fontSize: 13,
								color:    fg,
								hovered:  mc.Highlighted,
								grow:     true,
							})
							if selected {
								ui.Text(mc.Context, "✓").FontSize(13).TextColor(fg).SingleLine()
							}
						})
					})
					if mi.Chosen() {
						a.switchSource(cand)
					}
				}
			}, ui.PopoverMenuButtonOptions{
				At:   ui.AnchorBottomStart,
				Self: ui.AnchorTopStart,
				Open: &a.playerPopovers.source,
				Panel: func(panel ui.Element) {
					panel.Label("来源菜单").
						MinWidth(menuW).Width(menuW).MaxWidth(menuW).
						MaxHeight(menuMaxH).
						ScrollbarStyle(panelScrollbar).
						KeepScrollbars(true).
						Background(playerSourceMenuBg).
						Border(1, playerSourceMenuBorder).
						Radius(12).
						Padding(5).
						NoShadow().
						Shadow(0, 10, 28, 0, ui.RGBA(0, 0, 0, 0.54)).
						Transition(transPopoverDown)
				},
				Item: func(item ui.Element) {
					item.Padding(8, 14, 8, 10).Radius(7)
				},
			})
			a.syncMenuPopoverOpen(a.playerPopovers.source)
		} else {
			row := ui.Row(c.Key("panel-source-single")).FillWidth().Height(triggerH).Padding(0, triggerPadX).Radius(9).AlignItems(ui.Center).
				Background(ui.RGBA(255, 255, 255, 0.04)).
				Border(1, ui.RGBA(255, 255, 255, 0.08))
			rowHovered := row.Hovered()
			rowInnerW := max(float32(0), row.Bounds().W-2*triggerPadX-2)
			row.Children(func() {
				a.hoverMarqueeText(c, marqueeTextOptions{
					key:        "panel-source-single",
					text:       label,
					fontSize:   triggerFont,
					color:      ui.RGBA(248, 249, 255, 0.85),
					hovered:    rowHovered,
					grow:       true,
					fillHeight: true,
					knownWidth: rowInnerW,
				})
			})
		}
	}

	if inlineRow && !longCount {
		labelW := float32(48)
		if compact {
			labelW = 46
		}
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Column(c).Width(labelW).Shrink(0).Gap(1).Children(func() {
				lblSize, cntSize := float32(13), float32(11)
				if compact {
					lblSize, cntSize = 11.5, 10
				}
				ui.Text(c, "来源").FontSize(lblSize).Bold().TextColor(ui.RGBA(222, 222, 228, 0.96)).SingleLine()
				if count != "" {
					ui.Text(c, count).FontSize(cntSize).FontFeatures("tnum").TextColor(playerMutedText).SingleLine().ContentTransition(ctNumeric)
				}
			})
			ui.Box(c).Grow(1).MinWidth(0).Children(renderControl)
		})
		return
	}

	ui.Column(c).FillWidth().Gap(6).Shrink(0).Children(func() {
		panelSectionHeader(c, "来源", count)
		renderControl()
	})
}

// panelChannels changes the channel or browses today's EPG schedule & catchup without leaving the player.
func (a *app) panelChannels(c *ui.Context, pad, panelContentH float32) {
	compact := panelContentH <= 285
	headingSize := float32(15)
	closeSize := float32(28)
	if compact {
		headingSize = 13.5
		closeSize = 23
	}
	live := &a.live
	hasEPG := a.activeEPGURL() != ""
	if len(live.channels) <= 1 && hasEPG {
		ui.Row(c).FillWidth().Padding(0, pad).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, "节目单").FontSize(headingSize).Bold().Grow(1).SingleLine()
			if playerIconButton(c, "panel-close", iconClose, 14, "收起面板", "收起面板 (Esc)", false, false).Size(closeSize, closeSize).Radius(8).Clicked() {
				a.closeEpisodePanel()
			}
		})
		a.panelChannelEPG(c, pad)
		return
	}
	if !hasEPG {
		live.panelTab = 0
		ui.Row(c).FillWidth().Padding(0, pad).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, "频道").FontSize(headingSize).Bold().Grow(1).SingleLine()
			if playerIconButton(c, "panel-close", iconClose, 14, "收起面板", "收起面板 (Esc)", false, false).Size(closeSize, closeSize).Radius(8).Clicked() {
				a.closeEpisodePanel()
			}
		})
	} else {
		ui.Row(c).FillWidth().Padding(0, pad).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Box(c).Grow(1).Children(func() {
				slidingSegmented(c, "live-panel-tab", &live.panelTab, "频道", "节目单").FillWidth().Label("直播面板")
			})
			if playerIconButton(c, "panel-close", iconClose, 14, "收起面板", "收起面板 (Esc)", false, false).Size(closeSize, closeSize).Radius(8).Clicked() {
				a.closeEpisodePanel()
			}
		})
	}
	if live.panelTab == 1 && hasEPG {
		ui.Column(c.Key("live-panel-epg")).FillWidth().Grow(1).MinHeight(0).Transition(transTabPage).Children(func() {
			a.panelChannelEPG(c, pad)
		})
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
	scrollOuterRight, scrollContentRight := panelScrollInsets(pad)
	ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, scrollOuterRight, 0, 0).Children(func() {
		ui.List(c, &live.panelChannelList, len(indices), func(row int) {
			i := indices[row]
			channel := live.channels[i]
			current := i == live.playingChannel
			fav := live.state.isFavorite(channel.Name)
			bg := ui.RGBA(255, 255, 255, 0.05)
			border := ui.RGBA(255, 255, 255, 0.08)
			rowKey := fmt.Sprintf("panel-channel-%d", i)
			btn := ui.Row(c.Key(rowKey)).FillWidth().Padding(8, 10).Gap(8).
				Radius(10).AlignItems(ui.Center).Cursor(ui.CursorPointer).
				TextColor(playerText).Label("切换到 " + channel.Name)
			hovered := btn.Hovered()
			if current {
				bg = playerCoralSoft
				border = playerCoralBorder
			} else if hovered {
				bg = ui.RGBA(255, 255, 255, 0.10)
			}
			btn.Background(btn.AnimateColor("pch-bg", bg, animColor)).
				Border(1, btn.AnimateColor("pch-bd", border, animColor))
			applyPressPose(btn)
			btnBounds := btn.Bounds()
			btn.Children(func() {
				a.renderChannelLogo(c, channel, 26)
				colReserve := float32(20 + 2 + 26 + 8)
				if current {
					colReserve += 6 + 8
				}
				labelW := max(float32(0), btnBounds.W-colReserve)
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
					label := channel.Name
					if fav {
						label = "★ " + label
					}
					a.hoverMarqueeText(c, marqueeTextOptions{
						key:        rowKey,
						text:       label,
						fontSize:   13,
						bold:       current,
						color:      playerText,
						hovered:    hovered,
						knownWidth: labelW,
					})
					if cur, _ := a.channelEPGCurrent(channel); cur != nil {
						ui.Text(c, cur.Title).FontSize(11).TextColor(playerMutedText).SingleLine()
					}
				})
				if current {
					ui.Box(c).Size(6, 6).Radius(3).Background(playerCoral).Shrink(0).PassThrough()
				}
			})
			if btn.Clicked() && !current {
				a.playLiveChannel(i, 0)
			}
		}).FillWidth().FillHeight().Padding(0, scrollContentRight, 0, pad).Gap(6).ScrollbarStyle(panelScrollbar).KeepScrollbars(true)
	})
}

// panelLocalPlaylist lists sibling videos in the same directory so a viewer
// watching a local season can jump between files or toggle auto-next.
func (a *app) panelLocalPlaylist(c *ui.Context, pad, panelContentH float32) {
	compact := panelContentH <= 285
	medium := !compact && panelContentH <= 420
	headingSize := float32(15.5)
	metaSize := float32(11.5)
	closeSize := float32(28)
	rowGap := float32(6)
	footPadTop := float32(6)
	autoFont := float32(13)
	if compact {
		headingSize = 13.5
		metaSize = 10.5
		closeSize = 23
		rowGap = 4
		footPadTop = 3
		autoFont = 11.5
	} else if medium {
		headingSize = 14.5
		metaSize = 11
		closeSize = 26
		rowGap = 5
		footPadTop = 4
		autoFont = 12.5
	}
	ui.Row(c.Key("panel-local-top")).FillWidth().Padding(0, pad).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Gap(7).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "同目录视频").FontSize(headingSize).Bold().TextColor(playerText).SingleLine()
			if len(a.localPlaylist) > 0 {
				ui.Text(c, fmt.Sprintf("共 %d 集", len(a.localPlaylist))).FontSize(metaSize).TextColor(playerMutedText).SingleLine().ContentTransition(ctNumeric)
			}
		})
		if playerIconButton(c, "panel-close", iconClose, 14, "收起面板", "收起面板 (Esc)", false, false).Size(closeSize, closeSize).Radius(8).Clicked() {
			a.closeEpisodePanel()
		}
	})
	ui.Box(c).FillWidth().Padding(0, pad).Shrink(0).Children(func() {
		ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.12)).PassThrough()
	})
	scrollOuterRight, scrollContentRight := panelScrollInsets(pad)
	ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, scrollOuterRight, 0, 0).Children(func() {
		ui.Scroll(c.Key("panel-local-scroll")).FillWidth().FillHeight().Padding(0, scrollContentRight, 0, pad).Gap(rowGap).
			ScrollbarStyle(panelScrollbar).KeepScrollbars(true).Label("同目录视频列表").Children(func() {
			for i, path := range a.localPlaylist {
				name := filepath.Base(path)
				current := i == a.localPlaylistIdx
				row := a.panelListRowButton(c, fmt.Sprintf("panel-local-%d", i), name, "播放 "+name, current, compact)
				if current && a.panelRevealEpisode {
					align := ui.ScrollNearest
					if compact {
						align = ui.ScrollCenter
					}
					row.ScrollIntoView(ui.ScrollIntoViewOptions{Block: align})
					a.panelRevealEpisode = false
				}
				if row.Clicked() && !current {
					a.playLocalPlaylistIndex(i)
				}
			}
		})
	})
	footGap := float32(6)
	if compact {
		footGap = 3
	} else if medium {
		footGap = 4
	}
	ui.Box(c).FillWidth().Padding(0, pad).Shrink(0).Children(func() {
		ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.12)).PassThrough()
	})
	ui.Column(c.Key("panel-local-footer-deck")).FillWidth().Shrink(0).Padding(footPadTop, pad, 0, pad).Gap(footGap).Children(func() {
		a.panelSeriesSkipsInline(c, panelContentH)
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "播完自动下一集").FontSize(autoFont).TextColor(ui.RGBA(237, 238, 241, 0.90)).SingleLine().Grow(1)
			if ui.Switch(c, &a.autoNext).Label("自动下一集").Changed() {
				a.savePreferences()
			}
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
		if !p.Ended() && !a.skipEndingReady(p) && !ended {
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
	a.clearPlayingMedia()
	a.localPlaylist = nil
	a.localPlaylistIdx = -1
	a.playerOverlay = playerOverlayState{}
	a.closePlayerPopovers()
	a.playerPointer = playerPointerState{}
	a.playbackWait = playbackWait{}
	a.playFailure = nil
	a.resetFailureUI()
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
		ui.Text(c, currentLabel).FontSize(12).TextColor(playerText).SingleLine().ContentTransition(ctReplace).PassThrough()
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
	border := ui.Transparent
	fg := playerText
	if custom {
		bg = playerCoralSoft
		border = playerCoralBorder
		fg = ui.RGB(255, 168, 146)
	} else if a.playerPopovers.speed {
		bg = ui.RGBA(255, 255, 255, 0.14)
	} else if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.12)
	}
	animFg := animateButtonChrome(btn, "speed", bg, border, fg)
	applyPressPose(btn)
	btn.Children(func() {
		t := ui.Text(c, rateLabel).FontSize(12).FontFeatures("tnum").TextColor(animFg).SingleLine().ContentTransition(ctNumeric).PassThrough()
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
	keyedPopover(c, "pop-speed", btn, &a.playerPopovers.speed, func(panel ui.Element) {
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
		badgeBg := ui.Transparent
		badgeBd := ui.RGBA(248, 249, 255, 0.65)
		iconFg := ui.RGBA(248, 249, 255, 0.78)
		if hasActive {
			badgeBg = playerText
			badgeBd = playerText
			iconFg = playerInk
		}
		badge := ui.Box(c.Key("sub-cc-badge")).Size(22, 16).Radius(4).Center().PassThrough()
		badge.Background(badge.AnimateColor("sub-cc-bg", badgeBg, animColor)).
			Border(1.4, badge.AnimateColor("sub-cc-bd", badgeBd, animColor))
		animCC := badge.AnimateColor("sub-cc-fg", iconFg, animColor)
		badge.Children(func() {
			ui.Icon(c, iconCCLetters).Size(15, 9).TextColor(animCC).PassThrough()
		})
		if hasActive {
			ui.Box(c.Key("sub-cc-dot")).Size(4, 4).Radius(2).Absolute().Bottom(2).Left(15).
				Background(playerCoral).Transition(transMicroBadge).PassThrough()
		}
	})
	if btn.Clicked() {
		a.togglePlayerPopover(&a.playerPopovers.subtitle)
	}
	if a.playerPopovers.subtitle {
		a.playerOverlay.keepVisible = true
	}
	keyedPopover(c, "pop-subtitle", btn, &a.playerPopovers.subtitle, func(panel ui.Element) {
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
					rowBd := ui.Transparent
					if selected {
						rowBg = playerCoralSoft
						rowBd = playerCoralBorder
					} else if row.Hovered() {
						rowBg = ui.RGBA(255, 255, 255, 0.08)
					}
					row.Background(row.AnimateColor("sub-row-bg", rowBg, animColor)).
						Border(1, row.AnimateColor("sub-row-bd", rowBd, animColor))
					applyPressPose(row)
					row.Children(func() {
						dotColor := ui.Transparent
						if selected {
							dotColor = playerCoral
						}
						ui.Box(c).Size(6, 6).Radius(3).Background(row.AnimateColor("sub-row-dot", dotColor, animColor)).PassThrough()
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
					ui.Text(c, formatSubtitleDelay(a.playerExtra.subDelay)).FontSize(11).FontFeatures("tnum").TextColor(playerText).
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
			ui.Text(c, chipText).FontSize(12).FontFeatures("tnum").TextColor(playerText).
				SingleLine().ContentTransition(ctNumeric).PassThrough()
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
	animStar := favBtn.AnimateColor("fav-ico", starColor, animColor)
	applyPressPose(favBtn)
	favBtn.Children(func() {
		ui.Icon(c, starIcon).Size(17, 17).TextColor(animStar).ContentTransition(ctIcon).PassThrough()
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
