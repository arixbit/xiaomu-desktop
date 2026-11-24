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
)

var tabs = []struct {
	kind  routeKind
	label string
}{
	{routeHome, "首页"},
	{routeLive, "直播"},
	{routeHistory, "播放历史"},
	{routeFavorites, "追剧"},
	{routeSources, "片源"},
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
	btn := ui.Button(c, label).Radius(20).Padding(9, 16).TextColor(p.text)
	if p.reduceTransparency {
		bg := p.surfaceSubtle
		if btn.Hovered() {
			bg = p.surfaceHover
		}
		btn.Background(btn.AnimateColor("glass-bg", bg, animColor)).Border(1, p.border)
	} else {
		btn.Material(p.controlMat)
		if !p.dark || p.highContrast {
			btn.Border(1, p.border)
		}
	}
	return applyPressPose(btn)
}

func primaryButton(c *ui.Context, label string) ui.Element {
	p := paletteFor(c)
	btn := ui.Button(c, label).Radius(20).Padding(9, 18).TextColor(p.accentText)
	if p.reduceTransparency {
		btn.Background(p.accent)
	} else {
		btn.Material(p.primaryMat)
	}
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
	}
}

func (a *app) pageTitle() string {
	switch a.route() {
	case routeSearch:
		return "搜索结果"
	case routeDetail:
		return "影片详情"
	case routeSite:
		return a.browser.site.Label()
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
// toolbar and the active page.
func (a *app) shell(c *ui.Context) {
	p := paletteFor(c)
	ui.Box(c).Fill().LinearGradient(p.backdrop).TextColor(p.text).Children(func() {
		ui.Row(c).Fill().Padding(10).Gap(10).Children(func() {
			a.sidebar(c)
			ui.Column(c).Grow(1).FillHeight().MinWidth(0).Padding(4, 8, 8, 8).Gap(14).Children(func() {
				a.toolbar(c)
				rt := routeTransition(a.navDir)
				pageHost := ui.Box(c).Grow(1).FillWidth().MinHeight(0)
				if rt.Exit != nil {
					pageHost.ChildExit(*rt.Exit)
				}
				pageHost.Children(func() {
					ui.Box(c.Key(a.route())).Fill().Transition(rt).Children(func() {
						switch a.route() {
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
						case routeDetail:
							a.detailView(c)
						case routeSite:
							a.siteView(c)
						default:
							a.homeView(c)
						}
					})
				})
			})
		})
		a.statusView(c)
		a.importDialog(c)
		a.liveDialog(c)
		a.linkDialog(c)
		a.panAuthPromptDialog(c)
	})
}

func (a *app) sidebar(c *ui.Context) {
	p := paletteFor(c)
	winW, winH := c.Size()
	sideW, rail := activeSidebarWidth(winW)
	shortWin := winH > 0 && winH < 480
	top := max(float32(52), c.TitleBar().Bottom+14)
	padX, gap := float32(10), float32(4)
	if rail {
		padX = 8
	}
	if shortWin {
		gap = 2
	}
	side := ui.Column(c.Key("sidebar")).Width(sideW).FillHeight().Radius(22).Padding(top, padX, 10, padX).Gap(gap).
		Transition(ui.ElementTransition{Animation: &animSurface, Width: true})
	applyPane(side, p, p.paneMat)
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
			for _, tab := range tabs {
				if railNavButton(c, tab.kind, tab.label, a.root() == tab.kind, rowH).Clicked() {
					a.switchTab(tab.kind)
				}
			}
			ui.Spacer(c)
			btnH := float32(38)
			if shortWin {
				btnH = 32
			}
			if railIconButton(c, navIconFile, "打开文件", btnH).Clicked() {
				a.openLocalFileDialog()
			}
			if railIconButton(c, navIconLink, "打开链接", btnH).Clicked() {
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
		for _, tab := range tabs {
			row := listRow(c, tab.label, a.root() == tab.kind)
			if shortWin {
				row.Height(32)
			}
			if row.Clicked() {
				a.switchTab(tab.kind)
			}
		}
		ui.Spacer(c)
		openFileBtn := glassButton(c, "打开文件").FillWidth()
		openLinkBtn := glassButton(c, "打开链接").FillWidth()
		updBtn := updateButton(c, a.availableUpdate)
		if shortWin {
			openFileBtn.Padding(6, 12)
			openLinkBtn.Padding(6, 12)
			updBtn.Padding(6, 12)
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

func railNavButton(c *ui.Context, kind routeKind, label string, selected bool, height float32) ui.Element {
	p := paletteFor(c)
	row := ui.Row(c.Key("rail-"+label)).FillWidth().Height(height).Radius(12).
		Center().Cursor(ui.CursorPointer).Label(label).Tooltip(label)
	targetBg := ui.Transparent
	targetFg := p.textMuted
	if selected {
		targetBg = p.surfaceSelected
		targetFg = p.text
	} else if row.Hovered() {
		targetBg = p.surfaceHover
		targetFg = p.text
	}
	row.Background(row.AnimateColor("rail-bg", targetBg, animColor))
	applyPressPose(row)
	fg := row.AnimateColor("rail-fg", targetFg, animColor)
	row.Children(func() {
		ui.Icon(c, tabIcon(kind)).Size(19, 19).TextColor(fg).PassThrough()
	})
	return row
}

func railIconButton(c *ui.Context, icon *ui.SVG, label string, height float32) ui.Element {
	p := paletteFor(c)
	btn := ui.Button(c.Key("rail-action-"+label), "").FillWidth().Height(height).Radius(14).
		Center().Label(label).Tooltip(label).TextColor(p.text)
	if p.reduceTransparency {
		bg := p.surfaceSubtle
		if btn.Hovered() {
			bg = p.surfaceHover
		}
		btn.Background(btn.AnimateColor("rail-btn-bg", bg, animColor)).Border(1, p.border)
	} else {
		btn.Material(p.controlMat)
		if !p.dark || p.highContrast {
			btn.Border(1, p.border)
		}
	}
	applyPressPose(btn)
	btn.Children(func() {
		ui.Icon(c, icon).Size(17, 17).TextColor(p.text).PassThrough()
	})
	return btn
}

func (a *app) toolbar(c *ui.Context) {
	width, _ := c.Size()
	sideW, _ := activeSidebarWidth(width)
	// Match shell's outer padding, sidebar, gap and content padding.
	contentWidth := max(0, width-20-sideW-10-16)
	// Equal space on either side keeps search centered as back appears.
	searchWidth := min(float32(560), contentWidth-2*192)
	compact := searchWidth < 280
	height, searchTop, searchLeft := float32(44), float32(2), (contentWidth-searchWidth)/2
	navWidth := contentWidth
	if a.library.root != nil {
		if compact {
			height, searchTop, searchLeft, searchWidth = 92, 52, 0, contentWidth
		} else {
			navWidth = searchLeft - 12
		}
	}
	ui.Box(c).Key("toolbar").FillWidth().Height(height).Children(func() {
		bar := ui.Row(c).FillWidth().Height(44).AlignItems(ui.Center)
		if dragStripHeight(c) > 0 {
			bar.DragWindow().Label("拖动窗口")
		}
		bar.Children(func() {
			ui.Row(c).Width(navWidth).MinWidth(0).Gap(10).AlignItems(ui.Center).Children(func() {
				if len(a.nav) > 1 {
					if glassButton(c.Key("toolbar-back"), "返回").Transition(transBackButton).Clicked() {
						a.pop()
					}
				}
				ui.Text(c, a.pageTitle()).FontSize(22).Bold().SingleLine().Grow(1).MinWidth(0).
					ContentTransition(ctReplace)
			})
		})
		if a.library.root != nil {
			// Keep the same parent at both sizes so editing and the popup
			// survive resizing. Input sits above the draggable toolbar.
			a.librarySearch(c).Absolute().Top(searchTop).Left(searchLeft).Width(searchWidth)
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

func (a *app) librarySearch(c *ui.Context) ui.Element {
	p := paletteFor(c)
	return ui.Box(c).Key("library-search").Height(40).Children(func() {
		field := ui.Autocomplete(c, &a.browser.query, a.searchSuggestions()).Label("片名").Placeholder("搜索整个片库").
			Fill().Padding(0, 58, 0, 40).FontSize(16).Radius(20).
			FocusBind(&a.searchFocused, true).Bind(&a.searchFocus)
		if p.reduceTransparency {
			field.Background(p.surface).Border(1, p.border)
		} else {
			field.Material(p.controlMat)
			if !p.dark || p.highContrast {
				field.Border(1, p.border)
			}
		}
		if field.Changed() {
			a.focusSearchGrid = false
		}
		if field.Submitted() {
			a.searchLibrary()
		}
		ui.Icon(c, librarySearchIcon).Size(18, 18).Absolute().Left(14).Top(11).TextColor(p.textMuted).PassThrough()
		ui.Text(c, searchShortcutHint()).FontSize(12).TextColor(p.textMuted).Absolute().Right(14).Top(11).PassThrough()
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
	ui.DialogBase(c, &a.linkOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(p.dialogScrim).Transition(transDialogScrim)
		panel.Width(520).MaxWidth(520).Padding(26).Gap(14).Radius(26).TextColor(p.text).Transition(transDialogPanel)
		applyPane(panel, p, p.dialogMat)
		ui.Text(c, "打开链接").FontSize(24).Bold()
		ui.Text(c, "粘贴视频直链（mp4 / m3u8）或夸克、UC、阿里云盘、百度网盘分享链接").TextColor(p.textMuted)
		if ui.TextInput(c, &a.rawURL).Label("地址").Placeholder("https://example.com/video.mp4 或网盘分享链接").FillWidth().Submitted() {
			a.play()
		}
		ui.Collapsible(c.Key("link-advanced"), "高级选项（请求头）", &a.linkAdvanced, func() {
			ui.Column(c).FillWidth().Padding(6, 0, 2, 0).Gap(10).Children(func() {
				ui.TextInput(c, &a.referer).Label("Referer").Placeholder("来源页地址（可选）").FillWidth()
				ui.TextInput(c, &a.ua).Label("User-Agent").Placeholder("自定义客户端标识（可选）").FillWidth()
			})
		})
		if a.linkErr != "" {
			ui.Text(c, a.linkErr).FontSize(13).TextColor(p.warning)
		}
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
