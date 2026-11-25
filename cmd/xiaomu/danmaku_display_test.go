package main

import (
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/egoist/mygo/ui"
)

func TestDanmakuSpeedAndAreaControlsPersist(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferencesPath = filepath.Join(t.TempDir(), "preferences.json")
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("屏蔽词与高级弹幕服务…"); err != nil {
		t.Fatal(err)
	}
	if a.preferences.Danmaku.Speed != 0.5 || a.preferences.Danmaku.Area != 0.5 {
		t.Fatalf("defaults did not slow comments and limit their area: %+v", a.preferences.Danmaku)
	}
	r, ok := tt.Find("弹幕滚动速度")
	if !ok {
		t.Fatal("scroll speed control is missing")
	}
	tt.ClickAt(r.X, r.Y+r.H/2)
	r, ok = tt.Find("弹幕显示区域")
	if !ok {
		t.Fatal("display area control is missing")
	}
	tt.ClickAt(r.X+r.W-1, r.Y+r.H/2)
	prefs, err := readPlayerPreferences(a.preferencesPath)
	if err != nil || prefs.Danmaku.Speed != 0.25 || prefs.Danmaku.Area != 1 {
		t.Fatalf("saved display settings = %+v, err=%v", prefs.Danmaku, err)
	}
	if _, ok := tt.Find("弹幕字号"); !ok {
		t.Fatal("font size setting was lost")
	}
}

func TestDanmakuAreaFollowsVideoHeightAndAvoidsControls(t *testing.T) {
	bounds := ui.Rect{X: 20, Y: 10, W: 800, H: 600}
	picture := ui.Rect{X: 20, Y: 110, W: 800, H: 400}
	for _, tc := range []struct {
		fraction float32
		bottom   float32
	}{
		{0.25, 211},
		{0.5, 300},
		{0.75, 389},
		{1, 478},
	} {
		got := danmakuArea(picture, bounds, tc.fraction, true, false)
		if got.X != picture.X || got.W != picture.W || got.Y != 122 || got.Y+got.H != tc.bottom {
			t.Fatalf("area=%v: got %+v, want bottom=%v", tc.fraction, got, tc.bottom)
		}
	}

	// Minimum window with full-height video must still fit max font size at 25% area.
	small := ui.Rect{X: 0, Y: 0, W: 640, H: 420}
	gotSmall := danmakuArea(small, small, 0.25, true, false)
	if gotSmall.Y != 72 || gotSmall.H < 36*1.4 {
		t.Fatalf("small window 25%% area too short for max font size: %+v", gotSmall)
	}

	// Hidden fullscreen controls release the top and bottom control bands.
	full := ui.Rect{X: 0, Y: 0, W: 1440, H: 900}
	gotHidden := danmakuArea(full, full, 1, false, false)
	if gotHidden.Y != 12 || gotHidden.Y+gotHidden.H != 888 {
		t.Fatalf("hidden controls still deducted control height: %+v", gotHidden)
	}

	// Subtitle protection reserves the bottom 14% of the picture even in fullscreen.
	gotProtected := danmakuArea(full, full, 1, false, true)
	if gotProtected.Y != 12 || gotProtected.Y+gotProtected.H != 900-900*0.14 {
		t.Fatalf("subtitle protection did not reserve bottom band: %+v", gotProtected)
	}
}

func TestDanmakuDialogAlignmentAndScrollbarClearance(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferences.Danmaku.Enabled = true
	a.danmaku.episodes = []danmaku.Episode{
		{ID: "1", Anime: "示例剧 (2026) 【国产剧】 from iqiyi", Name: "第10集 标题示例"},
		{ID: "2", Anime: "示例剧 (2026) 【电视剧】 from 360", Name: "第10集"},
		{ID: "3", Anime: "示例剧 国际版 (2026) 【电视剧】 from 360", Name: "第10集"},
	}
	a.danmaku.selected = &a.danmaku.episodes[0]
	a.danmaku.message = "已加载 471 条弹幕 · 示例剧 · 第10集"
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("换集/搜片"); err != nil {
		t.Fatal(err)
	}
	viewport, ok := tt.Find("弹幕设置内容")
	if !ok {
		t.Fatal("scroll viewport missing")
	}

	// All four sliders must align identically on both left and right edges.
	sliders := []string{"弹幕字号", "弹幕不透明度", "弹幕滚动速度", "弹幕显示区域"}
	first, ok := tt.Find(sliders[0])
	if !ok {
		t.Fatalf("missing slider %s", sliders[0])
	}
	for _, label := range sliders[1:] {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("missing slider %s", label)
		}
		if math.Abs(float64(r.X-first.X)) > 0.5 || math.Abs(float64(r.W-first.W)) > 0.5 {
			t.Fatalf("slider %s (%+v) misaligned with %s (%+v)", label, r, sliders[0], first)
		}
	}

	// Rightmost controls inside the scroll container must keep clear of the 10px scrollbar.
	for _, label := range []string{"弹幕开关", "重置弹幕时间偏移", "搜索弹幕", "弹幕密度", "弹幕屏蔽词"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("missing control %s", label)
		}
		if viewport.X+viewport.W-(r.X+r.W) < 14 {
			t.Fatalf("control %s (%+v) overlaps scrollbar in viewport %+v", label, r, viewport)
		}
	}

	// Segmented density control must span the full remaining width equally.
	density, ok := tt.Find("弹幕密度")
	if !ok {
		t.Fatal("missing density control")
	}
	blocked, ok := tt.Find("弹幕屏蔽词")
	if !ok {
		t.Fatal("missing blocked keywords input")
	}
	if math.Abs(float64(density.X-blocked.X)) > 1 || math.Abs(float64(density.W-blocked.W)) > 1 {
		t.Fatalf("density segmented (%+v) does not match input width (%+v)", density, blocked)
	}

	// Episode result buttons must have vertical spacing between items.
	ep1, ok1 := tt.Find("示例剧 (2026) 【国产剧】 from iqiyi · 第10集 标题示例")
	ep2, ok2 := tt.Find("示例剧 (2026) 【电视剧】 from 360 · 第10集")
	if !ok1 || !ok2 {
		t.Fatalf("episode buttons missing: ok1=%v ok2=%v", ok1, ok2)
	}
	if ep2.Y-(ep1.Y+ep1.H) < 4 {
		t.Fatalf("episode buttons stick together: ep1=%+v ep2=%+v", ep1, ep2)
	}

	if dumpDir := os.Getenv("XIAOMU_DANMAKU_DIALOG_DUMP_DIR"); dumpDir != "" {
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
		save("dialog-3-results.png", tt)

		a.danmaku.episodes = append(a.danmaku.episodes,
			danmaku.Episode{ID: "4", Anime: "示例剧 (2026) 【电视剧】 from youku", Name: "第10集"},
			danmaku.Episode{ID: "5", Anime: "示例剧 (2026) 【电视剧】 from qq", Name: "第10集"},
			danmaku.Episode{ID: "6", Anime: "示例剧 (2026) 【电视剧】 from bilibili", Name: "第10集"},
		)
		tt.Frame()
		save("dialog-6-results.png", tt)

		tt.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, 140)
		save("dialog-scrolled.png", tt)
	}
}
