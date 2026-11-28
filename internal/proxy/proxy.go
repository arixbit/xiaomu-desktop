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
	"sync/atomic"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/httpx"
	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
)

const (
	playlistLimit       = 8 << 20
	upstreamIdleTimeout = 30 * time.Second
	previewIdleTimeout  = 6 * time.Second
	playlistCacheTTL    = 2 * time.Minute
	playlistCacheCap    = 16
)

type cachedPlaylist struct {
	body    string
	expires time.Time
}

var errUpstreamTimeout = errors.New("媒体服务器超过 30 秒未响应")

var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

// Proxy serves one local port.
type Proxy struct {
	base string
	srv  *http.Server
	ln   net.Listener

	mu          sync.RWMutex
	headers     map[string]string
	boundOrigin string
	sessions    map[string]*proxySession
	playlists   map[string]cachedPlaylist
	client      *http.Client
	readTimeout time.Duration

	// served counts the media bytes written to players. The proxy
	// only ever serves playback, so this is the video's throughput;
	// counting costs nothing in the video path.
	served atomic.Int64
}

// countingWriter passes every body byte through to the player and
// counts it. Headers are written by the server, not through here,
// so only media bytes count.
type countingWriter struct {
	http.ResponseWriter
	served *atomic.Int64
}

func (w countingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.served.Add(int64(n))
	return n, err
}

func (w countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// BytesServed is how many media bytes this proxy has handed to players.
func (p *Proxy) BytesServed() int64 { return p.served.Load() }

// Start listens on 127.0.0.1 and a free port.
func Start(headers map[string]string) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{
		base:      "http://" + ln.Addr().String(),
		ln:        ln,
		headers:   clone(headers),
		sessions:  map[string]*proxySession{},
		playlists: map[string]cachedPlaylist{},
		client:    httpx.NewStreamClient(0, true),
	}
	p.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("proxy: too many redirects")
		}
		if hdr, ok := req.Context().Value(sessionHeadersKey{}).(boundHeaders); ok {
			applyMap(req, hdr)
		} else {
			p.apply(req)
		}
		// The injected headers belong to the original target. When a
		// redirect leaves that origin, credential headers must not follow
		// (a media server stream can redirect to an unrelated address);
		// Referer/User-Agent stay, as plain playback sources rely on them.
		if len(via) > 0 && via[0].URL != nil && !sameOrigin(via[0].URL, req.URL) {
			req.Header.Del("Authorization")
			req.Header.Del("X-Emby-Authorization")
			req.Header.Del("X-Emby-Token")
			req.Header.Del("Cookie")
		}
		return nil
	}
	p.srv = &http.Server{Handler: p}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// Base is the http://127.0.0.1:port origin.
func (p *Proxy) Base() string { return p.base }

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// Wrap returns the local URL that plays target with the current headers.
func (p *Proxy) Wrap(target string) string {
	return p.base + "/u?url=" + url.QueryEscape(target)
}

func (p *Proxy) wrapWithSID(sid, target string) string {
	if sid == "" {
		return p.Wrap(target)
	}
	return p.base + "/u?sid=" + url.QueryEscape(sid) + "&url=" + url.QueryEscape(target)
}

// RegisterWrapSession mounts an isolated header set for /u?sid=<id>&url=...
// so replacing an active playback stream can reuse the same Proxy and upstream
// connection pool without mutating the current player's headers prematurely.
func (p *Proxy) RegisterWrapSession(target string, headers map[string]string) (id, localURL string) {
	id, _ = p.RegisterSession(SessionConfig{
		URL:     target,
		Headers: headers,
	})
	return id, p.wrapWithSID(id, target)
}

// SetHeaders replaces the headers injected on later requests. The set is
// unbound: credential headers in it are withheld until SetHeadersFor
// ties the set to a target origin.
func (p *Proxy) SetHeaders(h map[string]string) {
	p.mu.Lock()
	p.headers = clone(h)
	p.boundOrigin = ""
	p.invalidatePlaylistsLocked("u:|")
	p.mu.Unlock()
}

// SetHeadersFor replaces the injected headers and binds them to target's
// origin: credential headers are only sent to that origin, on every
// request including rewritten HLS segments and keys.
func (p *Proxy) SetHeadersFor(target string, h map[string]string) {
	p.mu.Lock()
	p.headers = clone(h)
	p.boundOrigin = originOf(target)
	p.invalidatePlaylistsLocked("u:|")
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

// Close stops the listener and releases upstream connections from this playback.
func (p *Proxy) Close() error {
	p.mu.Lock()
	p.playlists = nil
	p.mu.Unlock()
	if p.srv == nil {
		return nil
	}
	err := p.srv.Close()
	p.client.CloseIdleConnections()
	return err
}

func isPreviewRequest(r *http.Request) bool {
	return r != nil && r.URL != nil && r.URL.Query().Get("preview") == "1"
}

func isCacheablePlaylist(raw []byte) bool {
	return bytes.Contains(raw, []byte("#EXT-X-ENDLIST")) || bytes.Contains(raw, []byte("#EXT-X-STREAM-INF:"))
}

func (p *Proxy) cachedPlaylist(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	now := time.Now()
	p.mu.RLock()
	entry, ok := p.playlists[key]
	p.mu.RUnlock()
	if !ok || now.After(entry.expires) {
		return "", false
	}
	return entry.body, true
}

func (p *Proxy) storePlaylist(key, rewritten string, raw []byte) {
	if key == "" || !isCacheablePlaylist(raw) {
		return
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playlists == nil {
		p.playlists = map[string]cachedPlaylist{}
	}
	for k, v := range p.playlists {
		if now.After(v.expires) {
			delete(p.playlists, k)
		}
	}
	if _, exists := p.playlists[key]; !exists && len(p.playlists) >= playlistCacheCap {
		var oldestKey string
		var oldestExp time.Time
		for k, v := range p.playlists {
			if oldestKey == "" || v.expires.Before(oldestExp) {
				oldestKey, oldestExp = k, v.expires
			}
		}
		if oldestKey != "" {
			delete(p.playlists, oldestKey)
		}
	}
	p.playlists[key] = cachedPlaylist{
		body:    rewritten,
		expires: now.Add(playlistCacheTTL),
	}
}

func (p *Proxy) invalidatePlaylistsLocked(prefix string) {
	for k := range p.playlists {
		if strings.HasPrefix(k, prefix) {
			delete(p.playlists, k)
		}
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w = countingWriter{ResponseWriter: w, served: &p.served}
	if strings.HasPrefix(r.URL.Path, "/s/") {
		p.serveSession(w, r)
		return
	}
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
	reqCtx := r.Context()
	sid := r.URL.Query().Get("sid")
	var sessHeaders boundHeaders
	if sid != "" {
		sess, found := p.session(sid)
		if !found {
			http.NotFound(w, r)
			return
		}
		_, sessHeaders = sess.snapshot()
		reqCtx = context.WithValue(reqCtx, sessionHeadersKey{}, sessHeaders)
	}
	cacheKey := "u:" + sid + "|" + target
	if r.Method == http.MethodGet {
		if cached, ok := p.cachedPlaylist(cacheKey); ok {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, cached)
			return
		}
	}
	reqLimit := time.Duration(0)
	if isPreviewRequest(r) {
		reqLimit = previewIdleTimeout
	}
	req, err := http.NewRequestWithContext(reqCtx, r.Method, u.String(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if sid != "" {
		applyMap(req, sessHeaders)
	} else {
		p.apply(req)
	}
	// A rewritten playlist has different byte offsets from its upstream.
	// Fetch it whole; segment and MP4 byte ranges still pass through.
	if rg := r.Header.Get("Range"); rg != "" && !playlist("", u.Path) {
		req.Header.Set("Range", rg)
	}
	resp, err := p.doWithTimeout(req, reqLimit)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer httpx.DrainAndClose(resp.Body)
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
	wrap := p.Wrap
	if sid != "" {
		wrap = func(raw string) string { return p.wrapWithSID(sid, raw) }
	}
	if playlist(resp.Header.Get("Content-Type"), base.Path) {
		p.writePlaylistWith(w, resp, base.String(), cacheKey, reqCtx, sessHeaders, sid != "", wrap, reqLimit)
		return
	}
	// Sniff a short prefix so a playlist without a useful content type
	// still has its segment URLs rewritten.
	buf := make([]byte, 64)
	n, readErr := io.ReadFull(resp.Body, buf)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		http.Error(w, readErr.Error(), http.StatusBadGateway)
		return
	}
	if n > 0 && bytes.HasPrefix(bytes.TrimSpace(buf[:n]), []byte("#EXTM3U")) {
		if resp.StatusCode == http.StatusPartialContent {
			p.writePlaylistWith(w, resp, base.String(), cacheKey, reqCtx, sessHeaders, sid != "", wrap, reqLimit)
			return
		}
		rest, _, err := httpx.ReadLimit(resp.Body, resp.ContentLength, playlistLimit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		body := append(append([]byte{}, buf[:n]...), rest...)
		p.writeRewrittenWith(w, base.String(), cacheKey, body, wrap)
		return
	}
	copyMediaHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if n > 0 {
		_, _ = w.Write(buf[:n])
	}
	// w is a countingWriter; ResponseController reaches the server's
	// flusher through Unwrap. A response without a length is a live
	// stream (chunked HTTP-FLV and the like) and must reach the player
	// chunk by chunk, not sit in the server's write buffer.
	rc := http.NewResponseController(w)
	if resp.Header.Get("Content-Length") == "" && rc.Flush() == nil {
		copyFlushing(w, resp.Body, rc)
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

func copyFlushing(dst io.Writer, src io.Reader, rc *http.ResponseController) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *Proxy) writePlaylistWith(w http.ResponseWriter, resp *http.Response, playlistURL, cacheKey string, ctx context.Context, sessHeaders boundHeaders, hasSession bool, wrap func(string) string, timeoutLimit time.Duration) {
	if resp.StatusCode == http.StatusPartialContent {
		// Extensionless playlist endpoints can only be identified after the
		// response. Retry without the player's range before rewriting.
		req := resp.Request.Clone(ctx)
		req.Header.Del("Range")
		if hasSession {
			applyMap(req, sessHeaders)
		}
		httpx.DrainAndClose(resp.Body)
		full, err := p.doWithTimeout(req, timeoutLimit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer httpx.DrainAndClose(full.Body)
		if full.StatusCode != http.StatusOK {
			http.Error(w, "upstream did not return a complete playlist", http.StatusBadGateway)
			return
		}
		resp = full
		playlistURL = full.Request.URL.String()
	}
	body, _, err := httpx.ReadLimit(resp.Body, resp.ContentLength, playlistLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	p.writeRewrittenWith(w, playlistURL, cacheKey, body, wrap)
}

func (p *Proxy) apply(req *http.Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for k, v := range p.headers {
		if v == "" || strings.EqualFold(k, "Host") {
			continue
		}
		if isCredentialHeader(k) && requestOrigin(req.URL) != p.boundOrigin {
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
