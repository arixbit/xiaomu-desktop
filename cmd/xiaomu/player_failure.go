package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/egoist/mygo/ui"
)

var (
	iconFailureInterrupted = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M21 8V6.7A2.7 2.7 0 0 0 18.3 4H5.7A2.7 2.7 0 0 0 3 6.7v10.6A2.7 2.7 0 0 0 5.7 20h12.6A2.7 2.7 0 0 0 21 17.3V16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/><path d="M9 8.4l5.6 3.6L9 15.6z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/><path d="M20.8 10v2.1" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/><circle cx="20.8" cy="14.1" r=".75" fill="currentColor"/></svg>`))
	iconFailureSwap        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M3.5 8h15.8l-4-4M20.5 16H4.7l4 4" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	iconFailureCopy        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect x="8" y="8" width="12" height="13" rx="2" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/><path d="M16 5V4a1 1 0 0 0-1-1H5a2 2 0 0 0-2 2v10a1 1 0 0 0 1 1h1" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
)

var (
	failureCoralBg       = ui.RGB(255, 134, 105)
	failureCoralHover    = ui.RGB(255, 148, 122)
	failureCoralDisabled = ui.RGBA(255, 134, 105, 0.55)
	failureCoralInk      = ui.RGB(41, 20, 14)
	failureCoralBorder   = ui.RGBA(255, 170, 141, 0.45)

	failureSecondaryBg     = ui.RGBA(255, 255, 255, 0.055)
	failureSecondaryHover  = ui.RGBA(255, 255, 255, 0.12)
	failureSecondaryBorder = ui.RGBA(255, 255, 255, 0.14)
	failureSecondaryText   = ui.RGB(231, 231, 236)

	failureSignIconColor = ui.RGB(255, 161, 138)
	failureSignBorder    = ui.RGBA(255, 146, 115, 0.24)
	failureSignGrad      = ui.LinearGradient{
		Angle: 145,
		Stops: []ui.GradientStop{
			{Color: ui.RGBA(255, 137, 97, 0.14), At: 0},
			{Color: ui.RGBA(255, 121, 80, 0.05), At: 1},
		},
	}
	failureCodeBg     = ui.RGBA(12, 13, 18, 0.56)
	failureCodeBorder = ui.RGBA(255, 255, 255, 0.08)
	failureCodeText   = ui.RGB(221, 223, 231)
)

// playerFailureUIState holds transient UI state for the native VOD playback
// failure card without coupling presentation details to playback recovery.
type playerFailureUIState struct {
	detailsOpen         bool
	copied              bool
	copyFailed          bool
	copySeq             uint64
	retrying            bool
	retryJobGen         uint64
	panelFromRecovery   bool
	pendingSourceMenu   bool
	revealSourceTrigger bool
	lastFailureKey      string
}

func (a *app) resetFailureUI() {
	a.failureUI = playerFailureUIState{}
}

func (a *app) resetFailureUIForNewRequest() {
	keepPanelFlag := a.failureUI.panelFromRecovery
	a.failureUI = playerFailureUIState{
		panelFromRecovery: keepPanelFlag,
	}
}

func (a *app) isRetryingFailure() bool {
	if !a.failureUI.retrying {
		return false
	}
	if !a.running(jobPlay) {
		a.failureUI.retrying = false
		a.failureUI.retryJobGen = 0
		return false
	}
	if a.failureUI.retryJobGen != 0 && a.jobGen(jobPlay) != a.failureUI.retryJobGen {
		a.failureUI.retrying = false
		a.failureUI.retryJobGen = 0
		return false
	}
	return true
}

func (a *app) hasPlayerFailureOverlay(p mediaPlayer) bool {
	if !a.hasEpisodes() {
		return false
	}
	if a.playFailure != nil {
		return true
	}
	return p != nil && p.Err() != nil
}

// openSourcesFromFailure opens the right-side episode panel (if needed) and
// expands the controlled source dropdown menu in one click.
func (a *app) openSourcesFromFailure() {
	if !a.panelWasShown {
		a.failureUI.panelFromRecovery = true
	}
	a.panelOpen = true
	a.panelTouched = true
	a.panelSkipOpen = true
	a.panelRevealEpisode = false
	a.playerPopovers.closeAll()
	a.failureUI.pendingSourceMenu = true
	a.failureUI.revealSourceTrigger = true
	a.playerOverlay = playerOverlayState{keepVisible: true}
}

func (a *app) failureEpisodeContext(f *episodeFailure) string {
	epName := ""
	var at time.Duration
	if f != nil {
		epName = strings.TrimSpace(f.episode.Name)
		at = f.at
	} else {
		epName = strings.TrimSpace(a.playingEpisode)
		at = a.playbackPosition()
	}
	if at >= time.Second {
		progress := "已看到 " + playerClock(at)
		if epName != "" {
			return epName + " · " + progress
		}
		return progress
	}
	return epName
}

func formatRouteLabel(entry library.Entry, line string, multipleLines bool) string {
	label := strings.TrimSpace(entry.Label())
	line = strings.TrimSpace(line)
	if label == "" {
		return line
	}
	if line == "" {
		return label
	}
	if !multipleLines && (isGenericLineName(line) || line == label || strings.HasSuffix(label, " · "+line) || line == strings.TrimSpace(entry.Site.Name)) {
		return label
	}
	return label + " · " + line
}

func (a *app) failureRouteSummary(f *episodeFailure) string {
	line := ""
	if f != nil {
		line = strings.TrimSpace(f.line)
	}
	if line == "" {
		line = strings.TrimSpace(a.playingSource)
	}
	if a.title == nil {
		return line
	}
	multipleLines := a.title.detail != nil && len(a.title.detail.Sources) > 1
	return formatRouteLabel(a.title.item.Entry, line, multipleLines)
}

func (a *app) choiceRouteSummary(choice *playerSourceChoice) string {
	if choice == nil {
		return ""
	}
	line := strings.TrimSpace(choice.line)
	multipleLines := choice.detail != nil && len(choice.detail.Sources) > 1
	if line == "" && choice.detail != nil && a.panelSource >= 0 && a.panelSource < len(choice.detail.Sources) {
		line = strings.TrimSpace(choice.detail.Sources[a.panelSource].Name)
	}
	return formatRouteLabel(choice.item.Entry, line, multipleLines)
}

func (a *app) failureIdentityKey(f *episodeFailure) string {
	title := ""
	sourceKey := ""
	if a.title != nil {
		title = a.title.name
		sourceKey = sourceMemoryItemKey(a.title.item)
	}
	if choice := a.panelChoice; choice != nil && choice.failed {
		return title + "\x00choice\x00" + sourceMemoryItemKey(choice.item) + "\x00" + choice.line + "\x00" + choice.episode.Name
	}
	if f != nil {
		return title + "\x00" + sourceKey + "\x00" + f.line + "\x00" + f.episode.Name
	}
	return title + "\x00" + sourceKey + "\x00" + a.playingSource + "\x00" + a.playingEpisode
}

func (a *app) syncFailureIdentity(f *episodeFailure) {
	key := a.failureIdentityKey(f)
	if key != a.failureUI.lastFailureKey {
		keepPanelFlag := a.failureUI.panelFromRecovery
		retrying := a.failureUI.retrying
		retryGen := a.failureUI.retryJobGen
		pendingMenu := a.failureUI.pendingSourceMenu
		revealSrc := a.failureUI.revealSourceTrigger
		a.failureUI = playerFailureUIState{
			lastFailureKey:      key,
			panelFromRecovery:   keepPanelFlag,
			retrying:            retrying,
			retryJobGen:         retryGen,
			pendingSourceMenu:   pendingMenu,
			revealSourceTrigger: revealSrc,
		}
	}
}

func (a *app) canSearchOtherSourcesInFailure() (show, searching bool) {
	if a.title == nil || a.inStack(routeSearch) || len(a.candidates()) > 1 || a.library.root == nil {
		return false, false
	}
	search := a.titleSearch()
	searchingThis := search.running && titleKey(search.query) == titleKey(a.title.name)
	if searchingThis {
		return true, true
	}
	if titleKey(search.query) != titleKey(a.title.name) {
		return true, false
	}
	return false, false
}

func (a *app) triggerPanLoginFromFailure(f *episodeFailure) {
	if f == nil || f.authProvider == "" {
		return
	}
	provider := f.authProvider
	line, ep, at := f.line, f.episode, f.at
	originTitle := a.title
	originPlayGen := a.jobGen(jobPlay)
	originPlayer := a.player
	a.beginPanWebLogin(provider, func() {
		if a.title != originTitle || a.jobGen(jobPlay) != originPlayGen || a.player != originPlayer {
			return
		}
		a.resumeAfterPanLogin(line, ep, at)
	})
}

func playerFailureActionButton(
	c *ui.Context,
	key string,
	icon *ui.SVG,
	label string,
	primary bool,
	disabled bool,
	spinning bool,
	btnH float32,
	padX float32,
) ui.Element {
	btn := ui.ButtonBase(c.Key(key)).
		Height(btnH).Padding(0, padX).Gap(6).Radius(11).
		Center().Cursor(ui.CursorPointer).
		Label(label).Disabled(disabled)
	var bg, border, fg ui.Color
	if primary {
		bg = failureCoralBg
		border = failureCoralBorder
		fg = failureCoralInk
		if disabled {
			bg = failureCoralDisabled
		} else if btn.Hovered() {
			bg = failureCoralHover
		}
	} else {
		bg = failureSecondaryBg
		border = failureSecondaryBorder
		fg = failureSecondaryText
		if disabled {
			fg = ui.RGBA(231, 231, 236, 0.56)
		} else if btn.Hovered() {
			bg = failureSecondaryHover
			fg = playerText
		}
	}
	btn.Background(btn.AnimateColor(key+"-bg", bg, animColor)).Border(1, border)
	if !disabled {
		applyPressPose(btn)
	}
	btn.Children(func() {
		if spinning {
			ui.Spinner(c).Size(13, 13).TextColor(fg).PassThrough()
		} else if icon != nil {
			ui.Icon(c, icon).Size(15, 15).TextColor(fg).PassThrough()
		}
		txt := ui.Text(c, label).FontSize(13).TextColor(fg).SingleLine().PassThrough()
		if primary {
			txt.Bold()
		} else {
			txt.FontWeight(600)
		}
	})
	return btn
}

// playerFailureOverlay renders the centered native VOD playback failure card
// over the current picture stage (or available space between header and dock).
func (a *app) playerFailureOverlay(c *ui.Context, p mediaPlayer, err error, availW, availH float32, safeTop, safeBottom, safeLeft, safeRight float32) {
	f := a.playFailure
	a.syncFailureIdentity(f)

	retrying := a.isRetryingFailure()
	choice := a.panelChoice
	switching := !retrying && choice != nil && a.running(jobPlay)
	choiceFailed := !retrying && !switching && choice != nil && choice.failed
	choiceActive := switching || choiceFailed || (retrying && choice != nil)

	rawErr := ""
	if choiceActive && choice != nil && strings.TrimSpace(choice.message) != "" {
		rawErr = strings.TrimSpace(choice.message)
	} else if f != nil && f.err != nil {
		rawErr = strings.TrimSpace(f.err.Error())
	} else if err != nil {
		rawErr = strings.TrimSpace(err.Error())
	}
	if rawErr == "" {
		rawErr = "未知播放错误"
	}

	var authProvider pan.Provider
	retried := false
	if choiceActive && choice != nil {
		retried = choice.retried
	} else if f != nil {
		authProvider = f.authProvider
		retried = f.retried
	}

	heading := "这条线路播不了"
	switch {
	case retrying:
		heading = "正在重试当前线路…"
	case switching:
		heading = "正在打开新来源…"
	case authProvider != "":
		heading = "需要登录" + authProvider.Label()
	case retried:
		heading = "重试后仍然播不了"
	}

	contextLine := a.failureEpisodeContext(f)
	routeSummary := a.failureRouteSummary(f)
	if choiceActive && choice != nil {
		if s := a.choiceRouteSummary(choice); s != "" {
			routeSummary = s
		}
	}

	hasOtherSources := len(a.candidates()) > 1
	hasOtherLines := a.title != nil && a.title.detail != nil && len(a.title.detail.Sources) > 1
	if choiceActive && choice != nil {
		hasOtherLines = choice.detail != nil && len(choice.detail.Sources) > 1
	}
	canSearch, searchingSources := a.canSearchOtherSourcesInFailure()

	// Responsive sizing matching the prototype breakpoints at 1280x720 and 640x420.
	if availW <= 0 || availH <= 0 {
		winW, winH := c.Size()
		if availW <= 0 {
			availW = max(float32(240), winW-safeLeft-safeRight)
		}
		if availH <= 0 {
			availH = max(float32(140), winH-safeTop-safeBottom)
		}
	}
	narrow := availW <= 345
	shallow := availH <= 245
	veryShallow := availH <= 205

	layerPadX, layerPadY := float32(12), float32(8)
	if veryShallow {
		layerPadY = 3
		layerPadX = 6
	} else if shallow {
		layerPadY = 6
		layerPadX = 8
	}

	cardW := min(float32(374), max(float32(220), availW-2*layerPadX))
	maxCardH := max(float32(116), availH-2*layerPadY)

	padTop, padX, padBottom := float32(20), float32(22), float32(18)
	headGap := float32(13)
	headerTextGap := float32(5)
	signSize, signRadius, signIcon := float32(46), float32(14), float32(26)
	titleSize := float32(19)
	routeSize, routePadY, routePadX := float32(12), float32(3), float32(8)
	ctxSize := float32(13)
	actionsMarginTop := float32(18)
	actionGap := float32(8)
	btnH, btnPadX := float32(36), float32(13)
	detailsGap := float32(12)
	errMaxH := float32(104)
	errPadY, errPadX := float32(8), float32(10)

	if narrow {
		padX = 15
		actionGap = 6
		btnPadX = 10
	}
	if shallow {
		padTop, padBottom = 12, 12
		if !narrow {
			padX = 16
		}
		headGap = 10
		headerTextGap = 3
		signSize, signRadius, signIcon = 38, 11, 21
		titleSize = 17
		routeSize, routePadY, routePadX = 11, 2, 7
		ctxSize = 12
		actionsMarginTop = 10
		btnH = 32
		detailsGap = 7
		errMaxH = 62
		errPadY, errPadX = 5, 8
	}
	if veryShallow {
		padTop, padBottom = 9, 9
		headerTextGap = 2
		routePadY, routePadX = 1.5, 6
		actionsMarginTop = 7
		detailsGap = 6
		errMaxH = 44
	}

	// Determine primary coral button:
	// 1) Cloud-drive auth required -> 登录<Provider>
	// 2) Other sources available -> 换来源
	// 3) Multiple lines available (single source) -> 换下一条线路
	// 4) Ordinary single-source/single-line error -> 重试
	primaryAuth := authProvider != ""
	primarySource := !primaryAuth && hasOtherSources
	primaryLine := !primaryAuth && !hasOtherSources && hasOtherLines
	primaryRetry := !primaryAuth && !hasOtherSources && !hasOtherLines

	actionCount := 1 // 重试 is always present
	if authProvider != "" {
		actionCount++
	}
	if hasOtherSources {
		actionCount++
	}
	if hasOtherLines {
		actionCount++
	}
	if canSearch {
		actionCount++
	}
	wrapActions := actionCount > 2 || narrow

	layer := ui.Box(c.Key("player-failure-layer")).Absolute().
		Top(safeTop).Bottom(safeBottom).Left(safeLeft).Right(safeRight).
		Padding(layerPadY, layerPadX).Center()
	layer.Children(func() {
		card := stylePlayerFloatingSurface(c,
			ui.Scroll(c.Key("player-failure")).
				Width(cardW).MaxHeight(maxCardH).
				ScrollbarStyle(panelScrollbar).
				Padding(padTop, padX, padBottom, padX).
				Label("播放失败提示").
				Transition(transDialogPanel),
		)
		card.Children(func() {
			ui.Column(c).FillWidth().Gap(0).Children(func() {
				// Header: icon badge + title + current failed/switching route + episode & watched position.
				routeMaxW := max(float32(100), cardW-2*padX-signSize-headGap)
				routeBg := ui.RGBA(255, 255, 255, 0.065)
				routeBorder := ui.RGBA(255, 255, 255, 0.11)
				if c.Preferences().HighContrast {
					routeBorder = ui.RGBA(255, 255, 255, 0.28)
				}
				routeDotColor := failureSignIconColor
				if switching {
					routeDotColor = ui.RGBA(206, 210, 224, 0.85)
				}
				ui.Row(c).FillWidth().Gap(headGap).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(signSize, signSize).Radius(signRadius).Center().Shrink(0).
						Border(1, failureSignBorder).LinearGradient(failureSignGrad).PassThrough().Children(func() {
						ui.Icon(c, iconFailureInterrupted).Size(signIcon, signIcon).TextColor(failureSignIconColor).PassThrough()
					})
					ui.Column(c).Grow(1).MinWidth(0).Gap(headerTextGap).Children(func() {
						ui.Text(c, heading).FontSize(titleSize).Bold().TextColor(playerText).ContentTransition(ctReplace)
						if routeSummary != "" {
							routeBadge := ui.Row(c.Key("failure-route-badge")).
								AlignSelf(ui.Start).MaxWidth(routeMaxW).Padding(routePadY, routePadX).Gap(6).Radius(7).
								AlignItems(ui.Center).
								Background(routeBg).Border(1, routeBorder).
								Label("当前线路 " + routeSummary)
							routeHovered := routeBadge.Hovered()
							indicatorW := float32(5)
							if switching {
								indicatorW = 10
							}
							routeInnerW := max(float32(0), routeBadge.Bounds().W-2*routePadX-2-6-indicatorW)
							routeBadge.Children(func() {
								if switching {
									ui.Spinner(c).Size(10, 10).TextColor(routeDotColor).Shrink(0).PassThrough()
								} else {
									ui.Box(c).Size(5, 5).Radius(3).Background(routeDotColor).Shrink(0).PassThrough()
								}
								a.hoverMarqueeText(c, marqueeTextOptions{
									key:        "failure-route-badge",
									text:       routeSummary,
									fontSize:   routeSize,
									color:      ui.RGBA(236, 238, 246, 0.94),
									hovered:    routeHovered,
									fixedWidth: routeInnerW,
									knownWidth: routeInnerW,
								})
							})
						}
						if contextLine != "" {
							ui.Text(c, contextLine).FontSize(ctxSize).TextColor(ui.RGBA(186, 189, 204, 0.82)).SingleLine()
						}
					})
				})

				// Actions row: primary recovery action, retry, optional line/search/auth, and details toggle.
				actions := ui.Row(c).FillWidth().Padding(actionsMarginTop, 0, 0, 0).Gap(actionGap).AlignItems(ui.Center)
				if wrapActions {
					actions.Wrap()
				}
				actions.Children(func() {
					if authProvider != "" {
						if playerFailureActionButton(c, "failure-auth", nil, "登录"+authProvider.Label(), primaryAuth, switching, false, btnH, btnPadX).Clicked() {
							a.triggerPanLoginFromFailure(f)
						}
					}
					if hasOtherSources {
						if playerFailureActionButton(c, "failure-switch-source", iconFailureSwap, "换来源", primarySource, false, false, btnH, btnPadX).Clicked() {
							a.openSourcesFromFailure()
							c.Invalidate()
						}
					}
					if hasOtherLines {
						if playerFailureActionButton(c, "failure-next-line", nil, "换下一条线路", primaryLine, switching, false, btnH, btnPadX).Clicked() {
							a.playNextLine()
						}
					}
					if playerFailureActionButton(c, "failure-retry", iconRefresh, "重试", primaryRetry, retrying || switching, retrying, btnH, btnPadX).Clicked() {
						a.retryEpisode()
					}
					if canSearch {
						if searchingSources {
							playerFailureActionButton(c, "failure-searching", nil, "查找来源中…", false, true, true, btnH, btnPadX)
						} else if playerFailureActionButton(c, "failure-find-sources", nil, "查找其他来源", false, false, false, btnH, btnPadX).Clicked() {
							search := a.titleSearch()
							a.startScan(search, a.title.name, false)
							a.openEpisodePanel()
							a.playerOverlay = playerOverlayState{keepVisible: true}
						}
					}
					if !wrapActions {
						ui.Spacer(c)
					}

					// Details disclosure toggle.
					detailsBtn := ui.ButtonBase(c.Key("failure-details-toggle")).
						Height(max(float32(28), btnH-2)).Padding(0, 4).Gap(4).Radius(8).
						Center().Cursor(ui.CursorPointer).
						Label("错误详情").Expanded(a.failureUI.detailsOpen).Disabled(switching)
					detailsFg := ui.RGBA(182, 184, 196, 0.88)
					if switching {
						detailsFg = ui.RGBA(182, 184, 196, 0.40)
					} else if detailsBtn.Hovered() {
						detailsFg = playerText
					}
					detailsBtn.Children(func() {
						ui.Text(c, "错误详情").FontSize(12).TextColor(detailsFg).SingleLine().PassThrough()
						chev := ui.Icon(c, iconChevronDown).Size(12, 12).TextColor(detailsFg).PassThrough()
						if a.failureUI.detailsOpen {
							chev.Rotate(180)
						}
					})
					if detailsBtn.Clicked() {
						a.failureUI.detailsOpen = !a.failureUI.detailsOpen
						a.failureUI.copySeq++
						a.failureUI.copied = false
						a.failureUI.copyFailed = false
					}
				})

				// Expandable error details: source/line, copy button, and selectable monospace error box.
				if a.failureUI.detailsOpen {
					ui.Column(c.Key("player-failure-details")).FillWidth().Padding(detailsGap, 0, 0, 0).Gap(6).Children(func() {
						ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.12)).PassThrough()
						ui.Row(c).FillWidth().Padding(4, 0, 0, 0).Gap(8).AlignItems(ui.Center).Children(func() {
							if routeSummary != "" {
								ui.Text(c, routeSummary).FontSize(12).TextColor(ui.RGBA(180, 182, 194, 0.88)).Grow(1).MinWidth(0)
							} else {
								ui.Spacer(c)
							}
							copyLabel := "复制"
							copyFg := ui.RGBA(196, 198, 210, 0.90)
							switch {
							case a.failureUI.copied:
								copyLabel = "已复制"
								copyFg = ui.RGB(142, 232, 176)
							case a.failureUI.copyFailed:
								copyLabel = "复制失败"
								copyFg = ui.RGB(252, 165, 165)
							}
							copyBtn := ui.ButtonBase(c.Key("failure-copy-error")).
								Height(25).Padding(0, 5).Gap(4).Radius(6).
								Center().Cursor(ui.CursorPointer).Shrink(0).
								Label(copyLabel).Tooltip("复制错误信息")
							if copyBtn.Hovered() && !a.failureUI.copied && !a.failureUI.copyFailed {
								copyFg = playerText
							}
							copyBtn.Children(func() {
								ui.Icon(c, iconFailureCopy).Size(13, 13).TextColor(copyFg).PassThrough()
								ui.Text(c, copyLabel).FontSize(12).TextColor(copyFg).SingleLine().ContentTransition(ctReplace).PassThrough()
							})
							if copyBtn.Clicked() {
								a.failureUI.copySeq++
								seq := a.failureUI.copySeq
								a.failureUI.copied = false
								a.failureUI.copyFailed = false
								textToCopy := rawErr
								c.WriteClipboardThen(textToCopy, func(writeErr error) {
									if a.failureUI.copySeq != seq {
										return
									}
									a.failureUI.copied = writeErr == nil
									a.failureUI.copyFailed = writeErr != nil
								})
							}
						})

						ui.Box(c).FillWidth().Radius(8).
							Background(failureCodeBg).Border(1, failureCodeBorder).
							Padding(errPadY, errPadX).Clip().Children(func() {
							ui.Scroll(c.Key("player-failure-raw-scroll")).FillWidth().MaxHeight(errMaxH).
								ScrollbarStyle(panelScrollbar).Label("错误原文").Children(func() {
								ui.Text(c, rawErr).
									Font("monospace").FontSize(12).LineHeight(1.45).
									TextColor(failureCodeText).FillWidth().Selectable()
							})
						})
					})
				}
			})
		})
	})
}

func formatFailureErrorMessage(label string, err error) error {
	if err == nil {
		return fmt.Errorf("%s失败", label)
	}
	return fmt.Errorf("%s", jobError(label, err))
}
