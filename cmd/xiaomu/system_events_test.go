package main

import (
	"errors"
	"net/url"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestParseDeepLink(t *testing.T) {
	play := "https://example.com/v.mp4"
	cases := []struct {
		raw  string
		want deepLinkAction
		ok   bool
	}{
		{"xiaomu://play?url=" + url.QueryEscape(play), deepLinkAction{kind: "play", play: play}, true},
		{"xiaomu://search?wd=" + url.QueryEscape("示例剧"), deepLinkAction{kind: "search", query: "示例剧"}, true},
		{"xiaomu://search?wd=%20%20", deepLinkAction{}, false},                                 // blank keyword
		{"xiaomu://play?url=", deepLinkAction{}, false},                                        // missing url
		{"xiaomu://play?url=" + url.QueryEscape("file:///tmp/v.mp4"), deepLinkAction{}, false}, // not http(s)
		{"xiaomu://play?url=" + url.QueryEscape("javascript:alert(1)"), deepLinkAction{}, false},
		{"xiaomu://open?url=" + url.QueryEscape(play), deepLinkAction{}, false}, // unknown action
		{"https://example.com/v.mp4", deepLinkAction{}, false},                  // wrong scheme
		{"xiaomu://play?url=%zz", deepLinkAction{}, false},                      // bad encoding
	}
	for _, c := range cases {
		got, ok := parseDeepLink(c.raw)
		if ok != c.ok || got != c.want {
			t.Fatalf("parseDeepLink(%q) = %+v, %v; want %+v, %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestHandleDeepLinkPlay(t *testing.T) {
	a := &app{open: func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }}
	t.Cleanup(a.close)
	a.handleDeepLink("xiaomu://play?url=" + url.QueryEscape("https://example.com/v.mp4"))
	if a.player == nil || a.status != "正在播放" || a.playingLive || !a.playingDirect {
		t.Fatalf("play link did not start playback: player=%v status=%q live=%v direct=%v", a.player != nil, a.status, a.playingLive, a.playingDirect)
	}
}

func TestHandleDeepLinkSearch(t *testing.T) {
	a := &app{}
	t.Cleanup(a.close)
	a.library.root = &config.Config{URL: "https://example.com/config"}
	a.library.cached = map[string]*config.Config{}
	a.handleDeepLink("xiaomu://search?wd=" + url.QueryEscape("示例剧"))
	if a.route() != routeSearch || a.browser.query != "示例剧" {
		t.Fatalf("search link did not run a search: route=%v query=%q", a.route(), a.browser.query)
	}
	if len(a.searchHistory.queries) != 1 || a.searchHistory.queries[0] != "示例剧" {
		t.Fatalf("search link did not record history: %v", a.searchHistory.queries)
	}
}

func TestHandleDeepLinkInvalid(t *testing.T) {
	a := &app{}
	t.Cleanup(a.close)
	a.handleDeepLink("xiaomu://play?url=" + url.QueryEscape("file:///tmp/v.mp4"))
	if a.player != nil || a.status != "无法识别的链接" {
		t.Fatalf("invalid link changed playback: player=%v status=%q", a.player != nil, a.status)
	}
}

func TestOpenLocalFile(t *testing.T) {
	var recent []string
	a := &app{
		openLocal: func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) },
		addRecent: func(path string) error { recent = append(recent, path); return nil },
	}
	t.Cleanup(a.close)
	if !a.openLocalFile("/tmp/示例片.mp4") || a.player == nil || !a.playingFile || a.playingName != "示例片.mp4" || a.status != "正在播放" {
		t.Fatalf("local file did not play: player=%v file=%v name=%q status=%q", a.player != nil, a.playingFile, a.playingName, a.status)
	}
	if len(recent) != 1 || recent[0] != "/tmp/示例片.mp4" {
		t.Fatalf("local file was not recorded as recent: %v", recent)
	}
	if a.playingDirect && a.hasEpisodes() {
		t.Fatal("local file must not offer episodes")
	}
}

func TestOpenLocalFileRejectsOtherExtensions(t *testing.T) {
	a := &app{openLocal: func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }}
	t.Cleanup(a.close)
	if a.openLocalFile("/tmp/notes.txt") || a.player != nil {
		t.Fatalf("non-video file opened: player=%v status=%q", a.player != nil, a.status)
	}
}

func TestOpenLocalFileReportsOpenFailure(t *testing.T) {
	a := &app{openLocal: func(string) (*ui.VideoPlayer, error) { return nil, errors.New("open failed") }}
	t.Cleanup(a.close)
	if a.openLocalFile("/tmp/v.mp4") || a.player != nil || a.status == "" {
		t.Fatalf("failed open left a player or no status: player=%v status=%q", a.player != nil, a.status)
	}
}

func TestDeferUntilReadyQueuesBeforeWindow(t *testing.T) {
	a := &app{}
	t.Cleanup(a.close)
	called := false
	a.deferUntilReady(func() { called = true })
	if called {
		t.Fatal("queued work ran before the window existed")
	}
	a.window = &mygo.Window{}
	a.drainPending()
	if !called {
		t.Fatal("queued work did not run once the window existed")
	}
	called = false
	a.deferUntilReady(func() { called = true })
	if !called {
		t.Fatal("work after the window existed did not run at once")
	}
}

func TestHandleDroppedFilesPicksFirstPlayableVideo(t *testing.T) {
	var opened string
	a := &app{
		openLocal: func(path string) (*ui.VideoPlayer, error) {
			opened = path
			return ui.NewVideoPlayer(&stillVideo{})
		},
	}
	t.Cleanup(a.close)

	if a.handleDroppedFiles(nil) || a.player != nil {
		t.Fatal("empty drop must be a no-op")
	}
	if !a.handleDroppedFiles([]string{"/tmp/readme.txt", "/tmp/sub.srt", "/tmp/demo.mp4", "/tmp/other.mov"}) {
		t.Fatalf("expected playable video in drop list to open: status=%q", a.status)
	}
	if opened != "/tmp/demo.mp4" || !a.playingFile || a.playingName != "demo.mp4" {
		t.Fatalf("opened=%q playingFile=%v name=%q", opened, a.playingFile, a.playingName)
	}

	b := &app{
		openLocal: func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) },
	}
	t.Cleanup(b.close)
	if b.handleDroppedFiles([]string{"/tmp/notes.txt"}) || b.player != nil || b.status == "" {
		t.Fatalf("unsupported drop should report format hint: player=%v status=%q", b.player != nil, b.status)
	}
}

func TestOpenLocalFileUIEntryPointsAndShortcut(t *testing.T) {
	// 1. Welcome page button works without any imported source.
	var opened string
	a := &app{
		openLocal: func(path string) (*ui.VideoPlayer, error) {
			opened = path
			return ui.NewVideoPlayer(&stillVideo{})
		},
		pickLocalFile: func() ([]string, error) {
			return []string{"/tmp/welcome.mp4"}, nil
		},
	}
	t.Cleanup(a.close)

	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("打开本地视频"); err != nil {
		t.Fatalf("welcome open local button missing: %v (texts=%v)", err, tt.Texts())
	}
	if opened != "/tmp/welcome.mp4" || a.player == nil || !a.playingFile {
		t.Fatalf("welcome button did not start playback: opened=%q player=%v file=%v", opened, a.player != nil, a.playingFile)
	}

	// 2. Cmd+O works while already playing to switch to another local file.
	a.pickLocalFile = func() ([]string, error) {
		return []string{"/tmp/next.mov"}, nil
	}
	tt.Key(ui.Cmd, ui.KeyO)
	if opened != "/tmp/next.mov" || a.playingName != "next.mov" {
		t.Fatalf("Cmd+O in player did not open next file: opened=%q name=%q", opened, a.playingName)
	}

	// 3. Sidebar "打开文件" button triggers the picker from the shell too.
	a.leavePlayer()
	a.pickLocalFile = func() ([]string, error) {
		return []string{"/tmp/sidebar.m4v"}, nil
	}
	tt.Frame()
	if err := tt.Click("打开文件"); err != nil {
		t.Fatalf("sidebar open file button missing: %v (texts=%v)", err, tt.Texts())
	}
	if opened != "/tmp/sidebar.m4v" || a.playingName != "sidebar.m4v" {
		t.Fatalf("sidebar button did not open file: opened=%q name=%q", opened, a.playingName)
	}

	// 4. Picker error reports status cleanly and clears fileDialogOpen so retries work.
	a.leavePlayer()
	a.pickLocalFile = func() ([]string, error) {
		return nil, errors.New("cancelled by system")
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyO)
	if a.fileDialogOpen || a.status == "" {
		t.Fatalf("picker error state bad: fileDialogOpen=%v status=%q", a.fileDialogOpen, a.status)
	}
}
