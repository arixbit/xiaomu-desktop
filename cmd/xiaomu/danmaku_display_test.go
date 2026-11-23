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
		{0.25, 210},
		{0.5, 310},
		{0.75, 410},
		{1, 478},
	} {
		got := danmakuArea(picture, bounds, tc.fraction)
		if got.X != picture.X || got.W != picture.W || got.Y != 122 || got.Y+got.H != tc.bottom {
			t.Fatalf("area=%v: got %+v, want bottom=%v", tc.fraction, got, tc.bottom)
		}
	}
}
