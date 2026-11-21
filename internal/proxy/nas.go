package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// RegisterNASOpener sets the opener used by /nas. Registering again
// replaces it.
func (p *Proxy) RegisterNASOpener(op NASOpener) {
	p.mu.Lock()
	p.nasOpener = op
	p.mu.Unlock()
}

// WrapNAS returns the local URL that plays one NAS file through the proxy.
func (p *Proxy) WrapNAS(server, share, path string) string {
	q := "server=" + url.QueryEscape(server) + "&share=" + url.QueryEscape(share) + "&path=" + url.QueryEscape(path)
	return p.base + "/nas?" + q
}

// serveNAS serves one NAS file: full content, or the byte range the
// player asked for. Missing parameters are 400, a missing file 404, a
// NAS error 502 and an unsatisfiable range 416.
func (p *Proxy) serveNAS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	server, share, path := q.Get("server"), q.Get("share"), q.Get("path")
	if server == "" || share == "" || path == "" {
		http.Error(w, "缺少内网盘参数", http.StatusBadRequest)
		return
	}
	p.mu.RLock()
	opener := p.nasOpener
	p.mu.RUnlock()
	if opener == nil {
		http.Error(w, "内网盘未连接", http.StatusBadGateway)
		return
	}
	f, err := opener(r.Context(), server, share, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "文件不存在", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer f.Close()
	size := f.Size()
	w.Header().Set("Accept-Ranges", "bytes")
	if ct := nasContentType(path); ct != "" {
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
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
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
