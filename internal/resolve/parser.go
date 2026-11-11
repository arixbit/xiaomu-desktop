package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func (r Resolver) parse(ctx context.Context, client *http.Client, parser config.Parser, play catalog.Playback) (catalog.Playback, error) {
	target := parser.URL + play.URL
	if strings.Contains(parser.URL, "{url}") {
		target = strings.ReplaceAll(parser.URL, "{url}", play.URL)
	}
	h := maps.Clone(play.Headers)
	if h == nil {
		h = map[string]string{}
	}
	var ext struct{ Header map[string]string }
	if json.Unmarshal(parser.Ext, &ext) == nil {
		maps.Copy(h, ext.Header)
	}
	if parser.Type == 0 {
		page := catalog.Playback{URL: target, Headers: h, Parse: true}
		result, err := inspect(ctx, client, page, map[string]bool{}, 0)
		if err == nil || r.Sniff == nil {
			return result, err
		}
		return r.Sniff(ctx, page)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return catalog.Playback{}, err
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return catalog.Playback{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return catalog.Playback{}, fmt.Errorf("解析接口 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, pageLimit))
	if err != nil {
		return catalog.Playback{}, err
	}
	var envelope struct {
		URL  string
		Data json.RawMessage
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return catalog.Playback{}, fmt.Errorf("解析接口未返回 JSON")
	}
	if envelope.URL == "" && len(envelope.Data) > 0 {
		body = envelope.Data
	}
	result, err := catalog.ReadPlayback(string(body))
	if err != nil {
		return catalog.Playback{}, err
	}
	if result.Headers == nil {
		result.Headers = map[string]string{}
	}
	if header(result.Headers, "User-Agent") == "" {
		result.Headers["User-Agent"] = UserAgent
	}
	if Direct(result.URL) {
		return result, nil
	}
	return inspect(ctx, client, result, map[string]bool{}, 0)
}
