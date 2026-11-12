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
			fmt.Fprint(w, `{"sites":[{"key":"demo","name":"演示站点","type":3,"api":"./drpy2.js","ext":"./rule.js"},{"key":"jar","name":"安卓站点","type":3,"api":"csp_Test"}],"parses":[{"name":"数字类型","type":0,"url":"https://example.com/web?url="},{"name":"字符串类型","type":"1","url":"https://example.com/json?url="}]}`)
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
	if !tt.HasText("添加片源，开始观看") {
		t.Fatalf("first launch should ask for a source: %v", tt.Texts())
	}
	if brand, ok := tt.Find("小幕"); !ok || brand.Y < 50 {
		t.Fatalf("sidebar overlaps the macOS title bar: rect=%+v found=%v texts=%v", brand, ok, tt.Texts())
	}
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q: %v; status %s; texts %v", label, err, a.status, tt.Texts())
		}
	}
	click("配置地址")
	tt.Type(srv.URL + "/depot.json")
	click("导入")
	if len(a.library.feed.items) != 1 || a.browser.session != nil {
		t.Fatalf("import did not populate recommendations automatically: items=%d status=%s", len(a.library.feed.items), a.status)
	}
	click("最近更新")
	click("示例甲 · 🚀演示线路 · 演示站点")
	if a.route() != routeDetail || a.title == nil || a.title.detail == nil {
		t.Fatalf("title did not open from its only source: %s; texts %v", a.status, tt.Texts())
	}
	if parsers := entryParsers(a.title.item.Entry); len(parsers) != 2 || parsers[1].Type != 1 {
		t.Fatalf("line with mixed parser types failed: %s", a.status)
	}
	click("1")
	if !opened || a.status != "正在播放" {
		t.Fatalf("opened %v, status %s", opened, a.status)
	}
	click("返回")
	if a.route() != routeHome || a.title != nil {
		t.Fatalf("back did not leave the title: route=%v", a.route())
	}
	click("片名")
	tt.Type("甲")
	tt.Key(0, ui.KeyEnter)
	search := &a.library.search
	if a.route() != routeSearch || len(search.items) != 1 || search.query != "甲" {
		t.Fatalf("library search: route=%v query=%q results=%d status=%s", a.route(), search.query, len(search.items), a.status)
	}
	if len(a.library.feed.items) != 1 {
		t.Fatal("searching discarded the home feed")
	}
	click("片源")
	click("演示站点")
	if a.route() != routeSite || a.browser.session == nil {
		t.Fatalf("site did not open: %s; texts %v", a.status, tt.Texts())
	}
	click("下一页")
	if len(a.browser.results.Videos) != 0 || !strings.Contains(a.status, "0 部影片") {
		t.Fatal(a.status)
	}
	click("上一页")
	click("示例甲")
	if a.route() != routeDetail || a.title.detail == nil {
		t.Fatalf("title did not open from the site: %s", a.status)
	}
	click("返回")
	if a.route() != routeSite || a.browser.session == nil || a.title != nil {
		t.Fatal("back from a title should return to its site")
	}
	click("返回")
	if a.route() != routeSources || a.browser.session != nil {
		t.Fatal("leaving the site did not release it")
	}
	click("添加片源")
	click("加载配置")
	if a.browser.session != nil || a.title != nil || a.route() != routeHome {
		t.Fatal("old site state survived config reload")
	}
	if len(a.library.feed.items) != 1 {
		t.Fatal("recommendations were not restored after reload")
	}
	click("片源")
	click("移除片源")
	if a.library.root != nil || len(a.library.feed.items) != 0 {
		t.Fatal("removing the source kept its library")
	}
	click("首页")
	if !tt.HasText("添加片源，开始观看") {
		t.Fatalf("home did not return to the welcome: %v", tt.Texts())
	}
}

func TestCanceledJobDoesNotApply(t *testing.T) {
	for _, closeWindow := range []bool{false, true} {
		t.Run(fmt.Sprintf("close=%v", closeWindow), func(t *testing.T) {
			updates := make(chan func(), 1)
			a := &app{dispatch: func(fn func()) { updates <- fn }}
			applied, discarded := false, false
			a.run(jobSite, "加载", func(ctx context.Context) (jobResult, error) {
				<-ctx.Done()
				return jobResult{apply: func() { applied = true }, discard: func() { discarded = true }}, nil
			}, nil)
			if !a.running(jobSite) {
				t.Fatal("job did not start asynchronously")
			}
			if closeWindow {
				a.close()
			} else {
				a.cancelJobs()
			}
			select {
			case finish := <-updates:
				finish()
			case <-time.After(3 * time.Second):
				t.Fatal("job did not stop")
			}
			if applied || !discarded || a.busy() {
				t.Fatalf("applied=%v discarded=%v busy=%v", applied, discarded, a.busy())
			}
		})
	}
}

func TestNewJobReplacesOnlyTheOneInItsSlot(t *testing.T) {
	updates := make(chan func(), 3)
	a := &app{dispatch: func(fn func()) { updates <- fn }}
	applied := map[string]bool{}
	discarded := false
	a.run(jobSite, "旧请求", func(ctx context.Context) (jobResult, error) {
		<-ctx.Done()
		return jobResult{apply: func() { applied["旧请求"] = true }, discard: func() { discarded = true }}, nil
	}, nil)
	for slot, label := range map[jobSlot]string{jobLive: "别处", jobSite: "新请求"} {
		a.run(slot, label, func(context.Context) (jobResult, error) {
			return jobResult{apply: func() { applied[label] = true }}, nil
		}, nil)
	}
	for range 3 {
		select {
		case finish := <-updates:
			finish()
		case <-time.After(3 * time.Second):
			t.Fatal("a job did not finish")
		}
	}
	if applied["旧请求"] || !discarded {
		t.Fatalf("replaced job applied=%v discarded=%v", applied["旧请求"], discarded)
	}
	if !applied["新请求"] || !applied["别处"] || a.busy() {
		t.Fatalf("applied=%v busy=%v", applied, a.busy())
	}
}

func TestFailedJobReportsInPlace(t *testing.T) {
	a := &app{}
	var inPlace error
	a.run(jobTitle, "读取", func(context.Context) (jobResult, error) {
		return jobResult{}, fmt.Errorf("fixture failure")
	}, func(err error) { inPlace = err })
	if inPlace == nil || strings.Contains(a.status, "fixture failure") {
		t.Fatalf("in-place failure leaked to the status line: err=%v status=%q", inPlace, a.status)
	}
	a.run(jobTitle, "读取", func(context.Context) (jobResult, error) {
		return jobResult{}, fmt.Errorf("fixture failure")
	}, nil)
	if a.status != "读取失败：fixture failure" {
		t.Fatalf("status %q", a.status)
	}
}
