package main

import (
	"runtime"

	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"
)

// routeKind names a page. The first five are the sidebar's tabs; the others
// are pushed above a tab and left with the back button.
type routeKind int

const (
	routeHome routeKind = iota
	routeLive
	routeHistory
	routeFavorites
	routeSources
	routeSearch
	routeDetail
	routeSite
	routeMedia
)

var tabs = []struct {
	kind  routeKind
	label string
}{
	{routeHome, "首页"},
	{routeLive, "直播"},
	{routeHistory, "历史"},
	{routeFavorites, "追剧"},
	{routeSources, "片源管理"},
}

const (
	sidebarWidth        = 204
	sidebarCompactWidth = 172
	sidebarRailWidth    = 64
	sidebarCompactAt    = 820
	sidebarRailAt       = 600
)

var librarySearchIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="10" cy="10" r="6" fill="none" stroke="currentColor" stroke-width="2"/><path d="M15 15L21 21" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>`))

func glassButton(c *ui.Context, label string) ui.Element {
	p := paletteFor(c)
	btn := ui.Button(c, label).Radius(9).Padding(7, 14)
	bg := p.controlBg
	border := p.borderControl
	if btn.Hovered() {
		bg = p.controlHover
		border = ui.RGBA(255, 255, 255, 0.20)
	}
	btn.Background(btn.AnimateColor("glass-bg", bg, animColor)).
		Border(1, btn.AnimateColor("glass-bd", border, animColor)).
		TextColor(p.text)
	return applyPressPose(btn)
}

func ghostButton(c *ui.Context, label string) ui.Element {
	p := paletteFor(c)
	btn := ui.Button(c, label).Radius(9).Padding(7, 13)
	bg := p.surfaceSubtle
	border := p.border
	fg := p.textMuted
	if btn.Hovered() {
		bg = p.surfaceHover
		border = p.borderControl
		fg = p.text
	}
	btn.TextColor(animateButtonChrome(btn, "ghost", bg, border, fg))
	return applyPressPose(btn)
}

func primaryButton(c *ui.Context, label string) ui.Element {
	p := paletteFor(c)
	btn := ui.Button(c, label).Radius(9).Padding(7, 16).Bold().TextColor(p.accentText)
	bg := p.accent
	if btn.Hovered() {
		bg = p.accentHover
	}
	btn.Background(btn.AnimateColor("primary-bg", bg, animColor))
	return applyPressPose(btn)
}

// route is the page on show; root is the tab it was reached from.
func (a *app) route() routeKind {
	if len(a.nav) == 0 {
		return routeHome
	}
	return a.nav[len(a.nav)-1]
}

func (a *app) root() routeKind {
	if len(a.nav) == 0 {
		return routeHome
	}
	return a.nav[0]
}

func (a *app) inStack(kind routeKind) bool {
	for _, k := range a.nav {
		if k == kind {
			return true
		}
	}
	return false
}

func (a *app) push(kind routeKind) {
	if len(a.nav) == 0 {
		a.nav = []routeKind{routeHome}
	}
	if a.route() != kind {
		a.nav = append(a.nav, kind)
		a.navDir = 1
		a.clearArmed()
	}
}

// pop leaves the page on show, stopping the work only that page needed.
// goBack moves up one media-library folder, or leaves the current page.
func (a *app) goBack() {
	if a.route() == routeMedia && a.mediaAscend() {
		return
	}
	a.pop()
}

func (a *app) pop() {
	if len(a.nav) <= 1 {
		return
	}
	left := a.route()
	a.nav = a.nav[:len(a.nav)-1]
	a.navDir = -1
	a.clearArmed()
	switch left {
	case routeDetail:
		a.clearTitle()
		if !a.inStack(routeSearch) {
			a.stopScan(&a.library.search)
		}
	case routeSearch:
		a.stopScan(&a.library.search)
		a.browser.query = ""
	case routeSite:
		a.cancelJob(jobSite)
		a.closeSite()
	case routeMedia:
		a.cancelJob(jobSite)
		a.media.activeID = ""
		a.media.stack = nil
		a.media.items = nil
		a.media.err = ""
	}
	a.clearStatus()
}

func (a *app) switchTab(kind routeKind) {
	a.cancelJob(jobTitle)
	a.cancelJob(jobPlay)
	for len(a.nav) > 1 {
		a.pop()
	}
	a.nav = []routeKind{kind}
	a.navDir = 0
	a.clearArmed()
	a.clearStatus()
	switch kind {
	case routeHome:
		a.loadDiscoveryTab(false)
	case routeLive:
		a.showLive()
	case routeSources:
		if a.library.root == nil && (a.live.url != "" || len(a.live.sources) > 0) {
			a.sourcesTab = 1
		}
	}
}

func (a *app) pageTitle() string {
	switch a.route() {
	case routeSearch:
		return "搜索结果"
	case routeDetail:
		return ""
	case routeSite:
		return a.browser.site.Label()
	case routeMedia:
		if n := len(a.media.stack); n > 0 && a.media.stack[n-1].Title != "" {
			return a.media.stack[n-1].Title
		}
		return "媒体库"
	}
	for _, tab := range tabs {
		if tab.kind == a.route() {
			return tab.label
		}
	}
	return ""
}

// dragStripHeight is the height of the title bar band, or 0 in full
// screen, where the window has no title bar and cannot be dragged.
func dragStripHeight(c *ui.Context) float32 {
	bar := c.TitleBar()
	if bar == (ui.TitleBar{}) {
		return 0
	}
	return max(bar.Height, 32)
}

func activeSidebarWidth(windowWidth float32) (width float32, rail bool) {
	switch {
	case windowWidth > 0 && windowWidth < sidebarRailAt:
		return sidebarRailWidth, true
	case windowWidth > 0 && windowWidth < sidebarCompactAt:
		return sidebarCompactWidth, false
	default:
		return sidebarWidth, false
	}
}

// shell is every page but the player: the adaptive backdrop, floating sidebar,
// persistent page host (so routeDetail push/pop and toolbar-back never lose
// continuity), and unified toolbar.
func (a *app) shell(c *ui.Context) {
	ui.Themed(c.Key("app-shell"), appDarkTheme(c)).Fill().Background(ui.Transparent).Children(func() {
		p := paletteFor(c)
		tb := a.toolbarLayout(c)
		ui.Box(c).Fill().LinearGradient(p.backdrop).TextColor(p.text).Children(func() {
			ui.Row(c).Fill().Padding(10).Gap(10).Children(func() {
				a.sidebar(c)
				ui.Box(c.Key("shell-main")).Grow(1).FillHeight().MinWidth(0).Clip().Children(func() {
					rt := routeTransition(a.navDir)
					pageHost := ui.Box(c.Key("page-host")).Fill().Clip()
					if rt.Exit != nil {
						pageHost.ChildExit(*rt.Exit)
					}
					pageHost.Children(func() {
						route := a.route()
						page := ui.Box(c.Key(route)).Fill().Transition(rt)
						if route != routeDetail {
							page.Padding(4+tb.height+14, 8, 8, 8)
						}
						page.Children(func() {
							switch route {
							case routeDetail:
								a.detailView(c)
							case routeLive:
								a.liveView(c)
							case routeHistory:
								a.historyView(c)
							case routeFavorites:
								a.favoritesView(c)
							case routeSources:
								a.sourcesView(c)
							case routeSearch:
								a.searchView(c)
							case routeSite:
								a.siteView(c)
							case routeMedia:
								a.mediaLibraryView(c)
							default:
								a.homeView(c)
							}
						})
					})
					ui.Box(c.Key("toolbar-host")).Absolute().Top(4).Left(8).Right(8).Children(func() {
						a.toolbar(c, tb)
					})
				})
			})
			a.statusView(c)
			a.importDialog(c)
			a.liveDialog(c)
			a.linkDialog(c)
			a.panAuthPromptDialog(c)
			a.mediaServerDialog(c)
			a.localPosterDialog(c)
			a.localPosterSettingsDialog(c)
		})
	})
}

func (a *app) sidebar(c *ui.Context) {
	p := paletteFor(c)
	winW, winH := c.Size()
	sideW, rail := activeSidebarWidth(winW)
	shortWin := winH > 0 && winH < 480
	top := max(float32(52), c.TitleBar().Bottom+14)
	if p.reduceTransparency {
		top = max(float32(51), c.TitleBar().Bottom+13)
	}
	padX, gap := float32(10), float32(4)
	if rail {
		padX = 8
	}
	if shortWin {
		gap = 2
	}
	side := ui.Column(c.Key("sidebar")).Width(sideW).FillHeight().Radius(18).Padding(top, padX, 10, padX).Gap(gap)
	if p.reduceTransparency {
		side.Background(p.sidebarBg).Border(1, p.sidebarBorder)
	} else if p.highContrast {
		side.Border(1, p.sidebarBorder)
	}
	if dragStripHeight(c) > 0 {
		side.DragWindow().Label("拖动窗口（侧边栏）")
	}
	side.Children(func() {
		if rail {
			titleBottom := float32(10)
			if shortWin {
				titleBottom = 4
			}
			ui.Text(c, "小幕").FontSize(15).Bold().FillWidth().TextAlign(ui.Center).Padding(0, 0, titleBottom, 0)
			rowH := float32(40)
			if shortWin {
				rowH = 32
			}
			hasNewFollow := a.hasAnyFollowingNew()
			for _, tab := range tabs {
				if tab.kind == routeSources {
					continue
				}
				badge := tab.kind == routeFavorites && hasNewFollow
				if railNavButton(c, tab.kind, tab.label, a.root() == tab.kind, badge, rowH).Clicked() {
					a.switchTab(tab.kind)
				}
			}
			ui.Spacer(c)
			btnH := float32(38)
			if shortWin {
				btnH = 32
			}
			if railNavButton(c, routeSources, "片源管理", a.root() == routeSources, false, btnH).Clicked() {
				a.switchTab(routeSources)
			}
			if railIconButton(c, navIconFile, "打开文件", "打开本地视频 ("+openFileShortcutHint()+")", btnH).Clicked() {
				a.openLocalFileDialog()
			}
			if railIconButton(c, navIconLink, "打开链接", "粘贴视频直链或网盘分享链接", btnH).Clicked() {
				a.linkErr = ""
				a.linkOpen = true
			}
			if updateRailButton(c, a.availableUpdate, btnH).Clicked() {
				updater.CheckForUpdates()
			}
			return
		}
		titleBottom := float32(12)
		if shortWin {
			titleBottom = 6
		}
		ui.Text(c, "小幕").FontSize(20).Bold().Padding(0, 12, titleBottom, 12)
		hasNewFollow := a.hasAnyFollowingNew()
		for _, tab := range tabs {
			if tab.kind == routeSources {
				continue
			}
			row := navTabRow(c, tab.kind, tab.label, a.root() == tab.kind, tab.kind == routeFavorites && hasNewFollow)
			if shortWin {
				row.Height(32)
			}
			if row.Clicked() {
				a.switchTab(tab.kind)
			}
		}
		ui.Spacer(c)
		sourcesBtn := sidebarUtilityTabRow(c, routeSources, "片源管理", a.root() == routeSources)
		openFileBtn := sidebarActionButton(c, navIconFile, "打开文件", "打开本地视频 ("+openFileShortcutHint()+")")
		openLinkBtn := sidebarActionButton(c, navIconLink, "打开链接", "粘贴视频直链或网盘分享链接")
		updBtn := updateButton(c, a.availableUpdate)
		if shortWin {
			sourcesBtn.Height(32)
			openFileBtn.Height(32)
			openLinkBtn.Height(32)
			updBtn.Height(32)
		}
		if sourcesBtn.Clicked() {
			a.switchTab(routeSources)
		}
		if openFileBtn.Clicked() {
			a.openLocalFileDialog()
		}
		if openLinkBtn.Clicked() {
			a.linkErr = ""
			a.linkOpen = true
		}
		if updBtn.Clicked() {
			updater.CheckForUpdates()
		}
	})
}

func sidebarUtilityTabRow(c *ui.Context, kind routeKind, label string, selected bool) ui.Element {
	p := paletteFor(c)
	row := ui.Row(c.Key(label)).FillWidth().Height(34).Padding(0, 12).Gap(10).Radius(8).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	targetBg := ui.Transparent
	targetBorder := ui.Transparent
	targetFg := p.textMuted
	if selected {
		targetBg = p.surfaceSelected
		targetBorder = p.border
		targetFg = p.text
	} else if row.Hovered() {
		targetBg = p.surfaceHover
		targetFg = p.text
	}
	fg := animateButtonChrome(row, "side-util", targetBg, targetBorder, targetFg)
	applyPressPose(row)
	row.Children(func() {
		if selected {
			ui.Box(c.Key("sidebar-ind-util")).Absolute().Left(0).Top(8).Bottom(8).Width(3).Radius(2).
				Background(p.accent).Transition(transMatchIndicator(sidebarIndicatorMatch{rail: false}, true)).PassThrough()
		}
		ui.Icon(c, tabIcon(kind)).Size(16, 16).TextColor(fg).PassThrough()
		text := ui.Text(c, label).FontSize(13).SingleLine().TextColor(fg).Grow(1)
		if selected {
			text.Bold()
		}
	})
	return row
}

func sidebarActionButton(c *ui.Context, icon *ui.SVG, label, tooltip string) ui.Element {
	p := paletteFor(c)
	btn := ui.ButtonBase(c.Key("side-act-"+label)).FillWidth().Height(34).Padding(0, 12).Gap(10).Radius(8).
		Justify(ui.Start).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label).Tooltip(tooltip)
	bg := ui.Transparent
	fg := p.textMuted
	if btn.Hovered() {
		bg = p.surfaceHover
		fg = p.text
	}
	btn.Background(btn.AnimateColor("side-act-bg", bg, animColor))
	animFg := btn.AnimateColor("side-act-fg", fg, animColor)
	applyPressPose(btn)
	btn.Children(func() {
		ui.Icon(c, icon).Size(16, 16).TextColor(animFg).PassThrough()
		ui.Text(c, label).FontSize(13).TextColor(animFg).SingleLine().PassThrough()
	})
	return btn
}

func navTabRow(c *ui.Context, kind routeKind, label string, selected, hasBadge bool) ui.Element {
	p := paletteFor(c)
	row := ui.Row(c.Key(label)).FillWidth().Height(36).Padding(0, 12).Gap(10).Radius(9).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	if hasBadge {
		row.Tooltip("追剧有新集更新")
	}
	targetBg := ui.Transparent
	targetBorder := ui.Transparent
	targetFg := p.textMuted
	if selected {
		targetBg = p.surfaceSelected
		targetBorder = p.border
		targetFg = p.text
	} else if row.Hovered() {
		targetBg = p.surfaceHover
		targetFg = p.text
	}
	fg := animateButtonChrome(row, "row", targetBg, targetBorder, targetFg)
	applyPressPose(row)
	row.Children(func() {
		if selected {
			ui.Box(c.Key("sidebar-ind-"+label)).Absolute().Left(0).Top(9).Bottom(9).Width(3).Radius(2).
				Background(p.accent).Transition(transMatchIndicator(sidebarIndicatorMatch{rail: false}, true)).PassThrough()
		}
		ui.Icon(c, tabIcon(kind)).Size(16, 16).TextColor(fg).PassThrough()
		text := ui.Text(c, label).FontSize(13.5).SingleLine().TextColor(fg).Grow(1)
		if selected {
			text.Bold()
		}
		if hasBadge {
			ui.Box(c.Key("nav-follow-badge")).Size(7, 7).Radius(4).Background(p.accent).
				Label("追剧有新集").Transition(transMicroBadge).PassThrough()
		}
	})
	return row
}

func railNavButton(c *ui.Context, kind routeKind, label string, selected, hasBadge bool, height float32) ui.Element {
	p := paletteFor(c)
	tip := label
	if hasBadge {
		tip = label + "（有新集）"
	}
	row := ui.Row(c.Key("rail-" + label)).FillWidth().Height(height).Radius(12).
		Center().Cursor(ui.CursorPointer).Label(label).Tooltip(tip)
	targetBg := ui.Transparent
	targetBorder := ui.Transparent
	targetFg := p.textMuted
	if selected {
		targetBg = p.surfaceSelected
		targetBorder = p.border
		targetFg = p.text
	} else if row.Hovered() {
		targetBg = p.surfaceHover
		targetFg = p.text
	}
	fg := animateButtonChrome(row, "rail", targetBg, targetBorder, targetFg)
	applyPressPose(row)
	row.Children(func() {
		if selected {
			ui.Box(c.Key("rail-ind-"+label)).Absolute().Left(0).Top(8).Bottom(8).Width(3).Radius(2).
				Background(p.accent).Transition(transMatchIndicator(sidebarIndicatorMatch{rail: true}, true)).PassThrough()
		}
		ui.Icon(c, tabIcon(kind)).Size(19, 19).TextColor(fg).PassThrough()
		if hasBadge {
			ui.Box(c.Key("rail-follow-badge")).Size(7, 7).Radius(4).Absolute().Right(10).Top(8).
				Background(p.accent).Label("追剧有新集").Transition(transMicroBadge).PassThrough()
		}
	})
	return row
}

func railIconButton(c *ui.Context, icon *ui.SVG, label, tooltip string, height float32) ui.Element {
	p := paletteFor(c)
	if tooltip == "" {
		tooltip = label
	}
	btn := ui.ButtonBase(c.Key("rail-action-"+label)).FillWidth().Height(height).Radius(9).
		Center().Cursor(ui.CursorPointer).Label(label).Tooltip(tooltip).TextColor(p.textMuted)
	bg := ui.Transparent
	fg := p.textMuted
	if btn.Hovered() {
		bg = p.surfaceHover
		fg = p.text
	}
	btn.Background(btn.AnimateColor("rail-btn-bg", bg, animColor))
	animFg := btn.AnimateColor("rail-btn-fg", fg, animColor)
	applyPressPose(btn)
	btn.Children(func() {
		ui.Icon(c, icon).Size(17, 17).TextColor(animFg).PassThrough()
	})
	return btn
}

type toolbarMetrics struct {
	height      float32
	navWidth    float32
	searchTop   float32
	searchLeft  float32
	searchWidth float32
}

func (a *app) toolbarLayout(c *ui.Context) toolbarMetrics {
	width, _ := c.Size()
	sideW, _ := activeSidebarWidth(width)
	// Match shell's outer padding, sidebar, gap and content padding.
	contentWidth := max(0, width-20-sideW-10-16)
	// Equal space on either side keeps search centered as back appears.
	searchWidth := min(float32(560), contentWidth-2*192)
	compact := searchWidth < 280
	m := toolbarMetrics{
		height:      44,
		navWidth:    contentWidth,
		searchTop:   2,
		searchLeft:  (contentWidth - searchWidth) / 2,
		searchWidth: searchWidth,
	}
	if a.library.root != nil {
		if compact {
			m.height, m.searchTop, m.searchLeft, m.searchWidth = 92, 52, 0, contentWidth
		} else {
			m.navWidth = m.searchLeft - 12
		}
	}
	return m
}

func (a *app) toolbar(c *ui.Context, m toolbarMetrics) {
	ui.Box(c).Key("toolbar").FillWidth().Height(m.height).Children(func() {
		bar := ui.Row(c).FillWidth().Height(44).AlignItems(ui.Center)
		if dragStripHeight(c) > 0 {
			bar.DragWindow().Label("拖动窗口")
		}
		bar.Children(func() {
			ui.Row(c).Width(m.navWidth).MinWidth(0).Gap(10).AlignItems(ui.Center).Children(func() {
				if len(a.nav) > 1 {
					p := paletteFor(c)
					backBtn := glassButton(c.Key("toolbar-back"), "").Size(34, 34).Radius(9).Padding(0).Center().
						Label("返回").Tooltip("返回上一页 (Esc)").Transition(transBackButton)
					backBtn.Children(func() {
						ui.Icon(c, iconBack).Size(17, 17).TextColor(p.text).PassThrough()
					})
					if backBtn.Clicked() {
						a.goBack()
					}
				}
				if title := a.pageTitle(); title != "" {
					ui.Text(c, title).FontSize(20).Bold().SingleLine().Grow(1).MinWidth(0).
						ContentTransition(ctReplace)
				}
			})
		})
		if a.library.root != nil {
			// Keep the same parent at both sizes so editing and the popup
			// survive resizing. Input sits above the draggable toolbar.
			a.librarySearch(c).Absolute().Top(m.searchTop).Left(m.searchLeft).Width(m.searchWidth)
		}
	})
}

// searchShortcutHint is the search shortcut as the system shows it,
// mirroring ui.Cmd: Command on macOS, Control elsewhere.
func searchShortcutHint() string {
	if runtime.GOOS == "darwin" {
		return "⌘K"
	}
	return "Ctrl+K"
}

func openFileShortcutHint() string {
	if runtime.GOOS == "darwin" {
		return "⌘O"
	}
	return "Ctrl+O"
}

func (a *app) librarySearch(c *ui.Context) ui.Element {
	p := paletteFor(c)
	return ui.Box(c).Key("library-search").Height(40).Children(func() {
		field := ui.Autocomplete(c, &a.browser.query, a.searchSuggestions()).Label("片名").Placeholder("搜索整个片库").
			Fill().Padding(0, 54, 0, 38).FontSize(14).Radius(11).
			FocusBind(&a.searchFocused, true).Bind(&a.searchFocus)
		borderTarget := p.borderControl
		iconTarget := p.textMuted
		if a.searchFocused {
			borderTarget = p.accentBorder
			iconTarget = p.text
		} else if field.Hovered() {
			borderTarget = ui.RGBA(255, 255, 255, 0.22)
		}
		field.Border(1, field.AnimateColor("search-bd", borderTarget, animColor))
		if p.reduceTransparency {
			field.Background(p.surface)
		} else {
			field.Material(p.searchMat).Shadow(0, 6, 18, 0, ui.RGBA(0, 0, 0, 0.24))
		}
		if field.Changed() {
			a.focusSearchGrid = false
		}
		if field.Submitted() {
			a.searchLibrary()
		}
		ui.Icon(c, librarySearchIcon).Size(16, 16).Absolute().Left(13).Top(12).
			TextColor(field.AnimateColor("search-ico", iconTarget, animColor)).PassThrough()
		if a.browser.query != "" {
			clearBtn := ui.Button(c.Key("clear-search"), "×").Label("清空搜索").Tooltip("清空搜索").
				Absolute().Right(9).Top(9).Size(22, 22).Radius(6).Padding(0).FontSize(14).
				Transition(transMicroBadge)
			cBg, cFg := p.surfaceSubtle, p.textMuted
			if clearBtn.Hovered() {
				cBg, cFg = p.surfaceHover, p.text
			}
			clearBtn.Background(clearBtn.AnimateColor("clr-bg", cBg, animColor)).
				TextColor(clearBtn.AnimateColor("clr-fg", cFg, animColor))
			applyPressPose(clearBtn)
			if clearBtn.Clicked() {
				a.browser.query = ""
				a.focusSearchGrid = false
				a.searchFocused = true
			}
		} else if !a.searchFocused {
			ui.Text(c.Key("search-shortcut-hint"), searchShortcutHint()).FontSize(11).TextColor(p.textFaint).
				Absolute().Right(13).Top(12).Transition(transMicroBadge).PassThrough()
		}
	})
}

func (a *app) statusView(c *ui.Context) {
	busy := a.busy()
	a.syncToastState(c, busy)
	if a.status == "" && !busy {
		return
	}
	p := paletteFor(c)
	toast := ui.Row(c.Key("status-toast")).Absolute().Right(16).Bottom(16).MaxWidth(640).Padding(9, 14).Gap(10).
		Radius(16).AlignItems(ui.Center).Transition(transToast)
	applyPane(toast, p, p.dialogMat)
	toast.Children(func() {
		if a.status != "" {
			ui.Text(c, a.status).Selectable().Grow(1).ContentTransition(ctReplace)
		}
		if !busy && a.toast.undoFn != nil {
			undoLabel := a.toast.undoLabel
			if undoLabel == "" {
				undoLabel = "撤销"
			}
			if primaryButton(c, undoLabel).Padding(5, 12).Clicked() {
				fn := a.toast.undoFn
				a.clearStatus()
				fn()
			}
		}
		if busy {
			if glassButton(c, "取消").Label("取消加载").Padding(5, 12).Clicked() {
				a.cancelJobs()
			}
		} else if a.toast.ttl == 0 && a.status != "" {
			if glassButton(c, "关闭").Label("关闭提示").Padding(4, 10).Clicked() {
				a.clearStatus()
			}
		}
	})
}

// linkDialog plays one address the user pastes, without a library.
func (a *app) linkDialog(c *ui.Context) {
	p := paletteFor(c)
	_, winH := c.Size()
	keyedDialog(c, "dlg-link", &a.linkOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(p.dialogScrim).Transition(transDialogScrim)
		panel.Width(520).MaxWidth(520).MaxHeight(max(float32(260), winH-32)).Padding(24).Gap(12).Radius(26).TextColor(p.text)
		applyPane(panel, p, p.dialogMat)
		applyDialogPanelPose(c, panel)
		ui.Text(c, "打开链接").FontSize(24).Bold()
		hint := "粘贴视频直链（mp4 / m3u8）或夸克、UC、阿里云盘、百度网盘分享链接"
		if mpvBackendSupported() || a.openMPV != nil {
			hint = "粘贴视频直链（mp4 / m3u8 / rtsp / rtmp）或夸克、UC、阿里云盘、百度网盘分享链接"
		}
		ui.Scroll(c).FillWidth().MaxHeight(max(float32(120), winH-176)).Gap(12).ScrollbarStyle(appScrollbar).Children(func() {
			ui.Text(c, hint).TextColor(p.textMuted)
			if ui.TextInput(c, &a.rawURL).Label("地址").Placeholder("https://example.com/video.mp4 或网盘分享链接").FillWidth().AutoFocus().Submitted() {
				a.play()
			}
			ui.Collapsible(c.Key("link-advanced"), "高级选项（防盗链请求头，通常留空）", &a.linkAdvanced, func() {
				ui.Column(c).FillWidth().Padding(6, 0, 2, 0).Gap(10).Children(func() {
					ui.TextInput(c, &a.referer).Label("Referer").Placeholder("来源页地址（防盗链时填写）").FillWidth()
					ui.TextInput(c, &a.ua).Label("User-Agent").Placeholder("浏览器标识（通常留空）").FillWidth()
				})
			})
			if a.linkErr != "" {
				ui.Text(c.Key("link-err"), a.linkErr).FontSize(13).TextColor(p.warning).Transition(transInlineBanner)
			}
		})
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if glassButton(c, "取消").Clicked() {
				a.linkOpen = false
				a.linkErr = ""
			}
			if primaryButton(c, "播放").Clicked() {
				a.play()
			}
		})
	})
}
