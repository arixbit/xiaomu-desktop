package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

type danmakuPreferences struct {
	Enabled         bool    `json:"enabled"`
	API             string  `json:"api,omitempty"`
	Size            float64 `json:"size"`
	Opacity         float64 `json:"opacity"`
	Speed           float64 `json:"speed"`
	Area            float64 `json:"area"`
	Density         string  `json:"density,omitempty"`
	HideTop         bool    `json:"hide_top,omitempty"`
	HideBottom      bool    `json:"hide_bottom,omitempty"`
	HideColor       bool    `json:"hide_color,omitempty"`
	MergeRepeat     bool    `json:"merge_repeat"`
	ProtectSubtitle bool    `json:"protect_subtitle,omitempty"`
	Blocked         string  `json:"blocked,omitempty"`
}

func defaultDanmakuPreferences() danmakuPreferences {
	return danmakuPreferences{Size: 22, Opacity: 0.85, Speed: 0.5, Area: 0.5, MergeRepeat: true}
}

func (p danmakuPreferences) normalized() danmakuPreferences {
	p.API = strings.TrimSpace(p.API)
	if math.IsNaN(p.Size) || p.Size < 14 || p.Size > 36 {
		p.Size = 22
	}
	if math.IsNaN(p.Opacity) || p.Opacity < 0.1 || p.Opacity > 1 {
		p.Opacity = 0.85
	}
	if math.IsNaN(p.Speed) || p.Speed < 0.25 || p.Speed > 1.5 {
		p.Speed = 0.5
	}
	if math.IsNaN(p.Area) || p.Area < 0.25 || p.Area > 1 {
		p.Area = 0.5
	}
	switch p.Density {
	case "", "low", "high":
	default:
		p.Density = ""
	}
	p.Blocked = strings.TrimSpace(p.Blocked)
	return p
}

func danmakuDensityParams(density string) (maxRows int, minGap float32) {
	switch density {
	case "low":
		return 4, 48
	case "high":
		return 18, 8
	default:
		return 12, 12
	}
}

type danmakuState struct {
	open, advanced          bool
	address, query, title   string
	sourceURL               string
	episodeQuery            string
	message, preferredAnime string
	offset                  time.Duration
	episodes                []danmaku.Episode
	selected                *danmaku.Episode
	comments                []danmaku.Comment
	profile                 []float32
	profileDuration         time.Duration
	list                    ui.ListState
	screen                  danmaku.Screen
}

func (a *app) cancelDanmakuJob() {
	a.cancelJob(jobDanmaku)
}

func (a *app) stopDanmaku() {
	a.cancelDanmakuJob()
	s := &a.danmaku
	s.open, s.advanced, s.message, s.sourceURL, s.offset = false, false, "", "", 0
	s.episodes, s.selected, s.comments = nil, nil, nil
	s.profile, s.profileDuration = nil, 0
	s.screen.Reset(nil)
}

func (a *app) refreshDanmakuTrack() {
	s := &a.danmaku
	p := a.preferences.Danmaku.normalized()
	filtered := danmaku.Filter(s.comments, danmaku.FilterOptions{
		HideTop:     p.HideTop,
		HideBottom:  p.HideBottom,
		HideColor:   p.HideColor,
		MergeRepeat: p.MergeRepeat,
		Blocked:     danmaku.ParseBlockedTerms(p.Blocked),
	})
	s.screen.Reset(filtered)
	maxRows, minGap := danmakuDensityParams(p.Density)
	s.screen.SetDensity(maxRows, minGap)
}

func (a *app) toggleDanmaku() {
	if !a.hasEpisodes() {
		return
	}
	a.ensurePreferences()
	a.preferences.Danmaku.Enabled = !a.preferences.Danmaku.Enabled
	a.applyDanmakuEnabled()
	a.savePreferences()
}

func (a *app) applyDanmakuEnabled() {
	s := &a.danmaku
	if !a.preferences.Danmaku.Enabled {
		a.cancelDanmakuJob()
		s.message = "弹幕已关闭"
		return
	}
	if len(s.comments) == 0 {
		if s.title == "" && a.title != nil {
			s.title, s.query = a.title.name, a.title.name
			if number := danmakuNumber(a.playingEpisode); number > 0 {
				s.episodeQuery = strconv.Itoa(number)
			}
		}
		a.loadCurrentDanmaku()
	}
}

// Each episode gets its own cancellable job; the existing job generation
// rejects results from an older player, a disabled overlay or a closed window.
func (a *app) startDanmaku() {
	if !a.hasEpisodes() {
		return
	}
	s := &a.danmaku
	if s.title != a.title.name {
		s.title, s.query, s.preferredAnime = a.title.name, a.title.name, ""
	}
	s.episodeQuery = ""
	if number := danmakuNumber(a.playingEpisode); number > 0 {
		s.episodeQuery = strconv.Itoa(number)
	}
	if a.preferences.Danmaku.Enabled {
		a.loadCurrentDanmaku()
	}
}

func (a *app) loadCurrentDanmaku() {
	s := &a.danmaku
	if a.preferences.Danmaku.API != "" || s.sourceURL == "" {
		a.searchDanmaku(true)
		return
	}
	address := s.sourceURL
	s.message = "正在加载片源弹幕…"
	a.run(jobDanmaku, "", func(ctx context.Context) (jobResult, error) {
		comments, err := danmaku.Load(ctx, address)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			s.episodes, s.list = nil, ui.ListState{}
			s.selected, s.comments = nil, comments
			s.profile, s.profileDuration = nil, 0
			a.refreshDanmakuTrack()
			s.message = fmt.Sprintf("已加载 %d 条片源弹幕", len(comments))
			if len(comments) == 0 {
				s.message = "这一集暂无弹幕"
			}
		}}, nil
	}, a.failDanmaku)
}

func (a *app) danmakuClient() (*danmaku.Client, error) {
	if a.newDanmakuClient != nil {
		return a.newDanmakuClient(a.preferences.Danmaku.API)
	}
	return danmaku.NewClient(a.preferences.Danmaku.API)
}

func (a *app) searchDanmaku(automatic bool) {
	if !a.hasEpisodes() {
		return
	}
	s := &a.danmaku
	a.cancelDanmakuJob()
	s.episodes, s.list = nil, ui.ListState{}
	query := strings.TrimSpace(s.query)
	if query == "" {
		s.message = "请输入要查找弹幕的片名"
		return
	}
	episode := strings.TrimSpace(s.episodeQuery)
	number := 0
	if episode != "" {
		var err error
		number, err = strconv.Atoi(episode)
		if err != nil || number <= 0 {
			s.message = "集数请输入正整数，留空可查看全部分集"
			return
		}
		episode = strconv.Itoa(number)
	}
	client, err := a.danmakuClient()
	if err != nil {
		s.message = err.Error()
		return
	}
	_, index, ok := a.currentEpisode()
	if !ok {
		index = -1
	}
	name, preferred := a.playingEpisode, s.preferredAnime
	if episode != "" {
		name = episode
	}
	s.message = "正在匹配弹幕…"
	a.run(jobDanmaku, "", func(ctx context.Context) (jobResult, error) {
		episodes, err := client.Search(ctx, query, episode)
		if err != nil {
			return jobResult{}, err
		}
		if episode != "" {
			// Compatible services may ignore the episode query parameter.
			episodes = slices.DeleteFunc(episodes, func(candidate danmaku.Episode) bool {
				return danmakuNumber(candidate.Name) != number
			})
		}
		selected, matched := matchDanmakuEpisode(episodes, query, name, index, preferred)
		var comments []danmaku.Comment
		if automatic && matched {
			comments, err = client.Comments(ctx, selected.ID)
			if err != nil {
				return jobResult{}, err
			}
		}
		return jobResult{apply: func() {
			s.episodes, s.list = episodes, ui.ListState{}
			s.message = "请选择对应的影片和分集"
			if len(episodes) == 0 {
				s.message = "没有找到弹幕，试试修改片名"
				if episode != "" {
					s.message = fmt.Sprintf("没有找到第 %d 集的弹幕，试试修改片名或集数", number)
				}
			}
			if automatic && matched {
				a.applyDanmaku(selected, comments)
			}
		}}, nil
	}, a.failDanmaku)
}

func (a *app) loadDanmakuEpisode(episode danmaku.Episode) {
	client, err := a.danmakuClient()
	if err != nil {
		a.danmaku.message = err.Error()
		return
	}
	a.danmaku.message = "正在加载弹幕…"
	a.run(jobDanmaku, "", func(ctx context.Context) (jobResult, error) {
		comments, err := client.Comments(ctx, episode.ID)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if !a.preferences.Danmaku.Enabled {
				a.preferences.Danmaku.Enabled = true
				a.savePreferences()
			}
			a.applyDanmaku(episode, comments)
		}}, nil
	}, a.failDanmaku)
}

func (a *app) failDanmaku(err error) {
	switch {
	case errors.Is(err, context.Canceled):
		a.danmaku.message = "已取消弹幕请求"
	case errors.Is(err, context.DeadlineExceeded):
		a.danmaku.message = "弹幕请求超时，请重试或更换弹幕服务"
	default:
		a.danmaku.message = "获取弹幕失败：" + err.Error()
	}
}

func (a *app) applyDanmaku(episode danmaku.Episode, comments []danmaku.Comment) {
	s := &a.danmaku
	s.selected, s.comments, s.preferredAnime = &episode, comments, episode.AnimeID
	s.profile, s.profileDuration = nil, 0
	a.refreshDanmakuTrack()
	s.message = fmt.Sprintf("已加载 %d 条弹幕 · %s · %s", len(comments), episode.Anime, episode.Name)
	if len(comments) == 0 {
		s.message = "这一集暂无弹幕"
	}
}

func (a *app) danmakuButton(c *ui.Context) {
	if !a.hasEpisodes() {
		return
	}
	label := "弹幕"
	if a.preferences.Danmaku.Enabled && len(a.danmaku.comments) > 0 {
		label = "弹幕 · 开"
	}
	if ui.Button(c, label).Label("弹幕").Radius(16).Material(glass.Glass{Style: glass.Clear, Interactive: true}).TextColor(playerText).Clicked() {
		a.ensurePreferences()
		s := &a.danmaku
		s.open, s.address = true, a.preferences.Danmaku.API
		if s.title == "" {
			s.title = a.title.name
			if number := danmakuNumber(a.playingEpisode); number > 0 {
				s.episodeQuery = strconv.Itoa(number)
			}
		}
		if s.query == "" {
			s.query = a.title.name
		}
	}
}

var danmakuScrollbar = ui.ScrollbarStyle{
	TrackWidth: 10,
	ThumbWidth: 5,
	Radius:     3,
	MinLength:  24,
	Thumb:      ui.RGBA(255, 255, 255, 0.28),
	Hover:      ui.RGBA(255, 255, 255, 0.48),
	Active:     ui.RGBA(255, 255, 255, 0.68),
}

func danmakuSection(c *ui.Context, body func()) {
	ui.Column(c).FillWidth().Padding(10, 12).Gap(8).Radius(16).
		Background(ui.RGBA(255, 255, 255, 0.06)).Children(body)
}

func danmakuEpisodeButton(c *ui.Context, label string, selected bool) ui.Element {
	tint := ui.RGBA(255, 255, 255, 0.09)
	if selected {
		tint = ui.RGBA(255, 90, 64, 0.78)
	}
	return ui.Button(c, label).FillWidth().Padding(9, 12).Radius(12).
		Material(glass.Glass{Style: glass.Clear, Tint: tint, Interactive: true}).TextColor(playerText)
}

func (a *app) danmakuDialog(c *ui.Context) {
	s := &a.danmaku
	if !s.open {
		return
	}
	ui.Themed(c, ui.DarkTheme()).Absolute().Left(0).Right(0).Top(0).Bottom(0).
		Background(ui.Transparent).PassThrough().Children(func() {
		ui.DialogBase(c, &s.open, func(backdrop, panel ui.Element) {
			backdrop.Background(ui.RGBA(2, 8, 22, 0.67))
			_, height := c.Size()
			panel.Width(548).MaxWidth(548).Height(min(float32(720), height-32)).
				Padding(18, 8, 16, 20).Gap(10).Radius(24).
				Material(glass.Glass{Style: glass.Regular, Tint: ui.RGBA(18, 22, 38, 0.84)}).
				TextColor(playerText)
			ui.Row(c).FillWidth().Padding(0, 16, 0, 0).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "弹幕设置").FontSize(20).Bold().Grow(1)
				ui.Text(c, "按 D 键可快捷开关").FontSize(12).TextColor(textMuted)
			})
			ui.Scroll(c).Label("弹幕设置内容").FillWidth().Grow(1).MinHeight(0).
				Padding(4, 16, 4, 0).Gap(10).
				ScrollbarStyle(danmakuScrollbar).ScrollbarInsets(4, 0, 4, 0).
				Children(func() {
					danmakuSection(c, func() {
						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "显示弹幕").Grow(1)
							if ui.Switch(c, &a.preferences.Danmaku.Enabled).Label("弹幕开关").Changed() {
								a.applyDanmakuEnabled()
								a.savePreferences()
							}
						})
						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "字号").Width(64)
							if ui.Slider(c, &a.preferences.Danmaku.Size, 14, 36).Label("弹幕字号").Grow(1).Changed() {
								a.savePreferences()
							}
							ui.Text(c, strconv.Itoa(int(math.Round(a.preferences.Danmaku.Size)))).Width(46).TextAlign(ui.End).TextColor(textMuted)
						})
						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "不透明度").Width(64)
							if ui.Slider(c, &a.preferences.Danmaku.Opacity, 0.1, 1).Label("弹幕不透明度").Grow(1).Changed() {
								a.savePreferences()
							}
							ui.Text(c, fmt.Sprintf("%.0f%%", a.preferences.Danmaku.Opacity*100)).Width(46).TextAlign(ui.End).TextColor(textMuted)
						})
						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "滚动速度").Width(64)
							if ui.Slider(c, &a.preferences.Danmaku.Speed, 0.25, 1.5).Label("弹幕滚动速度").Grow(1).Changed() {
								a.savePreferences()
							}
							ui.Text(c, fmt.Sprintf("%.2f×", a.preferences.Danmaku.Speed)).Width(46).TextAlign(ui.End).TextColor(textMuted)
						})
						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "显示区域").Width(64)
							if ui.Slider(c, &a.preferences.Danmaku.Area, 0.25, 1).Label("弹幕显示区域").Grow(1).Changed() {
								a.savePreferences()
							}
							ui.Text(c, fmt.Sprintf("%.0f%%", a.preferences.Danmaku.Area*100)).Width(46).TextAlign(ui.End).TextColor(textMuted)
						})
						a.danmakuOffsetControls(c)
					})
					danmakuSection(c, func() {
						a.danmakuSearchControls(c)
						if s.message != "" {
							ui.Text(c, s.message).FontSize(12).TextColor(textMuted)
						}
						a.danmakuEpisodeResults(c, height)
						if glassButton(c, "服务设置（可选）").FillWidth().Clicked() {
							s.advanced = !s.advanced
						}
						if s.advanced {
							a.manualDanmaku(c)
						}
					})
					danmakuSection(c, func() {
						a.danmakuFilterControls(c)
					})
				})
			ui.Row(c).FillWidth().Padding(0, 16, 0, 0).Justify(ui.End).Children(func() {
				if glassButton(c, "完成").Clicked() {
					s.open = false
				}
			})
		})
	})
}

func (a *app) danmakuEpisodeResults(c *ui.Context, windowHeight float32) {
	s := &a.danmaku
	if len(s.episodes) == 0 {
		return
	}
	// Render short result lists inline so they never show a nested scrollbar or blank space.
	if len(s.episodes) <= 4 {
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			for _, episode := range s.episodes {
				selected := s.selected != nil && s.selected.ID == episode.ID
				if danmakuEpisodeButton(c, episode.Anime+" · "+episode.Name, selected).Key(episode.ID).Clicked() {
					a.loadDanmakuEpisode(episode)
				}
			}
		})
		return
	}
	listHeight := min(float32(164), max(float32(82), windowHeight-480))
	ui.Box(c).FillWidth().Padding(6, 4, 4, 6).Radius(14).Background(ui.RGBA(0, 0, 0, 0.22)).Children(func() {
		ui.List(c, &s.list, len(s.episodes), func(i int) {
			episode := s.episodes[i]
			selected := s.selected != nil && s.selected.ID == episode.ID
			ui.Box(c).FillWidth().Padding(0, 14, 6, 0).Children(func() {
				if danmakuEpisodeButton(c, episode.Anime+" · "+episode.Name, selected).Key(episode.ID).Clicked() {
					a.loadDanmakuEpisode(episode)
				}
			})
		}).FillWidth().Height(listHeight).ScrollbarStyle(danmakuScrollbar).ScrollbarInsets(2, 0, 2, 0)
	})
}

const maxDanmakuOffset = 10 * time.Minute

func clampDanmakuOffset(d time.Duration) time.Duration {
	return min(max(d, -maxDanmakuOffset), maxDanmakuOffset)
}

func formatDanmakuOffset(d time.Duration) string {
	seconds := int(d / time.Second)
	switch {
	case seconds > 0:
		return fmt.Sprintf("提前 %ds", seconds)
	case seconds < 0:
		return fmt.Sprintf("推迟 %ds", -seconds)
	default:
		return "0s"
	}
}

func (a *app) danmakuOffsetControls(c *ui.Context) {
	s := &a.danmaku
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "时间偏移").Width(64)
		for _, step := range []struct {
			label string
			delta time.Duration
		}{
			{"-5s", -5 * time.Second},
			{"-1s", -time.Second},
			{"+1s", time.Second},
			{"+5s", 5 * time.Second},
		} {
			if glassButton(c, step.label).Label("弹幕时间偏移 " + step.label).Padding(5, 10).Clicked() {
				s.offset = clampDanmakuOffset(s.offset + step.delta)
			}
		}
		ui.Text(c, formatDanmakuOffset(s.offset)).FontSize(13).TextColor(textMuted).TextAlign(ui.End).Grow(1)
		if glassButton(c, "重置").Label("重置弹幕时间偏移").Padding(5, 10).Disabled(s.offset == 0).Clicked() {
			s.offset = 0
		}
	})
}

func danmakuFilterChip(c *ui.Context, label string, active bool) ui.Element {
	tint := ui.RGBA(255, 255, 255, 0.10)
	if active {
		tint = ui.RGBA(255, 90, 64, 0.78)
	}
	return ui.Button(c, label).Label(label).Padding(6, 10).Radius(14).
		Material(glass.Glass{Style: glass.Clear, Tint: tint, Interactive: true}).TextColor(playerText)
}

func (a *app) danmakuFilterControls(c *ui.Context) {
	p := &a.preferences.Danmaku
	changed := false
	densityIndex := 1
	switch p.Density {
	case "low":
		densityIndex = 0
	case "high":
		densityIndex = 2
	}
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "密度").Width(64)
		ui.Box(c).Grow(1).Children(func() {
			if ui.Segmented(c, &densityIndex, "稀疏", "适中", "密集").FillEqually().Label("弹幕密度").Changed() {
				switch densityIndex {
				case 0:
					p.Density = "low"
				case 2:
					p.Density = "high"
				default:
					p.Density = ""
				}
				changed = true
			}
		})
	})
	ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
		for _, toggle := range []struct {
			label string
			value *bool
		}{
			{"屏蔽顶部", &p.HideTop},
			{"屏蔽底部", &p.HideBottom},
			{"屏蔽彩色", &p.HideColor},
			{"合并刷屏", &p.MergeRepeat},
			{"防挡字幕", &p.ProtectSubtitle},
		} {
			if danmakuFilterChip(c, toggle.label, *toggle.value).Clicked() {
				*toggle.value = !*toggle.value
				changed = true
			}
		}
	})
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "屏蔽词").Width(64)
		if ui.TextInput(c, &p.Blocked).Label("弹幕屏蔽词").Placeholder("逗号或空格分隔，如 剧透,广告").Grow(1).Changed() {
			changed = true
		}
	})
	if changed {
		a.refreshDanmakuTrack()
		a.savePreferences()
	}
}

func (a *app) manualDanmaku(c *ui.Context) {
	s := &a.danmaku
	ui.Text(c, "留空使用内置公共弹幕服务，也可填写兼容弹弹play的服务地址。").FontSize(12)
	ui.TextInput(c, &s.address).Label("弹幕服务地址").Placeholder("留空使用内置公共服务").FillWidth()
	if primaryButton(c, "保存并匹配").Clicked() {
		address := strings.TrimSpace(s.address)
		_, err := danmaku.NewClient(address)
		if address != "" && err != nil {
			s.message = err.Error()
		} else {
			a.cancelDanmakuJob()
			s.episodes, s.selected, s.comments = nil, nil, nil
			s.profile, s.profileDuration = nil, 0
			s.screen.Reset(nil)
			a.preferences.Danmaku.API = address
			a.preferences.Danmaku.Enabled = true
			s.preferredAnime = ""
			a.savePreferences()
			a.loadCurrentDanmaku()
		}
	}
}

func (a *app) danmakuSearchControls(c *ui.Context) {
	s := &a.danmaku
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		titleChanged := ui.TextInput(c, &s.query).Label("弹幕片名").Grow(1).Changed()
		ui.Text(c, "集数").FontSize(13)
		episodeChanged := ui.TextInput(c, &s.episodeQuery).Label("弹幕集数").Placeholder("可留空").Width(80).Changed()
		if titleChanged || episodeChanged {
			a.cancelDanmakuJob()
			s.episodes, s.message = nil, ""
			if titleChanged {
				s.preferredAnime = ""
			}
		}
		if glassButton(c, "搜索弹幕").Disabled(a.running(jobDanmaku)).Clicked() {
			a.searchDanmaku(false)
		}
	})
}

func (a *app) danmakuHighlightCurve(c *ui.Context, player *ui.VideoPlayer, duration time.Duration) {
	if !a.hasEpisodes() || !a.preferences.Danmaku.Enabled || len(a.danmaku.comments) == 0 || duration <= 0 {
		return
	}
	s := &a.danmaku
	if s.profile == nil || s.profileDuration != duration {
		s.profile = danmaku.DensityProfile(s.comments, duration, 80)
		s.profileDuration = duration
	}
	if len(s.profile) < 2 {
		return
	}
	profile := s.profile
	ratio := float32(min(max(player.Position().Seconds()/duration.Seconds(), 0), 1))
	ui.Box(c).FillWidth().Height(18).PassThrough().Label("弹幕高能曲线").Draw(func(p *ui.Painter, r ui.Rect) {
		if r.W <= 4 || r.H <= 2 {
			return
		}
		n := len(profile)
		points := make([][2]float32, n)
		for i, v := range profile {
			x := r.X + r.W*float32(i)/float32(n-1)
			y := r.Y + r.H - max(float32(1.5), v*(r.H-2))
			points[i] = [2]float32{x, y}
		}
		var path ui.Path
		path.MoveTo(r.X, r.Y+r.H)
		path.LineTo(points[0][0], points[0][1])
		for i := 1; i < n; i++ {
			midX := (points[i-1][0] + points[i][0]) / 2
			midY := (points[i-1][1] + points[i][1]) / 2
			path.QuadTo(points[i-1][0], points[i-1][1], midX, midY)
		}
		path.LineTo(points[n-1][0], points[n-1][1])
		path.LineTo(r.X+r.W, r.Y+r.H)
		path.Close()
		p.FillPathGradient(&path, ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
			{Color: ui.RGBA(255, 178, 130, 0.36), At: 0},
			{Color: ui.RGBA(255, 178, 130, 0.04), At: 1},
		}})
		p.StrokePolyline(points, 1.1, ui.RGBA(255, 200, 165, 0.62))
		if ratio > 0 {
			p.Clip(ui.Rect{X: r.X, Y: r.Y, W: r.W * ratio, H: r.H}, 0, func() {
				p.FillPathGradient(&path, ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
					{Color: ui.RGBA(255, 115, 78, 0.48), At: 0},
					{Color: ui.RGBA(255, 115, 78, 0.08), At: 1},
				}})
				p.StrokePolyline(points, 1.3, ui.RGBA(255, 145, 110, 0.9))
			})
		}
	})
}

func danmakuArea(picture, bounds ui.Rect, fraction float32, avoidControls, protectSubtitle bool) ui.Rect {
	top, limit := picture.Y+12, picture.Y+picture.H-12
	if avoidControls {
		top = max(top, bounds.Y+72)
		limit = min(limit, bounds.Y+bounds.H-132)
	}
	if protectSubtitle {
		subtitleBand := max(float32(48), picture.H*0.14)
		limit = min(limit, picture.Y+picture.H-subtitleBand)
	}
	usable := max(float32(0), limit-top)
	return ui.Rect{X: picture.X, Y: top, W: picture.W, H: usable * fraction}
}

func (a *app) danmakuOverlay(c *ui.Context, player *ui.VideoPlayer, avoidControls bool) {
	if !a.preferences.Danmaku.Enabled || len(a.danmaku.comments) == 0 || !a.hasEpisodes() {
		return
	}
	ui.Box(c).Absolute().Left(0).Right(0).Top(0).Bottom(0).PassThrough().Label("弹幕画面").Draw(func(p *ui.Painter, r ui.Rect) {
		if player != a.player || !player.Ready() || player.Err() != nil {
			return
		}
		w, h := player.Size()
		picture := playerPictureRect(r, w, h)
		settings := a.preferences.Danmaku.normalized()
		area := danmakuArea(picture, r, float32(settings.Area), avoidControls, settings.ProtectSubtitle)
		size := float32(settings.Size)
		if area.W <= 0 || area.H < size*1.4 {
			return
		}
		position := player.Position() + a.danmaku.offset
		sprites := a.danmaku.screen.Frame(position, area.W, area.H, size, float32(settings.Speed), func(text string) float32 {
			width, _ := p.MeasureText(0, ui.Span{Text: text, Size: size})
			return width
		})
		outline := ui.RGBA(0, 0, 0, float32(settings.Opacity)*0.85)
		p.Clip(area, 0, func() {
			for _, sprite := range sprites {
				color := ui.RGBA(uint8(sprite.Color>>16), uint8(sprite.Color>>8), uint8(sprite.Color), float32(settings.Opacity))
				x, y := area.X+sprite.X, area.Y+sprite.Y
				p.Text(x-1, y, sprite.Text, size, outline)
				p.Text(x+1, y, sprite.Text, size, outline)
				p.Text(x, y-1, sprite.Text, size, outline)
				p.Text(x, y+1, sprite.Text, size, outline)
				p.Text(x, y, sprite.Text, size, color)
			}
		})
		if player.Playing() && !player.Buffering() {
			if len(sprites) > 0 {
				p.AnimationFrameRate(60)
			} else if next, ok := a.danmaku.screen.Next(); ok && next > position {
				rate := max(player.Rate(), 0.1)
				p.After(min(time.Second, time.Duration(float64(next-position)/rate)))
			}
		}
	})
}

var danmakuEpisodeNumber = regexp.MustCompile(`(?i)^(?:第\s*|ep(?:isode)?\s*|e\s*)?0*([0-9]+)(?:\s*[集话話期]|\s*[:：._-]|\s|$)`)
var danmakuNamedEpisode = regexp.MustCompile(`第\s*0*([0-9]+)\s*[集话話期]`)
var danmakuTaggedEpisode = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s[0-9]+[\s._-]*)?(?:ep(?:isode)?[\s._-]*|e\s*)0*([0-9]+)(?:[^a-z0-9]|$)`)
var danmakuYearSuffix = regexp.MustCompile(`\s*[（(][0-9]{4}[)）]\s*$`)
var danmakuProviderSuffix = regexp.MustCompile(`(?i)\s*【[^】]+】\s*from\s+[a-z0-9_-]+\s*$`)

func danmakuNumber(name string) int {
	parts := danmakuNamedEpisode.FindStringSubmatch(name)
	if len(parts) < 2 {
		parts = danmakuTaggedEpisode.FindStringSubmatch(name)
	}
	if len(parts) < 2 {
		parts = danmakuEpisodeNumber.FindStringSubmatch(strings.TrimSpace(name))
	}
	if len(parts) < 2 {
		return 0
	}
	number, _ := strconv.Atoi(parts[1])
	return number
}

// A title and episode must match before automatic loading. When a compatible
// service lists the same edition on multiple platforms, use its first result.
// A manual selection remembers its series ID for subsequent episodes.
func matchDanmakuEpisode(episodes []danmaku.Episode, title, name string, index int, preferred string) (danmaku.Episode, bool) {
	key := titleKey(danmakuYearSuffix.ReplaceAllString(title, ""))
	number := danmakuNumber(name)
	var match danmaku.Episode
	var matchSeries string
	count := 0
	for _, episode := range episodes {
		series := danmakuProviderSuffix.ReplaceAllString(episode.Anime, "")
		if preferred != "" {
			if episode.AnimeID != preferred {
				continue
			}
		} else if titleKey(danmakuYearSuffix.ReplaceAllString(series, "")) != key {
			continue
		}
		candidateNumber := danmakuNumber(episode.Name)
		same := strings.TrimSpace(episode.Name) == strings.TrimSpace(name)
		if number > 0 {
			same = candidateNumber == number
		} else if !same && index >= 0 && candidateNumber > 0 {
			same = candidateNumber == index+1
		}
		if same {
			if count == 1 && preferred == "" && episode.Anime != match.Anime && titleKey(series) == matchSeries &&
				danmakuProviderSuffix.MatchString(episode.Anime) && danmakuProviderSuffix.MatchString(match.Anime) {
				continue
			}
			match, matchSeries = episode, titleKey(series)
			count++
		}
	}
	return match, count == 1
}
