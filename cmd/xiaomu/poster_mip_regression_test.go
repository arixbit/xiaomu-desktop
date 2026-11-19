package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestFixPosterMipAccounting(t *testing.T) {
	for _, size := range []struct {
		w, h   int
		pixels int64
	}{{1, 4096, 8191}, {513, 511, 350036}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			data := fixPNG(t, size.w, size.h)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
			defer srv.Close()
			q := make(chan func(), 8)
			a := &app{dispatch: func(f func()) { q <- f }}
			defer a.close()
			a.poster(srv.URL + "/poster.png")
			fixPump(t, q, func() bool { return len(a.posters.pending) == 0 }, time.Second)
			if len(a.posters.images) != 1 {
				t.Fatal("normal image did not load")
			}
			// Both the original and revised cache are exercised through the same UI path.
			var retained int64
			if field := reflect.ValueOf(a.posters).FieldByName("retained"); field.IsValid() {
				retained = field.Int()
			} else {
				retained = int64(size.w * size.h * 4)
			}
			want := size.pixels*4 + int64(len(data))
			t.Logf("reserved=%d required=%d (original pixels plus all ceil-halved levels and encoded source)", retained, want)
			if retained < want {
				t.Errorf("cache undercounts retained image bytes: %d < %d", retained, want)
			}
		})
	}
}
