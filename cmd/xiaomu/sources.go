package main

import (
	"fmt"
	"strings"

	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

type sourcesSiteStats struct {
	healthy        int
	allVisible     int
	panVisible     int
	regularVisible int
	failed         int
	unsupported    int
}

func (a *app) collectSourcesSiteStats() sourcesSiteStats {
	var st sourcesSiteStats
	for i := range a.library.documents {
		doc := &a.library.documents[i]
		for _, site := range doc.doc.Sites {
			if site.Kind.Unsupported() {
				st.unsupported++
				continue
			}
			entry := library.Entry{Site: site, Config: doc.doc, Line: doc.line}
			failed := a.isSiteFailed(entry.Key())
			if failed {
				st.failed++
				if !a.showFailedSites {
					continue
				}
			} else {
				st.healthy++
			}
			st.allVisible++
			if isPanSiteKind(site.Kind) {
				st.panVisible++
			} else {
				st.regularVisible++
			}
		}
	}
	return st
}

func (a *app) liveSourceChannelCount(index int) int {
	if index < 0 || index >= len(a.live.sources) {
		return 0
	}
	if index == a.live.selectedSource && len(a.live.channels) > 0 {
		return len(a.live.channels)
	}
	src := a.live.sources[index]
	if len(src.Channels) > 0 {
		return len(src.Channels)
	}
	if key := liveSourceCacheKey(src); key != "" {
		if cached := a.activeLiveChannelCache()[key]; len(cached) > 0 {
			return len(cached)
		}
	}
	return 0
}

func (a *app) totalLiveChannelCount() int {
	if len(a.live.sources) == 0 {
		return len(a.live.channels)
	}
	total := 0
	for i := range a.live.sources {
		total += a.liveSourceChannelCount(i)
	}
	return total
}

func (a *app) liveSourceSubtitle(index int, src iptv.Source) string {
	var parts []string
	if a.live.url != "" {
		lower := strings.ToLower(strings.TrimSpace(src.URL))
		if lower == "" {
			lower = strings.ToLower(strings.TrimSpace(a.live.url))
		}
		switch {
		case strings.Contains(lower, ".m3u"):
			parts = append(parts, "独立 M3U 订阅")
		case strings.Contains(lower, ".txt"):
			parts = append(parts, "独立 TXT 订阅")
		default:
			parts = append(parts, "独立直播订阅")
		}
	} else {
		parts = append(parts, "影视配置附带")
	}
	epg := strings.TrimSpace(src.EPG)
	if epg == "" {
		if key := liveSourceCacheKey(src); key != "" {
			epg = strings.TrimSpace(a.activeLiveEPGCache()[key])
		}
	}
	if epg == "" && index == a.live.selectedSource {
		epg = strings.TrimSpace(a.live.epgURL)
	}
	if epg != "" {
		parts = append(parts, "含 EPG 节目单")
	}
	if n := a.liveSourceChannelCount(index); n > 0 {
		parts = append(parts, fmt.Sprintf("%d 个频道", n))
	}
	return strings.Join(parts, " · ")
}

// sourcesView implements the S2 two-pillar Sources page: VOD (with embedded
// cloud-drive authorization and filterable site browser) and Live IPTV.
func (a *app) sourcesView(c *ui.Context) {
	if a.sourcesTab != 0 && a.sourcesTab != 1 {
		a.sourcesTab = 0
	}
	if a.sourcesSiteFilter < 0 || a.sourcesSiteFilter > 2 {
		a.sourcesSiteFilter = 0
	}
	l := &a.library
	p := paletteFor(c)
	stats := a.collectSourcesSiteStats()
	liveChannels := a.totalLiveChannelCount()

	vodTabText := "影视点播"
	if stats.healthy > 0 {
		vodTabText = fmt.Sprintf("影视点播（%d 站）", stats.healthy)
	}
	liveTabText := "直播源"
	if liveChannels > 0 {
		liveTabText = fmt.Sprintf("直播源（%d 频道）", liveChannels)
	}

	ui.Column(c).Fill().Gap(12).Children(func() {
		ui.Row(c).FillWidth().Padding(0, scrollGutter, 0, 0).Wrap().Gap(10).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			ui.Row(c).Gap(20).AlignItems(ui.End).Children(func() {
				if underlineTab(c, "sources-tab-vod", vodTabText, a.sourcesTab == 0).Label("影视点播").Clicked() {
					if a.sourcesTab != 0 {
						a.sourcesTab = 0
						a.clearArmed()
					}
				}
				if underlineTab(c, "sources-tab-live", liveTabText, a.sourcesTab == 1).Label("直播源").Clicked() {
					if a.sourcesTab != 1 {
						a.sourcesTab = 1
						a.clearArmed()
					}
				}
			})
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				if a.sourcesTab == 0 {
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
				} else {
					if a.live.url != "" {
						liveRefreshLabel := "刷新直播"
						if a.running(jobLive) {
							liveRefreshLabel = "刷新中…"
						}
						if glassButton(c, liveRefreshLabel).Disabled(a.running(jobLive)).Clicked() {
							a.refreshLive(a.live.url)
						}
					}
					liveBtnLabel := "添加直播源"
					if a.live.url != "" {
						liveBtnLabel = "更换直播源"
					}
					if primaryButton(c, liveBtnLabel).Clicked() {
						a.openLiveDialog()
					}
				}
			})
		})
		if a.speedTest.running {
			ui.Box(c).FillWidth().Padding(0, scrollGutter, 0, 0).Children(func() {
				banner := ui.Row(c).Key("speed-progress").FillWidth().Padding(8, 12).Gap(10).AlignItems(ui.Center).Transition(transInlineBanner)
				applyInnerSurface(banner, p, 9).Children(func() {
					ui.Text(c, fmt.Sprintf("测速中 %d/%d…", a.speedTest.done, a.speedTest.total)).
						FontSize(12.5).TextColor(p.textMuted).ContentTransition(ctNumeric)
					if a.speedTest.total > 0 {
						ui.Progress(c, float64(a.speedTest.done)/float64(a.speedTest.total)).Grow(1).Label("测速进度")
					}
				})
			})
		}
		if a.sourcesTab == 0 {
			a.sourcesVODTab(c, stats)
		} else {
			a.sourcesLiveTab(c, liveChannels)
		}
	})
}

func (a *app) sourcesVODTab(c *ui.Context, stats sourcesSiteStats) {
	l, b := &a.library, &a.browser
	p := paletteFor(c)
	surfaceCard := func(fn func()) {
		col := ui.Column(c).FillWidth().Padding(16, 18).Gap(12)
		applySurface(col, p, 13).Children(fn)
	}
	ui.Scroll(c).Key("sources-scroll-vod").Grow(1).FillWidth().MinHeight(0).Padding(0, scrollGutter, 0, 0).Gap(12).ScrollbarStyle(appScrollbar).Children(func() {
		// Card 1: Current VOD config + embedded cloud-drive authorization strip.
		surfaceCard(func() {
			ui.Row(c).FillWidth().Wrap().Gap(10).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				ui.Column(c).Grow(1).MinWidth(200).Gap(4).Children(func() {
					ui.Text(c, "当前影视配置").FontSize(15.5).Bold()
					address := "还没有添加"
					if l.root != nil {
						address = b.configURL
						if address == "" {
							address = l.root.URL
						}
					}
					ui.Text(c, address).FontSize(12.5).TextColor(p.textMuted).Selectable()
					if b.assets != "" {
						ui.Text(c, "脚本依赖目录："+b.assets).FontSize(12).TextColor(p.textFaint)
					}
				})
				if l.root != nil {
					ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
						refreshLabel := "刷新配置"
						if a.running(jobConfig) {
							refreshLabel = "刷新中…"
						}
						if glassButton(c, refreshLabel).Disabled(a.running(jobConfig)).Clicked() {
							a.refreshConfig(b.configURL)
						}
						if a.armedDangerButton(c, "remove-config", "移除片源", "确认移除？") {
							a.removeConfig()
						}
					})
				}
			})
			if a.running(jobConfig) {
				ui.Text(c, "正在加载配置…").FontSize(12.5).TextColor(p.textMuted)
			}
			ui.Box(c).FillWidth().Height(1).Background(p.divider).PassThrough()
			a.panCredentialsSection(c)
		})

		// Card 2: Full-width available site browser (shown once a VOD config is imported).
		if l.root != nil || len(l.documents) > 0 {
			surfaceCard(func() {
				ui.Row(c).FillWidth().Wrap().Gap(12).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
					ui.Row(c).Wrap().Gap(16).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "可用站点").FontSize(15.5).Bold()
						ui.Row(c).Gap(14).AlignItems(ui.End).Children(func() {
							if underlineTab(c, "sources-filter-all", fmt.Sprintf("全部 (%d)", stats.allVisible), a.sourcesSiteFilter == 0).Label("全部").Clicked() {
								a.sourcesSiteFilter = 0
							}
							if underlineTab(c, "sources-filter-pan", fmt.Sprintf("网盘站 (%d)", stats.panVisible), a.sourcesSiteFilter == 1).Label("网盘站").Clicked() {
								a.sourcesSiteFilter = 1
							}
							if underlineTab(c, "sources-filter-regular", fmt.Sprintf("常规站 (%d)", stats.regularVisible), a.sourcesSiteFilter == 2).Label("常规站").Clicked() {
								a.sourcesSiteFilter = 2
							}
						})
					})
					ui.Text(c, "点击站点直接进入分类浏览").FontSize(12).TextColor(p.textFaint)
				})

				renderedSites := 0
				for i := range l.documents {
					renderedSites += a.documentSites(c, &l.documents[i])
				}

				if len(l.documents) > 0 && stats.allVisible == 0 && stats.failed > 0 && !a.showFailedSites {
					ui.Text(c, "当前没有可用站点，可点击「重新测速」重新检测。").FontSize(13).TextColor(p.textMuted)
				} else if renderedSites == 0 && stats.allVisible > 0 {
					hint := "当前筛选下没有匹配的站点。"
					switch a.sourcesSiteFilter {
					case 1:
						hint = "当前片源中没有可用的网盘站点。"
					case 2:
						hint = "当前片源中没有可用的常规站点。"
					}
					ui.Text(c, hint).FontSize(13).TextColor(p.textMuted)
				} else if stats.allVisible == 0 && stats.failed == 0 && !a.running(jobConfig) {
					ui.Text(c, "当前配置暂无可用站点。").FontSize(13).TextColor(p.textMuted)
				}

				if stats.failed > 0 || stats.unsupported > 0 {
					ui.Box(c).FillWidth().Height(1).Background(p.divider).PassThrough()
					var parts []string
					if stats.failed > 0 {
						parts = append(parts, fmt.Sprintf("%d 个失效站点", stats.failed))
					}
					if stats.unsupported > 0 {
						parts = append(parts, fmt.Sprintf("%d 个暂不兼容站点", stats.unsupported))
					}
					prefix := "已隐藏 "
					if a.showFailedSites && stats.failed > 0 {
						prefix = "当前共含 "
					}
					note := prefix + strings.Join(parts, "、")
					if stats.failed > 0 {
						note += " · 点右上「重新测速」复检"
					}
					ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
						ui.Text(c, note).FontSize(12).TextColor(p.textFaint).Grow(1)
						if stats.failed > 0 {
							toggle := "查看失效站点"
							if a.showFailedSites {
								toggle = "隐藏失效站点"
							}
							if ghostButton(c, toggle).Padding(4, 10).Clicked() {
								a.showFailedSites = !a.showFailedSites
							}
						}
					})
				}
			})
		}
	})
}

func (a *app) sourcesLiveTab(c *ui.Context, liveChannels int) {
	p := paletteFor(c)
	surfaceCard := func(fn func()) {
		col := ui.Column(c).FillWidth().Padding(16, 18).Gap(10)
		applySurface(col, p, 13).Children(fn)
	}
	ui.Scroll(c).Key("sources-scroll-live").Grow(1).FillWidth().MinHeight(0).Padding(0, scrollGutter, 0, 0).Gap(12).ScrollbarStyle(appScrollbar).Children(func() {
		// Card 1: Current live subscription configuration.
		surfaceCard(func() {
			ui.Row(c).FillWidth().Wrap().Gap(10).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "当前直播源配置").FontSize(15.5).Bold()
					if liveChannels > 0 {
						ui.Text(c, fmt.Sprintf("%d 频道", liveChannels)).
							Padding(2, 8).Radius(5).FontSize(11).Bold().
							Background(ui.RGBA(63, 185, 106, 0.16)).
							Border(1, ui.RGBA(63, 185, 106, 0.36)).
							TextColor(p.successText).SingleLine().ContentTransition(ctNumeric)
					}
				})
				if a.live.url != "" {
					if a.armedDangerButton(c, "remove-live", "移除直播源", "确认移除？") {
						a.removeLive()
					}
				}
			})
			about := "还没有添加"
			switch {
			case a.live.url != "":
				about = a.live.url
			case len(a.live.sources) > 0:
				about = fmt.Sprintf("来自影视配置 · %d 个直播来源", len(a.live.sources))
			}
			ui.Text(c, about).FontSize(12.5).TextColor(p.textMuted).Selectable()
			ui.Text(c, "支持 M3U / M3U8、TXT 频道表；若未单独添加，自动继承影视配置中附带的直播来源。").
				FontSize(12).TextColor(p.textFaint)
		})

		// Card 2: Recognized live sources, or friendly empty guidance.
		if len(a.live.sources) == 0 {
			emptyCard := ui.Column(c).FillWidth().Padding(32, 24).Gap(10).Center()
			applySurface(emptyCard, p, 13).Children(func() {
				ui.Text(c, "还没有可用的直播来源").FontSize(16).Bold()
				ui.Text(c, "添加 M3U / M3U8 或 TXT 频道表地址，即可在「直播」页分组看电视。").
					FontSize(13).TextColor(p.textMuted)
				if primaryButton(c, "添加直播源").Label("空态添加直播源").Clicked() {
					a.openLiveDialog()
				}
			})
			return
		}

		surfaceCard(func() {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("已识别的直播来源（%d 个）", len(a.live.sources))).FontSize(15.5).Bold().Grow(1)
				goLive := ui.ButtonBase(c.Key("sources-go-live")).Padding(4, 6).Radius(6).Cursor(ui.CursorPointer).Label("前往直播")
				fg := p.textMuted
				if goLive.Hovered() {
					fg = p.text
				}
				goLive.Children(func() {
					ui.Text(c, "前往直播 ›").FontSize(12).TextColor(fg).SingleLine().PassThrough()
				})
				if goLive.Clicked() {
					a.switchTab(routeLive)
				}
			})
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				for i, src := range a.live.sources {
					active := i == a.live.selectedSource
					name := strings.TrimSpace(src.Name)
					if name == "" {
						name = fmt.Sprintf("直播来源 %d", i+1)
					}
					row := ui.Row(c).Key(fmt.Sprintf("live-src-%d", i)).FillWidth().Padding(11, 14).Gap(12).AlignItems(ui.Center)
					applyInnerSurface(row, p, 10)
					if active {
						row.Border(1, p.accentBorder)
					}
					row.Children(func() {
						dotColor := p.success
						if active {
							dotColor = p.accent
						}
						ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
							ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
								ui.Box(c).Size(7, 7).Radius(4).Background(dotColor).PassThrough()
								ui.Text(c, name).FontSize(13.5).Bold().SingleLine()
								if active {
									specMicroBadge(c, "当前源", true)
								}
							})
							ui.Text(c, a.liveSourceSubtitle(i, src)).FontSize(12).TextColor(p.textMuted).SingleLine()
						})
						if glassButton(c.Key(fmt.Sprintf("enter-live-%d", i)), "进入直播").Padding(6, 14).Label("进入直播 " + name).Clicked() {
							a.enterLiveSource(i)
						}
					})
				}
			})
		})
	})
}

func (a *app) enterLiveSource(index int) {
	if a.live.selectedSource != index || len(a.live.channels) == 0 {
		a.loadLiveSource(index)
	}
	if a.live.selectedSource < 0 {
		a.live.selectedSource = index
	}
	a.switchTab(routeLive)
}

// documentSites renders one line's sites filtered by health and the active S2
// site filter using the shared sourceStatusChip control.
func (a *app) documentSites(c *ui.Context, document *libraryDocument) int {
	p := paletteFor(c)
	name := document.line
	if name == "" {
		name = "已导入片库"
	}
	var visible []library.Entry
	for _, site := range document.doc.Sites {
		if site.Kind.Unsupported() {
			continue
		}
		entry := library.Entry{Site: site, Config: document.doc, Line: document.line}
		if a.isSiteFailed(entry.Key()) && !a.showFailedSites {
			continue
		}
		isPan := isPanSiteKind(site.Kind)
		if a.sourcesSiteFilter == 1 && !isPan {
			continue
		}
		if a.sourcesSiteFilter == 2 && isPan {
			continue
		}
		visible = append(visible, entry)
	}
	if len(visible) == 0 {
		return 0
	}
	label := fmt.Sprintf("%s · %d 个站点", name, len(visible))
	renderChips := func() {
		ui.Row(c).FillWidth().Wrap().Padding(4, 0, 6, 0).Gap(8).Children(func() {
			for i, entry := range visible {
				key := entry.Key()
				failed := a.isSiteFailed(key)
				isPan := isPanSiteKind(entry.Site.Kind)
				state := sourceChipNormal
				latencyText := ""
				tier := 0
				if failed {
					state = sourceChipFailed
					latencyText = "失效"
				} else if d, ok := a.latency.get(key); ok {
					latencyText = formatLatency(d)
					tier = latencyTier(d.Milliseconds())
				}
				chipKey := fmt.Sprintf("site-%d-%s", i, entry.Site.Key)
				if sourceStatusChip(c, chipKey, entry.Site.Name, isPan, latencyText, tier, state).Clicked() {
					a.openSite(entry)
				}
			}
		})
	}
	// When an explicit site-type filter is active, reveal matching sites directly
	// even if their line is collapsed under "全部".
	if a.sourcesSiteFilter != 0 {
		ui.Column(c).Key(document.doc.URL + "-filtered").FillWidth().Gap(4).Children(func() {
			ui.Text(c, label).FontSize(12.5).TextColor(p.textMuted)
			renderChips()
		})
		return len(visible)
	}
	ui.Collapsible(c.Key(document.doc.URL), label, &document.open, renderChips)
	return len(visible)
}
