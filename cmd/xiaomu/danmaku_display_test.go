package main

import (
	"path/filepath"
	"testing"

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
