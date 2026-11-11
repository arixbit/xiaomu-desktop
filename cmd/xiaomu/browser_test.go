package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestBrowserFlow(t *testing.T) {
	engine, err := os.ReadFile("../../internal/drpy/testdata/engine.js")
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/depot.json":
			fmt.Fprint(w, `{"urls":[{"name":"🚀演示线路","url":"./config.json"}]}`)
		case "/config.json":
			fmt.Fprint(w, `{"sites":[{"key":"demo","name":"演示站点","type":3,"api":"./drpy2.js","ext":"./rule.js"},{"key":"jar","name":"安卓站点","type":3,"api":"csp_Test"}]}`)
		case "/drpy2.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(engine)
		case "/rule.js":
			fmt.Fprintf(w, `var rule = {host:%q, class_name:"电影", class_url:"1", url:"/list/fyclass/fypage", searchUrl:"/search?wd=**&pg=fypage", 一级:".list&&.item"};`, srv.URL)
		case "/list/1/1", "/search":
			fmt.Fprint(w, `<div class="list"><div class="item"><a href="/v/7">示例甲</a></div></div>`)
		case "/list/1/2":
			fmt.Fprint(w, `<div class="list"></div>`)
		case "/v/7":
			fmt.Fprint(w, `<h1>示例甲</h1><div class="eps"><a href="/clip.mp4">第1集</a></div>`)
		case "/clip.mp4":
			if r.Header.Get("X-Local") != "1" {
				t.Errorf("missing site playback header: %v", r.Header)
			}
			if r.Header.Get("Referer") != "" {
				t.Errorf("direct-play header leaked: %v", r.Header)
			}
			w.Header().Set("Content-Type", "video/mp4")
			fmt.Fprint(w, "fixture media")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := &app{referer: "https://example.com/old"}
	a.browser.assets = "../../internal/drpy/assets"
	defer a.close()
	opened := false
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		if !strings.HasPrefix(raw, "http://127.0.0.1:") {
			t.Fatalf("not proxied: %s", raw)
		}
		resp, err := http.Get(raw)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if string(body) != "fixture media" {
			t.Fatalf("media %q", body)
		}
		opened = true
		return nil, nil
	}
	tt := ui.NewTester(a.view, 960, 640)
	if !tt.HasText("你的片库，从这里开始") {
		t.Fatalf("first launch should show the empty library: %v", tt.Texts())
	}
	if search, ok := tt.Find("片名"); !ok || search.Y < 50 {
		t.Fatalf("sidebar search overlaps the macOS title bar: rect=%+v found=%v texts=%v", search, ok, tt.Texts())
	}
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q: %v; status %s; texts %v", label, err, a.status, tt.Texts())
		}
	}
	click("添加片源")
	click("配置地址")
	tt.Type(srv.URL + "/depot.json")
	click("加载配置")
	click("🚀演示线路")
	click("安卓站点 · 暂不支持")
	if !strings.Contains(a.status, "专用 Android 爬虫") {
		t.Fatal(a.status)
	}
	click("演示站点")
	click("电影")
	click("下一页")
	if len(a.browser.results.Videos) != 0 || !strings.Contains(a.status, "0 部影片") {
		t.Fatal(a.status)
	}
	click("上一页")
	click("查看详情")
	click("第1集")
	if !opened || a.status != "正在播放" {
		t.Fatalf("opened %v, status %s", opened, a.status)
	}
	click("返回影片列表")
	click("片名")
	tt.Type("甲")
	click("搜索")
	if len(a.browser.results.Videos) != 1 || a.browser.search != "甲" {
		t.Fatalf("search %+v", a.browser)
	}
	click("添加片源")
	click("加载配置")
	if a.browser.session != nil || a.browser.detail != nil || len(a.browser.results.Videos) != 0 {
		t.Fatal("old site state survived config reload")
	}
}

func TestCanceledJobDoesNotApply(t *testing.T) {
	for _, closeWindow := range []bool{false, true} {
		t.Run(fmt.Sprintf("close=%v", closeWindow), func(t *testing.T) {
			updates := make(chan func(), 1)
			a := &app{dispatch: func(fn func()) { updates <- fn }}
			applied, discarded := false, false
			a.run("加载", func(ctx context.Context) (jobResult, error) {
				<-ctx.Done()
				return jobResult{apply: func() { applied = true }, discard: func() { discarded = true }}, nil
			})
			if !a.busy {
				t.Fatal("job did not start asynchronously")
			}
			a.run("重叠请求", func(context.Context) (jobResult, error) { t.Error("overlapping job ran"); return jobResult{}, nil })
			if closeWindow {
				a.close()
			} else {
				a.cancel()
			}
			select {
			case finish := <-updates:
				finish()
			case <-time.After(3 * time.Second):
				t.Fatal("job did not stop")
			}
			if applied || !discarded || a.busy {
				t.Fatalf("applied=%v discarded=%v busy=%v", applied, discarded, a.busy)
			}
		})
	}
}
