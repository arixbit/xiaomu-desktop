// Package xbpq interprets the common text rules used by csp_XBPQ locally.
// It reads the rule's HTML templates; it neither loads nor executes Android jars.
package xbpq

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
	"github.com/egoist/mygo/netproxy"
	"golang.org/x/net/html/charset"
)

type Session struct {
	rule    map[string]json.RawMessage
	home    string
	headers map[string]string
	client  *http.Client
}

func Open(ctx context.Context, site config.Site) (*Session, []catalog.Category, error) {
	raw := strings.TrimSpace(site.Ext)
	if !strings.HasPrefix(raw, "{") {
		res, err := config.HTTPFetcher(nil)(ctx, raw)
		if err != nil {
			return nil, nil, err
		}
		if res.Status < 200 || res.Status >= 300 {
			return nil, nil, fmt.Errorf("读取 XBPQ 规则：HTTP %d", res.Status)
		}
		body, err := config.Decode(res.Body)
		if err != nil {
			return nil, nil, err
		}
		raw = string(body)
	}
	jar, _ := cookiejar.New(nil)
	s := &Session{headers: map[string]string{"User-Agent": resolve.UserAgent}, client: &http.Client{Transport: netproxy.Transport(), Jar: jar, Timeout: 18 * time.Second}}
	if err := json.Unmarshal(config.Strip([]byte(raw)), &s.rule); err != nil {
		return nil, nil, fmt.Errorf("读取 XBPQ 规则：%w", err)
	}
	s.home = s.value("主页url")
	u, err := url.Parse(s.home)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, nil, fmt.Errorf("XBPQ 规则缺少有效的主页url")
	}
	var headers map[string]string
	if json.Unmarshal(s.rule["请求头"], &headers) == nil {
		maps.Copy(s.headers, headers)
	} else {
		switch h := s.value("请求头"); h {
		case "手机":
			s.headers["User-Agent"] = "Mozilla/5.0 (Linux; Android 12) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36"
		case "", "电脑":
		default:
			for _, pair := range strings.Split(h, "#") {
				k, v, ok := strings.Cut(pair, "$")
				if ok {
					s.headers[k] = v
				}
			}
		}
	}
	var categories []catalog.Category
	for _, part := range strings.Split(s.value("分类"), "#") {
		name, id, ok := strings.Cut(part, "$")
		if ok && id != "" {
			categories = append(categories, catalog.Category{ID: catalog.ID(id), Name: name})
		}
	}
	if len(categories) == 0 && s.value("分类值") != "" {
		names := strings.Split(s.value("分类"), "&")
		ids := strings.Split(s.value("分类值"), "&")
		for i, id := range ids {
			if i < len(names) && id != "" {
				categories = append(categories, catalog.Category{ID: catalog.ID(id), Name: names[i]})
			}
		}
	}
	if len(categories) == 0 {
		return nil, nil, fmt.Errorf("XBPQ 规则未提供分类")
	}
	return s, categories, nil
}

func (s *Session) Close() { s.client.CloseIdleConnections() }
func (s *Session) value(key string) string {
	var v string
	if json.Unmarshal(s.rule[key], &v) == nil {
		return strings.TrimSpace(v)
	}
	return ""
}

var placeholder = regexp.MustCompile("\\{([a-zA-Z]+)\\}")

func template(raw string, values map[string]string) string {
	return placeholder.ReplaceAllStringFunc(raw, func(m string) string { return values[m[1:len(m)-1]] })
}

func (s *Session) Category(ctx context.Context, id string, page int) (catalog.Page, error) {
	raw := s.value("分类url")
	if raw == "" {
		return catalog.Page{}, fmt.Errorf("XBPQ 规则未提供分类url")
	}
	raw = template(raw, map[string]string{"cateId": id, "catePg": strconv.Itoa(max(1, page))})
	return s.list(ctx, urljoin.Join(s.home, raw), false)
}
func (s *Session) Search(ctx context.Context, query string, page int) (catalog.Page, error) {
	raw := s.value("搜索url")
	if raw == "" {
		raw = "/index.php/vod/search.html?wd={wd}&page={pg}"
	}
	raw = template(raw, map[string]string{"wd": url.QueryEscape(query), "searchWord": url.QueryEscape(query), "pg": strconv.Itoa(max(1, page)), "catePg": strconv.Itoa(max(1, page))})
	return s.list(ctx, urljoin.Join(s.home, raw), true)
}
func (s *Session) fetch(ctx context.Context, raw string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", "", err
	}
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Referer", s.home)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("站点返回 HTTP %d", resp.StatusCode)
	}
	reader, err := charset.NewReader(io.LimitReader(resp.Body, 8<<20), resp.Header.Get("Content-Type"))
	if err != nil {
		return "", "", err
	}
	body, err := io.ReadAll(reader)
	return string(body), resp.Request.URL.String(), err
}

func (s *Session) list(ctx context.Context, raw string, search bool) (catalog.Page, error) {
	body, base, err := s.fetch(ctx, raw)
	if err != nil {
		return catalog.Page{}, err
	}
	rule := func(key string) string {
		if search {
			if v := s.value("搜索" + key); v != "" {
				return v
			}
		}
		return s.value(key)
	}
	blocks := extractAll(body, rule("数组"))
	if rule("数组") == "" {
		doc, _ := goquery.NewDocumentFromReader(strings.NewReader(body))
		doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
			if a.Find("img").Length() > 0 || a.Is(".stui-vodlist__thumb,.myui-vodlist__thumb") {
				if raw, e := goquery.OuterHtml(a); e == nil {
					blocks = append(blocks, raw)
				}
			}
		})
	}
	out := catalog.Page{}
	seen := map[string]bool{}
	for _, block := range blocks {
		d, _ := goquery.NewDocumentFromReader(strings.NewReader(block))
		a := d.Find("a[href]").First()
		link := extract(block, rule("链接"))
		if link == "" {
			link, _ = a.Attr("href")
		}
		if link == "" {
			continue
		}
		link = urljoin.Join(base, rule("链接前缀")+link+rule("链接后缀"))
		if seen[link] {
			continue
		}
		name := plain(extract(block, rule("标题")))
		if name == "" {
			name, _ = a.Attr("title")
		}
		if name == "" {
			name, _ = d.Find("img").First().Attr("alt")
		}
		if name == "" {
			name = plain(a.Text())
		}
		if name == "" {
			continue
		}
		pic := extract(block, rule("图片"))
		if pic == "" {
			img := d.Find("img").First()
			for _, attr := range []string{"data-original", "data-src", "src"} {
				pic, _ = img.Attr(attr)
				if pic != "" {
					break
				}
			}
			if pic == "" {
				pic, _ = a.Attr("data-original")
			}
		}
		if pic != "" {
			pic = urljoin.Join(base, html.UnescapeString(pic))
		}
		seen[link] = true
		out.Videos = append(out.Videos, catalog.Video{ID: catalog.ID(link), Name: name, Pic: pic, Remarks: plain(extract(block, rule("副标题")))})
	}
	out.HasNext = len(out.Videos) > 0
	if len(out.Videos) == 0 && strings.Contains(strings.ToLower(body), "captcha") {
		return out, fmt.Errorf("站点要求验证码")
	}
	return out, nil
}

func (s *Session) Detail(ctx context.Context, id string) (catalog.Detail, error) {
	body, base, err := s.fetch(ctx, id)
	if err != nil {
		return catalog.Detail{}, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return catalog.Detail{}, err
	}
	out := catalog.Detail{Name: strings.TrimSpace(doc.Find("h1").First().Text()), Content: plain(extract(body, s.value("简介"))), Year: plain(extract(body, s.value("年代")))}
	if out.Name == "" {
		out.Name = plain(doc.Find("title").Text())
	}
	if pic, ok := doc.Find("meta[property='og:image']").Attr("content"); ok {
		out.Pic = urljoin.Join(base, pic)
	}
	var names []string
	for _, block := range extractAll(body, s.value("线路数组")) {
		name := plain(extract(block, s.value("线路标题")))
		if name == "" {
			name = plain(block)
		}
		names = append(names, name)
	}
	groups := extractAll(body, s.value("播放数组"))
	if s.value("播放数组") == "" {
		doc.Find(".module-play-list,.stui-content__playlist,.myui-content__playlist,.hl-plays-list,.playlist").Each(func(_ int, g *goquery.Selection) { v, _ := g.Html(); groups = append(groups, v) })
	}
	for i, group := range groups {
		source := catalog.Source{Name: fmt.Sprintf("线路%d", i+1)}
		if i < len(names) && names[i] != "" {
			source.Name = names[i]
		}
		blocks := extractAll(group, s.value("播放列表"))
		if s.value("播放列表") == "" {
			d, _ := goquery.NewDocumentFromReader(strings.NewReader(group))
			d.Find("a[href]").Each(func(_ int, a *goquery.Selection) { v, _ := goquery.OuterHtml(a); blocks = append(blocks, v) })
		}
		for _, block := range blocks {
			d, _ := goquery.NewDocumentFromReader(strings.NewReader(block))
			a := d.Find("a[href]").First()
			link := extract(block, s.value("播放链接"))
			if link == "" {
				link, _ = a.Attr("href")
			}
			if link == "" {
				continue
			}
			name := plain(extract(block, s.value("播放标题")))
			if name == "" {
				name = plain(a.Text())
			}
			if name == "" {
				name = plain(block)
			}
			source.Episodes = append(source.Episodes, catalog.Episode{Name: name, ID: urljoin.Join(base, link)})
		}
		if len(source.Episodes) > 0 {
			out.Sources = append(out.Sources, source)
		}
	}
	if len(out.Sources) == 0 {
		return out, fmt.Errorf("站点未返回分集，请检查规则中的播放数组")
	}
	return out, nil
}

func (s *Session) Play(ctx context.Context, _ string, episode catalog.Episode) (catalog.Playback, error) {
	if err := ctx.Err(); err != nil {
		return catalog.Playback{}, err
	}
	h := maps.Clone(s.headers)
	h["Referer"] = s.home
	return catalog.Playback{URL: episode.ID, Headers: h, Parse: true}, nil
}

// Delimiter rules use start&&end, with optional earlier delimiters narrowing
// the match. Alternatives separated by || are tried in order.
func extractAll(body, rule string) []string { return between(body, rule, true) }

func between(body, rule string, keep bool) []string {
	if rule == "" {
		return nil
	}
	for _, alternative := range strings.Split(rule, "||") {
		parts := strings.Split(alternative, "&&")
		if len(parts) < 2 {
			continue
		}
		rest := body
		for _, prefix := range parts[:len(parts)-2] {
			_, after, ok := strings.Cut(rest, prefix)
			if !ok {
				rest = ""
				break
			}
			rest = after
		}
		start, end := parts[len(parts)-2], parts[len(parts)-1]
		var result []string
		for rest != "" {
			after := rest
			if start != "" {
				var ok bool
				_, after, ok = strings.Cut(rest, start)
				if !ok {
					break
				}
			}
			if end == "" {
				result = append(result, after)
				break
			}
			value, tail, ok := strings.Cut(after, end)
			if !ok {
				break
			}
			if keep {
				value = start + value + end
			}
			result = append(result, value)
			rest = tail
		}
		if len(result) > 0 {
			return result
		}
	}
	return nil
}
func extract(body, rule string) string {
	all := between(body, rule, false)
	if len(all) > 0 {
		return html.UnescapeString(all[0])
	}
	return ""
}
func plain(raw string) string {
	if !strings.Contains(raw, "<") {
		return strings.TrimSpace(html.UnescapeString(raw))
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return strings.Join(strings.Fields(doc.Text()), " ")
}
