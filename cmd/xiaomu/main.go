// Command xiaomu browses user-provided sources and plays HTTP media.
package main

import (
	"log"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	mpvplayer "github.com/arixbit/mygo-player"
	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/arixbit/xiaomu-desktop/internal/proxy"
)

// app is the window state; only the UI thread changes it. open is nil in the
// real process and uses mygo-player (macOS & Windows amd64/arm64) with system
// video.OpenURL fallback; tests replace open or openMPV.
type app struct {
	window  *mygo.Window
	rawURL  string
	referer string
	ua      string
	status  string
	player  mediaPlayer
	proxy   *proxy.Proxy
	open    func(string) (*ui.VideoPlayer, error)
	openMPV func(string, mpvplayer.OpenOptions) (mediaPlayer, error)

	proxySession  string
	panUI         panUIState
	panLogin      *panLoginSession
	startPanLogin func(pan.Provider, func())

	newDanmakuClient func(string) (*danmaku.Client, error)
	// openLocal plays a file from disk directly, without the proxy.
	// addRecent records a file in the system's recent documents.
	// notify posts a system notification; nowPlaying publishes to the
	// system Now Playing info. All are replaceable for headless
	// tests and wired to MyGo in main().
	openLocal        func(string) (*ui.VideoPlayer, error)
	pickLocalFile    func() ([]string, error)
	pickSubtitleFile func() ([]string, error)
	fileDialogOpen   bool
	addRecent        func(string) error
	notify           func(id, title, body string)
	nowPlaying       nowPlayingSink
	nowPlayingLast   nowPlayingSnapshot
	playbackPower    playbackPower
	// pending holds file/URL events that arrived before the window
	// existed; they run in order once it does.
	pending []func()
	// Only the latest navigation waits for a configuration still loading.
	pendingLibrary func()

	nav    []routeKind
	navDir int // 1 push, -1 pop, 0 tab switch
	// jobsMu guards jobs: a job's finish can run off the UI thread
	// (in tests dispatch is nil), while cancelJob runs on the UI
	// thread, so a slot's cancel function and generation are never
	// touched without it.
	jobsMu          sync.Mutex
	jobs            [jobSlots]job
	browser         browser
	title           *titlePage
	library         libraryBrowser
	discovery       discoveryBrowser
	live            liveBrowser
	history         historyStore
	localFileChecks map[string]localFileCheck
	favorites       favoriteStore
	searchHistory   searchHistoryStore
	searchFocused   bool
	searchFocus     ui.Handle
	latency         latencyStore
	siteHealth      siteHealthStore
	showFailedSites bool
	speedTest       sourceSpeedTest
	sourceMemory    sourceMemoryStore
	sourceAttempt   sourcePlaybackAttempt
	seriesSkips     seriesSkipStore
	skipPlayback    seriesSkipPlayback
	focusSearchGrid bool
	preferences     playerPreferences
	danmaku         danmakuState
	preferencesPath string
	sourcePath      string
	playbackWait    playbackWait
	playFailure     *episodeFailure
	linkOpen        bool
	linkAdvanced    bool
	linkErr         string
	closed          bool
	persist         bool
	dispatch        func(func())
	availableUpdate string

	toast     toastFeedback
	armed     armedConfirmState
	playerOSD playerOSDState
	skipUndo  skipUndoState

	playingLive       bool
	playingDirect     bool
	playingDirectMPV  bool
	playingDirectLive bool
	// playingFile is set for a local file opened from the file manager; playingName
	// is that file's name, shown in the header and Now Playing.
	playingFile       bool
	playingFilePath   string
	playingName       string
	localPlaylist     []string
	localPlaylistIdx  int
	playingSource     string
	playingEpisode    string
	playingSourceIdx  int
	playingEpisodeIdx int
	playingStreams    []catalog.Stream
	playingStreamIdx  int
	resumeAt          time.Duration
	resumeIssued      bool
	lastPosition      time.Duration

	// speedBytes/speedAt/speedText are the last throughput sample
	// taken from the proxy: the bytes served then, when, and the
	// text the player shows for the rate since.
	speedBytes       int64
	speedAt          time.Time
	speedText        string
	panelSource      int
	panelChoice      *playerSourceChoice
	panelTab         int
	panelOpen        bool
	panelTouched     bool
	fullScreen       bool
	playerOverlay    playerOverlayState
	playerPopovers   playerPopoverState
	playerPointer    playerPointerState
	playerFocus      ui.Handle
	autoNext         bool
	autoNextPending  bool
	autoNextCanceled bool
	muted            bool
	savedVolume      float64
	episodeEnd       playbackEnd
	playerExtra      playerExtraState
	seekPreview      seekPreviewState
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
	a.linkErr = ""
	raw := cleanUserURL(a.rawURL)
	if ref, ok := pan.ParseShareURL(raw); ok {
		a.openPanShare(ref)
		return
	}
	if a.playDirectURL(raw, playbackHeaders(a.referer, a.ua)) {
		a.linkOpen = false
		a.linkErr = ""
	} else if a.linkOpen {
		a.linkErr = a.status
		a.clearStatus()
	}
}

func (a *app) closeCurrentPlayer() {
	a.resetSeekPreview()
	a.stopDanmaku()
	a.cancelJob(jobSubtitle)
	a.releaseRightKeyHold(a.player)
	if a.player != nil {
		a.player.Close()
		a.player = nil
	}
}

// adoptPlayer makes p the current player and resets the per-play state.
// Episode replacements close the old player here, once the new one opens.
func (a *app) adoptPlayer(p mediaPlayer) {
	a.closeCurrentPlayer()
	a.player = p
	a.resetPerVideoExtraState()
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
	a.closePlayerPopovers()
	a.playerPointer = playerPointerState{}
	a.autoNextPending = false
	a.autoNextCanceled = false
	a.skipUndo = skipUndoState{}
	a.playerOSD = playerOSDState{}
	if a.muted {
		p.SetMuted(true)
	}
	a.episodeEnd = playbackEnd{}
	a.live.catchupProgram = nil
	a.playingDirectMPV = false
	a.playingDirectLive = false
	a.playingFile = false
	a.playingFilePath = ""
	a.playingName = ""
	a.localPlaylist = nil
	a.localPlaylistIdx = -1
	a.playingSource, a.playingEpisode = "", ""
	a.playingSourceIdx, a.playingEpisodeIdx = -1, -1
	a.playingStreams, a.playingStreamIdx = nil, 0
}

func (a *app) ensureProxy() (*proxy.Proxy, error) {
	if a.proxy != nil {
		return a.proxy, nil
	}
	p, err := proxy.Start(nil)
	if err != nil {
		return nil, err
	}
	a.proxy = p
	return p, nil
}

// openProxySession serves one playback stream on the shared loopback proxy
// using an isolated /s/<id>/... session so episode switches, line changes, and
// retries keep the existing upstream TCP/TLS connection pool warm while leaving
// the current player untouched until the replacement actually opens.
func (a *app) openProxySession(cfg proxy.SessionConfig, keepCurrent bool) bool {
	p, err := a.ensureProxy()
	if err != nil {
		a.status = "代理启动失败：" + err.Error()
		return false
	}
	oldSession := a.proxySession
	if !keepCurrent {
		a.closeCurrentPlayer()
		if oldSession != "" {
			p.RemoveSession(oldSession)
			a.proxySession = ""
			oldSession = ""
		}
	}
	sessID, localURL := p.RegisterSession(cfg)
	player, err := a.openMediaURL(localURL)
	if err != nil {
		p.RemoveSession(sessID)
		a.status = "无法打开：" + err.Error()
		a.syncNowPlaying()
		return false
	}
	a.adoptPlayer(player)
	if oldSession != "" && oldSession != sessID {
		p.RemoveSession(oldSession)
	}
	a.proxySession = sessID
	a.setSeekPreviewMedia(seekPreviewMedia{target: localURL})
	a.playingLive, a.playingDirect = false, false
	a.status = "正在播放"
	return true
}

// openPlaybackStream serves a stream through an isolated proxy session that
// preserves its filename extension and applies bounded range prefetch for
// non-HLS cloud-drive media.
func (a *app) openPlaybackStream(stream catalog.Stream, keepCurrent bool) bool {
	raw := strings.TrimSpace(stream.URL)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		a.status = "请输入 http 或 https 播放地址"
		return false
	}
	return a.openProxySession(proxy.SessionConfig{
		URL:      raw,
		Headers:  stream.Headers,
		Filename: stream.Filename,
		Prefetch: !stream.IsHLS,
	}, keepCurrent)
}

func (a *app) openMediaURL(localURL string) (mediaPlayer, error) {
	if a.open != nil {
		vp, err := a.open(localURL)
		if err != nil {
			return nil, err
		}
		return wrapSystemPlayer(vp), nil
	}
	if a.openMPV != nil {
		return a.openMPV(localURL, mpvplayer.OpenOptions{})
	}
	return openMediaWithFallback(localURL, mpvplayer.OpenOptions{}, video.OpenURL)
}

func (a *app) openMediaFile(path string, subtitles []string) (mediaPlayer, error) {
	if a.openLocal != nil {
		vp, err := a.openLocal(path)
		if err != nil {
			return nil, err
		}
		return wrapSystemPlayer(vp), nil
	}
	opts := mpvplayer.OpenOptions{Subtitles: subtitles}
	if a.openMPV != nil {
		return a.openMPV(path, opts)
	}
	return openMediaWithFallback(path, opts, video.Open)
}

func mpvOpenOptionsForHeaders(headers map[string]string) mpvplayer.OpenOptions {
	var opts mpvplayer.OpenOptions
	if len(headers) == 0 {
		return opts
	}
	extra := make(map[string]string, len(headers))
	for k, v := range headers {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		switch {
		case strings.EqualFold(k, "User-Agent"):
			opts.UserAgent = v
		case strings.EqualFold(k, "Referer"), strings.EqualFold(k, "Referrer"):
			opts.Referrer = v
		default:
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		opts.Headers = extra
	}
	return opts
}

// playDirectMPVURL opens non-HTTP streaming protocols (such as RTSP/RTMP)
// directly with mygo-player (libmpv) and passes per-stream headers natively.
func (a *app) playDirectMPVURL(raw string, headers map[string]string) bool {
	raw = strings.TrimSpace(raw)
	if a.proxy != nil && a.proxySession != "" {
		a.proxy.RemoveSession(a.proxySession)
		a.proxySession = ""
	}
	a.closeCurrentPlayer()
	opts := mpvOpenOptionsForHeaders(headers)
	var (
		player mediaPlayer
		err    error
	)
	if a.openMPV != nil {
		player, err = a.openMPV(raw, opts)
	} else {
		player, err = openMPVPlayer(raw, opts)
	}
	if err != nil {
		a.status = "无法打开：" + err.Error()
		a.syncNowPlaying()
		return false
	}
	a.adoptPlayer(player)
	a.setSeekPreviewMedia(seekPreviewMedia{target: raw, opts: opts})
	a.playingLive, a.playingDirect, a.playingDirectMPV = false, false, true
	a.status = "正在播放"
	return true
}

// playURL opens HTTP media through the proxy. The caller publishes Now
// Playing after assigning the episode, channel or direct-play state.
func (a *app) playURL(raw string, headers map[string]string) bool {
	return a.openPlaybackURL(raw, headers, false)
}

// openPlaybackURL serves HTTP media on the shared loopback proxy so episode
// switches, line changes, and retries keep the upstream TCP/TLS connection pool
// warm. Replacement opens stage headers under an isolated /u?sid=... session so
// the current player stays unaffected until the new stream actually opens.
func (a *app) openPlaybackURL(raw string, headers map[string]string, keepCurrent bool) bool {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		a.status = "请输入 http 或 https 播放地址"
		return false
	}
	p, err := a.ensureProxy()
	if err != nil {
		a.status = "代理启动失败：" + err.Error()
		return false
	}
	oldSession := a.proxySession
	var sessID, localURL string
	if keepCurrent {
		sessID, localURL = p.RegisterWrapSession(raw, headers)
	} else {
		a.closeCurrentPlayer()
		if oldSession != "" {
			p.RemoveSession(oldSession)
			a.proxySession = ""
			oldSession = ""
		}
		p.SetHeaders(headers)
		localURL = p.Wrap(raw)
	}
	player, err := a.openMediaURL(localURL)
	if err != nil {
		if sessID != "" {
			p.RemoveSession(sessID)
		}
		a.status = "无法打开：" + err.Error()
		a.syncNowPlaying()
		return false
	}
	a.adoptPlayer(player)
	if keepCurrent {
		p.SetHeaders(headers)
	}
	if oldSession != "" && oldSession != sessID {
		p.RemoveSession(oldSession)
	}
	a.proxySession = sessID
	a.setSeekPreviewMedia(seekPreviewMedia{target: localURL})
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
	// One instance only, before touching any state: a file opened with
	// the app, or a xiaomu:// link clicked, while xiaomu runs must reach
	// the running instance instead of starting another one that would
	// read and write the same history. The second instance forwards its
	// command line to the first and exits here.
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	// Xiaomu always uses a unified cinema dark appearance across all pages,
	// regardless of the OS light/dark setting.
	mygo.Theme.SetSource(mygo.ThemeDark)
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
	mygo.App.OnSecondInstance(func(args []string, workingDir string) {
		// The forwarded file or link reaches OnOpenFile/OnOpenURL
		// after this; bring the window forward first.
		if w := a.window; w != nil {
			w.Restore()
			w.Focus()
		}
	})
	mygo.NowPlaying.OnCommand(func(cmd mygo.MediaCommand, position time.Duration) {
		a.handleMediaCommand(cmd, position)
	})
	// Claim the xiaomu:// scheme for this install. It is declared in
	// mygo.json (urlSchemes), so the built .app lists it in Info.plist
	// and the Windows installer registers it; registering at launch
	// makes xiaomu the default handler for it.
	// Development builds must not claim it: `mygo dev` runs a temporary
	// executable, and registering would point xiaomu:// at that instead
	// of the installed app. The error is ignored: in `go run` there is
	// no bundle to declare it, and the app still works without being
	// the handler.
	if !mygo.IsDev() {
		_ = mygo.App.RegisterURLScheme("xiaomu")
	}
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "小幕",
			Width:           1120,
			Height:          760,
			MinWidth:        640,
			MinHeight:       420,
			StateKey:        "main",
			TitleBarStyle:   mygo.TitleBarHiddenInset,
			BackgroundColor: "#0b1530",
			Vibrancy:        mygo.VibrancyGlass,
			Content:         ui.View(a.view),
		})
		a.window = win
		a.dispatch = win.Update
		a.watchUpdates()
		if runtime.GOOS == "windows" {
			// The content under the caption buttons always has a dark background.
			win.SetWindowButtonsTheme(mygo.ThemeDark)
			fitMainWindow(win)
			win.OnUnmaximize(func() { win.Update(func() { fitMainWindow(win) }) })
		}
		a.initDiscovery()
		a.restoreHistory()
		a.restoreFavorites()
		a.restoreLiveState()
		a.restoreSearchHistory()
		a.restoreLatency()
		a.restoreSiteHealth()
		a.restorePreferences()
		a.restoreSeriesSkips()
		a.restoreSourceMemory()
		a.restorePanCredentials()
		a.setFullScreen(win.IsFullScreen())
		win.OnFileDrop(func(e *mygo.FileDropEvent) {
			if e == nil || len(e.Paths) == 0 {
				return
			}
			paths := append([]string(nil), e.Paths...)
			win.Update(func() {
				if !a.closed {
					a.handleDroppedFiles(paths)
				}
			})
		})
		win.OnEnterFullScreen(func() { win.Update(func() { a.setFullScreen(true) }) })
		win.OnLeaveFullScreen(func() {
			win.Update(func() {
				a.setFullScreen(false)
				if runtime.GOOS == "windows" {
					fitMainWindow(win)
				}
			})
		})
		a.restoreSavedSource()
		// Library actions wait separately for the asynchronous restore.
		a.drainPending()
		win.OnClosed(a.close)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// MyGo can center an oversized window outside the Windows work area or
// restore a saved rectangle that is only partially visible.
func fitMainWindow(win *mygo.Window) {
	if win.IsMaximized() || win.IsFullScreen() {
		return
	}
	bounds := win.Bounds()
	work := mygo.Screen.DisplayMatching(bounds).WorkArea
	if fitted := windowBoundsInWorkArea(bounds, work); fitted != bounds {
		win.SetBounds(fitted)
	}
}

func windowBoundsInWorkArea(bounds, work mygo.Rectangle) mygo.Rectangle {
	if work.Width <= 0 || work.Height <= 0 || bounds.Width <= 0 || bounds.Height <= 0 {
		return bounds
	}
	bounds.Width = min(bounds.Width, work.Width)
	bounds.Height = min(bounds.Height, work.Height)
	bounds.X = min(max(bounds.X, work.X), work.X+work.Width-bounds.Width)
	bounds.Y = min(max(bounds.Y, work.Y), work.Y+work.Height-bounds.Height)
	return bounds
}
