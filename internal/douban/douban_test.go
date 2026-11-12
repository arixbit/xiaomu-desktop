package douban

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

func TestFetchHotListsAndPersistIndependently(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/j/search_subjects" || q.Get("tag") != "热门" || q.Get("page_start") != "0" || q.Get("page_limit") != "24" || q.Get("sort") != "recommend" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		if r.Header.Get("Referer") != "https://movie.douban.com/" || r.Header.Get("User-Agent") != "Mozilla/5.0" {
			t.Errorf("headers: %v", r.Header)
		}
		fmt.Fprintf(w, `{"subjects":[{"id":"1","title":%q,"cover":"https://example.com/poster.jpg","rate":"8.5"},{"id":"2","title":"暂未评分","rate":"","episodes_info":"更新至12集"},{"id":"1","title":"重复"},{"id":"","title":"无效条目"},{"id":"3","title":" "}]}`, q.Get("type"))
	})
	for _, kind := range []Kind{Movie, TV} {
		feed, err := c.Fetch(context.Background(), kind)
		if err != nil {
			t.Fatal(err)
		}
		if len(feed.Subjects) != 2 || feed.Subjects[0].Title != string(kind) || feed.Subjects[0].Rating != "8.5" || feed.Subjects[1].Episodes != "更新至12集" {
			t.Fatalf("feed=%+v", feed)
		}
		if err := c.Save(feed); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []Kind{Movie, TV} {
		cached := New(c.CacheDir).Cached(kind)
		if !cached.Fresh(time.Now()) || cached.Subjects[0].Title != string(kind) {
			t.Fatalf("cache=%+v", cached)
		}
		info, err := os.Stat(c.cachePath(kind))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatal("cache permissions are too broad")
		}
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
		{"empty", `{"subjects":[]}`, "暂未返回", 200},
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
