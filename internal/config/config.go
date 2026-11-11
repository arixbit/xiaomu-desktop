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

	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
)

const maxBytes = 32 << 20

// Kind is how a site is driven.
type Kind string

const (
	KindXML     Kind = "xml"    // type 0, XML CMS
	KindJSON    Kind = "json"   // type 1, JSON CMS
	KindDRPY2   Kind = "drpy2"  // type 3, drpy2 engine plus a rule
	KindFongMi  Kind = "fongmi" // type 3, FongMi class spider
	KindCSP     Kind = "csp"    // csp_ jar class, not supported
	KindPython  Kind = "python" // .py spider, not supported
	KindUnknown Kind = "unknown"
)

// Unsupported reports kinds this prototype does not run.
func (k Kind) Unsupported() bool {
	switch k {
	case KindCSP, KindPython, KindUnknown, KindFongMi, KindXML, KindJSON:
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

// Config is a single (non-depot) configuration.
type Config struct {
	URL       string `json:"url"`
	Spider    string `json:"spider,omitempty"`
	Sites     []Site `json:"sites"`
	Notice    string `json:"notice,omitempty"`
	FromDepot bool   `json:"-"`
	Line      string `json:"-"`
}

// Response is one HTTP response used to load a config.
type Response struct {
	Status int
	Body   []byte
	URL    string // final URL after redirects; empty means the request URL
}

// Fetcher loads one URL. Tests supply a map; the CLI uses HTTPFetcher.
type Fetcher func(ctx context.Context, u string) (Response, error)

// HTTPFetcher downloads with a plain user agent. file: URLs are read from
// disk. The client follows redirects.
func HTTPFetcher(client *http.Client) Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
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
		return Response{Status: resp.StatusCode, Body: body, URL: final}, nil
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
		return nil, fmt.Errorf("config: %s: %w", pageURL, err)
	}
	clean := Strip(body)
	var head struct {
		URLs   []Line          `json:"urls"`
		Sites  json.RawMessage `json:"sites"`
		Spider string          `json:"spider"`
		Notice string          `json:"notice"`
	}
	if err := json.Unmarshal(clean, &head); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", pageURL, err)
	}
	if isDepot(head.URLs, head.Sites) {
		picked, err := pickLine(head.URLs, line)
		if err != nil {
			return nil, err
		}
		next := urljoin.Join(base, strings.TrimSpace(picked.URL))
		if extOf(next) == ".jar" {
			return nil, fmt.Errorf("config: refusing to download jar %s", next)
		}
		cfg, err := load(ctx, next, "", fetch, depth+1)
		if err != nil {
			return nil, err
		}
		cfg.FromDepot = true
		cfg.Line = picked.Name
		return cfg, nil
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
		Sites []struct {
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
		URL:    base,
		Spider: resolveSpider(base, spider),
		Notice: notice,
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
		return Response{}, fmt.Errorf("config: get %s: status %d", raw, res.Status)
	}
	return res, nil
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
