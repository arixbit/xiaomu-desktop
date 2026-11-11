package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNativeSniffCrossOriginFrame(t *testing.T) {
	if os.Getenv("XIAOMU_NATIVE_MEDIA_DIR") == "" {
		t.Skip("requires the macOS application event loop")
	}
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/clip.m3u8" {
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-ENDLIST\n")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<script>setTimeout(()=>fetch('/clip'+'.m3u8',{headers:{'X-Fixture':'frame'}}),500)</script>")
	}))
	defer child.Close()
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<iframe src='%s/player'></iframe>", child.URL)
	}))
	defer root.Close()
	win := mygo.NewWindow(mygo.WindowOptions{Title: "小幕取流测试", Hidden: true, Width: 320, Height: 180, Content: ui.View(func(c *ui.Context) { ui.Text(c, "取流测试") })})
	defer win.Destroy()
	a := &app{window: win}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	play, e := a.sniffPage(ctx, catalog.Playback{URL: root.URL})
	if e != nil {
		t.Fatal(e)
	}
	if play.URL != child.URL+"/clip.m3u8" || play.Headers["Referer"] != child.URL+"/player" {
		t.Fatalf("%+v", play)
	}
	found := false
	for k, v := range play.Headers {
		if (k == "X-Fixture" || k == "x-fixture") && v == "frame" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lost iframe request headers: %v", play.Headers)
	}
	t.Log("WKWebView executed a delayed cross-origin iframe request and captured media URL, Referer and custom headers")
}
