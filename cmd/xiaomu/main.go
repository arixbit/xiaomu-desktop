// Command xiaomu browses user-provided sources and plays HTTP media.
package main

import (
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"github.com/arixbit/xiaomu-desktop/internal/proxy"
)

// app is the window state; only the UI thread changes it. open is nil in the
// real process and calls video.OpenURL; tests replace it.
type app struct {
	window  *mygo.Window
	rawURL  string
	referer string
	ua      string
	status  string
	player  *ui.VideoPlayer
	posters posterCache
	proxy   *proxy.Proxy
	open    func(string) (*ui.VideoPlayer, error)

	nav       []routeKind
	jobs      [jobSlots]job
	browser   browser
	title     *titlePage
	library   libraryBrowser
	discovery discoveryBrowser
	live      liveBrowser
	history   historyStore
	linkOpen  bool
	closed    bool
	persist   bool
	dispatch  func(func())

	playingLive     bool
	playingDirect   bool
	playingSource   string
	playingEpisode  string
	resumeAt        time.Duration
	lastPosition    time.Duration
	panelSource     int
	panelTab        int
	panelOpen       bool
	panelTouched    bool
	fullScreen      bool
	playerOverlay   playerOverlayState
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
	a.shell(c)
}

func (a *app) play() {
	if a.playURL(a.rawURL, playbackHeaders(a.referer, a.ua)) {
		a.playingDirect, a.linkOpen = true, false
	}
}

// playURL hands an HTTP address to the player through the loopback proxy,
// which adds the request headers. It reports whether the player opened.
func (a *app) playURL(raw string, headers map[string]string) bool {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		a.status = "请输入 http 或 https 播放地址"
		return false
	}
	if a.proxy == nil {
		p, err := proxy.Start(nil)
		if err != nil {
			a.status = "代理启动失败: " + err.Error()
			return false
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
		return false
	}
	a.player = player
	a.playingLive, a.playingDirect = false, false
	a.resumeAt, a.lastPosition = 0, 0
	a.panelOpen, a.panelTouched, a.panelSource, a.panelTab = !a.fullScreen, false, -1, 0
	a.playerOverlay = playerOverlayState{}
	a.autoNextPending = false
	a.status = "正在播放"
	return true
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
		a.initDiscovery()
		a.restoreHistory()
		a.setFullScreen(win.IsFullScreen())
		win.OnEnterFullScreen(func() { win.Update(func() { a.setFullScreen(true) }) })
		win.OnLeaveFullScreen(func() { win.Update(func() { a.setFullScreen(false) }) })
		a.restoreSavedSource()
		win.OnClosed(a.close)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
