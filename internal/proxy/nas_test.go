package proxy

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
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
		case "没有这个文件.mp4":
			return nil, fs.ErrNotExist
		default:
			return nil, errors.New("nas boom")
		}
	})
	return p
}

func TestNASFullAndRange(t *testing.T) {
	p := nasTestProxy(t)
	url := p.WrapNAS("192.168.1.10", "Movies", `电影\示例.mp4`)

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
	url := p.WrapNAS("192.168.1.10", "Movies", `电影\示例.mp4`)

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
}

func TestNASMissingParamsAndErrors(t *testing.T) {
	p := nasTestProxy(t)

	for _, raw := range []string{
		p.Base() + "/nas",
		p.Base() + "/nas?server=192.168.1.10&share=Movies",
		p.Base() + "/nas?server=192.168.1.10&path=x.mp4",
	} {
		resp, err := http.Get(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET %s: status %d, want 400", raw, resp.StatusCode)
		}
	}

	resp, err := http.Get(p.WrapNAS("192.168.1.10", "Movies", "没有这个文件.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing file: status %d, want 404", resp.StatusCode)
	}

	resp, err = http.Get(p.WrapNAS("192.168.1.10", "Movies", "坏了.mp4"))
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
	resp, err = http.Get(p2.WrapNAS("192.168.1.10", "Movies", "示例.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("no opener: status %d, want 502", resp.StatusCode)
	}

	// POST is not a playback method.
	req, _ := http.NewRequest(http.MethodPost, p.WrapNAS("192.168.1.10", "Movies", `电影\示例.mp4`), strings.NewReader(""))
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
