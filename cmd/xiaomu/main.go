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
	// openLocal plays a file from disk directly, without the proxy.
	// addRecent records a file in the system's recent documents.
	// notify posts a system notification; nowPlaying publishes to the
	// system Now Playing info. All four are replaceable for headless
	// tests and wired to MyGo in main().
	openLocal      func(string) (*ui.VideoPlayer, error)
	addRecent      func(string) error
	notify         func(id, title, body string)
	nowPlaying     nowPlayingSink
	nowPlayingLast nowPlayingSnapshot
	playbackPower  playbackPower
	// pending holds file/URL events that arrived before the window
	// existed; they run in order once it does.
	pending []func()
	// Only the latest navigation waits for a configuration still loading.
	pendingLibrary func()

	nav             []routeKind
	jobs            [jobSlots]job
	browser         browser
	title           *titlePage
	library         libraryBrowser
	discovery       discoveryBrowser
	live            liveBrowser
	history         historyStore
	favorites       favoriteStore
	searchHistory   searchHistoryStore
	searchFocused   bool
	latency         latencyStore
	speedTest       sourceSpeedTest
	sourceMemory    sourceMemoryStore
	sourceAttempt   sourcePlaybackAttempt
	seriesSkips     seriesSkipStore
	skipPlayback    seriesSkipPlayback
	focusSearchGrid bool
	preferences     playerPreferences
	preferencesPath string
	playbackWait    playbackWait
	playFailure     *episodeFailure
	linkOpen        bool
	closed          bool
	persist         bool
	dispatch        func(func())

	playingLive   bool
	playingDirect bool
	// playingFile is set for a local file opened from Finder; playingName
	// is that file's name, shown in the header and Now Playing.
	playingFile    bool
	playingName    string
	playingSource  string
	playingEpisode string
	resumeAt       time.Duration
	resumeIssued   bool
	lastPosition   time.Duration

	// speedBytes/speedAt/speedText are the last throughput sample
	// taken from the proxy: the bytes served then, when, and the
	// text the player shows for the rate since.
	speedBytes      int64
	speedAt         time.Time
	speedText       string
	panelSource     int
	panelTab        int
	panelOpen       bool
	panelTouched    bool
	fullScreen      bool
	playerOverlay   playerOverlayState
	playerFocus     ui.Handle
	autoNext        bool
	autoNextPending bool
	episodeEnd      playbackEnd
}

func (a *app) view(c *ui.Context) {
	defer a.observeNowPlaying()
	defer a.syncPlaybackPower(time.Now())
	a.handleKeyboard(c)
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
	if a.playDirectURL(a.rawURL, playbackHeaders(a.referer, a.ua)) {
		a.linkOpen = false
	}
}

// adoptPlayer makes p the current player and resets the per-play state.
// playURL and openLocalFile share it; both close the old player before
// opening, so a failed open still leaves no player, as before.
func (a *app) adoptPlayer(p *ui.VideoPlayer) {
	if a.player != nil {
		a.player.Close()
	}
	a.player = p
	a.applyPlayerPreferences(p)
	a.playbackWait = playbackWait{}
	a.playFailure = nil
	// A new episode starts with no flow yet, so the speed reads
	// neutral until the first sample a second later.
	a.speedAt = time.Time{}
	a.speedText = "—"
	a.resumeAt, a.lastPosition = 0, 0
	a.resumeIssued = false
	a.panelOpen, a.panelTouched, a.panelSource, a.panelTab = !a.fullScreen, false, -1, 0
	a.playerOverlay = playerOverlayState{}
	a.autoNextPending = false
	a.episodeEnd = playbackEnd{}
	a.playingFile = false
	a.playingName = ""
	a.playingSource, a.playingEpisode = "", ""
}

// playURL opens HTTP media through the proxy. The caller publishes Now
// Playing after assigning the episode, channel or direct-play state.
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
		a.syncNowPlaying()
		return false
	}
	a.adoptPlayer(player)
	a.playingLive, a.playingDirect = false, false
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
	a.initUpdates()
	a.notify = postNotification
	a.addRecent = mygo.App.AddRecentDocument
	a.nowPlaying = mygo.NowPlaying
	a.playbackPower.keepAwake = mygo.Power.KeepAwake
	// System entry points. All are registered before Run so a file or
	// link that launched the app is delivered too; the handlers queue
	// with deferUntilReady when the window does not exist yet.
	mygo.App.OnOpenFile(func(path string) {
		a.deferUntilReady(func() { a.openLocalFile(path) })
	})
	mygo.App.OnOpenURL(func(raw string) {
		a.deferUntilReady(func() { a.handleDeepLink(raw) })
	})
	mygo.App.OnNotificationClick(func(id string) {
		a.deferUntilReady(func() { a.handleNotificationClick(id) })
	})
	mygo.App.OnDidBecomeActive(func() { mygo.ClearNotifications() })
	mygo.NowPlaying.OnCommand(func(cmd mygo.MediaCommand, position time.Duration) {
		a.handleMediaCommand(cmd, position)
	})
	// Claim the xiaomu:// scheme for this install. It is declared in
	// mygo.json (urlSchemes), so the built .app lists it in Info.plist;
	// registering at launch makes xiaomu the default handler for it.
	// The error is ignored: in `go run` there is no bundle to declare
	// it, and the app still works without being the handler.
	_ = mygo.App.RegisterURLScheme("xiaomu")
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
		a.restoreFavorites()
		a.restoreSearchHistory()
		a.restoreLatency()
		a.restorePreferences()
		a.restoreSeriesSkips()
		a.restoreSourceMemory()
		a.setFullScreen(win.IsFullScreen())
		win.OnEnterFullScreen(func() { win.Update(func() { a.setFullScreen(true) }) })
		win.OnLeaveFullScreen(func() { win.Update(func() { a.setFullScreen(false) }) })
		a.restoreSavedSource()
		// Library actions wait separately for the asynchronous restore.
		a.drainPending()
		win.OnClosed(a.close)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
