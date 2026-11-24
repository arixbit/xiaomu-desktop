package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/netimage"
)

func fixPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func fixPump(t *testing.T, q <-chan func(), ready func() bool, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for !ready() {
		select {
		case f := <-q:
			f()
		case <-time.After(time.Millisecond):
		case <-timer.C:
			t.Fatal("work did not settle")
		}
	}
}

func TestPosterBudgetAndNetImageCache(t *testing.T) {
	small, large, tooLarge := fixPNG(t, 512, 512), fixPNG(t, 2048, 2048), fixPNG(t, 4097, 1024)
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/large":
			w.Header().Set("Content-Type", "image/png")
			w.Write(large)
		case "/small":
			w.Header().Set("Content-Type", "image/png")
			w.Write(small)
		case "/oversize":
			w.Header().Set("Content-Type", "image/png")
			w.Write(tooLarge)
		case "/cached":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "max-age=3600")
			w.Write(small)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	img, err := fetchPoster(context.Background(), srv.URL+"/large")
	if err != nil {
		t.Fatal(err)
	}
	w, h := ui.ImageSize(img)
	if w != posterMaxSide || h != posterMaxSide {
		t.Fatalf("large poster size=%vx%v want=%dx%d", w, h, posterMaxSide, posterMaxSide)
	}
	smallImg, err := fetchPoster(context.Background(), srv.URL+"/small")
	if err != nil {
		t.Fatal(err)
	}
	sw, sh := ui.ImageSize(smallImg)
	if sw != 512 || sh != 512 {
		t.Fatalf("small poster was resized: %vx%v", sw, sh)
	}
	if _, err := fetchPoster(context.Background(), srv.URL+"/oversize"); err == nil {
		t.Error("over-budget dimensions decoded")
	}

	// Verify app.poster delegates cleanly to MyGo ui/netimage with warm cache hits.
	cachedURL := fmt.Sprintf("%s/cached", srv.URL)
	defer netimage.Forget(cachedURL)

	a := &app{}
	defer a.close()
	var got ui.ImageSource
	tt := ui.NewTester(func(c *ui.Context) {
		got = a.poster(c, cachedURL)
		if a.poster(c, "javascript:alert(1)") != nil {
			t.Error("non-http poster URL should be ignored")
		}
	}, 200, 200)

	deadline := time.Now().Add(2 * time.Second)
	for got == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
	}
	if got == nil {
		t.Fatal("netimage poster did not load")
	}
	before := requests.Load()
	for range 10 {
		tt.Frame()
		if got == nil {
			t.Fatal("warm netimage cache lost image")
		}
	}
	if requests.Load() != before {
		t.Errorf("warm netimage cache hit network: before=%d after=%d", before, requests.Load())
	}
}
