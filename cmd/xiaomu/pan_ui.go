package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type panUIState struct {
	creds          pan.Credentials
	path           string
	promptOpen     bool
	promptProvider pan.Provider
	promptReason   string
	onAuthSuccess  func()
	cardExpanded   bool
}

var panAccountProviders = []pan.Provider{
	pan.ProviderQuark,
	pan.ProviderAli,
	pan.ProviderUC,
	pan.ProviderBaidu,
}

func panCredentialsPath() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pan_credentials.json"), nil
}

func (a *app) restorePanCredentials() {
	p := a.panUI.path
	if p == "" {
		var err error
		p, err = panCredentialsPath()
		if err != nil {
			return
		}
		a.panUI.path = p
	}
	creds, err := pan.LoadCredentials(p)
	if err != nil {
		a.status = err.Error()
		return
	}
	a.panUI.creds = creds
	pan.SetDefaultCredentials(creds)
}

func (a *app) persistPanCredentials() error {
	c := a.panUI.creds
	a.panUI.creds = c
	pan.SetDefaultCredentials(c)
	if !a.persist {
		return nil
	}
	p := a.panUI.path
	if p == "" {
		var err error
		p, err = panCredentialsPath()
		if err != nil {
			return err
		}
		a.panUI.path = p
	}
	return pan.SaveCredentials(p, c)
}

func (a *app) logoutPanProvider(p pan.Provider) {
	a.panUI.creds.Clear(p)
	if err := a.persistPanCredentials(); err != nil {
		a.status = "退出登录保存失败：" + err.Error()
		return
	}
	a.setNotice("已退出" + p.Label() + "登录")
}

func (a *app) cleanupPanTempFiles() {
	if !a.panUI.creds.Has(pan.ProviderQuark) && !a.panUI.creds.Has(pan.ProviderUC) && !a.panUI.creds.Has(pan.ProviderBaidu) {
		a.setNotice("尚未登录夸克、UC 或百度网盘")
		return
	}
	a.run(jobPan, "清理网盘临时文件", func(ctx context.Context) (jobResult, error) {
		n, err := pan.CleanupAllTemp(ctx)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if n == 0 {
				a.setNotice("网盘临时目录已经是空的")
			} else {
				a.setNotice(fmt.Sprintf("已清理 %d 个临时转存文件", n))
			}
		}}, nil
	}, func(err error) {
		a.status = jobError("清理临时文件", err)
	})
}

func (a *app) panCredentialsCard(c *ui.Context) {
	creds := a.panUI.creds
	panSites := a.panSiteEntries()
	anySignedIn := false
	for _, provider := range panAccountProviders {
		if creds.Has(provider) {
			anySignedIn = true
			break
		}
	}
	p := paletteFor(c)
	renderBody := func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, "网盘账号").FontSize(17).Bold()
				ui.Text(c, "播放网盘影片时，未登录会自动弹出官方授权页面，完成后仅保存在本机。").
					FontSize(13).TextColor(p.textMuted)
			})
			if creds.Has(pan.ProviderQuark) || creds.Has(pan.ProviderUC) || creds.Has(pan.ProviderBaidu) {
				if glassButton(c, "清理临时文件").Disabled(a.running(jobPan)).Clicked() {
					a.cleanupPanTempFiles()
				}
			}
		})
		ui.Row(c).FillWidth().Wrap().Gap(10).Children(func() {
			for _, provider := range panAccountProviders {
				signedIn := creds.Has(provider)
				ui.Row(c).Key(string(provider)).Padding(8, 12).Gap(10).Radius(16).Background(p.surfaceSubtle).AlignItems(ui.Center).Children(func() {
					ui.Text(c, provider.Label()).Bold().FontSize(13)
					ui.Text(c, creds.Masked(provider)).FontSize(12).TextColor(p.textMuted)
					if signedIn {
						if a.armedDangerButton(c, "pan-logout-"+string(provider), "退出", "确认退出？") {
							a.logoutPanProvider(provider)
						}
					} else {
						if primaryButton(c, "登录").Padding(5, 12).Clicked() {
							a.beginPanWebLogin(provider, nil)
						}
					}
				})
			}
		})
		if len(panSites) > 0 {
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				ui.Text(c, fmt.Sprintf("已识别的网盘站点（%d 个，点击直接浏览）", len(panSites))).FontSize(13).TextColor(p.textMuted)
				ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
					for i, entry := range panSites {
						ui.Row(c).Key(i).Gap(6).AlignItems(ui.Center).Children(func() {
							if glassButton(c, entry.Site.Name).Clicked() {
								a.openSite(entry)
							}
							if d, ok := a.latency.get(entry.Key()); ok {
								ui.Text(c, formatLatency(d)).FontSize(12).TextColor(p.textMuted).ContentTransition(ctNumeric)
							}
						})
					}
				})
			})
		}
	}
	col := ui.Column(c).FillWidth().Padding(18).Gap(12)
	applySurface(col, p, 22)
	if len(panSites) == 0 && !anySignedIn {
		col.Children(func() {
			ui.Collapsible(c.Key("pan-accounts-optional"), "网盘账号（播放网盘影片时自动按需授权）", &a.panUI.cardExpanded, func() {
				ui.Column(c).FillWidth().Padding(8, 0, 0, 0).Gap(12).Children(renderBody)
			})
		})
		return
	}
	col.Children(renderBody)
}

func (a *app) panSiteEntries() []library.Entry {
	var out []library.Entry
	seen := map[string]bool{}
	for _, doc := range a.library.documents {
		for _, site := range doc.doc.Sites {
			switch site.Kind {
			case config.KindPanWeb, config.KindPanShare, config.KindAList:
			default:
				continue
			}
			entry := library.Entry{Site: site, Config: doc.doc, Line: doc.line}
			key := entry.Key()
			if seen[key] {
				continue
			}
			if !a.showFailedSites && a.isSiteFailed(key) {
				continue
			}
			seen[key] = true
			out = append(out, entry)
		}
	}
	return out
}

func (a *app) panAuthPromptDialog(c *ui.Context) {
	pal := paletteFor(c)
	ui.DialogBase(c, &a.panUI.promptOpen, func(backdrop, panel ui.Element) {
		p := a.panUI.promptProvider
		backdrop.Background(pal.dialogScrim).Transition(transDialogScrim)
		panel.Width(520).MaxWidth(520).Padding(26).Gap(14).Radius(26).TextColor(pal.text).Transition(transDialogPanel)
		applyPane(panel, pal, pal.dialogMat)
		ui.Text(c, "需要登录"+p.Label()).FontSize(22).Bold()
		if a.panUI.promptReason != "" {
			ui.Text(c, a.panUI.promptReason).TextColor(pal.textMuted)
		}
		ui.Text(c, "将打开"+p.Label()+"官方登录页面。登录完成后会自动保存授权到本机并继续播放。").
			FontSize(13).TextColor(pal.textMuted)
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if glassButton(c, "暂不登录").Clicked() {
				a.panUI.promptOpen = false
			}
			if primaryButton(c, "打开"+p.Label()+"官方登录页").Clicked() {
				cb := a.panUI.onAuthSuccess
				a.beginPanWebLogin(p, cb)
			}
		})
	})
}

// openPanShare opens a pasted cloud-drive share link inside Xiaomu's standard
// detail page so the user can choose episodes, quality and danmaku.
func (a *app) openPanShare(ref pan.ShareRef) {
	a.linkOpen = false
	a.clearTitle()
	syntheticCfg := &config.Config{URL: "pan://" + string(ref.Provider)}
	entry := library.Entry{
		Site: config.Site{
			Key:  "pan-share-" + string(ref.Provider),
			Name: ref.Provider.Label() + "分享",
			Kind: config.KindPanShare,
		},
		Config: syntheticCfg,
	}
	item := library.Item{
		Entry: entry,
		Video: catalog.Video{
			ID:   catalog.ID(ref.RawURL),
			Name: ref.Provider.Label() + "分享",
		},
	}
	a.title = &titlePage{
		name:   item.Video.Name,
		seeds:  []library.Item{item},
		auto:   false,
		failed: map[string]string{},
	}
	a.push(routeDetail)
	session := pan.OpenShareSession(ref)
	a.run(jobTitle, "解析网盘分享", func(ctx context.Context) (jobResult, error) {
		detail, err := session.Detail(ctx, ref.RawURL)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{discard: session.Close, apply: func() {
			if a.title == nil {
				session.Close()
				return
			}
			a.closeSession(a.title.session)
			a.title.session = session
			a.title.detail = &detail
			a.title.item = item
			if detail.Name != "" {
				a.title.name = detail.Name
			}
			a.title.line, a.title.chunk = 0, 0
		}}, nil
	}, func(err error) {
		if a.title != nil {
			a.title.failed[itemKey(item)] = jobError("解析网盘分享", err)
		}
		a.status = jobError("解析网盘分享", err)
	})
}
