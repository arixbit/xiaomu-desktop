package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/egoist/mygo/ui"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
func TestFixPosterBudget(t *testing.T) {
	small, large, tooLarge := fixPNG(t, 512, 512), fixPNG(t, 2048, 2048), fixPNG(t, 4097, 1024)
	var active, peak, requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		if strings.HasPrefix(r.URL.Path, "/slow/") {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
		if strings.HasPrefix(r.URL.Path, "/fail/") {
			http.Error(w, "failure", 503)
			return
		}
		data := small
		if r.URL.Path == "/large" {
			data = large
		}
		if r.URL.Path == "/oversize" {
			data = tooLarge
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(data)
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
	q := make(chan func(), 2048)
	a := &app{dispatch: func(f func()) { q <- f }}
	for i := range 100 {
		a.poster(fmt.Sprintf("%s/slow/%d", srv.URL, i))
	}
	time.Sleep(50 * time.Millisecond)
	t.Logf("100 slow URLs: active=%d peak=%d pending=%d", active.Load(), peak.Load(), len(a.posters.pending))
	if peak.Load() > 4 || len(a.posters.pending) > 36 {
		t.Error("unbounded poster work")
	}
	start := time.Now()
	a.close()
	deadline := time.Now().Add(100 * time.Millisecond)
	for active.Load() != 0 && time.Now().Before(deadline) {
		select {
		case f := <-q:
			f()
		case <-time.After(time.Millisecond):
		}
	}
	if active.Load() != 0 {
		t.Error("close retained active downloads")
	}
	fixPump(t, q, func() bool { return active.Load() == 0 }, time.Second)
	t.Logf("close requests zero: %v", time.Since(start))
	b := &app{dispatch: func(f func()) { q <- f }}
	defer b.close()
	for i := range 130 {
		b.poster(fmt.Sprintf("%s/cache/%d", srv.URL, i))
		fixPump(t, q, func() bool { return len(b.posters.pending) == 0 }, time.Second)
	}
	var decoded int64
	for _, img := range b.posters.images {
		w, h := ui.ImageSize(img)
		decoded += int64(w) * int64(h) * 4
	}
	t.Logf("cache actual decoded bytes=%d entries=%d", decoded, len(b.posters.images))
	if decoded > 64<<20 {
		t.Errorf("decoded cache exceeds 64MiB: %d", decoded)
	}
	before := requests.Load()
	for range 50 {
		for url := range b.posters.images {
			if b.poster(url) == nil {
				t.Fatal("warm cache lost image")
			}
		}
	}
	if requests.Load() != before {
		t.Error("hot cache requested network")
	}
	for i := range 1000 {
		b.poster(fmt.Sprintf("%s/fail/%d", srv.URL, i))
		fixPump(t, q, func() bool { return len(b.posters.pending) == 0 }, time.Second)
	}
	t.Logf("failed URL records=%d", len(b.posters.failed))
	if len(b.posters.failed) > 256 {
		t.Error("failure records unbounded")
	}
	for key := range b.posters.failed {
		b.posters.failed[key] = time.Now().Add(-31 * time.Second)
	}
	b.poster(srv.URL + "/cleanup")
	if len(b.posters.failed) != 0 {
		t.Errorf("expired failure records retained: %d", len(b.posters.failed))
	}
	fixPump(t, q, func() bool { return len(b.posters.pending) == 0 }, time.Second)
}
