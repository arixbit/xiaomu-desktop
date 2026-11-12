package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
	"github.com/egoist/mygo/netproxy"
	"golang.org/x/net/html/charset"
)

const cmsMaxBytes = 16 << 20

// cmsSession speaks the common TVBox type 0 (XML) and type 1 (JSON) APIs.
// Session.mu serializes its requests with Close, as it does for drpy2 calls.
type cmsSession struct {
	api    string
	kind   config.Kind
	client *http.Client
}

// OpenCMS opens a CMS session without fetching its optional category index.
// Search and detail endpoints can work even when that index is unavailable.
func OpenCMS(site config.Site) (*Session, error) {
	if site.Kind != config.KindJSON && site.Kind != config.KindXML {
		return nil, fmt.Errorf("站点不是 CMS 类型：%s", site.Kind)
	}
	u, err := url.Parse(site.API)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("CMS 站点地址无效：%s", site.API)
	}
	c := &cmsSession{api: site.API, kind: site.Kind, client: &http.Client{
		Transport: netproxy.Transport(), Timeout: 25 * time.Second,
	}}
	return &Session{cms: c, kind: site.Kind}, nil
}

func openCMS(ctx context.Context, site config.Site) (*Session, []Category, error) {
	s, err := OpenCMS(site)
	if err != nil {
		return nil, nil, err
	}
	categories, err := s.cms.categories(ctx)
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	return s, categories, nil
}

func (c *cmsSession) categories(ctx context.Context) ([]Category, error) {
	for _, action := range []string{"list", "class", "videolist"} {
		body, requestURL, err := c.request(ctx, action, nil)
		if err != nil {
			return nil, err
		}
		var categories []Category
		if c.kind == config.KindJSON {
			var doc struct {
				Class []Category `json:"class"`
			}
			if err := json.Unmarshal(body, &doc); err != nil {
				return nil, fmt.Errorf("CMS 分类 %s: %w", requestURL, err)
			}
			categories = doc.Class
		} else {
			doc, err := decodeCMSXML(body)
			if err != nil {
				return nil, fmt.Errorf("CMS 分类 %s: %w", requestURL, err)
			}
			for _, class := range doc.Class {
				if class.ID != "" {
					categories = append(categories, Category{ID: ID(class.ID), Name: strings.TrimSpace(class.Name)})
				}
			}
		}
		if len(categories) > 0 {
			return categories, nil
		}
	}
	// Search-only CMS APIs can return a valid list without a class table.
	return nil, nil
}

func (s *Session) cmsPage(ctx context.Context, category, query string, page int) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cms == nil {
		return Page{}, fmt.Errorf("站点已关闭，请重新选择")
	}
	c := s.cms
	params := map[string]string{"pg": strconv.Itoa(max(page, 1))}
	if category != "" {
		params["t"] = category
	}
	if query != "" {
		params["wd"] = query
	}
	action := "detail"
	if c.kind == config.KindXML {
		action = "videolist"
	}
	body, requestURL, err := c.request(ctx, action, params)
	if err != nil {
		return Page{}, err
	}
	var out Page
	if c.kind == config.KindJSON {
		out, err = readPage(string(body), page, nil)
	} else {
		var doc cmsXMLResponse
		doc, err = decodeCMSXML(body)
		if err == nil {
			for _, video := range doc.List.Videos {
				if v := video.asVideo(c.api); v.ID != "" && v.ID != "no_data" {
					out.Videos = append(out.Videos, v)
				}
			}
			out.HasNext = len(out.Videos) > 0
			if count, parseErr := strconv.Atoi(doc.List.PageCount); parseErr == nil && count > 0 {
				out.HasNext = page < count
			}
		}
	}
	if err != nil {
		return Page{}, fmt.Errorf("CMS 影片列表 %s: %w", requestURL, err)
	}
	for i := range out.Videos {
		out.Videos[i].Pic = absoluteCMSURL(c.api, out.Videos[i].Pic)
	}
	return out, nil
}

func (s *Session) cmsDetail(ctx context.Context, id string) (Detail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cms == nil {
		return Detail{}, fmt.Errorf("站点已关闭，请重新选择")
	}
	c := s.cms
	action := "detail"
	if c.kind == config.KindXML {
		action = "videolist"
	}
	body, requestURL, err := c.request(ctx, action, map[string]string{"ids": id})
	if err != nil {
		return Detail{}, err
	}
	var out Detail
	if c.kind == config.KindJSON {
		out, err = readDetail(string(body))
	} else {
		var doc cmsXMLResponse
		doc, err = decodeCMSXML(body)
		if err == nil {
			if len(doc.List.Videos) == 0 {
				err = fmt.Errorf("站点没有返回影片详情")
			} else {
				out = doc.List.Videos[0].asDetail(c.api)
				if len(out.Sources) == 0 {
					err = fmt.Errorf("此影片没有可播放分集，请换一部影片或站点")
				}
			}
		}
	}
	if err != nil {
		return Detail{}, fmt.Errorf("CMS 影片详情 %s: %w", requestURL, err)
	}
	out.Pic = absoluteCMSURL(c.api, out.Pic)
	return out, nil
}

func (s *Session) cmsPlay(ctx context.Context, episode Episode) (Playback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Playback{}, err
	}
	if s.cms == nil {
		return Playback{}, fmt.Errorf("站点已关闭，请重新选择")
	}
	raw := strings.TrimSpace(episode.ID)
	if strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
		raw = urljoin.Join(s.cms.api, raw)
	} else if relative, err := url.Parse(raw); err == nil && relative.Scheme == "" && relative.Host == "" {
		switch strings.ToLower(path.Ext(relative.Path)) {
		case ".mp4", ".m3u8":
			raw = urljoin.Join(s.cms.api, raw)
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Playback{}, fmt.Errorf("此分集没有有效的 http/https 地址")
	}
	return Playback{URL: u.String()}, nil
}

func (c *cmsSession) request(ctx context.Context, action string, params map[string]string) ([]byte, string, error) {
	u, err := url.Parse(c.api)
	if err != nil {
		return nil, c.api, err
	}
	q := u.Query()
	q.Set("ac", action)
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	requestURL := u.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, requestURL, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, requestURL, fmt.Errorf("CMS 请求 %s: %w", requestURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, cmsMaxBytes+1))
	if err != nil {
		return nil, requestURL, fmt.Errorf("CMS 读取 %s: %w", requestURL, err)
	}
	if len(body) > cmsMaxBytes {
		return nil, requestURL, fmt.Errorf("CMS 响应 %s 超过 %d 字节", requestURL, cmsMaxBytes)
	}
	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, requestURL, fmt.Errorf("CMS 请求 %s: HTTP %d (Content-Type %q)", requestURL, resp.StatusCode, contentType)
	}
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(bytes.ToLower(trimmed), []byte("<!doctype html")) || bytes.HasPrefix(bytes.ToLower(trimmed), []byte("<html")) {
		return nil, requestURL, fmt.Errorf("CMS 请求 %s: HTTP %d 返回 HTML (Content-Type %q)", requestURL, resp.StatusCode, contentType)
	}
	return body, requestURL, nil
}

func absoluteCMSURL(base, ref string) string {
	if strings.TrimSpace(ref) == "" {
		return ""
	}
	return urljoin.Join(base, ref)
}

type cmsXMLResponse struct {
	XMLName xml.Name `xml:"rss"`
	Class   []struct {
		ID   string `xml:"id,attr"`
		Name string `xml:",chardata"`
	} `xml:"class>ty"`
	List struct {
		PageCount string        `xml:"pagecount,attr"`
		Videos    []cmsXMLVideo `xml:"video"`
	} `xml:"list"`
}

type cmsXMLVideo struct {
	ID         string `xml:"id"`
	VodID      string `xml:"vod_id"`
	Name       string `xml:"name"`
	VodName    string `xml:"vod_name"`
	Pic        string `xml:"pic"`
	VodPic     string `xml:"vod_pic"`
	Remarks    string `xml:"note"`
	VodRemarks string `xml:"vod_remarks"`
	Year       string `xml:"year"`
	Type       string `xml:"type"`
	Content    string `xml:"des"`
	VodContent string `xml:"vod_content"`
	Episodes   []struct {
		Flag string `xml:"flag,attr"`
		Text string `xml:",chardata"`
	} `xml:"dl>dd"`
}

func decodeCMSXML(body []byte) (cmsXMLResponse, error) {
	var doc cmsXMLResponse
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.CharsetReader = charset.NewReaderLabel
	err := decoder.Decode(&doc)
	return doc, err
}

func (v cmsXMLVideo) asVideo(base string) Video {
	return Video{
		ID:      ID(firstNonempty(v.ID, v.VodID)),
		Name:    firstNonempty(v.Name, v.VodName),
		Pic:     absoluteCMSURL(base, firstNonempty(v.Pic, v.VodPic)),
		Remarks: firstNonempty(v.Remarks, v.VodRemarks),
		Year:    v.Year,
		Type:    v.Type,
		Content: firstNonempty(v.Content, v.VodContent),
	}
}

func (v cmsXMLVideo) asDetail(base string) Detail {
	video := v.asVideo(base)
	out := Detail{Name: video.Name, Pic: video.Pic, Content: video.Content, Year: video.Year, Type: video.Type}
	for _, dd := range v.Episodes {
		source := Source{Name: strings.TrimSpace(dd.Flag)}
		for _, item := range strings.Split(dd.Text, "#") {
			name, id, ok := strings.Cut(strings.TrimSpace(item), "$")
			if !ok {
				id, name = name, fmt.Sprintf("第 %d 集", len(source.Episodes)+1)
			}
			if strings.TrimSpace(id) != "" {
				source.Episodes = append(source.Episodes, Episode{Name: name, ID: id})
			}
		}
		if len(source.Episodes) > 0 {
			out.Sources = append(out.Sources, source)
		}
	}
	return out
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
