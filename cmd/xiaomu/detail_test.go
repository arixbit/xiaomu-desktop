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
		// The seeds keep distinct keys and names, but borrow the test
		// server's type and API, so a row can actually be opened the
		// way a viewer opens it.
		seeds = append(seeds, library.Item{
			Entry: library.Entry{
				Site:   config.Site{Key: fmt.Sprintf("site-%d", i), Name: fmt.Sprintf("来源 %d", i), Type: doc.Sites[0].Type, Kind: doc.Sites[0].Kind, API: doc.Sites[0].API},
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
	// section, so it never scrolls away with the sources.
	play, ok := tt.Find("立即播放")
	if !ok {
		t.Fatal("play button is missing")
	}
	sources, ok := tt.Find("来源")
	if !ok {
		t.Fatal("sources heading is missing")
	}
	if play.Y+play.H > sources.Y {
		t.Fatalf("play button is not above the sources: play=%+v sources=%+v", play, sources)
	}
	// The play button is a normal-sized button beside the favourite
	// button, not a full-width bar.
	fav, ok := tt.Find("加入想看")
	if !ok {
		t.Fatal("favourite button is missing")
	}
	if fav.Y < play.Y || fav.Y >= play.Y+play.H || fav.X <= play.X+play.W {
		t.Fatalf("favourite button is not beside the play button: play=%+v fav=%+v", play, fav)
	}
}

func TestDetailHeaderShortSynopsisLeavesNoHole(t *testing.T) {
	a, _ := newDetailTestApp(t, 3)
	a.title.detail.Content = "简介只有一句。"
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if !tt.HasText("简介只有一句。") || !tt.HasText("立即播放") || !tt.HasText("加入想看") {
		t.Fatalf("short-synopsis header missing: %v", tt.Texts())
	}
	// The header's height is driven by the poster (240), not by the
	// synopsis: with a one-line synopsis the sources heading still
	// sits just below the header, with no empty hole in between.
	title, ok := tt.Find("演示电影")
	if !ok {
		t.Fatal("title is missing")
	}
	sources, ok := tt.Find("来源")
	if !ok {
		t.Fatal("sources heading is missing")
	}
	if sources.Y-title.Y >= 300 {
		t.Fatalf("header is taller than the poster: title=%+v sources=%+v", title, sources)
	}
}

func TestDetailAccordionShowsAllSources(t *testing.T) {
	a, seeds := newDetailTestApp(t, 19)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if !tt.HasText("共 19 个") {
		t.Fatalf("sources count missing: %v", tt.Texts())
	}
	// Every source is a row in the list: no scroll box, no scroll hint.
	for i := 0; i < 19; i++ {
		if _, ok := tt.Find("来源 " + seeds[i].Entry.Label()); !ok {
			t.Fatalf("source row %d is missing", i)
		}
	}
	if tt.HasText("下滑查看更多") || tt.HasText("已显示全部") || tt.HasText("来源列表") {
		t.Fatalf("scroll-box leftovers show with the accordion: %v", tt.Texts())
	}
}

func TestDetailAccordionFewSources(t *testing.T) {
	a, seeds := newDetailTestApp(t, 3)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if !tt.HasText("共 3 个") {
		t.Fatalf("sources count missing: %v", tt.Texts())
	}
	for i := 0; i < 3; i++ {
		if _, ok := tt.Find("来源 " + seeds[i].Entry.Label()); !ok {
			t.Fatalf("source row %d is missing", i)
		}
	}
}

func TestDetailAccordionExpandsCurrentSource(t *testing.T) {
	a, seeds := newDetailTestApp(t, 3)
	a.title.detail.Sources = []catalog.Source{
		{Name: "线路一", Episodes: []catalog.Episode{{Name: "第1集", ID: "one"}, {Name: "第2集", ID: "two"}}},
		{Name: "线路二", Episodes: []catalog.Episode{{Name: "第1集", ID: "one"}}},
	}
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	// The current source is expanded: its lines and episodes show
	// inside its own row. (Episode chips render as "1" and "2":
	// episodeButtonLabel strips the 第/集 wrapper.)
	if !tt.HasText("播放线路") || !tt.HasText("选集") || !tt.HasText("共 2 集") || !tt.HasText("线路一") {
		t.Fatalf("expanded source content missing: %v", tt.Texts())
	}
	// "当前来源" sits on the current source's row.
	row, ok := tt.Find("来源 " + seeds[0].Entry.Label())
	if !ok {
		t.Fatal("current source row is missing")
	}
	state, ok := tt.Find("当前来源")
	if !ok {
		t.Fatal("current-source state is missing")
	}
	if state.Y < row.Y || state.Y >= row.Y+row.H {
		t.Fatalf("current-source state is not on the current row: state=%+v row=%+v", state, row)
	}
	// Choosing another source switches it: that row expands instead.
	if err := tt.Click("来源 " + seeds[1].Entry.Label()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.title == nil || itemKey(a.title.item) != itemKey(seeds[1]) {
		t.Fatalf("click did not switch the source: item=%+v", a.title)
	}
	row, ok = tt.Find("来源 " + seeds[1].Entry.Label())
	if !ok {
		t.Fatal("new current source row is missing")
	}
	state, ok = tt.Find("当前来源")
	if !ok {
		t.Fatal("current-source state is missing after the switch")
	}
	if state.Y < row.Y || state.Y >= row.Y+row.H {
		t.Fatalf("current-source state did not move with the switch: state=%+v row=%+v", state, row)
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
