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

func TestPreviewBypassesPrefetchAndCachesVODPlaylist(t *testing.T) {
	payload := make([]byte, 2*1024*1024+4096)
	_, _ = rand.Read(payload)

	var mp4Reqs, vodM3U8Reqs, liveM3U8Reqs atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie.mp4":
			mp4Reqs.Add(1)
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "movie.mp4", time.Unix(0, 0), bytes.NewReader(payload))
		case "/vod.m3u8":
			vodM3U8Reqs.Add(1)
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nseg0.ts\n#EXT-X-ENDLIST\n")
		case "/live.m3u8":
			liveM3U8Reqs.Add(1)
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nseg0.ts\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	p, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// 1. Preview request (?preview=1) on a prefetch-enabled MP4 session bypasses
	// multi-chunk prefetch and issues only a single direct upstream request.
	_, mp4URL := p.RegisterSession(SessionConfig{
		URL:      upstream.URL + "/movie.mp4",
		Filename: "movie.mp4",
		Prefetch: true,
	})
	resp, err := http.Get(mp4URL + "?preview=1")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, payload) {
		t.Fatalf("preview direct stream failed: status=%d err=%v len=%d", resp.StatusCode, err, len(body))
	}
	if got := mp4Reqs.Load(); got != 1 {
		t.Fatalf("preview request unexpectedly triggered prefetch chunks: upstream requests=%d, want 1", got)
	}

	// 2. VOD playlist (#EXT-X-ENDLIST) is cached across main + preview requests
	// and invalidated when session is removed.
	vodID, vodURL := p.RegisterSession(SessionConfig{
		URL:      upstream.URL + "/vod.m3u8",
		Filename: "vod.m3u8",
	})
	for _, u := range []string{vodURL, vodURL + "?preview=1"} {
		r, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK || !strings.Contains(string(b), "#EXT-X-ENDLIST") {
			t.Fatalf("vod playlist failed for %s: status=%d body=%s", u, r.StatusCode, b)
		}
	}
	if got := vodM3U8Reqs.Load(); got != 1 {
		t.Fatalf("VOD playlist not cached: upstream hits=%d, want 1", got)
	}
	p.RemoveSession(vodID)

	// 3. Live playlist (no #EXT-X-ENDLIST / #EXT-X-STREAM-INF) is NEVER cached.
	liveURL := p.Wrap(upstream.URL + "/live.m3u8")
	for range 2 {
		r, err := http.Get(liveURL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(r.Body)
		r.Body.Close()
	}
	if got := liveM3U8Reqs.Load(); got != 2 {
		t.Fatalf("live playlist was wrongly cached: upstream hits=%d, want 2", got)
	}
}
