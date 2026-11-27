package main

import (
	_ "embed"
	"strings"

	"github.com/egoist/mygo/ui"
)

// The short color logo is from https://www.themoviedb.org/about/logos-attribution.
//
//go:embed assets/tmdb.svg
var localPosterTMDBLogoSVG []byte

var localPosterTMDBLogo = ui.MustParseSVG(localPosterTMDBLogoSVG)

type localPosterSettingsState struct {
	open      bool
	autoMatch bool
	token     string
	err       string
	matchPath string // video that led to setup, if any
}

func (a *app) openLocalPosterSettings() {
	a.localPoster.settings = localPosterSettingsState{open: true, autoMatch: !a.preferences.DisableAutoLocalPoster}
	if a.localPoster.workerURL == "" {
		a.localPoster.settings.token = a.preferences.TMDBToken
	}
}

func (a *app) openLocalPosterSettingsFor(path string) {
	a.openLocalPosterSettings()
	a.localPoster.settings.matchPath = path
}

func (a *app) localPosterSettingsButton(c *ui.Context) {
	if ghostButton(c.Key("history-local-poster-settings"), "封面设置").Clicked() {
		a.openLocalPosterSettings()
	}
}

func (a *app) localPosterSettingsDialog(c *ui.Context) {
	s := &a.localPoster.settings
	usesWorker := a.localPoster.workerURL != ""
	p := paletteFor(c)
	winW, winH := c.Size()
	keyedDialog(c, "dlg-local-poster-settings", &s.open, func(backdrop, panel ui.Element) {
		backdrop.Background(p.dialogScrim).Transition(transDialogScrim)
		panel.Width(min(520, max(240, winW-32))).Padding(24).Gap(12).Radius(26).TextColor(p.text)
		applyPane(panel, p, p.dialogMat)
		applyDialogPanelPose(c, panel)
		ui.Text(c, "封面设置").FontSize(22).Bold()
		submit := false
		ui.Scroll(c).FillWidth().MaxHeight(max(120, winH-156)).Gap(10).ScrollbarStyle(appScrollbar).Children(func() {
			if usesWorker {
				if ui.Switch(c, &s.autoMatch).Label("自动匹配本地视频封面").Changed() {
					a.ensurePreferences()
					previous := a.preferences.DisableAutoLocalPoster
					a.preferences.DisableAutoLocalPoster = !s.autoMatch
					s.err = ""
					if err := a.savePreferences(); err != nil {
						a.preferences.DisableAutoLocalPoster = previous
						s.autoMatch = !previous
						s.err = "保存失败，请检查应用数据目录权限。"
					} else if !s.autoMatch {
						a.cancelJob(jobLocalPoster)
					}
				}
				ui.Text(c, "关闭自动匹配后，仍可手动搜索封面。").FontSize(13).TextColor(p.textMuted)
				ui.Text(c, "片名、年份和类型会发给小幕封面服务。视频文件和路径留在本机。").FontSize(13).TextColor(p.textMuted)
				if s.err != "" {
					ui.Text(c, s.err).FontSize(13).TextColor(p.warning)
				}
			} else {
				ui.Text(c, "填写个人 TMDB Read Access Token 可启用本地视频在线封面匹配。留空保存可关闭。").FontSize(13).TextColor(p.textMuted)
				ui.Text(c, "Token 仅保存在本机。搜索时发送片名和年份，视频文件和路径留在本机。").FontSize(13).TextColor(p.textMuted)
				submit = ui.TextInput(c, &s.token).Label("TMDB Read Access Token").
					Placeholder("粘贴 TMDB API 读取令牌").Password().MaxLength(4096).FillWidth().AutoFocus().Submitted()
				if s.err != "" {
					ui.Text(c, s.err).FontSize(13).TextColor(p.warning)
				}
			}
			ui.Text(c, "数据来源").FontSize(13).Bold()
			credit := ui.ButtonBase(c.Key("tmdb-credit")).Label("访问 TMDB").Size(104, 18).Cursor(ui.CursorPointer)
			credit.Children(func() {
				ui.Image(c, localPosterTMDBLogo).Fill().Fit(ui.Contain).PassThrough()
			})
			if credit.Clicked() {
				c.OpenURL("https://www.themoviedb.org")
			}
			ui.Text(c, "This product uses the TMDB API but is not endorsed or certified by TMDB.").FontSize(12).TextColor(p.textMuted)
		})
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if usesWorker {
				if primaryButton(c, "关闭").Clicked() {
					s.open = false
				}
				return
			}
			if glassButton(c, "取消").Clicked() {
				s.open = false
			}
			if primaryButton(c, "保存").Clicked() || submit {
				a.ensurePreferences()
				oldToken := a.preferences.TMDBToken
				a.preferences.TMDBToken = strings.TrimSpace(s.token)
				if err := a.savePreferences(); err != nil {
					a.preferences.TMDBToken = oldToken
					s.err = "保存失败，请检查应用数据目录权限。"
					return
				}
				s.open = false
				if a.preferences.TMDBToken != "" && s.matchPath != "" {
					if e, ok := a.history.findLocalByPath(s.matchPath); ok {
						a.openLocalPosterDialog(e)
						if a.localPoster.query != "" {
							a.searchLocalPoster()
						}
					}
				}
			}
		})
	})
	if !s.open {
		*s = localPosterSettingsState{}
	}
}
