package danmaku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/netproxy"
)

const maxResponseBytes = 16 << 20

// Episode is a selectable search result from a Dandanplay-compatible service.
type Episode struct {
	ID      string
	AnimeID string
	Anime   string
	Name    string
}

// Client uses only the service address supplied by the user.
type Client struct {
	base *url.URL
	HTTP *http.Client
}

// NewClient accepts a service root or its /api/v2 address, including a path prefix.
func NewClient(address string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("请输入 http 或 https 弹幕服务地址")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/api/v2") {
		u = u.JoinPath("api", "v2")
	}
	return &Client{base: u, HTTP: &http.Client{Transport: netproxy.Transport(), Timeout: 20 * time.Second}}, nil
}

// Search finds episodes by title and an optional episode number. IDs may be
// numbers or strings across services.
func (c *Client) Search(ctx context.Context, title, episode string) ([]Episode, error) {
	u := c.base.JoinPath("search", "episodes")
	query := u.Query()
	query.Set("anime", strings.TrimSpace(title))
	query.Del("episode")
	if episode = strings.TrimSpace(episode); episode != "" {
		query.Set("episode", episode)
	}
	u.RawQuery = query.Encode()
	data, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var response struct {
		ErrorCode int `json:"errorCode"`
		Animes    *[]struct {
			ID       json.RawMessage `json:"animeId"`
			Title    string          `json:"animeTitle"`
			Episodes []struct {
				ID    json.RawMessage `json:"episodeId"`
				Title string          `json:"episodeTitle"`
			} `json:"episodes"`
		} `json:"animes"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("弹幕搜索响应不是有效 JSON")
	}
	if response.ErrorCode != 0 || response.Animes == nil {
		return nil, fmt.Errorf("弹幕服务没有返回有效的搜索结果")
	}
	var episodes []Episode
	for _, anime := range *response.Animes {
		for _, episode := range anime.Episodes {
			id := stringID(episode.ID)
			if !validID(id) {
				continue
			}
			episodes = append(episodes, Episode{ID: id, AnimeID: stringID(anime.ID), Anime: anime.Title, Name: episode.Title})
			if len(episodes) == 3000 {
				return episodes, nil
			}
		}
	}
	return episodes, nil
}

// Comments downloads all timed comments for an episode.
func (c *Client) Comments(ctx context.Context, id string) ([]Comment, error) {
	if !validID(id) {
		return nil, fmt.Errorf("弹幕分集 ID 无效")
	}
	u := c.base.JoinPath("comment", id)
	query := u.Query()
	query.Set("format", "json")
	u.RawQuery = query.Encode()
	data, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func (c *Client) get(ctx context.Context, u *url.URL) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("弹幕请求地址无效")
	}
	req.Header.Set("User-Agent", "xiaomu")
	req.Header.Set("Accept", "application/json, application/xml;q=0.9, text/xml;q=0.8")
	response, err := c.HTTP.Do(req)
	if err != nil {
		// A URL can contain a private service token. Keep it out of UI errors.
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return nil, fmt.Errorf("弹幕请求失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("弹幕服务返回 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取弹幕响应失败：%w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("弹幕响应超过 16 MB")
	}
	return data, nil
}

func stringID(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return string(number)
	}
	return ""
}

func validID(id string) bool {
	return id != "" && id != "null" && id != "." && id != ".." && len(id) <= 200 && !strings.ContainsAny(id, "/\\%?#\x00\r\n")
}
