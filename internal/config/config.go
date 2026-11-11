// Package config loads a TVBox-style config the user points at.
//
// It distinguishes a FongMi depot (a urls list of lines) from a single
// config, accepts JSON with comments and trailing commas, unwraps
// JPEG/PNG bodies that carry prefix**base64, and falls back from HTTP to
// HTTPS. Relative api, ext and spider paths are resolved against the
// config URL. Jars are never downloaded.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
	"github.com/egoist/mygo/netproxy"
)

const maxBytes = 32 << 20

// Kind is how a site is driven.
type Kind string

const (
	KindXML     Kind = "xml"    // type 0, XML CMS
	KindJSON    Kind = "json"   // type 1, JSON CMS
	KindDRPY2   Kind = "drpy2"  // type 3, drpy2 engine plus a rule
	KindFongMi  Kind = "fongmi" // type 3, FongMi class spider
	KindXBPQ    Kind = "xbpq"   // csp_XBPQ text rules interpreted natively
	KindCSP     Kind = "csp"    // csp_ jar class, not supported
	KindPython  Kind = "python" // .py spider, not supported
	KindUnknown Kind = "unknown"
)

// Unsupported reports kinds this prototype does not run.
func (k Kind) Unsupported() bool {
	switch k {
	case KindCSP, KindPython, KindUnknown:
		return true
	default:
		return false
	}
}

// Line is one entry of a depot.
type Line struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Site is one video site with paths already resolved.
type Site struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Type int    `json:"type"`
	API  string `json:"api"`
	Ext  string `json:"ext"`
	Jar  string `json:"jar,omitempty"`
	Kind Kind   `json:"kind"`
}

// Parser is a TVBox web (type 0) or JSON (type 1) playback resolver.
type Parser struct {
	Name string          `json:"name"`
	Type int             `json:"type"`
	URL  string          `json:"url"`
	Ext  json.RawMessage `json:"ext,omitempty"`
}

// Config is a single (non-depot) configuration.
type Config struct {
	Parsers   []Parser      `json:"parses,omitempty"`
	Flags     []string      `json:"flags,omitempty"`
	URL       string        `json:"url"`
	Spider    string        `json:"spider,omitempty"`
	Sites     []Site        `json:"sites"`
	Lives     []iptv.Source `json:"lives,omitempty"`
	Notice    string        `json:"notice,omitempty"`
	FromDepot bool          `json:"-"`
	Line      string        `json:"-"`
	Lines     []Line        `json:"-"` // available choices when this document is a depot
}

// Response is one HTTP response used to load a config.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
	URL         string // final URL after redirects; empty means the request URL
}

// Fetcher loads one URL. Tests supply a map; the CLI uses HTTPFetcher.
type Fetcher func(ctx context.Context, u string) (Response, error)

// HTTPFetcher downloads with a plain user agent. file: URLs are read from
// disk. The client follows redirects.
func HTTPFetcher(client *http.Client) Fetcher {
	return httpFetcher(client, nil)
}

func httpFetcher(client *http.Client, headers map[string]string) Fetcher {
	if client == nil {
		client = &http.Client{Transport: netproxy.Transport(), Timeout: 30 * time.Second}
	}
	return func(ctx context.Context, raw string) (Response, error) {
		if strings.HasPrefix(raw, "file://") {
			p := strings.TrimPrefix(raw, "file://")
			b, err := os.ReadFile(p)
			if err != nil {
				return Response{}, err
			}
			return Response{Status: 200, Body: b, URL: raw}, nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return Response{}, err
		}
		req.Header.Set("User-Agent", "xiaomu/0.0.1")
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			return Response{}, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
		if err != nil {
			return Response{}, err
		}
		final := raw
		if resp.Request != nil && resp.Request.URL != nil {
			final = resp.Request.URL.String()
		}
		return Response{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body, URL: final}, nil
	}
}

// Load fetches pageURL, selects line when the document is a depot, and
// returns the resolved config. line is the depot entry name; empty uses
// the first line.
func Load(ctx context.Context, pageURL, line string, fetch Fetcher) (*Config, error) {
	if fetch == nil {
		fetch = HTTPFetcher(nil)
	}
	return load(ctx, pageURL, line, fetch, 0)
}

func load(ctx context.Context, pageURL, line string, fetch Fetcher, depth int) (*Config, error) {
	if depth > 4 {
		return nil, fmt.Errorf("config: depot nested too deep at %s", pageURL)
	}
	cfg, err := Read(ctx, pageURL, fetch)
	if err != nil {
		return nil, err
	}
	if len(cfg.Lines) == 0 {
		return cfg, nil
	}
	picked, err := pickLine(cfg.Lines, line)
	if err != nil {
		return nil, err
	}
	cfg, err = load(ctx, picked.URL, "", fetch, depth+1)
	if err != nil {
		return nil, err
	}
	cfg.FromDepot, cfg.Line = true, picked.Name
	return cfg, nil
}

// Read loads one document without choosing a depot line. Lines and Sites have
// resolved URLs; callers can present the choices before fetching another file.
func Read(ctx context.Context, pageURL string, fetch Fetcher) (*Config, error) {
	if extOf(pageURL) == ".jar" {
		return nil, fmt.Errorf("config: refusing to download jar %s", pageURL)
	}
	if fetch == nil {
		fetch = HTTPFetcher(nil)
	}
	res, err := fetchFallback(ctx, pageURL, fetch)
	if err != nil {
		return nil, err
	}
	base := res.URL
	if base == "" {
		base = pageURL
	}
	body, err := Decode(res.Body)
	if err != nil {
		return nil, fmt.Errorf("config: %s: HTTP %d%s: %w", pageURL, res.Status, contentTypeDetail(res.ContentType), err)
	}
	if !looksLikeJSON(body) {
		what := "返回的内容不是 TVBox 配置 JSON 或 IPTV 频道列表，请检查配置地址"
		if strings.HasPrefix(strings.TrimSpace(string(body)), "<") || strings.Contains(strings.ToLower(res.ContentType), "html") {
			what = "返回的是 HTML 页面，不是 TVBox 配置 JSON，请检查配置地址"
		} else if channels, err := iptv.Parse(base, body); err == nil {
			return &Config{URL: base, Lives: []iptv.Source{{Name: "IPTV 直播", Channels: channels}}}, nil
		}
		return nil, fmt.Errorf("config: %s: HTTP %d%s: %s", pageURL, res.Status, contentTypeDetail(res.ContentType), what)
	}
	clean := Strip(body)
	if strings.HasPrefix(strings.TrimSpace(string(clean)), "[") {
		lives, err := iptv.ParseSources(base, clean)
		if err != nil {
			return nil, err
		}
		return &Config{URL: base, Lives: lives}, nil
	}
	var head struct {
		URLs   []Line          `json:"urls"`
		Sites  json.RawMessage `json:"sites"`
		Spider string          `json:"spider"`
		Notice string          `json:"notice"`
		Lives  json.RawMessage `json:"lives"`
	}
	if err := json.Unmarshal(clean, &head); err != nil {
		return nil, fmt.Errorf("config: parse %s: HTTP %d%s: %w", pageURL, res.Status, contentTypeDetail(res.ContentType), err)
	}
	if isDepot(head.URLs, head.Sites) && (len(head.Lives) == 0 || string(head.Lives) == "null") {
		for i := range head.URLs {
			head.URLs[i].URL = urljoin.Join(base, strings.TrimSpace(head.URLs[i].URL))
		}
		return &Config{URL: base, Lines: head.URLs, FromDepot: true}, nil
	}
	return buildConfig(base, clean, head.Spider, head.Notice)
}

func isDepot(urls []Line, sites json.RawMessage) bool {
	if len(urls) == 0 {
		return false
	}
	s := bytesTrim(sites)
	return len(s) == 0 || string(s) == "null"
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func pickLine(lines []Line, name string) (Line, error) {
	if len(lines) == 0 {
		return Line{}, fmt.Errorf("config: depot has no lines")
	}
	if strings.TrimSpace(name) == "" {
		return lines[0], nil
	}
	var names []string
	for _, ln := range lines {
		if ln.Name == name {
			return ln, nil
		}
		names = append(names, ln.Name)
	}
	return Line{}, fmt.Errorf("config: line %q not found (%s)", name, strings.Join(names, ", "))
}

func buildConfig(base string, clean []byte, spider, notice string) (*Config, error) {
	var raw struct {
		Parsers []Parser        `json:"parses"`
		Flags   []string        `json:"flags"`
		Lives   json.RawMessage `json:"lives"`
		Sites   []struct {
			Key  string          `json:"key"`
			Name string          `json:"name"`
			Type int             `json:"type"`
			API  string          `json:"api"`
			Ext  json.RawMessage `json:"ext"`
			Jar  string          `json:"jar"`
		} `json:"sites"`
	}
	if err := json.Unmarshal(clean, &raw); err != nil {
		return nil, fmt.Errorf("config: parse sites: %w", err)
	}
	cfg := &Config{
		URL:     base,
		Spider:  resolveSpider(base, spider),
		Notice:  notice,
		Parsers: raw.Parsers, Flags: raw.Flags,
	}
	for i := range cfg.Parsers {
		cfg.Parsers[i].URL = urljoin.Join(base, cfg.Parsers[i].URL)
	}
	var err error
	cfg.Lives, err = iptv.ParseSources(base, raw.Lives)
	if err != nil {
		return nil, fmt.Errorf("config: 读取直播配置：%w", err)
	}
	for _, s := range raw.Sites {
		api := resolveMaybe(base, s.API)
		ext := extString(base, s.Ext)
		jar := resolveSpider(base, s.Jar)
		cfg.Sites = append(cfg.Sites, Site{
			Key:  s.Key,
			Name: s.Name,
			Type: s.Type,
			API:  api,
			Ext:  ext,
			Jar:  jar,
			Kind: Classify(s.Type, api, ext, ""),
		})
	}
	return cfg, nil
}

func extString(base string, raw json.RawMessage) string {
	s := bytesTrim(raw)
	if len(s) == 0 || string(s) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(s, &text) == nil {
		return resolveMaybe(base, text)
	}
	return string(s)
}

func resolveSpider(base, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	parts := strings.Split(v, ";")
	parts[0] = resolveMaybe(base, parts[0])
	return strings.Join(parts, ";")
}

// resolveMaybe rewrites relative paths and leaves csp_ names and ordinary
// strings alone.
func resolveMaybe(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || isCSP(ref) {
		return ref
	}
	if strings.Contains(ref, "://") {
		return ref
	}
	if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") || strings.HasPrefix(ref, "/") || strings.Contains(ref, "/") || extOf(ref) != "" {
		return urljoin.Join(base, ref)
	}
	return ref
}

func fetchFallback(ctx context.Context, raw string, fetch Fetcher) (Response, error) {
	res, err := fetch(ctx, raw)
	if failed(res, err) && strings.HasPrefix(strings.ToLower(raw), "http://") {
		alt := "https://" + raw[len("http://"):]
		res2, err2 := fetch(ctx, alt)
		if !failed(res2, err2) {
			return res2, nil
		}
	}
	if err != nil {
		return Response{}, fmt.Errorf("config: get %s: %w", raw, err)
	}
	if res.Status < 200 || res.Status >= 300 {
		return Response{}, fmt.Errorf("config: get %s: HTTP %d%s", raw, res.Status, contentTypeDetail(res.ContentType))
	}
	return res, nil
}

func contentTypeDetail(contentType string) string {
	if contentType == "" {
		return ""
	}
	return fmt.Sprintf(" (Content-Type %q)", contentType)
}

func failed(res Response, err error) bool {
	return err != nil || res.Status < 200 || res.Status >= 300
}

// Classify decides how a site runs. source is an optional snippet of the
// api script; empty means classify from the paths alone.
func Classify(typ int, api, ext, source string) Kind {
	if extOf(api) == ".py" || extOf(ext) == ".py" {
		return KindPython
	}
	if isCSP(api) {
		if strings.Split(path.Base(api), ";")[0] == "csp_XBPQ" {
			return KindXBPQ
		}
		return KindCSP
	}
	switch typ {
	case 0:
		return KindXML
	case 1:
		return KindJSON
	case 3:
		return classifyJS(api, ext, source)
	default:
		return KindUnknown
	}
}

func classifyJS(api, ext, source string) Kind {
	if extOf(api) != ".js" && extOf(ext) != ".js" {
		return KindUnknown
	}
	if strings.Contains(source, "extends Spider") {
		return KindFongMi
	}
	base := strings.ToLower(path.Base(stripQuery(api)))
	if strings.Contains(base, "drpy") || strings.Contains(source, "var rule") || strings.Contains(source, "let rule") {
		return KindDRPY2
	}
	// A shared engine plus a separate rule file.
	if extOf(api) == ".js" && extOf(ext) == ".js" {
		return KindDRPY2
	}
	if extOf(api) == ".js" {
		return KindFongMi
	}
	return KindUnknown
}

func isCSP(api string) bool {
	api = strings.TrimSpace(api)
	base := path.Base(stripQuery(api))
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = base[:i]
	}
	return strings.HasPrefix(base, "csp_") && !strings.Contains(base, ".")
}

func extOf(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "{") || strings.HasPrefix(ref, "[") {
		return ""
	}
	ref = stripQuery(ref)
	if i := strings.IndexByte(ref, ';'); i >= 0 {
		ref = ref[:i]
	}
	if u, err := url.Parse(ref); err == nil && u.Path != "" {
		ref = u.Path
	}
	return strings.ToLower(path.Ext(ref))
}

func stripQuery(ref string) string {
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		return ref[:i]
	}
	return ref
}
