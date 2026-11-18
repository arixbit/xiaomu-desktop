package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

var playerInk = ui.RGB(9, 15, 31)
var playerText = ui.RGB(248, 249, 255)

const playerOverlayIdle = 3 * time.Second

type playerOverlayState struct {
	hideAt             time.Time
	pointerX, pointerY float32
	pointerOver        bool
	keepVisible        bool
}

func (a *app) setFullScreen(full bool) {
	a.fullScreen = full
	a.playerOverlay = playerOverlayState{}
	if full {
		a.panelOpen, a.panelTouched = false, false
	}
}

func (a *app) playerOverlaysVisible(c *ui.Context, screen ui.Element) bool {
	s := &a.playerOverlay
	x, y, over := screen.PointerPosition()
	now := c.Now()
	if s.hideAt.IsZero() || s.keepVisible || over && (!s.pointerOver || x != s.pointerX || y != s.pointerY) {
		s.hideAt = now.Add(playerOverlayIdle)
	}
	s.pointerX, s.pointerY, s.pointerOver = x, y, over
	s.keepVisible = false
	if !a.fullScreen {
		return true
	}
	if now.Before(s.hideAt) {
		c.After(s.hideAt.Sub(now))
		return true
	}
	return false
}

// playerScreen keeps the system video in one view and paints the controls
// over it. All VideoPlayer calls stay on MyGo's UI thread.
func (a *app) playerScreen(c *ui.Context) {
	a.advanceLive(a.player)
	p := a.player
	if p == nil {
		return
	}
	a.trackPlayback(c, p)
	a.checkPlayback(c, p)
	a.advanceEpisode(p)
	width, _ := c.Size()
	compact := width < 900
	overlayPanel := compact || a.fullScreen
	// The panel holds a title's episodes and sources, or a live list's channels.
	hasEpisodes := a.hasEpisodes() || (a.playingLive && len(a.live.channels) > 1)
	screen := ui.Box(c).Fill().Background(playerInk).ObserveInput(func(ev ui.InputEvent) {
		a.playerOverlay.hideAt = ev.Time.Add(playerOverlayIdle)
	})
	showControls := a.playerOverlaysVisible(c, screen)
	showPanel := hasEpisodes && a.panelOpen && (!compact || a.panelTouched) && showControls
	if !showControls {
		screen.Cursor(ui.CursorNone)
	}
	screen.Children(func() {
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Grow(1).FillHeight().Clip().Children(func() {
				ui.Video(c, p).Key(p).Fill().Fit(ui.Contain).Label("视频画面").Focusable().AutoFocus().Bind(&a.playerFocus)
				a.playerState(c, p)
				if showControls {
					a.playerControls(c, p, hasEpisodes, compact)
					a.playerHeader(c, hasEpisodes, compact)
				}
			})
			if showPanel && !overlayPanel {
				ui.Box(c).Width(320).FillHeight().Padding(10, 10, 10, 0).Children(func() {
					a.playerPanel(c).Fill()
				})
			}
		})
		if showPanel && overlayPanel {
			bottom := float32(12)
			if a.fullScreen {
				bottom = 140
			}
			a.playerPanel(c).Absolute().Top(70).Right(12).Bottom(bottom).Width(280)
		}
	})
}

func (a *app) playerHeader(c *ui.Context, hasEpisodes, compact bool) {
	name := "直链播放"
	if !a.playingDirect && a.title != nil {
		name = a.title.name
		if a.playingEpisode != "" {
			name += " · " + a.playingEpisode
		}
	}
	if a.playingLive && a.live.playingChannel < len(a.live.channels) {
		name = "直播 · " + a.live.channels[a.live.playingChannel].Name
	}
	top := float32(12)
	if bar := c.TitleBar(); bar.Bottom+8 > top {
		top = bar.Bottom + 8
	}
	left := max(float32(88), c.TitleBar().Left+14)
	ui.Row(c).Absolute().Top(top).Left(left).Right(16).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Padding(5).Radius(18).Material(glass.Glass{Style: glass.Regular}).Children(func() {
			if ui.Button(c, "返回").Radius(16).Material(glass.Glass{Style: glass.Clear}).TextColor(playerText).Clicked() {
				a.leavePlayer()
			}
		})
		// The title takes the wide middle of the header: it is the
		// player's drag handle. The back and panel buttons on either side
		// keep working, and only the title area drags; a double click on
		// it maximizes the window.
		title := ui.Row(c).Padding(8, 14).Radius(20).Material(glass.Glass{Style: glass.Clear}).Grow(1)
		if dragStripHeight(c) > 0 {
			title.DragWindow().Label("拖动窗口")
		}
		title.Children(func() {
			ui.Text(c, name).SingleLine().TextColor(playerText).TextShadow(0, 1, 4, playerInk)
		})
		if hasEpisodes {
			panelLabel, closeLabel := "线路与选集", "收起选集"
			if a.playingLive {
				panelLabel, closeLabel = "频道列表", "收起频道"
			}
			if a.panelOpen && (!compact || a.panelTouched) {
				panelLabel = closeLabel
			}
			ui.Row(c).Padding(5).Radius(18).Material(glass.Glass{Style: glass.Regular}).Children(func() {
				if ui.Button(c, panelLabel).Radius(16).Material(glass.Glass{Style: glass.Clear}).TextColor(playerText).Clicked() {
					if compact && !a.panelTouched {
						a.panelOpen = true
					} else {
						a.panelOpen = !a.panelOpen
					}
					a.panelTouched = true
				}
			})
		}
	})
}

func (a *app) playerState(c *ui.Context, p *ui.VideoPlayer) {
	message := ""
	switch {
	case a.running(jobPlay):
		message = a.status
	case a.playFailure != nil && a.hasEpisodes():
		a.playerFailure(c, a.playFailure.err)
		return
	case p.Err() != nil:
		if a.hasEpisodes() {
			a.playerFailure(c, p.Err())
			return
		}
		message = "播放失败：" + p.Err().Error()
	case !p.Ready():
		message = "正在加载视频…"
		if a.playingLive && time.Since(a.live.started) >= 15*time.Second {
			message = a.status
		}
	case p.Buffering():
		message = "正在缓冲…"
	case a.status != "" && a.status != "正在播放":
		message = a.status
	}
	if message == "" {
		return
	}
	ui.Row(c).Absolute().Top(84).Left(24).Right(24).Center().Children(func() {
		ui.Text(c, message).Selectable().Padding(9, 14).Radius(14).
			Material(glass.Glass{Style: glass.Regular}).MaxWidth(620)
	})
}

func (a *app) playerControls(c *ui.Context, p *ui.VideoPlayer, hasEpisodes, compact bool) {
	ui.Row(c).Absolute().Left(18).Right(18).Bottom(18).Justify(ui.Center).Children(func() {
		ui.Column(c).FillWidth().MaxWidth(900).Padding(10, 15).Gap(8).
			Radius(24).Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(9, 15, 31, 0.59)}).
			TextColor(playerText).Children(func() {
			duration := p.Duration()
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				if duration > 0 && !a.playingLive {
					ui.Text(c, playerClock(p.Position())).FontSize(12)
					position := p.Position().Seconds()
					progress := ui.Slider(c, &position, 0, duration.Seconds()).Label("播放进度").Grow(1)
					if progress.Pressed() {
						a.playerOverlay.keepVisible = true
					}
					if progress.Changed() {
						p.Seek(time.Duration(position * float64(time.Second)))
					}
					ui.Text(c, playerClock(duration)).FontSize(12)
				} else {
					ui.Text(c, "直播").FontSize(12).Grow(1)
				}
				w, h := p.Size()
				resolution := "分辨率读取中…"
				if w > 0 && h > 0 {
					resolution = fmt.Sprintf("分辨率 %d×%d", w, h)
				} else if p.Err() != nil {
					resolution = "分辨率未知"
				}
				ui.Text(c, resolution).FontSize(12).SingleLine()
			})
			ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				if a.playingLive {
					a.livePlayerControls(c)
				}
				hasEpisodes := a.hasEpisodes()
				source, episode, current := a.currentEpisode()
				if hasEpisodes && ui.Button(c, "上一集").Radius(16).Material(glass.Glass{Style: glass.Clear, Interactive: true}).TextColor(playerText).
					Disabled(!current || episode == 0).Clicked() {
					a.playEpisode(a.title.detail.Sources[source].Name, a.title.detail.Sources[source].Episodes[episode-1])
				}
				playLabel := "暂停"
				if !p.Playing() {
					playLabel = "播放"
				}
				if ui.Button(c, playLabel).Radius(16).Material(glass.Glass{Style: glass.Clear, Interactive: true}).TextColor(playerText).Clicked() {
					togglePlayback(p)
				}
				if hasEpisodes && ui.Button(c, "下一集").Radius(16).Material(glass.Glass{Style: glass.Clear, Interactive: true}).TextColor(playerText).
					Disabled(!current || episode+1 >= len(a.title.detail.Sources[source].Episodes)).Clicked() {
					a.playEpisode(a.title.detail.Sources[source].Name, a.title.detail.Sources[source].Episodes[episode+1])
				}
				ui.Text(c, "音量").FontSize(12)
				volume := p.Volume()
				volumeControl := ui.Slider(c, &volume, 0, 1).Label("调节音量").Width(88)
				if volumeControl.Pressed() {
					a.playerOverlay.keepVisible = true
				}
				if volumeControl.Changed() {
					p.SetVolume(volume)
					a.ensurePreferences()
					a.preferences.Volume = volume
					a.savePreferences()
				}
				ui.Spacer(c)
				rate := p.Rate()
				if rate <= 0 {
					rate = 1
				}
				if !a.playingLive && ui.Button(c, strconv.FormatFloat(rate, 'f', -1, 64)+"×").Label("播放速度").
					Radius(16).Material(glass.Glass{Style: glass.Clear, Interactive: true}).TextColor(playerText).Clicked() {
					a.ensurePreferences()
					a.preferences.Rate = nextPlayerRate(rate)
					p.SetRate(a.preferences.Rate)
					a.savePreferences()
				}
				if a.window != nil {
					fullLabel := "全屏"
					if a.fullScreen {
						fullLabel = "退出全屏"
					}
					if ui.Button(c, fullLabel).Radius(16).Material(glass.Glass{Style: glass.Clear, Interactive: true}).TextColor(playerText).Clicked() {
						a.window.ToggleFullScreen()
						a.playerFocus.Focus(c)
					}
				}
			})
		})
	})
}

// playerPanel floats beside the picture: a title's episodes and sources, or
// a live list's channels.
func (a *app) playerPanel(c *ui.Context) ui.Element {
	panel := ui.Themed(c, ui.DarkTheme()).Padding(16).Gap(12).Radius(24).
		Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(40, 35, 70, 0.67)}).
		TextColor(playerText)
	if panel.Hovered() {
		a.playerOverlay.keepVisible = true
	}
	panel.Children(func() {
		if a.playingLive {
			a.panelChannels(c)
			return
		}
		if !a.hasEpisodes() { // the header left the player earlier in this frame
			return
		}
		candidates := a.candidates()
		tabs := []string{"选集"}
		if len(candidates) > 1 {
			tabs = append(tabs, "换源")
		}
		skipTab := len(tabs)
		tabs = append(tabs, "跳过")
		if a.panelTab < 0 || a.panelTab >= len(tabs) {
			a.panelTab = 0
		}
		ui.Segmented(c, &a.panelTab, tabs...).Label("面板内容")
		if a.panelTab == skipTab {
			ui.Scroll(c).FillWidth().Grow(1).Gap(10).Children(func() {
				a.panelSeriesSkips(c)
			})
			return
		}
		if a.panelTab == 1 && len(candidates) > 1 {
			a.panelSources(c, candidates)
			return
		}
		a.panelEpisodes(c)
	})
	return panel
}

func panelButton(c *ui.Context, label string, selected bool) ui.Element {
	tint := ui.RGBA(55, 50, 92, 0.39)
	if selected {
		tint = ui.RGBA(102, 98, 143, 0.57)
	}
	return ui.Button(c, label).FillWidth().Padding(10, 12).Radius(14).
		Material(glass.Glass{Style: glass.Clear, Tint: tint, Interactive: true}).TextColor(playerText)
}

func (a *app) panelEpisodes(c *ui.Context) {
	detail := a.title.detail
	currentSource, _, playing := a.currentEpisode()
	if a.panelSource < 0 || a.panelSource >= len(detail.Sources) {
		a.panelSource = 0
		if playing {
			a.panelSource = currentSource
		}
	}
	ui.Scroll(c).FillWidth().Grow(1).Gap(8).Children(func() {
		ui.Text(c, "线路").Bold()
		for i, source := range detail.Sources {
			name := source.Name
			if name == "" {
				name = fmt.Sprintf("线路 %d", i+1)
			}
			if panelButton(c, name, i == a.panelSource).Key(fmt.Sprintf("player-source-%d", i)).Clicked() {
				if _, index, requested, at, ok := a.recoveryEpisode(); ok {
					if _, candidate, found := matchEpisode(catalog.Detail{Sources: detail.Sources[i : i+1]}, requested.Name, index); found {
						a.playEpisodeAt(source.Name, candidate, at)
					}
				}
				a.panelSource = i
			}
		}
		ui.Text(c, "选集").Bold()
		selected := detail.Sources[a.panelSource]
		ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
			for i, episode := range selected.Episodes {
				active := selected.Name == a.playingSource && episode.Name == a.playingEpisode
				button := ui.Button(c, episodeButtonLabel(episode.Name)).Key(fmt.Sprintf("player-episode-%d", i)).MinWidth(52).Radius(12)
				if active {
					button.Background(ui.RGB(255, 255, 255)).TextColor(playerInk)
				} else {
					button.Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(55, 50, 92, 0.51), Interactive: true}).TextColor(playerText)
				}
				if button.Clicked() {
					a.playEpisode(selected.Name, episode)
				}
			}
		})
	})
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "播完自动下一集").Grow(1)
		if ui.Switch(c, &a.autoNext).Label("自动下一集").Changed() {
			a.savePreferences()
		}
	})
}

// panelSources lists the other sites that have the title; choosing one
// keeps the episode and the position.
func (a *app) panelSources(c *ui.Context, candidates []library.Item) {
	ui.Text(c, "换来源后从当前集、当前进度继续播放").FontSize(12)
	ui.Scroll(c).FillWidth().Grow(1).Gap(6).Children(func() {
		for _, item := range candidates {
			key := itemKey(item)
			current := itemKey(a.title.item) == key
			if panelButton(c, item.Entry.Label(), current).Key(key).Label("换到 "+item.Entry.Label()).Clicked() && !current {
				a.switchSource(item)
			}
		}
	})
}

// panelChannels changes the channel without leaving the player.
func (a *app) panelChannels(c *ui.Context) {
	live := &a.live
	ui.Text(c, "频道").FontSize(18).Bold()
	ui.Scroll(c).FillWidth().Grow(1).Gap(6).Children(func() {
		for i, channel := range live.channels {
			if live.group != "" && channel.Group != live.group {
				continue
			}
			current := i == live.playingChannel
			if panelButton(c, channel.Name, current).Key(fmt.Sprintf("panel-channel-%d", i)).Label("切换到 "+channel.Name).Clicked() && !current {
				a.playLiveChannel(i, 0)
			}
		}
	})
}

// playerFailure offers the ways out of an episode that does not play.
func (a *app) playerFailure(c *ui.Context, err error) {
	ui.Row(c).Absolute().Top(96).Left(24).Right(24).Center().Children(func() {
		ui.Column(c).MaxWidth(460).Padding(22).Gap(14).Radius(26).Material(glass.Glass{}).TextColor(playerText).Children(func() {
			ui.Text(c, "这条线路播不了").FontSize(18).Bold()
			if f := a.playFailure; f != nil {
				ui.Text(c, f.line+" · "+f.episode.Name).FontSize(13)
			}
			ui.Text(c, err.Error()).Selectable()
			if a.status != "" && a.status != "正在播放" {
				ui.Text(c, a.status)
			}
			a.recoveryActions(c)
		})
	})
}

// hasEpisodes reports whether what plays is an episode of the title on show.
func (a *app) hasEpisodes() bool {
	return !a.playingDirect && !a.playingLive && a.title != nil && a.title.detail != nil && len(a.title.detail.Sources) > 0
}

// trackPlayback seeks to where a resumed episode stopped, then keeps the
// position for the play history and for changing line or source.
func (a *app) trackPlayback(c *ui.Context, p *ui.VideoPlayer) {
	if !p.Ready() || p.Err() != nil || a.playFailure != nil || a.autoNextPending {
		return
	}
	a.applyOpeningSkip(p)
	if a.resumeAt > 0 {
		if !a.resumeIssued {
			if duration := p.Duration(); duration > 0 {
				a.resumeAt = min(a.resumeAt, max(0, duration-time.Second))
			}
			p.Seek(a.resumeAt)
			a.lastPosition, a.resumeIssued = a.resumeAt, true
			return
		}
		// AVPlayer seeks asynchronously. Keep the intended position until it
		// arrives, so a second line switch cannot accidentally resume at zero.
		if p.Position() < a.resumeAt-time.Second {
			return
		}
		a.resumeAt, a.resumeIssued = 0, false
	}
	a.lastPosition = p.Position()
	a.recordHistory(p, c.Now())
}

func (a *app) advanceEpisode(p *ui.VideoPlayer) {
	if !a.hasEpisodes() || !a.autoNext || a.autoNextPending || a.running(jobPlay) || a.playFailure != nil || p.Err() != nil {
		return
	}
	if !p.Ended() && !a.skipEndingReady(p) {
		return
	}
	source, episode, ok := a.currentEpisode()
	if !ok {
		return
	}
	episodes := a.title.detail.Sources[source].Episodes
	if episode+1 >= len(episodes) {
		return
	}
	a.recordEpisodeCompletion(p)
	a.autoNextPending = true
	a.playEpisode(a.title.detail.Sources[source].Name, episodes[episode+1])
}

func (a *app) currentEpisode() (sourceIndex, episodeIndex int, ok bool) {
	if a.title == nil || a.title.detail == nil || a.playingEpisode == "" {
		return 0, 0, false
	}
	for i, source := range a.title.detail.Sources {
		if source.Name != a.playingSource {
			continue
		}
		for j, episode := range source.Episodes {
			if episode.Name == a.playingEpisode {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func (a *app) leavePlayer() {
	a.cancelJob(jobPlay)
	a.flushHistory()
	if a.player != nil {
		a.player.Close()
		a.player = nil
	}
	a.playingLive, a.playingDirect = false, false
	a.playerOverlay = playerOverlayState{}
	a.playbackWait = playbackWait{}
	a.playFailure = nil
	a.status = ""
	// A title played from the history has no page to go back to.
	if a.route() != routeDetail {
		a.clearTitle()
	}
}

var episodeNumber = regexp.MustCompile(`^(?:第\s*)?([0-9]+)\s*集(?:\s*(?:已完结|完结))?$|^([0-9]+)$`)

// Keep the original name for playback and matching between sources.
func episodeButtonLabel(name string) string {
	parts := episodeNumber.FindStringSubmatch(strings.TrimSpace(name))
	if parts == nil {
		return name
	}
	digits := parts[1] + parts[2]
	if number, err := strconv.Atoi(digits); err == nil {
		return strconv.Itoa(number)
	}
	return name
}

func (a *app) livePlayerControls(c *ui.Context) {
	live := &a.live
	if ui.Button(c, "上一频道").Disabled(live.playingChannel <= 0).Clicked() {
		a.playLiveChannel(live.playingChannel-1, 0)
	}
	if ui.Button(c, "下一频道").Disabled(live.playingChannel+1 >= len(live.channels)).Clicked() {
		a.playLiveChannel(live.playingChannel+1, 0)
	}
	if live.playingChannel >= 0 && live.playingChannel < len(live.channels) {
		count := len(live.channels[live.playingChannel].Streams)
		if count > 1 && ui.Button(c, fmt.Sprintf("切换线路 %d/%d", live.playingStream+1, count)).Clicked() {
			a.playLiveChannel(live.playingChannel, (live.playingStream+1)%count)
		}
	}
}

func nextPlayerRate(current float64) float64 {
	for _, rate := range []float64{1, 1.25, 1.5, 2} {
		if rate > current+0.01 {
			return rate
		}
	}
	return 1
}

func playerClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int64(d / time.Second)
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}
