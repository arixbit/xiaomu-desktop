package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func TestOpenMisclassifiedDrpyObject(t *testing.T) {
	engine, err := os.ReadFile("../drpy/testdata/engine.js")
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/entry.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(engine)
		case "/rule.js":
			fmt.Fprintf(w, "var rule = {host:%q, class_name:\"电影\", class_url:\"1\", url:\"/list/fyclass/fypage\"};", srv.URL)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, categories, err := Open(context.Background(), config.Site{Kind: config.KindFongMi, Type: 3, API: srv.URL + "/entry.js", Ext: srv.URL + "/rule.js"}, "../drpy/assets")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(categories) != 1 || categories[0].ID != "1" {
		t.Fatalf("categories: %+v", categories)
	}
}

func TestHomeRecommendationsWithoutCategorySelection(t *testing.T) {
	for _, kind := range []config.Kind{config.KindJSON, config.KindXML, config.KindDRPY2, config.KindFongMi, config.KindXBPQ} {
		t.Run(string(kind), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case config.KindJSON:
					if r.URL.Query().Get("t") != "" {
						t.Error("home unnecessarily selected a category")
					}
					fmt.Fprint(w, `{"list":[{"vod_id":"home","vod_name":"首页影片"}],"pagecount":1}`)
				case config.KindXML:
					fmt.Fprint(w, `<rss><list><video><id>home</id><name>首页影片</name></video></list></rss>`)
				case config.KindDRPY2:
					w.Header().Set("Content-Type", "text/javascript")
					fmt.Fprint(w, `export default {init(){}, home(){return JSON.stringify({class:[]})}, homeVod(){return JSON.stringify({list:[{vod_id:'home',vod_name:'首页影片'}]})}};`)
				case config.KindFongMi:
					w.Header().Set("Content-Type", "text/javascript")
					fmt.Fprint(w, `import {Spider} from 'catvod'; export default class Demo extends Spider { homeVideoContent(){return {list:[{vod_id:'home',vod_name:'首页影片'}]}} }`)
				case config.KindXBPQ:
					fmt.Fprint(w, `<a href="/film" title="首页影片"><img src="/poster.jpg"></a>`)
				}
			}))
			defer srv.Close()
			ext, err := json.Marshal(map[string]string{"主页url": srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			s, categories, err := Open(context.Background(), config.Site{Kind: kind, API: srv.URL + "/entry.js", Ext: string(ext)}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			page, err := Home(context.Background(), s, categories)
			if err != nil || len(page.Videos) != 1 || page.Videos[0].Name != "首页影片" {
				t.Fatalf("home=%+v err=%v", page, err)
			}
		})
	}
}

func TestHomeKeepsVideosReturnedWithCategories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		fmt.Fprint(w, `export default {init(){},home(){return JSON.stringify({class:[],list:[{vod_id:'home',vod_name:'首页影片'}]})},homeVod(){throw new Error('must use returned home list')}};`)
	}))
	defer srv.Close()
	s, categories, err := Open(context.Background(), config.Site{Kind: config.KindDRPY2, API: srv.URL + "/home.js"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := Home(context.Background(), s, categories)
	if err != nil || len(page.Videos) != 1 || page.Videos[0].ID != "home" {
		t.Fatalf("home=%+v err=%v", page, err)
	}
}

func TestOpenRejectsHTMLScript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>blocked</html>")
	}))
	defer srv.Close()
	_, _, err := Open(context.Background(), config.Site{Kind: config.KindFongMi, Type: 3, API: srv.URL + "/entry.js"}, "")
	if err == nil || !strings.Contains(err.Error(), srv.URL+"/entry.js") || !strings.Contains(err.Error(), "HTTP 200") || !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("error: %v", err)
	}
}
