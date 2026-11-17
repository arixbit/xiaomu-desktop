package main

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

// This opt-in test uses generated, silent h264.mp4 and hevc.mp4 files. It
// exercises the real system decoder and loopback proxy, not a mocked player.
func TestNativeVODRecoveryAndPreferences(t *testing.T) {
	dir := os.Getenv("XIAOMU_NATIVE_MEDIA_DIR")
	if dir == "" {
		t.Skip("set XIAOMU_NATIVE_MEDIA_DIR to generated h264.mp4 and hevc.mp4 fixtures")
	}
	for _, name := range []string{"h264.mp4", "hevc.mp4"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	files := http.FileServer(http.Dir(dir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/waiting.mp4" {
			select {
			case <-r.Context().Done():
			case <-stop:
			}
			return
		}
		if r.Header.Get("User-Agent") != "VODFixture" || r.Header.Get("Referer") != "https://example.com/" {
			t.Error("lost media headers while switching lines")
			http.Error(w, "missing headers", http.StatusForbidden)
			return
		}
		files.ServeHTTP(w, r)
	}))
	defer srv.Close()
	defer close(stop)
	a := &app{preferences: playerPreferences{Volume: 0.2, Rate: 1.5}}
	defer a.close()
	a.title = &titlePage{name: "本地实播验证", detail: &catalog.Detail{}}
	for _, name := range []string{"waiting", "h264", "hevc"} {
		a.title.detail.Sources = append(a.title.detail.Sources, catalog.Source{Name: name, Episodes: []catalog.Episode{{Name: "第1集", ID: srv.URL + "/" + name + ".mp4"}}})
	}
	a.title.session = &recoverySession{play: func(_ context.Context, _ string, e catalog.Episode) (catalog.Playback, error) {
		return catalog.Playback{URL: e.ID, Headers: map[string]string{"User-Agent": "VODFixture", "Referer": "https://example.com/"}}, nil
	}}
	a.playEpisodeAt("waiting", a.title.detail.Sources[0].Episodes[0], 4*time.Second)
	if a.player == nil {
		t.Fatal(a.status)
	}
	a.player.SetMuted(true)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Advance(playbackWaitLimit + time.Second)
	if a.playFailure == nil || a.playFailure.at != 4*time.Second || a.player.Playing() {
		t.Fatal("waiting native player did not expose recovery and keep its pending position")
	}
	if err := tt.Click("换下一条线路"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"h264", "hevc"} {
		if a.playingSource != name || a.player == nil {
			t.Fatalf("expected %s, got %s: %s", name, a.playingSource, a.status)
		}
		p := a.player
		p.SetMuted(true)
		if p.Rate() != 1.5 || p.Volume() != 0.2 {
			t.Fatalf("%s lost preferences: rate=%g volume=%g", name, p.Rate(), p.Volume())
		}
		deadline := time.Now().Add(15 * time.Second)
		var first [32]byte
		ready, changed := false, false
		for time.Now().Before(deadline) {
			tt.Frame()
			if err := p.Err(); err != nil {
				t.Fatal(err)
			}
			if p.Ready() {
				if w, h := p.Size(); w != 160 || h != 90 {
					t.Fatalf("%s frame size %dx%d", name, w, h)
				}
				// Sample only the picture, excluding time labels and controls.
				img := tt.Image()
				var pixels []byte
				for y := 300; y < 420; y++ {
					for x := 250; x < 450; x++ {
						i := img.PixOffset(x, y)
						pixels = append(pixels, img.Pix[i:i+4]...)
					}
				}
				hash := sha256.Sum256(pixels)
				if !ready {
					first, ready = hash, true
				} else if hash != first {
					changed = true
				}
				if changed && p.Position() >= 5*time.Second && a.resumeAt == 0 {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		if !ready || !changed || p.Position() < 5*time.Second || a.resumeAt != 0 {
			t.Fatalf("%s did not decode and resume: ready=%v changed=%v position=%s resume=%s", name, ready, changed, p.Position(), a.resumeAt)
		}
		t.Logf("%s: changing 160x90 frames; resumed at %s; rate=%g volume=%g; headers preserved", name, p.Position(), p.Rate(), p.Volume())
		if name == "h264" {
			p.Pause()
			p.Seek(4 * time.Second)
			a.lastPosition = 4 * time.Second
			a.playNextLine()
		}
	}
}

func TestNativeSeriesSkipAndSourceMemory(t *testing.T) {
	dir := os.Getenv("XIAOMU_NATIVE_MEDIA_DIR")
	if dir == "" {
		t.Skip("set XIAOMU_NATIVE_MEDIA_DIR to generated h264.mp4 fixture")
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()
	doc := &config.Config{URL: "https://example.com/config"}
	item := library.Item{Entry: library.Entry{Config: doc, Site: config.Site{Key: "local"}}, Video: catalog.Video{ID: "fixture", Name: "本地跳过验证"}}
	a := &app{autoNext: true, preferences: playerPreferences{Rate: 1, Volume: 0}}
	defer a.close()
	a.sourceMemory.path = filepath.Join(t.TempDir(), "source-playback.json")
	a.title = &titlePage{name: item.Video.Name, item: item, failed: map[string]string{}, detail: &catalog.Detail{Sources: []catalog.Source{{Name: "本地线路", Episodes: []catalog.Episode{
		{Name: "第1集", ID: srv.URL + "/h264.mp4"},
		{Name: "第2集", ID: srv.URL + "/h264.mp4"},
	}}}}}
	a.title.session = &recoverySession{play: func(_ context.Context, _ string, e catalog.Episode) (catalog.Playback, error) {
		return catalog.Playback{URL: e.ID}, nil
	}}
	_ = a.seriesSkips.set(a.title.name, seriesSkipRule{Enabled: true, Opening: 2, Ending: 2})
	a.playEpisode("本地线路", a.title.detail.Sources[0].Episodes[0])
	if a.player == nil {
		t.Fatal(a.status)
	}
	p := a.player
	tt := ui.NewTester(a.view, 1120, 760)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if err := p.Err(); err != nil {
			t.Fatal(err)
		}
		if p.Position() >= 2*time.Second && a.sourceAttempt.succeeded && a.resumeAt == 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if p.Position() < 2*time.Second || !a.sourceAttempt.succeeded || a.resumeAt != 0 {
		t.Fatalf("native opening/actual playback: position=%s success=%v resume=%s", p.Position(), a.sourceAttempt.succeeded, a.resumeAt)
	}
	p.Seek(p.Duration() - time.Second)
	deadline = time.Now().Add(5 * time.Second)
	for a.playingEpisode != "第2集" && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(25 * time.Millisecond)
	}
	if a.playingEpisode != "第2集" || len(a.history.entries) != 1 || a.history.entries[0].Episode != "第1集" || !a.history.entries[0].finished() {
		t.Fatalf("native credits did not advance with completion: episode=%s history=%+v", a.playingEpisode, a.history.entries)
	}
	entries, err := readSourceMemory(a.sourceMemory.path)
	if err != nil || !entries[titleKey(item.Video.Name)].Preferred.matches(item) {
		t.Fatalf("native playback source not saved: %+v, %v", entries, err)
	}
	data, err := os.ReadFile(a.sourceMemory.path)
	if err != nil || strings.Contains(string(data), "h264.mp4") {
		t.Fatal("source memory persisted media URL")
	}
	t.Log("AVPlayer skipped opening, recorded actual playback, skipped credits and started episode 2")
}
