package main

import (
	"fmt"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

// newDetailTestApp is a detail page with count sources for one title.
// The first seed is the source on show; the rest are the other sites
// that have the same title.
func newDetailTestApp(t *testing.T, count int) (*app, []library.Item) {
	t.Helper()
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	seeds := make([]library.Item, 0, count)
	for i := 0; i < count; i++ {
		seeds = append(seeds, library.Item{
			Entry: library.Entry{
				Site:   config.Site{Key: fmt.Sprintf("site-%d", i), Name: fmt.Sprintf("来源 %d", i)},
				Config: doc,
			},
			Video: catalog.Video{ID: catalog.ID(fmt.Sprintf("%d", i)), Name: "演示电影", Remarks: "HD"},
		})
	}
	a.title = &titlePage{
		name:   "演示电影",
		seeds:  seeds,
		failed: map[string]string{},
		item:   seeds[0],
		detail: &catalog.Detail{
			Name:    "演示电影",
			Year:    "2026",
			Type:    "喜剧",
			Content: "简介第一句。简介第二句。",
			Sources: []catalog.Source{{Name: "线路", Episodes: []catalog.Episode{{Name: "第1集", ID: "one"}}}},
		},
	}
	a.nav = []routeKind{routeHome, routeDetail}
	t.Cleanup(a.close)
	return a, seeds
}

func TestStripHTML(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`<p>八仙"在成仙和成功之间选择了"成团"！</p>`, `八仙"在成仙和成功之间选择了"成团"！`},
		{`<p>第一句。</p><p>第二句。</p>`, "第一句。第二句。"},
		{`A&nbsp;&nbsp;B &amp; C`, "A B & C"},
		{`没有标签`, "没有标签"},
		{`<b>粗体</b>和<a href="https://example.com">链接</a>`, "粗体和链接"},
		{`<p>转义 &lt;tag&gt; 和 &quot;引号&quot;</p>`, `转义 <tag> 和 "引号"`},
	} {
		if got := stripHTML(tc.in); got != tc.want {
			t.Fatalf("stripHTML(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDetailHeaderShowsPlayButtonAboveSources(t *testing.T) {
	// Open the title the way a viewer does: the test site returns the
	// synopsis wrapped in <p> tags, and the detail page must show it
	// without them.
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	if err := tt.Click("片名"); err != nil {
		t.Fatal(err)
	}
	tt.Type("演示电影")
	tt.Key(0, ui.KeyEnter)
	if a.route() != routeSearch {
		t.Fatalf("typed search did not run: route=%v", a.route())
	}
	poster, ok := tt.Find("演示电影 · 示例来源")
	if !ok {
		t.Fatalf("result label missing: %v", tt.Texts())
	}
	tt.ClickAt(poster.X+poster.W/2, poster.Y+poster.H/2)
	if a.route() != routeDetail || a.title == nil || a.title.detail == nil {
		t.Fatalf("click did not open the result: route=%v title=%+v", a.route(), a.title)
	}
	if !tt.HasText("演示电影") || !tt.HasText("立即播放") || !tt.HasText("简介第一句。") {
		t.Fatalf("detail header missing: %v", tt.Texts())
	}
	if tt.HasText("<p>") {
		t.Fatalf("synopsis still has HTML tags: %v", tt.Texts())
	}
	// The play button lives in the fixed header, above the sources
	// box, so it never scrolls away with the sources.
	play, ok := tt.Find("立即播放")
	if !ok {
		t.Fatal("play button is missing")
	}
	box, ok := tt.Find("来源列表")
	if !ok {
		t.Fatal("sources box is missing")
	}
	if play.Y+play.H > box.Y {
		t.Fatalf("play button is not above the sources box: play=%+v box=%+v", play, box)
	}
}

func TestDetailSourcesGridShowsNineFirst(t *testing.T) {
	a, seeds := newDetailTestApp(t, 19)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if !tt.HasText("共 19 个") || !tt.HasText("下滑查看更多") {
		t.Fatalf("sources count or scroll hint missing: %v", tt.Texts())
	}
	box, ok := tt.Find("来源列表")
	if !ok {
		t.Fatal("sources box is missing")
	}
	if box.H != sourceBoxHeight {
		t.Fatalf("sources box height = %v, want %v", box.H, sourceBoxHeight)
	}
	// The first nine cards tile three rows inside the box; the tenth
	// card is only a peek below the box's bottom edge, so it needs
	// the scroll. The tester clips rects to the visible area, so a
	// partly visible card reports a short height.
	for i := 0; i < 10; i++ {
		rect, ok := tt.Find("来源 " + seeds[i].Entry.Label())
		if !ok {
			t.Fatalf("source card %d is missing", i)
		}
		if i < 9 && rect.H != sourceCardHeight {
			t.Fatalf("card %d is not fully inside the box: card=%+v box=%+v", i, rect, box)
		}
		if i == 9 && rect.H >= sourceCardHeight {
			t.Fatalf("tenth card is already visible without scrolling: card=%+v box=%+v", rect, box)
		}
	}
	// Scrolling the box to the end swaps the hint.
	a.title.sourcesScroll.Y = 1e9
	tt.Frame()
	if !tt.HasText("已显示全部 19 个来源") || tt.HasText("下滑查看更多") {
		t.Fatalf("end-of-list hint did not show: %v", tt.Texts())
	}
}

func TestDetailSourcesFewNeedNoScrollHint(t *testing.T) {
	a, _ := newDetailTestApp(t, 3)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if !tt.HasText("共 3 个") {
		t.Fatalf("sources count missing: %v", tt.Texts())
	}
	if tt.HasText("下滑查看更多") || tt.HasText("已显示全部") {
		t.Fatalf("scroll hint shows with only three sources: %v", tt.Texts())
	}
}

func TestDetailSourceHintOnlyWhenCurrentFails(t *testing.T) {
	a, seeds := newDetailTestApp(t, 3)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if tt.HasText("当前来源播不了") {
		t.Fatalf("hint shows while the current source is fine: %v", tt.Texts())
	}
	a.title.failed[itemKey(seeds[0])] = "读取失败"
	tt.Frame()
	if !tt.HasText("当前来源播不了") {
		t.Fatalf("hint missing after the current source failed: %v", tt.Texts())
	}
}
