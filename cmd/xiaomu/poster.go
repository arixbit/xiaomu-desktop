package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/netproxy"
	"github.com/egoist/mygo/ui"
)

const maxPosterBytes = 6 << 20

var posterClient = &http.Client{Transport: netproxy.Transport(), Timeout: 10 * time.Second}

// posterCache is accessed only from the UI thread. Network and image decoding
// happen in a goroutine; its result returns through app.dispatch.
type posterCache struct {
	images  map[string]ui.ImageSource
	pending map[string]bool
	failed  map[string]time.Time
	order   []string
}

func (a *app) poster(raw string) ui.ImageSource {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	p := &a.posters
	if image := p.images[raw]; image != nil {
		return image
	}
	if p.pending[raw] || time.Since(p.failed[raw]) < 30*time.Second || a.dispatch == nil || a.closed {
		return nil
	}
	if p.pending == nil {
		p.pending = make(map[string]bool)
	}
	p.pending[raw] = true
	dispatch := a.dispatch
	go func() {
		image, err := fetchPoster(context.Background(), raw)
		dispatch(func() {
			delete(p.pending, raw)
			if a.closed {
				return
			}
			if err != nil {
				if p.failed == nil {
					p.failed = make(map[string]time.Time)
				}
				p.failed[raw] = time.Now()
				return
			}
			if p.images == nil {
				p.images = make(map[string]ui.ImageSource)
			}
			// A bounded cache keeps long scrolling sessions from retaining every
			// poster decoded by the site.
			if len(p.order) >= 128 {
				delete(p.images, p.order[0])
				p.order = p.order[1:]
			}
			p.images[raw] = image
			p.order = append(p.order, raw)
		})
	}()
	return nil
}

func fetchPoster(ctx context.Context, raw string) (ui.ImageSource, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	// Douban's image CDN checks the referring page. Only apply these headers
	// to its own hosts, leaving user-provided site poster requests unchanged.
	host := strings.ToLower(req.URL.Hostname())
	if host == "doubanio.com" || strings.HasSuffix(host, ".doubanio.com") || host == "douban.com" || strings.HasSuffix(host, ".douban.com") {
		req.Header.Set("User-Agent", "Mozilla/5.0")
		req.Header.Set("Referer", "https://movie.douban.com/")
	}
	image, err := readPoster(req)
	if err == nil || ctx.Err() != nil || req.URL.Port() != "" || !strings.HasPrefix(req.URL.Path, "/view/photo/") {
		return image, err
	}
	// Douban serves the same public poster path on these image hosts. Some
	// hosts return a verification page, so try one alternate within the same
	// deadline and through the user's existing proxy settings.
	switch host {
	case "img1.doubanio.com":
		host = "img3.doubanio.com"
	case "img2.doubanio.com", "img3.doubanio.com", "img9.doubanio.com":
		host = "img1.doubanio.com"
	default:
		return image, err
	}
	retry := req.Clone(ctx)
	retry.URL.Host = host
	retry.Host = host
	return readPoster(retry)
}

func readPoster(req *http.Request) (ui.ImageSource, error) {
	resp, err := posterClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("海报 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPosterBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPosterBytes {
		return nil, fmt.Errorf("海报超过 %d 字节", maxPosterBytes)
	}
	return ui.DecodeImage(data)
}
