// Package douban reads movie and TV discovery metadata. It never supplies
// playback URLs; the application searches the user's library by title.
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

// Kind identifies an independent hot list.
type Kind string

const (
	Movie    Kind = "movie"
	TV       Kind = "tv"
	maxBytes      = 2 << 20
	cacheAge      = 6 * time.Hour
	endpoint      = "https://movie.douban.com/j/search_subjects"
)

// Subject contains only the metadata used by the discovery grid.
type Subject struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Cover    string `json:"cover"`
	Rating   string `json:"rate"`
	Episodes string `json:"episodes_info,omitempty"`
}

// Feed is a cached snapshot of one list; the timestamp is the last success.
type Feed struct {
	Kind     Kind      `json:"kind"`
	Updated  time.Time `json:"updated"`
	Subjects []Subject `json:"subjects"`
}

// Fresh reports whether a feed can be reused without a network request.
func (f Feed) Fresh(now time.Time) bool {
	age := now.Sub(f.Updated)
	return len(f.Subjects) > 0 && age >= 0 && age < cacheAge
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
	return feed
}

// Fetch downloads a fresh list. A failed or empty response never replaces the
// last successful cache. Cache writes are separate so a full disk cannot hide
// a successfully fetched list.
func (c *Client) Fetch(ctx context.Context, kind Kind) (Feed, error) {
	if !validKind(kind) {
		return Feed{}, fmt.Errorf("未知豆瓣榜单：%s", kind)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return Feed{}, err
	}
	q := u.Query()
	q.Set("type", string(kind))
	q.Set("tag", "热门")
	q.Set("sort", "recommend")
	q.Set("page_limit", "24")
	q.Set("page_start", "0")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Feed{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://movie.douban.com/")
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
	var response struct {
		Subjects []Subject `json:"subjects"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Feed{}, fmt.Errorf("无法读取豆瓣榜单：%w", err)
	}
	subjects := cleanSubjects(response.Subjects)
	if len(subjects) == 0 {
		return Feed{}, fmt.Errorf("豆瓣暂未返回影片，请稍后重试")
	}
	return Feed{Kind: kind, Updated: time.Now(), Subjects: subjects}, nil
}

// Save atomically persists a successful list in the app's local cache.
func (c *Client) Save(feed Feed) error {
	if c.CacheDir == "" {
		return nil
	}
	if !validKind(feed.Kind) || feed.Updated.IsZero() || len(cleanSubjects(feed.Subjects)) == 0 {
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
func validKind(kind Kind) bool { return kind == Movie || kind == TV }

func cleanSubjects(subjects []Subject) []Subject {
	out := make([]Subject, 0, min(24, len(subjects)))
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
		out = append(out, subject)
		if len(out) == 24 {
			break
		}
	}
	return out
}
