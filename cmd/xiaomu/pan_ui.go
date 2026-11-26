package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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

func isPanSiteKind(kind config.Kind) bool {
	switch kind {
	case config.KindPanWeb, config.KindPanShare, config.KindAList:
		return true
	default:
		return false
	}
}

// panCredentialsSection renders the compact cloud-drive authorization strip
// embedded inside the VOD configuration card on the Sources page.
func (a *app) panCredentialsSection(c *ui.Context) {
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
	var cleanProviders []string
	for _, prov := range []pan.Provider{pan.ProviderQuark, pan.ProviderUC, pan.ProviderBaidu} {
		if creds.Has(prov) {
			cleanProviders = append(cleanProviders, prov.Label())
		}
	}
	renderBody := func() {
		ui.Row(c).FillWidth().Wrap().Gap(10).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "网盘原画授权").FontSize(13.5).Bold()
				ui.Text(c, "供下方网盘站点播放原画，仅存本机").FontSize(12).TextColor(p.textMuted)
			})
			if len(cleanProviders) > 0 {
				tempDir := creds.EffectiveTempDir()
				scope := fmt.Sprintf("将清空 %s 中的「%s」目录（夸克/UC 会同时清理回收站），不影响其他目录",
					strings.Join(cleanProviders, "、"), tempDir)
				if a.running(jobPan) {
					glassButton(c, "清理中…").Tooltip(scope).Disabled(true)
				} else if a.armedDangerButtonWithTooltip(c, "pan-cleanup-temp", "清理临时文件", "确认清理？", scope) {
					a.cleanupPanTempFiles()
				}
			}
		})
		if a.armed.key == "pan-cleanup-temp" && c.Now().Before(a.armed.until) && len(cleanProviders) > 0 {
			ui.Text(c, fmt.Sprintf("再次点击「确认清理？」将清空 %s 的「%s」目录（夸克/UC 含回收站），按 Esc 可取消。",
				strings.Join(cleanProviders, "、"), creds.EffectiveTempDir())).
				FontSize(12).TextColor(p.warning)
		}
		ui.Row(c).FillWidth().Wrap().Gap(10).Children(func() {
			for _, provider := range panAccountProviders {
				signedIn := creds.Has(provider)
				tile := ui.Row(c).Key(string(provider)).Grow(1).MinWidth(180).Padding(9, 11).Gap(8).AlignItems(ui.Center)
				applyInnerSurface(tile, p, 9).Children(func() {
					dotColor := p.textFaint
					statusColor := p.textFaint
					if signedIn {
						dotColor = p.success
						statusColor = p.textMuted
					}
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Box(c).Size(6, 6).Radius(3).Background(dotColor).PassThrough()
							ui.Text(c, provider.Label()).FontSize(12.5).Bold().SingleLine()
						})
						ui.Text(c, creds.Masked(provider)).FontSize(11).TextColor(statusColor).SingleLine()
					})
					if signedIn {
						if a.armedDangerButtonCompact(c, "pan-logout-"+string(provider), "退出", "确认退出？") {
							a.logoutPanProvider(provider)
						}
					} else {
						if primaryButton(c, "登录").Padding(4, 10).FontSize(12).Clicked() {
							a.beginPanWebLogin(provider, nil)
						}
					}
				})
			}
		})
	}
	if len(panSites) == 0 && !anySignedIn {
		ui.Collapsible(c.Key("pan-accounts-optional"), "网盘原画授权（播放网盘影片时自动按需授权）", &a.panUI.cardExpanded, func() {
			ui.Column(c).FillWidth().Padding(8, 0, 0, 0).Gap(10).Children(renderBody)
		})
		return
	}
	ui.Column(c).FillWidth().Gap(10).Children(renderBody)
}

func (a *app) panSiteEntries() []library.Entry {
	var out []library.Entry
	seen := map[string]bool{}
	for _, doc := range a.library.documents {
		for _, site := range doc.doc.Sites {
			if !isPanSiteKind(site.Kind) {
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
	originTitle := a.title
	session := pan.OpenShareSession(ref)
	a.run(jobTitle, "解析网盘分享", func(ctx context.Context) (jobResult, error) {
		detail, err := session.Detail(ctx, ref.RawURL)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{discard: session.Close, apply: func() {
			if a.title != originTitle {
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
		if provider, needAuth := pan.AuthRequiredProvider(err); needAuth {
			if a.title == originTitle && a.title != nil {
				a.title.failed[itemKey(item)] = err.Error()
			}
			a.clearStatus()
			a.promptPanLogin(provider, err.Error(), func() {
				if a.title != originTitle {
					return
				}
				a.openPanShare(ref)
			})
			return
		}
		if a.title == originTitle && a.title != nil {
			a.title.failed[itemKey(item)] = jobError("解析网盘分享", err)
		}
		a.status = jobError("解析网盘分享", err)
	})
}
