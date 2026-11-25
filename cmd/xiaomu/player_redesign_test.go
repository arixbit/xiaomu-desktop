package main

import (
	"image"
	"io"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/arixbit/xiaomu-desktop/internal/douban"
	"github.com/egoist/mygo/ui"
)

type sizedTimedVideo struct {
	w, h int
	sent bool
}

func (v *sizedTimedVideo) Next() (ui.VideoFrame, error) {
	if v.sent {
		return ui.VideoFrame{}, io.EOF
	}
	v.sent = true
	return ui.VideoFrame{Image: image.NewRGBA(image.Rect(0, 0, v.w, v.h)), Duration: time.Minute}, nil
}

func (v *sizedTimedVideo) Rewind() error         { v.sent = false; return nil }
func (*sizedTimedVideo) Duration() time.Duration { return time.Minute }

func TestFormatResolutionBadge(t *testing.T) {
	cases := []struct {
		name     string
		w, h     int
		wantText string
		wantTier resolutionTier
	}{
		{"8K", 7680, 4320, "8K", resTier8K},
		{"4K standard", 3840, 2160, "4K", resTier4K},
		{"4K ultrawide scope", 3840, 1600, "4K", resTier4K},
		{"2K QHD", 2560, 1440, "2K", resTier2K},
		{"2K ultrawide", 2560, 1080, "2K", resTier2K},
		{"1080P standard", 1920, 1080, "1080P", resTier1080P},
		{"1080P ultrawide scope", 1920, 800, "1080P", resTier1080P},
		{"1080P vertical portrait", 1080, 1920, "1080P", resTier1080P},
		{"720P standard", 1280, 720, "720P", resTier720P},
		{"720P scope", 1280, 536, "720P", resTier720P},
		{"480P wide", 854, 480, "480P", resTier480P},
		{"480P PAL", 720, 576, "480P", resTier480P},
		{"360P", 640, 360, "360P", resTier360P},
		{"SD low", 320, 240, "SD", resTierSD},
		{"SD tiny", 2, 2, "SD", resTierSD},
		{"zero", 0, 0, "", resTierNone},
		{"zero width", 0, 1080, "", resTierNone},
		{"negative", -1920, 1080, "", resTierNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotText, gotTier := formatResolutionBadge(tc.w, tc.h)
			if gotText != tc.wantText || gotTier != tc.wantTier {
				t.Fatalf("formatResolutionBadge(%d, %d) = (%q, %v), want (%q, %v)",
					tc.w, tc.h, gotText, gotTier, tc.wantText, tc.wantTier)
			}
		})
	}
}

func TestPlanBPopoversMutualExclusionSwitchesAndDialogs(t *testing.T) {
	fp := newFakeTrackPlayer(t)
	a := &app{
		player:         fp,
		autoNext:       true,
		playingSource:  "线路",
		playingEpisode: "第1集",
		preferences:    defaultPlayerPreferences(),
	}
	a.title = &titlePage{
		name:   "示例剧",
		failed: map[string]string{},
		detail: &catalog.Detail{
			Name: "示例剧",
			Sources: []catalog.Source{{
				Name: "线路",
				Episodes: []catalog.Episode{
					{Name: "第1集", ID: "1"},
					{Name: "第2集", ID: "2"},
				},
			}},
		},
	}
	a.danmaku.comments = []danmaku.Comment{
		{Time: time.Second, Mode: 1, Color: 0xffffff, Text: "示例弹幕"},
	}
	t.Cleanup(a.close)

	tt := ui.NewTester(a.view, 1120, 760)

	// 1. Open Danmaku popover and toggle 显示弹幕 switch inside popover.
	if err := tt.Click("弹幕"); err != nil {
		t.Fatalf("click danmaku icon: %v", err)
	}
	if !a.playerPopovers.danmaku || !tt.HasText("显示弹幕") || !tt.HasText("换集/搜片") {
		t.Fatalf("danmaku popover did not open: popovers=%+v texts=%v", a.playerPopovers, tt.Texts())
	}
	if err := tt.Click("弹幕开关"); err != nil {
		t.Fatalf("toggle danmaku switch inside popover: %v", err)
	}
	if !a.preferences.Danmaku.Enabled {
		t.Fatal("danmaku switch inside popover did not enable danmaku")
	}
	// Quick time offset in Danmaku popover must align with Subtitle popover & full dialog:
	// left − advances ("提前"), right + delays ("推迟").
	if err := tt.Click("弹幕提前 1s"); err != nil {
		t.Fatalf("click danmaku advance -1s in popover: %v", err)
	}
	if a.danmaku.offset != time.Second || !tt.HasText("提前 1s") {
		t.Fatalf("danmaku -1s did not advance by 1s: offset=%v texts=%v", a.danmaku.offset, tt.Texts())
	}
	if err := tt.Click("弹幕推迟 1s"); err != nil {
		t.Fatalf("click danmaku delay +1s in popover: %v", err)
	}
	if a.danmaku.offset != 0 {
		t.Fatalf("danmaku +1s did not cancel advance: offset=%v", a.danmaku.offset)
	}

	// 2. Mutual exclusion: opening Subtitle popover closes Danmaku popover.
	if err := tt.Click("切换字幕"); err != nil {
		t.Fatalf("click subtitle CC icon: %v", err)
	}
	if a.playerPopovers.danmaku || !a.playerPopovers.subtitle || !tt.HasText("显示字幕") {
		t.Fatalf("subtitle popover did not exclusively replace danmaku popover: %+v", a.playerPopovers)
	}

	// Toggle 显示字幕 switch OFF then ON inside Subtitle popover restores track.
	if err := tt.Click("字幕开关"); err != nil {
		t.Fatalf("toggle subtitle switch off: %v", err)
	}
	if fp.subVisible || fp.activeSub != 0 {
		t.Fatalf("subtitle switch off failed: visible=%v activeSub=%d", fp.subVisible, fp.activeSub)
	}
	if err := tt.Click("字幕开关"); err != nil {
		t.Fatalf("toggle subtitle switch on: %v", err)
	}
	if !fp.subVisible || fp.activeSub != 1 {
		t.Fatalf("subtitle switch on did not restore track 1: visible=%v activeSub=%d", fp.subVisible, fp.activeSub)
	}

	// 3. Mutual exclusion: opening Speed popover closes Subtitle popover.
	if err := tt.Click("播放速度"); err != nil {
		t.Fatalf("click speed button: %v", err)
	}
	if a.playerPopovers.subtitle || !a.playerPopovers.speed || !tt.HasText("长按 → 键可临时 2× 加速") {
		t.Fatalf("speed popover did not exclusively replace subtitle popover: %+v", a.playerPopovers)
	}
	if err := tt.Click("1.5×"); err != nil {
		t.Fatalf("choose 1.5x speed: %v", err)
	}
	if a.playerPopovers.speed || fp.Rate() != 1.5 {
		t.Fatalf("selecting speed did not apply & close popover: open=%v rate=%v", a.playerPopovers.speed, fp.Rate())
	}

	// 4. Right-clicking Danmaku button toggles danmaku directly without opening popover.
	dmRect, ok := tt.Find("弹幕")
	if !ok {
		t.Fatal("danmaku button missing")
	}
	tt.PressButton(1, dmRect.X+dmRect.W/2, dmRect.Y+dmRect.H/2)
	tt.ReleaseButton(1, dmRect.X+dmRect.W/2, dmRect.Y+dmRect.H/2)
	if a.preferences.Danmaku.Enabled || a.playerPopovers.danmaku {
		t.Fatalf("right-click danmaku should toggle off without opening popover: enabled=%v popover=%v",
			a.preferences.Danmaku.Enabled, a.playerPopovers.danmaku)
	}

	// 5. Navigating from Danmaku popover -> full danmakuDialog closes popover.
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("换集/搜片"); err != nil {
		t.Fatalf("open full danmaku dialog from popover: %v", err)
	}
	if a.playerPopovers.danmaku || !a.danmaku.open || !tt.HasText("弹幕设置") {
		t.Fatalf("danmaku dialog navigation failed: popover=%v dialog=%v", a.playerPopovers.danmaku, a.danmaku.open)
	}
	a.danmaku.open = false
	tt.Frame()

	// 6. Navigating from Subtitle popover -> full subtitleDialog closes popover.
	if err := tt.Click("切换字幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("字号样式与在线匹配…"); err != nil {
		t.Fatalf("open full subtitle dialog from popover: %v", err)
	}
	if a.playerPopovers.subtitle || !a.playerExtra.subOnline.open || !tt.HasText("字幕设置与在线匹配") {
		t.Fatalf("subtitle dialog navigation failed: popover=%v dialog=%v", a.playerPopovers.subtitle, a.playerExtra.subOnline.open)
	}
}

func TestPlanBFullscreenKeepVisibleWhilePopoverOpen(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.setFullScreen(true)
	tt := ui.NewTester(a.view, 1120, 760)

	// Open Danmaku popover in fullscreen and wait well past playerOverlayIdle (3s).
	if err := tt.Click("弹幕"); err != nil {
		t.Fatalf("open danmaku popover in fullscreen: %v", err)
	}
	tt.Move(500, 300)
	tt.Advance(8 * time.Second)
	if !a.playerPopovers.danmaku || !tt.HasText("显示弹幕") {
		t.Fatalf("fullscreen controls faded out while popover was open: popovers=%+v texts=%v", a.playerPopovers, tt.Texts())
	}
	if _, ok := tt.Find("返回"); !ok {
		t.Fatal("fullscreen top HUD hid while popover was open")
	}

	// Escape closes the popover FIRST without leaving fullscreen.
	tt.Key(0, ui.KeyEscape)
	if a.playerPopovers.anyOpen() || !a.fullScreen {
		t.Fatalf("Escape did not close popover while staying in fullscreen: popovers=%+v full=%v", a.playerPopovers, a.fullScreen)
	}

	// Once closed, idle fullscreen auto-hides controls after 3s as usual.
	tt.Advance(4 * time.Second)
	if _, ok := tt.Find("返回"); ok {
		t.Fatal("fullscreen controls failed to hide after popover closed")
	}
}

func TestResolutionBadgeDisplaysTierAndTogglesStatsOverlay(t *testing.T) {
	vp, err := ui.NewVideoPlayer(&sizedTimedVideo{w: 1920, h: 800})
	if err != nil {
		t.Fatal(err)
	}
	a := &app{player: wrapSystemPlayer(vp)}
	t.Cleanup(a.close)

	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("1080P") {
		t.Fatalf("1920x800 scope video did not show 1080P badge: %v", tt.Texts())
	}
	if a.playerExtra.statsOpen {
		t.Fatal("stats overlay should start closed")
	}
	if err := tt.Click("视频分辨率"); err != nil {
		t.Fatalf("click resolution badge: %v", err)
	}
	if !a.playerExtra.statsOpen || !tt.HasText("播放信息") {
		t.Fatalf("clicking resolution badge did not open stats overlay: open=%v texts=%v", a.playerExtra.statsOpen, tt.Texts())
	}
	if err := tt.Click("视频分辨率"); err != nil {
		t.Fatalf("second click resolution badge: %v", err)
	}
	if a.playerExtra.statsOpen {
		t.Fatal("second click on resolution badge did not close stats overlay")
	}
}

func TestDiscoveryHeaderNoDanglingRefreshWhenTimestampZero(t *testing.T) {
	a := &app{}
	a.library.root = &config.Config{URL: "https://example.com/config"}
	a.library.cached = map[string]*config.Config{}
	a.discovery.hot.feed = douban.Feed{
		Kind:     douban.Hot,
		Subjects: []douban.Subject{{ID: "1", Title: "热门演示", Rating: "8.8"}},
	}
	defer a.close()

	for _, width := range []int{1120, 1240} {
		tt := ui.NewTester(a.view, width, 700)
		tabsEl, ok := tt.Find("榜单")
		if !ok {
			t.Fatalf("width=%d missing tabs", width)
		}
		btn, ok := tt.Find("刷新榜单")
		if !ok {
			t.Fatalf("width=%d missing refresh icon button", width)
		}
		sameRow := btn.Y < tabsEl.Y+tabsEl.H && tabsEl.Y < btn.Y+btn.H
		if !sameRow {
			t.Fatalf("width=%d refresh button dangled onto a separate empty row when Updated is zero: tabs=%+v btn=%+v",
				width, tabsEl, btn)
		}
		if btn.X < tabsEl.X+tabsEl.W-0.5 || btn.X+btn.W > float32(width)+1 {
			t.Fatalf("width=%d inline refresh button overlapped tabs or overflowed: tabs=%+v btn=%+v", width, tabsEl, btn)
		}
	}
}

func TestAlwaysDarkModeIgnoresLightHostTheme(t *testing.T) {
	a := &app{}
	defer a.close()

	var resolvedDark bool
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Themed(c, ui.LightTheme()).Fill().Children(func() {
			resolvedDark = appDarkTheme(c).Dark
			a.view(c)
		})
	}, 240, 160)
	tt.Frame()
	if !resolvedDark {
		t.Fatal("appDarkTheme(c).Dark = false under ui.LightTheme(); want true")
	}
	img := tt.Image()
	bounds := img.Bounds()
	var sumLuma, samples uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 8 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 8 {
			r, g, b, _ := img.At(x, y).RGBA()
			sumLuma += uint64(r>>8)*299 + uint64(g>>8)*587 + uint64(b>>8)*114
			samples++
		}
	}
	if avgLuma := sumLuma / (samples * 1000); avgLuma > 90 {
		t.Fatalf("shell rendered light pixels under LightTheme host: avgLuma=%d (want <= 90)", avgLuma)
	}
}
