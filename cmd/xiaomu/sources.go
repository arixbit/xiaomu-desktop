package main

import (
	"fmt"
	"strings"

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
			// One press re-measures every site; the button stays
			// disabled until the last site answers or times out.
			if glassButton(c, "重新测速").Disabled(a.speedTest.running || a.library.root == nil).Clicked() {
				a.startSourceSpeedTest()
			}
			if primaryButton(c, "添加片源").Clicked() {
				b.importOpen = true
			}
		})
		if a.speedTest.running {
			ui.Text(c, fmt.Sprintf("测速中 %d/%d…", a.speedTest.done, a.speedTest.total)).TextColor(textMuted)
		}
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
			totalVisible, totalFailed, totalUnsupported := 0, 0, 0
			for i := range l.documents {
				v, f, u := a.documentSites(c, &l.documents[i])
				totalVisible += v
				totalFailed += f
				totalUnsupported += u
			}
			if l.root != nil && len(l.documents) > 0 && totalVisible == 0 && totalFailed > 0 && !a.showFailedSites {
				ui.Text(c, "当前没有可用站点，可点击「重新测速」重新检测。").FontSize(13).TextColor(textMuted)
			}
			if totalFailed > 0 || totalUnsupported > 0 {
				var parts []string
				if totalFailed > 0 {
					parts = append(parts, fmt.Sprintf("%d 个失效站点", totalFailed))
				}
				if totalUnsupported > 0 {
					parts = append(parts, fmt.Sprintf("%d 个不兼容站点", totalUnsupported))
				}
				prefix := "已过滤 "
				if a.showFailedSites && totalFailed > 0 {
					prefix = "当前共含 "
				}
				ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, prefix+strings.Join(parts, "、")+"（点击「重新测速」可重新检测）").FontSize(12).TextColor(textMuted).Grow(1)
					if totalFailed > 0 {
						toggle := "查看失效站点"
						if a.showFailedSites {
							toggle = "隐藏失效站点"
						}
						if glassButton(c, toggle).Padding(5, 10).Clicked() {
							a.showFailedSites = !a.showFailedSites
						}
					}
				})
			}
		})
		a.panCredentialsCard(c)
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
				if a.live.url != "" {
					if glassButton(c, "刷新直播").Disabled(a.running(jobLive)).Clicked() {
						a.importLive(a.live.url)
					}
					if glassButton(c, "移除直播源").Clicked() {
						a.removeLive()
					}
				}
			})
		})
	})
}

// documentSites lists one line's valid sites; choosing one browses it alone.
// Invalid sites (unsupported or live-failed) are filtered out by default.
func (a *app) documentSites(c *ui.Context, document *libraryDocument) (visibleCount, failedCount, unsupportedCount int) {
	name := document.line
	if name == "" {
		name = "已导入片库"
	}
	var visible []library.Entry
	for _, site := range document.doc.Sites {
		if site.Kind.Unsupported() {
			unsupportedCount++
			continue
		}
		entry := library.Entry{Site: site, Config: document.doc, Line: document.line}
		failed := a.isSiteFailed(entry.Key())
		if failed {
			failedCount++
			if !a.showFailedSites {
				continue
			}
		}
		visible = append(visible, entry)
	}
	visibleCount = len(visible)
	if visibleCount == 0 {
		return visibleCount, failedCount, unsupportedCount
	}
	label := fmt.Sprintf("%s · %d 个站点", name, visibleCount)
	ui.Collapsible(c.Key(document.doc.URL), label, &document.open, func() {
		ui.Column(c).FillWidth().Padding(4, 0, 8, 0).Gap(10).Children(func() {
			ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
				for i, entry := range visible {
					key := entry.Key()
					// The button stays the site's name; its last
					// measured latency sits beside it, in the same
					// form as the detail page. An unmeasured site
					// shows nothing, never a made-up number.
					ui.Row(c).Key(i).Gap(6).AlignItems(ui.Center).Children(func() {
						if glassButton(c, entry.Site.Name).Clicked() {
							a.openSite(entry)
						}
						if a.isSiteFailed(key) {
							ui.Text(c, "失效").FontSize(12).TextColor(textMuted)
						} else if d, ok := a.latency.get(key); ok {
							ui.Text(c, formatLatency(d)).FontSize(13).TextColor(textMuted)
						}
					})
				}
			})
		})
	})
	return visibleCount, failedCount, unsupportedCount
}
