package drpy

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

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
		Modules: map[string]string{engineURL: string(engineJS)},
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
