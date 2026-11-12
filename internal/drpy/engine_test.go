package drpy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCallAwaitTimerCompatibility(t *testing.T) {
	const entry = "https://example.com/timer.js"
	eng, err := Open(Options{Modules: map[string]string{entry: `export default {
  run() {
    return new Promise(resolve => {
      const canceled = setTimeout(() => resolve("wrong"), 10);
      clearTimeout(canceled);
      setTimeout(() => resolve("ready"), 10);
    });
  }
};`}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.Load(entry); err != nil {
		t.Fatal(err)
	}
	got, err := eng.CallAwait(context.Background(), "run")
	if err != nil || got != "ready" {
		t.Fatalf("timer result %q, %v", got, err)
	}
}

func TestRequestHTMLFailureNamesUpstream(t *testing.T) {
	const entry = "https://example.com/engine.js"
	const api = "https://example.com/api/list"
	eng, err := Open(Options{
		Modules: map[string]string{entry: `export default {
  category() { return JSON.parse(req("https://example.com/api/list").content); }
};`},
		Fetch: func(context.Context, Request) (Result, error) {
			return Result{Status: 404, Headers: map[string]string{"Content-Type": "text/html"}, Content: "<html>Not Found</html>"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.Load(entry); err != nil {
		t.Fatal(err)
	}
	_, err = eng.Call(context.Background(), "category")
	if err == nil || !strings.Contains(err.Error(), api) || !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "Content-Type") {
		t.Fatalf("error: %v", err)
	}
}

func TestLoadContextCancelsImport(t *testing.T) {
	started := make(chan struct{})
	eng, err := Open(Options{
		Modules: map[string]string{"https://example.com/engine.js": `import "./slow.js"; export default {};`},
		Fetch: func(ctx context.Context, _ Request) (Result, error) {
			close(started)
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.LoadContext(ctx, "https://example.com/engine.js") }()
	select {
	case <-started:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("import did not start")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("load error %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("import did not cancel")
	}
	if err := eng.LoadContext(ctx, "https://example.com/engine.js"); !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled: %v", err)
	}
}

func TestLocalModuleOverrides(t *testing.T) {
	const base = "https://example.com/lib/"
	dir := t.TempDir()
	for name, src := range map[string]string{
		"modules.json":  `{"https://example.com/lib/dependency.js":"dependency.js","https://example.com/lib/child.js":"child.js"}`,
		"dependency.js": `import value from "./child.js"; export default value;`,
		"child.js":      `export default 0;`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var fetched []string
	eng, err := Open(Options{
		AssetsDir: dir,
		Modules: map[string]string{
			base + "engine.js": `import local from "./dependency.js";
import remote from "https://other.example/dependency.js";
export default { home() { return String(local + remote); } };`,
			base + "child.js": `export default 40;`,
		},
		Fetch: func(_ context.Context, req Request) (Result, error) {
			fetched = append(fetched, req.URL)
			if req.URL != "https://other.example/dependency.js" {
				return Result{}, fmt.Errorf("unexpected network request: %s", req.URL)
			}
			return Result{Status: 200, Content: `export default 2;`}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.Load(base + "engine.js"); err != nil {
		t.Fatal(err)
	}
	got, err := eng.Call(context.Background(), "home")
	if err != nil || got != "42" || len(fetched) != 1 {
		t.Fatalf("home = %q, %v; fetched %v", got, err, fetched)
	}
}

func TestInvalidModuleOverrides(t *testing.T) {
	for _, tt := range []struct {
		name, manifest, want string
	}{
		{"malformed", `{`, "modules.json"},
		{"missing file", `{"https://example.com/lib.js":"missing.js"}`, "https://example.com/lib.js"},
		{"parent path", `{"https://example.com/lib.js":"../outside.js"}`, "must be relative"},
		{"absolute path", `{"https://example.com/lib.js":"/outside.js"}`, "must be relative"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "modules.json"), []byte(tt.manifest), 0600); err != nil {
				t.Fatal(err)
			}
			eng, err := Open(Options{AssetsDir: dir})
			if err == nil {
				eng.Close()
				t.Fatal("expected override error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestRemoteModuleResponse(t *testing.T) {
	const js = `export default { home() { return "loaded"; } };`
	for _, tt := range []struct {
		name, contentType, body string
		status                  int
		wantErr                 string
	}{
		{"forbidden", "text/html; charset=UTF-8", "<!DOCTYPE html><html>blocked</html>", 403, "HTTP 403"},
		{"missing", "text/plain", "not found", 404, "HTTP 404"},
		{"html", "text/html", "<html>challenge</html>", 200, "expected JavaScript"},
		{"disguised html", "application/javascript", "\ufeff \n<!DOCTYPE html><html>challenge</html>", 200, "expected JavaScript"},
		{"json", "application/json", `{"error":"blocked"}`, 200, "expected JavaScript"},
		{"javascript", "text/javascript; charset=utf-8", js, 200, ""},
		{"plain text", "text/plain; charset=utf-8", js, 200, ""},
		{"binary download", "application/octet-stream", js, 200, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/engine.js" {
					w.Header().Set("Content-Type", "text/javascript")
					fmt.Fprint(w, `export { default } from "./dependency.js";`)
					return
				}
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			eng, err := Open(Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			err = eng.Load(srv.URL + "/engine.js")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected module response error")
				}
				for _, want := range []string{tt.wantErr, srv.URL + "/dependency.js", fmt.Sprintf("HTTP %d", tt.status), tt.contentType} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not contain %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := eng.Call(context.Background(), "home")
			if err != nil || got != "loaded" {
				t.Fatalf("home = %q, %v", got, err)
			}
		})
	}
}

func TestClassicScriptPublishesCryptoJS(t *testing.T) {
	const (
		libURL   = "https://fixture.example/crypto-js.js"
		entryURL = "https://fixture.example/entry.js"
	)
	entry := `import "./crypto-js.js";
export default {
  init() {},
  home() { return JSON.stringify({ class: [{ type_id: "1", type_name: globalThis.CryptoJS && globalThis.CryptoJS.tag || "" }] }); },
  category() { return "{}"; },
  detail() { return "{}"; },
  search() { return "{}"; },
  play() { return "{}"; }
};`
	eng, err := Open(Options{Modules: map[string]string{
		libURL:   `module.exports = { AES: {}, tag: "ok" };`,
		entryURL: entry,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.Load(entryURL); err != nil {
		t.Fatal(err)
	}
	home, err := eng.Call(context.Background(), "home", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(home, `"type_name":"ok"`) {
		t.Fatalf("home %s", home)
	}
}

func TestRuleChain(t *testing.T) {
	engineJS, err := os.ReadFile("testdata/engine.js")
	if err != nil {
		t.Fatal(err)
	}
	ruleJS, err := os.ReadFile("testdata/rule.js")
	if err != nil {
		t.Fatal(err)
	}
	const (
		engineURL = "https://fixture.example/engine.js"
		ruleURL   = "https://fixture.example/rule.js"
		listURL   = "https://fixture.example/list/1/1"
		detailURL = "https://fixture.example/v/7"
		searchURL = "https://fixture.example/search?wd=%E7%94%B2&pg=1"
	)
	pages := map[string]string{
		ruleURL: string(ruleJS),
		listURL: `<div class="list">
			<div class="item"><a href="/v/7">示例甲</a><img data-src="/pic/7.jpg"></div>
			<div class="item"><a href="/v/8">示例乙</a></div>
		</div>`,
		detailURL: `<h1>示例甲</h1><div class="eps"><a href="/play/7">第1集</a><a href="https://cdn.example/a.m3u8">第2集</a></div>`,
		searchURL: `<div class="list"><div class="item"><a href="/v/7">示例甲</a></div></div>`,
	}
	var seen []Request
	eng, err := Open(Options{
		AssetsDir: "assets", // Explicit offline stand-ins for this host-only fixture.
		Modules:   map[string]string{engineURL: string(engineJS)},
		Fetch: func(_ context.Context, req Request) (Result, error) {
			seen = append(seen, req)
			body, ok := pages[req.URL]
			if !ok {
				t.Errorf("unexpected req %s", req.URL)
				return Result{Content: "", Headers: map[string]string{}}, nil
			}
			return Result{Content: body, Headers: map[string]string{"Content-Type": "text/html"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.Load(engineURL); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := eng.Call(ctx, "init", ruleURL); err != nil {
		t.Fatal(err)
	}
	home, err := eng.Call(ctx, "home", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(home, "电影") || !strings.Contains(home, "剧集") {
		t.Fatalf("home %s", home)
	}
	cat, err := eng.Call(ctx, "category", "1", 1, map[string]any{}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var catObj struct {
		List []struct {
			ID   string `json:"vod_id"`
			Name string `json:"vod_name"`
			Pic  string `json:"vod_pic"`
		} `json:"list"`
	}
	if err := json.Unmarshal([]byte(cat), &catObj); err != nil {
		t.Fatal(err, cat)
	}
	if len(catObj.List) != 2 || catObj.List[0].Name != "示例甲" || catObj.List[0].ID != detailURL {
		t.Fatalf("category %+v raw %s", catObj.List, cat)
	}
	if catObj.List[0].Pic != "/pic/7.jpg" {
		t.Fatalf("pic %q", catObj.List[0].Pic)
	}
	detail, err := eng.Call(ctx, "detail", detailURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "示例甲") || !strings.Contains(detail, "https://fixture.example/play/7") || !strings.Contains(detail, "https://cdn.example/a.m3u8") {
		t.Fatalf("detail %s", detail)
	}
	search, err := eng.Call(ctx, "search", "甲", false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(search, "示例甲") {
		t.Fatalf("search %s", search)
	}
	play, err := eng.Call(ctx, "play", "默认", "https://fixture.example/play/7", []any{})
	if err != nil {
		t.Fatal(err)
	}
	var playObj struct {
		URL    string            `json:"url"`
		Header map[string]string `json:"header"`
	}
	if err := json.Unmarshal([]byte(play), &playObj); err != nil {
		t.Fatal(err, play)
	}
	if playObj.URL != "https://fixture.example/play/7" || playObj.Header["X-Local"] != "1" {
		t.Fatalf("play %+v", playObj)
	}
	var referer string
	for _, req := range seen {
		if req.URL == listURL {
			referer = req.Headers["Referer"]
		}
	}
	if referer != "https://fixture.example" {
		t.Fatalf("list referer %q calls %#v", referer, seen)
	}
}
