package main

import (
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"
)

// routeKind names a page. The first four are the sidebar's tabs; the others
// are pushed above a tab and left with the back button.
type routeKind int

const (
	routeHome routeKind = iota
	routeLive
	routeHistory
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
	{routeSources, "片源"},
}

// Liquid Glass floats over the content: panes for the sidebar and panels,
// clear capsules for controls, and one tint for the primary action.
var (
	glassPane    = glass.Glass{Clarity: 0.5}
	glassControl = glass.Glass{Style: glass.Clear, Interactive: true}
	glassPrimary = glass.Glass{Tint: ui.RGBA(255, 90, 64, 0.92), Interactive: true}
	accentInk    = ui.Hex("#1a0f0c")
	textMuted    = ui.RGBA(243, 239, 231, 0.78)
	surface      = ui.RGBA(12, 13, 20, 0.55)
	backdrop     = ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: ui.Hex("#0b1530"), At: 0},
		{Color: ui.Hex("#222c4e"), At: 0.42},
		{Color: ui.Hex("#6b4057"), At: 0.74},
		{Color: ui.Hex("#241727"), At: 1},
	}}
)

func glassButton(c *ui.Context, label string) ui.Element {
	return ui.Button(c, label).Radius(20).Padding(9, 16).Material(glassControl)
}

func primaryButton(c *ui.Context, label string) ui.Element {
	return ui.Button(c, label).Radius(20).Padding(9, 18).Material(glassPrimary).TextColor(accentInk)
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
	}
}

// pop leaves the page on show, stopping the work only that page needed.
func (a *app) pop() {
	if len(a.nav) <= 1 {
		return
	}
	left := a.route()
	a.nav = a.nav[:len(a.nav)-1]
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
	a.status = ""
}

func (a *app) switchTab(kind routeKind) {
	for len(a.nav) > 1 {
		a.pop()
	}
	a.nav = []routeKind{kind}
	a.status = ""
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

// shell is every page but the player: the backdrop, the floating sidebar,
// the toolbar and the page on show.
func (a *app) shell(c *ui.Context) {
	ui.Themed(c, ui.DarkTheme()).Fill().LinearGradient(backdrop).Children(func() {
		ui.Row(c).Fill().Padding(10).Gap(10).Children(func() {
			a.sidebar(c)
			ui.Column(c).Grow(1).FillHeight().MinWidth(0).Padding(4, 8, 8, 8).Gap(14).Children(func() {
				a.toolbar(c)
				ui.Box(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
					switch a.route() {
					case routeLive:
						a.liveView(c)
					case routeHistory:
						a.historyView(c)
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
		a.statusView(c)
		a.importDialog(c)
		a.liveDialog(c)
		a.linkDialog(c)
	})
}

func (a *app) sidebar(c *ui.Context) {
	// HiddenInset places macOS window controls over this pane.
	top := max(float32(52), c.TitleBar().Bottom+14)
	ui.Column(c).Width(204).FillHeight().Radius(22).Padding(top, 10, 10, 10).Gap(4).
		Material(glassPane).Children(func() {
		ui.Text(c, "小幕").FontSize(20).Bold().Padding(0, 12, 12, 12)
		for _, tab := range tabs {
			if listRow(c, tab.label, a.root() == tab.kind).Clicked() {
				a.switchTab(tab.kind)
			}
		}
		ui.Spacer(c)
		if glassButton(c, "打开链接").FillWidth().Clicked() {
			a.linkOpen = true
		}
		if glassButton(c, "检查更新").FillWidth().Clicked() {
			updater.CheckForUpdates()
		}
	})
}

func (a *app) toolbar(c *ui.Context) {
	ui.Row(c).FillWidth().Height(44).Gap(10).AlignItems(ui.Center).Children(func() {
		if glassButton(c, "返回").Disabled(len(a.nav) <= 1).Clicked() {
			a.pop()
		}
		ui.Text(c, a.pageTitle()).FontSize(22).Bold().SingleLine().Grow(1)
		if a.library.root == nil {
			return
		}
		if ui.SearchField(c, &a.browser.query).Label("片名").Placeholder("搜索整个片库").
			Width(300).Radius(20).Material(glassControl).Submitted() {
			a.searchLibrary()
		}
	})
}

func (a *app) statusView(c *ui.Context) {
	busy := a.busy()
	if a.status == "" && !busy {
		return
	}
	ui.Row(c).Absolute().Right(16).Bottom(16).MaxWidth(640).Padding(9, 13).Gap(10).
		Radius(16).Material(glass.Glass{}).AlignItems(ui.Center).Children(func() {
		if a.status != "" {
			ui.Text(c, a.status).Selectable().Grow(1)
		}
		if busy && ui.Button(c, "取消加载").Clicked() {
			a.cancelJobs()
		}
	})
}

// linkDialog plays one address the user pastes, without a library.
func (a *app) linkDialog(c *ui.Context) {
	ui.DialogBase(c, &a.linkOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(2, 8, 22, 0.67))
		panel.Width(520).MaxWidth(520).Padding(26).Gap(14).Radius(26).Material(glass.Glass{})
		ui.Text(c, "打开链接").FontSize(24).Bold()
		ui.Text(c, "粘贴 mp4 / m3u8 视频直链").TextColor(textMuted)
		ui.TextInput(c, &a.rawURL).Label("地址").Placeholder("https://example.com/video.mp4").FillWidth()
		ui.TextInput(c, &a.referer).Label("Referer").Placeholder("Referer（可选）").FillWidth()
		ui.TextInput(c, &a.ua).Label("User-Agent").Placeholder("User-Agent（可选）").FillWidth()
		if a.status != "" {
			ui.Text(c, a.status).TextColor(textMuted)
		}
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if glassButton(c, "取消").Clicked() {
				a.linkOpen = false
			}
			if primaryButton(c, "播放").Clicked() {
				a.play()
			}
		})
	})
}
