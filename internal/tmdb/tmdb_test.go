package tmdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestSearchMatchesMovieAndTV(t *testing.T) {
	const title = "测试作品 & 别名"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("unexpected authorization: %q", auth)
		}
		query := r.URL.Query()
		if query.Get("query") != title || query.Get("language") != "zh-CN" || query.Get("include_adult") != "false" || query.Has("api_key") {
			t.Errorf("unexpected query: %v", query)
		}
		switch r.URL.Path {
		case "/search/movie":
			if query.Get("year") != "2025" || query.Has("first_air_date_year") {
				t.Errorf("unexpected movie filters: %v", query)
			}
			io.WriteString(w, `{"results":[
				{"id":1,"title":"测试作品","original_title":"Fixture Movie","release_date":"2025-04-03","poster_path":"/movie_poster.jpg"},
				{"id":2,"original_title":"Only Original","poster_path":null},
				{"id":3,"title":"测试作品 3","poster_path":"//example.com/other.jpg"},
				{"id":4,"title":"测试作品 4","poster_path":"/../other.jpg"}
			]}`)
		case "/search/tv":
			if query.Get("first_air_date_year") != "2025" || query.Has("year") {
				t.Errorf("unexpected TV filters: %v", query)
			}
			io.WriteString(w, `{"results":[{"id":1,"name":"测试剧集","original_name":"Fixture Series","first_air_date":"2025-06-02","poster_path":"/show.jpg"}]}`)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(" test-token ")
	client.base = server.URL
	got, err := client.Search(t.Context(), title, "2025", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Match{
		{ID: 1, Kind: Movie, Title: "测试作品", OriginalTitle: "Fixture Movie", Year: "2025", PosterURL: posterBase + "/movie_poster.jpg"},
		{ID: 2, Kind: Movie, Title: "Only Original", OriginalTitle: "Only Original"},
		{ID: 3, Kind: Movie, Title: "测试作品 3"},
		{ID: 4, Kind: Movie, Title: "测试作品 4"},
		{ID: 1, Kind: TV, Title: "测试剧集", OriginalTitle: "Fixture Series", Year: "2025", PosterURL: posterBase + "/show.jpg"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Search() = %#v, want %#v", got, want)
	}
}

func TestSearchRejectsPartialResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/tv" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{"results":[{"id":1,"title":"Fixture","poster_path":"/poster.jpg"}]}`)
	}))
	defer server.Close()
	client := New("fixture-token")
	client.base = server.URL
	got, err := client.Search(t.Context(), "Fixture", "", "")
	if got != nil || err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("Search() = %v, %v, want no partial result and HTTP 429 error", got, err)
	}
}

func TestSearchDoesNotFollowRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/movie" {
			t.Errorf("unexpected redirect request to %s", r.URL.Path)
			return
		}
		http.Redirect(w, r, "/redirect-target", http.StatusFound)
	}))
	defer server.Close()
	client := New("fixture-token")
	client.base = server.URL
	if _, err := client.Search(t.Context(), "Fixture", "", Movie); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("Search() error = %v, want HTTP 302 error", err)
	}
}

func TestSearchCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the cancelled search reached the test server")
	}))
	defer server.Close()
	client := New("fixture-token")
	client.base = server.URL
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Search(ctx, "Fixture", "", TV); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search() error = %v, want context.Canceled", err)
	}
}
