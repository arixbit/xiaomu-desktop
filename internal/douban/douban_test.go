package douban

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

type localTransport struct {
	base      *url.URL
	transport http.RoundTripper
}

func (tr localTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = tr.base.Scheme, tr.base.Host
	clone.URL = &u
	return tr.transport.RoundTrip(clone)
}

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := New(t.TempDir())
	c.HTTP = &http.Client{Transport: localTransport{u, srv.Client().Transport}}
	return c
}

func TestFetchAllCollectionsAndPersistIndependently(t *testing.T) {
	wantPaths := map[Kind]string{
		Hot:         "/rexxar/api/v2/subject_collection/subject_real_time_hotest/items",
		Movie:       "/rexxar/api/v2/subject_collection/movie_hot_gaia/items",
		TV:          "/rexxar/api/v2/subject_collection/tv_hot/items",
		Show:        "/rexxar/api/v2/subject_collection/show_hot/items",
		Animation:   "/rexxar/api/v2/subject_collection/tv_animation/items",
		ShortDrama:  "/rexxar/api/v2/subject/recent_hot/tv",
		Documentary: "/rexxar/api/v2/subject_collection/tv_documentary/items",
		Top250:      "/rexxar/api/v2/subject_collection/movie_top250/items",
	}
	seenPaths := map[string]bool{}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		wantReferer := "https://m.douban.com/"
		if r.URL.Path == wantPaths[ShortDrama] {
			wantReferer = "https://movie.douban.com/tv/"
			if q.Get("start") != "0" || q.Get("limit") != "24" || q.Get("category") != "tv" || q.Get("type") != "tv_short_drama" || q.Get("include_short_drama") != "true" {
				t.Errorf("unexpected short drama query: %s", r.URL)
			}
		} else if q.Get("start") != "0" || q.Get("count") != "24" {
			t.Errorf("unexpected query: %s", r.URL)
		}
		if r.Header.Get("Referer") != wantReferer || r.Header.Get("User-Agent") != "Mozilla/5.0" {
			t.Errorf("headers: %v (want referer %q)", r.Header, wantReferer)
		}
		seenPaths[r.URL.Path] = true
		switch r.URL.Path {
		case wantPaths[ShortDrama]:
			fmt.Fprint(w, `{"total":100,"items":[
				{"id":"short-1","title":"热门短剧演示","type":"tv","pic":{"large":"https://example.com/short.jpg"},"rating":{"value":7.8}},
				{"id":"playlist-1","title":"非影视片单","type":"playlist"}
			]}`)
		case wantPaths[Movie]:
			// movie_hot_gaia uses cover.url instead of pic.
			fmt.Fprint(w, `{"total":300,"subject_collection_items":[
				{"id":"1","title":"movie","type":"movie","cover":{"url":"https://example.com/movie.jpg"},"rating":{"value":8.5}},
				{"id":"2","title":"暂未评分","type":"movie","rating":{"value":0},"episodes_info":"更新至12集"},
				{"id":"1","title":"重复","type":"movie"},
				{"id":"","title":"无效条目","type":"movie"},
				{"id":"3","title":" ","type":"movie"}
			]}`)
		case wantPaths[Hot]:
			// subject_real_time_hotest can include non-video items such as books/music.
			fmt.Fprint(w, `{"total":20,"subject_collection_items":[
				{"id":"10","title":"hot","type":"tv","pic":{"large":"https://example.com/hot.jpg"},"rating":{"value":8.7},"rank":1},
				{"id":"11","title":"一本热书","type":"book","rating":{"value":9.1},"rank":2},
				{"id":"12","title":"一首热歌","type":"music","rating":{"value":8.8},"rank":3}
			]}`)
		case wantPaths[Top250]:
			fmt.Fprint(w, `{"total":250,"subject_collection_items":[
				{"id":"250","title":"top250","type":"movie","cover_url":"https://example.com/top.jpg","rating":{"value":9.7},"rank":1}
			]}`)
		default:
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rexxar/api/v2/subject_collection/"), "/items")
			fmt.Fprintf(w, `{"total":10,"subject_collection_items":[{"id":%q,"title":%q,"type":"tv","pic":{"normal":"https://example.com/p.jpg"},"rating":{"value":8.1},"episodes_info":"更新至8集"}]}`, name, name)
		}
	})

	for _, kind := range AllKinds() {
		feed, err := c.Fetch(context.Background(), kind)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !seenPaths[wantPaths[kind]] {
			t.Fatalf("%s did not hit %s", kind, wantPaths[kind])
		}
		if err := c.Save(feed); err != nil {
			t.Fatalf("%s save: %v", kind, err)
		}
		cached := New(c.CacheDir).Cached(kind)
		if !cached.Fresh(time.Now()) || len(cached.Subjects) != len(feed.Subjects) {
			t.Fatalf("%s cache=%+v", kind, cached)
		}
		info, err := os.Stat(c.cachePath(kind))
		if err != nil {
			t.Fatal(err)
		}
		// Windows has no Unix permission bits to check.
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			t.Fatal("cache permissions are too broad")
		}
	}

	movie := c.Cached(Movie)
	if len(movie.Subjects) != 2 || movie.Subjects[0].Cover != "https://example.com/movie.jpg" || movie.Subjects[0].Rating != "8.5" || movie.Subjects[1].Rating != "" || movie.Subjects[1].Episodes != "更新至12集" || !movie.HasMore() {
		t.Fatalf("movie=%+v", movie)
	}
	hot := c.Cached(Hot)
	if len(hot.Subjects) != 1 || hot.Subjects[0].Title != "hot" || hot.Subjects[0].Rank != 1 || hot.Next != 3 || !hot.HasMore() {
		t.Fatalf("hot=%+v", hot)
	}
	short := c.Cached(ShortDrama)
	if len(short.Subjects) != 1 || short.Subjects[0].Title != "热门短剧演示" || short.Subjects[0].Rating != "7.8" || short.Next != 2 || !short.HasMore() {
		t.Fatalf("short=%+v", short)
	}
	top := c.Cached(Top250)
	if len(top.Subjects) != 1 || top.Subjects[0].Cover != "https://example.com/top.jpg" || top.Subjects[0].Rank != 1 || top.Subjects[0].Rating != "9.7" || !top.HasMore() {
		t.Fatalf("top250=%+v", top)
	}
	if len(HotKinds()) != len(AllKinds())-1 {
		t.Fatalf("HotKinds=%v AllKinds=%v", HotKinds(), AllKinds())
	}
}

func TestFetchPageAndMergePagination(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("start") {
		case "0":
			fmt.Fprint(w, `{"total":3,"subject_collection_items":[
				{"id":"1","title":"第一部","type":"movie","rating":{"value":9.7},"rank":1},
				{"id":"2","title":"第二部","type":"movie","rating":{"value":9.6},"rank":2}
			]}`)
		case "2":
			fmt.Fprint(w, `{"total":3,"subject_collection_items":[
				{"id":"2","title":"第二部重复","type":"movie","rating":{"value":9.6},"rank":2},
				{"id":"3","title":"第三部","type":"movie","rating":{"value":9.5},"rank":3}
			]}`)
		default:
			fmt.Fprint(w, `{"total":3,"subject_collection_items":[]}`)
		}
	})

	p1, err := c.Fetch(context.Background(), Top250)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Subjects) != 2 || p1.Next != 2 || p1.Total != 3 || !p1.HasMore() {
		t.Fatalf("p1=%+v", p1)
	}
	origUpdated := p1.Updated

	p2, err := c.FetchPage(context.Background(), Top250, p1.Next)
	if err != nil {
		t.Fatal(err)
	}
	merged := MergePage(p1, p2)
	if len(merged.Subjects) != 3 || merged.Subjects[2].ID != "3" || merged.Subjects[2].Rank != 3 {
		t.Fatalf("merged=%+v", merged)
	}
	if !merged.Updated.Equal(origUpdated) {
		t.Fatalf("merge altered original refresh timestamp: %v vs %v", merged.Updated, origUpdated)
	}
	if merged.HasMore() {
		t.Fatalf("expected pagination exhausted: %+v", merged)
	}

	// Empty trailing page succeeds cleanly without error and keeps HasMore false.
	emptyPage, err := c.FetchPage(context.Background(), Top250, 99)
	if err != nil {
		t.Fatalf("empty trailing page errored: %v", err)
	}
	done := MergePage(merged, emptyPage)
	if done.HasMore() || len(done.Subjects) != 3 {
		t.Fatalf("done=%+v", done)
	}
}

func TestBadResponsesLeavePreviousCacheIntact(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
		status           int
	}{
		{"rate limited", "", "HTTP 429", 429},
		{"html validation", "<html>verify</html>", "网页验证", 200},
		{"malformed", "{broken", "无法读取", 200},
		{"empty", `{"subject_collection_items":[]}`, "暂未返回", 200},
		{"only non-video", `{"subject_collection_items":[{"id":"b1","title":"书","type":"book"}]}`, "暂未返回", 200},
		{"missing list", `{"error":"blocked"}`, "暂未返回", 200},
		{"oversized", strings.Repeat(" ", maxBytes+1), "过大", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status); fmt.Fprint(w, tt.body) })
			old := Feed{Kind: Movie, Updated: time.Now().Add(-24 * time.Hour), Subjects: []Subject{{ID: "cached", Title: "上次的推荐"}}}
			if err := c.Save(old); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Fetch(context.Background(), Movie); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v", err)
			}
			cached := c.Cached(Movie)
			if len(cached.Subjects) != 1 || cached.Subjects[0].ID != "cached" || cached.Fresh(time.Now()) {
				t.Fatalf("cache overwritten: %+v", cached)
			}
		})
	}
}

func TestCacheRecoveryAndCanceledRequest(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	if err := os.WriteFile(c.cachePath(Movie), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if cached := c.Cached(Movie); len(cached.Subjects) != 0 {
		t.Fatal("corrupt cache accepted")
	}
	feed := Feed{Kind: Movie, Updated: time.Now(), Subjects: []Subject{{ID: "demo", Title: "演示"}}}
	if err := c.Save(feed); err != nil {
		t.Fatal(err)
	}
	if len(c.Cached(Movie).Subjects) != 1 {
		t.Fatal("cache did not recover")
	}
	if err := c.Save(Feed{Kind: Movie}); err == nil {
		t.Fatal("empty feed overwrote cache")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Fetch(ctx, Movie); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation=%v", err)
	}
}
