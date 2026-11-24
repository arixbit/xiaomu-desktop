package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A live stream without Content-Length must reach the player chunk by
// chunk while upstream is still sending, not wait in the write buffer.
func TestProxyFlushesLiveStreamChunks(t *testing.T) {
	// Longer than the proxy's 64-byte playlist sniff, like a real FLV
	// header plus metadata tag.
	chunk := "FLV\x01\x05\x00\x00\x00\x09" + strings.Repeat("live-chunk", 16)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		io.WriteString(w, chunk)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	p, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	got := make(chan string, 1)
	go func() {
		resp, err := http.Get(p.Wrap(srv.URL + "/live.flv"))
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, len(chunk))
		n, err := io.ReadFull(resp.Body, buf)
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		got <- string(buf[:n])
	}()
	select {
	case s := <-got:
		if s != chunk {
			t.Fatalf("first chunk = %q, want %q", s, chunk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live chunk was not flushed while upstream was still streaming")
	}
}
