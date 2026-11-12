// Package proxy is a loopback HTTP proxy that injects request headers.
//
// MyGo's video.OpenURL cannot set headers. Playback therefore opens a URL
// on 127.0.0.1, and this proxy fetches the real address with the Referer
// and User-Agent the user typed. HLS playlists are rewritten so segments
// and keys take the same path.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
	"github.com/egoist/mygo/netproxy"
)

const playlistLimit = 8 << 20

var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

// Proxy serves one local port.
type Proxy struct {
	base string
	srv  *http.Server
	ln   net.Listener

	mu      sync.RWMutex
	headers map[string]string
	client  *http.Client
}

// Start listens on 127.0.0.1 and a free port.
func Start(headers map[string]string) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	transport := netproxy.Transport().Clone()
	transport.DisableCompression = true
	p := &Proxy{
		base:    "http://" + ln.Addr().String(),
		ln:      ln,
		headers: clone(headers),
		client:  &http.Client{Transport: transport},
	}
	p.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("proxy: too many redirects")
		}
		p.apply(req)
		return nil
	}
	p.srv = &http.Server{Handler: p}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// Base is the http://127.0.0.1:port origin.
func (p *Proxy) Base() string { return p.base }

// Wrap returns the local URL that plays target with the current headers.
func (p *Proxy) Wrap(target string) string {
	return p.base + "/u?url=" + url.QueryEscape(target)
}

// SetHeaders replaces the headers injected on later requests.
func (p *Proxy) SetHeaders(h map[string]string) {
	p.mu.Lock()
	p.headers = clone(h)
	p.mu.Unlock()
}

// Header returns one injected header, for tests.
func (p *Proxy) Header(name string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for k, v := range p.headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// Close stops the listener.
func (p *Proxy) Close() error {
	if p.srv == nil {
		return nil
	}
	return p.srv.Close()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/u" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	target := r.URL.Query().Get("url")
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "url must be http or https", http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.apply(req)
	// A rewritten playlist has different byte offsets from its upstream.
	// Fetch it whole; segment and MP4 byte ranges still pass through.
	if rg := r.Header.Get("Range"); rg != "" && !playlist("", u.Path) {
		req.Header.Set("Range", rg)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	base := u
	if resp.Request != nil && resp.Request.URL != nil {
		base = resp.Request.URL
	}

	if r.Method == http.MethodHead {
		copyMediaHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyMediaHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	if playlist(resp.Header.Get("Content-Type"), base.Path) {
		p.writePlaylist(w, resp, base.String())
		return
	}
	// Sniff a short prefix so a playlist without a useful content type
	// still has its segment URLs rewritten.
	buf := make([]byte, 64)
	n, _ := io.ReadFull(resp.Body, buf)
	if n > 0 && bytes.HasPrefix(bytes.TrimSpace(buf[:n]), []byte("#EXTM3U")) {
		if resp.StatusCode == http.StatusPartialContent {
			p.writePlaylist(w, resp, base.String())
			return
		}
		rest, err := io.ReadAll(io.LimitReader(resp.Body, playlistLimit))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		body := append(append([]byte{}, buf[:n]...), rest...)
		p.writeRewritten(w, base.String(), body)
		return
	}
	copyMediaHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if n > 0 {
		_, _ = w.Write(buf[:n])
	}
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) writePlaylist(w http.ResponseWriter, resp *http.Response, playlistURL string) {
	if resp.StatusCode == http.StatusPartialContent {
		// Extensionless playlist endpoints can only be identified after the
		// response. Retry without the player's range before rewriting.
		req := resp.Request.Clone(resp.Request.Context())
		req.Header.Del("Range")
		_ = resp.Body.Close()
		full, err := p.client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer full.Body.Close()
		if full.StatusCode != http.StatusOK {
			http.Error(w, "upstream did not return a complete playlist", http.StatusBadGateway)
			return
		}
		resp = full
		playlistURL = full.Request.URL.String()
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, playlistLimit))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	p.writeRewritten(w, playlistURL, body)
}

func (p *Proxy) writeRewritten(w http.ResponseWriter, playlistURL string, body []byte) {
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("#EXTM3U")) {
		http.Error(w, "upstream returned a page instead of an HLS playlist", http.StatusBadGateway)
		return
	}
	out := rewriteM3U8(string(body), playlistURL, p.Wrap)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, out)
}

func (p *Proxy) apply(req *http.Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for k, v := range p.headers {
		if v == "" || strings.EqualFold(k, "Host") {
			continue
		}
		req.Header.Set(k, v)
	}
}

func playlist(ct, pth string) bool {
	lct := strings.ToLower(ct)
	lp := strings.ToLower(pth)
	return strings.Contains(lct, "mpegurl") || strings.Contains(lct, "m3u8") || strings.HasSuffix(lp, ".m3u8")
}

func copyMediaHeaders(dst, src http.Header) {
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := src.Get(k); v != "" {
			dst.Set(k, v)
		}
	}
}

func rewriteM3U8(body, playlistURL string, wrap func(string) string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trim := strings.TrimRight(line, "\r")
		raw := strings.TrimSpace(trim)
		if raw == "" {
			lines[i] = trim
			continue
		}
		if strings.HasPrefix(raw, "#") {
			lines[i] = uriAttr.ReplaceAllStringFunc(trim, func(m string) string {
				sub := uriAttr.FindStringSubmatch(m)
				if len(sub) < 2 || sub[1] == "" {
					return m
				}
				return `URI="` + wrap(urljoin.Join(playlistURL, sub[1])) + `"`
			})
			continue
		}
		lines[i] = wrap(urljoin.Join(playlistURL, raw))
	}
	return strings.Join(lines, "\n")
}

func clone(h map[string]string) map[string]string {
	if len(h) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}
