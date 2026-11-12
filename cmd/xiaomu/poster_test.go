package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFetchPoster(t *testing.T) {
	var pngData bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/poster.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngData.Bytes())
		case "/blocked":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("blocked"))
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>blocked</html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	poster, err := fetchPoster(context.Background(), srv.URL+"/poster.png")
	if err != nil || poster == nil {
		t.Fatalf("poster %v, error %v", poster, err)
	}
	if _, err := fetchPoster(context.Background(), srv.URL+"/blocked"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("HTTP error: %v", err)
	}
	if _, err := fetchPoster(context.Background(), srv.URL+"/html"); err == nil {
		t.Fatal("HTML response accepted as a poster")
	}
}

type posterTestTransport func(*http.Request) (*http.Response, error)

func (f posterTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDoubanPosterRetriesOneAlternateHost(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, raw, alternate string
		secondFails          bool
	}{
		{"verification page", "https://img9.doubanio.com/view/photo/poster.jpg?size=small", "img1.doubanio.com", false},
		{"primary unavailable", "https://img1.doubanio.com/view/photo/poster.jpg", "img3.doubanio.com", false},
		{"bounded failure", "https://img3.doubanio.com/view/photo/poster.jpg", "img1.doubanio.com", true},
		{"other source", "https://example.com/view/photo/poster.jpg", "", false},
		{"lookalike host", "https://img9.doubanio.com.example.com/view/photo/poster.jpg", "", false},
		{"unrelated path", "https://img9.doubanio.com/other.jpg", "", false},
		{"custom port", "https://img9.doubanio.com:8443/view/photo/poster.jpg", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original, _ := url.Parse(tt.raw)
			calls := 0
			var deadline time.Time
			previous := posterClient
			defer func() { posterClient = previous }()
			posterClient = &http.Client{Transport: posterTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				gotDeadline, ok := r.Context().Deadline()
				if !ok {
					t.Fatal("poster request has no deadline")
				}
				if calls == 1 {
					deadline = gotDeadline
				} else if calls != 2 || r.URL.Host != tt.alternate || r.Host != tt.alternate || !gotDeadline.Equal(deadline) {
					t.Fatalf("retry escaped its host/deadline bounds: calls=%d URL=%s deadline=%s", calls, r.URL, gotDeadline)
				}
				if r.URL.RequestURI() != original.RequestURI() {
					t.Fatal("retry changed the requested image")
				}
				body := []byte("<html>verification required</html>")
				if calls == 2 && !tt.secondFails {
					if r.Header.Get("Referer") != "https://movie.douban.com/" || r.Header.Get("User-Agent") != "Mozilla/5.0" {
						t.Fatal("retry lost image headers")
					}
					body = data.Bytes()
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
			})}
			_, err := fetchPoster(context.Background(), tt.raw)
			wantErr := tt.alternate == "" || tt.secondFails
			wantCalls := 1
			if tt.alternate != "" {
				wantCalls = 2
			}
			if (err != nil) != wantErr || calls != wantCalls {
				t.Fatalf("error=%v calls=%d, want error=%v calls=%d", err, calls, wantErr, wantCalls)
			}
		})
	}
}

func TestDoubanPosterHeadersOnlyReachDoubanHosts(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantReferer := ""
		if r.URL.Path == "/douban" {
			wantReferer = "https://movie.douban.com/"
		}
		if got := r.Header.Get("Referer"); got != wantReferer {
			t.Errorf("referer=%q want=%q", got, wantReferer)
		}
		if wantReferer != "" && r.Header.Get("User-Agent") != "Mozilla/5.0" {
			t.Errorf("user agent=%q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(data.Bytes())
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	previous := posterClient
	posterClient = &http.Client{Transport: metadataTestTransport{u, srv.Client().Transport}}
	defer func() { posterClient = previous }()
	for _, raw := range []string{"https://img3.doubanio.com/douban", "https://example.com/ordinary", "https://doubanio.com.example.com/ordinary"} {
		if _, err := fetchPoster(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}
}
