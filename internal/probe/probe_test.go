package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func TestProbeFixture(t *testing.T) {
	engineJS, err := os.ReadFile("../drpy/testdata/engine.js")
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/engine.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(engineJS)
		case "/rule.js":
			fmt.Fprintf(w, `var rule = {
  title: "fixture",
  host: %q,
  url: "/list/fyclass/fypage",
  searchUrl: "/search?wd=**&pg=fypage",
  class_name: "电影",
  class_url: "1",
  一级: ".list&&.item",
  搜索: ".list&&.item"
};`, srv.URL)
		case "/list/1/1", "/search":
			_, _ = ioWrite(w, `<div class="list"><div class="item"><a href="/v/7">示例甲</a><img data-src="/p.jpg"></div></div>`)
		case "/v/7":
			_, _ = ioWrite(w, `<h1>示例甲</h1><div class="eps"><a href="/play/7">第1集</a></div>`)
		case "/cfg.json":
			fmt.Fprintf(w, `{
  "spider": "./nope.jar;md5",
  "sites": [
    {"key":"demo","name":"演示","type":3,"api":%q,"ext":%q},
    {"key":"jar","name":"jar站","type":3,"api":"csp_Demo"}
  ]
}`, srv.URL+"/engine.js", srv.URL+"/rule.js")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rows, err := Run(context.Background(), Options{
		AssetsDir: "../drpy/assets",
		ConfigURL: srv.URL + "/cfg.json",
		N:         2,
		Timeout:   15 * time.Second,
		Fetch:     config.HTTPFetcher(srv.Client()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d", len(rows))
	}
	if rows[0].Status != "PASS" || rows[0].Home != "ok" || rows[0].Category != "ok" || rows[0].Detail != "ok" || rows[0].Play != "ok" {
		t.Fatalf("demo %+v", rows[0])
	}
	if !strings.Contains(rows[0].Note, "/play/7") {
		t.Fatalf("play note %q", rows[0].Note)
	}
	if rows[1].Status != "SKIP" || rows[1].Kind != config.KindCSP {
		t.Fatalf("jar %+v", rows[1])
	}
	searched, err := Run(context.Background(), Options{
		AssetsDir: "../drpy/assets",
		ConfigURL: srv.URL + "/cfg.json",
		Query:     "示例",
		N:         1,
		Fetch:     config.HTTPFetcher(srv.Client()),
	})
	if err != nil || len(searched) != 1 || searched[0].Status != "PASS" {
		t.Fatalf("search probe: %+v, %v", searched, err)
	}
}

func ioWrite(w http.ResponseWriter, s string) (int, error) { return fmt.Fprint(w, s) }
