package pan

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
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

// WebSiteURL extracts a plaintext site root URL from a $$$-delimited or JSON ext
// used by open pan web spiders (such as csp_Wogg, csp_Wobg, csp_Wo4k, csp_Panta).
func WebSiteURL(ext string) string {
	ext = strings.TrimSpace(ext)
	if ext == "" {
		return ""
	}
	for _, part := range strings.Split(ext, "$$$") {
		p := strings.TrimSpace(part)
		u, err := url.Parse(p)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		lower := strings.ToLower(u.Path)
		if strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".txt") || strings.HasSuffix(lower, ".js") {
			continue
		}
		return strings.TrimRight(u.String(), "/")
	}
	return ""
}

// WebSiteSession natively scrapes standard MacCMS / Wogg-template pan resource sites
// whose detail pages publish cloud-drive share links, then delegates playback to pan.
type WebSiteSession struct {
	baseURL string
	client  *http.Client
	home    catalog.Page
	cats    []catalog.Category
}

var (
	vodShowHref   = regexp.MustCompile(`(?i)/(?:vodshow|vodtype|show)/([A-Za-z0-9_-]+)`)
	vodDetailHref = regexp.MustCompile(`(?i)/(?:voddetail|detail|vod/detail/id)/([A-Za-z0-9_-]+)`)
)

// OpenWebSite opens a plaintext Wogg-style pan site without Android jars.
func OpenWebSite(ctx context.Context, site config.Site) (*WebSiteSession, []catalog.Category, error) {
	base := WebSiteURL(site.Ext)
	if base == "" {
		return nil, nil, fmt.Errorf("此网盘站点未提供明文网址，需要专用 Android 爬虫")
	}
	jar, _ := cookiejar.New(nil)
	s := &WebSiteSession{
		baseURL: base,
		client:  &http.Client{Transport: netproxy.Transport(), Jar: jar, Timeout: 15 * time.Second},
	}
	doc, finalURL, err := s.fetchDoc(ctx, base+"/")
	if err != nil {
		return nil, nil, err
	}
	s.cats = parseWebCategories(doc)
	if len(s.cats) == 0 {
		s.cats = []catalog.Category{
			{ID: "1", Name: "电影"},
			{ID: "2", Name: "剧集"},
			{ID: "3", Name: "动漫"},
			{ID: "4", Name: "综艺"},
		}
	}
	s.home = parseWebCards(doc, finalURL)
	return s, s.cats, nil
}

func (s *WebSiteSession) fetchDoc(ctx context.Context, target string) (*goquery.Document, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", resolve.UserAgent)
	req.Header.Set("Referer", s.baseURL+"/")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("网盘站点返回 HTTP %d", resp.StatusCode)
	}
	reader, err := charset.NewReader(io.LimitReader(resp.Body, 4<<20), resp.Header.Get("Content-Type"))
	if err != nil {
		reader = io.LimitReader(resp.Body, 4<<20)
	}
	doc, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		return nil, "", err
	}
	final := target
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return doc, final, nil
}

func parseWebCategories(doc *goquery.Document) []catalog.Category {
	var cats []catalog.Category
	seen := map[string]bool{}
	doc.Find("nav a[href], .nav a[href], .navbar a[href], header a[href]").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		name := strings.TrimSpace(a.Text())
		if name == "" || len([]rune(name)) > 12 || name == "首页" {
			return
		}
		m := vodShowHref.FindStringSubmatch(href)
		if len(m) < 2 {
			return
		}
		id := strings.Split(m[1], "-")[0]
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		cats = append(cats, catalog.Category{ID: catalog.ID(id), Name: name})
	})
	return cats
}

func parseWebCards(doc *goquery.Document, base string) catalog.Page {
	var videos []catalog.Video
	seen := map[string]bool{}
	add := func(href, name, pic, remarks string) {
		href = strings.TrimSpace(href)
		name = strings.TrimSpace(name)
		if href == "" || name == "" {
			return
		}
		full := urljoin.Join(base, href)
		if seen[full] {
			return
		}
		seen[full] = true
		if pic != "" {
			pic = urljoin.Join(base, pic)
		}
		videos = append(videos, catalog.Video{
			ID:      catalog.ID(full),
			Name:    name,
			Pic:     pic,
			Remarks: strings.TrimSpace(remarks),
		})
	}

	// Standard Wogg / MacCMS module-item cards.
	doc.Find(".module-item, .module-card-item, .module-search-item, .vodlist_item, .stui-vodlist__box").Each(func(_ int, item *goquery.Selection) {
		link := item.Find("a[href*='detail'], a[href*='voddetail'], a[title]").First()
		if link.Length() == 0 {
			link = item.Find("a[href]").First()
		}
		href, _ := link.Attr("href")
		name := strings.TrimSpace(item.Find(".module-item-title, .module-card-item-title, .video-name, .title, h3").First().Text())
		if name == "" {
			name, _ = link.Attr("title")
		}
		if name == "" {
			name = strings.TrimSpace(link.Text())
		}
		img := item.Find("img").First()
		pic := firstNonEmpty(attr(img, "data-original"), attr(img, "data-src"), attr(img, "src"))
		remarks := strings.TrimSpace(item.Find(".module-item-note, .module-item-text, .pic-text, .video-serial").First().Text())
		add(href, name, pic, remarks)
	})

	// Fallback: anchor tags matching detail URL patterns.
	if len(videos) == 0 {
		doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
			href, _ := a.Attr("href")
			if !vodDetailHref.MatchString(href) {
				return
			}
			name := firstNonEmpty(attr(a, "title"), strings.TrimSpace(a.Text()))
			img := a.Find("img").First()
			pic := firstNonEmpty(attr(img, "data-original"), attr(img, "data-src"), attr(img, "src"))
			add(href, name, pic, "")
		})
	}
	return catalog.Page{Videos: videos, HasNext: len(videos) >= 12}
}

func attr(sel *goquery.Selection, name string) string {
	v, _ := sel.Attr(name)
	return strings.TrimSpace(v)
}

func (s *WebSiteSession) Home(ctx context.Context) (catalog.Page, error) {
	if len(s.home.Videos) > 0 {
		return s.home, ctx.Err()
	}
	doc, finalURL, err := s.fetchDoc(ctx, s.baseURL+"/")
	if err != nil {
		return catalog.Page{}, err
	}
	return parseWebCards(doc, finalURL), nil
}

func (s *WebSiteSession) Category(ctx context.Context, id string, page int) (catalog.Page, error) {
	if page < 1 {
		page = 1
	}
	candidates := []string{
		fmt.Sprintf("%s/index.php/vod/show/id/%s/page/%d.html", s.baseURL, url.PathEscape(id), page),
		fmt.Sprintf("%s/vodshow/%s--------%d---.html", s.baseURL, url.PathEscape(id), page),
	}
	var lastErr error
	for _, target := range candidates {
		doc, finalURL, err := s.fetchDoc(ctx, target)
		if err != nil {
			lastErr = err
			continue
		}
		p := parseWebCards(doc, finalURL)
		if len(p.Videos) > 0 {
			return p, nil
		}
	}
	if lastErr != nil {
		return catalog.Page{}, lastErr
	}
	return catalog.Page{}, nil
}

func (s *WebSiteSession) Search(ctx context.Context, query string, page int) (catalog.Page, error) {
	if page < 1 {
		page = 1
	}
	q := url.PathEscape(strings.TrimSpace(query))
	candidates := []string{
		fmt.Sprintf("%s/index.php/vod/search/page/%d/wd/%s.html", s.baseURL, page, q),
		fmt.Sprintf("%s/vodsearch/%s----------%d---.html", s.baseURL, q, page),
		fmt.Sprintf("%s/index.php/vod/search.html?wd=%s&page=%d", s.baseURL, url.QueryEscape(strings.TrimSpace(query)), page),
	}
	var lastErr error
	for _, target := range candidates {
		doc, finalURL, err := s.fetchDoc(ctx, target)
		if err != nil {
			lastErr = err
			continue
		}
		p := parseWebCards(doc, finalURL)
		if len(p.Videos) > 0 {
			return p, nil
		}
	}
	if lastErr != nil {
		return catalog.Page{}, lastErr
	}
	return catalog.Page{}, nil
}

func (s *WebSiteSession) Detail(ctx context.Context, id string) (catalog.Detail, error) {
	target := urljoin.Join(s.baseURL+"/", id)
	doc, finalURL, err := s.fetchDoc(ctx, target)
	if err != nil {
		return catalog.Detail{}, err
	}
	htmlStr, _ := doc.Html()
	// Also scan anchor hrefs explicitly in case links are split or encoded in attributes.
	var linkTexts strings.Builder
	linkTexts.WriteString(htmlStr)
	doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		if h, ok := a.Attr("href"); ok {
			linkTexts.WriteByte('\n')
			linkTexts.WriteString(h)
			if t := strings.TrimSpace(a.Text()); t != "" {
				linkTexts.WriteByte(' ')
				linkTexts.WriteString(t)
			}
		}
	})
	refs := ExtractShareURLs(linkTexts.String())
	if len(refs) == 0 {
		return catalog.Detail{}, fmt.Errorf("详情页未找到网盘分享链接")
	}
	sources, err := ExpandShares(ctx, refs)
	if err != nil {
		return catalog.Detail{}, err
	}
	name := strings.TrimSpace(doc.Find("h1, .page-title, .video-info-header h1").First().Text())
	img := doc.Find(".module-item-pic img, .video-cover img").First()
	pic := firstNonEmpty(attr(img, "data-original"), attr(img, "data-src"), attr(img, "src"))
	if pic != "" {
		pic = urljoin.Join(finalURL, pic)
	}
	desc := strings.TrimSpace(doc.Find(".video-info-content, .module-info-introduction-content").First().Text())
	return catalog.Detail{
		Name:    name,
		Pic:     pic,
		Content: desc,
		Sources: sources,
	}, nil
}

func (s *WebSiteSession) Play(ctx context.Context, _ string, ep catalog.Episode) (catalog.Playback, error) {
	return ResolveEpisode(ctx, ep.ID)
}

func (s *WebSiteSession) Close() {}
