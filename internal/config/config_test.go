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

func TestReadHTMLResponseExplainsConfigurationError(t *testing.T) {
	const target = "https://example.com/config"
	fetch := func(context.Context, string) (Response, error) {
		return Response{Status: 200, ContentType: "text/html; charset=utf-8", Body: []byte("<html>landing page</html>")}, nil
	}
	_, err := Read(context.Background(), target, fetch)
	if err == nil || !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "HTTP 200") || !strings.Contains(err.Error(), "Content-Type") || !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("error: %v", err)
	}
}

func TestReadDepotWithoutFollowingLines(t *testing.T) {
	const root = "https://example.com/box/depot.json"
	var calls []string
	fetch := func(_ context.Context, raw string) (Response, error) {
		calls = append(calls, raw)
		return Response{Status: 200, URL: root, Body: []byte(`{"urls":[{"name":"🚀演示","url":"./config.json"},{"name":"jar","url":"./app.jar"}]}`)}, nil
	}
	cfg, err := Read(context.Background(), root, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(cfg.Lines) != 2 || cfg.Lines[0].URL != "https://example.com/box/config.json" {
		t.Fatalf("cfg %+v calls %v", cfg, calls)
	}
	if _, err := Read(context.Background(), cfg.Lines[1].URL, fetch); err == nil || len(calls) != 1 {
		t.Fatal("jar was fetched")
	}
}

func TestXBPQAndConfiguredParsers(t *testing.T) {
	cfg, err := Read(context.Background(), "https://example.com/config.json", func(context.Context, string) (Response, error) {
		return Response{Status: 200, Body: []byte("{\"sites\":[{\"key\":\"rule\",\"type\":3,\"api\":\"csp_XBPQ\",\"ext\":\"./rule.json\"}],\"parses\":[{\"name\":\"web\",\"type\":0,\"url\":\"./parse?url=\"}]}")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sites[0].Kind != KindXBPQ || cfg.Sites[0].Kind.Unsupported() {
		t.Fatal(cfg.Sites)
	}
	if len(cfg.Parsers) != 1 || cfg.Parsers[0].URL != "https://example.com/parse?url=" {
		t.Fatal(cfg.Parsers)
	}
	if !Classify(3, "csp_Custom", "", "").Unsupported() {
		t.Fatal("custom jar misclassified")
	}
}

func TestReadHashCommentsPreservesStrings(t *testing.T) {
	const body = `{
   "sites": [{"key":"demo","name":"演示\"#站点","type":1,"api":"./api#section"}],
   # JSON parsing endpoints
   "parses": [
     {"name":"JSON","type":1,"url":"./parse?url="}, # inline comment
     /* mixed comments */
     # web parsing endpoints
     {"name":"Web","type":0,"url":"./player#fragment"}, # trailing comma
   ],
   "notice":"第一集$https://example.com/1.m3u8#第二集$https://example.com/2.m3u8",
   "flags":["test#flag", # array trailing comma
   ], # object trailing comma
 } # comment at end of file`
	for _, prefix := range []string{"", "# leading comment\n", "// first\n/* second */\n# third\n"} {
		for _, newline := range []string{"\n", "\r\n", "\r"} {
			input := strings.ReplaceAll(prefix+body, "\n", newline)
			cfg, err := Read(context.Background(), "https://example.com/TVbox.text", func(context.Context, string) (Response, error) {
				return Response{Status: 200, ContentType: "text/plain; charset=utf-8", Body: []byte(input)}, nil
			})
			if err != nil {
				t.Fatalf("prefix=%q newline=%q: %v", prefix, newline, err)
			}
			if len(cfg.Sites) != 1 || cfg.Sites[0].Name != "演示\"#站点" || cfg.Sites[0].API != "https://example.com/api#section" {
				t.Fatalf("site strings changed: %+v", cfg.Sites)
			}
			if len(cfg.Parsers) != 2 || cfg.Parsers[1].URL != "https://example.com/player#fragment" {
				t.Fatalf("parsers: %+v", cfg.Parsers)
			}
			if cfg.Notice != "第一集$https://example.com/1.m3u8#第二集$https://example.com/2.m3u8" || len(cfg.Flags) != 1 || cfg.Flags[0] != "test#flag" {
				t.Fatalf("hash inside string changed: notice=%q flags=%v", cfg.Notice, cfg.Flags)
			}
		}
	}
}

func TestParserTypeAcceptsNumberAndString(t *testing.T) {
	for _, item := range []struct {
		raw  string
		want int
	}{
		{"0", 0}, {"1", 1}, {"\"0\"", 0}, {"\"1\"", 1}, {"\"2\"", 2}, {"\"3\"", 3}, {"null", 0},
	} {
		body := []byte("{\"sites\":[{\"key\":\"demo\",\"type\":1,\"api\":\"https://example.com/api\"}],\"parses\":[{\"name\":\"演示解析\",\"type\":" + item.raw + ",\"url\":\"./parse?url=\",\"ext\":{\"header\":{\"X-Test\":\"preserved\"}}}]}")
		cfg, err := Read(context.Background(), "https://example.com/config.json", func(context.Context, string) (Response, error) { return Response{Status: 200, Body: body}, nil })
		if err != nil {
			t.Fatalf("type=%s: %v", item.raw, err)
		}
		if len(cfg.Sites) != 1 || len(cfg.Parsers) != 1 {
			t.Fatalf("type=%s: %+v", item.raw, cfg)
		}
		p := cfg.Parsers[0]
		if p.Type != item.want || p.Name != "演示解析" || p.URL != "https://example.com/parse?url=" || !strings.Contains(string(p.Ext), "preserved") {
			t.Fatalf("type=%s: %+v", item.raw, p)
		}
	}
}

func TestParserTypeRejectsNonInteger(t *testing.T) {
	for _, raw := range []string{"\"web\"", "true", "{}", "[]", "1.5", "\"1.5\"", "92233720368547758080"} {
		var p Parser
		if err := json.Unmarshal([]byte("{\"type\":"+raw+"}"), &p); err == nil {
			t.Fatalf("invalid type %s silently became %d", raw, p.Type)
		}
	}
}

func TestClassifyPanAndAListSites(t *testing.T) {
	const page = "https://cdn.example.com/box/cfg.json"
	body := []byte(`{
		"spider": "./app.jar",
		"sites": [
			{"key":"al1","name":"Alist","type":3,"api":"csp_Alist","ext":{"演示盘":"https://alist.example.com"}},
			{"key":"al2","name":"AlistJS","type":3,"api":"./lib/alist.min.js","ext":"./js/alist.json"},
			{"key":"qs","name":"夸克分享","type":3,"api":"csp_QuarkShare","ext":"./lib/token.json$$$./json/quarkshare.txt$$$db$$$1"},
			{"key":"wg","name":"开放玩偶","type":3,"api":"csp_Wobg","ext":"./lib/token.json$$$https://wogg.example.com/$$$null$$$1$$$./wogg.json"},
			{"key":"guard1","name":"私有玩偶","type":3,"api":"csp_WoGGGuard","ext":{"Cloud-drive":"tvfan/Cloud-drive.txt"}},
			{"key":"guard2","name":"私有密文","type":3,"api":"csp_SeedhubGuard","ext":"5++encrypted"}
		]
	}`)
	cfg, err := Read(context.Background(), page, func(_ context.Context, u string) (Response, error) {
		if strings.Contains(u, ".jar") {
			t.Errorf("refused to fetch jar: %s", u)
		}
		return Response{Status: 200, Body: body, URL: page}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Site{}
	for _, s := range cfg.Sites {
		by[s.Key] = s
	}
	if by["al1"].Kind != KindAList || by["al1"].Kind.Unsupported() {
		t.Fatalf("csp_Alist = %+v", by["al1"])
	}
	if by["al2"].Kind != KindAList || by["al2"].Ext != "https://cdn.example.com/box/js/alist.json" {
		t.Fatalf("alist.min.js = %+v", by["al2"])
	}
	if by["qs"].Kind != KindPanShare || !strings.Contains(by["qs"].Ext, "https://cdn.example.com/box/json/quarkshare.txt") {
		t.Fatalf("csp_QuarkShare = %+v", by["qs"])
	}
	if by["wg"].Kind != KindPanWeb || !strings.Contains(by["wg"].Ext, "https://wogg.example.com/") {
		t.Fatalf("csp_Wobg = %+v", by["wg"])
	}
	if by["guard1"].Kind != KindCSP || !by["guard1"].Kind.Unsupported() {
		t.Fatalf("private Guard without plaintext URL must stay unsupported: %+v", by["guard1"])
	}
	if by["guard2"].Kind != KindCSP || !by["guard2"].Kind.Unsupported() {
		t.Fatalf("encrypted Guard must stay unsupported: %+v", by["guard2"])
	}
}
