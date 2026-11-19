package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/ui"
)

// Run with XIAOMU_NATIVE_MEDIA_DIR pointing to a generated short HLS stream.
// Overstating its final segment models streams whose last decoded timestamp
// falls short of the manifest duration, without using any external media.
func TestNativeAutoNextAtHLSEnd(t *testing.T) {
	dir := os.Getenv("XIAOMU_NATIVE_MEDIA_DIR")
	if dir == "" {
		t.Skip("set XIAOMU_NATIVE_MEDIA_DIR to a short generated stream.m3u8 fixture")
	}
	body, err := os.ReadFile(filepath.Join(dir, "stream.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	last, target := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, "#EXTINF:") {
			last = i
		}
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			target = i
		}
	}
	if last < 0 || target < 0 || !strings.Contains(string(body), "#EXT-X-ENDLIST") {
		t.Fatal("expected a complete HLS media playlist")
	}
	duration, _, _ := strings.Cut(strings.TrimPrefix(lines[last], "#EXTINF:"), ",")
	seconds, err := strconv.ParseFloat(duration, 64)
	if err != nil {
		t.Fatal(err)
	}
	lines[last] = fmt.Sprintf("#EXTINF:%.6f,", seconds+0.1)
	oldTarget, err := strconv.Atoi(strings.TrimPrefix(lines[target], "#EXT-X-TARGETDURATION:"))
	if err != nil {
		t.Fatal(err)
	}
	lines[target] = fmt.Sprintf("#EXT-X-TARGETDURATION:%d", max(oldTarget, int(math.Ceil(seconds+0.1))))
	files := http.FileServer(http.Dir(dir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/end-gap.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, strings.Join(lines, "\n"))
			return
		}
		files.ServeHTTP(w, r)
	}))
	defer srv.Close()
	a := &app{autoNext: true, preferences: playerPreferences{Rate: 1, Volume: 0}}
	defer a.close()
	a.title = &titlePage{name: "本地自动切集验证", failed: map[string]string{}, detail: &catalog.Detail{Sources: []catalog.Source{{Name: "本地线路", Episodes: []catalog.Episode{
		{Name: "第1集", ID: srv.URL + "/end-gap.m3u8"},
		{Name: "第2集", ID: srv.URL + "/stream.m3u8"},
	}}}}}
	a.title.session = &recoverySession{play: func(_ context.Context, _ string, e catalog.Episode) (catalog.Playback, error) {
		return catalog.Playback{URL: e.ID}, nil
	}}
	a.playEpisode("本地线路", a.title.detail.Sources[0].Episodes[0])
	if a.player == nil {
		t.Fatal(a.status)
	}
	p := a.player
	tt := ui.NewTester(a.view, 1120, 760)
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	lastFrame := time.Now()
	for a.playingEpisode != "第2集" {
		select {
		case <-deadline.C:
			t.Fatalf("native player did not advance: position=%s duration=%s ended=%v playing=%v buffering=%v failure=%v", p.Position(), p.Duration(), p.Ended(), p.Playing(), p.Buffering(), a.playFailure)
		case <-tick.C:
			if err := p.Err(); err != nil {
				t.Fatal(err)
			}
			// The native media clock runs in real time; keep the Tester's
			// otherwise fixed UI clock in step for the end settling period.
			now := time.Now()
			tt.Advance(now.Sub(lastFrame))
			lastFrame = now
		}
	}
	if a.player == p || len(a.history.entries) != 1 || a.history.entries[0].Episode != "第1集" || !a.history.entries[0].finished() {
		t.Fatalf("automatic transition lost episode completion: %+v", a.history.entries)
	}
	for !a.player.Ready() || a.player.Position() < 100*time.Millisecond {
		select {
		case <-deadline.C:
			t.Fatal("next episode opened but did not begin playing")
		case <-tick.C:
			if err := a.player.Err(); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			tt.Advance(now.Sub(lastFrame))
			lastFrame = now
		}
	}
	t.Log("AVPlayer completed the short HLS episode and played episode 2 automatically")
}
