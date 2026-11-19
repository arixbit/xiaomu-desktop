package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFixPosterRetainsFrequentlyVisibleImage(t *testing.T) {
	png := fixPNG(t, 32, 32)
	var hotRequests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hot" {
			hotRequests.Add(1)
		}
		w.Write(png)
	}))
	defer srv.Close()
	q := make(chan func(), 64)
	a := &app{dispatch: func(f func()) { q <- f }}
	defer a.close()
	hot := srv.URL + "/hot"
	a.poster(hot)
	fixPump(t, q, func() bool { return len(a.posters.pending) == 0 }, time.Second)
	original := a.poster(hot)
	if original == nil {
		t.Fatal("ordinary image did not load")
	}
	for i := range 256 {
		if a.poster(hot) != original {
			t.Fatalf("frequently visible image evicted after %d cold images", i)
		}
		a.poster(fmt.Sprintf("%s/cold/%d", srv.URL, i))
		fixPump(t, q, func() bool { return len(a.posters.pending) == 0 }, time.Second)
	}
	if a.poster(hot) != original || hotRequests.Load() != 1 {
		t.Fatal("visible image was downloaded or decoded again")
	}
	if len(a.posters.images) > 128 || a.posters.retained > posterCacheBytes {
		t.Fatal("hot retention exceeded cache budgets")
	}
	t.Logf("256 cold images with repeated hot image: hot requests=%d entries=%d retained=%d", hotRequests.Load(), len(a.posters.images), a.posters.retained)
}
