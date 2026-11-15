package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyTimesOutHeadersAndStalledBody(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "body"}[headers], func(t *testing.T) {
			canceled := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if headers {
					w.Header().Set("Content-Type", "video/mp4")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
				close(canceled)
			}))
			defer srv.Close()
			p, err := Start(nil)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			p.readTimeout = 100 * time.Millisecond
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(p.Wrap(srv.URL + "/video.mp4"))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), errUpstreamTimeout.Error()) {
				t.Fatalf("timeout response: %d %s", resp.StatusCode, body)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("upstream request leaked after timeout")
			}
		})
	}
}

func TestProxyTimeoutDoesNotLimitStreamDuration(t *testing.T) {
	const chunks = 12
	chunk := strings.Repeat("video", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		for range chunks {
			io.WriteString(w, chunk)
			w.(http.Flusher).Flush()
			time.Sleep(25 * time.Millisecond)
		}
	}))
	defer srv.Close()
	p, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.readTimeout = 100 * time.Millisecond
	resp, err := http.Get(p.Wrap(srv.URL + "/video.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != strings.Repeat(chunk, chunks) {
		t.Fatalf("continuous stream was cut off: len=%d err=%v", len(body), err)
	}
}
