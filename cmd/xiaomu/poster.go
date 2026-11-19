package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/netproxy"
	"github.com/egoist/mygo/ui"
)

const (
	maxPosterBytes       = 6 << 20
	posterWorkers        = 4
	posterQueueLimit     = 32
	posterPixelLimit     = 2048 * 2048
	posterDimensionLimit = 4096
	posterCacheBytes     = 64 << 20
	posterFailureLimit   = 256
	posterFailureTTL     = 30 * time.Second
)

var posterClient = &http.Client{Transport: netproxy.Transport(), Timeout: 10 * time.Second}

// Cache metadata is owned by the UI. Four workers hold at most one decoded
// result each until the UI accepts it; the queue holds only URL strings.
type posterResource struct {
	image    ui.ImageSource
	retained int64 // encoded source plus RGBA and room for all mip levels
}

type posterCache struct {
	costs    map[string]int64
	retained int64
	images   map[string]ui.ImageSource
	pending  map[string]bool
	failed   map[string]time.Time
	order    []string
	bytes    int64
	jobs     chan string
	cancel   context.CancelFunc
}

func (p *posterCache) expire(now time.Time) {
	for key, at := range p.failed {
		if now.Sub(at) >= posterFailureTTL {
			delete(p.failed, key)
		}
	}
}

func (p *posterCache) close() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.images = nil
	p.pending = nil
	p.failed = nil
	p.order = nil
	p.bytes = 0
	p.costs = nil
	p.retained = 0
}

func posterDecodedBytes(img ui.ImageSource) int64 {
	w, h := ui.ImageSize(img)
	// DecodeBitmap stores tightly packed premultiplied RGBA, even for JPEG.
	return int64(w) * int64(h) * 4
}

func posterRetainedBytes(img ui.ImageSource, encoded int) int64 {
	w, h := ui.ImageSize(img)
	width, height := int64(w), int64(h)
	bytes := int64(encoded)
	// MyGo rounds each halved dimension up and never below one. A flat
	// 4/3 multiplier undercounts odd-sized and especially narrow images.
	for width > 0 && height > 0 {
		bytes += width * height * 4
		if width == 1 && height == 1 {
			break
		}
		width, height = (width+1)/2, (height+1)/2
	}
	return bytes
}

func (a *app) startPosters() {
	p := &a.posters
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.jobs = make(chan string, posterQueueLimit)
	p.pending = make(map[string]bool)
	dispatch := a.dispatch
	for range posterWorkers {
		go func() {
			for {
				var raw string
				select {
				case <-ctx.Done():
					return
				case raw = <-p.jobs:
				}
				if ctx.Err() != nil {
					return
				}
				resource, err := fetchPosterResource(ctx, raw)
				if ctx.Err() != nil {
					return
				}
				accepted := make(chan struct{})
				dispatch(func() {
					defer close(accepted)
					delete(p.pending, raw)
					if a.closed || ctx.Err() != nil {
						return
					}
					p.expire(time.Now())
					if err != nil {
						if p.failed == nil {
							p.failed = make(map[string]time.Time)
						}
						if len(p.failed) >= posterFailureLimit {
							oldest := ""
							var at time.Time
							for key, when := range p.failed {
								if at.IsZero() || when.Before(at) {
									oldest, at = key, when
								}
							}
							delete(p.failed, oldest)
						}
						p.failed[raw] = time.Now()
						return
					}
					img := resource.image
					size := posterDecodedBytes(img)
					cost := resource.retained
					if cost > posterCacheBytes {
						return
					}
					if p.images == nil {
						p.images = make(map[string]ui.ImageSource)
						p.costs = make(map[string]int64)
					}
					for (p.retained+cost > posterCacheBytes || len(p.order) >= 128) && len(p.order) > 0 {
						key := p.order[0]
						p.order = p.order[1:]
						p.bytes -= posterDecodedBytes(p.images[key])
						delete(p.images, key)
						p.retained -= p.costs[key]
						delete(p.costs, key)
					}
					p.images[raw] = img
					p.order = append(p.order, raw)
					p.bytes += size
					p.costs[raw] = cost
					p.retained += cost
				})
				select {
				case <-accepted:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	// Failure TTL is real cleanup even when the user stops scrolling.
	go func() {
		ticker := time.NewTicker(posterFailureTTL)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				accepted := make(chan struct{})
				dispatch(func() {
					defer close(accepted)
					if !a.closed {
						p.expire(time.Now())
					}
				})
				select {
				case <-accepted:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

func (a *app) poster(raw string) ui.ImageSource {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	p := &a.posters
	if img := p.images[raw]; img != nil {
		// Keep repeatedly visible posters ahead of cold downloads. Otherwise
		// scrolling can evict and decode the image that is still on screen.
		for i, key := range p.order {
			if key == raw {
				copy(p.order[i:], p.order[i+1:])
				p.order[len(p.order)-1] = raw
				break
			}
		}
		return img
	}
	p.expire(time.Now())
	if p.pending[raw] || !p.failed[raw].IsZero() || a.dispatch == nil || a.closed {
		return nil
	}
	if p.cancel == nil {
		a.startPosters()
	}
	select {
	case p.jobs <- raw:
		p.pending[raw] = true
	default: // A completion redraw retries visible URLs; never block the UI.
	}
	return nil
}

func fetchPoster(ctx context.Context, raw string) (ui.ImageSource, error) {
	resource, err := fetchPosterResource(ctx, raw)
	if err != nil {
		return nil, err
	}
	return resource.image, nil
}

func fetchPosterResource(ctx context.Context, raw string) (*posterResource, error) {
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

func readPoster(req *http.Request) (*posterResource, error) {
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
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// Preserve vector posters without allocating a pixel buffer. ParseSVG is
		// bounded by the same encoded-body limit; its viewport must fit too.
		svg, svgErr := ui.ParseSVG(data)
		if svgErr != nil {
			return nil, err
		}
		w, h := ui.ImageSize(svg)
		if w <= 0 || h <= 0 || w > posterDimensionLimit || h > posterDimensionLimit || float64(w)*float64(h) > posterPixelLimit {
			return nil, fmt.Errorf("海报尺寸超出预算")
		}
		return &posterResource{image: svg, retained: posterRetainedBytes(svg, len(data))}, nil
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > posterDimensionLimit || cfg.Height > posterDimensionLimit || cfg.Width > posterPixelLimit/cfg.Height {
		return nil, fmt.Errorf("海报尺寸 %d×%d 超出预算", cfg.Width, cfg.Height)
	}
	img, err := ui.DecodeImage(data)
	if req.Context().Err() != nil {
		return nil, req.Context().Err()
	}
	if err != nil {
		return nil, err
	}
	return &posterResource{image: img, retained: posterRetainedBytes(img, len(data))}, nil
}
