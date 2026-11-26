package main

import (
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

func TestHistorySingleRemoveSupportsUndoAndBulkClearRequiresTwoSteps(t *testing.T) {
	a := &app{nav: []routeKind{routeHistory}}
	a.history.note(historyEntry{Name: "片甲", Episode: "第1集", Position: 40, Duration: 600, Updated: time.Now()})
	a.history.note(historyEntry{Name: "片乙", Episode: "第2集", Position: 80, Duration: 600, Updated: time.Now()})
	tt := ui.NewTester(a.view, 1120, 760)

	// Single remove deletes immediately and provides an Undo toast.
	if err := tt.Click("移除 片乙"); err != nil {
		t.Fatal(err)
	}
	if len(a.history.entries) != 1 || a.history.entries[0].Name != "片甲" {
		t.Fatalf("single remove did not delete entry: %+v", a.history.entries)
	}
	if !tt.HasText("撤销") {
		t.Fatalf("undo button missing after single remove: %v", tt.Texts())
	}
	if err := tt.Click("撤销"); err != nil {
		t.Fatal(err)
	}
	if len(a.history.entries) != 2 || a.history.entries[0].Name != "片乙" {
		t.Fatalf("undo did not restore removed history entry: %+v", a.history.entries)
	}

	// Restoring an already-present title must not duplicate entries.
	a.history.restore(0, historyEntry{Name: "片乙"})
	if len(a.history.entries) != 2 {
		t.Fatalf("historyStore.restore created a duplicate entry: %+v", a.history.entries)
	}

	// Direct a.status error overwrites must not inherit a stale Undo button or TTL.
	a.setUndoNotice("临时提示", "撤销", func() {})
	a.status = "独立错误消息"
	tt.Frame()
	if tt.HasText("撤销") {
		t.Fatalf("direct error status inherited stale undo button: %v", tt.Texts())
	}
	tt.Advance(undoNoticeDuration + time.Second)
	if a.status != "独立错误消息" {
		t.Fatalf("direct error status auto-dismissed with stale TTL: %q", a.status)
	}
	a.clearStatus()
	tt.Frame()

	// Bulk clear requires two taps within TTL; waiting past TTL resets it safely.
	if err := tt.Click("清空历史"); err != nil {
		t.Fatal(err)
	}
	if len(a.history.entries) != 2 || !tt.HasText("确认清空？") {
		t.Fatalf("first tap should arm confirmation without clearing: entries=%d texts=%v", len(a.history.entries), tt.Texts())
	}
	tt.Advance(armedConfirmTTL + 500*time.Millisecond)
	if !tt.HasText("清空历史") || tt.HasText("确认清空？") {
		t.Fatalf("armed confirmation did not auto-reset after timeout: %v", tt.Texts())
	}
	if err := tt.Click("清空历史"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确认清空？"); err != nil {
		t.Fatal(err)
	}
	if len(a.history.entries) != 0 || !tt.HasText("还没有播放记录") {
		t.Fatalf("confirmed clear did not empty history: entries=%d", len(a.history.entries))
	}
	// Bulk clear also offers a 5-second Undo button to restore all entries.
	if !tt.HasText("撤销") {
		t.Fatalf("bulk clear missing undo action: %v", tt.Texts())
	}
	if err := tt.Click("撤销"); err != nil {
		t.Fatal(err)
	}
	if len(a.history.entries) != 2 {
		t.Fatalf("bulk clear undo did not restore all entries: %+v", a.history.entries)
	}
	// Confirm clearing again and verify undo notice auto-dismisses cleanly.
	if err := tt.Click("清空历史"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确认清空？"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(undoNoticeDuration + 500*time.Millisecond)
	if a.status != "" {
		t.Fatalf("transient undo notice did not auto-dismiss: %q", a.status)
	}
}

func TestPlayerVODLoadingNeverShowsLiveAndLocalHidesNetworkSpeed(t *testing.T) {
	a := newPlayerTestApp(t) // stillVideo has Duration == 0
	a.playingLive = false
	a.playingFile = false
	tt := ui.NewTester(a.view, 1120, 760)
	if tt.HasText("直播") {
		t.Fatalf("VOD with unknown duration falsely displayed 直播: %v", tt.Texts())
	}
	if !tt.HasText("加载中…") {
		t.Fatalf("VOD with unknown duration missing loading indicator: %v", tt.Texts())
	}

	a.playingFile = true
	a.playingName = "local-demo.mp4"
	tt.Frame()
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "网速") {
			t.Fatalf("local file playback should hide network speed: %v", tt.Texts())
		}
	}
}

func TestPlayerMuteVolumeFullscreenShortcutsAndOSD(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.player.SetVolume(0.8)
	a.panelOpen = true
	tt := ui.NewTester(a.view, 1120, 760)

	// Windowed Esc closes side panel without closing player.
	tt.Key(0, ui.KeyEscape)
	if a.panelOpen || a.player == nil {
		t.Fatalf("Esc did not close side panel cleanly: panelOpen=%v player=%v", a.panelOpen, a.player != nil)
	}

	// M toggles mute and preserves volume on unmute, showing OSD.
	tt.Key(0, ui.KeyM)
	if !a.muted || !tt.HasText("已静音") {
		t.Fatalf("M did not mute with OSD: muted=%v texts=%v", a.muted, tt.Texts())
	}
	// Pressing Down while muted must keep playback muted.
	tt.Key(0, ui.KeyDown)
	if !a.muted {
		t.Fatal("KeyDown while muted unexpectedly unmuted playback")
	}
	tt.Key(0, ui.KeyM)
	if a.muted || a.player.Volume() < 0.79 {
		t.Fatalf("second M did not restore volume: muted=%v vol=%v", a.muted, a.player.Volume())
	}

	// Up/Down adjust volume by 5%.
	tt.Key(0, ui.KeyDown)
	if got := a.player.Volume(); got < 0.74 || got > 0.76 {
		t.Fatalf("Down key volume = %v, want ~0.75", got)
	}
	tt.Key(0, ui.KeyUp)
	if got := a.player.Volume(); got < 0.79 || got > 0.81 {
		t.Fatalf("Up key volume = %v, want ~0.80", got)
	}

	// F toggles fullscreen.
	tt.Key(0, ui.KeyF)
	if !a.fullScreen {
		t.Fatal("F key did not enter fullscreen")
	}
	tt.Key(0, ui.KeyF)
	if a.fullScreen {
		t.Fatal("second F key did not exit fullscreen")
	}

	// OSD auto-dismisses after duration.
	tt.Advance(playerOSDDuration + 200*time.Millisecond)
	if a.playerOSD.text != "" {
		t.Fatalf("OSD did not auto-dismiss: %q", a.playerOSD.text)
	}
}

func TestUpNextPillAllowsCancelingAutoNext(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.autoNext = true
	a.player.Seek(55 * time.Second) // 5s remaining in 60s episode
	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("暂不连播") {
		t.Fatalf("up-next pill missing near episode end: %v", tt.Texts())
	}
	// Pausing hides the up-next pill so it does not tick while paused.
	a.player.Pause()
	tt.Frame()
	if tt.HasText("暂不连播") {
		t.Fatalf("up-next pill remained visible while paused: %v", tt.Texts())
	}
	a.player.Play()
	tt.Frame()
	if err := tt.Click("取消自动连播"); err != nil {
		t.Fatal(err)
	}
	if !a.autoNextCanceled {
		t.Fatal("canceling up-next did not set autoNextCanceled")
	}
	a.player.Seek(a.player.Duration())
	a.advanceEpisode(a.player, time.Now())
	if a.autoNextPending {
		t.Fatal("canceled auto-next still advanced episode")
	}
}

func TestOpeningSkipOffersUndoPill(t *testing.T) {
	for _, full := range []bool{false, true} {
		name := "windowed"
		if full {
			name = "fullscreen"
		}
		t.Run(name, func(t *testing.T) {
			a := newKeyboardTestApp(t)
			a.fullScreen = full
			if err := a.seriesSkips.set(a.title.name, seriesSkipRule{Enabled: true, Opening: 12}); err != nil {
				t.Fatal(err)
			}
			a.player.Pause()
			tt := ui.NewTester(a.view, 1280, 720)
			tt.Frame()
			if !tt.HasText("已跳过片头 12 秒") || !tt.HasText("回看片头") {
				t.Fatalf("opening skip undo pill missing skipped seconds: %v", tt.Texts())
			}
			video, okV := tt.Find("视频画面")
			labelR, okL := tt.Find("已跳过片头 12 秒")
			btnR, okB := tt.Find("回看片头")
			dock, okD := tt.Find("播放控制条")
			if !okV || !okL || !okB || !okD {
				t.Fatalf("missing bounds: video=%v label=%v btn=%v dock=%v", okV, okL, okB, okD)
			}
			pillCenterX := (labelR.X + btnR.X + btnR.W) / 2
			videoCenterX := video.X + video.W/2
			if absf(pillCenterX-videoCenterX) > 12 {
				t.Fatalf("skip undo pill not centered horizontally over video: pillCenter=%.2f videoCenter=%.2f (label=%+v btn=%+v video=%+v)",
					pillCenterX, videoCenterX, labelR, btnR, video)
			}
			if btnR.Y+btnR.H >= dock.Y {
				t.Fatalf("skip undo pill overlapped dock: pillBottom=%.2f dockTop=%.2f", btnR.Y+btnR.H, dock.Y)
			}
			if err := tt.Click("回看片头"); err != nil {
				t.Fatal(err)
			}
			if a.player.Position() != 0 {
				t.Fatalf("clicking undo did not rewind to 0: pos=%v", a.player.Position())
			}
		})
	}
}

func TestDetailResumesWatchedEpisodeAndExpandsLongSynopsis(t *testing.T) {
	a, _ := newDetailTestApp(t, 3)
	a.title.detail.Sources[0].Episodes = []catalog.Episode{
		{Name: "第1集", ID: "1"},
		{Name: "第2集", ID: "2"},
		{Name: "第3集", ID: "3"},
	}
	a.title.detail.Content = strings.Repeat("这是一段较长的影片简介内容用于测试展开与收起功能。", 6)
	a.history.note(historyEntry{
		Name:     a.title.name,
		Episode:  "第2集",
		Position: 45,
		Duration: 600,
		Updated:  time.Now(),
	})
	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("继续播放 · 第2集") {
		t.Fatalf("detail CTA did not reflect watch history: %v", tt.Texts())
	}
	if !tt.HasText("展开简介") {
		t.Fatalf("long synopsis missing expand toggle: %v", tt.Texts())
	}
	if err := tt.Click("展开简介"); err != nil {
		t.Fatal(err)
	}
	if !a.title.synopsisExpanded || !tt.HasText("收起简介") {
		t.Fatalf("synopsis did not expand: expanded=%v", a.title.synopsisExpanded)
	}
}

func TestDiscoverSiblingSubtitlesFindsMatchingTracks(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Sample.Movie.2026.mkv")
	for _, name := range []string{
		"Sample.Movie.2026.mkv",
		"Sample.Movie.2026.srt",
		"Sample.Movie.2026.zh.ass",
		"Other.Movie.srt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stub"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := discoverSiblingSubtitles(video)
	if len(got) != 2 {
		t.Fatalf("discoverSiblingSubtitles = %v, want 2 matching files", got)
	}
	if filepath.Base(got[0]) != "Sample.Movie.2026.srt" || filepath.Base(got[1]) != "Sample.Movie.2026.zh.ass" {
		t.Fatalf("unexpected subtitle order/matches: %v", got)
	}
}

func TestSiteHealthExpiresAfterTTL(t *testing.T) {
	a := &app{}
	key := "https://example.com/config\x00site1"
	a.siteHealth.failed = map[string]time.Time{
		key: time.Now().Add(-siteHealthTTL - time.Minute),
	}
	if a.isSiteFailed(key) {
		t.Fatal("expired site failure should automatically recover")
	}
	if snap := a.siteHealth.snapshotKeys(); len(snap) != 0 {
		t.Fatalf("expired failure leaked into snapshotKeys: %v", snap)
	}
}

func TestResponsiveThemesAndRailRenderCleanly(t *testing.T) {
	doc := &config.Config{
		URL:   "https://example.com/config.json",
		Sites: []config.Site{{Key: "s1", Name: "示例站", Type: 1, Kind: config.KindJSON, API: "https://example.com/api"}},
	}
	a := &app{}
	defer a.close()
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}
	a.library.feed.items = []library.Item{{
		Entry: library.Entry{Site: doc.Sites[0], Config: doc},
		Video: catalog.Video{ID: "1", Name: "示例影片"},
	}}
	a.library.feed.groups = groupTitles(a.library.feed.items)

	for _, tc := range []struct {
		name string
		w, h int
		dark bool
	}{
		{"rail-light", 540, 420, false},
		{"rail-dark", 540, 420, true},
		{"compact-light", 640, 420, false},
		{"compact-dark", 640, 420, true},
		{"wide-light", 1120, 760, false},
		{"wide-dark", 1120, 760, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				th := ui.LightTheme()
				if tc.dark {
					th = ui.DarkTheme()
				}
				ui.Themed(c, th).Fill().Children(func() {
					a.view(c)
				})
			}, tc.w, tc.h)
			tt.Frame()
			if _, ok := tt.Find("首页"); !ok {
				t.Fatalf("%s: sidebar home entry missing", tc.name)
			}
			if _, ok := tt.Find("片源管理"); !ok {
				t.Fatalf("%s: sidebar sources entry missing", tc.name)
			}
			upd, ok := tt.Find("检查更新")
			if !ok || upd.Y+upd.H > float32(tc.h)-10 {
				t.Fatalf("%s: sidebar update button overflows window: %+v (h=%d)", tc.name, upd, tc.h)
			}
			if dumpDir := os.Getenv("XIAOMU_VISUAL_VERIFY_DIR"); dumpDir != "" {
				f, err := os.Create(filepath.Join(dumpDir, tc.name+".png"))
				if err != nil {
					t.Fatal(err)
				}
				if err := png.Encode(f, tt.Image()); err != nil {
					f.Close()
					t.Fatal(err)
				}
				_ = f.Close()
			}
		})
	}

	if dumpDir := os.Getenv("XIAOMU_VISUAL_VERIFY_DIR"); dumpDir != "" {
		// Also capture Cinema Player (HUD + OSD + Up-Next pill + Panel) and History Undo/Armed state.
		pa := newKeyboardTestApp(t)
		pa.autoNext = true
		pa.panelOpen = true
		pa.player.Seek(55 * time.Second)
		pa.showPlayerOSD("音量 80%")
		ptt := ui.NewTester(pa.view, 1120, 760)
		ptt.Advance(250 * time.Millisecond)
		if f, err := os.Create(filepath.Join(dumpDir, "player-cinema.png")); err == nil {
			_ = png.Encode(f, ptt.Image())
			_ = f.Close()
		}

		ha := &app{nav: []routeKind{routeHistory}}
		ha.history.note(historyEntry{Name: "示例剧集", Episode: "第4集", Position: 620, Duration: 2400, Origin: "高速站点", Updated: time.Now()})
		ha.setUndoNotice("已移除「旧剧」", "撤销", func() {})
		htt := ui.NewTester(ha.view, 1120, 760)
		_ = htt.Click("清空历史")
		htt.Advance(220 * time.Millisecond)
		if f, err := os.Create(filepath.Join(dumpDir, "history-undo-armed.png")); err == nil {
			_ = png.Encode(f, htt.Image())
			_ = f.Close()
		}
	}
}

func TestMarqueeHonorsReduceMotion(t *testing.T) {
	a := &app{}
	defer a.close()
	hovered := true
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(100).Height(32).Children(func() {
			a.hoverMarqueeText(c, marqueeTextOptions{
				key:        "rm-marquee",
				text:       "这是一段非常非常长的剧集标题用来验证减少动态效果时不会自动滚动",
				fontSize:   13,
				color:      ui.RGB(255, 255, 255),
				hovered:    hovered,
				knownWidth: 100,
			})
		})
	}, 200, 60)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Advance(marqueeLeadPause + 2*time.Second)
	if a.marquee.lastOffset != 0 {
		t.Fatalf("marquee scrolled under ReduceMotion: lastOffset=%.2f", a.marquee.lastOffset)
	}
}

func TestDetailRoutePreservesPersistentShellAndBackButton(t *testing.T) {
	a, _ := newDetailTestApp(t, 4)
	a.nav = []routeKind{routeHome}
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	searchHome, ok := tt.Find("片名")
	if !ok {
		t.Fatal("home search input missing")
	}

	// Push into detail route.
	a.push(routeDetail)
	tt.Frame()
	if _, ok := tt.Find("返回"); !ok {
		t.Fatalf("detail route missing back button: %v", tt.Texts())
	}
	searchDetail, ok := tt.Find("片名")
	if !ok {
		t.Fatal("detail search input missing")
	}
	if absf(searchDetail.X-searchHome.X) > 2 || absf(searchDetail.Y-searchHome.Y) > 2 {
		t.Fatalf("search bar jumped across routeDetail push: home=%+v detail=%+v", searchHome, searchDetail)
	}

	// Pop back to home via Back button click.
	if err := tt.Click("返回"); err != nil {
		t.Fatalf("click back from detail: %v", err)
	}
	if a.route() != routeHome {
		t.Fatalf("back button did not return to routeHome: route=%v", a.route())
	}
	tt.Advance(320 * time.Millisecond)
	searchAfter, ok := tt.Find("片名")
	if !ok || absf(searchAfter.X-searchHome.X) > 2 || absf(searchAfter.Y-searchHome.Y) > 2 {
		t.Fatalf("search bar drifted after returning home: home=%+v after=%+v", searchHome, searchAfter)
	}
}

func TestFailureCopyFeedbackAutoResetsAndDetailsDisclose(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.player = &unknownSizeMediaPlayer{err: errors.New("403 forbidden stream")}
	a.playFailure = &episodeFailure{
		err:     errors.New("403 forbidden stream"),
		line:    "主线路",
		episode: catalog.Episode{Name: "第1集", ID: "1"},
	}
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()

	if err := tt.Click("错误详情"); err != nil {
		t.Fatalf("expand failure details: %v", err)
	}
	if !a.failureUI.detailsOpen || !tt.HasText("复制") {
		t.Fatalf("failure details did not reveal copy button: open=%v texts=%v", a.failureUI.detailsOpen, tt.Texts())
	}
	if err := tt.Click("复制"); err != nil {
		t.Fatalf("click copy diagnostics: %v", err)
	}
	if !a.failureUI.copied || !tt.HasText("已复制") {
		t.Fatalf("copy action did not show 已复制 feedback: copied=%v texts=%v", a.failureUI.copied, tt.Texts())
	}
	tt.Advance(failureCopyResetDelay + 200*time.Millisecond)
	if a.failureUI.copied || !tt.HasText("复制") {
		t.Fatalf("copy confirmation did not auto-reset after delay: copied=%v texts=%v", a.failureUI.copied, tt.Texts())
	}
}

func TestSlidingSegmentedSwitchesCleanlyAndRespectsReduceMotion(t *testing.T) {
	for _, reduceMotion := range []bool{false, true} {
		name := "motion"
		if reduceMotion {
			name = "reduce-motion"
		}
		t.Run(name, func(t *testing.T) {
			a := newKeyboardTestApp(t)
			a.preferences = defaultPlayerPreferences()
			a.preferences.Danmaku.Enabled = true
			tt := ui.NewTester(a.view, 1120, 760)
			tt.SetPreferences(ui.Preferences{ReduceMotion: reduceMotion})
			tt.Frame()
			if err := tt.Click("弹幕"); err != nil {
				t.Fatalf("open danmaku popover: %v", err)
			}
			if err := tt.Click("稀疏"); err != nil {
				t.Fatalf("select low density: %v", err)
			}
			if a.preferences.Danmaku.Density != "low" {
				t.Fatalf("density = %q, want low", a.preferences.Danmaku.Density)
			}
			tt.Advance(180 * time.Millisecond)
			if err := tt.Click("密集"); err != nil {
				t.Fatalf("select high density: %v", err)
			}
			if a.preferences.Danmaku.Density != "high" {
				t.Fatalf("density = %q, want high", a.preferences.Danmaku.Density)
			}
		})
	}
}

func TestReduceMotionRendersAllBreakpointsCleanly(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.panelOpen = true
	a.showPlayerOSD("音量 80%")
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"narrow-rail", 540, 420},
		{"compact", 640, 420},
		{"wide", 1120, 760},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := ui.NewTester(a.view, tc.w, tc.h)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.Frame()
			tt.Advance(300 * time.Millisecond)
			if _, ok := tt.Find("播放控制条"); !ok {
				t.Fatalf("%s: player dock missing under ReduceMotion", tc.name)
			}
		})
	}
}

func TestDialogBackdropClickDismissesCleanly(t *testing.T) {
	a := &app{nav: []routeKind{routeHome}}
	defer a.close()
	a.browser.importOpen = true
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	if !tt.HasText("高级选项") {
		t.Fatal("import dialog did not render")
	}
	// Clicking outside the centered 520px panel on the backdrop scrim must dismiss the dialog.
	tt.ClickAt(20, 20)
	if a.browser.importOpen {
		t.Fatal("clicking backdrop scrim did not dismiss import dialog")
	}
	tt.Advance(220 * time.Millisecond)
	if tt.HasText("高级选项") {
		t.Fatalf("import dialog remained visible after backdrop dismissal: %v", tt.Texts())
	}

	// Verify player danmaku dialog also dismisses on backdrop click.
	pa := newKeyboardTestApp(t)
	pa.danmaku.open = true
	ptt := ui.NewTester(pa.view, 1120, 760)
	ptt.Frame()
	if !ptt.HasText("弹幕设置") {
		t.Fatal("danmaku dialog did not render")
	}
	ptt.ClickAt(20, 20)
	if pa.danmaku.open {
		t.Fatal("clicking backdrop scrim did not dismiss danmaku dialog")
	}
}

func TestLivePageIdleProducesZeroRebuilds(t *testing.T) {
	a := &app{nav: []routeKind{routeLive}}
	defer a.close()
	a.live.sources = []iptv.Source{{Name: "测试直播"}}
	a.live.selectedSource = 0
	a.live.channels = []iptv.Channel{
		{Name: "CCTV-1 综合", Group: "央视", Streams: []iptv.Stream{{URL: "https://example.com/1.m3u8"}}},
	}
	a.live.state.last = &liveLastChannel{ChannelName: "CCTV-1 综合", Group: "央视"}
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Advance(400 * time.Millisecond) // allow initial reveal transitions to settle
	if tt.FramePending() {
		t.Fatal("idle live page kept requesting frames after settling")
	}
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Fatal("idle live page scheduled continuous animation frames")
	}
}

func TestScanIndicatorStaysIdleUnderReduceMotion(t *testing.T) {
	a := &app{nav: []routeKind{routeHome}}
	defer a.close()
	a.library.feed.running = true
	a.library.feed.progress = library.Event{Completed: 2, Total: 10}
	tt := ui.NewTester(func(c *ui.Context) {
		a.scanBar(c, &a.library.feed)
	}, 900, 80)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Advance(300 * time.Millisecond)
	if tt.FramePending() {
		t.Fatal("scan indicator scheduled spinner animation frames under ReduceMotion")
	}
}

func TestMuteButtonUnmutesSavedZeroVolumeInOneClick(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.muted = false
	a.savedVolume = 0
	a.player.SetVolume(0)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	if !a.isEffectivelyMuted(a.player) {
		t.Fatal("expected zero volume to report effectively muted")
	}
	if err := tt.Click("静音切换"); err != nil {
		t.Fatalf("click mute button: %v", err)
	}
	if a.muted || a.player.Volume() <= 0 {
		t.Fatalf("first click on zero-volume player did not unmute: muted=%v vol=%v", a.muted, a.player.Volume())
	}
}
