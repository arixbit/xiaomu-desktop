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

	"github.com/arixbit/xiaomu-desktop/internal/httpx"
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
	posterClient     = httpx.NewClient(posterTimeout)
	posterClientOnce sync.Once
	posterNetClient  = &http.Client{Transport: posterRoundTripper{}, Timeout: posterTimeout}
)

func init() {
	// Go's default redirect handling strips Authorization across hosts but
	// copies custom headers, so a media server poster that redirects to
	// another address would carry the Emby token with it. When a media
	// poster request names its authorizing server (in the request
	// context), credentials follow only while the target still matches
	// that server, path prefix included. Other posters get the plain
	// cross-origin strip.
	posterClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("海报重定向次数过多")
		}
		if srv, ok := req.Context().Value(posterAuthKey{}).(posterServer); ok {
			if !srv.matches(req.URL) {
				stripPosterCredentials(req)
			}
			return nil
		}
		if len(via) > 0 && via[0].URL != nil && !posterSameOrigin(via[0].URL, req.URL) {
			stripPosterCredentials(req)
		}
		return nil
	}
}

type posterAuthKey struct{}

func stripPosterCredentials(req *http.Request) {
	req.Header.Del("Authorization")
	req.Header.Del("X-Emby-Authorization")
	req.Header.Del("X-Emby-Token")
	req.Header.Del("Cookie")
}

func posterSameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

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
	} else {
		if headers, srv, ok := mediaPosterAuth(cloned.URL); ok {
			for key, value := range headers {
				if value != "" {
					cloned.Header.Set(key, value)
				}
			}
			cloned = cloned.WithContext(context.WithValue(cloned.Context(), posterAuthKey{}, srv))
		}
		stripMediaPosterQuery(cloned.URL)
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
	defer httpx.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("海报 HTTP %d", resp.StatusCode)
	}
	data, exceeded, err := httpx.ReadLimit(resp.Body, resp.ContentLength, maxPosterBytes)
	if err != nil {
		return nil, err
	}
	if exceeded {
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
	defer httpx.DrainAndClose(resp.Body)
	data, exceeded, err := httpx.ReadLimit(resp.Body, resp.ContentLength, maxPosterBytes)
	if err != nil {
		return nil, err
	}
	if exceeded {
		return nil, fmt.Errorf("海报超过 %d 字节", maxPosterBytes)
	}
	if img, err := ui.DecodeBitmapScaled(data, posterMaxSide); err == nil {
		return img, nil
	}
	return ui.ParseSVG(data)
}
