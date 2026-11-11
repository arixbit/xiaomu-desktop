// Command xiaomu is a minimal native window: paste an mp4 or m3u8 URL,
// optionally a Referer and User-Agent, and play it. Headers are injected
// by a loopback proxy because video.OpenURL cannot set them.
package main

import (
	"log"
	"net/url"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"github.com/arixbit/xiaomu-desktop/internal/proxy"
)

// app is the window state. open is nil in the real process and calls
// video.OpenURL; tests replace it.
type app struct {
	rawURL  string
	referer string
	ua      string
	status  string
	player  *ui.VideoPlayer
	proxy   *proxy.Proxy
	open    func(string) (*ui.VideoPlayer, error)
}

func (a *app) view(c *ui.Context) {
	ui.Column(c).Fill().Padding(20).Gap(10).Children(func() {
		ui.Text(c, "小幕").FontSize(22).Bold()
		ui.Text(c, "粘贴 mp4 / m3u8 直链。Referer 与 User-Agent 经本机代理注入。").TextColor(c.Theme().TextMuted)
		ui.TextInput(c, &a.rawURL).Label("地址").Placeholder("https://example.com/video.mp4").Width(680)
		ui.TextInput(c, &a.referer).Label("Referer").Placeholder("可选").Width(680)
		ui.TextInput(c, &a.ua).Label("User-Agent").Placeholder("可选").Width(680)
		if ui.PrimaryButton(c, "播放").Clicked() {
			a.play()
		}
		if a.status != "" {
			ui.Text(c, a.status)
		}
		if a.player != nil {
			ui.Video(c, a.player).Height(380).Fit(ui.Contain)
		}
	})
}

func (a *app) play() {
	raw := strings.TrimSpace(a.rawURL)
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
	a.proxy.SetHeaders(playbackHeaders(a.referer, a.ua))
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
	a := &app{}
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:     "小幕",
			Width:     960,
			Height:    640,
			MinWidth:  640,
			MinHeight: 420,
			StateKey:  "main",
			Content:   ui.View(a.view),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
