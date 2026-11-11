package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInjectsHeadersAndRange(t *testing.T) {
	var gotUA, gotRef, gotRange string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotRef = r.Header.Get("Referer")
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("moov"))
	}))
	defer up.Close()

	p, err := Start(map[string]string{"User-Agent": "TestUA", "Referer": "https://fixture.example/"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	req, _ := http.NewRequest(http.MethodGet, p.Wrap(up.URL+"/a.mp4"), nil)
	req.Header.Set("Range", "bytes=0-3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(body) != "moov" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if gotUA != "TestUA" || gotRef != "https://fixture.example/" || gotRange != "bytes=0-3" {
		t.Fatalf("headers ua=%q ref=%q range=%q", gotUA, gotRef, gotRange)
	}
	if !strings.HasPrefix(p.Base(), "http://127.0.0.1:") {
		t.Fatalf("base %s", p.Base())
	}

	p.SetHeaders(map[string]string{"User-Agent": "Other"})
	if p.Header("User-Agent") != "Other" || p.Header("Referer") != "" {
		t.Fatalf("headers not replaced")
	}
}

func TestRewritePlaylist(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://fixture.example/" {
			t.Errorf("referer %q", r.Header.Get("Referer"))
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, strings.Join([]string{
			"#EXTM3U",
			`#EXT-X-KEY:METHOD=AES-128,URI="key.key"`,
			"#EXTINF:4,",
			"seg0.ts",
			"https://cdn.example/a.ts",
			"#EXT-X-STREAM-INF:BANDWIDTH=1",
			"sub.m3u8",
		}, "\n"))
	}))
	defer up.Close()

	p, err := Start(map[string]string{"Referer": "https://fixture.example/"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := http.Get(p.Wrap(up.URL + "/live.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if strings.Contains(text, "\nseg0.ts") || strings.Contains(text, `URI="key.key"`) {
		t.Fatalf("not rewritten:\n%s", text)
	}
	if !strings.Contains(text, p.Base()+"/u?url=") {
		t.Fatalf("proxy base missing:\n%s", text)
	}
	if !strings.Contains(text, "cdn.example") {
		t.Fatalf("absolute segment missing:\n%s", text)
	}
}

func TestRejectsNonHTTP(t *testing.T) {
	p, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	resp, err := http.Get(p.Wrap("file:///tmp/a.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRedirectedPlaylistUsesFinalDirectoryAndFullBody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/entry.m3u8":
			http.Redirect(w, r, "/cdn/master.m3u8", http.StatusFound)
		case "/cdn/master.m3u8":
			if r.Header.Get("Range") != "" {
				t.Error("playlist range was forwarded")
			}
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nsub/media.m3u8\n")
		case "/cdn/sub/media.m3u8":
			io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nseg.ts\n#EXT-X-ENDLIST\n")
		case "/cdn/sub/seg.ts":
			io.WriteString(w, "segment")
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	p, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	raw := p.Wrap(up.URL + "/entry.m3u8")
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest("GET", raw, nil)
		req.Header.Set("Range", "bytes=0-1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("step %d HTTP %d: %s", i, resp.StatusCode, body)
		}
		if i == 2 {
			if string(body) != "segment" {
				t.Fatalf("segment=%q", body)
			}
			break
		}
		if resp.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" {
			t.Fatal(resp.Header)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				raw = line
				break
			}
		}
	}
}

func TestExtensionlessPartialPlaylistIsFetchedWhole(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := "#EXTM3U\n#EXTINF:4,\nsegment.ts\n#EXT-X-ENDLIST\n"
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.Header.Get("Range") != "" {
			w.WriteHeader(206)
			io.WriteString(w, body[:10])
			return
		}
		io.WriteString(w, body)
	}))
	defer up.Close()
	p, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	req, _ := http.NewRequest("GET", p.Wrap(up.URL+"/stream"), nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "#EXT-X-ENDLIST") || !strings.Contains(string(body), p.Wrap(up.URL+"/segment.ts")) {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
}
