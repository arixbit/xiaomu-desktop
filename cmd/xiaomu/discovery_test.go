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

func TestDoubanCardsSearchUserLibraryAndPlay(t *testing.T) {
	var movies, series atomic.Int32
	var base string
	client, raw := metadataTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/j/search_subjects":
			title := "演示电影"
			if r.URL.Query().Get("type") == "tv" {
				title = "演示剧集"
				series.Add(1)
			} else {
				movies.Add(1)
			}
			fmt.Fprintf(w, `{"subjects":[{"id":"douban-id","title":%q,"rate":"8.6","episodes_info":"更新至12集"}]}`, title)
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
				fmt.Fprintf(w, `{"list":[{"vod_name":"演示电影","vod_play_from":"直连","vod_play_url":%q}]}`, "第1集$"+base+"/clip.mp4")
				return
			}
			if q.Get("wd") != "" && q.Get("wd") != "演示电影" {
				t.Errorf("wrong search query: %s", q.Get("wd"))
			}
			fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"site-id","vod_name":"演示电影"}]}`)
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
	tt := ui.NewTester(a.view, 960, 640)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q: %v; texts=%v", label, err, tt.Texts())
		}
	}
	if !tt.HasText("演示电影") || !tt.HasText("豆瓣 8.6") {
		t.Fatalf("missing Douban metadata: %v", tt.Texts())
	}
	click("热播剧集")
	if !tt.HasText("演示剧集") {
		t.Fatal(tt.Texts())
	}
	click("热播电影")
	if movies.Load() != 1 || series.Load() != 1 {
		t.Fatal("switching tabs repeated fresh requests")
	}
	// A poster goes straight to the title: its sources are searched for and
	// the first one opens without a results page in between.
	click("查找影片 演示电影")
	search := &a.library.search
	if a.route() != routeDetail || search.query != "演示电影" || len(search.items) != 1 {
		t.Fatalf("route=%v search=%q results=%d", a.route(), search.query, len(search.items))
	}
	if a.title == nil || a.title.detail == nil {
		t.Fatalf("the title's only source did not open: %s; texts=%v", a.status, tt.Texts())
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
	if a.route() != routeHome || !tt.HasText("豆瓣 8.6") || movies.Load() != 1 {
		t.Fatalf("home did not restore: %v", tt.Texts())
	}
	// A fresh app instance reuses the disk cache even with a failing network.
	reopened := &app{}
	defer reopened.close()
	reopened.discovery.client = douban.New(client.CacheDir)
	reopened.discovery.client.HTTP = &http.Client{Transport: failingMetadataTransport{}}
	reopened.loadDiscoveryTab(false)
	if len(reopened.discovery.movie.feed.Subjects) != 1 || reopened.discovery.movie.message != "" {
		t.Fatal("restart lost fresh cached feed")
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
		fmt.Fprint(w, `{"subjects":[{"id":"new","title":"新推荐","rate":"9.0"}]}`)
	})
	old := douban.Feed{Kind: douban.Movie, Updated: time.Now().Add(-24 * time.Hour), Subjects: []douban.Subject{{ID: "old", Title: "旧推荐"}}}
	if err := client.Save(old); err != nil {
		t.Fatal(err)
	}
	a := &app{}
	a.discovery.client = client
	defer a.close()
	a.loadDiscoveryTab(false)
	if a.discovery.movie.feed.Subjects[0].Title != "旧推荐" || a.discovery.movie.message == "" {
		t.Fatal("stale feed was discarded")
	}
	if !client.Cached(douban.Movie).Updated.Equal(old.Updated) {
		t.Fatal("failed refresh changed cache timestamp")
	}
	failed.Store(false)
	a.loadDiscoveryTab(true)
	if a.discovery.movie.feed.Subjects[0].Title != "新推荐" || a.discovery.movie.message != "" {
		t.Fatal("retry did not recover")
	}
	if len(a.discovery.tv.feed.Subjects) != 0 {
		t.Fatal("movie refresh populated TV feed")
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
	if a.discovery.movie.message != "" || len(a.discovery.movie.feed.Subjects) != 0 {
		t.Fatal("closed window accepted an update")
	}
}

func TestFreshCacheRendersBeforeRefreshCompletes(t *testing.T) {
	// The forced refresh stays blocked until cancellation; cached posters must
	// reach the UI before that request completes.
	client, _ := metadataTestClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	feed := douban.Feed{Kind: douban.Movie, Updated: time.Now(), Subjects: []douban.Subject{{ID: "cached", Title: "缓存影片"}}}
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
	if !a.discovery.movie.loading || len(a.discovery.movie.feed.Subjects) != 1 {
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
