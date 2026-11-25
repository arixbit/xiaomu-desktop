package main

import (
	"context"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mpvplayer "github.com/arixbit/mygo-player"
	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/egoist/mygo/ui"
)

func TestCleanUserURLAndDirectRTSPPlayback(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  https://example.com/a.mp4  ", "https://example.com/a.mp4"},
		{`"https://example.com/a.mp4"`, "https://example.com/a.mp4"},
		{"“https://example.com/a.mp4”", "https://example.com/a.mp4"},
		{"<rtsp://example.com:554/live>", "rtsp://example.com:554/live"},
		{"【https://example.com/config.json】", "https://example.com/config.json"},
	} {
		if got := cleanUserURL(tc.in); got != tc.want {
			t.Fatalf("cleanUserURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	var gotTarget string
	a := &app{
		preferences: playerPreferences{Rate: 1.5, Volume: 1},
		openMPV: func(target string, opts mpvplayer.OpenOptions) (mediaPlayer, error) {
			gotTarget = target
			vp, err := ui.NewVideoPlayer(&stillVideo{})
			if err != nil {
				return nil, err
			}
			return wrapSystemPlayer(vp), nil
		},
	}
	t.Cleanup(a.close)
	a.linkOpen = true
	a.rawURL = "  “rtsp://example.com:554/live/ch1”  "
	a.play()
	if a.player == nil || !a.playingDirect || !a.playingDirectMPV || !a.playingDirectLive || !a.isLiveStream() || gotTarget != "rtsp://example.com:554/live/ch1" {
		t.Fatalf("direct RTSP playback failed: player=%v direct=%v mpv=%v live=%v target=%q status=%q err=%q",
			a.player != nil, a.playingDirect, a.playingDirectMPV, a.playingDirectLive, gotTarget, a.status, a.linkErr)
	}
	if a.player.Rate() != 1 {
		t.Fatalf("direct RTSP stream retained VOD rate %v, want 1", a.player.Rate())
	}
	// Direct RTSP playback shows Live indicator, hides proxy speed, and hides VOD rate/loop controls.
	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("直播") {
		t.Fatalf("direct RTSP stream should show 直播 indicator: %v", tt.Texts())
	}
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "网速") || txt == "播放速度" {
			t.Fatalf("direct RTSP stream should hide proxy speed and VOD rate: %v", tt.Texts())
		}
	}
	if err := tt.Click("更多播放控制"); err != nil {
		t.Fatal(err)
	}
	for _, item := range tt.Menu() {
		if strings.Contains(item, "循环") || strings.Contains(item, "播完本") {
			t.Fatalf("direct RTSP stream should not offer loop or end-of-video sleep: %v", tt.Menu())
		}
	}
}

func TestDialogValidationErrorsStayInsideDialogWithoutStaleToast(t *testing.T) {
	a := &app{}
	t.Cleanup(a.close)
	tt := ui.NewTester(a.view, 1120, 760)

	// 1. Link dialog error stays inside dialog; canceling leaves no global status toast.
	if err := tt.Click("打开链接"); err != nil {
		t.Fatal(err)
	}
	if !tt.Focused("地址") {
		t.Fatal("opening link dialog should auto-focus the URL input")
	}
	if err := tt.Click("播放"); err != nil {
		t.Fatal(err)
	}
	if a.linkErr == "" || a.status != "" {
		t.Fatalf("link dialog error leaked into global status: linkErr=%q status=%q", a.linkErr, a.status)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if a.status != "" || a.linkErr != "" {
		t.Fatalf("canceling link dialog left stale error: status=%q linkErr=%q", a.status, a.linkErr)
	}

	// 2. Import dialog auto-focuses input and keeps empty-URL error inside dialog.
	a.browser.importOpen = true
	tt.Frame()
	if !tt.Focused("配置地址") {
		t.Fatal("opening import dialog should auto-focus config URL input")
	}
	a.loadConfig("")
	tt.Frame()
	if a.browser.importErr == "" || a.status != "" {
		t.Fatalf("import dialog error leaked into global status: importErr=%q status=%q", a.browser.importErr, a.status)
	}
}

func TestSingleMovieHidesUselessSidePanelAndFullscreenHidesPin(t *testing.T) {
	a := newPlayerTestApp(t)
	// Reduce to single source, single line, single episode (a movie).
	a.title.detail.Sources[0].Episodes = []catalog.Episode{{Name: "正片", ID: "one"}}
	a.playingEpisode = "正片"
	a.panelOpen = true
	tt := ui.NewTester(a.view, 1120, 760)

	if tt.HasText("选集") || tt.HasText("线路") {
		t.Fatalf("single-source single-episode movie should not show empty side panel: %v", tt.Texts())
	}
	if _, ok := tt.Find("窗口置顶"); !ok {
		t.Fatalf("windowed player should show pin button: %v", tt.Texts())
	}

	// Fullscreen hides the meaningless pin button and ignores P shortcut.
	a.setFullScreen(true)
	tt.Frame()
	if _, ok := tt.Find("窗口置顶"); ok {
		t.Fatalf("fullscreen player should hide pin button: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyP)
	if a.playerExtra.pinned {
		t.Fatal("P shortcut should not pin window while in fullscreen")
	}
}

func TestSubtitleAndDanmakuTimeOffsetShareSameDirectionAndLabels(t *testing.T) {
	if got := formatSubtitleDelay(-500 * time.Millisecond); got != "提前 0.5s" {
		t.Fatalf("formatSubtitleDelay(-0.5s) = %q, want 提前 0.5s", got)
	}
	if got := formatSubtitleDelay(1500 * time.Millisecond); got != "推迟 1.5s" {
		t.Fatalf("formatSubtitleDelay(+1.5s) = %q, want 推迟 1.5s", got)
	}
	if got := formatSubtitleDelay(0); got != "0.0s" {
		t.Fatalf("formatSubtitleDelay(0) = %q, want 0.0s", got)
	}

	// Without active subtitle tracks, subtitle popover hides useless offset controls
	// and bracket shortcuts report clearly instead of mutating delay silently.
	fp := newFakeTrackPlayer(t)
	fp.subs = nil
	fp.activeSub = 0
	a := &app{player: fp, preferences: defaultPlayerPreferences()}
	t.Cleanup(a.close)
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("切换字幕"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("当前视频无可用字幕轨") || tt.HasText("−0.5s") || tt.HasText("+0.5s") {
		t.Fatalf("empty subtitle popover should hide delay controls: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Key(0, ui.KeyBracketRight)
	if a.playerExtra.subDelay != 0 || a.playerOSD.text != "当前未开启字幕" {
		t.Fatalf("bracket key with no subtitle mutated delay=%v osd=%q", a.playerExtra.subDelay, a.playerOSD.text)
	}

	// Online video without ASSRT token auto-expands token section in subtitle dialog.
	a.openSubtitleDialog()
	tt.Frame()
	if !a.playerExtra.subOnline.tokenOpen || !tt.HasText("按片名搜索设置（需填写 ASSRT Token）") {
		t.Fatalf("online subtitle dialog should proactively show token requirement: open=%v texts=%v", a.playerExtra.subOnline.tokenOpen, tt.Texts())
	}
}

func TestTrackSwitchOSDAndSleepTimerRadioAndRotationReset(t *testing.T) {
	fp := newFakeTrackPlayer(t)
	fp.audios = []mediaTrack{
		{ID: 1, Title: "国语", Lang: "zh", Selected: true},
		{ID: 2, Title: "英语", Lang: "en", Selected: false},
	}
	fp.subs = []mediaTrack{
		{ID: 1, Title: "简体中字", Lang: "zh-hans", Selected: true},
		{ID: 2, Title: "English", Lang: "en", Selected: false},
	}
	fp.activeAudio = 1
	fp.activeSub = 1
	a := &app{player: fp, preferences: defaultPlayerPreferences()}
	t.Cleanup(a.close)
	tt := ui.NewTester(a.view, 1120, 760)

	// Switching audio track gives immediate OSD feedback.
	if err := tt.Click("切换音轨"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("英语 · English"); err != nil {
		t.Fatalf("choose audio failed: %v (menu=%v)", err, tt.Menu())
	}
	if !strings.Contains(a.playerOSD.text, "音轨：") {
		t.Fatalf("audio switch missing OSD feedback: %q", a.playerOSD.text)
	}

	// Disabling subtitle via popover switch gives immediate OSD feedback.
	if err := tt.Click("切换字幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("字幕开关"); err != nil {
		t.Fatalf("disable sub switch failed: %v (texts=%v)", err, tt.Texts())
	}
	if a.playerOSD.text != "字幕已关" {
		t.Fatalf("disable subtitle missing OSD: %q", a.playerOSD.text)
	}
	tt.Key(0, ui.KeyEscape)

	// Sleep timer selection toggles off when clicked a second time, and rotated video offers 1-click reset.
	if err := tt.Click("更多播放控制"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("定时关闭", "30 分钟"); err != nil {
		t.Fatalf("choose sleep 30m failed: %v", err)
	}
	if a.playerExtra.sleepMode != sleepDuration || a.playerExtra.sleepMinutes != 30 {
		t.Fatalf("sleep timer not set: mode=%v mins=%d", a.playerExtra.sleepMode, a.playerExtra.sleepMinutes)
	}
	if err := tt.Click("更多播放控制"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("定时关闭", "30 分钟"); err != nil {
		t.Fatalf("toggle sleep 30m off failed: %v", err)
	}
	if a.playerExtra.sleepMode != sleepOff {
		t.Fatalf("clicking active sleep timer again did not cancel it: mode=%v", a.playerExtra.sleepMode)
	}

	tt.Key(0, ui.KeyR)
	if a.playerExtra.rotation != 90 || fp.videoRotate != 90 {
		t.Fatalf("rotation = %d / player %d, want 90", a.playerExtra.rotation, fp.videoRotate)
	}
	if err := tt.Click("更多播放控制"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("恢复正常方向"); err != nil {
		t.Fatalf("1-click rotation reset missing: %v (menu=%v)", err, tt.Menu())
	}
	if a.playerExtra.rotation != 0 || fp.videoRotate != 0 {
		t.Fatalf("rotation reset failed: rot=%d player=%d", a.playerExtra.rotation, fp.videoRotate)
	}
}

func TestEscapeDisarmsArmedDangerButton(t *testing.T) {
	a := &app{nav: []routeKind{routeHistory}}
	a.history.note(historyEntry{Name: "片甲", Episode: "第1集", Position: 40, Duration: 600, Updated: time.Now()})
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("清空历史"); err != nil {
		t.Fatal(err)
	}
	if a.armed.key == "" || !tt.HasText("确认清空？") {
		t.Fatal("danger button did not arm")
	}
	tt.Key(0, ui.KeyEscape)
	if a.armed.key != "" || tt.HasText("确认清空？") {
		t.Fatalf("Escape did not disarm danger confirmation: armed=%+v texts=%v", a.armed, tt.Texts())
	}
}

func TestDetailAutoSelectsWatchedEpisodeChunk(t *testing.T) {
	a := &app{}
	eps := make([]catalog.Episode, 80)
	for i := range eps {
		eps[i] = catalog.Episode{Name: fmt.Sprintf("第%d集", i+1), ID: fmt.Sprintf("%d", i+1)}
	}
	detail := catalog.Detail{
		Name:    "长剧示例",
		Sources: []catalog.Source{{Name: "线路", Episodes: eps}},
	}
	a.history.note(historyEntry{Name: "长剧示例", Line: "线路", Episode: "第65集", Position: 120, Duration: 2400})
	if got := a.initialEpisodeChunk("长剧示例", detail, 0); got != 1 {
		t.Fatalf("initialEpisodeChunk = %d, want 1 (51-80)", got)
	}
}

func TestLiveUnfavoritingLastChannelReturnsToAllAndHidesStaleLastResume(t *testing.T) {
	a := &app{nav: []routeKind{routeLive}}
	a.live.sources = []iptv.Source{{Name: "直播A", URL: "https://example.com/a.m3u"}}
	a.live.selectedSource = 0
	a.live.channels = []iptv.Channel{
		{Name: "频道甲", Group: "新闻", Streams: []iptv.Stream{{URL: "https://example.com/a.m3u8"}}},
	}
	// Stale last-watched channel from a completely different source must not render a dead resume bar.
	a.live.state.last = &liveLastChannel{ChannelName: "已删除频道", SourceName: "旧直播源", SourceURL: "https://example.com/old.m3u"}
	tt := ui.NewTester(a.view, 1120, 760)
	if tt.HasText("上次观看 · 已删除频道") {
		t.Fatalf("stale unresumable last channel banner should stay hidden: %v", tt.Texts())
	}

	// Favorite, filter into ★ 收藏, then unfavorite the last favorite: must auto-return to All Channels.
	if err := tt.Click("收藏 频道甲"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("收藏频道"); err != nil {
		t.Fatal(err)
	}
	if a.live.group != liveGroupFavorites {
		t.Fatalf("group = %q, want favorites", a.live.group)
	}
	if err := tt.Click("取消收藏 频道甲"); err != nil {
		t.Fatal(err)
	}
	if a.live.group != "" || !tt.HasText("频道甲") {
		t.Fatalf("removing last favorite left user stranded in vanished group: group=%q texts=%v", a.live.group, tt.Texts())
	}
}

func TestRefreshConfigStaysOnSourcesPageAndNotifies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sites":[{"key":"s1","name":"示例站","type":1,"api":"/api"}]}`)
	}))
	defer srv.Close()
	a := &app{}
	t.Cleanup(a.close)
	a.loadConfig(srv.URL + "/config.json")
	a.switchTab(routeSources)
	a.refreshConfig(srv.URL + "/config.json")
	if a.route() != routeSources {
		t.Fatalf("refreshConfig kicked user away from Sources page: route=%v", a.route())
	}
	if a.status != "影视配置已刷新" {
		t.Fatalf("refreshConfig missing completion notice: %q", a.status)
	}
}

func TestSearchClearButtonAndSidebarFollowBadgeAndPlayerStateDismiss(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	// Add a followed show with a new episode so the sidebar badge appears.
	a.favorites.entries = []favoriteEntry{{
		Name:          "新更剧",
		EpisodeCount:  10,
		LatestEpisode: "第10集",
		Checked:       time.Now(),
	}}
	a.history.note(historyEntry{Name: "新更剧", Episode: "第8集", Position: 100, Duration: 600})
	tt := ui.NewTester(a.view, 1120, 760)
	if _, ok := tt.Find("追剧有新集"); !ok {
		t.Fatalf("sidebar follow tab missing new-episode badge: %v", tt.Texts())
	}

	// Typing in search hides shortcut hint and reveals one-click clear button.
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("示例")
	if tt.HasText("⌘K") || tt.HasText("Ctrl+K") {
		t.Fatalf("shortcut hint should hide while typing: %v", tt.Texts())
	}
	if err := tt.Click("清空搜索"); err != nil {
		t.Fatalf("clear search button missing: %v (texts=%v)", err, tt.Texts())
	}
	if a.browser.query != "" {
		t.Fatalf("clear search did not empty query: %q", a.browser.query)
	}

	// Persistent error in player state pill can be dismissed cleanly.
	pa := newKeyboardTestApp(t)
	pa.status = "模拟持久错误"
	ptt := ui.NewTester(pa.view, 1120, 760)
	if err := ptt.Click("关闭播放提示"); err != nil {
		t.Fatalf("dismissible player state button missing: %v (texts=%v)", err, ptt.Texts())
	}
	if pa.status != "" {
		t.Fatalf("clicking dismiss did not clear player status: %q", pa.status)
	}
}

func TestHistoryRemovedSourceShowsActionablePrimaryButton(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	a.nav = []routeKind{routeHistory}
	a.history.note(historyEntry{
		Name:      "演示电影",
		ConfigURL: "https://example.com/old-config.json",
		Episode:   "第1集",
		Position:  30,
		Duration:  600,
		Updated:   time.Now(),
	})
	tt := ui.NewTester(a.view, 1120, 760)
	if _, ok := tt.Find("继续播放 演示电影"); ok {
		t.Fatalf("unavailable history row should not show dead 继续播放 button: %v", tt.Texts())
	}
	if _, ok := tt.Find("搜片库 演示电影"); !ok {
		t.Fatalf("unavailable history row with library should promote 搜片库 button: %v", tt.Texts())
	}
	// When no library is imported at all, offer one-click 添加片源 instead.
	a.library.root = nil
	tt.Frame()
	if _, ok := tt.Find("添加片源以恢复 演示电影"); !ok {
		t.Fatalf("unavailable history row without library should offer 添加片源: %v", tt.Texts())
	}
}

func TestSubtitleDialogLayoutClearanceAndUXSnapshots(t *testing.T) {
	fp := newFakeTrackPlayer(t)
	a := &app{player: fp, preferences: defaultPlayerPreferences()}
	t.Cleanup(a.close)
	a.playerExtra.subDelay = -500 * time.Millisecond
	a.openSubtitleDialog()
	tt := ui.NewTester(a.view, 1120, 760)

	viewport, ok := tt.Find("字幕设置内容")
	if !ok {
		t.Fatal("subtitle dialog scroll viewport missing")
	}
	var prevRight float32
	for _, label := range []string{"字幕偏移 提前 2s", "字幕偏移 提前 0.5s", "字幕偏移 推迟 0.5s", "字幕偏移 推迟 2s", "重置字幕偏移"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("missing subtitle offset control %s", label)
		}
		if prevRight > 0 && r.X < prevRight-0.5 {
			t.Fatalf("subtitle offset control %s (%+v) overlaps previous right edge %.1f", label, r, prevRight)
		}
		if viewport.X+viewport.W-(r.X+r.W) < 14 {
			t.Fatalf("subtitle offset control %s (%+v) overlaps scrollbar in viewport %+v", label, r, viewport)
		}
		prevRight = r.X + r.W
	}

	dumpDir := os.Getenv("XIAOMU_UX_VERIFY_DIR")
	if dumpDir == "" {
		return
	}
	save := func(name string, tester *ui.Tester) {
		f, err := os.Create(filepath.Join(dumpDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tester.Image()); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	save("subtitle-dialog-online.png", tt)

	// Local video subtitle dialog (primary Shooter auto-match button).
	a.playingFile = true
	a.playingFilePath = "/tmp/Sample.Movie.2026.mkv"
	a.openSubtitleDialog()
	tt.Frame()
	save("subtitle-dialog-local.png", tt)

	// Side panel with series skip controls ("片头到这" / "片尾从这" + dynamic auto-next warning).
	sa := newKeyboardTestApp(t)
	sa.autoNext = false
	sa.panelOpen = true
	sa.panelTab = 1
	if err := sa.seriesSkips.set(sa.title.name, seriesSkipRule{Enabled: true, Opening: 90, Ending: 120}); err != nil {
		t.Fatal(err)
	}
	stt := ui.NewTester(sa.view, 1120, 760)
	stt.Frame()
	stt.Advance(500 * time.Millisecond)
	save("player-panel-skip.png", stt)

	// Danmaku dialog with unified time-offset labels.
	da := newPlayerTestApp(t)
	da.preferences = defaultPlayerPreferences()
	da.preferences.Danmaku.Enabled = true
	da.danmaku.offset = 5 * time.Second
	da.danmaku.open = true
	da.danmaku.message = "已加载 471 条弹幕 · 示例剧 · 第10集"
	dtt := ui.NewTester(da.view, 1120, 760)
	dtt.Frame()
	dtt.Advance(500 * time.Millisecond)
	save("danmaku-dialog.png", dtt)

	// Single-episode movie player (side panel hidden).
	ma := newPlayerTestApp(t)
	ma.title.detail.Sources[0].Episodes = []catalog.Episode{{Name: "正片", ID: "one"}}
	ma.playingEpisode = "正片"
	ma.panelOpen = true
	mtt := ui.NewTester(ma.view, 1120, 760)
	mtt.Advance(220 * time.Millisecond)
	save("player-single-movie.png", mtt)

	// Shell: sidebar follow badge + search clear button + link dialog + replace import dialog.
	_, doc := suggestionTestServer(t)
	ba := newSuggestionTestApp(t, doc)
	ba.browser.configURL = "https://example.com/config.json"
	ba.browser.query = "测试关键词"
	ba.favorites.entries = []favoriteEntry{{
		Name:          "新更剧",
		EpisodeCount:  10,
		LatestEpisode: "第10集",
		Checked:       time.Now(),
	}}
	ba.history.note(historyEntry{Name: "新更剧", Episode: "第8集", Position: 100, Duration: 600})
	btt := ui.NewTester(ba.view, 1120, 760)
	btt.Advance(220 * time.Millisecond)
	save("shell-badge-and-clear.png", btt)

	ba.linkOpen = true
	btt.Frame()
	btt.Advance(500 * time.Millisecond)
	save("link-dialog.png", btt)

	ba.linkOpen = false
	btt.Frame()
	btt.Advance(500 * time.Millisecond)
	ba.browser.importOpen = true
	btt.Frame()
	btt.Advance(500 * time.Millisecond)
	save("import-dialog-replace.png", btt)

	// Live page (favorites without clashing 置顶 badge).
	la := &app{nav: []routeKind{routeLive}}
	la.live.sources = []iptv.Source{{Name: "直播A", URL: "https://example.com/a.m3u"}}
	la.live.selectedSource = 0
	la.live.channels = []iptv.Channel{
		{Name: "新闻综合", Group: "综合", Streams: []iptv.Stream{{URL: "https://example.com/1.m3u8"}, {URL: "https://example.com/2.m3u8"}}},
		{Name: "体育频道", Group: "体育", Streams: []iptv.Stream{{URL: "https://example.com/3.m3u8"}}},
	}
	la.live.state.toggleFavorite("新闻综合")
	ltt := ui.NewTester(la.view, 1120, 760)
	ltt.Advance(220 * time.Millisecond)
	save("live-page.png", ltt)

	// Minimum-size 640x420 link dialog with advanced fields & long error.
	minApp := &app{
		linkOpen:     true,
		linkAdvanced: true,
		linkErr:      "这是一条较长的错误提示消息，用于验证最小窗口 640×420 下高级选项展开后操作按钮依然清晰可达且不被裁切。",
	}
	t.Cleanup(minApp.close)
	mintt := ui.NewTester(minApp.view, 640, 420)
	mintt.Frame()
	mintt.Advance(500 * time.Millisecond)
	save("link-dialog-640x420.png", mintt)

	// Plan B player control bar + Danmaku / Subtitle / Speed popovers + 640x420 narrow bar.
	vp4k, err := ui.NewVideoPlayer(&sizedTimedVideo{w: 3840, h: 2160})
	if err != nil {
		t.Fatal(err)
	}
	pbPlayer := newFakeTrackPlayer(t)
	pbPlayer.systemMediaPlayer.p.Close()
	pbPlayer.systemMediaPlayer = &systemMediaPlayer{p: vp4k}
	pbApp := &app{
		player:         pbPlayer,
		autoNext:       true,
		playingSource:  "线路",
		playingEpisode: "第01集",
		preferences:    defaultPlayerPreferences(),
	}
	pbApp.title = &titlePage{
		name:   "示例影片",
		failed: map[string]string{},
		detail: &catalog.Detail{
			Name: "示例影片",
			Sources: []catalog.Source{{
				Name: "线路",
				Episodes: []catalog.Episode{
					{Name: "第01集", ID: "1"},
					{Name: "第02集", ID: "2"},
				},
			}},
		},
	}
	pbApp.preferences.Danmaku.Enabled = true
	pbComments := make([]danmaku.Comment, 0, 120)
	for i := range 120 {
		pbComments = append(pbComments, danmaku.Comment{
			Time:  time.Duration(i*500) * time.Millisecond,
			Mode:  1,
			Color: 0xffffff,
			Text:  fmt.Sprintf("高能名场面 #%d", i+1),
		})
	}
	pbApp.applyDanmaku(danmaku.Episode{ID: "101", Anime: "示例影片", Name: "第01集"}, pbComments)
	pbPlayer.Seek(18 * time.Second)
	t.Cleanup(pbApp.close)

	pbtt := ui.NewTester(pbApp.view, 1120, 760)
	pbtt.Advance(240 * time.Millisecond)
	if err := pbtt.Click("弹幕"); err != nil {
		t.Fatalf("snapshot click danmaku: %v", err)
	}
	pbtt.Advance(220 * time.Millisecond)
	save("player-plan-b-danmaku-popover.png", pbtt)

	if err := pbtt.Click("切换字幕"); err != nil {
		t.Fatalf("snapshot click subtitle: %v", err)
	}
	pbtt.Advance(220 * time.Millisecond)
	save("player-plan-b-subtitle-popover.png", pbtt)

	if err := pbtt.Click("播放速度"); err != nil {
		t.Fatalf("snapshot click speed: %v", err)
	}
	pbtt.Advance(220 * time.Millisecond)
	save("player-plan-b-speed-popover.png", pbtt)

	pbApp.closePlayerPopovers()
	pbApp.title.seeds = []library.Item{
		{Entry: library.Entry{Config: &config.Config{URL: "https://example.com/a"}, Site: config.Site{Key: "a", Name: "示例站点 A"}}},
		{Entry: library.Entry{Config: &config.Config{URL: "https://example.com/b"}, Site: config.Site{Key: "b", Name: "示例站点 B"}}},
	}
	pbApp.title.item = pbApp.title.seeds[0]
	pbApp.title.detail.Sources = []catalog.Source{
		{Name: "夸克网盘", Episodes: []catalog.Episode{
			{Name: "01｜4K", ID: "1"}, {Name: "02~4K", ID: "2"}, {Name: "03~4K", ID: "3"},
			{Name: "04~4K", ID: "4"}, {Name: "05~4K", ID: "5"}, {Name: "06~4K", ID: "6"},
			{Name: "07~4K", ID: "7"}, {Name: "08~4K", ID: "8"}, {Name: "09~4K", ID: "9"},
			{Name: "10~4K", ID: "10"}, {Name: "11~4K", ID: "11"}, {Name: "12~4K", ID: "12"},
		}},
		{Name: "夸克网盘 · 2", Episodes: []catalog.Episode{{Name: "第01集", ID: "21"}}},
		{Name: "百度网盘", Episodes: []catalog.Episode{{Name: "第01集", ID: "31"}}},
		{Name: "百度网盘 · 2", Episodes: []catalog.Episode{{Name: "第01集", ID: "41"}}},
		{Name: "百度网盘 · 3", Episodes: []catalog.Episode{{Name: "第01集", ID: "51"}}},
		{Name: "百度网盘 · 4", Episodes: []catalog.Episode{{Name: "第01集", ID: "61"}}},
	}
	pbApp.playingSource = "夸克网盘"
	pbApp.playingEpisode = "12~4K"
	pbApp.panelOpen = true
	pbApp.panelTouched = true
	pbtt.Frame()
	pbtt.Advance(260 * time.Millisecond)
	save("player-panel-episodes.png", pbtt)

	pbApp.panelOpen = false
	pbApp.panelTouched = false
	pbtt.SetSize(640, 420)
	pbtt.Advance(220 * time.Millisecond)
	save("player-plan-b-640x420.png", pbtt)
}

func TestPanCleanupRequiresTwoStepConfirmationAndShowsCustomTempDir(t *testing.T) {
	a := &app{nav: []routeKind{routeSources}}
	t.Cleanup(a.close)
	a.panUI.creds = pan.Credentials{
		QuarkCookie: "k=v",
		UCCookie:    "k=v",
		TempDirName: "自定义转存目录",
	}
	tt := ui.NewTester(a.view, 1120, 760)

	// First tap must arm confirmation without running cleanup (jobPan stays idle).
	if err := tt.Click("清理临时文件"); err != nil {
		t.Fatal(err)
	}
	if a.running(jobPan) {
		t.Fatal("first tap on pan cleanup started deletion without confirmation")
	}
	if a.armed.key != "pan-cleanup-temp" || !tt.HasText("确认清理？") {
		t.Fatalf("first tap did not arm confirmation: armed=%+v texts=%v", a.armed, tt.Texts())
	}
	foundScope := false
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "自定义转存目录") && strings.Contains(txt, "夸克") && strings.Contains(txt, "UC") {
			foundScope = true
			break
		}
	}
	if !foundScope {
		t.Fatalf("armed cleanup warning did not disclose custom directory and providers: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	if a.armed.key != "" || tt.HasText("确认清理？") {
		t.Fatalf("Escape did not cancel armed pan cleanup: armed=%+v", a.armed)
	}
}

func TestLateConfigRefreshDoesNotDismantleActivePlayback(t *testing.T) {
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/refresh.json" {
			<-gate
		}
		fmt.Fprint(w, `{"sites":[{"key":"s1","name":"示例站","type":1,"api":"/api"}]}`)
	}))
	defer srv.Close()

	a := &app{}
	t.Cleanup(a.close)
	a.loadConfig(srv.URL + "/config.json")
	a.switchTab(routeSources)

	// Switch to async dispatch so refresh stays in flight while user starts playback.
	uiQ := make(chan func(), 16)
	a.dispatch = func(fn func()) { uiQ <- fn }
	a.refreshConfig(srv.URL + "/refresh.json")
	if !a.running(jobConfig) {
		t.Fatal("expected async config refresh to be in flight")
	}

	// User switches to History and starts watching a series before refresh arrives.
	a.switchTab(routeHistory)
	vp, err := ui.NewVideoPlayer(&timedVideo{})
	if err != nil {
		t.Fatal(err)
	}
	a.adoptPlayer(wrapSystemPlayer(vp))
	a.title = &titlePage{
		name: "正在追的剧",
		detail: &catalog.Detail{
			Name:    "正在追的剧",
			Sources: []catalog.Source{{Name: "线路", Episodes: []catalog.Episode{{Name: "第1集", ID: "1"}, {Name: "第2集", ID: "2"}}}},
		},
	}
	a.playingSource, a.playingEpisode = "线路", "第1集"
	a.push(routeDetail)
	origTitle := a.title

	// Release the delayed config refresh and drain its UI callback.
	close(gate)
	select {
	case fn := <-uiQ:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for async refresh callback")
	}

	if a.player == nil || a.title != origTitle || a.playingEpisode != "第1集" || a.route() != routeDetail {
		t.Fatalf("late config refresh dismantled active playback: player=%v titleSame=%v ep=%q route=%v",
			a.player != nil, a.title == origTitle, a.playingEpisode, a.route())
	}
}

func TestCancelImportAndLiveDialogsIsolateDraftsAndAbortInFlightRequests(t *testing.T) {
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.json":
			fmt.Fprint(w, `{"sites":[{"key":"a","name":"甲站","type":1,"api":"/api"}]}`)
		case "/b-slow.json":
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"sites":[{"key":"b","name":"乙站","type":1,"api":"/api"}]}`)
		case "/live-a.m3u":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,频道甲\nhttps://example.com/a.m3u8\n")
		case "/live-b-slow.m3u":
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,频道乙\nhttps://example.com/b.m3u8\n")
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	a := &app{}
	t.Cleanup(a.close)
	urlA := srv.URL + "/a.json"
	a.loadConfig(urlA)
	a.switchTab(routeSources)

	// 1. Typing URL B in 更换片源 and clicking 取消 must not mutate active configURL.
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("更换片源"); err != nil {
		t.Fatal(err)
	}
	a.browser.importURL = srv.URL + "/b-slow.json"
	a.browser.importAssets = "/tmp/draft-assets"
	tt.Frame()
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if a.browser.configURL != urlA || a.browser.assets != "" {
		t.Fatalf("canceling import dialog leaked draft into active config: url=%q assets=%q", a.browser.configURL, a.browser.assets)
	}

	// 2. Submitting delayed URL B then pressing Escape must cancel the in-flight request.
	uiQ := make(chan func(), 16)
	a.dispatch = func(fn func()) { uiQ <- fn }
	if err := tt.Click("更换片源"); err != nil {
		t.Fatal(err)
	}
	a.browser.importURL = srv.URL + "/b-slow.json"
	tt.Frame()
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	if !a.running(jobConfig) {
		t.Fatal("expected delayed config import to be running")
	}
	tt.Key(0, ui.KeyEscape)
	if a.browser.importOpen || a.running(jobConfig) {
		t.Fatalf("Escape did not close and cancel in-flight config import: open=%v running=%v", a.browser.importOpen, a.running(jobConfig))
	}
	if a.status == "加载配置…" {
		t.Fatalf("canceling import left stale loading status: %q", a.status)
	}

	// 3. Same for live-import dialog: Escape cancels in-flight live import and clears loading status.
	a.dispatch = nil
	liveA := srv.URL + "/live-a.m3u"
	a.importLive(liveA)
	a.dispatch = func(fn func()) { uiQ <- fn }
	a.openLiveDialog()
	tt.Frame()
	a.live.input = srv.URL + "/live-b-slow.m3u"
	tt.Frame()
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	if !a.running(jobLive) {
		t.Fatal("expected delayed live import to be running")
	}
	tt.Key(0, ui.KeyEscape)
	if a.live.importOpen || a.running(jobLive) || a.live.url != liveA {
		t.Fatalf("Escape did not abort in-flight live import: open=%v running=%v url=%q", a.live.importOpen, a.running(jobLive), a.live.url)
	}
	if a.status == "导入直播…" {
		t.Fatalf("canceling live import left stale loading status: %q", a.status)
	}
	close(gate)

	// 4. Failed HTTP import clears global loading status and shows error inside dialog only.
	a.dispatch = nil
	a.openImportDialog()
	tt.Frame()
	a.browser.importURL = srv.URL + "/fail"
	tt.Frame()
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	if a.browser.importErr == "" || a.status != "" {
		t.Fatalf("failed config import leaked global status: importErr=%q status=%q", a.browser.importErr, a.status)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if a.status != "" {
		t.Fatalf("closing failed config dialog revealed stale status: %q", a.status)
	}

	a.openLiveDialog()
	tt.Frame()
	a.live.input = srv.URL + "/fail"
	tt.Frame()
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	if a.live.importErr == "" || a.status != "" {
		t.Fatalf("failed live import leaked global status: importErr=%q status=%q", a.live.importErr, a.status)
	}
}

func TestAlternateLineSingleEpisodeRemainsClickable(t *testing.T) {
	a := newPlayerTestApp(t)
	a.title.detail.Sources = []catalog.Source{
		{Name: "主线", Episodes: []catalog.Episode{{Name: "第1集", ID: "1"}, {Name: "第2集", ID: "2"}, {Name: "第3集", ID: "3"}}},
		{Name: "单集线", Episodes: []catalog.Episode{{Name: "正片", ID: "movie"}}},
	}
	a.playingSource, a.playingEpisode = "主线", "第3集"
	a.panelOpen = true
	tt := ui.NewTester(a.view, 1120, 760)

	if err := tt.Click("单集线"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("正片") {
		t.Fatalf("alternate line with sole differently-named episode hid its only episode: %v", tt.Texts())
	}
}

func TestBackgroundNoticeQueuesBehindActiveUndoToast(t *testing.T) {
	a := &app{nav: []routeKind{routeHistory}}
	a.history.note(historyEntry{Name: "片甲", Episode: "第1集", Position: 40, Duration: 600, Updated: time.Now()})
	a.history.note(historyEntry{Name: "片乙", Episode: "第2集", Position: 80, Duration: 600, Updated: time.Now()})
	tt := ui.NewTester(a.view, 1120, 760)

	if err := tt.Click("清空历史"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确认清空？"); err != nil {
		t.Fatal(err)
	}
	// Background completion fires during the 5-second Undo window.
	a.setNotice("测速完成，3 个站点均可用")
	tt.Frame()
	if !tt.HasText("撤销") || !tt.HasText("已清空播放历史") {
		t.Fatalf("background notice erased active Undo toast: %v", tt.Texts())
	}
	// If user lets Undo expire without clicking, the queued notice appears next.
	tt.Advance(undoNoticeDuration + 100*time.Millisecond)
	if a.status != "测速完成，3 个站点均可用" {
		t.Fatalf("queued notice did not surface after undo expired: status=%q", a.status)
	}
}

func TestStalePanLoginCompletionDoesNotInterruptNewTitle(t *testing.T) {
	a := &app{}
	t.Cleanup(a.close)
	titleA := &titlePage{name: "片A", failed: map[string]string{}}
	titleB := &titlePage{name: "片B", failed: map[string]string{}}
	a.title = titleA
	retried := false
	originTitle := titleA
	a.promptPanLogin(pan.ProviderQuark, "需要登录", func() {
		if a.title != originTitle {
			return
		}
		retried = true
	})
	// User navigates to title B before finishing login for title A.
	a.title = titleB
	if a.panUI.onAuthSuccess != nil {
		a.panUI.onAuthSuccess()
	}
	if retried || a.title != titleB {
		t.Fatalf("stale login callback interrupted title B: retried=%v title=%v", retried, a.title)
	}
}

func TestSubtitleDelayGuardsUnselectedTracksFallbackAndNoTokenSearch(t *testing.T) {
	// 1. Tracks exist and SubtitleVisible() is true, but none is Selected
	// (mirrors mygo-player DisableSubtitleTrack after initial auto-select).
	fp := newFakeTrackPlayer(t)
	a := &app{player: fp, preferences: defaultPlayerPreferences()}
	t.Cleanup(a.close)
	tt := ui.NewTester(a.view, 1120, 760)
	for i := range fp.subs {
		fp.subs[i].Selected = false
	}
	fp.subVisible = true
	tt.Frame()
	tt.Key(0, ui.KeyBracketRight)
	if a.playerExtra.subDelay != 0 || a.playerOSD.text != "当前未开启字幕" {
		t.Fatalf("unselected subtitle tracks allowed delay change: delay=%v osd=%q", a.playerExtra.subDelay, a.playerOSD.text)
	}

	// 2. Fallback system player (non-trackCapablePlayer) must not mutate subDelay.
	vp, err := ui.NewVideoPlayer(&timedVideo{})
	if err != nil {
		t.Fatal(err)
	}
	fa := &app{player: wrapSystemPlayer(vp), preferences: defaultPlayerPreferences()}
	t.Cleanup(fa.close)
	ftt := ui.NewTester(fa.view, 1120, 760)
	ftt.Key(0, ui.KeyBracketRight)
	if fa.playerExtra.subDelay != 0 {
		t.Fatalf("fallback player mutated subDelay: %v", fa.playerExtra.subDelay)
	}

	// 3. Movie loop OSD and sleep badge match menu wording.
	fa.toggleLoop(fa.player)
	if fa.playerOSD.text != "循环播放已开" {
		t.Fatalf("single movie loop OSD = %q, want 循环播放已开", fa.playerOSD.text)
	}
	fa.setSleepAfterEpisode()
	if got := fa.sleepTimerBadge(time.Now()); got != "定时 · 播完本片" {
		t.Fatalf("single movie sleep badge = %q, want 定时 · 播完本片", got)
	}

	// 4. Online subtitle dialog without ASSRT token omits local-match hint and keeps search disabled.
	a.playingFile = false
	a.openSubtitleDialog()
	tt.Frame()
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "上方「自动匹配字幕」") {
			t.Fatalf("online video subtitle dialog mentioned absent local-match button: %v", tt.Texts())
		}
	}
}

func TestLiveRemovedChannelFromLoadedSourceAndMissingPendingResume(t *testing.T) {
	a := &app{nav: []routeKind{routeLive}}
	a.live.sources = []iptv.Source{
		{Name: "直播A", URL: "https://example.com/a.m3u"},
		{Name: "直播B", URL: "https://example.com/b.m3u"},
	}
	a.live.selectedSource = 0
	a.live.channels = []iptv.Channel{
		{Name: "现有频道", Group: "新闻", Streams: []iptv.Stream{{URL: "https://example.com/1.m3u8"}}},
	}
	// Channel was removed from the ALREADY-LOADED source A: must not show dead resume bar.
	a.live.state.last = &liveLastChannel{ChannelName: "已删频道", SourceName: "直播A", SourceURL: "https://example.com/a.m3u"}
	if a.canResumeLastLiveChannel() {
		t.Fatal("canResumeLastLiveChannel returned true for channel absent from already-loaded source")
	}

	// Pending resume onto another source that lacks the channel must notify instead of failing silently.
	a.live.pendingResume = "不存在频道"
	a.tryPendingLiveResume()
	if !strings.Contains(a.status, "不存在频道") {
		t.Fatalf("missing pending live resume failed silently: status=%q", a.status)
	}
}

func TestSearchWithOnlyExcludedSitesDoesNotRecommendRetry(t *testing.T) {
	a := &app{nav: []routeKind{routeHome, routeSearch}}
	a.library.search.query = "冷门影片"
	a.library.search.progress = library.Event{Completed: 2, Total: 2, Skipped: 4, SkippedHealth: 0}
	tt := ui.NewTester(a.view, 1120, 760)
	if tt.HasText("重试全部站点") || strings.Contains(a.library.search.summary(), "暂不可用") {
		t.Fatalf("intentional exclusions triggered useless retry UI: summary=%q texts=%v", a.library.search.summary(), tt.Texts())
	}

	// Retryable health-skipped sites DO offer retry.
	a.library.search.progress.SkippedHealth = 1
	tt.Frame()
	if !tt.HasText("重试全部站点") || !strings.Contains(a.library.search.summary(), "1 个站点暂不可用") {
		t.Fatalf("health-skipped sites did not offer retry: summary=%q texts=%v", a.library.search.summary(), tt.Texts())
	}
}

func TestFollowingRefreshCountsCheckedSkippedAndFailedAccurately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("ids") {
		case "ok-1":
			fmt.Fprint(w, `{"list":[{"vod_id":"ok-1","vod_name":"正常剧","vod_play_from":"线路","vod_play_url":"第1集$http://example.com/1.mp4#第2集$http://example.com/2.mp4#第3集$http://example.com/3.mp4"}]}`)
		case "bad-1":
			http.Error(w, "upstream down", http.StatusInternalServerError)
		default:
			fmt.Fprint(w, `{"list":[]}`)
		}
	}))
	defer srv.Close()

	doc := &config.Config{
		URL:   srv.URL + "/cfg.json",
		Sites: []config.Site{{Key: "s1", Name: "示例站", Type: 1, Kind: config.KindJSON, API: srv.URL + "/api"}},
	}
	a := &app{}
	t.Cleanup(a.close)
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}

	// 1. All favorites belong to a removed configuration: must not claim "已检查，暂无新集".
	a.favorites.entries = []favoriteEntry{
		{Name: "旧片源剧", ConfigURL: "https://example.com/removed.json", EpisodeCount: 5, LatestEpisode: "第5集"},
	}
	a.refreshFollowingMode(true)
	if !strings.Contains(a.status, "无可检查") {
		t.Fatalf("all-skipped refresh misreported status: %q", a.status)
	}

	// 2. Mixed run: 1 checked with new episode + 1 failed check reports BOTH counts.
	a.favorites.entries = []favoriteEntry{
		{Name: "正常剧", ConfigURL: doc.URL, SiteKey: "s1", VideoID: "ok-1", EpisodeCount: 1, LatestEpisode: "第1集"},
		{Name: "故障剧", ConfigURL: doc.URL, SiteKey: "s1", VideoID: "bad-1", EpisodeCount: 1, LatestEpisode: "第1集"},
	}
	a.history.note(historyEntry{Name: "正常剧", Episode: "第1集", Position: 60, Duration: 600})
	a.refreshFollowingMode(true)
	if !strings.Contains(a.status, "1 部有新集") || !strings.Contains(a.status, "1 部检查失败") {
		t.Fatalf("mixed refresh did not report both new and failed counts: %q", a.status)
	}
}

func TestMinimumWindow640x420LinkDialogReachableAndClickable(t *testing.T) {
	a := &app{
		linkOpen:     true,
		linkAdvanced: true,
		linkErr:      "这是一条很长的错误提示，用于测试在 640×420 最小窗口下展开高级选项后，取消和播放按钮依然在可视区域内、可点击且可通过 Tab 到达。",
	}
	t.Cleanup(a.close)
	tt := ui.NewTester(a.view, 640, 420)
	tt.Frame()
	tt.Advance(500 * time.Millisecond)

	for _, label := range []string{"取消", "播放"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("missing button %s at 640x420: %v", label, tt.Texts())
		}
		if r.X < 0 || r.Y < 0 || r.X+r.W > 640 || r.Y+r.H > 420 || r.W <= 0 || r.H <= 0 {
			t.Fatalf("button %s overflows 640x420 viewport: %+v", label, r)
		}
	}
	// Verify Tab can reach 取消 and 播放 inside the dialog.
	reachedCancel, reachedPlay := false, false
	for range 12 {
		tt.Key(0, ui.KeyTab)
		if tt.Focused("取消") {
			reachedCancel = true
		}
		if tt.Focused("播放") {
			reachedPlay = true
		}
	}
	if !reachedCancel || !reachedPlay {
		t.Fatalf("Tab navigation at 640x420 could not reach action buttons: cancel=%v play=%v", reachedCancel, reachedPlay)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("clicking 取消 at 640x420 failed: %v", err)
	}
	if a.linkOpen {
		t.Fatal("clicking 取消 at 640x420 did not close link dialog")
	}
}

func TestRound2GPTReviewEdgeCases(t *testing.T) {
	// 1. Late refresh arriving while jobPlay is still resolving a history resume must not clear title/cancel jobPlay.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cfg.json":
			fmt.Fprint(w, `{"sites":[{"key":"s1","name":"示例站","type":1,"api":"/api"}]}`)
		case "/empty.json":
			fmt.Fprint(w, `{}`)
		case "/api":
			fmt.Fprint(w, `{"list":[{"vod_id":"z1","vod_name":"零集剧","vod_play_from":"线路","vod_play_url":""}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &app{}
	t.Cleanup(a.close)
	a.loadConfig(srv.URL + "/cfg.json")
	a.browser.assets = "/original/assets"

	// Simulate an in-flight jobPlay from History before player is adopted.
	playGate := make(chan struct{})
	uiQ := make(chan func(), 16)
	a.dispatch = func(fn func()) { uiQ <- fn }
	a.title = &titlePage{name: "恢复中的剧", failed: map[string]string{}}
	origTitle := a.title
	a.run(jobPlay, "正在加载", func(ctx context.Context) (jobResult, error) {
		select {
		case <-playGate:
		case <-ctx.Done():
			return jobResult{}, ctx.Err()
		}
		return jobResult{}, nil
	}, func(error) {})
	a.applyLibraryConfigMode(srv.URL+"/cfg.json", a.library.root, nil, nil, nil, true)
	if a.title != origTitle || !a.running(jobPlay) {
		t.Fatalf("late refresh canceled in-flight history resume: titleSame=%v runningPlay=%v", a.title == origTitle, a.running(jobPlay))
	}
	close(playGate)
	a.dispatch = nil

	// 2. Importing `{}` from dialog must NOT overwrite active assets and keeps error inside dialog.
	tt := ui.NewTester(a.view, 1120, 760)
	a.openImportDialog()
	tt.Frame()
	a.browser.importURL = srv.URL + "/empty.json"
	a.browser.importAssets = "/wrong/draft/assets"
	tt.Frame()
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	if a.browser.assets != "/original/assets" || !a.browser.importOpen || a.browser.importErr == "" {
		t.Fatalf("empty config import corrupted assets or dismissed error: assets=%q open=%v err=%q",
			a.browser.assets, a.browser.importOpen, a.browser.importErr)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}

	// 3. Failed background refresh preserves active bulk-history Undo toast and surfaces error after Undo expires.
	a.switchTab(routeHistory)
	a.history.note(historyEntry{Name: "片甲", Episode: "第1集", Position: 30, Duration: 600, Updated: time.Now()})
	tt.Frame()
	if err := tt.Click("清空历史"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确认清空？"); err != nil {
		t.Fatal(err)
	}
	a.refreshConfig(srv.URL + "/missing-404")
	tt.Frame()
	if !tt.HasText("撤销") {
		t.Fatalf("failed background refresh wiped active Undo toast: status=%q texts=%v", a.status, tt.Texts())
	}
	tt.Advance(undoNoticeDuration + 100*time.Millisecond)
	if !strings.Contains(a.status, "加载配置失败") {
		t.Fatalf("queued background refresh error did not surface after Undo expired: status=%q", a.status)
	}
	a.clearStatus()

	// 4. Pasted RTSP failure must NOT trigger IPTV channel failover even when IPTV channels are loaded.
	a.live.channels = []iptv.Channel{
		{Name: "无关频道", Streams: []iptv.Stream{{URL: "https://example.com/ch1.m3u8"}, {URL: "https://example.com/ch2.m3u8"}}},
	}
	a.live.playingChannel = 0
	a.playingLive = false
	a.playingDirect = true
	a.playingDirectLive = true
	a.live.started = time.Now().Add(-time.Minute)
	vp, err := ui.NewVideoPlayer(&stillVideo{})
	if err != nil {
		t.Fatal(err)
	}
	p := wrapSystemPlayer(vp)
	a.player = p
	a.advanceLive(p)
	if a.playingLive {
		t.Fatal("failed pasted RTSP stream triggered IPTV channel failover")
	}
	a.closeCurrentPlayer()

	// 5. Same-name sources with different URLs OR inline (empty URL) must NOT expose dead resume.
	a.live.sources = []iptv.Source{
		{Name: "IPTV 直播", URL: "https://example.com/list-1.m3u"},
		{Name: "IPTV 直播", URL: "https://example.com/list-2.m3u"},
		{Name: "IPTV 直播", URL: ""},
	}
	a.live.selectedSource = 0
	a.live.channels = []iptv.Channel{{Name: "现存频道", Streams: []iptv.Stream{{URL: "https://example.com/1.m3u8"}}}}
	a.live.state.last = &liveLastChannel{ChannelName: "已删频道", SourceName: "IPTV 直播", SourceURL: "https://example.com/list-1.m3u"}
	if a.canResumeLastLiveChannel() {
		t.Fatal("same-name different-URL or inline source matched dead last-watched channel")
	}

	// 6. Zero-episode follow-refresh response must NOT count as checked or announce stale updates.
	a.favorites.entries = []favoriteEntry{
		{Name: "零集剧", ConfigURL: srv.URL + "/cfg.json", SiteKey: "s1", VideoID: "z1", EpisodeCount: 10, LatestEpisode: "第10集"},
	}
	a.history.note(historyEntry{Name: "零集剧", Episode: "第2集", Position: 60, Duration: 600})
	a.refreshFollowingMode(true)
	if strings.Contains(a.status, "有新集") {
		t.Fatalf("zero-episode response falsely announced stale new episode: %q", a.status)
	}
}
