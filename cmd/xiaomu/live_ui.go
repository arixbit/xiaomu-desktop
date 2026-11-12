package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

type liveBrowser struct {
	url, input, query, group string
	importOpen, restore      bool
	sources                  []iptv.Source
	selectedSource           int
	channels                 []iptv.Channel
	playingChannel           int
	playingStream            int
	attempts                 int
	started                  time.Time
}

func (a *app) setLiveSources(sources []iptv.Source) {
	a.live.sources = sources
	a.live.channels = nil
	a.live.selectedSource = -1
	a.live.group, a.live.query = "", ""
}

func (a *app) showLive() {
	a.browser.sidebar = "live"
	if a.live.restore {
		a.importLive(a.live.url)
	} else if a.live.selectedSource < 0 && len(a.live.sources) > 0 {
		a.loadLiveSource(0)
	}
}

func (a *app) importLive(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		a.status = "请先粘贴直播列表地址"
		return
	}
	a.run("导入直播", func(ctx context.Context) (jobResult, error) {
		doc, err := config.Load(ctx, raw, "", nil)
		if err != nil {
			return jobResult{}, err
		}
		if len(doc.Lives) == 0 {
			return jobResult{}, fmt.Errorf("此配置没有 IPTV 直播，请导入 M3U、TXT 或含 lives 的配置")
		}
		return jobResult{apply: func() {
			a.setLiveSources(doc.Lives)
			a.live.url, a.live.input, a.live.restore = raw, raw, false
			a.browser.sidebar = "live"
			a.loadLiveSource(0)
			if a.persist {
				if err := a.saveLiveSource(raw); err != nil {
					a.status += "；保存直播地址失败：" + err.Error()
				}
			}
		}}, nil
	})
}

func (a *app) loadLiveSource(index int) {
	if index < 0 || index >= len(a.live.sources) {
		return
	}
	source := a.live.sources[index]
	a.run("加载频道", func(ctx context.Context) (jobResult, error) {
		channels, err := config.LiveChannels(ctx, source, nil)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			a.live.channels, a.live.selectedSource = channels, index
			a.live.group, a.live.query = "", ""
			a.status = fmt.Sprintf("已加载 %d 个直播频道，选择频道即可播放", len(channels))
		}}, nil
	})
}

func (a *app) playLiveChannel(index, streamIndex int) {
	a.live.attempts = 0
	a.startLiveChannel(index, streamIndex)
}

func (a *app) startLiveChannel(index, streamIndex int) {
	if index < 0 || index >= len(a.live.channels) {
		return
	}
	channel := a.live.channels[index]
	if streamIndex < 0 || streamIndex >= len(channel.Streams) {
		return
	}

	for a.live.attempts < len(channel.Streams) {
		stream := channel.Streams[streamIndex]
		a.live.attempts++
		a.live.playingChannel, a.live.playingStream = index, streamIndex
		u, err := url.Parse(stream.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			a.status = "此频道使用非 HTTP 直播协议；当前支持 HTTP/HTTPS 视频流，组播频道请使用 udpxy 转换后的地址"
		} else {
			a.live.started = time.Now()
			a.playURL(stream.URL, stream.Headers)
			if a.status == "正在播放" {
				a.direct, a.playingLive = false, true
				return
			}
		}
		streamIndex = (streamIndex + 1) % len(channel.Streams)
	}

}

func (a *app) liveView(c *ui.Context) {
	live := &a.live
	ui.Column(c).Fill().Padding(28, 30).Gap(16).Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "IPTV 直播").FontSize(26).Bold().Grow(1)
			if ui.Button(c, "添加直播源").Disabled(a.busy).Clicked() {
				live.importOpen = true
			}
		})
		if len(live.sources) == 0 {
			ui.Column(c).FillWidth().Grow(1).Center().Gap(12).Children(func() {
				ui.Text(c, "添加直播源，开始看电视").FontSize(24).Bold()
				ui.Text(c, "支持 M3U、TXT 频道列表，以及 TVBox 配置中的直播。")
			})
		} else {
			ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
				for i, source := range live.sources {
					if ui.Button(c, source.Name).Key(fmt.Sprintf("live-source-%d", i)).
						Selected(i == live.selectedSource).Disabled(a.busy).Clicked() {
						a.loadLiveSource(i)
					}
				}
			})
			ui.SearchField(c, &live.query).Label("频道搜索").Placeholder("搜索频道名称").FillWidth()
			ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
				if ui.Button(c, "全部频道").Selected(live.group == "").Clicked() {
					live.group = ""
				}
				seen := map[string]bool{}
				for _, channel := range live.channels {
					if seen[channel.Group] {
						continue
					}
					seen[channel.Group] = true
					if ui.Button(c, channel.Group).Key("live-group-" + channel.Group).Selected(live.group == channel.Group).Clicked() {
						live.group = channel.Group
					}
				}
			})
			ui.Scroll(c).FillWidth().Grow(1).Gap(8).Children(func() {
				visible := 0
				for i, channel := range live.channels {
					if live.group != "" && channel.Group != live.group || !strings.Contains(strings.ToLower(channel.Name), strings.ToLower(strings.TrimSpace(live.query))) {
						continue
					}
					visible++
					ui.Row(c).Key(fmt.Sprintf("live-channel-%d", i)).FillWidth().Padding(12).Gap(10).Radius(14).
						Material(glass.Glass{Style: glass.Clear}).AlignItems(ui.Center).Children(func() {
						ui.Column(c).Grow(1).Gap(4).Children(func() {
							ui.Text(c, channel.Name).Bold()
							ui.Text(c, fmt.Sprintf("%s · %d 条线路", channel.Group, len(channel.Streams))).FontSize(12)
						})
						if ui.Button(c, "播放").Label("播放 " + channel.Name).Disabled(a.busy).Clicked() {
							a.playLiveChannel(i, 0)
						}
					})
				}
				if visible == 0 && !a.busy {
					ui.Text(c, "没有找到频道，可换个关键词或重新选择直播来源。")
				}
			})
		}
	})
	ui.DialogBase(c, &live.importOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(2, 8, 22, 170))
		panel.Width(520).MaxWidth(520).Padding(26).Gap(16).Radius(22).Material(glass.Glass{Style: glass.Regular})
		ui.Text(c, "添加直播源").FontSize(24).Bold()
		ui.Text(c, "粘贴 M3U、TXT 或 TVBox 直播配置地址。")
		ui.TextInput(c, &live.input).Label("直播地址").Placeholder("https://example.com/channels.m3u").FillWidth().Disabled(a.busy)
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				live.importOpen = false
			}
			if ui.PrimaryButton(c, "导入直播").Disabled(a.busy).Clicked() {
				live.importOpen = false
				a.importLive(live.input)
			}
		})
	})
}

// advanceLive tries each alternative once when the player fails or never starts.
func (a *app) advanceLive(p *ui.VideoPlayer) {
	if !a.playingLive || a.busy || p == nil || a.live.playingChannel < 0 || a.live.playingChannel >= len(a.live.channels) {
		return
	}
	if p.Err() == nil && (p.Ready() || time.Since(a.live.started) < 15*time.Second) {
		return
	}
	count := len(a.live.channels[a.live.playingChannel].Streams)
	if a.live.attempts >= count {
		a.status = fmt.Sprintf("该频道的 %d 条线路均未能启动，请稍后重试", count)
		return
	}
	a.startLiveChannel(a.live.playingChannel, (a.live.playingStream+1)%count)
}
