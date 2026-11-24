//go:build darwin

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/ui"
)

// TestNativeMPVPlaysMKVWithTracksAndProxiedStream verifies real mygo-player
// (libmpv + VideoToolbox + zero-copy Metal) playback on macOS for both:
//  1. A local multi-audio + embedded-subtitle MKV file (audio & subtitle switching)
//  2. A proxied HTTP HEVC MKV stream with custom Cookie headers
func TestNativeMPVPlaysMKVWithTracksAndProxiedStream(t *testing.T) {
	if os.Getenv("XIAOMU_NATIVE_MPV_TEST") == "" {
		t.Skip("set XIAOMU_NATIVE_MPV_TEST=1 to run real macOS mygo-player MKV verification")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	dir := t.TempDir()
	srtPath := filepath.Join(dir, "subs.srt")
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:05,000\n你好小幕\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Multi-audio (AAC + AC-3) + embedded SRT H.264 MKV for local file playback.
	localMKV := filepath.Join(dir, "multi_track.mkv")
	cmd := exec.Command(ffmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=4",
		"-i", srtPath,
		"-map", "0:v:0", "-map", "1:a:0", "-map", "2:a:0", "-map", "3:s:0",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-g", "24",
		"-c:a:0", "aac", "-b:a:0", "96k",
		"-c:a:1", "ac3", "-b:a:1", "192k",
		"-c:s", "srt",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:0", "title=English",
		"-metadata:s:a:1", "language=zho", "-metadata:s:a:1", "title=中文配音",
		"-metadata:s:s:0", "language=zho", "-metadata:s:s:0", "title=简体中文",
		localMKV,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg local multi-track mkv failed: %v\n%s", err, string(out))
	}

	t.Run("local_mkv_tracks_and_subtitles", func(t *testing.T) {
		a := &app{preferences: playerPreferences{Rate: 1, Volume: 0}}
		defer a.close()

		if !a.openLocalFile(localMKV) || a.player == nil {
			t.Fatalf("openLocalFile MKV failed: status=%q", a.status)
		}
		p := a.player
		p.SetMuted(true)
		mpvP, ok := p.(*mpvMediaPlayer)
		if !ok {
			t.Fatalf("expected *mpvMediaPlayer on darwin, got %T", p)
		}

		tt := ui.NewTester(func(c *ui.Context) { p.View(c).Size(320, 180) }, 320, 180)
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			if err := p.Err(); err != nil {
				t.Fatalf("mpv local MKV error: %v", err)
			}
			w, h := p.Size()
			if p.Ready() && w == 320 && h == 180 && p.Position() >= 500*time.Millisecond && len(mpvP.AudioTracks()) == 2 && len(mpvP.SubtitleTracks()) == 1 {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		w, h := p.Size()
		audios := mpvP.AudioTracks()
		subs := mpvP.SubtitleTracks()
		if !p.Ready() || w != 320 || h != 180 || len(audios) != 2 || len(subs) != 1 {
			t.Fatalf("local MKV not ready/tracks incomplete: ready=%v size=%dx%d pos=%s audios=%+v subs=%+v err=%v",
				p.Ready(), w, h, p.Position(), audios, subs, p.Err())
		}
		if err := mpvP.SelectAudioTrack(audios[1].ID); err != nil {
			t.Fatalf("SelectAudioTrack failed: %v", err)
		}
		if err := mpvP.DisableSubtitleTrack(); err != nil {
			t.Fatalf("DisableSubtitleTrack failed: %v", err)
		}
		if err := mpvP.SelectSubtitleTrack(subs[0].ID); err != nil {
			t.Fatalf("SelectSubtitleTrack failed: %v", err)
		}
		t.Logf("mygo-player decoded local MKV %dx%d pos=%s dur=%s frames=%d hwdec=%q audios=%d subs=%d",
			w, h, p.Position(), p.Duration(), mpvP.p.FramesRendered(), mpvP.CurrentHWDec(), len(audios), len(subs))
	})

	// 2. HEVC MKV over authenticated HTTP proxy session (models 4K cloud-drive original MKV).
	hevcMKV := filepath.Join(dir, "hevc_cloud.mkv")
	cmd2 := exec.Command(ffmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=520:sample_rate=48000:duration=4",
		"-c:v", "libx265", "-x265-params", "log-level=error", "-pix_fmt", "yuv420p", "-g", "24",
		"-c:a", "aac", "-b:a", "96k",
		hevcMKV,
	)
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg hevc mkv failed: %v\n%s", err, string(out))
	}

	t.Run("proxied_http_hevc_mkv", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Cookie") != "pan_token=ok" {
				http.Error(w, "missing cookie", http.StatusForbidden)
				return
			}
			http.ServeFile(w, r, hevcMKV)
		}))
		defer srv.Close()

		a := &app{preferences: playerPreferences{Rate: 1, Volume: 0}}
		defer a.close()

		ok := a.openPlaybackStream(catalog.Stream{
			Label:          "原画 MKV",
			Quality:        "original",
			URL:            srv.URL + "/hevc_cloud.mkv",
			Headers:        map[string]string{"Cookie": "pan_token=ok"},
			Filename:       "hevc_cloud.mkv",
			NativePlayable: true,
			IsOriginal:     true,
		}, false)
		if !ok || a.player == nil {
			t.Fatalf("openPlaybackStream HEVC MKV failed: status=%q", a.status)
		}
		p := a.player
		p.SetMuted(true)
		mpvP, isMPV := p.(*mpvMediaPlayer)
		if !isMPV {
			t.Fatalf("expected *mpvMediaPlayer on darwin, got %T", p)
		}

		tt := ui.NewTester(func(c *ui.Context) { p.View(c).Size(320, 180) }, 320, 180)
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			if err := p.Err(); err != nil {
				t.Fatalf("mpv proxied HEVC MKV error: %v", err)
			}
			w, h := p.Size()
			if p.Ready() && w == 320 && h == 180 && p.Position() >= 500*time.Millisecond && a.proxy.BytesServed() > 0 {
				t.Logf("mygo-player decoded proxied HEVC MKV %dx%d pos=%s dur=%s bytes=%d frames=%d hwdec=%q",
					w, h, p.Position(), p.Duration(), a.proxy.BytesServed(), mpvP.p.FramesRendered(), mpvP.CurrentHWDec())
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		w, h := p.Size()
		t.Fatalf("proxied HEVC MKV did not advance: ready=%v size=%dx%d pos=%s bytes=%d err=%v",
			p.Ready(), w, h, p.Position(), a.proxy.BytesServed(), p.Err())
	})
}
