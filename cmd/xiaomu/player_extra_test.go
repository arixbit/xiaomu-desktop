package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/arixbit/xiaomu-desktop/internal/subtitle"
	"github.com/egoist/mygo/ui"
)

func TestDanmakuTextLayoutCacheReusesAndPrunes(t *testing.T) {
	var s danmakuState
	s.layoutFrame = 1
	l1 := s.layoutFor("前方高能", 22)
	if l1 == nil {
		t.Fatal("expected shaped layout")
	}
	w, h := l1.Size()
	if w <= 0 || h <= 0 {
		t.Fatalf("unexpected layout size: %vx%v", w, h)
	}
	s.layoutFrame = 2
	l2 := s.layoutFor("前方高能", 22)
	if l1 != l2 {
		t.Fatal("expected cached TextLayout pointer reuse across frames")
	}
	// Font size change invalidates cached layouts.
	l3 := s.layoutFor("前方高能", 28)
	if l3 == l1 {
		t.Fatal("expected font size change to reshape layout")
	}
	// Prune removes stale off-screen layouts once cache exceeds threshold.
	s.layoutFrame = 10
	for i := range 40 {
		s.layoutFor(strings.Repeat("弹", i+1), 28)
	}
	s.layoutFrame = 11
	active := s.layoutFor("当前在屏", 28)
	s.pruneLayouts(1)
	if len(s.layouts) != 1 || s.layouts["当前在屏"].layout != active {
		t.Fatalf("expected prune to keep only active frame layouts, got %d", len(s.layouts))
	}
	// Render through danmakuOverlay to verify end-to-end TextLayout drawing.
	a := newKeyboardTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferences.Danmaku.Enabled = true
	a.danmaku.comments = []danmaku.Comment{
		{Time: time.Second, Mode: 1, Color: 0xffffff, Text: "缓存测试弹幕"},
	}
	a.refreshDanmakuTrack()
	a.player.Seek(2 * time.Second)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	if len(a.danmaku.layouts) == 0 {
		t.Fatal("danmakuOverlay did not populate TextLayout cache")
	}
	cached := a.danmaku.layouts["缓存测试弹幕"].layout
	tt.Advance(50 * time.Millisecond)
	if a.danmaku.layouts["缓存测试弹幕"].layout != cached {
		t.Fatal("danmakuOverlay reshaped identical comment on subsequent frame")
	}
}

func TestRightKeyLongPress2xSpeedAndShortTapSeek(t *testing.T) {
	a := newKeyboardTestApp(t)
	p := a.player
	p.Seek(10 * time.Second)
	tt := ui.NewTester(a.view, 1120, 760)

	// Long press Right (>= 300ms): boosts rate to 2x and shows pill without seeking +10s.
	tt.KeyDown(ui.KeyRight)
	tt.Advance(350 * time.Millisecond)
	if p.Rate() != 2.0 {
		t.Fatalf("rate during long press = %v, want 2.0", p.Rate())
	}
	if !tt.HasText("▶▶ 2× 倍速中") {
		t.Fatalf("2x boost pill missing: %v", tt.Texts())
	}
	posBeforeRelease := p.Position()
	tt.KeyUp(ui.KeyRight)
	if p.Rate() != 1.0 {
		t.Fatalf("rate after release = %v, want 1.0", p.Rate())
	}
	if p.Position()-posBeforeRelease >= 5*time.Second {
		t.Fatalf("releasing long-press unexpectedly jumped +10s: before=%v after=%v", posBeforeRelease, p.Position())
	}

	// Short press Right: seeks +10s as before.
	p.Pause()
	p.Seek(15 * time.Second)
	tt.Key(0, ui.KeyRight)
	if p.Position() != 25*time.Second || p.Rate() != 1.0 {
		t.Fatalf("short Right press: pos=%v rate=%v, want 25s / 1.0", p.Position(), p.Rate())
	}
}

func TestMiniWindowAndAlwaysOnTop(t *testing.T) {
	a := newKeyboardTestApp(t)
	tt := ui.NewTester(a.view, 1120, 760)

	// Toggle always-on-top via P shortcut.
	tt.Key(0, ui.KeyP)
	if !a.playerExtra.pinned || !tt.HasText("已置顶") {
		t.Fatalf("P shortcut did not pin window: pinned=%v texts=%v", a.playerExtra.pinned, tt.Texts())
	}
	tt.Key(0, ui.KeyP)
	if a.playerExtra.pinned {
		t.Fatal("second P did not unpin")
	}

	// Enter mini window via T shortcut: enables pinned + hides full header, shows mini HUD when paused.
	a.player.Pause()
	tt.Key(0, ui.KeyT)
	if !a.playerExtra.miniMode || !a.playerExtra.pinned {
		t.Fatalf("T did not enter pinned mini mode: mini=%v pinned=%v", a.playerExtra.miniMode, a.playerExtra.pinned)
	}
	if !tt.HasText("还原") || tt.HasText("换源与选集") {
		t.Fatalf("mini HUD mismatch: %v", tt.Texts())
	}

	// Exit mini mode via clicking 还原 button.
	if err := tt.Click("退出迷你小窗"); err != nil {
		t.Fatalf("exit mini button missing: %v", err)
	}
	if a.playerExtra.miniMode || a.playerExtra.pinned {
		t.Fatalf("exiting mini mode did not restore state: mini=%v pinned=%v", a.playerExtra.miniMode, a.playerExtra.pinned)
	}
}

func TestPlayerAspectRotationLoopStatsAndScreenshot(t *testing.T) {
	fp := newFakeTrackPlayer(t)
	shotDir := t.TempDir()
	a := &app{
		player:         fp,
		autoNext:       true,
		playingSource:  "线路",
		playingEpisode: "第1集",
		playingName:    "测试电影.mkv",
		playingFile:    true,
		playerExtra: playerExtraState{
			captureScreenshot: func() ([]byte, error) {
				return []byte("\x89PNG\r\n\x1a\nfake"), nil
			},
			screenshotDir: func() (string, error) { return shotDir, nil },
		},
	}
	t.Cleanup(a.close)

	tt := ui.NewTester(a.view, 1120, 760)

	// Aspect / crop & rotation via menu and shortcut.
	if err := tt.Click("更多播放控制"); err != nil {
		t.Fatalf("more menu missing: %v", err)
	}
	if err := tt.ChooseMenuItem("画面 · 铺满裁切"); err != nil {
		t.Fatalf("choose fill aspect failed: %v (menu=%v)", err, tt.Menu())
	}
	if fp.props["panscan"] != "1" || a.playerVideoFit() != ui.Cover {
		t.Fatalf("fill aspect not applied: props=%v fit=%v", fp.props, a.playerVideoFit())
	}

	tt.Key(0, ui.KeyR)
	if fp.props["video-rotate"] != "90" {
		t.Fatalf("video-rotate = %q, want 90", fp.props["video-rotate"])
	}

	// Single-episode loop suppresses auto-next.
	tt.Key(0, ui.KeyL)
	if !a.playerExtra.loop || !fp.loop {
		t.Fatalf("loop toggle failed: state=%v player=%v", a.playerExtra.loop, fp.loop)
	}

	// Stats overlay via I shortcut.
	tt.Key(0, ui.KeyI)
	if !a.playerExtra.statsOpen || !tt.HasText("播放信息") || !tt.HasText("硬解：videotoolbox") {
		t.Fatalf("stats overlay missing expected details: open=%v texts=%v", a.playerExtra.statsOpen, tt.Texts())
	}

	// One-click screenshot via S shortcut.
	tt.Key(0, ui.KeyS)
	entries, err := os.ReadDir(shotDir)
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".png") {
		t.Fatalf("screenshot not saved: entries=%v err=%v status=%q", entries, err, a.status)
	}
}

func TestSleepTimerDurationAndAfterEpisode(t *testing.T) {
	a := newKeyboardTestApp(t)
	tt := ui.NewTester(a.view, 1120, 760)
	if !a.player.Playing() {
		t.Fatal("player should start playing")
	}

	// 15-minute duration sleep timer sets badge and pauses playback when deadline arrives.
	a.setSleepTimerMinutes(15)
	tt.Frame()
	if !tt.HasText("定时 15:00") {
		t.Fatalf("sleep timer badge missing: %v", tt.Texts())
	}
	// Shorten deadline to 2 seconds so headless Tester only renders 2 frames.
	a.playerExtra.sleepDeadline = tt.Now().Add(2 * time.Second)
	tt.Advance(3 * time.Second)
	if a.player.Playing() || a.playerExtra.sleepMode != sleepOff {
		t.Fatalf("sleep timer did not pause playback: playing=%v mode=%v", a.player.Playing(), a.playerExtra.sleepMode)
	}

	// After-episode sleep timer pauses instead of advancing to episode 2.
	a.player.Play()
	a.setSleepAfterEpisode()
	a.player.Seek(time.Minute)
	tt.Advance(3 * time.Second)
	if a.playingEpisode != "第1集" {
		t.Fatalf("after-episode sleep timer advanced to %q", a.playingEpisode)
	}
}

func TestSubtitleDelayPrefsChineseAutoSelectAndOnlineMatch(t *testing.T) {
	// Start with English selected and Chinese available as track 2; PreferChinese should auto-select track 2.
	fp := newFakeTrackPlayer(t)
	fp.subs = []mediaTrack{
		{ID: 1, Title: "English", Lang: "en", Codec: "subrip", Selected: true},
		{ID: 2, Title: "简体中字", Lang: "zh-hans", Codec: "ass", Selected: false},
	}
	fp.activeSub = 1

	// Prepare local video file for Shooter hash + local httptest server for both Shooter and ASSRT.
	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "Sample.4K.mkv")
	if err := os.WriteFile(videoPath, make([]byte, 16384), 0o644); err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/shooter":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"Desc":  "射手匹配中字",
					"Delay": 500,
					"Files": []map[string]any{
						{"Ext": "srt", "Link": srv.URL + "/sub.srt"},
					},
				},
			})
		case "/sub.srt":
			_, _ = w.Write([]byte("1\n00:00:01,000 --> 00:00:03,000\n你好世界\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &app{
		player:          fp,
		playingFile:     true,
		playingFilePath: videoPath,
		playingName:     "Sample.4K.mkv",
		preferences:     defaultPlayerPreferences(),
		playerExtra: playerExtraState{
			subtitleCacheDir: filepath.Join(tmpDir, "subs"),
			newSubtitleClient: func(token string) *subtitle.Client {
				return &subtitle.Client{
					HTTP:           srv.Client(),
					ShooterBaseURL: srv.URL + "/shooter",
					ASSRTBaseURL:   srv.URL,
					ASSRTToken:     token,
				}
			},
		},
	}
	t.Cleanup(a.close)
	a.applySubtitlePreferences(fp)

	tt := ui.NewTester(a.view, 1120, 760)
	if fp.activeSub != 2 {
		t.Fatalf("PreferChinese did not auto-select Chinese subtitle track: activeSub=%d", fp.activeSub)
	}
	if !strings.Contains(fp.props["slang"], "zh") || fp.props["sub-scale"] != "1.00" || fp.props["sub-pos"] != "100" {
		t.Fatalf("subtitle preference props not applied: %v", fp.props)
	}

	// Bracket keys adjust sub-delay by ±0.5s.
	tt.Key(0, ui.KeyBracketRight)
	if fp.props["sub-delay"] != "0.5" {
		t.Fatalf("] did not set sub-delay +0.5: %q", fp.props["sub-delay"])
	}
	tt.Key(0, ui.KeyBracketLeft)
	if fp.props["sub-delay"] != "0.0" {
		t.Fatalf("[ did not reset sub-delay to 0.0: %q", fp.props["sub-delay"])
	}

	// Open subtitle dialog, run Shooter hash match, click candidate to download and attach.
	a.openSubtitleDialog()
	tt.Frame()
	if err := tt.Click("射手哈希自动匹配"); err != nil {
		t.Fatalf("shooter match button missing: %v (texts=%v)", err, tt.Texts())
	}
	if len(a.playerExtra.subOnline.results) != 1 {
		t.Fatalf("expected 1 shooter candidate, got %+v (msg=%q)", a.playerExtra.subOnline.results, a.playerExtra.subOnline.message)
	}
	if err := tt.Click("射手匹配中字 · 中文 · SRT · 射手"); err != nil {
		t.Fatalf("candidate button missing: %v (texts=%v)", err, tt.Texts())
	}
	if len(fp.addedSubs) != 1 || !strings.HasSuffix(fp.addedSubs[0], ".srt") {
		t.Fatalf("online subtitle not attached: added=%v msg=%q", fp.addedSubs, a.playerExtra.subOnline.message)
	}
	if fp.props["sub-delay"] != "0.5" {
		t.Fatalf("shooter delay hint not applied: %q", fp.props["sub-delay"])
	}
}
