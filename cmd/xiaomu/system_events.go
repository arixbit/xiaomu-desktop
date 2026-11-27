package main

// System entry points: files opened with the app and xiaomu:// deep links.
//
// MyGo delivers both through App.OnOpenFile / App.OnOpenURL, on the main
// thread, possibly before the window exists (a file or link that launched
// the app). Handlers registered in main() therefore queue the work with
// deferUntilReady instead of touching the UI directly.

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

// playableFileExts are the container extensions supported by the system player
// across all desktop platforms.
var playableFileExts = map[string]bool{
	".mp4":  true,
	".mov":  true,
	".m4v":  true,
	".m3u8": true,
}

// mpvExtraFileExts are additional container extensions handled natively when
// mygo-player (libmpv) is supported on the current platform.
var mpvExtraFileExts = map[string]bool{
	".mkv":  true,
	".webm": true,
	".flv":  true,
	".ts":   true,
	".m2ts": true,
	".avi":  true,
	".wmv":  true,
	".mpg":  true,
	".mpeg": true,
	".rmvb": true,
	".3gp":  true,
	".ogv":  true,
}

var subtitleFileExts = map[string]bool{
	".srt": true,
	".ass": true,
	".ssa": true,
	".vtt": true,
	".sup": true,
	".sub": true,
}

func playableFileExtensions() []string {
	if mpvBackendSupported() {
		return []string{"mp4", "mkv", "mov", "m4v", "m3u8", "webm", "flv", "ts", "m2ts", "avi", "wmv", "mpg", "mpeg", "rmvb", "3gp", "ogv"}
	}
	return []string{"mp4", "mov", "m4v", "m3u8"}
}

func subtitleFileExtensions() []string {
	return []string{"srt", "ass", "ssa", "vtt", "sup", "sub"}
}

func isPlayableFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if playableFileExts[ext] {
		return true
	}
	return mpvBackendSupported() && mpvExtraFileExts[ext]
}

func isSubtitleFile(path string) bool {
	return subtitleFileExts[strings.ToLower(filepath.Ext(path))]
}

// discoverSiblingSubtitles automatically locates sidecar subtitle files sharing
// the same directory and basename as the opened video (e.g. movie.srt,
// movie.zh.ass), sparing the user from manually loading companion subtitles.
func discoverSiblingSubtitles(videoPath string) []string {
	dir := filepath.Dir(videoPath)
	base := filepath.Base(videoPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	stemLower := strings.ToLower(stem)
	var exact, tagged []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isSubtitleFile(name) {
			continue
		}
		subStem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		full := filepath.Join(dir, name)
		switch {
		case subStem == stemLower:
			exact = append(exact, full)
		case strings.HasPrefix(subStem, stemLower+".") || strings.HasPrefix(subStem, stemLower+"_") || strings.HasPrefix(subStem, stemLower+"-"):
			tagged = append(tagged, full)
		}
	}
	return append(exact, tagged...)
}

// deferUntilReady runs fn once the window exists. Events arriving before
// that (launch file / launch URL) wait in a.pending and run in order.
func (a *app) deferUntilReady(fn func()) {
	if a.closed {
		return
	}
	if a.window != nil {
		if a.dispatch != nil {
			a.dispatch(func() {
				if !a.closed {
					fn()
				}
			})
		} else {
			fn()
		}
		return
	}
	a.pending = append(a.pending, fn)
}

// drainPending runs the work queued before the window existed.
func (a *app) drainPending() {
	pending := a.pending
	a.pending = nil
	for _, fn := range pending {
		a.deferUntilReady(fn)
	}
}

// withLibrary delays navigation while the imported configuration loads.
// A later navigation replaces it; opening media abandons it too.
func (a *app) withLibrary(fn func()) {
	a.pendingLibrary = nil
	if a.running(jobConfig) {
		a.pendingLibrary = fn
		return
	}
	fn()
}

func (a *app) finishLibraryEvent() {
	fn := a.pendingLibrary
	a.pendingLibrary = nil
	if fn != nil && a.library.root != nil && !a.closed {
		fn()
	}
}

// prepareExternalPlayback prevents an old resolver from replacing the
// newly requested media, and saves the episode's history before switching.
func (a *app) prepareExternalPlayback() {
	a.pendingLibrary = nil
	a.cancelJob(jobTitle)
	a.cancelJob(jobPlay)
	a.clearPanelChoice()
	a.flushHistory()
	a.sourceAttempt = sourcePlaybackAttempt{}
}

// cleanUserURL trims whitespace and accidentally copied surrounding quotes or
// brackets around a pasted address.
func cleanUserURL(raw string) string {
	s := strings.TrimSpace(raw)
	for len(s) >= 2 {
		switch {
		case strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`),
			strings.HasPrefix(s, `'`) && strings.HasSuffix(s, `'`),
			strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">"):
			s = strings.TrimSpace(s[1 : len(s)-1])
		case strings.HasPrefix(s, "“") && strings.HasSuffix(s, "”"):
			s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "“"), "”"))
		case strings.HasPrefix(s, "‘") && strings.HasSuffix(s, "’"):
			s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "‘"), "’"))
		case strings.HasPrefix(s, "【") && strings.HasSuffix(s, "】"):
			s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "【"), "】"))
		default:
			return s
		}
	}
	return s
}

func (a *app) playDirectURL(raw string, headers map[string]string) bool {
	raw = cleanUserURL(raw)
	u, err := url.Parse(raw)
	scheme := ""
	if err == nil {
		scheme = strings.ToLower(u.Scheme)
	}
	switch {
	case err == nil && u.Host != "" && (scheme == "http" || scheme == "https"):
		a.prepareExternalPlayback()
		if !a.playURL(raw, headers) {
			return false
		}
		a.playingDirect = true
		a.syncNowPlaying()
		return true
	case err == nil && u.Host != "" && (scheme == "rtsp" || scheme == "rtmp") && (mpvBackendSupported() || a.openMPV != nil):
		a.prepareExternalPlayback()
		if !a.playDirectMPVURL(raw, headers) {
			return false
		}
		a.playingDirect = true
		a.playingDirectLive = true
		if a.player != nil {
			a.player.SetRate(1)
		}
		a.syncNowPlaying()
		return true
	default:
		if mpvBackendSupported() || a.openMPV != nil {
			a.status = "请输入有效的视频地址（http / https / rtsp / rtmp）或网盘分享链接"
		} else {
			a.status = "请输入 http 或 https 播放地址"
		}
		return false
	}
}

// discoverSiblingVideos scans the opened video's directory for playable video
// files and returns them in natural order along with the current file's index.
func discoverSiblingVideos(videoPath string) (files []string, current int) {
	clean := filepath.Clean(videoPath)
	dir := filepath.Dir(clean)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, -1
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !isPlayableFile(name) {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, -1
	}
	sort.Slice(names, func(i, j int) bool {
		return naturalLess(names[i], names[j])
	})
	files = make([]string, len(names))
	current = -1
	for i, name := range names {
		full := filepath.Join(dir, name)
		files[i] = full
		if full == clean {
			current = i
		}
	}
	return files, current
}

// naturalLess orders filenames with embedded numbers numerically (e.g. EP2 < EP10).
func naturalLess(a, b string) bool {
	ia, ib := 0, 0
	for ia < len(a) && ib < len(b) {
		ca, cb := a[ia], b[ib]
		da, db := ca >= '0' && ca <= '9', cb >= '0' && cb <= '9'
		if da && db {
			za, zb := ia, ib
			for ia < len(a) && a[ia] == '0' {
				ia++
			}
			for ib < len(b) && b[ib] == '0' {
				ib++
			}
			sa, sb := ia, ib
			for ia < len(a) && a[ia] >= '0' && a[ia] <= '9' {
				ia++
			}
			for ib < len(b) && b[ib] >= '0' && b[ib] <= '9' {
				ib++
			}
			la, lb := ia-sa, ib-sb
			if la != lb {
				return la < lb
			}
			if a[sa:ia] != b[sb:ib] {
				return a[sa:ia] < b[sb:ib]
			}
			if (sa - za) != (sb - zb) {
				return (sa - za) < (sb - zb)
			}
			continue
		}
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return ca < cb
		}
		ia++
		ib++
	}
	if ia != len(a) || ib != len(b) {
		return ia == len(a)
	}
	return a < b
}

// openLocalFile plays a video file from disk, without the HTTP proxy:
// no headers need injecting for a local file. It is a no-op (and reports
// false) for anything that is not a playable video extension.
func (a *app) openLocalFile(path string) bool {
	return a.openLocalFileWithSubtitles(path, nil)
}

func (a *app) openLocalFileWithSubtitles(path string, subtitles []string) bool {
	return a.openLocalFileAt(path, subtitles, a.localResumePosition(path), false)
}

func (a *app) openLocalFileAt(path string, subtitles []string, at time.Duration, keepPanel bool) bool {
	if !isPlayableFile(path) {
		if mpvBackendSupported() {
			a.status = "小幕不支持打开该文件格式，请选择常见视频文件（如 mp4、mkv、mov、ts、flv 等）"
		} else {
			a.status = "小幕只能打开 mp4、mov、m4v 或 m3u8 视频文件"
		}
		return false
	}
	open, touched := a.panelOpen, a.panelTouched
	skipOpen, wasShown := a.panelSkipOpen, a.panelWasShown
	a.prepareExternalPlayback()
	a.closeCurrentPlayer()
	clean := filepath.Clean(path)
	if len(subtitles) == 0 {
		subtitles = discoverSiblingSubtitles(clean)
	}
	p, err := a.openMediaFile(clean, subtitles)
	if err != nil {
		a.status = "打开文件失败：" + err.Error()
		a.syncNowPlaying()
		return false
	}
	a.adoptPlayer(p)
	if keepPanel {
		a.panelOpen, a.panelTouched = open, touched
		a.panelSkipOpen, a.panelWasShown = skipOpen, wasShown
	}
	a.playingName = filepath.Base(clean)
	a.playingFilePath = clean
	a.setSeekPreviewMedia(seekPreviewMedia{target: clean, isLocal: true})
	a.playingSource, a.playingEpisode = "", ""
	a.playingLive, a.playingDirect = false, true
	a.playingFile = true
	a.localPlaylist, a.localPlaylistIdx = discoverSiblingVideos(clean)
	a.resumeAt = at
	a.status = "正在播放"
	a.revealContinueWatchingKey(localHistoryKey(clean))
	// The player view shows whenever a.player is set; the page stack is
	// untouched, so leaving the player returns to where the user was.
	if a.addRecent != nil {
		// Best effort: the file plays even if the recent-documents
		// record fails.
		_ = a.addRecent(clean)
	}
	a.syncNowPlaying()
	a.startDanmaku()
	a.closeLocalPosterDialog()
	a.startLocalPoster(clean)
	return true
}

func (a *app) hasLocalPlaylist() bool {
	return a.playingFile && len(a.localPlaylist) > 1 && a.localPlaylistIdx >= 0 && a.localPlaylistIdx < len(a.localPlaylist)
}

func (a *app) nextLocalFile() (string, bool) {
	if !a.hasLocalPlaylist() || a.localPlaylistIdx+1 >= len(a.localPlaylist) {
		return "", false
	}
	return a.localPlaylist[a.localPlaylistIdx+1], true
}

func (a *app) prevLocalFile() (string, bool) {
	if !a.hasLocalPlaylist() || a.localPlaylistIdx <= 0 {
		return "", false
	}
	return a.localPlaylist[a.localPlaylistIdx-1], true
}

func (a *app) playLocalPlaylistIndex(idx int) bool {
	if !a.playingFile || idx < 0 || idx >= len(a.localPlaylist) {
		return false
	}
	return a.openLocalFileAt(a.localPlaylist[idx], nil, 0, true)
}

// handleDroppedFiles opens the first playable video from dropped or selected
// paths (attaching any companion subtitle files dropped alongside it). Dropping
// a subtitle file onto an active track-capable player loads it directly.
func (a *app) handleDroppedFiles(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	var videoPath string
	var subtitles []string
	for _, p := range paths {
		if videoPath == "" && isPlayableFile(p) {
			videoPath = p
		} else if isSubtitleFile(p) {
			subtitles = append(subtitles, p)
		}
	}
	if videoPath != "" {
		return a.openLocalFileWithSubtitles(videoPath, subtitles)
	}
	if len(subtitles) > 0 && a.player != nil {
		if tp, ok := a.player.(trackCapablePlayer); ok {
			if err := tp.AddSubtitleFile(subtitles[0]); err != nil {
				a.status = "加载字幕失败：" + err.Error()
				return false
			}
			a.setNotice("已加载外挂字幕：" + filepath.Base(subtitles[0]))
			a.showPlayerOSD("已加载外挂字幕")
			return true
		}
	}
	return a.openLocalFile(paths[0])
}

// openSubtitleFileDialog lets the user attach an external subtitle file to the
// currently playing track-capable mediaPlayer.
func (a *app) openSubtitleFileDialog() {
	if a.closed || a.fileDialogOpen || a.player == nil {
		return
	}
	if _, ok := a.player.(trackCapablePlayer); !ok {
		return
	}
	pick := a.pickSubtitleFile
	if pick == nil {
		if a.window == nil {
			return
		}
		win := a.window
		pick = func() ([]string, error) {
			return mygo.Dialog.Open(mygo.OpenDialogOptions{
				Parent: win,
				Title:  "选择外挂字幕文件",
				Filters: []mygo.FileFilter{{
					Name:       "字幕文件",
					Extensions: subtitleFileExtensions(),
				}},
			})
		}
	}
	a.fileDialogOpen = true
	dispatch := a.dispatch
	run := func() {
		paths, err := pick()
		apply := func() {
			a.fileDialogOpen = false
			if a.closed {
				return
			}
			if err != nil {
				a.status = "选择字幕失败：" + err.Error()
				return
			}
			if len(paths) == 0 || a.player == nil {
				return
			}
			if tp, ok := a.player.(trackCapablePlayer); ok {
				if err := tp.AddSubtitleFile(paths[0]); err != nil {
					a.status = "加载字幕失败：" + err.Error()
					return
				}
				a.setNotice("已加载外挂字幕：" + filepath.Base(paths[0]))
				a.showPlayerOSD("已加载外挂字幕")
			}
		}
		if dispatch == nil {
			apply()
		} else {
			dispatch(apply)
		}
	}
	if dispatch == nil {
		run()
	} else {
		go run()
	}
}

// openLocalFileDialog prompts the user to choose a local video file and plays
// it immediately. The native dialog blocks until dismissed, so real runs invoke
// it off the UI thread and dispatch the chosen path back onto the UI thread.
func (a *app) openLocalFileDialog() {
	if a.closed || a.fileDialogOpen {
		return
	}
	pick := a.pickLocalFile
	if pick == nil {
		if a.window == nil {
			return
		}
		win := a.window
		pick = func() ([]string, error) {
			return mygo.Dialog.Open(mygo.OpenDialogOptions{
				Parent: win,
				Title:  "打开本地视频",
				Filters: []mygo.FileFilter{{
					Name:       "视频文件",
					Extensions: playableFileExtensions(),
				}},
			})
		}
	}
	a.fileDialogOpen = true
	dispatch := a.dispatch
	run := func() {
		paths, err := pick()
		apply := func() {
			a.fileDialogOpen = false
			if a.closed {
				return
			}
			if err != nil {
				a.status = "选择文件失败：" + err.Error()
				return
			}
			if len(paths) > 0 {
				a.handleDroppedFiles(paths)
			}
		}
		if dispatch == nil {
			apply()
		} else {
			dispatch(apply)
		}
	}
	if dispatch == nil {
		run()
	} else {
		go run()
	}
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
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
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
		a.setNotice("无法识别的链接")
		return
	}
	switch action.kind {
	case "play":
		a.playDirectURL(action.play, nil)
	case "search":
		// The player view covers every page while a video plays, so
		// leave it first; the search page then shows on arrival.
		a.withLibrary(func() {
			a.leavePlayer()
			a.browser.query = action.query
			a.searchLibrary()
		})
	}
}
