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
	Enabled bool    `json:"enabled"`
	API     string  `json:"api,omitempty"`
	Size    float64 `json:"size"`
	Opacity float64 `json:"opacity"`
	Speed   float64 `json:"speed"`
	Area    float64 `json:"area"`
}

func defaultDanmakuPreferences() danmakuPreferences {
	return danmakuPreferences{Size: 22, Opacity: 0.85, Speed: 0.5, Area: 0.5}
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
	return p
}

type danmakuState struct {
	open, advanced          bool
	address, query, title   string
	sourceURL               string
	episodeQuery            string
	message, preferredAnime string
	episodes                []danmaku.Episode
	selected                *danmaku.Episode
	comments                []danmaku.Comment
	list                    ui.ListState
	screen                  danmaku.Screen
}

func (a *app) cancelDanmakuJob() {
	a.cancelJob(jobDanmaku)
}

func (a *app) stopDanmaku() {
	a.cancelDanmakuJob()
	s := &a.danmaku
	s.open, s.advanced, s.message, s.sourceURL = false, false, "", ""
	s.episodes, s.selected, s.comments = nil, nil, nil
	s.screen.Reset(nil)
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
			s.screen.Reset(comments)
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
	s.screen.Reset(comments)
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

func (a *app) danmakuDialog(c *ui.Context) {
	s := &a.danmaku
	ui.DialogBase(c, &s.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(2, 8, 22, 0.67))
		_, height := c.Size()
		panel.Width(540).MaxWidth(540).Height(min(float32(720), height-32)).Padding(22).Gap(12).Radius(24).Material(glass.Glass{})
		ui.Text(c, "弹幕设置").FontSize(22).Bold()
		ui.Scroll(c).Label("弹幕设置内容").FillWidth().Grow(1).MinHeight(0).Gap(12).Children(func() {
			ui.Text(c, "跟随当前分集加载弹幕，片源未提供时自动搜索。").FontSize(13)
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "显示弹幕").Grow(1)
				if ui.Switch(c, &a.preferences.Danmaku.Enabled).Label("弹幕开关").Changed() {
					if !a.preferences.Danmaku.Enabled {
						a.cancelDanmakuJob()
						s.message = "弹幕已关闭"
					} else if len(s.comments) == 0 {
						a.loadCurrentDanmaku()
					}
					a.savePreferences()
				}
			})
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "字号").Width(48)
				if ui.Slider(c, &a.preferences.Danmaku.Size, 14, 36).Label("弹幕字号").Grow(1).Changed() {
					a.savePreferences()
				}
				ui.Text(c, strconv.Itoa(int(a.preferences.Danmaku.Size))).Width(30)
			})
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "不透明度").Width(56)
				if ui.Slider(c, &a.preferences.Danmaku.Opacity, 0.1, 1).Label("弹幕不透明度").Grow(1).Changed() {
					a.savePreferences()
				}
			})
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "滚动速度").Width(64)
				if ui.Slider(c, &a.preferences.Danmaku.Speed, 0.25, 1.5).Label("弹幕滚动速度").Grow(1).Changed() {
					a.savePreferences()
				}
				ui.Text(c, fmt.Sprintf("%.2f×", a.preferences.Danmaku.Speed)).Width(48)
			})
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "显示区域").Width(64)
				if ui.Slider(c, &a.preferences.Danmaku.Area, 0.25, 1).Label("弹幕显示区域").Grow(1).Changed() {
					a.savePreferences()
				}
				ui.Text(c, fmt.Sprintf("%.0f%%", a.preferences.Danmaku.Area*100)).Width(48)
			})
			a.danmakuSearchControls(c)
			if glassButton(c, "服务设置（可选）").FillWidth().Clicked() {
				s.advanced = !s.advanced
			}
			if s.advanced {
				a.manualDanmaku(c)
			}
			if s.message != "" {
				ui.Text(c, s.message).FontSize(12)
			}
			if len(s.episodes) > 0 {
				ui.List(c, &s.list, len(s.episodes), func(i int) {
					episode := s.episodes[i]
					selected := s.selected != nil && s.selected.ID == episode.ID
					if panelButton(c, episode.Anime+" · "+episode.Name, selected).Key(episode.ID).Clicked() {
						a.loadDanmakuEpisode(episode)
					}
				}).FillWidth().Height(min(float32(180), max(float32(80), height-480)))
			}
		})
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			if glassButton(c, "完成").Clicked() {
				s.open = false
			}
		})
	})
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

func danmakuArea(picture, bounds ui.Rect, fraction float32, avoidControls bool) ui.Rect {
	top, limit := picture.Y+12, picture.Y+picture.H-12
	if avoidControls {
		top = max(top, bounds.Y+72)
		limit = min(limit, bounds.Y+bounds.H-132)
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
		area := danmakuArea(picture, r, float32(settings.Area), avoidControls)
		size := float32(settings.Size)
		if area.W <= 0 || area.H < size*1.4 {
			return
		}
		position := player.Position()
		sprites := a.danmaku.screen.Frame(position, area.W, area.H, size, float32(settings.Speed), func(text string) float32 {
			width, _ := p.MeasureText(0, ui.Span{Text: text, Size: size})
			return width
		})
		p.Clip(area, 0, func() {
			for _, sprite := range sprites {
				color := ui.RGBA(uint8(sprite.Color>>16), uint8(sprite.Color>>8), uint8(sprite.Color), float32(settings.Opacity))
				x, y := area.X+sprite.X, area.Y+sprite.Y
				p.Text(x+1, y+1, sprite.Text, size, ui.RGBA(0, 0, 0, float32(settings.Opacity)))
				p.Text(x, y, sprite.Text, size, color)
			}
		})
		if player.Playing() && !player.Buffering() {
			if len(sprites) > 0 {
				p.AnimationFrameRate(30)
			} else if next := a.danmaku.screen.Next(); next > position {
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
