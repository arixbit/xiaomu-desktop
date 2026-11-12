// Package catalog exposes a site's categories, videos and episodes.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/drpy"
)

// ID accepts the string and numeric identifiers used by site scripts.
type ID string

// UnmarshalJSON reads an identifier without rounding numeric values.
func (id *ID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*id = ID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*id = ID(n.String())
	return nil
}

// Category is a site's browse category.
type Category struct {
	ID   ID     `json:"type_id"`
	Name string `json:"type_name"`
}

// Video is one search or category result.
type Video struct {
	ID      ID     `json:"vod_id"`
	Name    string `json:"vod_name"`
	Pic     string `json:"vod_pic"`
	Remarks string `json:"vod_remarks"`
	Year    string `json:"vod_year"`
	Type    string `json:"vod_class"`
	Content string `json:"vod_content"`
}

// Page contains a page of videos and whether another page may exist.
type Page struct {
	Videos  []Video
	HasNext bool
}

// Episode is an item passed to the site's play function.
type Episode struct {
	Name string
	ID   string
}

// Source is a named group of episodes from a video's detail response.
type Source struct {
	Name     string
	Episodes []Episode
}

// Detail describes a video and all its playable groups.
type Detail struct {
	Name    string
	Pic     string
	Content string
	Year    string
	Type    string
	Sources []Source
}

// Playback preserves the site result until the web resolver produces media.
type Playback struct {
	URL     string
	Headers map[string]string
	Parse   bool
	JX      bool
	PlayURL string
}

// Session owns one site's runtime. Calls and Close are serialized.
type Session struct {
	mu     sync.Mutex
	engine *drpy.Engine
	cms    *cmsSession
	kind   config.Kind
	home   Page
}

// Open initializes the chosen site and reads its available categories.
func Open(ctx context.Context, site config.Site, assets string) (*Session, []Category, error) {
	if site.Kind == config.KindXML || site.Kind == config.KindJSON {
		return openCMS(ctx, site)
	}
	if site.Kind != config.KindDRPY2 {
		return nil, nil, fmt.Errorf("暂不支持此站点类型：%s", site.Kind)
	}
	eng, err := drpy.Open(drpy.Options{AssetsDir: assets})
	if err != nil {
		return nil, nil, err
	}
	s := &Session{engine: eng, kind: site.Kind}
	if err := eng.LoadContext(ctx, site.API); err != nil {
		s.Close()
		return nil, nil, err
	}
	if _, err := eng.Call(ctx, "init", site.Ext); err != nil {
		s.Close()
		return nil, nil, fmt.Errorf("初始化站点：%w", err)
	}
	raw, err := eng.Call(ctx, "home", false)
	var home struct {
		Categories []Category `json:"class"`
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &home)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.Close()
		return nil, nil, fmt.Errorf("读取首页：%w", err)
	}
	s.home, _ = readPage(raw, 1, nil) // Some engines include videos with the categories.
	return s, home.Categories, nil
}

// Home returns the site's own recommendations or the CMS latest updates.
func (s *Session) Home(ctx context.Context) (Page, error) {
	if s.kind == config.KindXML || s.kind == config.KindJSON {
		return s.cmsPage(ctx, "", "", 1)
	}
	if len(s.home.Videos) > 0 {
		return s.home, ctx.Err()
	}
	raw, err := s.call(ctx, "homeVod")
	return readPage(raw, 1, err)
}

// Close releases the runtime after any current call finishes.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		_ = s.engine.Close() // The runtime is no longer usable even if cleanup reports an error.
		s.engine = nil
	}
	s.cms = nil
}

func (s *Session) call(ctx context.Context, method string, args ...any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.engine == nil {
		return "", fmt.Errorf("站点已关闭，请重新选择")
	}
	raw, err := s.engine.Call(ctx, method, args...)
	if err == nil {
		err = ctx.Err()
	}
	return raw, err
}

// Category reads a numbered page from a category.
func (s *Session) Category(ctx context.Context, id string, page int) (Page, error) {
	if s.kind == config.KindXML || s.kind == config.KindJSON {
		return s.cmsPage(ctx, id, "", page)
	}
	raw, err := s.call(ctx, "category", id, page, false, map[string]any{})
	return readPage(raw, page, err)
}

// Search searches this site for a title.
func (s *Session) Search(ctx context.Context, query string, page int) (Page, error) {
	if s.kind == config.KindXML || s.kind == config.KindJSON {
		return s.cmsPage(ctx, "", query, page)
	}
	raw, err := s.call(ctx, "search", query, false, page)
	return readPage(raw, page, err)
}

func readPage(raw string, page int, err error) (Page, error) {
	if err != nil {
		return Page{}, err
	}
	var doc struct {
		List      []Video `json:"list"`
		PageCount ID      `json:"pagecount"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Page{}, fmt.Errorf("读取影片列表：%w", err)
	}
	out := Page{}
	for _, video := range doc.List {
		// drpy uses no_data as an empty-list placeholder, not a real video.
		if video.ID != "" && video.ID != "no_data" {
			out.Videos = append(out.Videos, video)
		}
	}
	out.HasNext = len(out.Videos) > 0
	if count, err := strconv.Atoi(string(doc.PageCount)); err == nil && count > 0 {
		out.HasNext = page < count
	}
	return out, nil
}

// Detail reads every playback source and episode for the chosen video.
func (s *Session) Detail(ctx context.Context, id string) (Detail, error) {
	if s.kind == config.KindXML || s.kind == config.KindJSON {
		return s.cmsDetail(ctx, id)
	}
	raw, err := s.call(ctx, "detail", id)
	if err != nil {
		return Detail{}, err
	}
	return readDetail(raw)
}

func readDetail(raw string) (Detail, error) {
	var doc struct {
		List []struct {
			Name    string `json:"vod_name"`
			Pic     string `json:"vod_pic"`
			Content string `json:"vod_content"`
			Year    string `json:"vod_year"`
			Type    string `json:"vod_class"`
			From    string `json:"vod_play_from"`
			URLs    string `json:"vod_play_url"`
		} `json:"list"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Detail{}, fmt.Errorf("读取详情：%w", err)
	}
	if len(doc.List) == 0 {
		return Detail{}, fmt.Errorf("站点没有返回影片详情")
	}
	v := doc.List[0]
	out := Detail{Name: v.Name, Pic: v.Pic, Content: v.Content, Year: v.Year, Type: v.Type}
	names := strings.Split(v.From, "$$$")
	for i, group := range strings.Split(v.URLs, "$$$") {
		source := Source{}
		if i < len(names) {
			source.Name = names[i]
		}
		for _, item := range strings.Split(group, "#") {
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
	if len(out.Sources) == 0 {
		return Detail{}, fmt.Errorf("此影片没有可播放分集，请换一部影片或站点")
	}
	return out, nil
}

// Play reads an episode result; the application resolves pages before playback.
func (s *Session) Play(ctx context.Context, source string, episode Episode) (Playback, error) {
	if s.kind == config.KindXML || s.kind == config.KindJSON {
		return s.cmsPlay(ctx, episode)
	}
	raw, err := s.call(ctx, "play", source, episode.ID, []any{})
	if err != nil {
		return Playback{}, err
	}
	return ReadPlayback(raw)
}

// ReadPlayback reads the common drpy/FongMi player result.
func ReadPlayback(raw string) (Playback, error) {
	var doc struct {
		URL     string            `json:"url"`
		Header  map[string]string `json:"header"`
		Parse   ID                `json:"parse"`
		JX      ID                `json:"jx"`
		PlayURL string            `json:"playUrl"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Playback{}, fmt.Errorf("读取播放地址：%w", err)
	}
	u, err := url.Parse(strings.TrimSpace(doc.URL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Playback{}, fmt.Errorf("站点没有返回可播放的 http/https 地址")
	}
	return Playback{URL: u.String(), Headers: doc.Header,
		Parse: doc.Parse != "" && doc.Parse != "0",
		JX:    doc.JX != "" && doc.JX != "0", PlayURL: doc.PlayURL}, nil
}
