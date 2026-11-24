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
	"sync"
	"time"

	"github.com/egoist/mygo/netproxy"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/netimage"
)

const (
	maxPosterBytes       = 6 << 20
	posterMaxSide        = 640
	posterPixelLimit     = 8 << 20
	posterDimensionLimit = 4096
	posterTimeout        = 10 * time.Second
)

var (
	posterClient     = &http.Client{Transport: netproxy.Transport(), Timeout: posterTimeout}
	posterClientOnce sync.Once
	posterNetClient  = &http.Client{Transport: posterRoundTripper{}}
)

func initPosterNetImage() {
	posterClientOnce.Do(func() {
		netimage.SetClient(posterNetClient)
	})
}

func (a *app) poster(c *ui.Context, raw string) ui.ImageSource {
	raw = strings.TrimSpace(raw)
	if raw == "" || a.closed {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	initPosterNetImage()
	src, _ := netimage.Load(c, raw, netimage.Options{MaxSide: posterMaxSide})
	return src
}

// posterRoundTripper adapts MyGo's ui/netimage downloader with Douban referer
// scoping, bounded single-alternate-host retry for verification pages, and
// Xiaomu's byte/dimension budgets.
type posterRoundTripper struct{}

func (posterRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > posterTimeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, posterTimeout)
		defer cancel()
	}
	cloned := req.Clone(ctx)
	host := strings.ToLower(cloned.URL.Hostname())
	// Douban's image CDN checks the referring page. Only apply these headers
	// to its own hosts, leaving user-provided site poster requests unchanged.
	if isDoubanImageHost(host) {
		cloned.Header.Set("User-Agent", "Mozilla/5.0")
		cloned.Header.Set("Referer", "https://movie.douban.com/")
	}
	resp, err := fetchValidatedPosterResponse(cloned)
	if err == nil || ctx.Err() != nil {
		return resp, err
	}
	altHost, ok := doubanAlternateHost(cloned.URL, host)
	if !ok {
		return nil, err
	}
	retry := cloned.Clone(ctx)
	retry.URL.Host = altHost
	retry.Host = altHost
	return fetchValidatedPosterResponse(retry)
}

func isDoubanImageHost(host string) bool {
	return host == "doubanio.com" || strings.HasSuffix(host, ".doubanio.com") ||
		host == "douban.com" || strings.HasSuffix(host, ".douban.com")
}

// Douban serves the same public poster path on these image hosts. Some hosts
// return an HTML verification page, so try one alternate within the same
// deadline and through the user's existing proxy settings.
func doubanAlternateHost(u *url.URL, host string) (string, bool) {
	if u == nil || u.Port() != "" || !strings.HasPrefix(u.Path, "/view/photo/") {
		return "", false
	}
	switch host {
	case "img1.doubanio.com":
		return "img3.doubanio.com", true
	case "img2.doubanio.com", "img3.doubanio.com", "img9.doubanio.com":
		return "img1.doubanio.com", true
	default:
		return "", false
	}
}

func fetchValidatedPosterResponse(req *http.Request) (*http.Response, error) {
	resp, err := posterClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotModified {
		return resp, nil
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
	if err := validatePosterBytes(data); err != nil {
		return nil, err
	}
	out := *resp
	out.Body = io.NopCloser(bytes.NewReader(data))
	out.ContentLength = int64(len(data))
	return &out, nil
}

func validatePosterBytes(data []byte) error {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		svg, svgErr := ui.ParseSVG(data)
		if svgErr != nil {
			return err
		}
		w, h := ui.ImageSize(svg)
		if w <= 0 || h <= 0 || w > posterDimensionLimit || h > posterDimensionLimit || float64(w)*float64(h) > posterPixelLimit {
			return fmt.Errorf("海报尺寸超出预算")
		}
		return nil
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > posterDimensionLimit || cfg.Height > posterDimensionLimit || cfg.Width > posterPixelLimit/cfg.Height {
		return fmt.Errorf("海报尺寸 %d×%d 超出预算", cfg.Width, cfg.Height)
	}
	return nil
}

// fetchPoster downloads and decodes one poster using the same transport and
// budgets as ui/netimage (used directly by unit tests).
func fetchPoster(ctx context.Context, raw string) (ui.ImageSource, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := posterNetClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if img, err := ui.DecodeBitmapScaled(data, posterMaxSide); err == nil {
		return img, nil
	}
	return ui.ParseSVG(data)
}
