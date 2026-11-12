package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
	"github.com/arixbit/xiaomu-desktop/internal/source"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

type browser struct {
	configURL, assets, line, site, query string
	sidebar                              string
	importOpen                           bool
	selectedVideo                        int
	grid                                 ui.GridState
	doc                                  *config.Config
	session                              source.Session
	categories                           []catalog.Category
	category, search                     string
	page                                 int
	results                              catalog.Page
	detail                               *catalog.Detail
}

// Only the UI thread changes app state. A job returns an update to apply on
// that thread and optional cleanup if cancellation makes the result obsolete.
type jobResult struct {
	apply   func()
	discard func()
}

func (a *app) run(label string, work func(context.Context) (jobResult, error)) {
	if a.busy || a.closed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	a.cancel, a.busy, a.status = cancel, true, label+"…"
	dispatch := a.dispatch
	execute := func() {
		result, err := work(ctx)
		finish := func() {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			cancel()
			a.cancel, a.busy = nil, false
			if err != nil || a.closed {
				if result.discard != nil {
					result.discard()
				}
				if !a.closed {
					switch {
					case errors.Is(err, context.Canceled):
						a.status = "已取消，可以重试或选择其他站点"
					case errors.Is(err, context.DeadlineExceeded):
						a.status = "请求超时，请重试或换一个站点"
					default:
						a.status = label + "失败：" + err.Error()
					}
				}
				return
			}
			if result.apply != nil {
				result.apply()
			}
		}
		if dispatch == nil {
			finish()
		} else {
			dispatch(finish)
		}
	}
	if dispatch == nil {
		execute()
	} else {
		go execute()
	}
}

func (a *app) close() {
	a.closed = true
	a.stopLibraryScan()
	a.stopDiscovery()
	if a.cancel != nil {
		a.cancel()
	}
	if s := a.browser.session; s != nil {
		// Close waits for an in-flight script, so keep it off the UI thread.
		if a.busy {
			go s.Close()
		} else {
			s.Close()
		}
		a.browser.session = nil
	}
	if a.player != nil {
		a.player.Close()
		a.player = nil
	}
	if a.proxy != nil {
		_ = a.proxy.Close()
		a.proxy = nil
	}
}

func (a *app) loadConfig(raw, line string) {
	a.readConfig(raw, line, false)
}

func (a *app) readConfig(raw, line string, restoring bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		a.status = "请先粘贴配置地址"
		return
	}
	a.run("加载配置", func(ctx context.Context) (jobResult, error) {
		doc, err := config.Read(ctx, raw, nil)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			b := &a.browser
			if b.session != nil {
				b.session.Close()
			}
			*b = browser{configURL: raw, assets: b.assets, doc: doc, line: line}
			b.selectedVideo = -1
			if !restoring && len(doc.Sites) == 0 && len(doc.Lives) > 0 {
				a.live.url, a.live.restore = "", false
			}
			if a.live.url == "" {
				a.setLiveSources(doc.Lives)
			}
			a.resetLibrary(doc)
			if a.persist && line == "" {
				if err := a.saveImportedSource(raw); err != nil {
					a.status += "；保存片源地址失败：" + err.Error()
				}
			}
			if len(doc.Sites) == 0 && len(doc.Lives) > 0 {
				a.showLive()
			}
		}}, nil
	})
}

func (a *app) openSite(site config.Site) {
	a.openSiteInConfig(site, a.browser.doc, a.browser.line)
}

func (a *app) openSiteInConfig(site config.Site, doc *config.Config, line string) {
	if site.Kind.Unsupported() {
		a.status = unsupported(site.Kind)
		return
	}
	assets := strings.TrimSpace(a.browser.assets)
	a.run("打开站点", func(ctx context.Context) (jobResult, error) {
		session, categories, err := source.Open(ctx, site, assets)
		if err != nil {
			return jobResult{}, err
		}
		var first catalog.Page
		var firstErr error
		if len(categories) > 0 {
			first, firstErr = session.Category(ctx, string(categories[0].ID), 1)
		}
		return jobResult{discard: session.Close, apply: func() {
			b := &a.browser
			if b.session != nil {
				b.session.Close()
			}
			b.session, b.categories, b.site = session, categories, site.Name
			b.doc, b.line, b.sidebar = doc, line, "site"
			b.results, b.detail, b.page = first, nil, 0
			b.category, b.query, b.search = "", "", ""
			b.selectedVideo = -1
			if len(categories) > 0 {
				b.category = string(categories[0].ID)
				if firstErr == nil {
					b.page = 1
				}
			}
			if firstErr != nil {
				a.status = "站点已打开，读取首个分类失败：" + firstErr.Error()
			} else {
				a.status = "站点已打开"
			}
		}}, nil
	})
}

func (a *app) loadPage(category, query string, page int) {
	session := a.browser.session
	if session == nil {
		return
	}
	query = strings.TrimSpace(query)
	if category == "" && query == "" {
		a.status = "请输入片名"
		return
	}
	a.run("读取影片", func(ctx context.Context) (jobResult, error) {
		var result catalog.Page
		var err error
		if query != "" {
			result, err = session.Search(ctx, query, page)
		} else {
			result, err = session.Category(ctx, category, page)
		}
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			b := &a.browser
			b.category, b.search, b.page = category, query, page
			b.results, b.detail = result, nil
			b.selectedVideo = -1
			a.status = fmt.Sprintf("第 %d 页 · %d 部影片", page, len(result.Videos))
			if len(result.Videos) == 0 {
				a.status += "，可换分类、搜索或换站点"
			}
		}}, nil
	})
}

func (a *app) loadDetail(video catalog.Video) {
	session := a.browser.session
	if session == nil {
		return
	}
	a.run("读取详情", func(ctx context.Context) (jobResult, error) {
		detail, err := session.Detail(ctx, string(video.ID))
		if err != nil {
			return jobResult{}, err
		}
		if detail.Name == "" {
			detail.Name = video.Name
		}
		return jobResult{apply: func() { a.browser.detail = &detail; a.status = "请选择分集播放" }}, nil
	})
}

func (a *app) playEpisode(source string, episode catalog.Episode) {
	session := a.browser.session
	if session == nil {
		return
	}
	a.run("解析分集", func(ctx context.Context) (jobResult, error) {
		play, err := session.Play(ctx, source, episode)
		if err != nil {
			return jobResult{}, err
		}
		resolver := resolve.Resolver{Sniff: a.sniffPage}
		if a.browser.doc != nil {
			resolver.Parsers = a.browser.doc.Parsers
		}
		play, err = resolver.Resolve(ctx, play)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			a.playURL(play.URL, play.Headers)
			if a.player != nil {
				a.playingSource, a.playingEpisode = source, episode.Name
			}
		}}, nil
	})
}

func unsupported(kind config.Kind) string {
	if kind == config.KindCSP {
		return "此站点依赖专用 Android 爬虫，当前尚未适配；通用 XBPQ 规则已支持"
	}
	return fmt.Sprintf("当前版本暂不支持 %s 站点，请选择其他站点", kind)
}

func (a *app) browserView(c *ui.Context) {
	b := &a.browser
	if b.sidebar == "" {
		b.sidebar = "library"
	}
	ui.Box(c).Fill().LinearGradient(ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: ui.Hex("#0b1530"), At: 0},
		{Color: ui.Hex("#263153"), At: 0.42},
		{Color: ui.Hex("#714258"), At: 0.72},
		{Color: ui.Hex("#221727"), At: 1},
	}}).Children(func() {
		ui.Row(c).Fill().Gap(0).Children(func() {
			ui.Themed(c, ui.LightTheme()).Width(246).FillHeight().Radius(22).
				Material(glass.Glass{Style: glass.Clear, Clarity: 0.5, Tint: ui.RGBA(224, 224, 242, 165)}).
				Children(func() {
					// HiddenInset places macOS window controls over this pane.
					top := max(float32(58), c.TitleBar().Bottom+14)
					ui.Column(c).Fill().Padding(top, 14, 16, 14).Gap(16).Children(func() {
						if ui.SearchField(c, &b.query).Label("片名").Placeholder("搜索整个片库").
							FillWidth().Disabled(a.busy).Submitted() {
							a.searchLibrary()
						}
						if ui.Button(c, "全库搜索").FillWidth().Disabled(a.busy).Clicked() {
							a.searchLibrary()
						}
						a.browserSidebar(c)
						ui.Column(c).Gap(8).Children(func() {
							if ui.Button(c, "添加片源").FillWidth().Radius(20).
								Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(255, 255, 255, 130), Interactive: true}).Clicked() {
								b.importOpen = true
							}
							if ui.Button(c, "直链播放").FillWidth().Radius(20).
								Material(glass.Glass{Style: glass.Clear, Interactive: true}).Clicked() {
								a.direct = true
							}
						})
					})
				})
			ui.Themed(c, ui.DarkTheme()).Grow(1).FillHeight().MinWidth(0).
				LinearGradient(ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
					{Color: ui.Hex("#0b1530"), At: 0},
					{Color: ui.Hex("#222c4e"), At: 0.42},
					{Color: ui.Hex("#6b4057"), At: 0.74},
					{Color: ui.Hex("#241727"), At: 1},
				}}).Children(func() {
				if b.sidebar == "live" {
					a.liveView(c)
				} else {
					a.catalogView(c)
				}
			})
		})
	})
	a.importDialog(c)
}

func (a *app) browserSidebar(c *ui.Context) {
	b := &a.browser
	list := ui.Sidebar(c, &b.sidebar, func() {
		ui.SidebarSection(c, "浏览", nil, func() {
			ui.SidebarItem(c, "library", nil, "首页")
			if a.library.query != "" {
				ui.SidebarItem(c, "search", nil, "搜索结果")
			}
			ui.SidebarItem(c, "live", nil, "直播")
			if a.library.root != nil {
				ui.SidebarItem(c, "sources", nil, "管理片库")
			}
		})
		if b.doc == nil {
			return
		}
	}).Grow(1).FillWidth().Background(ui.Transparent)
	if !list.Changed() || a.busy {
		return
	}
	switch {
	case b.sidebar == "library":
		b.detail = nil
		if a.library.query != "" {
			a.startLibraryScan("", false)
		}
		a.loadDiscoveryTab(false)
	case b.sidebar == "live":
		a.showLive()
	case b.sidebar == "search":
		b.detail = nil
	}
}

func (a *app) importDialog(c *ui.Context) {
	b := &a.browser
	ui.DialogBase(c, &b.importOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(2, 8, 22, 170))
		panel.Width(520).MaxWidth(520).Padding(26).Gap(16).Radius(22).
			Material(glass.Glass{Style: glass.Regular})
		ui.Text(c, "添加片源").FontSize(24).Bold()
		ui.Text(c, "导入后自动汇集影片推荐，可直接搜索整个片库。也支持 M3U 和 TXT 直播列表。").TextColor(c.Theme().TextMuted)
		ui.TextInput(c, &b.configURL).Label("配置地址").Placeholder("https://example.com/config.json").FillWidth().Disabled(a.busy)
		ui.TextInput(c, &b.assets).Label("脚本目录（可选）").Placeholder("本地依赖目录").FillWidth().Disabled(a.busy)
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				b.importOpen = false
			}
			if ui.PrimaryButton(c, "加载配置").Disabled(a.busy).Clicked() {
				if strings.TrimSpace(b.configURL) != "" {
					b.importOpen = false
				}
				a.loadConfig(b.configURL, "")
			}
		})
	})
}

func (a *app) siteCatalogView(c *ui.Context) {
	b := &a.browser
	if b.session == nil {
		a.catalogEmpty(c)
		return
	}
	ui.Column(c).Fill().Gap(0).Children(func() {
		ui.Row(c).FillWidth().Height(78).Padding(0, 34).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "片库").FontSize(17).Bold().Grow(1)
			if b.site != "" {
				ui.Text(c, b.site).SingleLine().TextColor(ui.RGBA(255, 255, 255, 210))
			}
			if ui.Button(c, "搜索").Radius(18).Material(glass.Glass{Style: glass.Clear, Interactive: true}).Disabled(a.busy).Clicked() {
				a.searchLibrary()
			}
		})
		if b.detail != nil {
			a.detailView(c)
			return
		}
		if len(b.results.Videos) > 0 && b.page == 1 {
			a.featuredVideo(c, b.results.Videos[0])
		} else {
			ui.Column(c).FillWidth().Height(170).Padding(26, 42).Gap(8).Justify(ui.Center).Children(func() {
				ui.Text(c, b.site).FontSize(32).Bold()
				ui.Text(c, "选择分类或搜索片名，找到想看的影片。").TextColor(ui.RGBA(255, 255, 255, 205))
			})
		}
		ui.Themed(c, ui.LightTheme()).FillWidth().Grow(1).MinHeight(0).
			Background(ui.RGBA(249, 248, 251, 248)).Children(func() {
			ui.Column(c).Fill().Padding(27, 30).Gap(18).Children(func() {
				ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
					for i, category := range b.categories {
						if ui.Button(c, category.Name).Key(fmt.Sprintf("category-%d", i)).Radius(16).
							Selected(b.search == "" && b.category == string(category.ID)).Disabled(a.busy).Clicked() {
							a.loadPage(string(category.ID), "", 1)
						}
					}
				})
				ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
					title := "最近更新"
					if b.search != "" {
						title = "搜索结果"
					}
					ui.Text(c, title).FontSize(21).Bold().Grow(1)
					if b.page > 0 {
						if ui.Button(c, "上一页").Disabled(a.busy || b.page <= 1).Clicked() {
							a.loadPage(b.category, b.search, b.page-1)
						}
						ui.Text(c, fmt.Sprintf("第 %d 页", b.page)).TextColor(c.Theme().TextMuted)
						if ui.Button(c, "下一页").Disabled(a.busy || !b.results.HasNext).Clicked() {
							a.loadPage(b.category, b.search, b.page+1)
						}
					}
				})
				if len(b.results.Videos) == 0 {
					ui.Column(c).Grow(1).FillWidth().Center().Gap(8).Children(func() {
						ui.Text(c, "这里还没有影片").FontSize(21).Bold()
						ui.Text(c, "可以选择其他分类，或搜索片名。").TextColor(c.Theme().TextMuted)
					})
					return
				}
				b.grid.Selected = &b.selectedVideo
				b.grid.Label = func(i int) string { return videoTitle(b.results.Videos[i]) }
				b.grid.Key = func(i int) any { return string(b.results.Videos[i].ID) }
				if ui.GridView(c, &b.grid, len(b.results.Videos), 148, 280, func(i int) {
					a.videoCard(c, b.results.Videos[i])
				}).Grow(1).FillWidth().Changed() && b.selectedVideo >= 0 && b.selectedVideo < len(b.results.Videos) {
					a.loadDetail(b.results.Videos[b.selectedVideo])
				}
			})
		})
	})
}

func (a *app) catalogEmpty(c *ui.Context) {
	b := &a.browser
	ui.Box(c).Fill().RadialGradient(ui.RadialGradient{
		Center: ui.Point{X: 0.78, Y: 0.56},
		Stops: []ui.GradientStop{
			{Color: ui.RGBA(236, 138, 104, 120), At: 0},
			{Color: ui.RGBA(176, 95, 113, 55), At: 0.45},
			{Color: ui.Transparent, At: 1},
		},
	}).Draw(func(p *ui.Painter, r ui.Rect) {
		// An empty library keeps the reference's sunset and skyline without
		// inventing films or sources. It is painted with MyGo's native painter.
		horizon := r.Y + r.H - 178
		base := horizon - 73
		p.Fill(ui.Rect{X: r.X + r.W*0.72 - 49, Y: base - 92, W: 98, H: 98}, ui.RGBA(255, 224, 167, 220), 49)
		building := ui.Hex("#281d29")
		for _, block := range [][3]float32{
			{0, 0.06, 34}, {0.06, 0.05, 19}, {0.11, 0.08, 57},
			{0.19, 0.07, 25}, {0.26, 0.06, 45}, {0.32, 0.09, 17},
			{0.41, 0.07, 68}, {0.48, 0.08, 26}, {0.56, 0.07, 39},
			{0.63, 0.08, 17}, {0.71, 0.07, 58}, {0.78, 0.08, 29},
			{0.86, 0.07, 49}, {0.93, 0.07, 30},
		} {
			p.Fill(ui.Rect{X: r.X + r.W*block[0], Y: base - block[2], W: r.W*block[1] + 1, H: block[2] + 1}, building, 0)
		}
		p.Fill(ui.Rect{X: r.X, Y: base, W: r.W, H: horizon - base}, ui.RGBA(35, 24, 35, 225), 0)
	}).Children(func() {
		ui.Column(c).Fill().Gap(0).Children(func() {
			ui.Row(c).FillWidth().Height(82).Padding(0, 36).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "片库").FontSize(17).Bold().TextColor(ui.RGB(255, 255, 255))
				ui.Spacer(c)
				if b.doc != nil && ui.Button(c, "添加片源").Radius(20).
					Material(glass.Glass{Style: glass.Clear, Interactive: true}).Clicked() {
					b.importOpen = true
				}
			})
			ui.Column(c).Grow(1).FillWidth().Padding(28, 52).Gap(17).Justify(ui.Center).Children(func() {
				title := "你的片库，从这里开始"
				message := "添加自己的配置，影片就会出现在这里。"
				if b.doc != nil && len(b.doc.Lines) > 0 {
					title, message = "片源已添加", "从左侧选择一条线路，开始浏览。"
				} else if b.doc != nil && len(b.doc.Sites) > 0 {
					title, message = "片源已添加", "从左侧选择一个站点，开始浏览。"
				}
				ui.Text(c, title).FontSize(38).Bold().TextColor(ui.RGB(255, 255, 255)).
					TextShadow(0, 3, 12, ui.RGBA(0, 0, 0, 100))
				ui.Text(c, message).FontSize(16).TextColor(ui.RGBA(255, 255, 255, 220))
				if b.doc == nil && ui.Button(c, "添加片源").Width(184).Padding(12, 24).Radius(24).
					Background(ui.RGB(255, 255, 255)).TextColor(ui.Hex("#17203c")).Clicked() {
					b.importOpen = true
				}
			})
			ui.Themed(c, ui.LightTheme()).FillWidth().Height(178).
				Background(ui.RGBA(248, 247, 250, 245)).Children(func() {
				ui.Column(c).Fill().Padding(27, 44).Gap(10).Children(func() {
					ui.Text(c, "片库").FontSize(21).Bold()
					ui.Text(c, "添加片库后，直接搜索片名，或从首页推荐中选择影片播放。").
						TextColor(c.Theme().TextMuted)
				})
			})
		})
	})
}

func (a *app) featuredVideo(c *ui.Context, video catalog.Video) {
	ui.Row(c).FillWidth().Height(245).Padding(18, 44, 28, 44).Gap(28).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Gap(12).Children(func() {
			ui.Text(c, videoTitle(video)).FontSize(40).Bold().TextColor(ui.RGB(255, 255, 255)).SingleLine()
			if meta := videoMeta(video.Year, video.Type, video.Remarks); meta != "" {
				ui.Text(c, meta).TextColor(ui.RGBA(255, 255, 255, 215)).SingleLine()
			}
			if ui.Button(c, "查看详情").Width(150).Padding(10, 18).Radius(22).
				Background(ui.RGB(255, 255, 255)).TextColor(ui.Hex("#17203c")).Disabled(a.busy).Clicked() {
				a.loadDetail(video)
			}
		})
		ui.Box(c).Size(146, 204).Radius(14).Clip().Gradient(ui.Hex("#534b75"), ui.Hex("#c27b77"), 180).Children(func() {
			if poster := a.poster(video.Pic); poster != nil {
				ui.Image(c, poster).Fill().Fit(ui.Cover)
			}
		})
	})
}

func (a *app) videoCard(c *ui.Context, video catalog.Video) {
	ui.Box(c).Fill().Radius(15).Clip().Gradient(ui.Hex("#202c52"), ui.Hex("#8d5367"), 180).Children(func() {
		if poster := a.poster(video.Pic); poster != nil {
			ui.Image(c, poster).Fill().Fit(ui.Cover)
		}
		ui.Column(c).Absolute().Left(0).Right(0).Bottom(0).Padding(26, 12, 12, 12).Gap(4).
			Gradient(ui.RGBA(10, 13, 24, 0), ui.RGBA(10, 13, 24, 230), 180).Children(func() {
			ui.Text(c, videoTitle(video)).Bold().TextColor(ui.RGB(255, 255, 255)).SingleLine()
			if meta := videoMeta(video.Year, video.Type, video.Remarks); meta != "" {
				ui.Text(c, meta).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 220)).SingleLine()
			}
		})
	})
}

func (a *app) detailView(c *ui.Context) {
	b := &a.browser
	detail := b.detail
	ui.Themed(c, ui.LightTheme()).Grow(1).FillWidth().MinHeight(0).
		Background(ui.RGBA(249, 248, 251, 248)).Children(func() {
		ui.Scroll(c).Fill().Padding(30, 38).Gap(20).Children(func() {
			if ui.Button(c, "返回影片列表").Disabled(a.busy).Clicked() {
				b.detail = nil
				b.selectedVideo = -1
				a.library.selected = -1
				return
			}
			ui.Row(c).FillWidth().Gap(22).Children(func() {
				ui.Box(c).Size(170, 240).Radius(16).Clip().Background(c.Theme().Surface).Children(func() {
					if poster := a.poster(detail.Pic); poster != nil {
						ui.Image(c, poster).Fill().Fit(ui.Cover)
					}
				})
				ui.Column(c).Grow(1).Gap(11).Children(func() {
					ui.Text(c, detail.Name).FontSize(27).Bold()
					if meta := videoMeta(detail.Year, detail.Type, ""); meta != "" {
						ui.Text(c, meta).TextColor(c.Theme().TextMuted)
					}
					if detail.Content != "" {
						ui.Text(c, detail.Content).TextColor(c.Theme().TextMuted)
					}
				})
			})
			ui.Text(c, "选集播放").FontSize(19).Bold()
			if len(detail.Sources) == 0 {
				ui.Text(c, "这个站点没有提供可播放分集。").TextColor(c.Theme().TextMuted)
			}
			for i, source := range detail.Sources {
				name := source.Name
				if name == "" {
					name = fmt.Sprintf("播放线路 %d", i+1)
				}
				ui.Text(c, name).Bold()
				ui.Row(c).Key(fmt.Sprintf("source-%d", i)).FillWidth().Wrap().Gap(8).Children(func() {
					for j, episode := range source.Episodes {
						if ui.Button(c, episodeButtonLabel(episode.Name)).Key(j).Disabled(a.busy).Clicked() {
							a.playEpisode(source.Name, episode)
						}
					}
				})
			}
		})
	})
}

func videoTitle(video catalog.Video) string {
	if video.Name != "" {
		return video.Name
	}
	return string(video.ID)
}

func videoMeta(parts ...string) string {
	var shown []string
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			shown = append(shown, part)
		}
	}
	return strings.Join(shown, " · ")
}
