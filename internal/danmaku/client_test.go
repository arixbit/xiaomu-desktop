package danmaku

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientSearchAndLoad(t *testing.T) {
	for _, suffix := range []string{"/private-prefix", "/private-prefix/api/v2/"} {
		t.Run(suffix, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Query().Get("key") != "example-only" {
					t.Error("lost service query parameters")
				}
				switch r.URL.Path {
				case "/private-prefix/api/v2/search/episodes":
					if r.URL.Query().Get("anime") != "示例 剧" {
						t.Errorf("query = %s", r.URL.RawQuery)
					}
					io.WriteString(w, `{"animes":[{"animeId":11,"animeTitle":"示例 剧","episodes":[{"episodeId":101,"episodeTitle":"第1话"},{"episodeId":"episode-two","episodeTitle":"第2话"},{"episodeId":"../bad","episodeTitle":"invalid"}]}]}`)
				case "/private-prefix/api/v2/comment/101":
					if r.URL.Query().Get("format") != "json" {
						t.Error("did not request an explicit format")
					}
					// Some compatible services return XML despite the format hint.
					io.WriteString(w, `<i><d p="1,1,25,16777215">示例弹幕</d></i>`)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client, err := NewClient(server.URL + suffix + "?key=example-only")
			if err != nil {
				t.Fatal(err)
			}
			episodes, err := client.Search(context.Background(), "示例 剧", "")
			if err != nil || len(episodes) != 2 || episodes[0].ID != "101" || episodes[1].ID != "episode-two" {
				t.Fatalf("search = %+v, %v", episodes, err)
			}
			comments, err := client.Comments(context.Background(), episodes[0].ID)
			if err != nil || len(comments) != 1 || comments[0].Text != "示例弹幕" || calls.Load() != 2 {
				t.Fatalf("comments = %+v, %v; calls=%d", comments, err, calls.Load())
			}
		})
	}
}

func TestClientSearchEpisodeParameter(t *testing.T) {
	for _, episode := range []string{"", "2"} {
		t.Run("episode="+episode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				if query.Get("anime") != "示例片" || query.Get("episode") != episode || query.Get("key") != "example-only" {
					t.Errorf("unexpected search parameters: %s", r.URL.RawQuery)
				}
				if episode == "" && query.Has("episode") {
					t.Error("blank input kept a stale episode filter")
				}
				io.WriteString(w, `{"animes":[]}`)
			}))
			defer server.Close()
			client, err := NewClient(server.URL + "?episode=99&key=example-only")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Search(context.Background(), "示例片", episode); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClientErrorsAndCancellation(t *testing.T) {
	for _, address := range []string{"", "file:///tmp/comments", "https://user:pass@example.com", "https://example.com/#fragment"} {
		if _, err := NewClient(address); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "private response body")
	}))
	defer server.Close()
	client, err := NewClient(server.URL + "/private-prefix?key=example-only")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), "示例", ""); err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "private") {
		t.Fatalf("HTTP error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Search(ctx, "示例", ""); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "private-prefix") || strings.Contains(err.Error(), "example-only") {
		t.Fatalf("cancellation error = %v", err)
	}
	for _, id := range []string{"", "..", "../other", "%2fother", "a?key=x"} {
		if _, err := client.Comments(context.Background(), id); err == nil {
			t.Errorf("accepted invalid ID %q", id)
		}
	}
}

func TestLoadSourceCommentURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "episode=2&token=example-only" {
			t.Errorf("source comment URL was rewritten: %s", r.URL)
		}
		io.WriteString(w, `<i><d p="1,1,25,16777215">片源弹幕</d></i>`)
	}))
	defer server.Close()
	comments, err := Load(context.Background(), server.URL+"/comments/2.xml?episode=2&token=example-only")
	if err != nil || len(comments) != 1 || comments[0].Text != "片源弹幕" {
		t.Fatalf("source comments = %+v, err=%v", comments, err)
	}
	for _, address := range []string{"", "file:///tmp/comments.xml", "https://user:example-only@example.com/comments.xml"} {
		if _, err := Load(context.Background(), address); err == nil {
			t.Errorf("invalid source URL was accepted: %s", address)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Load(ctx, server.URL+"/comments.xml"); !errors.Is(err, context.Canceled) {
		t.Fatalf("source request did not honor cancellation: %v", err)
	}
}

func TestClientBoundsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("x", 64<<10)
		for sent := 0; sent <= maxResponseBytes; sent += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), "示例", ""); err == nil || !strings.Contains(err.Error(), "16 MB") {
		t.Fatalf("oversize error = %v", err)
	}
}
