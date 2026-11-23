package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	updatenative "github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
)

// watchUpdates connects the existing background checks to the main window.
// Subscribe before reading so a check completed during startup is also shown.
func (a *app) watchUpdates() {
	refresh := func() { a.setAvailableUpdate(updater.AvailableVersion()) }
	a.window.OnClosed(updater.OnChange(refresh))
	refresh()
}

func (a *app) setAvailableUpdate(version string) {
	apply := func() {
		if !a.closed {
			a.availableUpdate = version
		}
	}
	if a.dispatch == nil {
		apply()
	} else {
		a.dispatch(apply)
	}
}

func updateButton(c *ui.Context, version string) ui.Element {
	label := "检查更新"
	if version != "" {
		label = "有新版本"
	}
	button := glassButton(c, "").FillWidth().Gap(8).Label(label)
	if version != "" {
		button.Tooltip("新版本 " + version + " 可用，点击查看更新")
	}
	button.Children(func() {
		ui.Text(c, label).SingleLine()
		if version != "" {
			ui.Box(c).Size(7, 7).Radius(4).Background(ui.RGB(255, 90, 64)).Label("新版本提示").PassThrough()
		}
	})
	return button
}

// initUpdates runs before App.Run. The native plugin checks in the background
// and asks before installing, unless the user opts into automatic downloads.
func (a *app) initUpdates() {
	mygo.Use(updatenative.New(updater.Options{Language: "zh-Hans"}))
	updater.OnInstalled(a.flushHistory)
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: "小幕", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			updater.MenuItem(),
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleFileMenu},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	}))
}
