package proxy

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeNASFile struct {
	data []byte
	off  int64
}

func (f *fakeNASFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeNASFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.off = off
	case io.SeekCurrent:
		f.off += off
	case io.SeekEnd:
		f.off = int64(len(f.data)) + off
	}
	return f.off, nil
}

func (f *fakeNASFile) Size() int64 { return int64(len(f.data)) }

func (f *fakeNASFile) Close() error { return nil }

func nasTestProxy(t *testing.T) *Proxy {
	t.Helper()
	p, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.RegisterNASOpener(func(ctx context.Context, server, share, path string) (NASFile, error) {
		if server != "192.168.1.10" || share != "Movies" {
			return nil, errors.New("wrong server")
		}
		switch path {
		case `电影\示例.mp4`:
			return &fakeNASFile{data: []byte("0123456789")}, nil
		case `纪录片.mov`:
			return &fakeNASFile{data: []byte("abcdefgh")}, nil
		case "empty.mp4":
			return &fakeNASFile{}, nil
		case "没有这个文件.mp4":
			return nil, fs.ErrNotExist
		default:
			return nil, errors.New("nas boom")
		}
	})
	return p
}

// nasURL issues a playback token for one file and returns its URL.
func nasURL(t *testing.T, p *Proxy, server, share, path string) string {
	t.Helper()
	token, err := p.IssueNASFile(server, share, path)
	if err != nil {
		t.Fatal(err)
	}
	return p.WrapNAS(token)
}

func TestNASFullAndRange(t *testing.T) {
	p := nasTestProxy(t)
	url := nasURL(t, p, "192.168.1.10", "Movies", `电影\示例.mp4`)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "0123456789" {
		t.Fatalf("full: status %d body %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "10" {
		t.Fatalf("Content-Length = %q", cl)
	}
	if ar := resp.Header.Get("Accept-Ranges"); ar != "bytes" {
		t.Fatalf("Accept-Ranges = %q", ar)
	}
	if got := p.BytesServed(); got != 10 {
		t.Fatalf("served = %d, want 10", got)
	}

	cases := []struct {
		rg   string
		want string
		cr   string
	}{
		{"bytes=2-5", "2345", "bytes 2-5/10"},
		{"bytes=8-", "89", "bytes 8-9/10"},
		{"bytes=-3", "789", "bytes 7-9/10"},
		{"bytes=0-99", "0123456789", "bytes 0-9/10"},
	}
	for _, tt := range cases {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Range", tt.rg)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent || string(body) != tt.want ||
			resp.Header.Get("Content-Range") != tt.cr {
			t.Fatalf("range %q: status %d body %q Content-Range %q, want 206 %q %q",
				tt.rg, resp.StatusCode, body, resp.Header.Get("Content-Range"), tt.want, tt.cr)
		}
	}
	if got := p.BytesServed(); got != 10+4+2+3+10 {
		t.Fatalf("served = %d, want 29", got)
	}
}

func TestNASRangeErrorsAndHead(t *testing.T) {
	p := nasTestProxy(t)
	url := nasURL(t, p, "192.168.1.10", "Movies", `电影\示例.mp4`)

	for _, rg := range []string{"bytes=10-", "bytes=5-2", "bytes=x-y", "bytes=0-1,4-5", "items=0-1", "bytes=-0"} {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Range", rg)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable ||
			resp.Header.Get("Content-Range") != "bytes */10" {
			t.Fatalf("range %q: status %d Content-Range %q", rg, resp.StatusCode, resp.Header.Get("Content-Range"))
		}
	}

	req, _ := http.NewRequest(http.MethodHead, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) != 0 || resp.Header.Get("Content-Length") != "10" {
		t.Fatalf("HEAD: status %d body %d Content-Length %q", resp.StatusCode, len(body), resp.Header.Get("Content-Length"))
	}

	req, _ = http.NewRequest(http.MethodHead, url, nil)
	req.Header.Set("Range", "bytes=2-5")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(body) != 0 ||
		resp.Header.Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("HEAD range: status %d body %d Content-Range %q", resp.StatusCode, len(body), resp.Header.Get("Content-Range"))
	}

	url = nasURL(t, p, "192.168.1.10", "Movies", "empty.mp4")
	req = httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=-3")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != http.StatusRequestedRangeNotSatisfiable || w.Header().Get("Content-Range") != "bytes */0" {
		t.Fatalf("empty suffix range: status %d Content-Range %q", w.Code, w.Header().Get("Content-Range"))
	}
}

func TestNASMissingParamsAndErrors(t *testing.T) {
	p := nasTestProxy(t)

	// No token at all, an unknown token, and the old server/share/path
	// parameters without a token are all refused: the token alone
	// decides what may be read.
	for _, raw := range []string{
		p.Base() + "/nas",
		p.Base() + "/nas?token=0123456789abcdef0123456789abcdef",
		p.Base() + "/nas?server=192.168.1.10&share=Movies&path=x.mp4",
	} {
		resp, err := http.Get(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("GET %s: status %d, want 403", raw, resp.StatusCode)
		}
	}

	resp, err := http.Get(nasURL(t, p, "192.168.1.10", "Movies", "没有这个文件.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing file: status %d, want 404", resp.StatusCode)
	}

	resp, err = http.Get(nasURL(t, p, "192.168.1.10", "Movies", "坏了.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("nas error: status %d, want 502", resp.StatusCode)
	}

	// No opener registered at all: the same 502.
	p2, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	token, err := p2.IssueNASFile("192.168.1.10", "Movies", "示例.mp4")
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(p2.WrapNAS(token))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("no opener: status %d, want 502", resp.StatusCode)
	}

	// POST is not a playback method.
	req, _ := http.NewRequest(http.MethodPost, nasURL(t, p, "192.168.1.10", "Movies", `电影\示例.mp4`), strings.NewReader(""))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: status %d, want 405", resp.StatusCode)
	}
}

// A playback token opens only the file it was issued for: extra path or
// server parameters are ignored, a revoked token is refused, and the URL
// itself carries no server, share or path to reuse.
func TestNASTokenScopeAndRevocation(t *testing.T) {
	p := nasTestProxy(t)

	tokenA, err := p.IssueNASFile("192.168.1.10", "Movies", `电影\示例.mp4`)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, err := p.IssueNASFile("192.168.1.10", "Movies", `纪录片.mov`)
	if err != nil {
		t.Fatal(err)
	}
	if tokenA == tokenB {
		t.Fatal("two issues produced the same token")
	}
	urlA := p.WrapNAS(tokenA)
	for _, leaked := range []string{"server=", "share=", "path=", "192.168.1.10", "Movies", "示例"} {
		if strings.Contains(urlA, leaked) {
			t.Fatalf("token URL leaks %q: %s", leaked, urlA)
		}
	}
	get := func(raw string) (int, string) {
		t.Helper()
		resp, err := http.Get(raw)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}
	if status, body := get(urlA); status != http.StatusOK || body != "0123456789" {
		t.Fatalf("token A: status %d body %q", status, body)
	}
	// Asking for another file with token A still serves file A.
	other := urlA + "&path=" + url.QueryEscape(`纪录片.mov`) + "&server=10.0.0.1&share=Other"
	if status, body := get(other); status != http.StatusOK || body != "0123456789" {
		t.Fatalf("token A with other-file parameters: status %d body %q", status, body)
	}
	// Token B serves its own file.
	if status, body := get(p.WrapNAS(tokenB)); status != http.StatusOK || body != "abcdefgh" {
		t.Fatalf("token B: status %d body %q", status, body)
	}
	// Revoking one token leaves the other working.
	p.RevokeNASFile(tokenA)
	if status, _ := get(urlA); status != http.StatusForbidden {
		t.Fatalf("revoked token A: status %d, want 403", status)
	}
	if status, body := get(p.WrapNAS(tokenB)); status != http.StatusOK || body != "abcdefgh" {
		t.Fatalf("token B after revoking A: status %d body %q", status, body)
	}
	// Revoking everything stops the rest too.
	p.RevokeAllNASFiles()
	if status, _ := get(p.WrapNAS(tokenB)); status != http.StatusForbidden {
		t.Fatalf("token B after revoke-all: status %d, want 403", status)
	}
	// Revoking an unknown token is a no-op, not a panic.
	p.RevokeNASFile("0123456789abcdef0123456789abcdef")
}

type blockedNASFile struct {
	fakeNASFile
	ctx   context.Context
	reads chan<- context.Context
}

func (f *blockedNASFile) ReadAt(_ []byte, _ int64) (int, error) {
	f.reads <- f.ctx
	<-f.ctx.Done()
	return 0, f.ctx.Err()
}

func TestNASActiveReadCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reads := make(chan context.Context, 3)
	p := &Proxy{}
	p.RegisterNASOpener(func(ctx context.Context, _, _, _ string) (NASFile, error) {
		return &blockedNASFile{fakeNASFile: fakeNASFile{data: []byte("0123456789")}, ctx: ctx, reads: reads}, nil
	})
	tokenA, err := p.IssueNASFile("example.com", "Movies", "first.mp4")
	if err != nil {
		t.Fatal(err)
	}
	tokenB, err := p.IssueNASFile("example.com", "Movies", "second.mp4")
	if err != nil {
		t.Fatal(err)
	}
	type activeRead struct {
		ctx    context.Context
		cancel context.CancelFunc
		done   <-chan struct{}
	}
	start := func(token, byteRange string) activeRead {
		requestCtx, cancel := context.WithCancel(ctx)
		t.Cleanup(cancel)
		req := httptest.NewRequestWithContext(requestCtx, http.MethodGet, p.WrapNAS(token), nil)
		if byteRange != "" {
			req.Header.Set("Range", byteRange)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.ServeHTTP(httptest.NewRecorder(), req)
		}()
		select {
		case readCtx := <-reads:
			return activeRead{ctx: readCtx, cancel: cancel, done: done}
		case <-ctx.Done():
			t.Fatal("playback request never started reading")
			return activeRead{}
		}
	}
	awaitCanceled := func(read activeRead) {
		select {
		case <-read.done:
			if err := read.ctx.Err(); !errors.Is(err, context.Canceled) {
				t.Fatalf("read context error = %v, want canceled", err)
			}
		case <-ctx.Done():
			t.Fatal("canceled playback request did not return")
		}
	}
	a := start(tokenA, "bytes=0-3")
	b := start(tokenB, "")
	seek := start(tokenB, "bytes=2-5")
	seek.cancel()
	awaitCanceled(seek)
	if err := b.ctx.Err(); err != nil {
		t.Fatalf("canceling one range canceled another request for that token: %v", err)
	}
	p.RevokeNASFile(tokenA)
	awaitCanceled(a)
	if err := b.ctx.Err(); err != nil {
		t.Fatalf("revoking one token canceled another token's request: %v", err)
	}
	p.RevokeAllNASFiles()
	awaitCanceled(b)
}

type blockedNASWriter struct {
	*httptest.ResponseRecorder
	ctx         context.Context
	writing     chan struct{}
	interrupted chan struct{}
}

func (w *blockedNASWriter) Write([]byte) (int, error) {
	close(w.writing)
	select {
	case <-w.interrupted:
		return 0, os.ErrDeadlineExceeded
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
}

func (w *blockedNASWriter) SetWriteDeadline(time.Time) error {
	close(w.interrupted)
	return nil
}

func TestNASRevocationInterruptsWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := &Proxy{}
	p.RegisterNASOpener(func(context.Context, string, string, string) (NASFile, error) {
		return &fakeNASFile{data: []byte("0123456789")}, nil
	})
	token, err := p.IssueNASFile("example.com", "Movies", "video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, p.WrapNAS(token), nil)
	req.Header.Set("Range", "bytes=2-5")
	w := &blockedNASWriter{
		ResponseRecorder: httptest.NewRecorder(), ctx: ctx,
		writing: make(chan struct{}), interrupted: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.ServeHTTP(w, req)
	}()
	select {
	case <-w.writing:
	case <-ctx.Done():
		t.Fatal("playback response never started writing")
	}
	p.RevokeNASFile(token)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("revocation did not interrupt the blocked response")
	}
}
