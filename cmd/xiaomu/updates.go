package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	updatenative "github.com/egoist/mygo/plugins/updater/native"
)

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
