package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewTransportTuning(t *testing.T) {
	tr := NewTransport()
	if tr.MaxIdleConns < 100 {
		t.Fatalf("MaxIdleConns = %d, want >= 100", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost < 16 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want >= 16", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxConnsPerHost <= 0 {
		t.Fatalf("MaxConnsPerHost = %d, want > 0", tr.MaxConnsPerHost)
	}
	if tr.TLSHandshakeTimeout != DefaultTLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, DefaultTLSHandshakeTimeout)
	}
	if tr.Proxy == nil {
		t.Fatal("Proxy func should be preserved from netproxy.Transport()")
	}
}

func TestTracingRoundTripperLogsSlowPhasesAndSanitizesURL(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate server TTFB processing delay.
		time.Sleep(45 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("first-chunk-"))
		if flusher != nil {
			flusher.Flush()
		}
		// Simulate slow body transfer phase.
		time.Sleep(45 * time.Millisecond)
		_, _ = w.Write([]byte("second-chunk"))
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	var mu sync.Mutex
	var recorded []TraceStats

	tr := WrapRoundTripper(srv.Client().Transport, TracingOptions{
		Threshold: 60 * time.Millisecond,
		Logger:    logger,
		OnSlow: func(s TraceStats) {
			mu.Lock()
			recorded = append(recorded, s)
			mu.Unlock()
		},
	})
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/search?token=secret123&q=test#frag", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, exceeded, err := ReadLimit(resp.Body, resp.ContentLength, 1<<20)
	DrainAndClose(resp.Body)
	if err != nil || exceeded || string(body) != "first-chunk-second-chunk" {
		t.Fatalf("ReadLimit got len=%d exceeded=%v err=%v", len(body), exceeded, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 1 {
		t.Fatalf("expected 1 slow trace record, got %d", len(recorded))
	}
	s := recorded[0]
	if s.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", s.StatusCode)
	}
	if s.TLS <= 0 {
		t.Fatalf("expected positive TLS duration on fresh HTTPS connection, got %v", s.TLS)
	}
	if s.TTFB < 30*time.Millisecond {
		t.Fatalf("expected TTFB >= 30ms, got %v", s.TTFB)
	}
	if s.Transfer < 30*time.Millisecond {
		t.Fatalf("expected Transfer >= 30ms, got %v", s.Transfer)
	}
	if s.Total < 80*time.Millisecond {
		t.Fatalf("expected Total >= 80ms, got %v", s.Total)
	}
	if s.BytesRead != int64(len("first-chunk-second-chunk")) {
		t.Fatalf("BytesRead = %d, want %d", s.BytesRead, len("first-chunk-second-chunk"))
	}
	if strings.Contains(s.URL, "secret123") || strings.Contains(s.URL, "frag") {
		t.Fatalf("URL not sanitized: %q", s.URL)
	}

	var entry map[string]any
	if err := json.Unmarshal(logBuf.Bytes(), &entry); err != nil {
		t.Fatalf("slow log is not valid JSON: %v\nraw=%s", err, logBuf.String())
	}
	for _, key := range []string{"method", "url", "host", "status", "conn_reused", "bytes", "dns_ms", "tcp_ms", "tls_ms", "ttfb_ms", "transfer_ms", "total_ms"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("missing expected structured log field %q in %v", key, entry)
		}
	}
	if strings.Contains(logBuf.String(), "secret123") {
		t.Fatalf("secret query token leaked in log: %s", logBuf.String())
	}
}

func TestTracingRoundTripperSkipsFastRequestsAndRecordsTimeoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fast" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	var slowCount atomic.Int32
	var lastMu sync.Mutex
	var last TraceStats
	var logBuf bytes.Buffer
	tr := WrapRoundTripper(srv.Client().Transport, TracingOptions{
		Threshold: 40 * time.Millisecond,
		Logger:    slog.New(slog.NewJSONHandler(&logBuf, nil)),
		OnSlow: func(s TraceStats) {
			slowCount.Add(1)
			lastMu.Lock()
			last = s
			lastMu.Unlock()
		},
	})
	client := &http.Client{Transport: tr, Timeout: 80 * time.Millisecond}

	// Fast request must not trigger slow log.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/fast", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	DrainAndClose(resp.Body)
	if slowCount.Load() != 0 {
		t.Fatalf("fast request logged as slow: %s", logBuf.String())
	}

	// Stalled TTFB request that times out after 80ms should trigger slow log with Err & TTFB.
	slowReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/stall", nil)
	resp2, err := client.Do(slowReq)
	if err == nil {
		DrainAndClose(resp2.Body)
		t.Fatal("expected timeout error")
	}
	if slowCount.Load() != 1 {
		t.Fatalf("expected 1 slow timeout log, got %d", slowCount.Load())
	}
	lastMu.Lock()
	defer lastMu.Unlock()
	if last.Err == nil || last.TTFB < 40*time.Millisecond {
		t.Fatalf("expected error + TTFB attribution on stalled request, got err=%v ttfb=%v", last.Err, last.TTFB)
	}
}

func TestTracingRoundTripperConcurrentRaceSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	tr := NewTransport()
	tr.Proxy = nil
	wrapped := WrapRoundTripper(tr, TracingOptions{
		Threshold: 1 * time.Millisecond,
		Logger:    slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	client := &http.Client{Transport: wrapped, Timeout: 5 * time.Second}

	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/concurrent", nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("concurrent Do failed: %v", err)
				return
			}
			_, _, _ = ReadLimit(resp.Body, resp.ContentLength, 1024)
			DrainAndClose(resp.Body)
		})
	}
	wg.Wait()
}

func TestDrainAndCloseReusesHTTP1KeepAlive(t *testing.T) {
	var reusedCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("not-read-by-caller-", 64)))
	}))
	defer srv.Close()

	tr := NewTransport()
	tr.Proxy = nil
	wrapped := WrapRoundTripper(tr, TracingOptions{
		Threshold: time.Nanosecond,
		Logger:    slog.New(slog.NewJSONHandler(io.Discard, nil)),
		OnSlow: func(s TraceStats) {
			if s.Reused {
				reusedCount.Add(1)
			}
		},
	})
	client := &http.Client{Transport: wrapped, Timeout: 5 * time.Second}

	for range 5 {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/err", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		// Simulate caller returning early on non-200 without reading body.
		DrainAndClose(resp.Body)
	}
	if reusedCount.Load() < 4 {
		t.Fatalf("expected >= 4 reused connections across 5 sequential requests, got %d", reusedCount.Load())
	}
}

func TestReadLimitAndPreallocation(t *testing.T) {
	payload := bytes.Repeat([]byte("abc123"), 4096)

	// Exact match with known contentLength.
	got, exceeded, err := ReadLimit(bytes.NewReader(payload), int64(len(payload)), int64(len(payload)))
	if err != nil || exceeded || !bytes.Equal(got, payload) {
		t.Fatalf("exact read failed: len=%d exceeded=%v err=%v", len(got), exceeded, err)
	}

	// Fast rejection when Content-Length header already exceeds limit.
	got, exceeded, err = ReadLimit(bytes.NewReader(payload), int64(len(payload)+10), int64(len(payload)))
	if err != nil || !exceeded || len(got) != 0 {
		t.Fatalf("header limit check failed: len=%d exceeded=%v err=%v", len(got), exceeded, err)
	}

	// Streaming unknown Content-Length (-1) exceeding limit stops at limit+1.
	got, exceeded, err = ReadLimit(bytes.NewReader(payload), -1, 100)
	if err != nil || !exceeded || len(got) != 100 {
		t.Fatalf("unknown length exceeded failed: len=%d exceeded=%v err=%v", len(got), exceeded, err)
	}
}

func TestSanitizeURL(t *testing.T) {
	u, _ := url.Parse("https://user:pass@api.example.com:8443/v1/items?token=abc&k=v#section")
	if got := SanitizeURL(u); got != "https://api.example.com:8443/v1/items" {
		t.Fatalf("SanitizeURL = %q", got)
	}
	if got := SanitizeURL(nil); got != "" {
		t.Fatalf("SanitizeURL(nil) = %q", got)
	}
}
