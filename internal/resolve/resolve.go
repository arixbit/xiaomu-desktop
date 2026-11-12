// Package resolve turns a site player result into a media URL. Web pages are
// inspected before AVPlayer is opened, with WebView sniffing for dynamic pages.
package resolve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
	"github.com/egoist/mygo/netproxy"
)

const UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
const pageLimit = 2 << 20

type Sniffer func(context.Context, catalog.Playback) (catalog.Playback, error)

type Resolver struct {
	Client  *http.Client
	Parsers []config.Parser
	Sniff   Sniffer
}

// Direct reports media extensions in the path, never in a page's query.
func Direct(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".m3u8", ".mp4", ".m4v", ".mov", ".mp3", ".m4a", ".aac", ".flv", ".ts":
		return true
	}
	return false
}

func (r Resolver) Resolve(ctx context.Context, play catalog.Playback) (catalog.Playback, error) {
	if err := ctx.Err(); err != nil {
		return catalog.Playback{}, err
	}
	if play.PlayURL != "" {
		play.URL = play.PlayURL + play.URL
		play.PlayURL = ""
	} else if Direct(play.URL) && !play.JX {
		play.Parse = false
		return play, nil
	}
	client := r.Client
	if client == nil {
		jar, _ := cookiejar.New(nil)
		client = &http.Client{Transport: netproxy.Transport(), Jar: jar, Timeout: 12 * time.Second}
	}
	play.Headers = maps.Clone(play.Headers)
	if play.Headers == nil {
		play.Headers = map[string]string{}
	}
	if header(play.Headers, "User-Agent") == "" {
		play.Headers["User-Agent"] = UserAgent
	}
	out, err := inspect(ctx, client, play, map[string]bool{}, 0)
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return catalog.Playback{}, ctx.Err()
	}

	// Only parsers supplied by this configuration are used. No resolver service
	// or source address is built into the app.
	var parserErrors []string
	for _, parser := range r.Parsers {
		if parser.Type != 0 && parser.Type != 1 {
			continue
		}
		if len(parserErrors) >= 3 || ctx.Err() != nil {
			break
		}
		parseCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
		result, parseErr := r.parse(parseCtx, client, parser, play)
		cancel()
		if parseErr == nil {
			return result, nil
		}
		parserErrors = append(parserErrors, parser.Name+": "+parseErr.Error())
	}
	if r.Sniff != nil {
		out, sniffErr := r.Sniff(ctx, play)
		if sniffErr == nil {
			return out, nil
		}
		return catalog.Playback{}, fmt.Errorf("网页取流失败：%w", sniffErr)
	}
	return catalog.Playback{}, err
}

func inspect(ctx context.Context, client *http.Client, play catalog.Playback, seen map[string]bool, depth int) (catalog.Playback, error) {
	if depth > 4 || seen[play.URL] {
		return catalog.Playback{}, fmt.Errorf("播放页循环跳转")
	}
	seen[play.URL] = true
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, play.URL, nil)
	if err != nil || req.URL.Host == "" || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return catalog.Playback{}, fmt.Errorf("播放地址不是 http/https")
	}
	for k, v := range play.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return catalog.Playback{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return catalog.Playback{}, fmt.Errorf("播放站点返回 HTTP %d", resp.StatusCode)
	}
	prefix := make([]byte, 512)
	n, _ := io.ReadFull(resp.Body, prefix)
	prefix = prefix[:n]
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	text := strings.TrimSpace(string(prefix))
	isHTML := strings.HasPrefix(text, "<") || strings.Contains(ct, "text/html")
	if !isHTML && (strings.HasPrefix(text, "#EXTM3U") || strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/") || strings.Contains(ct, "mpegurl") || (len(prefix) > 8 && string(prefix[4:8]) == "ftyp")) {
		play.URL = resp.Request.URL.String()
		play.Parse, play.JX = false, false
		if client.Jar != nil {
			cookies := client.Jar.Cookies(resp.Request.URL)
			if len(cookies) > 0 {
				var parts []string
				for _, c := range cookies {
					parts = append(parts, c.Name+"="+c.Value)
				}
				play.Headers["Cookie"] = strings.Join(parts, "; ")
			}
		}
		return play, nil
	}
	rest, err := io.ReadAll(io.LimitReader(resp.Body, pageLimit+1))
	if err != nil {
		return catalog.Playback{}, err
	}
	if len(rest) > pageLimit {
		return catalog.Playback{}, fmt.Errorf("播放页面超过大小限制")
	}
	body := string(prefix) + string(rest)
	base := resp.Request.URL.String()
	candidates := Candidates(base, body)
	var last error
	for i, c := range candidates {
		if i >= 12 {
			break
		}
		if seen[c] {
			continue
		}
		next := play
		next.URL = c
		next.Headers = maps.Clone(play.Headers)
		next.Headers["Referer"] = base
		out, e := inspect(ctx, client, next, seen, depth+1)
		if e == nil {
			return out, nil
		}
		last = e
		if ctx.Err() != nil {
			return catalog.Playback{}, ctx.Err()
		}
	}
	if last != nil {
		return catalog.Playback{}, last
	}
	return catalog.Playback{}, fmt.Errorf("页面尚未给出媒体地址，需要执行网页播放器")
}

var quoted = regexp.MustCompile("[\"']([^\"'<>\\r\\n]+)[\"']")
var playerObject = regexp.MustCompile("(?s)(?:var|let|const)\\s+player_[a-zA-Z0-9_]+\\s*=\\s*(\\{[^\\n]*?\\})\\s*(?:;|<)")

// Candidates extracts ordinary HTML5 players, MacCMS player objects, embedded
// media literals and iframe players without evaluating untrusted JavaScript.
func Candidates(base, body string) []string {
	var out, frames []string
	seen := map[string]bool{}
	add := func(raw string) {
		raw = html.UnescapeString(strings.ReplaceAll(strings.TrimSpace(raw), "\\/", "/"))
		if raw == "" {
			return
		}
		raw = urljoin.Join(base, raw)
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, raw)
	}
	doc, e := goquery.NewDocumentFromReader(strings.NewReader(body))
	if e == nil {
		doc.Find("video[src], source[src], audio[src]").Each(func(_ int, s *goquery.Selection) { v, _ := s.Attr("src"); add(v) })
	}
	for _, m := range playerObject.FindAllStringSubmatch(body, -1) {
		var p struct {
			URL     string
			Encrypt catalog.ID
		}
		if json.Unmarshal([]byte(m[1]), &p) != nil {
			continue
		}
		raw := p.URL
		if p.Encrypt == "2" {
			if b, e := base64.StdEncoding.DecodeString(raw); e == nil {
				raw = string(b)
			}
		}
		if p.Encrypt == "1" || p.Encrypt == "2" {
			if v, e := url.PathUnescape(raw); e == nil {
				raw = v
			}
		}
		add(raw)
	}
	normalized := strings.ReplaceAll(body, "\\/", "/")
	for _, m := range quoted.FindAllStringSubmatch(normalized, -1) {
		if Direct(m[1]) {
			add(m[1])
		}
	}
	if e == nil {
		doc.Find("iframe[src]").Each(func(_ int, s *goquery.Selection) { v, _ := s.Attr("src"); frames = append(frames, v) })
	}
	for _, f := range frames {
		add(f)
	}
	return out
}

func header(h map[string]string, k string) string {
	for n, v := range h {
		if strings.EqualFold(n, k) {
			return v
		}
	}
	return ""
}
