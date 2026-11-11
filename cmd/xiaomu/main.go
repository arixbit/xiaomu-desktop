// Command xiaomu browses user-provided drpy2 sites and plays HTTP media.
package main

import (
	"context"
	"log"
	"net/url"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"github.com/arixbit/xiaomu-desktop/internal/proxy"
)

// app is the window state. open is nil in the real process and calls
// video.OpenURL; tests replace it.
type app struct {
	window          *mygo.Window
	rawURL          string
	referer         string
	ua              string
	status          string
	player          *ui.VideoPlayer
	posters         posterCache
	proxy           *proxy.Proxy
	open            func(string) (*ui.VideoPlayer, error)
	browser         browser
	live            liveBrowser
	playingLive     bool
	direct          bool
	busy            bool
	closed          bool
	persist         bool
	cancel          context.CancelFunc
	dispatch        func(func())
	playingSource   string
	playingEpisode  string
	panelSource     int
	panelOpen       bool
	panelTouched    bool
	autoNext        bool
	autoNextPending bool
}

func (a *app) view(c *ui.Context) {
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	}
	if a.player != nil {
		a.playerScreen(c)
		return
	}
	ui.Box(c).Fill().Children(func() {
		if a.direct {
			ui.Column(c).Fill().Padding(28).Gap(14).Background(c.Theme().Background).Children(func() {
				ui.Text(c, "直链播放").FontSize(26).Bold()
				if ui.Button(c, "返回片库").Clicked() {
					a.direct = false
				}
				a.directView(c)
			})
		} else {
			a.browserView(c)
		}
		a.statusView(c)
	})
}

func (a *app) statusView(c *ui.Context) {
	if a.status == "" && !a.busy {
		return
	}
	ui.Row(c).Absolute().Right(16).Bottom(16).MaxWidth(640).Padding(9, 13).Gap(10).
		Radius(16).Material(glass.Glass{}).Children(func() {
		if a.status != "" {
			ui.Text(c, a.status).Selectable().Grow(1)
		}
		if a.busy && ui.Button(c, "取消加载").Clicked() {
			a.cancel()
		}
	})
}

func (a *app) directView(c *ui.Context) {
	ui.Scroll(c).FillWidth().Grow(1).Gap(10).Children(func() {
		ui.Text(c, "粘贴 mp4 / m3u8 视频直链").TextColor(c.Theme().TextMuted)
		ui.TextInput(c, &a.rawURL).Label("地址").Placeholder("https://example.com/video.mp4").FillWidth().MaxWidth(680)
		ui.TextInput(c, &a.referer).Label("Referer").Placeholder("可选").FillWidth().MaxWidth(680)
		ui.TextInput(c, &a.ua).Label("User-Agent").Placeholder("可选").FillWidth().MaxWidth(680)
		if ui.PrimaryButton(c, "播放").Clicked() {
			a.play()
		}
	})
}

func (a *app) play() {
	a.playURL(a.rawURL, playbackHeaders(a.referer, a.ua))
}

func (a *app) playURL(raw string, headers map[string]string) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		a.status = "请输入 http 或 https 播放地址"
		return
	}
	if a.proxy == nil {
		p, err := proxy.Start(nil)
		if err != nil {
			a.status = "代理启动失败: " + err.Error()
			return
		}
		a.proxy = p
	}
	a.proxy.SetHeaders(headers)
	wrapped := a.proxy.Wrap(raw)
	if a.player != nil {
		a.player.Close()
		a.player = nil
	}
	opener := a.open
	if opener == nil {
		opener = video.OpenURL
	}
	player, err := opener(wrapped)
	if err != nil {
		a.status = "无法打开: " + err.Error()
		return
	}
	a.player = player
	a.playingLive = false
	a.panelOpen, a.panelTouched, a.panelSource = true, false, -1
	a.autoNextPending = false
	a.status = "正在播放"
}

func playbackHeaders(referer, ua string) map[string]string {
	h := map[string]string{}
	if v := strings.TrimSpace(referer); v != "" {
		h["Referer"] = v
	}
	if v := strings.TrimSpace(ua); v != "" {
		h["User-Agent"] = v
	}
	return h
}

func main() {
	a := &app{autoNext: true, persist: true}
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:         "小幕",
			Width:         1120,
			Height:        760,
			MinWidth:      640,
			MinHeight:     420,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHiddenInset,
			Vibrancy:      mygo.VibrancyGlass,
			Content:       ui.View(a.view),
		})
		a.window = win
		a.dispatch = win.Update
		a.restoreSavedSource()
		win.OnClosed(a.close)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
