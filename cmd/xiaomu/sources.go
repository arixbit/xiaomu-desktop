package main

import (
	"fmt"

	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

// sourcesView is the one place where sources are added, refreshed and
// removed, and where a single site can be browsed.
func (a *app) sourcesView(c *ui.Context) {
	l, b := &a.library, &a.browser
	card := func(fn func()) {
		ui.Column(c).FillWidth().Padding(18).Gap(12).Radius(22).Background(surface).Children(fn)
	}
	ui.Scroll(c).Fill().Gap(14).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "首页和搜索会汇集这里的全部站点。").TextColor(textMuted).Grow(1)
			if primaryButton(c, "添加片源").Clicked() {
				b.importOpen = true
			}
		})
		card(func() {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, "影视配置").FontSize(17).Bold()
					address := "还没有添加"
					if l.root != nil {
						address = b.configURL
					}
					ui.Text(c, address).FontSize(13).TextColor(textMuted).Selectable()
					if b.assets != "" {
						ui.Text(c, "脚本目录："+b.assets).FontSize(13).TextColor(textMuted)
					}
				})
				if l.root == nil {
					return
				}
				if glassButton(c, "刷新配置").Disabled(a.running(jobConfig)).Clicked() {
					a.loadConfig(b.configURL)
				}
				if glassButton(c, "移除片源").Clicked() {
					a.removeConfig()
				}
			})
			if a.running(jobConfig) {
				ui.Text(c, "正在加载配置…").TextColor(textMuted)
			}
			for i := range l.documents {
				a.documentSites(c, &l.documents[i])
			}
		})
		card(func() {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, "直播源").FontSize(17).Bold()
					about, add := "还没有添加", "添加直播源"
					switch {
					case a.live.url != "":
						about, add = a.live.url, "更换直播源"
					case len(a.live.sources) > 0:
						about = fmt.Sprintf("来自影视配置 · %d 个直播来源", len(a.live.sources))
					}
					ui.Text(c, about).FontSize(13).TextColor(textMuted).Selectable()
					if glassButton(c, add).AlignSelf(ui.Start).Clicked() {
						a.live.importOpen = true
					}
				})
				if a.live.url != "" && glassButton(c, "移除直播源").Clicked() {
					a.removeLive()
				}
			})
		})
	})
}

// documentSites lists one line's sites; choosing one browses it alone.
func (a *app) documentSites(c *ui.Context, document *libraryDocument) {
	name := document.line
	if name == "" {
		name = "已导入片库"
	}
	skipped := 0
	for _, site := range document.doc.Sites {
		if site.Kind.Unsupported() {
			skipped++
		}
	}
	label := fmt.Sprintf("%s · %d 个站点", name, len(document.doc.Sites)-skipped)
	ui.Collapsible(c.Key(document.doc.URL), label, &document.open, func() {
		ui.Column(c).FillWidth().Padding(4, 0, 8, 0).Gap(10).Children(func() {
			ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
				for i, site := range document.doc.Sites {
					if site.Kind.Unsupported() {
						continue
					}
					if glassButton(c, site.Name).Key(i).Clicked() {
						a.openSite(library.Entry{Site: site, Config: document.doc, Line: document.line})
					}
				}
			})
			if skipped > 0 {
				ui.Text(c, fmt.Sprintf("另有 %d 个站点暂不兼容，已从首页和搜索中跳过", skipped)).FontSize(12).TextColor(textMuted)
			}
		})
	})
}
