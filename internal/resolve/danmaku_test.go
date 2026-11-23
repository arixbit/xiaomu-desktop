package resolve

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func TestResolvePreservesSourceDanmaku(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			fmt.Fprint(w, `<video src="/video.m3u8"></video>`)
		case "/video.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n")
		case "/parser":
			fmt.Fprint(w, `{"url":"https://example.com/resolved.m3u8"}`)
		default:
			fmt.Fprint(w, "<html>dynamic player</html>")
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, address, replacement string
		resolver                   Resolver
	}{
		{"direct", server.URL + "/video.m3u8", "", Resolver{}},
		{"page", server.URL + "/page", "", Resolver{}},
		{"JSON parser", server.URL + "/dynamic", "", Resolver{Parsers: []config.Parser{{Type: 1, URL: server.URL + "/parser?url="}}}},
		{"sniffer", server.URL + "/dynamic", "", Resolver{Sniff: func(context.Context, catalog.Playback) (catalog.Playback, error) {
			return catalog.Playback{URL: "https://example.com/resolved.m3u8"}, nil
		}}},
		{"replacement", server.URL + "/dynamic", "https://example.com/replacement.xml", Resolver{Sniff: func(context.Context, catalog.Playback) (catalog.Playback, error) {
			return catalog.Playback{URL: "https://example.com/resolved.m3u8", Danmaku: "https://example.com/replacement.xml"}, nil
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := catalog.ReadPlayback(fmt.Sprintf(`{"url":%q,"danmaku":"https://example.com/source.xml"}`, tc.address))
			if err != nil {
				t.Fatal(err)
			}
			output, err := tc.resolver.Resolve(context.Background(), input)
			want := input.Danmaku
			if tc.replacement != "" {
				want = tc.replacement
			}
			if err != nil || output.Danmaku != want || want == "" {
				t.Fatalf("resolved track = %q, want %q, err=%v", output.Danmaku, want, err)
			}
		})
	}
}
