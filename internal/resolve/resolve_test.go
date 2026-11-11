package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
)

func TestPageRedirectIframeAndMediaHeaders(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch":
			http.Redirect(w, r, "/pages/watch", 302)
		case "/pages/watch":
			fmt.Fprint(w, "<iframe src='player'></iframe>")
		case "/pages/player":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "fixture", Path: "/"})
			fmt.Fprint(w, "<script>const vid = '../media/index.m3u8';</script>")
		case "/media/index.m3u8":
			if r.Header.Get("Referer") != srv.URL+"/pages/player" || r.Header.Get("X-Test") != "1" || r.Header.Get("Cookie") != (&http.Cookie{Name: "session", Value: "fixture"}).String() {
				t.Errorf("lost playback headers: %v", r.Header)
			}
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\nsegment.ts\n#EXT-X-ENDLIST\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	play, e := (Resolver{}).Resolve(context.Background(), catalog.Playback{URL: srv.URL + "/watch", Headers: map[string]string{"X-Test": "1"}})
	if e != nil || play.URL != srv.URL+"/media/index.m3u8" || play.Headers["Cookie"] != (&http.Cookie{Name: "session", Value: "fixture"}).String() {
		t.Fatalf("%+v %v", play, e)
	}
}

func TestMacCMSAndQueryAreNotConfusedWithMedia(t *testing.T) {
	body := "<script>var player_aaaa={\"url\":\"https%3A%2F%2Fexample.com%2Fa.m3u8%3Ftoken%3Da%2Bb\",\"encrypt\":1};</script>"
	got := Candidates("https://example.com/watch", body)
	if len(got) != 1 || got[0] != "https://example.com/a.m3u8?token=a+b" {
		t.Fatal(got)
	}
	if Direct("https://example.com/watch?url=a.mp4") || Direct("https://example.com/watch#a.m3u8") {
		t.Fatal("web page mistaken for media")
	}
}

func TestDynamicPageUsesSnifferAndPreservesCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><script>startPlayer()</script></html>")
	}))
	defer srv.Close()
	called := false
	r := Resolver{Sniff: func(ctx context.Context, p catalog.Playback) (catalog.Playback, error) {
		called = true
		if !strings.HasPrefix(p.URL, srv.URL) {
			t.Fatal(p)
		}
		return catalog.Playback{URL: "https://example.com/resolved.m3u8"}, nil
	}}
	play, e := r.Resolve(context.Background(), catalog.Playback{URL: srv.URL})
	if e != nil || !called || play.URL != "https://example.com/resolved.m3u8" {
		t.Fatalf("%+v %v called=%v", play, e, called)
	}
	called = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e = r.Resolve(ctx, catalog.Playback{URL: srv.URL})
	if e != context.Canceled || called {
		t.Fatalf("%v called=%v", e, called)
	}
}

func TestConfiguredJSONParser(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/watch" {
			fmt.Fprint(w, "<html>dynamic player</html>")
			return
		}
		if r.URL.Path == "/parse" {
			if r.URL.Query().Get("url") != srv.URL+"/watch" || r.Header.Get("X-Parser") != "fixture" {
				t.Error(r.URL, r.Header)
			}
			fmt.Fprint(w, "{\"data\":{\"url\":\"https://example.com/parsed.m3u8\",\"header\":{\"Referer\":\"https://example.com/player\"}}}")
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	r := Resolver{Parsers: []config.Parser{{Name: "fixture", Type: 1, URL: srv.URL + "/parse?url=", Ext: json.RawMessage("{\"header\":{\"X-Parser\":\"fixture\"}}")}}}
	p, e := r.Resolve(context.Background(), catalog.Playback{URL: srv.URL + "/watch", JX: true})
	if e != nil || p.URL != "https://example.com/parsed.m3u8" || p.Headers["Referer"] != "https://example.com/player" {
		t.Fatalf("%+v %v", p, e)
	}
}
