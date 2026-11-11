package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
