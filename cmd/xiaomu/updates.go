package main

import (
	"runtime"

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
	p := paletteFor(c)
	label := "检查更新"
	if version != "" {
		label = "有新版本"
	}
	button := glassButton(c, "").FillWidth().Gap(8).Label(label)
	if version != "" {
		button.Tooltip("新版本 " + version + " 可用，点击查看更新")
	}
	button.Children(func() {
		ui.Text(c, label).SingleLine().ContentTransition(ctReplace)
		if version != "" {
			ui.Box(c).Size(7, 7).Radius(4).Background(p.accent).Label("新版本提示").PassThrough()
		}
	})
	return button
}

func updateRailButton(c *ui.Context, version string, height float32) ui.Element {
	p := paletteFor(c)
	label := "检查更新"
	if version != "" {
		label = "有新版本"
	}
	btn := ui.Button(c.Key("rail-update"), "").FillWidth().Height(height).Radius(14).
		Center().Label(label).TextColor(p.text)
	if version != "" {
		btn.Tooltip("新版本 " + version + " 可用，点击查看更新")
	} else {
		btn.Tooltip(label)
	}
	if p.reduceTransparency {
		bg := p.surfaceSubtle
		if btn.Hovered() {
			bg = p.surfaceHover
		}
		btn.Background(btn.AnimateColor("rail-upd-bg", bg, animColor)).Border(1, p.border)
	} else {
		btn.Material(p.controlMat)
		if !p.dark || p.highContrast {
			btn.Border(1, p.border)
		}
	}
	applyPressPose(btn)
	btn.Children(func() {
		ui.Icon(c, navIconUpdate).Size(17, 17).TextColor(p.text).PassThrough()
		if version != "" {
			ui.Box(c).Size(7, 7).Radius(4).Absolute().Right(10).Top(8).Background(p.accent).Label("新版本提示").PassThrough()
		}
	})
	return btn
}

// initUpdates runs before App.Run. The native plugin checks in the background
// and asks before installing, unless the user opts into automatic downloads.
func (a *app) initUpdates() {
	mygo.Use(updatenative.New(updater.Options{Language: "zh-Hans"}))
	updater.OnInstalled(a.flushHistory)
	fileItems := []*mygo.MenuItem{
		{
			Label:       "打开视频文件…",
			Accelerator: "CmdOrCtrl+O",
			Click: func(*mygo.MenuItem, *mygo.Window) {
				a.deferUntilReady(a.openLocalFileDialog)
			},
		},
		{Role: mygo.RoleOpenRecent},
		mygo.Separator(),
	}
	if runtime.GOOS == "darwin" {
		fileItems = append(fileItems, &mygo.MenuItem{Role: mygo.RoleClose})
	} else {
		fileItems = append(fileItems, &mygo.MenuItem{Role: mygo.RoleQuit})
	}
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
		{Label: "文件", Submenu: fileItems},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	}))
}
