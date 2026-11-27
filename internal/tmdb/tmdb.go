// Package tmdb searches The Movie Database for artwork for local videos.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/httpx"
)

const (
	apiBase         = "https://api.themoviedb.org/3"
	posterBase      = "https://image.tmdb.org/t/p/w500"
	searchTimeout   = 8 * time.Second
	maxSearchBytes  = 1 << 20
	maxSearchResult = 20
)

type Kind string

const (
	Movie Kind = "movie"
	TV    Kind = "tv"
)

type Match struct {
	ID            int
	Kind          Kind
	Title         string
	OriginalTitle string
	Year          string
	PosterURL     string
}

type Client struct {
	token string
	base  string
	http  *http.Client
}

// New takes an opt-in API Read Access Token from the application settings.
func New(token string) *Client {
	client := httpx.NewClient(searchTimeout)
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{token: strings.TrimSpace(token), base: apiBase, http: client}
}

// Search returns the first page of films or series. An empty kind searches
// both so a caller can detect same-name results across the two kinds.
func (c *Client) Search(ctx context.Context, title, year string, kind Kind) ([]Match, error) {
	if c.token == "" {
		return nil, errors.New("请填写 TMDB Read Access Token")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("请输入片名")
	}
	year = strings.TrimSpace(year)
	switch kind {
	case Movie, TV:
		return c.searchKind(ctx, title, year, kind)
	case "":
		var movies, shows []Match
		var movieErr, showErr error
		var wg sync.WaitGroup
		wg.Go(func() { movies, movieErr = c.searchKind(ctx, title, year, Movie) })
		wg.Go(func() { shows, showErr = c.searchKind(ctx, title, year, TV) })
		wg.Wait()
		// A partial result cannot establish that a match is unambiguous.
		if movieErr != nil {
			return nil, movieErr
		}
		if showErr != nil {
			return nil, showErr
		}
		return append(movies, shows...), nil
	default:
		return nil, errors.New("不支持的 TMDB 作品类型")
	}
}

type searchItem struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate   string `json:"release_date"`
	Name          string `json:"name"`
	OriginalName  string `json:"original_name"`
	FirstAirDate  string `json:"first_air_date"`
	PosterPath    string `json:"poster_path"`
}

func (c *Client) searchKind(ctx context.Context, title, year string, kind Kind) ([]Match, error) {
	query := url.Values{"query": {title}, "language": {"zh-CN"}, "include_adult": {"false"}}
	if year != "" {
		yearKey := "year"
		if kind == TV {
			yearKey = "first_air_date_year"
		}
		query.Set(yearKey, year)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/search/"+string(kind)+"?"+query.Encode(), nil)
	if err != nil {
		return nil, errors.New("TMDB 搜索请求无效")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("无法连接 TMDB")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB 搜索失败（HTTP %d）", resp.StatusCode)
	}
	body, exceeded, err := httpx.ReadLimit(resp.Body, resp.ContentLength, maxSearchBytes)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("读取 TMDB 搜索结果失败")
	}
	if exceeded {
		return nil, errors.New("TMDB 搜索结果过大")
	}
	var response struct {
		Results []searchItem `json:"results"`
	}
	if json.Unmarshal(body, &response) != nil || response.Results == nil {
		return nil, errors.New("TMDB 搜索结果格式无效")
	}
	items := response.Results[:min(len(response.Results), maxSearchResult)]
	matches := make([]Match, 0, len(items))
	for _, item := range items {
		name, original, date := item.Title, item.OriginalTitle, item.ReleaseDate
		if kind == TV {
			name, original, date = item.Name, item.OriginalName, item.FirstAirDate
		}
		name, original = strings.TrimSpace(name), strings.TrimSpace(original)
		if name == "" {
			name = original
		}
		if item.ID <= 0 || name == "" {
			continue
		}
		match := Match{ID: item.ID, Kind: kind, Title: name, OriginalTitle: original}
		if parsed, err := time.Parse(time.DateOnly, date); err == nil {
			match.Year = parsed.Format("2006")
		}
		if posterPath.MatchString(item.PosterPath) {
			match.PosterURL = posterBase + item.PosterPath
		}
		matches = append(matches, match)
	}
	return matches, nil
}

// Accept a single image filename from the API, then attach the fixed CDN host.
var posterPath = regexp.MustCompile(`(?i)^/[a-z0-9_-]+\.(jpg|jpeg|png|webp)$`)
