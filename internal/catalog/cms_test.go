package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func TestCMSSessionFlow(t *testing.T) {
	for _, kind := range []config.Kind{config.KindJSON, config.KindXML} {
		t.Run(string(kind), func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("ac") == "list" || q.Get("ac") == "class" {
					if kind == config.KindJSON {
						fmt.Fprint(w, "{\"class\":[{\"type_id\":\"1\",\"type_name\":\"电影\"}]}")
					} else {
						fmt.Fprint(w, "<rss><class><ty id=\"1\">电影</ty></class></rss>")
					}
					return
				}
				if q.Get("ids") == "7" {
					if kind == config.KindJSON {
						fmt.Fprintf(w, "{\"list\":[{\"vod_name\":\"示例\",\"vod_play_from\":\"直链\",\"vod_play_url\":\"第1集$%s/clip.mp4\"}]}", srv.URL)
					} else {
						fmt.Fprint(w, "<rss><list><video><id>7</id><name>示例</name><dl><dd flag=\"直链\">第1集$/clip.mp4</dd></dl></video></list></rss>")
					}
					return
				}
				if q.Get("wd") == "示例" {
					if q.Get("pg") != "1" || q.Get("t") != "" {
						t.Errorf("unexpected search query: %s", r.URL.RawQuery)
					}
				} else if q.Get("t") != "1" || q.Get("pg") != "1" {
					t.Errorf("unexpected category query: %s", r.URL.RawQuery)
				}
				if kind == config.KindJSON {
					fmt.Fprint(w, "{\"pagecount\":1,\"list\":[{\"vod_id\":\"7\",\"vod_name\":\"示例\"}]}")
				} else {
					fmt.Fprint(w, "<rss><list pagecount=\"1\"><video><id>7</id><name>示例</name></video></list></rss>")
				}
			}))
			defer srv.Close()
			s, cats, err := Open(context.Background(), config.Site{Kind: kind, API: srv.URL + "/api"}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if len(cats) != 1 || cats[0].ID != "1" {
				t.Fatalf("categories: %+v", cats)
			}
			page, err := s.Category(context.Background(), "1", 1)
			if err != nil || len(page.Videos) != 1 || page.Videos[0].ID != "7" {
				t.Fatalf("page: %+v, %v", page, err)
			}
			searched, err := s.Search(context.Background(), "示例", 1)
			if err != nil || len(searched.Videos) != 1 || searched.Videos[0].ID != "7" {
				t.Fatalf("search: %+v, %v", searched, err)
			}
			detail, err := s.Detail(context.Background(), "7")
			if err != nil || len(detail.Sources) != 1 || len(detail.Sources[0].Episodes) != 1 {
				t.Fatalf("detail: %+v, %v", detail, err)
			}
			play, err := s.Play(context.Background(), detail.Sources[0].Name, detail.Sources[0].Episodes[0])
			if err != nil || play.URL != srv.URL+"/clip.mp4" {
				t.Fatalf("play: %+v, %v", play, err)
			}
		})
	}
}

func TestCMSHTMLResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>not a CMS API</html>")
	}))
	defer srv.Close()
	_, _, err := Open(context.Background(), config.Site{Kind: config.KindJSON, API: srv.URL + "/api"}, "")
	if err == nil || !strings.Contains(err.Error(), srv.URL+"/api") || !strings.Contains(err.Error(), "HTTP 200") || !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("error: %v", err)
	}
}
