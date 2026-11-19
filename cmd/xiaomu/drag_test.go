package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// dragBar is the traffic-light band the headless tester pretends the
// window has, matching a hidden-title-bar window on macOS.
var dragBar = ui.TitleBar{Height: 28, Left: 78, Right: 0, Bottom: 28}

func TestWindowedOverlayHandsPressToNativeWindowDrag(t *testing.T) {
	a := &app{}
	pressed := false
	tt := ui.NewTester(func(c *ui.Context) {
		screen := ui.Box(c).Fill().DragWindow()
		if !a.playerOverlaysVisible(c, screen) {
			t.Error("windowed controls must stay visible")
		}
		// Read active state while painting: unlike reading it during layout,
		// this does not turn the drag region into a clickable control.
		screen.DrawOver(func(_ *ui.Painter, _ ui.Rect) { pressed = screen.Pressed() })
	}, 1120, 760)
	tt.Move(500, 24)
	tt.Press(500, 24)
	tt.Advance(time.Second / 30)
	if pressed {
		t.Fatal("overlay tracking captured the press instead of handing it to native window dragging")
	}
	tt.Release(580, 64)
}

func TestShellDragHandles(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	// The toolbar row itself is a drag handle, and it sits where the
	// toolbar was before any drag handle existed: no extra band was
	// added above it, so the layout is back to its old geometry.
	bar, ok := tt.Find("拖动窗口")
	if !ok {
		t.Fatal("toolbar has no window drag handle")
	}
	if bar.X != 232 || bar.Y != 14 || bar.W != 870 || bar.H != 44 {
		t.Fatalf("toolbar drag handle is not the toolbar: %+v", bar)
	}
	// The search field still sits inside the toolbar, so it keeps
	// working instead of dragging.
	search, ok := tt.Find("片名")
	if !ok {
		t.Fatal("toolbar search field is missing")
	}
	if search.Y < bar.Y || search.Y+search.H > bar.Y+bar.H {
		t.Fatalf("search field is not inside the toolbar: bar=%+v search=%+v", bar, search)
	}
	// The sidebar pane is a drag handle too, and its title stays where
	// it was before any drag handle existed.
	side, ok := tt.Find("拖动窗口（侧边栏）")
	if !ok {
		t.Fatal("sidebar has no window drag handle")
	}
	if side.X != 10 || side.Y != 10 || side.W != 204 || side.H != 740 {
		t.Fatalf("sidebar drag handle is not the sidebar pane: %+v", side)
	}
	title, ok := tt.Find("小幕")
	if !ok {
		t.Fatal("sidebar title is missing")
	}
	if title.Y != 62 {
		t.Fatalf("sidebar title moved: y=%v want 62", title.Y)
	}
	// The bottom buttons are fully visible again: nothing is pushed
	// past the window's bottom edge.
	update, ok := tt.Find("检查更新")
	if !ok {
		t.Fatal("sidebar update button is missing")
	}
	if update.Y+update.H > 760 {
		t.Fatalf("update button is cut off at the bottom: %+v", update)
	}
	// In full screen there is no title bar, so there are no drag
	// handles at all — but the sidebar title stays put.
	tt.SetTitleBar(ui.TitleBar{})
	tt.Frame()
	if _, ok := tt.Find("拖动窗口"); ok {
		t.Fatal("toolbar drag handle is drawn in full screen, where it cannot drag")
	}
	if _, ok := tt.Find("拖动窗口（侧边栏）"); ok {
		t.Fatal("sidebar drag handle is drawn in full screen, where it cannot drag")
	}
	title, ok = tt.Find("小幕")
	if !ok || title.Y != 62 {
		t.Fatalf("sidebar title moved in full screen: %+v", title)
	}
}

func TestPlayerDragHandle(t *testing.T) {
	a := newPlayerTestApp(t)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	background, ok := tt.Find("拖动窗口（播放页空白）")
	if !ok || background != (ui.Rect{W: 1120, H: 760}) {
		t.Fatalf("player background has no full-page window drag handle: %+v", background)
	}
	// The header's title is the drag handle: it sits beside the back
	// button, below the traffic-light band, not in a band above it.
	title, ok := tt.Find("拖动窗口")
	if !ok {
		t.Fatal("player header has no window drag handle")
	}
	back, ok := tt.Find("返回")
	if !ok {
		t.Fatal("player header is missing")
	}
	if title.Y < dragBar.Bottom || title.X <= back.X+back.W {
		t.Fatalf("player drag handle is not the header title: title=%+v back=%+v", title, back)
	}
	// In full screen there is no title bar, so there is no drag handle.
	tt.SetTitleBar(ui.TitleBar{})
	tt.Frame()
	if _, ok := tt.Find("拖动窗口"); ok {
		t.Fatal("player drag handle is drawn in full screen, where it cannot drag")
	}
	if _, ok := tt.Find("拖动窗口（播放页空白）"); ok {
		t.Fatal("player background remains draggable in fullscreen")
	}
}
