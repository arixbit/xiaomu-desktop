package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// NASFile is one open NAS file served to the player. The proxy only ever
// reads it with ReadAt, so a Range request never depends on another
// request's position.
type NASFile interface {
	io.ReaderAt
	io.Seeker
	Size() int64
	Close() error
}

// NASOpener opens one file on a NAS for a proxy request. It is registered
// by the app, which owns the NAS connection; the proxy closes the file.
type NASOpener func(ctx context.Context, server, share, path string) (NASFile, error)

// nasBinding is one file bound to a playback token: the server, share and
// share-relative path the token may open, and nothing else.
type nasBinding struct {
	server, share, path string
	ctx                 context.Context
	cancel              context.CancelFunc
}

// IssueNASFile binds one NAS file to a fresh random token and returns it.
// The token is 128 bits from crypto/rand, so it cannot be guessed; it
// opens only the bound file, never any other path on the share.
func (p *Proxy) IssueNASFile(server, share, path string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	if p.nasTokens == nil {
		p.nasTokens = make(map[string]nasBinding)
	}
	p.nasTokens[token] = nasBinding{server: server, share: share, path: path, ctx: ctx, cancel: cancel}
	p.mu.Unlock()
	return token, nil
}

// WrapNAS returns the local URL that plays the file bound to token. The
// URL carries only the token: no server, share or path for another local
// process to reuse.
func (p *Proxy) WrapNAS(token string) string {
	return p.base + "/nas?token=" + url.QueryEscape(token)
}

// RevokeNASFile revokes one token and interrupts its active requests.
func (p *Proxy) RevokeNASFile(token string) {
	if token == "" {
		return
	}
	p.mu.Lock()
	if binding, ok := p.nasTokens[token]; ok {
		delete(p.nasTokens, token)
		binding.cancel()
	}
	p.mu.Unlock()
}

// RevokeAllNASFiles revokes all tokens and interrupts their active requests.
func (p *Proxy) RevokeAllNASFiles() {
	p.mu.Lock()
	for _, binding := range p.nasTokens {
		binding.cancel()
	}
	p.nasTokens = nil
	p.mu.Unlock()
}

// RegisterNASOpener sets the opener used by /nas. Registering again
// replaces it.
func (p *Proxy) RegisterNASOpener(op NASOpener) {
	p.mu.Lock()
	p.nasOpener = op
	p.mu.Unlock()
}

// serveNAS serves one NAS file: full content, or the byte range the
// player asked for. The request carries only a playback token; the token
// decides which file is served, so no caller can ask for another path.
// An unknown or revoked token is 403, a missing file 404, a NAS error
// 502 and an unsatisfiable range 416.
func (p *Proxy) serveNAS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "缺少播放令牌", http.StatusForbidden)
		return
	}
	p.mu.RLock()
	binding, ok := p.nasTokens[token]
	opener := p.nasOpener
	p.mu.RUnlock()
	if !ok {
		http.Error(w, "播放令牌无效或已撤销", http.StatusForbidden)
		return
	}
	if opener == nil {
		http.Error(w, "内网盘未连接", http.StatusBadGateway)
		return
	}
	// Cancellation must be visible to SMB as soon as this token is
	// revoked, even if the write-deadline callback hasn't run yet.
	ctx, cancel := context.WithCancel(binding.ctx)
	defer cancel()
	stopRequest := context.AfterFunc(r.Context(), cancel)
	defer stopRequest()
	revoked := make(chan struct{})
	stop := context.AfterFunc(binding.ctx, func() {
		// The handler can be blocked writing to the player instead of
		// reading SMB. Interrupt the write as well as the file read.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now())
		close(revoked)
	})
	defer func() {
		if !stop() {
			// The response writer is valid only while this handler runs.
			<-revoked
		}
	}()
	if ctx.Err() != nil || r.Context().Err() != nil {
		return
	}
	f, err := opener(ctx, binding.server, binding.share, binding.path)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "文件不存在", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer f.Close()
	if ctx.Err() != nil {
		return
	}
	size := f.Size()
	w.Header().Set("Accept-Ranges", "bytes")
	if ct := nasContentType(binding.path); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	rg := r.Header.Get("Range")
	if rg == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.Copy(w, io.NewSectionReader(f, 0, size))
		return
	}
	start, length, ok := parseRange(rg, size)
	if !ok {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		_, _ = io.WriteString(w, "range not satisfiable")
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, size))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(http.StatusPartialContent)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.CopyN(w, io.NewSectionReader(f, start, length), length)
}

// parseRange parses one "bytes=start-end" range. Suffix ("bytes=-n") and
// open-ended ("bytes=n-") forms are accepted; anything else, or a range
// past the end of the file, is rejected.
func parseRange(header string, size int64) (start, length int64, ok bool) {
	if size <= 0 || !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(header, "bytes=")
	lo, hi, found := strings.Cut(spec, "-")
	if !found || lo == "" && hi == "" {
		return 0, 0, false
	}
	if lo == "" {
		// Suffix: the last n bytes. A zero suffix asks for no bytes
		// at all, which has no valid Content-Range.
		suffix, err := strconv.ParseInt(hi, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, suffix, true
	}
	start, err := strconv.ParseInt(lo, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	if hi == "" {
		return start, size - start, true
	}
	end, err := strconv.ParseInt(hi, 10, 64)
	if err != nil || end < start {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end - start + 1, true
}

// nasContentType is the media type for a playable NAS file.
func nasContentType(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".mov"):
		return "video/quicktime"
	case strings.HasSuffix(lower, ".mp4"), strings.HasSuffix(lower, ".m4v"):
		return "video/mp4"
	default:
		return ""
	}
}
