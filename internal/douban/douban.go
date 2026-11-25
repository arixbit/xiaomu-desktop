// Package douban reads movie, TV, variety, animation, documentary and Top 250
// discovery metadata. It never supplies playback URLs; the application searches
// the user's library by title.
package douban

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/netproxy"
)

// Kind identifies an independent Douban discovery list.
type Kind string

const (
	Hot         Kind = "hot"
	Movie       Kind = "movie"
	TV          Kind = "tv"
	Show        Kind = "show"
	Animation   Kind = "animation"
	ShortDrama  Kind = "short"
	Documentary Kind = "documentary"
	Top250      Kind = "top250"

	PageSize          = 24
	maxSubjects       = 250
	maxBytes          = 2 << 20
	cacheAge          = 6 * time.Hour
	endpointFmt       = "https://m.douban.com/rexxar/api/v2/subject_collection/%s/items"
	recentHotTVURL    = "https://m.douban.com/rexxar/api/v2/subject/recent_hot/tv"
	collectionReferer = "https://m.douban.com/"
	tvExploreReferer  = "https://movie.douban.com/tv/"
)

var (
	allKinds = []Kind{Hot, Movie, TV, Show, Animation, ShortDrama, Documentary, Top250}
	hotKinds = []Kind{Hot, Movie, TV, Show, Animation, ShortDrama, Documentary}
)

// AllKinds returns every supported Douban list in display order.
func AllKinds() []Kind {
	out := make([]Kind, len(allKinds))
	copy(out, allKinds)
	return out
}

// HotKinds returns the current trending lists (excluding classic Top 250).
func HotKinds() []Kind {
	out := make([]Kind, len(hotKinds))
	copy(out, hotKinds)
	return out
}

// Subject contains only the metadata used by the discovery grid.
type Subject struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Cover    string `json:"cover"`
	Rating   string `json:"rate"`
	Episodes string `json:"episodes_info,omitempty"`
	Rank     int    `json:"rank,omitempty"`
}

// Feed is a cached snapshot of one list; the timestamp is the last full refresh.
type Feed struct {
	Kind     Kind      `json:"kind"`
	Updated  time.Time `json:"updated"`
	Subjects []Subject `json:"subjects"`
	Total    int       `json:"total,omitempty"`
	Next     int       `json:"next,omitempty"`
}

// Fresh reports whether a feed can be reused without a network request.
func (f Feed) Fresh(now time.Time) bool {
	age := now.Sub(f.Updated)
	return len(f.Subjects) > 0 && age >= 0 && age < cacheAge
}

// HasMore reports whether the Douban collection has additional pages to load.
func (f Feed) HasMore() bool {
	return f.Next > 0 && f.Total > 0 && f.Next < f.Total && len(f.Subjects) < maxSubjects
}

// MergePage appends a fetched page onto base, deduplicating by subject ID while
// keeping the original refresh timestamp and advancing pagination state safely.
func MergePage(base, page Feed) Feed {
	merged := base
	if merged.Kind == "" {
		merged.Kind = page.Kind
	}
	if merged.Updated.IsZero() {
		merged.Updated = page.Updated
	}
	combined := make([]Subject, 0, len(base.Subjects)+len(page.Subjects))
	combined = append(combined, base.Subjects...)
	combined = append(combined, page.Subjects...)
	merged.Subjects = cleanSubjects(combined)
	if page.Total > 0 {
		merged.Total = page.Total
	}
	if page.Next > base.Next {
		merged.Next = page.Next
	} else {
		// Pagination did not advance; mark exhausted so callers never loop.
		merged.Next = max(base.Next, merged.Total)
	}
	return merged
}

// Client uses the application's proxy settings and an optional disk cache.
// Set HTTP to provide an isolated transport in tests.
type Client struct {
	HTTP     *http.Client
	CacheDir string
}

// New creates a metadata client. An empty cache directory disables persistence.
func New(cacheDir string) *Client {
	return &Client{HTTP: &http.Client{Transport: netproxy.Transport(), Timeout: 10 * time.Second}, CacheDir: cacheDir}
}

// Cached returns the last valid list, including stale lists for offline display.
// Missing or damaged cache entries are a cache miss, never an empty replacement.
func (c *Client) Cached(kind Kind) Feed {
	if c.CacheDir == "" || !validKind(kind) {
		return Feed{}
	}
	f, err := os.Open(c.cachePath(kind))
	if err != nil {
		return Feed{}
	}
	defer f.Close()
	var feed Feed
	if json.NewDecoder(io.LimitReader(f, maxBytes)).Decode(&feed) != nil || feed.Kind != kind || feed.Updated.IsZero() {
		return Feed{}
	}
	feed.Subjects = cleanSubjects(feed.Subjects)
	if len(feed.Subjects) == 0 {
		return Feed{}
	}
	return feed
}

// Fetch downloads the first page of a list. A failed or empty response never
// replaces the last successful cache. Cache writes are separate so a full disk
// cannot hide a successfully fetched list.
func (c *Client) Fetch(ctx context.Context, kind Kind) (Feed, error) {
	return c.FetchPage(ctx, kind, 0)
}

// FetchPage downloads one page of a list starting at the given offset.
func (c *Client) FetchPage(ctx context.Context, kind Kind, start int) (Feed, error) {
	if !validKind(kind) {
		return Feed{}, fmt.Errorf("未知豆瓣榜单：%s", kind)
	}
	if start < 0 {
		start = 0
	}
	reqURL, referer, err := pageRequestSpec(kind, start)
	if err != nil {
		return Feed{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return Feed{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", referer)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Feed{}, fmt.Errorf("连接豆瓣失败：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Feed{}, fmt.Errorf("豆瓣暂时不可用（HTTP %d）", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return Feed{}, fmt.Errorf("读取豆瓣榜单：%w", err)
	}
	if len(body) > maxBytes {
		return Feed{}, fmt.Errorf("豆瓣榜单响应过大")
	}
	if strings.HasPrefix(strings.TrimSpace(string(body)), "<") || strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "html") {
		return Feed{}, fmt.Errorf("豆瓣返回了网页验证，稍后可重试")
	}

	var response collectionResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return Feed{}, fmt.Errorf("无法读取豆瓣榜单：%w", err)
	}
	items := response.Items
	if kind == ShortDrama {
		items = response.RecommendItems
	}
	rawCount := len(items)
	subjects := parseCollectionItems(kind, start, items)
	now := time.Now()
	total := response.Total
	if total < 0 {
		total = 0
	}
	if rawCount == 0 {
		if start == 0 {
			return Feed{}, fmt.Errorf("豆瓣暂未返回影片，请稍后重试")
		}
		return Feed{Kind: kind, Updated: now, Total: total, Next: max(start, total)}, nil
	}
	if start == 0 && len(subjects) == 0 {
		return Feed{}, fmt.Errorf("豆瓣暂未返回影片，请稍后重试")
	}
	return Feed{
		Kind:     kind,
		Updated:  now,
		Subjects: subjects,
		Total:    total,
		Next:     start + rawCount,
	}, nil
}

// Save atomically persists a successful list in the app's local cache.
func (c *Client) Save(feed Feed) error {
	if c.CacheDir == "" {
		return nil
	}
	feed.Subjects = cleanSubjects(feed.Subjects)
	if !validKind(feed.Kind) || feed.Updated.IsZero() || len(feed.Subjects) == 0 {
		return fmt.Errorf("不能缓存空的豆瓣榜单")
	}
	if err := os.MkdirAll(c.CacheDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.CacheDir, ".douban-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(feed); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.cachePath(feed.Kind))
}

func (c *Client) cachePath(kind Kind) string {
	return filepath.Join(c.CacheDir, "douban-hot-"+string(kind)+".json")
}

func collectionID(kind Kind) (string, bool) {
	switch kind {
	case Hot:
		return "subject_real_time_hotest", true
	case Movie:
		return "movie_hot_gaia", true
	case TV:
		return "tv_hot", true
	case Show:
		return "show_hot", true
	case Animation:
		return "tv_animation", true
	case Documentary:
		return "tv_documentary", true
	case Top250:
		return "movie_top250", true
	default:
		return "", false
	}
}

func pageRequestSpec(kind Kind, start int) (rawURL, referer string, err error) {
	if kind == ShortDrama {
		u, err := url.Parse(recentHotTVURL)
		if err != nil {
			return "", "", err
		}
		q := u.Query()
		q.Set("start", strconv.Itoa(start))
		q.Set("limit", strconv.Itoa(PageSize))
		q.Set("category", "tv")
		q.Set("type", "tv_short_drama")
		q.Set("include_short_drama", "true")
		u.RawQuery = q.Encode()
		return u.String(), tvExploreReferer, nil
	}
	colID, ok := collectionID(kind)
	if !ok {
		return "", "", fmt.Errorf("未知豆瓣榜单：%s", kind)
	}
	u, err := url.Parse(fmt.Sprintf(endpointFmt, colID))
	if err != nil {
		return "", "", err
	}
	q := u.Query()
	q.Set("start", strconv.Itoa(start))
	q.Set("count", strconv.Itoa(PageSize))
	u.RawQuery = q.Encode()
	return u.String(), collectionReferer, nil
}

func validKind(kind Kind) bool {
	if kind == ShortDrama {
		return true
	}
	_, ok := collectionID(kind)
	return ok
}

type collectionResponse struct {
	Count          int              `json:"count"`
	Start          int              `json:"start"`
	Total          int              `json:"total"`
	Items          []collectionItem `json:"subject_collection_items"`
	RecommendItems []collectionItem `json:"items"`
}

type collectionItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Type         string `json:"type"`
	EpisodesInfo string `json:"episodes_info"`
	Rank         int    `json:"rank"`
	CoverURL     string `json:"cover_url"`
	Pic          *struct {
		Large  string `json:"large"`
		Normal string `json:"normal"`
	} `json:"pic"`
	Cover *struct {
		URL string `json:"url"`
	} `json:"cover"`
	Rating *struct {
		Value float64 `json:"value"`
	} `json:"rating"`
}

func parseCollectionItems(kind Kind, start int, items []collectionItem) []Subject {
	raw := make([]Subject, 0, len(items))
	for i, item := range items {
		if typ := strings.ToLower(strings.TrimSpace(item.Type)); typ != "" && typ != "movie" && typ != "tv" {
			continue
		}
		var cover string
		switch {
		case item.Pic != nil && strings.TrimSpace(item.Pic.Large) != "":
			cover = item.Pic.Large
		case item.Pic != nil && strings.TrimSpace(item.Pic.Normal) != "":
			cover = item.Pic.Normal
		case item.Cover != nil && strings.TrimSpace(item.Cover.URL) != "":
			cover = item.Cover.URL
		default:
			cover = item.CoverURL
		}
		var rating string
		if item.Rating != nil && item.Rating.Value > 0 && item.Rating.Value <= 10 {
			rating = fmt.Sprintf("%.1f", item.Rating.Value)
		}
		rank := item.Rank
		if rank <= 0 && kind == Top250 {
			rank = start + i + 1
		}
		if rank < 0 {
			rank = 0
		}
		raw = append(raw, Subject{
			ID:       item.ID,
			Title:    item.Title,
			Cover:    strings.TrimSpace(cover),
			Rating:   rating,
			Episodes: strings.TrimSpace(item.EpisodesInfo),
			Rank:     rank,
		})
	}
	return cleanSubjects(raw)
}

func cleanSubjects(subjects []Subject) []Subject {
	out := make([]Subject, 0, min(maxSubjects, len(subjects)))
	seen := map[string]bool{}
	for _, subject := range subjects {
		subject.ID, subject.Title = strings.TrimSpace(subject.ID), strings.TrimSpace(subject.Title)
		if subject.ID == "" || subject.Title == "" || seen[subject.ID] {
			continue
		}
		seen[subject.ID] = true
		u, err := url.Parse(subject.Cover)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			subject.Cover = ""
		}
		score, err := strconv.ParseFloat(subject.Rating, 64)
		if err != nil || !(score > 0 && score <= 10) {
			subject.Rating = ""
		}
		if subject.Rank < 0 {
			subject.Rank = 0
		}
		out = append(out, subject)
		if len(out) == maxSubjects {
			break
		}
	}
	return out
}
