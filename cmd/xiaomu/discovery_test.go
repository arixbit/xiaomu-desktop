package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/douban"
	"github.com/egoist/mygo/ui"
)

type metadataTestTransport struct {
	base *url.URL
	next http.RoundTripper
}

func (tr metadataTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = tr.base.Scheme, tr.base.Host
	clone.URL = &u
	return tr.next.RoundTrip(clone)
}

func metadataTestClient(t *testing.T, handler http.HandlerFunc) (*douban.Client, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := douban.New(t.TempDir())
	c.HTTP = &http.Client{Transport: metadataTestTransport{u, srv.Client().Transport}}
	return c, srv.URL
}

func TestDiscoveryGridArrowsMoveWithoutOpening(t *testing.T) {
	a := &app{}
	a.library.root = &config.Config{URL: "https://example.com/config"}
	a.library.cached = map[string]*config.Config{}
	a.discovery.hot.feed = douban.Feed{Subjects: []douban.Subject{
		{ID: "1", Title: "影片一"},
		{ID: "2", Title: "影片二"},
	}}
	defer a.close()
	tt := ui.NewTester(a.view, 960, 640)
	if !tt.HasText("影片一") || !tt.HasText("影片二") {
		t.Fatalf("home feed did not render: %v", tt.Texts())
	}
	a.discovery.hot.grid.Handle.Focus()
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	if a.discovery.hot.selected != 0 || a.route() != routeHome {
		t.Fatalf("Down did not move the choice: selected=%d route=%v", a.discovery.hot.selected, a.route())
	}
	// Down moves a whole row, Right moves to the next poster in the row.
	tt.Key(0, ui.KeyRight)
	if a.discovery.hot.selected != 1 {
		t.Fatalf("Right did not move the choice: selected=%d", a.discovery.hot.selected)
	}
	tt.Key(0, ui.KeyEnter)
	if a.route() != routeDetail || a.title == nil {
		t.Fatalf("Enter did not open the choice: route=%v title=%+v", a.route(), a.title)
	}
}

func TestDoubanCardsSearchUserLibraryAndPlay(t *testing.T) {
	var hits [8]atomic.Int32
	var base string
	client, raw := metadataTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rexxar/api/v2/subject_collection/subject_real_time_hotest/items":
			hits[0].Add(1)
			fmt.Fprint(w, `{"total":1,"subject_collection_items":[{"id":"hot-id","title":"演示热播","type":"movie","rating":{"value":8.6},"rank":1}]}`)
		case "/rexxar/api/v2/subject_collection/movie_hot_gaia/items":
			hits[1].Add(1)
			fmt.Fprint(w, `{"total":48,"subject_collection_items":[{"id":"movie-id","title":"演示电影","type":"movie","rating":{"value":8.4}}]}`)
		case "/rexxar/api/v2/subject_collection/tv_hot/items":
			hits[2].Add(1)
			fmt.Fprint(w, `{"total":48,"subject_collection_items":[{"id":"tv-id","title":"演示剧集","type":"tv","rating":{"value":8.9},"episodes_info":"更新至12集"}]}`)
		case "/rexxar/api/v2/subject_collection/show_hot/items":
			hits[3].Add(1)
			fmt.Fprint(w, `{"total":30,"subject_collection_items":[{"id":"show-id","title":"演示综艺","type":"tv","rating":{"value":8.7}}]}`)
		case "/rexxar/api/v2/subject_collection/tv_animation/items":
			hits[4].Add(1)
			fmt.Fprint(w, `{"total":1,"subject_collection_items":[{"id":"ani-id","title":"演示动漫","type":"tv","rating":{"value":9.0}}]}`)
		case "/rexxar/api/v2/subject/recent_hot/tv":
			hits[5].Add(1)
			fmt.Fprint(w, `{"total":100,"items":[{"id":"short-id","title":"演示短剧","type":"tv","rating":{"value":7.8}}]}`)
		case "/rexxar/api/v2/subject_collection/tv_documentary/items":
			hits[6].Add(1)
			fmt.Fprint(w, `{"total":1,"subject_collection_items":[{"id":"doc-id","title":"演示纪录片","type":"tv","rating":{"value":9.2}}]}`)
		case "/rexxar/api/v2/subject_collection/movie_top250/items":
			hits[7].Add(1)
			fmt.Fprint(w, `{"total":250,"subject_collection_items":[{"id":"top-id","title":"演示经典","type":"movie","rating":{"value":9.7},"rank":1}]}`)
		case "/config":
			fmt.Fprint(w, `{"sites":[{"key":"local","name":"本地站点","type":1,"api":"/api"}]}`)
		case "/api":
			q := r.URL.Query()
			if q.Get("ac") == "list" {
				fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
				return
			}
			if q.Get("ids") != "" {
				if q.Get("ids") != "site-id" {
					t.Errorf("Douban metadata ID used as playback ID: %s", q.Get("ids"))
				}
				fmt.Fprintf(w, `{"list":[{"vod_name":"演示热播","vod_play_from":"直连","vod_play_url":%q}]}`, "第1集$"+base+"/clip.mp4")
				return
			}
			if q.Get("wd") != "" && q.Get("wd") != "演示热播" {
				t.Errorf("wrong search query: %s", q.Get("wd"))
			}
			fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"site-id","vod_name":"演示热播"}]}`)
		case "/clip.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			fmt.Fprint(w, "fixture video")
		default:
			http.NotFound(w, r)
		}
	})
	base = raw
	a := &app{}
	a.discovery.client = client
	defer a.close()
	a.loadConfig(raw + "/config")
	tt := ui.NewTester(a.view, 1120, 760)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q: %v; texts=%v", label, err, tt.Texts())
		}
	}
	// Default tab lands on 实时热门 and hides 加载更多 when total is exhausted.
	if !tt.HasText("演示热播") || !tt.HasText("豆瓣 8.6") || tt.HasText("加载更多") {
		t.Fatalf("missing default hot metadata or unexpected load-more: %v", tt.Texts())
	}
	for _, tc := range []struct {
		tab, title, badge string
		hasMore           bool
	}{
		{"热门电影", "演示电影", "豆瓣 8.4", true},
		{"热门剧集", "演示剧集", "豆瓣 8.9", true},
		{"热门综艺", "演示综艺", "豆瓣 8.7", true},
		{"热门动漫", "演示动漫", "豆瓣 9.0", false},
		{"热门短剧", "演示短剧", "豆瓣 7.8", true},
		{"热门纪录片", "演示纪录片", "豆瓣 9.2", false},
		{"Top 250", "演示经典", "#1 · 豆瓣 9.7", true},
	} {
		click(tc.tab)
		if !tt.HasText(tc.title) || !tt.HasText(tc.badge) || tt.HasText("加载更多") != tc.hasMore {
			t.Fatalf("tab %q mismatch: want title=%q badge=%q hasMore=%v; texts=%v", tc.tab, tc.title, tc.badge, tc.hasMore, tt.Texts())
		}
	}
	click("实时热门")
	for i := range hits {
		if hits[i].Load() != 1 {
			t.Fatalf("tab %d repeated or missed fresh request: hits=%d", i, hits[i].Load())
		}
	}
	// A poster goes straight to the title: its sources are searched for and
	// the first one opens without a results page in between. The click goes
	// by coordinates: the poster image covers the card, so a label click
	// would land on the image rather than the card behind it.
	poster, ok := tt.Find("查找影片 演示热播")
	if !ok {
		t.Fatalf("poster label missing: %v", tt.Texts())
	}
	tt.ClickAt(poster.X+poster.W/2, poster.Y+poster.H/2)
	search := &a.library.search
	if a.route() != routeDetail || search.query != "演示热播" || len(search.items) != 1 {
		t.Fatalf("route=%v search=%q results=%d", a.route(), search.query, len(search.items))
	}
	if a.title == nil || a.title.detail == nil {
		t.Fatalf("the title's only source did not open: %s; texts=%v", a.status, tt.Texts())
	}
	// The card's score carries into the detail header.
	if !tt.HasText("豆瓣 8.6") {
		t.Fatalf("detail header did not carry the card score: %v", tt.Texts())
	}
	opened := false
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		response, err := http.Get(raw)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		if string(data) != "fixture video" {
			t.Fatalf("media=%s", data)
		}
		opened = true
		return nil, nil
	}
	click("1")
	if !opened {
		t.Fatal(a.status)
	}
	click("返回")
	if a.route() != routeHome || !tt.HasText("豆瓣 8.6") || hits[0].Load() != 1 {
		t.Fatalf("home did not restore: %v", tt.Texts())
	}
	// A fresh app instance reuses the disk cache even with a failing network.
	reopened := &app{}
	defer reopened.close()
	reopened.discovery.client = douban.New(client.CacheDir)
	reopened.discovery.client.HTTP = &http.Client{Transport: failingMetadataTransport{}}
	reopened.loadDiscoveryTab(false)
	if len(reopened.discovery.hot.feed.Subjects) != 1 || reopened.discovery.hot.message != "" {
		t.Fatal("restart lost fresh cached feed")
	}
}

func TestDoubanLoadMorePaginationRetryAndForceRefresh(t *testing.T) {
	var failPage2 atomic.Bool
	failPage2.Store(true)
	client, _ := metadataTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rexxar/api/v2/subject_collection/movie_top250/items" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("start") {
		case "0":
			fmt.Fprint(w, `{"total":4,"subject_collection_items":[
				{"id":"1","title":"经典一","type":"movie","rating":{"value":9.7},"rank":1},
				{"id":"2","title":"经典二","type":"movie","rating":{"value":9.6},"rank":2}
			]}`)
		case "2":
			if failPage2.Load() {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"total":4,"subject_collection_items":[
				{"id":"3","title":"经典三","type":"movie","rating":{"value":9.5},"rank":3},
				{"id":"4","title":"经典四","type":"movie","rating":{"value":9.4},"rank":4}
			]}`)
		default:
			fmt.Fprint(w, `{"total":4,"subject_collection_items":[]}`)
		}
	})
	a := &app{}
	a.library.root = &config.Config{URL: "https://example.com/config"}
	a.library.cached = map[string]*config.Config{}
	a.discovery.client = client
	a.discovery.tab = string(douban.Top250)
	defer a.close()
	a.loadDiscoveryTab(false)

	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("经典一") || !tt.HasText("#1 · 豆瓣 9.7") || !tt.HasText("已显示 2 / 4") || !tt.HasText("加载更多") {
		t.Fatalf("page 1 state wrong: %v", tt.Texts())
	}
	if err := tt.Click("加载更多"); err != nil {
		t.Fatal(err)
	}
	// Failing page 2 keeps page 1 intact and offers a retry for page 2.
	if len(a.discovery.top250.feed.Subjects) != 2 || !a.discovery.top250.moreFailed || !tt.HasText("加载更多失败，可重试。") {
		t.Fatalf("failed page 2 broke state: len=%d moreFailed=%v texts=%v", len(a.discovery.top250.feed.Subjects), a.discovery.top250.moreFailed, tt.Texts())
	}
	failPage2.Store(false)
	tt.Advance(300 * time.Millisecond)
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	if len(a.discovery.top250.feed.Subjects) != 4 || !tt.HasText("经典四") || !tt.HasText("#4 · 豆瓣 9.4") || tt.HasText("加载更多") {
		t.Fatalf("retry did not append page 2 cleanly: len=%d texts=%v", len(a.discovery.top250.feed.Subjects), tt.Texts())
	}
	if cached := client.Cached(douban.Top250); len(cached.Subjects) != 4 || cached.HasMore() {
		t.Fatalf("merged pages were not cached: %+v", cached)
	}

	// Force-refreshing while load-more is in flight must cancel and ignore the stale load-more.
	started := make(chan struct{}, 1)
	client2, _ := metadataTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("start") {
		case "2":
			started <- struct{}{}
			<-r.Context().Done()
		default:
			fmt.Fprint(w, `{"total":4,"subject_collection_items":[{"id":"fresh","title":"最新第一","type":"movie","rating":{"value":9.8},"rank":1}]}`)
		}
	})
	updates := make(chan func(), 8)
	b := &app{dispatch: func(fn func()) { updates <- fn }}
	b.discovery.client = client2
	b.discovery.tab = string(douban.Top250)
	b.discovery.top250.feed = douban.Feed{
		Kind:     douban.Top250,
		Updated:  time.Now(),
		Subjects: []douban.Subject{{ID: "1", Title: "旧一"}, {ID: "2", Title: "旧二"}},
		Total:    4,
		Next:     2,
	}
	defer b.close()
	b.loadMoreDiscovery()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("load-more request did not start")
	}
	b.loadDiscoveryTab(true)
	deadline := time.After(time.Second)
	for b.discovery.top250.loading {
		select {
		case fn := <-updates:
			fn()
		case <-deadline:
			t.Fatal("force refresh did not finish")
		}
	}
	// Drain any stale callback queued by the canceled load-more.
	for len(updates) > 0 {
		(<-updates)()
	}
	if len(b.discovery.top250.feed.Subjects) != 1 || b.discovery.top250.feed.Subjects[0].ID != "fresh" || b.discovery.top250.message != "" {
		t.Fatalf("stale load-more corrupted force refresh: %+v msg=%q", b.discovery.top250.feed, b.discovery.top250.message)
	}
}

func TestDoubanScrollNearBottomAutoLoadsNextPage(t *testing.T) {
	var pageRequests atomic.Int32
	var failPage2 atomic.Bool
	makePage := func(start, count, total int) string {
		var items string
		for i := range count {
			idx := start + i + 1
			if i > 0 {
				items += ","
			}
			items += fmt.Sprintf(`{"id":"%d","title":"经典%d","type":"movie","rating":{"value":9.5},"rank":%d}`, idx, idx, idx)
		}
		return fmt.Sprintf(`{"total":%d,"subject_collection_items":[%s]}`, total, items)
	}
	client, _ := metadataTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rexxar/api/v2/subject_collection/movie_top250/items" {
			http.NotFound(w, r)
			return
		}
		pageRequests.Add(1)
		switch r.URL.Query().Get("start") {
		case "0":
			fmt.Fprint(w, makePage(0, 24, 72))
		case "24":
			if failPage2.Load() {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, makePage(24, 24, 72))
		case "48":
			fmt.Fprint(w, makePage(48, 24, 72))
		default:
			fmt.Fprint(w, `{"total":72,"subject_collection_items":[]}`)
		}
	})

	a := &app{}
	a.library.root = &config.Config{URL: "https://example.com/config"}
	a.library.cached = map[string]*config.Config{}
	a.discovery.client = client
	a.discovery.tab = string(douban.Top250)
	defer a.close()
	a.loadDiscoveryTab(false)

	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	list := &a.discovery.top250
	if list.scroll.MaxY <= 0 {
		t.Fatalf("expected scrollable 24-item grid, got MaxY=%v", list.scroll.MaxY)
	}
	if len(list.feed.Subjects) != 24 || pageRequests.Load() != 1 {
		t.Fatalf("initial top view must not pre-fetch page 2: len=%d reqs=%d", len(list.feed.Subjects), pageRequests.Load())
	}
	if tt.HasText("加载更多") {
		t.Fatalf("scrollable grid should auto-load instead of showing static button: %v", tt.Texts())
	}

	// Scrolling partway down (far from bottom) must not fetch page 2.
	list.scroll.Y = 100
	tt.Frame()
	if len(list.feed.Subjects) != 24 || pageRequests.Load() != 1 {
		t.Fatalf("partial scroll fetched too early: Y=%v MaxY=%v len=%d reqs=%d", list.scroll.Y, list.scroll.MaxY, len(list.feed.Subjects), pageRequests.Load())
	}

	// Scrolling near bottom triggers page 2 automatically; a failure must not loop-retry.
	failPage2.Store(true)
	list.scroll.Y = list.scroll.MaxY - 100
	tt.Frame()
	tt.Frame()
	if !list.moreFailed || len(list.feed.Subjects) != 24 || pageRequests.Load() != 2 {
		t.Fatalf("failed auto-load broke or looped: failed=%v len=%d reqs=%d", list.moreFailed, len(list.feed.Subjects), pageRequests.Load())
	}

	// Retrying succeeds and appends page 2, without immediately double-firing page 3.
	failPage2.Store(false)
	tt.Advance(300 * time.Millisecond)
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if len(list.feed.Subjects) != 48 || pageRequests.Load() != 3 {
		t.Fatalf("page 2 retry did not cleanly append without double-firing page 3: len=%d reqs=%d", len(list.feed.Subjects), pageRequests.Load())
	}

	// Scrolling near the new bottom automatically loads page 3 and then stops at 72/72.
	list.scroll.Y = list.scroll.MaxY - 100
	tt.Frame()
	tt.Frame()
	if len(list.feed.Subjects) != 72 || list.feed.HasMore() || pageRequests.Load() != 4 {
		t.Fatalf("scrolling to new bottom did not load final page: len=%d hasMore=%v reqs=%d", len(list.feed.Subjects), list.feed.HasMore(), pageRequests.Load())
	}
	list.scroll.Y = list.scroll.MaxY
	tt.Frame()
	if pageRequests.Load() != 4 {
		t.Fatalf("exhausted feed sent extra request: reqs=%d", pageRequests.Load())
	}
}

func TestDiscoveryHeaderLayoutWideAndCompact(t *testing.T) {
	a := &app{}
	a.library.root = &config.Config{URL: "https://example.com/config"}
	a.library.cached = map[string]*config.Config{}
	a.discovery.hot.feed = douban.Feed{
		Kind:     douban.Hot,
		Updated:  time.Now(),
		Subjects: []douban.Subject{{ID: "1", Title: "热门演示", Rating: "8.8"}},
	}
	defer a.close()

	for _, width := range []int{1360, 1300, 1120, 1100, 960, 640} {
		a.discovery.tab = string(douban.Hot)
		tt := ui.NewTester(a.view, width, 700)
		tabsEl, ok := tt.Find("榜单")
		if !ok {
			t.Fatalf("width=%d missing tabs control", width)
		}
		lastTab, ok := tt.Find("最近更新")
		if !ok {
			t.Fatalf("width=%d missing last tab", width)
		}
		btn, ok := tt.Find("刷新榜单")
		if !ok {
			t.Fatalf("width=%d missing refresh button", width)
		}
		meta, ok := tt.Find("豆瓣榜单 · 更新于 " + a.discovery.hot.feed.Updated.Local().Format("01-02 15:04"))
		if !ok {
			t.Fatalf("width=%d missing updated timestamp", width)
		}
		if btn.X+btn.W > float32(width)+1 {
			t.Fatalf("width=%d refresh button overflow: btn=%+v", width, btn)
		}
		if tabsEl.X+tabsEl.W > float32(width)+1 {
			t.Fatalf("width=%d tabs overflow: tabs=%+v", width, tabsEl)
		}
		if lastTab.X+lastTab.W > float32(width)+1 {
			t.Fatalf("width=%d last tab outside window: lastTab=%+v", width, lastTab)
		}
		// Updated time and refresh button must not overlap tabs on the same row.
		sameRow := btn.Y < tabsEl.Y+tabsEl.H && tabsEl.Y < btn.Y+btn.H
		if sameRow && (meta.X < tabsEl.X+tabsEl.W-0.5 || btn.X < meta.X+meta.W-0.5) {
			t.Fatalf("width=%d header items overlap: tabs=%+v meta=%+v btn=%+v", width, tabsEl, meta, btn)
		}
		if err := tt.Click("最近更新"); err != nil {
			t.Fatalf("width=%d click last tab: %v", width, err)
		}
		if a.discovery.tab != "sites" {
			t.Fatalf("width=%d clicking last tab did not switch to sites: got %q", width, a.discovery.tab)
		}
	}
}

type failingMetadataTransport struct{}

func (failingMetadataTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("offline fixture")
}

func TestDoubanFailureKeepsCacheAndSupportsRetry(t *testing.T) {
	var failed atomic.Bool
	failed.Store(true)
	client, _ := metadataTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if failed.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"total":1,"subject_collection_items":[{"id":"new","title":"新推荐","type":"movie","rating":{"value":9.0}}]}`)
	})
	old := douban.Feed{Kind: douban.Hot, Updated: time.Now().Add(-24 * time.Hour), Subjects: []douban.Subject{{ID: "old", Title: "旧推荐"}}}
	if err := client.Save(old); err != nil {
		t.Fatal(err)
	}
	a := &app{}
	a.discovery.client = client
	defer a.close()
	a.loadDiscoveryTab(false)
	if a.discovery.hot.feed.Subjects[0].Title != "旧推荐" || a.discovery.hot.message == "" {
		t.Fatal("stale feed was discarded")
	}
	if !client.Cached(douban.Hot).Updated.Equal(old.Updated) {
		t.Fatal("failed refresh changed cache timestamp")
	}
	failed.Store(false)
	a.loadDiscoveryTab(true)
	if a.discovery.hot.feed.Subjects[0].Title != "新推荐" || a.discovery.hot.message != "" {
		t.Fatal("retry did not recover")
	}
	if len(a.discovery.tv.feed.Subjects) != 0 {
		t.Fatal("hot refresh populated TV feed")
	}
}

func TestClosingCancelsDoubanRefreshAndIgnoresQueuedUpdates(t *testing.T) {
	started := make(chan struct{}, 1)
	client, _ := metadataTestClient(t, func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-r.Context().Done() })
	updates := make(chan func(), 8)
	a := &app{dispatch: func(fn func()) { updates <- fn }}
	a.discovery.client = client
	a.loadDiscoveryTab(false)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	a.close()
	select {
	case update := <-updates:
		update()
	case <-time.After(time.Second):
		t.Fatal("request was not canceled")
	}
	if a.discovery.hot.message != "" || len(a.discovery.hot.feed.Subjects) != 0 {
		t.Fatal("closed window accepted an update")
	}
}

func TestFreshCacheRendersBeforeRefreshCompletes(t *testing.T) {
	// The forced refresh stays blocked until cancellation; cached posters must
	// reach the UI before that request completes.
	client, _ := metadataTestClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	feed := douban.Feed{Kind: douban.Hot, Updated: time.Now(), Subjects: []douban.Subject{{ID: "cached", Title: "缓存影片"}}}
	if err := client.Save(feed); err != nil {
		t.Fatal(err)
	}
	updates := make(chan func(), 8)
	a := &app{dispatch: func(fn func()) { updates <- fn }}
	a.discovery.client = client
	defer a.close()
	a.loadDiscoveryTab(true)
	select {
	case update := <-updates:
		update()
	case <-time.After(time.Second):
		t.Fatal("cache was not delivered")
	}
	if !a.discovery.hot.loading || len(a.discovery.hot.feed.Subjects) != 1 {
		t.Fatal("cached feed waited for network")
	}
	a.stopDiscovery()
	select {
	case update := <-updates:
		update()
	case <-time.After(time.Second):
		t.Fatal("refresh did not stop")
	}
}
