package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/douban"
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
	fav, ok := tt.Find("加入追剧")
	if !ok {
		t.Fatal("favourite button is missing")
	}
	// Same row (text metrics differ by sub-pixels across platforms), to the right.
	if fav.Y >= play.Y+play.H || play.Y >= fav.Y+fav.H || fav.X <= play.X+play.W {
		t.Fatalf("favourite button is not beside the play button: play=%+v fav=%+v", play, fav)
	}
}

func TestDetailHeaderShortSynopsisLeavesNoHole(t *testing.T) {
	a, _ := newDetailTestApp(t, 3)
	a.title.detail.Content = "简介只有一句。"
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if !tt.HasText("简介只有一句。") || !tt.HasText("立即播放") || !tt.HasText("加入追剧") {
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

func TestDetailHeaderShowsDoubanScore(t *testing.T) {
	a, _ := newDetailTestApp(t, 3)
	a.title.score = "8.5"
	tt := ui.NewTester(a.view, 1120, 760)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if !tt.HasText("豆瓣 8.5") {
		t.Fatalf("known score missing: %v", tt.Texts())
	}
	// A title with no known score shows no score at all.
	a.title.score = ""
	tt.Frame()
	for _, text := range tt.Texts() {
		if strings.Contains(text, "豆瓣") {
			t.Fatalf("unknown score invented one: %v", tt.Texts())
		}
	}
}

func TestDoubanScoreLookup(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	a.discovery.hot.feed = douban.Feed{Subjects: []douban.Subject{
		{Title: "演示热播", Rating: "8.8"},
	}}
	a.discovery.movie.feed = douban.Feed{Subjects: []douban.Subject{
		{Title: "演示电影", Rating: "8.6"},
		{Title: "没有评分", Rating: ""},
	}}
	a.discovery.tv.feed = douban.Feed{Subjects: []douban.Subject{
		{Title: "演示剧集", Rating: "9.1"},
	}}
	a.discovery.top250.feed = douban.Feed{Subjects: []douban.Subject{
		{Title: "演示经典", Rating: "9.7", Rank: 1},
	}}
	if got := a.doubanScore("演示热播"); got != "8.8" {
		t.Fatalf("hot list score = %q, want 8.8", got)
	}
	if got := a.doubanScore("演示电影"); got != "8.6" {
		t.Fatalf("movie list score = %q, want 8.6", got)
	}
	if got := a.doubanScore("演示剧集"); got != "9.1" {
		t.Fatalf("tv list score = %q, want 9.1", got)
	}
	if got := a.doubanScore("演示经典"); got != "9.7" {
		t.Fatalf("top250 list score = %q, want 9.7", got)
	}
	if got := a.doubanScore("没有评分"); got != "" {
		t.Fatalf("unrated title score = %q, want empty", got)
	}
	if got := a.doubanScore("不在榜上"); got != "" {
		t.Fatalf("unknown title score = %q, want empty", got)
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

func TestAutoOpenDetailHedgesSlowPrimaryAndSkipsBackupWhenFast(t *testing.T) {
	origDelay := detailHedgeDelay
	detailHedgeDelay = 30 * time.Millisecond
	t.Cleanup(func() { detailHedgeDelay = origDelay })

	t.Run("slow primary loses to hedged backup and cancels", func(t *testing.T) {
		primaryCanceled := make(chan struct{}, 1)
		var backupHits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/slow":
				<-r.Context().Done()
				primaryCanceled <- struct{}{}
			case "/fast":
				backupHits.Add(1)
				fmt.Fprint(w, `{"list":[{"vod_id":"2","vod_name":"演示剧","vod_play_from":"快线","vod_play_url":"第1集$https://example.com/1.mp4"}]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		doc := &config.Config{URL: srv.URL + "/config"}
		seeds := []library.Item{
			{Entry: library.Entry{Config: doc, Site: config.Site{Key: "slow", Name: "慢源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/slow"}}, Video: catalog.Video{ID: "1", Name: "演示剧"}},
			{Entry: library.Entry{Config: doc, Site: config.Site{Key: "fast", Name: "快源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/fast"}}, Video: catalog.Video{ID: "2", Name: "演示剧"}},
		}
		a := &app{}
		defer a.close()
		a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}
		a.openTitle("演示剧", "", seeds)
		if a.title == nil || a.title.detail == nil || a.title.item.Entry.Site.Key != "fast" {
			t.Fatalf("hedged backup did not win: title=%+v", a.title)
		}
		if backupHits.Load() != 1 {
			t.Fatalf("backupHits=%d, want 1", backupHits.Load())
		}
		select {
		case <-primaryCanceled:
		case <-time.After(time.Second):
			t.Fatal("slow primary request was not canceled after backup won")
		}
	})

	t.Run("fast primary never touches backup", func(t *testing.T) {
		var backupHits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/backup" {
				backupHits.Add(1)
			}
			fmt.Fprint(w, `{"list":[{"vod_id":"1","vod_name":"演示剧","vod_play_from":"主线","vod_play_url":"第1集$https://example.com/1.mp4"}]}`)
		}))
		defer srv.Close()

		doc := &config.Config{URL: srv.URL + "/config"}
		seeds := []library.Item{
			{Entry: library.Entry{Config: doc, Site: config.Site{Key: "primary", Name: "主源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/primary"}}, Video: catalog.Video{ID: "1", Name: "演示剧"}},
			{Entry: library.Entry{Config: doc, Site: config.Site{Key: "backup", Name: "备源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/backup"}}, Video: catalog.Video{ID: "2", Name: "演示剧"}},
		}
		a := &app{}
		defer a.close()
		a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}
		a.openTitle("演示剧", "", seeds)
		if a.title == nil || a.title.detail == nil || a.title.item.Entry.Site.Key != "primary" {
			t.Fatalf("fast primary did not win: title=%+v", a.title)
		}
		if backupHits.Load() != 0 {
			t.Fatalf("backup was requested unnecessarily: hits=%d", backupHits.Load())
		}
	})

	t.Run("both failing falls through to third candidate", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/third" {
				fmt.Fprint(w, `{"list":[{"vod_id":"3","vod_name":"演示剧","vod_play_from":"三线","vod_play_url":"第1集$https://example.com/3.mp4"}]}`)
				return
			}
			http.Error(w, "down", http.StatusBadGateway)
		}))
		defer srv.Close()

		doc := &config.Config{URL: srv.URL + "/config"}
		seeds := []library.Item{
			{Entry: library.Entry{Config: doc, Site: config.Site{Key: "bad1", Name: "坏源1", Type: 1, Kind: config.KindJSON, API: srv.URL + "/bad1"}}, Video: catalog.Video{ID: "1", Name: "演示剧"}},
			{Entry: library.Entry{Config: doc, Site: config.Site{Key: "bad2", Name: "坏源2", Type: 1, Kind: config.KindJSON, API: srv.URL + "/bad2"}}, Video: catalog.Video{ID: "2", Name: "演示剧"}},
			{Entry: library.Entry{Config: doc, Site: config.Site{Key: "third", Name: "三源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/third"}}, Video: catalog.Video{ID: "3", Name: "演示剧"}},
		}
		a := &app{}
		defer a.close()
		a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}
		a.openTitle("演示剧", "", seeds)
		if a.title == nil || a.title.detail == nil || a.title.item.Entry.Site.Key != "third" {
			t.Fatalf("third candidate did not open after first two failed: title=%+v", a.title)
		}
		if len(a.title.failed) != 2 {
			t.Fatalf("failed count = %d, want 2: %+v", len(a.title.failed), a.title.failed)
		}
	})
}

func TestSwitchingSourcesPreservesTitleHeaderMetadataAndFillsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s1":
			fmt.Fprint(w, `{"list":[{"vod_id":"1","vod_name":"演示剧","vod_pic":"https://example.com/site1.jpg","vod_year":"2026","vod_class":"科幻","vod_content":"首源完整简介。","vod_play_from":"线路A","vod_play_url":"第1集$https://example.com/a1.mp4"}]}`)
		case "/s2":
			fmt.Fprint(w, `{"list":[{"vod_id":"2","vod_name":"演示剧","vod_pic":"https://example.com/site2-blur.jpg","vod_year":"","vod_class":"广告分类","vod_content":"广告简介不应覆盖。","vod_play_from":"线路B","vod_play_url":"第1集$https://example.com/b1.mp4#第2集$https://example.com/b2.mp4"}]}`)
		case "/empty":
			fmt.Fprint(w, `{"list":[{"vod_id":"3","vod_name":"缺信息剧","vod_pic":"","vod_year":"","vod_class":"","vod_content":"","vod_play_from":"线路C","vod_play_url":"第1集$https://example.com/c1.mp4"}]}`)
		case "/rich":
			fmt.Fprint(w, `{"list":[{"vod_id":"4","vod_name":"缺信息剧","vod_pic":"https://example.com/rich.jpg","vod_year":"2025","vod_class":"悬疑","vod_content":"补齐后的简介。","vod_play_from":"线路D","vod_play_url":"第1集$https://example.com/d1.mp4"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	doc := &config.Config{URL: srv.URL + "/config"}
	s1 := library.Item{Entry: library.Entry{Config: doc, Site: config.Site{Key: "s1", Name: "来源1", Type: 1, Kind: config.KindJSON, API: srv.URL + "/s1"}}, Video: catalog.Video{ID: "1", Name: "演示剧"}}
	s2 := library.Item{Entry: library.Entry{Config: doc, Site: config.Site{Key: "s2", Name: "来源2", Type: 1, Kind: config.KindJSON, API: srv.URL + "/s2"}}, Video: catalog.Video{ID: "2", Name: "演示剧"}}

	// 1. Opening with a Douban poster locks the poster and keeps first source's metadata on switch.
	a := &app{}
	defer a.close()
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}
	a.openTitleWithScore("演示剧", "https://example.com/douban.jpg", []library.Item{s1, s2}, "8.9")
	if a.title.detail == nil || a.title.detail.Pic != "https://example.com/douban.jpg" || a.title.detail.Year != "2026" || a.title.detail.Content != "首源完整简介。" {
		t.Fatalf("initial detail metadata wrong: %+v", a.title.detail)
	}
	a.title.auto = false
	a.openCandidate(s2)
	if a.title.detail.Pic != "https://example.com/douban.jpg" || a.title.detail.Year != "2026" || a.title.detail.Type != "科幻" || a.title.detail.Content != "首源完整简介。" {
		t.Fatalf("switching source overwrote header metadata: %+v", a.title.detail)
	}
	if len(a.title.detail.Sources) != 1 || a.title.detail.Sources[0].Name != "线路B" || len(a.title.detail.Sources[0].Episodes) != 2 {
		t.Fatalf("switching source did not update lines/episodes: %+v", a.title.detail.Sources)
	}

	// 2. When initial header fields are missing, switching to a richer source fills them in.
	empty := library.Item{Entry: library.Entry{Config: doc, Site: config.Site{Key: "empty", Name: "空源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/empty"}}, Video: catalog.Video{ID: "3", Name: "缺信息剧"}}
	rich := library.Item{Entry: library.Entry{Config: doc, Site: config.Site{Key: "rich", Name: "完整源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/rich"}}, Video: catalog.Video{ID: "4", Name: "缺信息剧"}}
	b := &app{}
	defer b.close()
	b.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}
	b.openTitle("缺信息剧", "", []library.Item{empty, rich})
	if b.title.detail.Pic != "" || b.title.detail.Year != "" || b.title.detail.Content != "" {
		t.Fatalf("expected empty initial metadata: %+v", b.title.detail)
	}
	b.title.auto = false
	b.openCandidate(rich)
	if b.title.pic != "https://example.com/rich.jpg" || b.title.detail.Pic != "https://example.com/rich.jpg" || b.title.detail.Year != "2025" || b.title.detail.Type != "悬疑" || b.title.detail.Content != "补齐后的简介。" {
		t.Fatalf("richer source did not fill missing header metadata: pic=%q detail=%+v", b.title.pic, b.title.detail)
	}
	b.openCandidate(empty)
	if b.title.detail.Pic != "https://example.com/rich.jpg" || b.title.detail.Year != "2025" || b.title.detail.Content != "补齐后的简介。" {
		t.Fatalf("switching back to empty source wiped filled metadata: %+v", b.title.detail)
	}
}
