package main

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// nearRect allows for sub-pixel differences in text metrics across platforms.
func nearRect(a, b ui.Rect) bool {
	return math.Abs(float64(a.X-b.X)) < 0.5 && math.Abs(float64(a.Y-b.Y)) < 0.5 &&
		math.Abs(float64(a.W-b.W)) < 0.5 && math.Abs(float64(a.H-b.H)) < 0.5
}

func TestSidebarUpdateHint(t *testing.T) {
	a := &app{}
	t.Cleanup(a.close)
	tt := ui.NewTester(a.view, 640, 420)
	before, ok := tt.Find("检查更新")
	if !ok {
		t.Fatal("update entry is missing")
	}
	if _, ok := tt.Find("新版本提示"); ok {
		t.Fatal("an unchecked version already shows an update hint")
	}
	a.setAvailableUpdate("0.8.5")
	tt.Frame()
	button, ok := tt.Find("有新版本")
	if !ok || !nearRect(button, before) {
		t.Fatalf("update hint moved or replaced the sidebar entry: before=%+v after=%+v", before, button)
	}
	dot, ok := tt.Find("新版本提示")
	if !ok || dot.W < 1 || dot.H < 1 || dot.X < button.X || dot.Y < button.Y || dot.X+dot.W > button.X+button.W || dot.Y+dot.H > button.Y+button.H {
		t.Fatalf("update dot is outside its button: button=%+v dot=%+v", button, dot)
	}
	if button.X+button.W > sidebarWidth+20 || button.Y < 210 || button.Y+button.H > 420 {
		t.Fatalf("update hint does not fit the bottom-left sidebar: %+v", button)
	}
	// The updater clears availability for up-to-date, failed and skipped checks.
	a.setAvailableUpdate("")
	tt.Frame()
	if !tt.HasText("检查更新") || tt.HasText("有新版本") {
		t.Fatalf("cleared availability left a stale hint: %v", tt.Texts())
	}
	if _, ok := tt.Find("新版本提示"); ok {
		t.Fatal("cleared availability left a stale dot")
	}
}

func TestAvailableUpdateUsesWindowDispatch(t *testing.T) {
	queued := make(chan func(), 2)
	a := &app{dispatch: func(update func()) { queued <- update }}
	t.Cleanup(a.close)
	go a.setAvailableUpdate("0.8.5")
	var apply func()
	select {
	case apply = <-queued:
	case <-time.After(time.Second):
		t.Fatal("update availability did not dispatch to the UI thread")
	}
	if a.availableUpdate != "" {
		t.Fatal("background update changed UI state before dispatch")
	}
	apply()
	if a.availableUpdate != "0.8.5" {
		t.Fatal("dispatched availability did not reach the sidebar state")
	}
	a.setAvailableUpdate("")
	a.close()
	(<-queued)()
	if a.availableUpdate != "0.8.5" {
		t.Fatal("a queued update changed state after the window closed")
	}
}

func TestUpdateButtonRemainsClickable(t *testing.T) {
	for _, version := range []string{"", "0.8.5"} {
		t.Run("version="+version, func(t *testing.T) {
			clicks := 0
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Box(c).Fill().Children(func() {
					if updateButton(c, version).Clicked() {
						clicks++
					}
				})
			}, sidebarWidth-20, 60)
			label := "检查更新"
			if version != "" {
				label = "有新版本"
			}
			if err := tt.Click(label); err != nil {
				t.Fatal(err)
			}
			if clicks != 1 {
				t.Fatalf("clicking %s activated the button %d times", label, clicks)
			}
			if version != "" {
				dot, ok := tt.Find("新版本提示")
				if !ok {
					t.Fatal("missing update dot")
				}
				tt.ClickAt(dot.X+dot.W/2, dot.Y+dot.H/2)
				if clicks != 2 {
					t.Fatal("update dot swallowed the button click")
				}
			}
		})
	}
}
