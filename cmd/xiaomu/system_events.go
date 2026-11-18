package main

// System entry points: files opened with the app and xiaomu:// deep links.
//
// MyGo delivers both through App.OnOpenFile / App.OnOpenURL, on the main
// thread, possibly before the window exists (a file or link that launched
// the app). Handlers registered in main() therefore queue the work with
// deferUntilReady instead of touching the UI directly.

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui/video"
)

// playableFileExts are the extensions the built .app claims in mygo.json
// (fileAssociations). Double-clicking one in Finder opens it in xiaomu.
var playableFileExts = map[string]bool{
	".mp4":  true,
	".mov":  true,
	".m4v":  true,
	".m3u8": true,
}

func isPlayableFile(path string) bool {
	return playableFileExts[strings.ToLower(filepath.Ext(path))]
}

// deferUntilReady runs fn once the window exists. Events arriving before
// that (launch file / launch URL) wait in a.pending and run in order.
func (a *app) deferUntilReady(fn func()) {
	if a.window != nil {
		fn()
		return
	}
	a.pending = append(a.pending, fn)
}

// drainPending runs the work queued before the window existed.
func (a *app) drainPending() {
	pending := a.pending
	a.pending = nil
	for _, fn := range pending {
		fn()
	}
}

// openLocalFile plays a video file from disk, without the HTTP proxy:
// no headers need injecting for a local file. It is a no-op (and reports
// false) for anything that is not a playable video extension.
func (a *app) openLocalFile(path string) bool {
	if !isPlayableFile(path) {
		a.status = "小幕只能打开 mp4、mov、m4v 或 m3u8 视频文件"
		return false
	}
	if a.openLocal == nil {
		a.openLocal = video.Open
	}
	if a.player != nil {
		a.player.Close()
		a.player = nil
	}
	p, err := a.openLocal(path)
	if err != nil {
		a.status = "打开文件失败：" + err.Error()
		return false
	}
	a.adoptPlayer(p)
	a.playingName = filepath.Base(path)
	a.playingSource, a.playingEpisode = "", ""
	a.playingLive, a.playingDirect = false, true
	a.playingFile = true
	a.status = "正在播放"
	// The player view shows whenever a.player is set; the page stack is
	// untouched, so leaving the player returns to where the user was.
	if a.addRecent != nil {
		// Best effort: the file plays even if the recent-documents
		// record fails.
		_ = a.addRecent(path)
	}
	a.syncNowPlaying()
	return true
}

// deepLinkAction is one parsed xiaomu:// URL.
type deepLinkAction struct {
	kind  string // "play" or "search"
	play  string // play URL for kind "play"
	query string // search keyword for kind "search"
}

// parseDeepLink parses a xiaomu:// URL. Supported forms:
//
//	xiaomu://play?url=<url-encoded http(s) video/play URL>
//	xiaomu://search?wd=<keyword>
//
// Anything else (wrong scheme, unknown host, non-http(s) play URL) is
// rejected: a link must never make the app open or execute arbitrary
// content.
func parseDeepLink(raw string) (deepLinkAction, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "xiaomu" {
		return deepLinkAction{}, false
	}
	switch u.Host {
	case "play":
		play := u.Query().Get("url")
		if play == "" {
			return deepLinkAction{}, false
		}
		pu, err := url.Parse(play)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") {
			return deepLinkAction{}, false
		}
		return deepLinkAction{kind: "play", play: play}, true
	case "search":
		query := strings.TrimSpace(u.Query().Get("wd"))
		if query == "" {
			return deepLinkAction{}, false
		}
		return deepLinkAction{kind: "search", query: query}, true
	default:
		return deepLinkAction{}, false
	}
}

// handleDeepLink routes one xiaomu:// URL: play opens the URL in the
// player (via the proxy, like 打开链接）, search runs a library search.
func (a *app) handleDeepLink(raw string) {
	action, ok := parseDeepLink(raw)
	if !ok {
		a.status = "无法识别的链接"
		return
	}
	switch action.kind {
	case "play":
		a.playURL(action.play, nil)
	case "search":
		// The player view covers every page while a video plays, so
		// leave it first; the search page then shows on arrival.
		if a.player != nil {
			a.leavePlayer()
		}
		a.browser.query = action.query
		a.searchLibrary()
	}
}
