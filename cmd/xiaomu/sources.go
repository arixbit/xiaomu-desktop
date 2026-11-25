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
	p := paletteFor(c)
	card := func(fn func()) {
		col := ui.Column(c).FillWidth().Padding(18).Gap(12)
		applySurface(col, p, 22).Children(fn)
	}
	ui.Scroll(c).Fill().Gap(14).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "首页和搜索会汇集这里的全部站点。").TextColor(p.textMuted).Grow(1)
			if l.root != nil && (a.speedTest.running || len(a.speedTestEntries()) > 0) {
				if a.speedTest.running {
					if glassButton(c, "停止测速").Clicked() {
						a.cancelSourceSpeedTest()
						a.setNotice("已停止测速")
					}
				} else if glassButton(c, "重新测速").Clicked() {
					a.startSourceSpeedTest()
				}
			}
			importBtnLabel := "添加片源"
			if l.root != nil {
				importBtnLabel = "更换片源"
			}
			if primaryButton(c, importBtnLabel).Clicked() {
				a.openImportDialog()
			}
		})
		if a.speedTest.running {
			ui.Row(c).Key("speed-progress").FillWidth().Gap(10).AlignItems(ui.Center).Transition(transInlineBanner).Children(func() {
				ui.Text(c, fmt.Sprintf("测速中 %d/%d…", a.speedTest.done, a.speedTest.total)).
					FontSize(13).TextColor(p.textMuted).ContentTransition(ctNumeric)
				if a.speedTest.total > 0 {
					ui.Progress(c, float64(a.speedTest.done)/float64(a.speedTest.total)).Grow(1).Label("测速进度")
				}
			})
		}
		card(func() {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, "影视配置").FontSize(17).Bold()
					address := "还没有添加"
					if l.root != nil {
						address = b.configURL
					}
					ui.Text(c, address).FontSize(13).TextColor(p.textMuted).Selectable()
					if b.assets != "" {
						ui.Text(c, "脚本依赖目录："+b.assets).FontSize(13).TextColor(p.textMuted)
					}
				})
				if l.root == nil {
					return
				}
				refreshLabel := "刷新配置"
				if a.running(jobConfig) {
					refreshLabel = "刷新中…"
				}
				if glassButton(c, refreshLabel).Disabled(a.running(jobConfig)).Clicked() {
					a.refreshConfig(b.configURL)
				}
				ui.Box(c).Width(4)
				if a.armedDangerButton(c, "remove-config", "移除片源", "确认移除？") {
					a.removeConfig()
				}
			})
			if a.running(jobConfig) {
				ui.Text(c, "正在加载配置…").TextColor(p.textMuted)
			}
			totalVisible, totalFailed, totalUnsupported := 0, 0, 0
			for i := range l.documents {
				v, f, u := a.documentSites(c, &l.documents[i])
				totalVisible += v
				totalFailed += f
				totalUnsupported += u
			}
			if l.root != nil && len(l.documents) > 0 && totalVisible == 0 && totalFailed > 0 && !a.showFailedSites {
				ui.Text(c, "当前没有可用站点，可点击「重新测速」重新检测。").FontSize(13).TextColor(p.textMuted)
			}
			if totalFailed > 0 || totalUnsupported > 0 {
				var parts []string
				if totalFailed > 0 {
					parts = append(parts, fmt.Sprintf("%d 个失效站点", totalFailed))
				}
				if totalUnsupported > 0 {
					parts = append(parts, fmt.Sprintf("%d 个暂不兼容站点", totalUnsupported))
				}
				prefix := "已隐藏 "
				if a.showFailedSites && totalFailed > 0 {
					prefix = "当前共含 "
				}
				note := prefix + strings.Join(parts, "、")
				if totalFailed > 0 {
					note += " · 点「重新测速」复检"
				}
				ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, note).FontSize(12).TextColor(p.textMuted).Grow(1)
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
			ui.Row(c).FillWidth().Wrap().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(180).Gap(3).Children(func() {
					ui.Text(c, "直播源").FontSize(17).Bold()
					about := "还没有添加"
					switch {
					case a.live.url != "":
						about = a.live.url
					case len(a.live.sources) > 0:
						about = fmt.Sprintf("来自影视配置 · %d 个直播来源", len(a.live.sources))
					}
					ui.Text(c, about).FontSize(13).TextColor(p.textMuted).Selectable()
				})
				ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
					addLabel := "添加直播源"
					if a.live.url != "" {
						addLabel = "更换直播源"
					}
					if glassButton(c, addLabel).Clicked() {
						a.openLiveDialog()
					}
					if a.live.url != "" {
						liveRefreshLabel := "刷新直播"
						if a.running(jobLive) {
							liveRefreshLabel = "刷新中…"
						}
						if glassButton(c, liveRefreshLabel).Disabled(a.running(jobLive)).Clicked() {
							a.refreshLive(a.live.url)
						}
						ui.Box(c).Width(4)
						if a.armedDangerButton(c, "remove-live", "移除直播源", "确认移除？") {
							a.removeLive()
						}
					}
				})
			})
		})
	})
}

// documentSites lists one line's valid sites; choosing one browses it alone.
// Invalid sites (unsupported or live-failed) are filtered out by default.
func (a *app) documentSites(c *ui.Context, document *libraryDocument) (visibleCount, failedCount, unsupportedCount int) {
	p := paletteFor(c)
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
					ui.Row(c).Key(i).Gap(6).AlignItems(ui.Center).Children(func() {
						if glassButton(c, entry.Site.Name).Clicked() {
							a.openSite(entry)
						}
						if a.isSiteFailed(key) {
							ui.Text(c, "失效").FontSize(12).TextColor(p.warning)
						} else if d, ok := a.latency.get(key); ok {
							ui.Text(c, formatLatency(d)).FontSize(12).TextColor(p.textMuted).ContentTransition(ctNumeric)
						}
					})
				}
			})
		})
	})
	return visibleCount, failedCount, unsupportedCount
}
