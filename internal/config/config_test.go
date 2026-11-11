package config

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	in := []byte(`{
	  // line
	  "a": "http://keep", /* block */
	  "b": 1,
	}`)
	out := string(Strip(in))
	if strings.Contains(out, "// line") || strings.Contains(out, "/*") || strings.Contains(out, "block") {
		t.Fatalf("comments left: %s", out)
	}
	if !strings.Contains(out, `"http://keep"`) {
		t.Fatalf("string damaged: %s", out)
	}
	var v struct {
		A string `json:"a"`
		B int    `json:"b"`
	}
	if err := parseJSON(out, &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "http://keep" || v.B != 1 {
		t.Fatalf("%+v", v)
	}
}

func parseJSON(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func TestDecodeMarked(t *testing.T) {
	raw := []byte(`{"sites":[{"key":"a","name":"n","type":1,"api":"http://example.com/api"}]}`)
	b64 := base64.StdEncoding.EncodeToString(raw)
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0x00, 0x11}, []byte("abcd1234**"+b64+"\n!!")...)
	got, err := Decode(jpeg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"type":1`) {
		t.Fatalf("jpeg decode: %s", got)
	}
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("zz**"+b64)...)
	got, err = Decode(png)
	if err != nil || !strings.Contains(string(got), "example.com") {
		t.Fatalf("png: %v %s", err, got)
	}
	got, err = Decode([]byte("prefix**" + b64))
	if err != nil || !looksLikeJSON(got) {
		t.Fatalf("text: %v %s", err, got)
	}
	plain := []byte(`{"spider":"http://example.com/a.jar;md5**zz","sites":[]}`)
	got, err = Decode(plain)
	if err != nil || string(got) != string(plain) {
		t.Fatalf("plain json with ** changed: %s", got)
	}
}

func TestLoadRelativeAndNoJarFetch(t *testing.T) {
	const page = "https://cdn.example/box/cfg.json"
	body := []byte(`{
		"spider": "./jar/app.jar;md5",
		"sites": [
			{"key":"d","name":"规则","type":3,"api":"./drpy2.min.js","ext":"../rules/a.js"},
			{"key":"j","name":"jar","type":3,"api":"csp_Demo"},
			{"key":"p","name":"py","type":3,"api":"./s.py"},
			{"key":"x","name":"xml","type":0,"api":"http://example.com/api.php/provide/vod/at/xml"},
			{"key":"js","name":"json","type":1,"api":"./provide/vod"},
			{"key":"fm","name":"类","type":3,"api":"./spider.js","ext":"./cfg.json"}
		]
	}`)
	var calls []string
	fetch := func(_ context.Context, u string) (Response, error) {
		calls = append(calls, u)
		if strings.Contains(u, ".jar") || strings.Contains(u, "drpy") || strings.Contains(u, "rules") {
			t.Errorf("loader fetched %s", u)
		}
		return Response{Status: 200, Body: body, URL: page}, nil
	}
	cfg, err := Load(context.Background(), page, "", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls %v", calls)
	}
	if cfg.Spider != "https://cdn.example/box/jar/app.jar;md5" {
		t.Fatalf("spider %s", cfg.Spider)
	}
	by := map[string]Site{}
	for _, s := range cfg.Sites {
		by[s.Key] = s
	}
	if by["d"].API != "https://cdn.example/box/drpy2.min.js" || by["d"].Ext != "https://cdn.example/rules/a.js" || by["d"].Kind != KindDRPY2 {
		t.Fatalf("drpy site %+v", by["d"])
	}
	if by["j"].Kind != KindCSP || by["j"].API != "csp_Demo" {
		t.Fatalf("csp %+v", by["j"])
	}
	if by["p"].Kind != KindPython {
		t.Fatalf("py %+v", by["p"])
	}
	if by["x"].Kind != KindXML || by["js"].Kind != KindJSON {
		t.Fatalf("cms %+v %+v", by["x"], by["js"])
	}
	if by["js"].API != "https://cdn.example/box/provide/vod" {
		t.Fatalf("json api %s", by["js"].API)
	}
	if by["fm"].Kind != KindFongMi {
		t.Fatalf("fongmi %+v", by["fm"])
	}
}

func TestDepotAndHTTPSFallback(t *testing.T) {
	raw := []byte(`{"sites":[{"key":"d","name":"规则","type":3,"api":"./drpy2.min.js","ext":"./a.js"}]}`)
	b64 := base64.StdEncoding.EncodeToString([]byte(`{
		"urls": [
			{"name":"备用","url":"./other.json"},
			{"name":"正线","url":"https://cdn.example/line.json"}
		]
	}`))
	depot := append([]byte{0xFF, 0xD8, 0xFF}, []byte("k**"+b64)...)
	var calls []string
	fetch := func(_ context.Context, u string) (Response, error) {
		calls = append(calls, u)
		switch u {
		case "http://cdn.example/depot.json":
			return Response{}, errString("connection refused")
		case "https://cdn.example/depot.json":
			return Response{Status: 200, Body: depot, URL: u}, nil
		case "https://cdn.example/line.json":
			return Response{Status: 200, Body: raw, URL: u}, nil
		default:
			t.Errorf("unexpected %s", u)
			return Response{Status: 404}, nil
		}
	}
	cfg, err := Load(context.Background(), "http://cdn.example/depot.json", "正线", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.FromDepot || cfg.Line != "正线" {
		t.Fatalf("depot meta %+v", cfg)
	}
	if cfg.Sites[0].API != "https://cdn.example/drpy2.min.js" || cfg.Sites[0].Kind != KindDRPY2 {
		t.Fatalf("site %+v", cfg.Sites[0])
	}
	if strings.Join(calls, ",") != "http://cdn.example/depot.json,https://cdn.example/depot.json,https://cdn.example/line.json" {
		t.Fatalf("calls %v", calls)
	}
	if _, err := Load(context.Background(), "https://cdn.example/depot.json", "没有", fetch); err == nil {
		t.Fatal("expected missing line")
	}
}

func TestClassifySource(t *testing.T) {
	api := "https://cdn.example/app.js"
	ext := "https://cdn.example/rule.js"
	if got := Classify(3, api, ext, ""); got != KindDRPY2 {
		t.Fatalf("paths %s", got)
	}
	if got := Classify(3, api, ext, "class App extends Spider {}"); got != KindFongMi {
		t.Fatalf("class %s", got)
	}
	if got := Classify(3, api, "https://cdn.example/cfg.json", "var rule = {}"); got != KindDRPY2 {
		t.Fatalf("rule source %s", got)
	}
	if got := Classify(3, "https://cdn.example/drpy2.min.js", ext, "function init(){}"); got != KindDRPY2 {
		t.Fatalf("engine name %s", got)
	}
}
