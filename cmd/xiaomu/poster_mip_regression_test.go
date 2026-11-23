package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestFixPosterMipAccounting(t *testing.T) {
	for _, size := range []struct {
		w, h         int
		wantW, wantH int
		pixels       int64
	}{
		{1, 4096, 1, 640, 1281},
		{513, 511, 513, 511, 350036},
	} {
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
			gotW, gotH := ui.ImageSize(a.posters.images[srv.URL+"/poster.png"])
			if int(gotW) != size.wantW || int(gotH) != size.wantH {
				t.Fatalf("scaled size=%vx%v want=%dx%d", gotW, gotH, size.wantW, size.wantH)
			}
			// DecodeBitmapScaled drops encoded source bytes and retains only scaled pixels plus mip levels.
			var retained int64
			if field := reflect.ValueOf(a.posters).FieldByName("retained"); field.IsValid() {
				retained = field.Int()
			} else {
				retained = int64(size.wantW * size.wantH * 4)
			}
			want := size.pixels * 4
			t.Logf("reserved=%d required=%d (scaled pixels plus all ceil-halved levels)", retained, want)
			if retained != want {
				t.Errorf("cache retained image bytes=%d want=%d", retained, want)
			}
		})
	}
}
