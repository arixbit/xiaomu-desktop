package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/arixbit/xiaomu-desktop/internal/httpx"
)

// WorkerClient searches Xiaomu's hosted TMDB gateway without any credentials
// in the desktop client. Images are still fetched directly from TMDB's CDN.
type WorkerClient struct {
	endpoint *url.URL
	http     *http.Client
}

// NewWorker takes the deployed service's base URL. HTTP is supported only
// for loopback fixtures.
func NewWorker(baseURL string) (*WorkerClient, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, errors.New("封面服务地址无效")
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && net.ParseIP(base.Hostname()).IsLoopback()) {
		return nil, errors.New("封面服务需要 HTTPS")
	}
	client := httpx.NewClient(searchTimeout)
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &WorkerClient{endpoint: base.JoinPath("v1", "posters"), http: client}, nil
}

type workerMatch struct {
	ID            int    `json:"id"`
	Kind          Kind   `json:"kind"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Year          string `json:"year"`
	PosterURL     string `json:"poster_url"`
}

func (c *WorkerClient) Search(ctx context.Context, title, year string, kind Kind) ([]Match, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("请输入片名")
	}
	if kind != "" && kind != Movie && kind != TV {
		return nil, errors.New("不支持的 TMDB 作品类型")
	}
	query := url.Values{"title": {title}}
	if year = strings.TrimSpace(year); year != "" {
		query.Set("year", year)
	}
	if kind != "" {
		query.Set("kind", string(kind))
	}
	endpoint := *c.endpoint
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("封面搜索请求无效")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Xiaomu-Poster/1.0")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("无法连接封面服务")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		return nil, errors.New("封面查询请求过多，请稍后重试")
	case http.StatusServiceUnavailable:
		return nil, errors.New("封面服务暂不可用，请稍后重试")
	default:
		return nil, fmt.Errorf("封面搜索失败（HTTP %d）", resp.StatusCode)
	}
	body, exceeded, err := httpx.ReadLimit(resp.Body, resp.ContentLength, maxSearchBytes)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("读取封面搜索结果失败")
	}
	if exceeded {
		return nil, errors.New("封面搜索结果过大")
	}
	var result struct {
		Matches []workerMatch `json:"matches"`
	}
	if json.Unmarshal(body, &result) != nil || result.Matches == nil || len(result.Matches) > 2*maxSearchResult {
		return nil, errors.New("封面搜索结果格式无效")
	}
	matches := make([]Match, 0, len(result.Matches))
	for _, item := range result.Matches {
		name, original := strings.TrimSpace(item.Title), strings.TrimSpace(item.OriginalTitle)
		if item.ID <= 0 || name == "" || (item.Kind != Movie && item.Kind != TV) || (kind != "" && item.Kind != kind) {
			return nil, errors.New("封面搜索结果格式无效")
		}
		match := Match{ID: item.ID, Kind: item.Kind, Title: name, OriginalTitle: original, Year: item.Year}
		if path, ok := strings.CutPrefix(item.PosterURL, posterBase); ok && posterPath.MatchString(path) {
			match.PosterURL = posterBase + path
		}
		matches = append(matches, match)
	}
	return matches, nil
}
