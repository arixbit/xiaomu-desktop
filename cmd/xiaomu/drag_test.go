package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// dragBar is the traffic-light band the headless tester pretends the
// window has, matching a hidden-title-bar window on macOS.
var dragBar = ui.TitleBar{Height: 28, Left: 78, Right: 0, Bottom: 28}

func TestShellDragStrip(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	strip, ok := tt.Find("拖动窗口")
	if !ok {
		t.Fatal("shell has no window drag strip")
	}
	if strip.Y != 0 || strip.H != 32 || strip.W != 1120 {
		t.Fatalf("drag strip is not the top band: %+v", strip)
	}
	// The strip must not cover the toolbar search field, which sits
	// below it.
	search, ok := tt.Find("片名")
	if !ok {
		t.Fatal("toolbar search field is missing")
	}
	if search.Y < strip.Y+strip.H {
		t.Fatalf("drag strip covers the search field: strip=%+v search=%+v", strip, search)
	}
	// The sidebar title stays where it was before the strip: the strip
	// takes 32px at the top and the sidebar gives 32px of its top
	// padding back.
	title, ok := tt.Find("小幕")
	if !ok {
		t.Fatal("sidebar title is missing")
	}
	if title.Y != 62 {
		t.Fatalf("sidebar title moved with the strip: y=%v want 62", title.Y)
	}
	// In full screen there is no title bar, so the strip is gone and
	// the sidebar keeps its old top padding: the title stays put.
	tt.SetTitleBar(ui.TitleBar{})
	tt.Frame()
	if _, ok := tt.Find("拖动窗口"); ok {
		t.Fatal("drag strip is drawn in full screen, where it cannot drag")
	}
	title, ok = tt.Find("小幕")
	if !ok || title.Y != 62 {
		t.Fatalf("sidebar title moved in full screen: %+v", title)
	}
}

func TestPlayerDragStrip(t *testing.T) {
	a := newPlayerTestApp(t)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	strip, ok := tt.Find("拖动窗口")
	if !ok {
		t.Fatal("player has no window drag strip")
	}
	if strip.Y != 0 || strip.H != 32 || strip.W != 1120 {
		t.Fatalf("player drag strip is not the top band: %+v", strip)
	}
	// The strip must not cover the header's back button.
	back, ok := tt.Find("返回")
	if !ok {
		t.Fatal("player header is missing")
	}
	if back.Y < strip.Y+strip.H {
		t.Fatalf("drag strip covers the player header: strip=%+v back=%+v", strip, back)
	}
}
