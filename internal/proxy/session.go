package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/httpx"
)

// SessionConfig configures an isolated loopback stream endpoint (/s/<id>/<filename>)
// with its own headers, optional range-prefetch acceleration and URL refresh callback.
type SessionConfig struct {
	URL      string
	Headers  map[string]string
	Filename string
	Prefetch bool
	Refresh  func(ctx context.Context) (string, map[string]string, error)
}

type sessionHeadersKey struct{}

type proxySession struct {
	mu            sync.RWMutex
	targetURL     string
	headers       map[string]string
	filename      string
	prefetch      bool
	refresh       func(ctx context.Context) (string, map[string]string, error)
	lastRefreshed time.Time
}

// RegisterSession mounts a dedicated local route that keeps its own headers and
// preserves the filename extension.
func (p *Proxy) RegisterSession(cfg SessionConfig) (id, localURL string) {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	id = hex.EncodeToString(raw[:])

	name := sanitizeFilename(cfg.Filename, cfg.URL)
	s := &proxySession{
		targetURL: strings.TrimSpace(cfg.URL),
		headers:   clone(cfg.Headers),
		filename:  name,
		prefetch:  cfg.Prefetch,
		refresh:   cfg.Refresh,
	}
	p.mu.Lock()
	if p.sessions == nil {
		p.sessions = map[string]*proxySession{}
	}
	p.sessions[id] = s
	p.mu.Unlock()
	return id, p.base + "/s/" + id + "/" + url.PathEscape(name)
}

// RemoveSession unmounts a registered session.
func (p *Proxy) RemoveSession(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	delete(p.sessions, id)
	p.invalidatePlaylistsLocked("s:" + id + "|")
	p.invalidatePlaylistsLocked("u:" + id + "|")
	p.mu.Unlock()
}

func (p *Proxy) session(id string) (*proxySession, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.sessions[id]
	return s, ok
}

func (s *proxySession) snapshot() (target string, headers map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.targetURL, clone(s.headers)
}

func (s *proxySession) tryRefresh(ctx context.Context) (string, map[string]string, bool) {
	if s.refresh == nil {
		return "", nil, false
	}
	s.mu.Lock()
	// Coalesce/rate-limit refreshes so concurrent range probes do not hammer upstream.
	if time.Since(s.lastRefreshed) < 5*time.Second {
		t, h := s.targetURL, clone(s.headers)
		s.mu.Unlock()
		return t, h, true
	}
	s.mu.Unlock()

	newURL, newHeaders, err := s.refresh(ctx)
	if err != nil || strings.TrimSpace(newURL) == "" {
		return "", nil, false
	}
	s.mu.Lock()
	s.targetURL = strings.TrimSpace(newURL)
	if newHeaders != nil {
		s.headers = clone(newHeaders)
	}
	s.lastRefreshed = time.Now()
	t, h := s.targetURL, clone(s.headers)
	s.mu.Unlock()
	return t, h, true
}

func (p *Proxy) serveSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	// Path format: /s/<id>/<filename> or /s/<id>/sub?url=<encoded>
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	id, tail, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		http.NotFound(w, r)
		return
	}
	sess, found := p.session(id)
	if !found {
		http.NotFound(w, r)
		return
	}

	target, headers := sess.snapshot()
	isSub := tail == "sub"
	if isSub {
		target = r.URL.Query().Get("url")
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "url must be http or https", http.StatusBadRequest)
		return
	}

	isPreview := isPreviewRequest(r)
	reqLimit := time.Duration(0)
	if isPreview {
		reqLimit = previewIdleTimeout
	}
	cacheKey := "s:" + id + "|" + target
	if r.Method == http.MethodGet {
		if cached, ok := p.cachedPlaylist(cacheKey); ok {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, cached)
			return
		}
	}

	isHLSPath := playlist("", u.Path) || playlist("", sess.filename)
	if r.Method == http.MethodGet && sess.prefetch && !isSub && !isHLSPath && !isPreview {
		if p.servePrefetch(w, r, sess, target, headers) {
			return
		}
	}

	resp, err := p.doSessionRequestWithTimeout(r.Context(), r.Method, target, headers, r.Header.Get("Range"), isHLSPath, reqLimit)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !isSub && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		if newURL, newHdr, refreshed := sess.tryRefresh(r.Context()); refreshed {
			p.mu.Lock()
			p.invalidatePlaylistsLocked("s:" + id + "|")
			p.mu.Unlock()
			httpx.DrainAndClose(resp.Body)
			resp, err = p.doSessionRequestWithTimeout(r.Context(), r.Method, newURL, newHdr, r.Header.Get("Range"), isHLSPath, reqLimit)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			headers = newHdr
			cacheKey = "s:" + id + "|" + newURL
		}
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
	wrapSub := func(raw string) string {
		return p.base + "/s/" + id + "/sub?url=" + url.QueryEscape(raw)
	}
	if playlist(resp.Header.Get("Content-Type"), base.Path) {
		p.writeSessionPlaylist(w, resp, base.String(), cacheKey, headers, wrapSub, r.Context(), reqLimit)
		return
	}
	buf := make([]byte, 64)
	n, readErr := io.ReadFull(resp.Body, buf)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		http.Error(w, readErr.Error(), http.StatusBadGateway)
		return
	}
	if n > 0 && bytes.HasPrefix(bytes.TrimSpace(buf[:n]), []byte("#EXTM3U")) {
		if resp.StatusCode == http.StatusPartialContent {
			p.writeSessionPlaylist(w, resp, base.String(), cacheKey, headers, wrapSub, r.Context(), reqLimit)
			return
		}
		restBody, _, err := httpx.ReadLimit(resp.Body, resp.ContentLength, playlistLimit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		body := append(append([]byte{}, buf[:n]...), restBody...)
		p.writeRewrittenWith(w, base.String(), cacheKey, body, wrapSub)
		return
	}
	copyMediaHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if n > 0 {
		_, _ = w.Write(buf[:n])
	}
	rc := http.NewResponseController(w)
	if resp.Header.Get("Content-Length") == "" && rc.Flush() == nil {
		copyFlushing(w, resp.Body, rc)
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) doSessionRequest(ctx context.Context, method, target string, headers map[string]string, rangeHdr string, isHLS bool) (*http.Response, error) {
	return p.doSessionRequestWithTimeout(ctx, method, target, headers, rangeHdr, isHLS, 0)
}

func (p *Proxy) doSessionRequestWithTimeout(ctx context.Context, method, target string, headers map[string]string, rangeHdr string, isHLS bool, timeoutLimit time.Duration) (*http.Response, error) {
	reqCtx := context.WithValue(ctx, sessionHeadersKey{}, headers)
	req, err := http.NewRequestWithContext(reqCtx, method, target, nil)
	if err != nil {
		return nil, err
	}
	applyMap(req, headers)
	if rangeHdr != "" && !isHLS {
		req.Header.Set("Range", rangeHdr)
	}
	return p.doWithTimeout(req, timeoutLimit)
}

func (p *Proxy) writeSessionPlaylist(w http.ResponseWriter, resp *http.Response, playlistURL, cacheKey string, headers map[string]string, wrap func(string) string, ctx context.Context, timeoutLimit time.Duration) {
	if resp.StatusCode == http.StatusPartialContent {
		reqCtx := context.WithValue(ctx, sessionHeadersKey{}, headers)
		req := resp.Request.Clone(reqCtx)
		req.Header.Del("Range")
		applyMap(req, headers)
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

func (p *Proxy) writeRewrittenWith(w http.ResponseWriter, playlistURL, cacheKey string, body []byte, wrap func(string) string) {
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("#EXTM3U")) {
		http.Error(w, "upstream returned a page instead of an HLS playlist", http.StatusBadGateway)
		return
	}
	out := rewriteM3U8(string(body), playlistURL, wrap)
	p.storePlaylist(cacheKey, out, body)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, out)
}

func applyMap(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		if v == "" || strings.EqualFold(k, "Host") {
			continue
		}
		req.Header.Set(k, v)
	}
}

func sanitizeFilename(filename, rawURL string) string {
	name := strings.TrimSpace(filename)
	if name == "" {
		if u, err := url.Parse(rawURL); err == nil {
			name = path.Base(u.Path)
		}
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "stream.mp4"
	}
	return name
}
