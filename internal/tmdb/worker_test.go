package tmdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkerSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/posters" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("search should not send credentials")
		}
		if r.UserAgent() != "Xiaomu-Poster/1.0" {
			t.Errorf("User-Agent = %q", r.UserAgent())
		}
		q := r.URL.Query()
		if q.Get("title") != "离线片名 & 示例" || q.Get("year") != "2018" || q.Get("kind") != "tv" || len(q) != 3 {
			t.Errorf("query = %v", q)
		}
		io.WriteString(w, `{"matches":[{"id":42,"kind":"tv","title":"离线片名","original_title":"Example","year":"2018","poster_url":"https://image.tmdb.org/t/p/w500/example.jpg"},{"id":43,"kind":"tv","title":"离线片名","poster_url":"https://example.com/other.jpg"}]}`)
	}))
	defer server.Close()
	client, err := NewWorker(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Search(context.Background(), " 离线片名 & 示例 ", "2018", TV)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Match{ID: 42, Kind: TV, Title: "离线片名", OriginalTitle: "Example", Year: "2018", PosterURL: posterBase + "/example.jpg"}) || got[1].ID != 43 || got[1].PosterURL != "" {
		t.Fatalf("matches = %+v", got)
	}
}

func TestWorkerSearchEmptyAndInvalidResponse(t *testing.T) {
	tests := []struct {
		body string
		ok   bool
	}{
		{`{"matches":[]}`, true},
		{`{"matches":null}`, false},
		{`{"matches":[{"id":1,"kind":"person","title":"Example"}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if len(r.URL.Query()) != 1 {
					t.Errorf("unexpected optional query parameters: %v", r.URL.Query())
				}
				io.WriteString(w, tt.body)
			}))
			defer server.Close()
			client, err := NewWorker(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.Search(context.Background(), "Example", "", "")
			if tt.ok && (err != nil || len(got) != 0) || !tt.ok && err == nil {
				t.Fatalf("Search() = %v, %v", got, err)
			}
		})
	}
}

func TestWorkerSearchRedirectAndRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/moved/v1/posters":
			t.Error("redirect was followed")
		case "/v1/posters":
			if r.URL.Query().Get("title") == "redirect" {
				http.Redirect(w, r, "/moved/v1/posters", http.StatusFound)
			} else {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
			}
		}
	}))
	defer server.Close()
	client, err := NewWorker(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), "redirect", "", ""); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect error = %v", err)
	}
	if _, err := client.Search(context.Background(), "rate-limit", "", ""); err == nil || !strings.Contains(err.Error(), "请求过多") {
		t.Fatalf("rate limit error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Search(ctx, "Example", "", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestNewWorkerRejectsInvalidEndpoints(t *testing.T) {
	for _, endpoint := range []string{"", "http://example.com", "https://name@example.com", "https://example.com/?foo=bar", "https://example.com/#fragment"} {
		if _, err := NewWorker(endpoint); err == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
}
