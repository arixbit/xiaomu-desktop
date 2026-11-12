package config

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/iptv"
)

func TestLiveConfigExternalPlaylistRedirectAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config.json":
			fmt.Fprint(w, `{"sites":[],"lives":[{"name":"测试直播","url":"./redirect","ua":"ListUA","header":{"Referer":"https://example.com/"}}]}`)
		case "/redirect":
			http.Redirect(w, r, "/lists/list.m3u", http.StatusFound)
		case "/lists/list.m3u":
			if r.Header.Get("User-Agent") != "ListUA" || r.Header.Get("Referer") != "https://example.com/" {
				t.Errorf("missing list headers: %v", r.Header)
			}
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,示例\n#EXTVLCOPT:http-user-agent=StreamUA\n./live.m3u8\n")
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	doc, err := Read(context.Background(), srv.URL+"/config.json", nil)
	if err != nil || len(doc.Lives) != 1 {
		t.Fatalf("doc=%+v err=%v", doc, err)
	}
	channels, err := LiveChannels(context.Background(), doc.Lives[0], nil)
	if err != nil || len(channels) != 1 {
		t.Fatalf("channels=%+v err=%v", channels, err)
	}
	stream := channels[0].Streams[0]
	if stream.URL != srv.URL+"/lists/live.m3u8" || stream.Headers["User-Agent"] != "StreamUA" || stream.Headers["Referer"] != "https://example.com/" {
		t.Fatalf("stream=%+v", stream)
	}
}

func TestReadLiveLists(t *testing.T) {
	for _, body := range []string{
		"#EXTM3U\n#EXTINF:-1,示例\nhttps://example.com/live.m3u8\n",
		"分组,#genre#\n示例,https://example.com/live.m3u8\n",
		`[{"name":"分组","channels":[{"name":"示例","urls":["https://example.com/live.m3u8"]}]}]`,
		`{"lives":[{"name":"分组","channels":[{"name":"示例","urls":["https://example.com/live.m3u8"]}]}]}`,
	} {
		doc, err := Read(context.Background(), "https://example.com/list", func(context.Context, string) (Response, error) {
			return Response{Status: 200, Body: []byte(body)}, nil
		})
		if err != nil || len(doc.Lives) != 1 || len(doc.Lives[0].Channels) != 1 || doc.Lives[0].Channels[0].Name != "示例" {
			t.Fatalf("body=%q doc=%+v err=%v", body, doc, err)
		}
	}
}

func TestLiveNestedLoopAndCancellation(t *testing.T) {
	source := iptv.Source{URL: "https://example.com/loop.json"}
	fetches := 0
	fetch := func(context.Context, string) (Response, error) {
		fetches++
		return Response{Status: 200, Body: []byte(`{"lives":"./loop.json"}`)}, nil
	}
	if _, err := LiveChannels(context.Background(), source, fetch); err == nil || !strings.Contains(err.Error(), "嵌套") || fetches > 5 {
		t.Fatalf("fetches=%d err=%v", fetches, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LiveChannels(ctx, source, fetch); err != context.Canceled {
		t.Fatal(err)
	}
}
