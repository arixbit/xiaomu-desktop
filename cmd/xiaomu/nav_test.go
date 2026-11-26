package main

import (
	"fmt"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestRootPagesHideBackButton(t *testing.T) {
	_, doc := suggestionTestServer(t)
	for _, tab := range tabs {
		t.Run(tab.label, func(t *testing.T) {
			a := newSuggestionTestApp(t, doc)
			a.nav = []routeKind{tab.kind}
			tt := ui.NewTester(a.view, 1120, 760)
			if _, ok := tt.Find("返回"); ok {
				t.Fatal("root page offers a back button without a previous page")
			}
		})
	}
}

func TestToolbarBackPreservesSearchThenDisappearsAtRoot(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("片名"); err != nil {
		t.Fatal(err)
	}
	tt.Type("演示电影")
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	if a.route() != routeDetail {
		t.Fatal("search result did not open its detail")
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	if a.route() != routeSearch || a.browser.query != "演示电影" || len(a.library.search.items) != 1 {
		t.Fatalf("returning from detail lost the search: route=%v query=%q items=%d", a.route(), a.browser.query, len(a.library.search.items))
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	if a.route() != routeHome {
		t.Fatal("second back did not return home")
	}
	if _, ok := tt.Find("返回"); ok {
		t.Fatal("back button remained on the home page")
	}
}

func TestToolbarSearchLayoutSurvivesResizeAndNavigation(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	if err := tt.Click("片名"); err != nil {
		t.Fatal(err)
	}
	tt.Type("演示")
	for _, width := range []int{640, 900, 1120, 1568, 640} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			tt.SetSize(width, 760)
			search, ok := tt.Find("片名")
			if !ok {
				t.Fatal("search disappeared after resize")
			}
			bar, ok := tt.Find("拖动窗口")
			if !ok {
				t.Fatal("toolbar lost its drag area")
			}
			if search.X < bar.X || search.X+search.W > bar.X+bar.W+1 || search.H < 40 {
				t.Fatalf("search is clipped or too short: search=%+v bar=%+v", search, bar)
			}
			if math.Abs(float64(search.X+search.W/2-bar.X-bar.W/2)) > 1 {
				t.Fatalf("search is not centered over the content: search=%+v bar=%+v", search, bar)
			}
			if width < 1000 {
				if search.Y < bar.Y+bar.H || math.Abs(float64(search.W-bar.W)) > 1 {
					t.Fatalf("narrow search should fill a row below navigation: search=%+v bar=%+v", search, bar)
				}
			} else if search.Y < bar.Y || search.Y+search.H > bar.Y+bar.H || search.W < 480 || search.W > 560 {
				t.Fatalf("wide search should fit the toolbar: search=%+v bar=%+v", search, bar)
			}
			if !tt.Focused("片名") || a.browser.query != "演示" || !tt.HasText("演示电影") {
				t.Fatal("resize lost search focus, text or suggestions")
			}
			// A secondary page must leave the search in the same position.
			a.push(routeSearch)
			tt.Frame()
			withBack, _ := tt.Find("片名")
			back, ok := tt.Find("返回")
			if !ok || withBack != search {
				t.Fatal("secondary navigation moved the search or hid back")
			}
			if back.X < withBack.X+withBack.W && back.X+back.W > withBack.X && back.Y < withBack.Y+withBack.H && back.Y+back.H > withBack.Y {
				t.Fatal("back overlaps the search")
			}
			a.nav = []routeKind{routeHome}
			tt.Frame()
		})
	}
}

func TestSearchShortcutAndEscape(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	a.nav = []routeKind{routeHome, routeSearch}
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Key(ui.Cmd, ui.KeyK)
	if !tt.Focused("片名") {
		t.Fatal("Cmd+K did not focus the library search")
	}
	tt.Type("演示")
	if !tt.HasText("演示电影") {
		t.Fatal("search suggestions are missing")
	}
	tt.Key(0, ui.KeyEscape)
	if tt.HasText("演示电影") || !tt.Focused("片名") || a.route() != routeSearch {
		t.Fatal("first Escape must close only suggestions")
	}
	tt.Key(0, ui.KeyEscape)
	if tt.Focused("片名") || a.route() != routeSearch || a.browser.query != "演示" {
		t.Fatal("second Escape must leave the search field without navigating or clearing text")
	}
	tt.Key(0, ui.KeyEscape)
	if a.route() != routeHome {
		t.Fatal("third Escape must return to the previous page")
	}
}

func TestSearchShortcutRespectsDialog(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	a.linkOpen = true
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("地址"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyK)
	if !tt.Focused("地址") || tt.Focused("片名") || !a.linkOpen {
		t.Fatal("Cmd+K stole focus from a dialog")
	}
}

func TestSearchShortcutSelectsExistingQuery(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("旧片名")
	if a.browser.query != "旧片名" {
		t.Fatalf("query=%q", a.browser.query)
	}
	// While already focused, Cmd+K selects the query so typing replaces it.
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("新片名")
	if a.browser.query != "新片名" {
		t.Fatalf("Cmd+K while focused did not select existing query: query=%q", a.browser.query)
	}
	// Leave search, then press Cmd+K again: typing replaces the old query.
	tt.Key(0, ui.KeyEscape)
	tt.Key(0, ui.KeyEscape)
	if tt.Focused("片名") {
		t.Fatal("search stayed focused after Escape")
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("替换片名")
	if a.browser.query != "替换片名" {
		t.Fatalf("Cmd+K after blur did not select existing query: query=%q", a.browser.query)
	}
}

func TestHomeScrollbarClearance(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	a.favorites.entries = []favoriteEntry{{Name: "追剧样本", EpisodeCount: 12, LatestEpisode: "第12集"}}
	a.history.note(historyEntry{Name: "在看样本", Episode: "第1集", Position: 60, Duration: 1800})
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()

	scroll, ok := tt.Find("首页内容")
	if !ok {
		t.Fatal("home scroll container missing")
	}
	maxContentRight := scroll.X + scroll.W - appScrollbar.TrackWidth
	for _, label := range []string{"全部追剧", "刷新榜单"} {
		rect, found := tt.Find(label)
		if !found {
			t.Fatalf("%s missing on home page", label)
		}
		if rect.X+rect.W > maxContentRight+0.5 {
			t.Fatalf("%s overlaps home scrollbar track: rect=%+v maxRight=%v scroll=%+v", label, rect, maxContentRight, scroll)
		}
	}
}

func TestSidebarPlacesSourcesManagementWithBottomUtilities(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()

	fav, ok := tt.Find("追剧")
	if !ok {
		t.Fatal("追剧 missing")
	}
	sources, ok := tt.Find("片源管理")
	if !ok {
		t.Fatal("片源管理 missing")
	}
	openFile, ok := tt.Find("打开文件")
	if !ok {
		t.Fatal("打开文件 missing")
	}
	if sources.Y-fav.Y-fav.H < 100 {
		t.Fatalf("片源管理 should be separated into the bottom group below spacer: fav=%+v sources=%+v", fav, sources)
	}
	if sources.Y+sources.H > openFile.Y+0.5 || openFile.Y-(sources.Y+sources.H) > 12 {
		t.Fatalf("片源管理 should sit directly above 打开文件: sources=%+v openFile=%+v", sources, openFile)
	}
	if err := tt.Click("片源管理"); err != nil {
		t.Fatal(err)
	}
	if a.route() != routeSources || a.pageTitle() != "片源管理" {
		t.Fatalf("clicking 片源管理 did not open sources page: route=%v title=%q", a.route(), a.pageTitle())
	}
}
