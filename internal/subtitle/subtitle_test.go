package subtitle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestComputeShooterHash(t *testing.T) {
	dir := t.TempDir()
	tooSmall := filepath.Join(dir, "tiny.mkv")
	if err := os.WriteFile(tooSmall, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ComputeShooterHash(tooSmall); err == nil {
		t.Fatal("expected error for tiny file")
	}

	data := make([]byte, 32768)
	for i := range data {
		data[i] = byte((i*31 + 7) % 251)
	}
	videoPath := filepath.Join(dir, "movie.4k.mkv")
	if err := os.WriteFile(videoPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ComputeShooterHash(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(got, ";")
	if len(parts) != 4 {
		t.Fatalf("hash parts = %d, want 4 (%q)", len(parts), got)
	}
	size := int64(len(data))
	offsets := [4]int64{4096, size / 3 * 2, size / 3, size - 8192}
	for i, off := range offsets {
		sum := md5.Sum(data[off : off+4096])
		want := hex.EncodeToString(sum[:])
		if parts[i] != want {
			t.Fatalf("chunk %d hash = %q, want %q", i, parts[i], want)
		}
	}
}

func TestSearchShooterAndDownload(t *testing.T) {
	var gotPathInfo, gotHash string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/subapi.php":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			gotHash = r.FormValue("filehash")
			gotPathInfo = r.FormValue("pathinfo")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"Desc":  "简英双语特效",
					"Delay": 500,
					"Files": []map[string]any{
						{"Ext": "ass", "Link": srv.URL + "/dl/1.ass"},
						{"Ext": "rar", "Link": srv.URL + "/dl/ignore.rar"},
					},
				},
			})
		case "/dl/1.ass":
			_, _ = w.Write([]byte("[Script Info]\nTitle: Demo\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{
		HTTP:           srv.Client(),
		ShooterBaseURL: srv.URL + "/api/subapi.php",
	}
	cands, err := c.SearchShooter(context.Background(), "a;b;c;d", "/Users/secret/private/Movie.2026.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != "a;b;c;d" || gotPathInfo != "Movie.2026.mkv" {
		t.Fatalf("shooter request hash=%q pathinfo=%q (must strip directory)", gotHash, gotPathInfo)
	}
	if len(cands) != 1 || cands[0].Format != "ass" || cands[0].DelayMS != 500 {
		t.Fatalf("unexpected candidates: %+v", cands)
	}

	dest := t.TempDir()
	path, err := c.Download(context.Background(), cands[0], dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, ".ass") {
		t.Fatalf("downloaded path = %q", path)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "[Script Info]") {
		t.Fatalf("downloaded body = %q, err=%v", string(body), err)
	}
}

func TestSearchShooterReturnsHTTPErrorBeforeBody(t *testing.T) {
	sendBody := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.(http.Flusher).Flush()
		<-sendBody
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	defer close(sendBody)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c := &Client{HTTP: srv.Client(), ShooterBaseURL: srv.URL}
	_, err := c.SearchShooter(ctx, "a;b;c;d", "sample.mkv")
	if err == nil || err.Error() != "射手字幕服务返回 HTTP 503" {
		t.Fatalf("unexpected search error: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("search waited for the error body until the request timed out")
	}
}

func TestSearchASSRTAndZipExtraction(t *testing.T) {
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for _, entry := range []struct {
		name string
		body string
	}{
		{"__MACOSX/._movie.chs.ass", "junk"},
		{"sub/movie.eng.srt", "1\n00:00:01,000 --> 00:00:02,000\nHello\n"},
		{"sub/movie.chs&eng.ass", "[Script Info]\nTitle: Chinese\n"},
	} {
		fw, err := zw.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(entry.body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sub/search":
			if r.URL.Query().Get("token") != "demo-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0,
				"sub": map[string]any{
					"subs": []map[string]any{
						{
							"id":          42,
							"native_name": "示例电影 简英字幕",
							"subtype":     "ASS",
							"lang":        map[string]any{"desc": "双语"},
						},
					},
				},
			})
		case "/v1/sub/detail":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 0,
				"sub": map[string]any{
					"subs": []map[string]any{
						{
							"id":       42,
							"filename": "bundle.zip",
							"url":      srv.URL + "/bundle.zip",
						},
					},
				},
			})
		case "/bundle.zip":
			_, _ = w.Write(zipBuf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Without token, ASSRT returns ErrTokenRequired.
	noTok := &Client{HTTP: srv.Client(), ASSRTBaseURL: srv.URL}
	if _, err := noTok.SearchASSRT(context.Background(), "示例电影"); !errors.Is(err, ErrTokenRequired) {
		t.Fatalf("expected ErrTokenRequired, got %v", err)
	}

	c := &Client{HTTP: srv.Client(), ASSRTBaseURL: srv.URL, ASSRTToken: "demo-token"}
	cands, err := c.SearchASSRT(context.Background(), "示例电影")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].DetailID != 42 || cands[0].Lang != "双语" {
		t.Fatalf("unexpected ASSRT candidates: %+v", cands)
	}
	outPath, err := c.Download(context.Background(), cands[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(outPath, ".ass") {
		t.Fatalf("expected Chinese .ass extracted from zip, got %q", outPath)
	}
	body, err := os.ReadFile(outPath)
	if err != nil || !strings.Contains(string(body), "Title: Chinese") {
		t.Fatalf("extracted body = %q, err=%v", string(body), err)
	}
}
