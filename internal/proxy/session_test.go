package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionRoutingHeadersRefreshAndHLS(t *testing.T) {
	var expired atomic.Bool
	expired.Store(true)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "sid=fresh" && expired.Load() && r.URL.Path == "/old.m3u8" {
			http.Error(w, "expired", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/fresh.m3u8":
			if r.Header.Get("Cookie") != "sid=fresh" || r.Header.Get("Referer") != "https://pan.example.com/" {
				http.Error(w, "bad headers", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:5.0,\nseg0.ts\n"))
		case "/seg0.ts":
			if r.Header.Get("Cookie") != "sid=fresh" {
				http.Error(w, "missing cookie on segment", http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte("SEGMENT-BYTES"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	p, err := Start(map[string]string{"Referer": "https://other.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	id, localURL := p.RegisterSession(SessionConfig{
		URL:      upstream.URL + "/old.m3u8",
		Headers:  map[string]string{"Cookie": "sid=old", "Referer": "https://pan.example.com/"},
		Filename: "4k.m3u8",
		Refresh: func(context.Context) (string, map[string]string, error) {
			expired.Store(false)
			return upstream.URL + "/fresh.m3u8", map[string]string{
				"Cookie":  "sid=fresh",
				"Referer": "https://pan.example.com/",
			}, nil
		},
	})
	if !strings.HasSuffix(localURL, "/4k.m3u8") {
		t.Fatalf("expected filename preserved in local URL, got %s", localURL)
	}

	resp, err := http.Get(localURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var segURL string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "http://") {
			segURL = strings.TrimSpace(line)
		}
	}
	if !strings.Contains(segURL, "/s/"+id+"/sub?url=") {
		t.Fatalf("segment URL did not stay in session route: %s", segURL)
	}

	segResp, err := http.Get(segURL)
	if err != nil {
		t.Fatal(err)
	}
	segData, _ := io.ReadAll(segResp.Body)
	segResp.Body.Close()
	if segResp.StatusCode != http.StatusOK || string(segData) != "SEGMENT-BYTES" {
		t.Fatalf("segment status=%d data=%q", segResp.StatusCode, segData)
	}

	p.RemoveSession(id)
	gone, err := http.Get(localURL)
	if err != nil {
		t.Fatal(err)
	}
	gone.Body.Close()
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("removed session returned %d, want 404", gone.StatusCode)
	}
}

func TestSessionRangePrefetchIntegrityAndSeek(t *testing.T) {
	payload := make([]byte, 3*1024*1024+12345) // > 3 MiB across 4 prefetch chunks
	_, _ = rand.Read(payload)

	var rangeRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "token=ok" {
			http.Error(w, "unauthorized", http.StatusForbidden)
			return
		}
		if r.Header.Get("Range") != "" {
			rangeRequests.Add(1)
		}
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "movie.mp4", time.Unix(0, 0), bytes.NewReader(payload))
	}))
	defer upstream.Close()

	p, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	_, localURL := p.RegisterSession(SessionConfig{
		URL:      upstream.URL + "/movie.mp4",
		Headers:  map[string]string{"Cookie": "token=ok"},
		Filename: "4K原画.mp4",
		Prefetch: true,
	})

	// 1. Full stream via concurrent range prefetch.
	resp, err := http.Get(localURL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, payload) {
		t.Fatalf("full prefetch mismatch: status=%d len=%d want=%d", resp.StatusCode, len(got), len(payload))
	}
	if rangeRequests.Load() < 3 {
		t.Fatalf("expected multi-chunk range prefetch requests, got %d", rangeRequests.Load())
	}

	// 2. Mid-file range seek spanning multiple chunks.
	req, _ := http.NewRequest(http.MethodGet, localURL, nil)
	req.Header.Set("Range", "bytes=1500000-")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusPartialContent || !bytes.Equal(got2, payload[1500000:]) {
		t.Fatalf("seek prefetch mismatch: status=%d len=%d want=%d", resp2.StatusCode, len(got2), len(payload)-1500000)
	}
}
