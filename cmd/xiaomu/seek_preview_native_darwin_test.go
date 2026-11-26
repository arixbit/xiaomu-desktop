//go:build darwin

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/egoist/mygo/ui"
)

// generateThreeSceneVideo creates a 9-second 640x360 H.264 MP4/MKV containing
// three unmistakably distinct 3-second visual scenes so native seek-preview
// tests can verify real frame extraction pixel-by-pixel.
func generateThreeSceneVideo(t *testing.T, ffmpegPath, outPath string, c1, c2, c3 string) {
	t.Helper()
	// Each 3-second segment has a distinct saturated background plus a bright
	// contrasting stripe/band at a different vertical position so screenshots
	// are immediately distinguishable both visually and programmatically.
	filter := fmt.Sprintf(
		"color=c=%s:s=640x360:r=24:d=3,drawbox=x=40:y=40:w=560:h=48:color=white@0.85:t=fill[v0];"+
			"color=c=%s:s=640x360:r=24:d=3,drawbox=x=40:y=156:w=560:h=48:color=white@0.85:t=fill[v1];"+
			"color=c=%s:s=640x360:r=24:d=3,drawbox=x=40:y=272:w=560:h=48:color=white@0.85:t=fill[v2];"+
			"[v0][v1][v2]concat=n=3:v=1:a=0[v]",
		c1, c2, c3,
	)
	cmd := exec.Command(ffmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-filter_complex", filter,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=9",
		"-map", "[v]", "-map", "0:a:0",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-g", "24", "-keyint_min", "24",
		"-c:a", "aac", "-b:a", "64k",
		"-shortest",
		outPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg 3-scene fixture failed: %v\n%s", err, string(out))
	}
}

func sampleRegionAvgRGB(img *image.RGBA, r image.Rectangle) (rAvg, gAvg, bAvg float64) {
	r = r.Intersect(img.Bounds())
	if r.Empty() {
		return 0, 0, 0
	}
	var sr, sg, sb, n float64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			sr += float64(c.R)
			sg += float64(c.G)
			sb += float64(c.B)
			n++
		}
	}
	if n == 0 {
		return 0, 0, 0
	}
	return sr / n, sg / n, sb / n
}

func savePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func cropImage(img *image.RGBA, r ui.Rect) *image.RGBA {
	b := image.Rect(int(r.X), int(r.Y), int(r.X+r.W), int(r.Y+r.H)).Intersect(img.Bounds())
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.SetRGBA(x, y, img.RGBAAt(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

func attachNativePreviewDanmaku(a *app, dur time.Duration) {
	a.preferences.Danmaku.Enabled = true
	comments := []danmaku.Comment{
		{Time: 1 * time.Second, Mode: 1, Color: 0xffffff, Text: "红场开篇"},
		{Time: 2 * time.Second, Mode: 1, Color: 0xffffff, Text: "准备切镜"},
		{Time: 4 * time.Second, Mode: 1, Color: 0xffffff, Text: "绿幕高能"},
		{Time: 4500 * time.Millisecond, Mode: 1, Color: 0xffffff, Text: "高能名场面"},
		{Time: 5 * time.Second, Mode: 1, Color: 0xffffff, Text: "太清晰了"},
		{Time: 7 * time.Second, Mode: 1, Color: 0xffffff, Text: "蓝海终章"},
		{Time: 7500 * time.Millisecond, Mode: 1, Color: 0xffffff, Text: "结尾收束"},
	}
	a.applyDanmaku(danmaku.Episode{ID: "native-demo", Anime: "实测样例", Name: "第1集"}, comments)
}

// TestNativeMPVSeekHoverPreviewRealFrames verifies real mygo-player v0.5.2
// seek-hover thumbnail decoding on macOS across:
//  1. Local 3-scene video (Red / Green / Blue) with danmaku curve + click commit
//  2. Proxied HTTP 3-scene MKV with custom Cookie & Referer headers
//  3. Rapid sweep & media switch to a second 3-scene video (Yellow / Magenta / Cyan)
func TestNativeMPVSeekHoverPreviewRealFrames(t *testing.T) {
	if os.Getenv("XIAOMU_NATIVE_MPV_TEST") == "" {
		t.Skip("set XIAOMU_NATIVE_MPV_TEST=1 to run real macOS mygo-player seek-preview verification")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	dir := t.TempDir()
	videoRGB := filepath.Join(dir, "three_scenes_rgb.mp4")
	generateThreeSceneVideo(t, ffmpegPath, videoRGB, "#cc2936", "#1f9d55", "#2563eb")

	videoMKV := filepath.Join(dir, "three_scenes_proxied.mkv")
	generateThreeSceneVideo(t, ffmpegPath, videoMKV, "#cc2936", "#1f9d55", "#2563eb")

	videoYMC := filepath.Join(dir, "three_scenes_ymc.mp4")
	generateThreeSceneVideo(t, ffmpegPath, videoYMC, "#eab308", "#c026d3", "#06b6d4")

	reviewDir := os.Getenv("XIAOMU_SEEK_PREVIEW_REVIEW_DIR")

	waitMainReady := func(t *testing.T, tt *ui.Tester, a *app, mpvP *mpvMediaPlayer) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			if err := a.player.Err(); err != nil {
				t.Fatalf("main player error: %v", err)
			}
			w, h := a.player.Size()
			if a.player.Ready() && a.canSeekPlayer(a.player) && w == 640 && h == 360 && a.player.Duration() >= 8*time.Second && mpvP.p.FramesRendered() > 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("main player did not become ready: ready=%v seekable=%v dur=%s frames=%d err=%v",
			a.player.Ready(), a.canSeekPlayer(a.player), a.player.Duration(), mpvP.p.FramesRendered(), a.player.Err())
	}

	waitHoverFrame := func(t *testing.T, tt *ui.Tester, a *app, target, dur time.Duration) ui.Rect {
		t.Helper()
		wantBucket := seekPreviewBucket(target, dur)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			a.seekPreview.mu.Lock()
			ready := a.seekPreview.readyGen == a.seekPreview.gen &&
				a.seekPreview.readyBucket == wantBucket &&
				a.seekPreview.readySource != nil
			failed := a.seekPreview.failed
			a.seekPreview.mu.Unlock()
			if failed {
				t.Fatalf("real seek preview failed for target=%s", target)
			}
			if ready {
				tt.Frame()
				if r, ok := tt.Find("进度预览画面"); ok {
					return r
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("real seek preview timed out waiting for target=%s", target)
		return ui.Rect{}
	}

	// 1. Local 3-scene MP4: verify 3 real frames (Red / Green / Blue), main
	// stays on Scene 1 (Red), 0 seeks during hover, 1 seek on click.
	t.Run("local_three_scenes_and_evidence", func(t *testing.T) {
		a := &app{preferences: playerPreferences{Rate: 1, Volume: 0}}
		defer a.close()

		if !a.openLocalFile(videoRGB) || a.player == nil {
			t.Fatalf("openLocalFile failed: %s", a.status)
		}
		mpvP, ok := a.player.(*mpvMediaPlayer)
		if !ok {
			t.Fatalf("expected *mpvMediaPlayer, got %T", a.player)
		}
		mpvP.SetMuted(true)
		spy := &spySeekPlayer{mediaPlayer: mpvP}
		a.player = spy

		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		waitMainReady(t, tt, a, mpvP)

		// Pause on Scene 1 (~0.8s) so main picture stays firmly on Red while we
		// hover across Red (1.5s), Green (4.5s), Blue (7.5s).
		mpvP.Pause()
		mpvP.Seek(800 * time.Millisecond)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			if absDuration(mpvP.Position()-800*time.Millisecond) < 400*time.Millisecond && mpvP.p.FramesRendered() > 1 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		spy.resetCounts()
		attachNativePreviewDanmaku(a, spy.Duration())
		tt.Frame()

		dur := spy.Duration()
		axis, ok := tt.Find("播放进度")
		if !ok {
			t.Fatal("missing 播放进度")
		}
		videoRect, ok := tt.Find("视频画面")
		if !ok {
			t.Fatal("missing 视频画面")
		}
		trackY := axis.Y + axis.H - 10

		type sceneCheck struct {
			tag      string
			frac     float32
			dominant string
		}
		scenes := []sceneCheck{
			{"t1-red", 1.5 / 9.0, "red"},
			{"t2-green", 4.5 / 9.0, "green"},
			{"t3-blue", 7.5 / 9.0, "blue"},
		}
		for _, sc := range scenes {
			x := axis.X + axis.W*sc.frac
			wantT := seekPreviewTimeForX(x-axis.X, axis.W, dur)
			tt.Move(x, trackY)
			frameRect := waitHoverFrame(t, tt, a, wantT, dur)
			img := tt.Image()

			// Sample central band of thumbnail (avoiding white stripe & bottom timestamp pill).
			sampleBox := image.Rect(
				int(frameRect.X+frameRect.W*0.25),
				int(frameRect.Y+frameRect.H*0.28),
				int(frameRect.X+frameRect.W*0.75),
				int(frameRect.Y+frameRect.H*0.62),
			)
			if sc.dominant == "green" {
				// Green has the white bar in the middle, so sample upper band.
				sampleBox = image.Rect(
					int(frameRect.X+frameRect.W*0.20),
					int(frameRect.Y+frameRect.H*0.12),
					int(frameRect.X+frameRect.W*0.80),
					int(frameRect.Y+frameRect.H*0.34),
				)
			}
			pr, pg, pb := sampleRegionAvgRGB(img, sampleBox)

			// Verify main video canvas is STILL Red (Scene 1)!
			mainBox := image.Rect(
				int(videoRect.X+videoRect.W*0.35),
				int(videoRect.Y+videoRect.H*0.35),
				int(videoRect.X+videoRect.W*0.65),
				int(videoRect.Y+videoRect.H*0.55),
			)
			mr, mg, mb := sampleRegionAvgRGB(img, mainBox)
			t.Logf("local %s target=%s previewRGB=(%.1f,%.1f,%.1f) mainRGB=(%.1f,%.1f,%.1f) mainPos=%s seeks=%d",
				sc.tag, wantT, pr, pg, pb, mr, mg, mb, spy.Position(), len(spy.seekSnapshot()))

			if mr < 140 || mr < mg+60 || mr < mb+60 {
				t.Fatalf("local %s: main picture drifted away from Scene 1 Red: mainRGB=(%.1f,%.1f,%.1f)", sc.tag, mr, mg, mb)
			}
			switch sc.dominant {
			case "red":
				if pr < 140 || pr < pg+60 || pr < pb+60 {
					t.Fatalf("local %s: expected Red preview, got RGB=(%.1f,%.1f,%.1f)", sc.tag, pr, pg, pb)
				}
			case "green":
				if pg < 110 || pg < pr+45 || pg < pb+20 {
					t.Fatalf("local %s: expected Green preview, got RGB=(%.1f,%.1f,%.1f)", sc.tag, pr, pg, pb)
				}
			case "blue":
				if pb < 140 || pb < pr+60 || pb < pg+40 {
					t.Fatalf("local %s: expected Blue preview, got RGB=(%.1f,%.1f,%.1f)", sc.tag, pr, pg, pb)
				}
			}

			if reviewDir != "" {
				savePNG(t, filepath.Join(reviewDir, fmt.Sprintf("local-1280x720-%s.png", sc.tag)), img)
				if card, ok := tt.Find("进度预览"); ok {
					savePNG(t, filepath.Join(reviewDir, fmt.Sprintf("local-thumb-%s.png", sc.tag)), cropImage(img, card))
				}
			}
		}

		// Still 0 seeks after hovering all 3 scenes!
		if seeks := spy.seekSnapshot(); len(seeks) != 0 {
			t.Fatalf("hovering 3 real scenes caused main seeks: %v", seeks)
		}

		// Also capture no-curve, 640x420 small window, and fullscreen hover evidence if requested.
		if reviewDir != "" {
			// 1280x720 with danmaku curve disabled at Scene 2 Green.
			a.preferences.Danmaku.Enabled = false
			tt.Frame()
			axisNoCurve, _ := tt.Find("播放进度")
			xNC := axisNoCurve.X + axisNoCurve.W*(4.5/9.0)
			tt.Move(xNC, axisNoCurve.Y+axisNoCurve.H-10)
			waitHoverFrame(t, tt, a, seekPreviewTimeForX(xNC-axisNoCurve.X, axisNoCurve.W, dur), dur)
			savePNG(t, filepath.Join(reviewDir, "local-1280x720-no-curve-t2-green.png"), tt.Image())
			a.preferences.Danmaku.Enabled = true
			tt.Frame()

			// Fullscreen hover at Scene 2 Green.
			a.setFullScreen(true)
			tt.Frame()
			axisFS, _ := tt.Find("播放进度")
			xFS := axisFS.X + axisFS.W*(4.5/9.0)
			tt.Move(xFS, axisFS.Y+axisFS.H-10)
			waitHoverFrame(t, tt, a, seekPreviewTimeForX(xFS-axisFS.X, axisFS.W, dur), dur)
			savePNG(t, filepath.Join(reviewDir, "local-fullscreen-t2-green.png"), tt.Image())
			a.setFullScreen(false)
			tt.Frame()

			// 640x420 small window hover at Scene 3 Blue (near right edge to verify inward clamp).
			tt.SetSize(640, 420)
			tt.Frame()
			axisS, _ := tt.Find("播放进度")
			xS := axisS.X + axisS.W*(8.0/9.0)
			tt.Move(xS, axisS.Y+axisS.H-10)
			waitHoverFrame(t, tt, a, seekPreviewTimeForX(xS-axisS.X, axisS.W, dur), dur)
			savePNG(t, filepath.Join(reviewDir, "local-640x420-t3-blue-edge.png"), tt.Image())
			tt.SetSize(1280, 720)
			tt.Frame()
		}

		// Click Scene 3 Blue (7.5s) -> exactly 1 seek to ~7.5s, stays paused, main video becomes Blue!
		axis, _ = tt.Find("播放进度")
		clickX := axis.X + axis.W*(7.5/9.0)
		wantCommit := seekPreviewTimeForX(clickX-axis.X, axis.W, dur)
		beforeMainFrames := mpvP.p.FramesRendered()
		tt.ClickAt(clickX, axis.Y+axis.H-10)
		seeks := spy.seekSnapshot()
		if len(seeks) != 1 || absDuration(seeks[0]-wantCommit) > 30*time.Millisecond {
			t.Fatalf("after click: seeks=%v want 1 seek at %s", seeks, wantCommit)
		}
		if spy.Playing() {
			t.Fatal("clicking seek bar resumed paused main player")
		}

		// Wait for main player to render Scene 3 Blue!
		deadline = time.Now().Add(5 * time.Second)
		mainTurnedBlue := false
		for time.Now().Before(deadline) {
			tt.Frame()
			if mpvP.p.FramesRendered() > beforeMainFrames && absDuration(mpvP.Position()-wantCommit) < time.Second {
				img := tt.Image()
				mr, mg, mb := sampleRegionAvgRGB(img, image.Rect(
					int(videoRect.X+videoRect.W*0.35),
					int(videoRect.Y+videoRect.H*0.35),
					int(videoRect.X+videoRect.W*0.65),
					int(videoRect.Y+videoRect.H*0.55),
				))
				if mb > 140 && mb > mr+60 && mb > mg+40 {
					mainTurnedBlue = true
					if reviewDir != "" {
						// Move pointer away and let numeric clock transition settle
						// to capture a clean post-seek main picture.
						tt.Move(20, 20)
						tt.Advance(300 * time.Millisecond)
						tt.Frame()
						savePNG(t, filepath.Join(reviewDir, "local-1280x720-after-click-t3-blue.png"), tt.Image())
					}
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !mainTurnedBlue {
			t.Fatalf("main player did not jump to Scene 3 Blue after click: pos=%s", mpvP.Position())
		}
		t.Logf("local click verified: seeks=%d target=%s mainPos=%s paused=%v", len(seeks), seeks[0], mpvP.Position(), !spy.Playing())
	})

	// 2. Proxied HTTP VOD with custom Cookie + Referer headers while main player stays PLAYING.
	t.Run("proxied_http_with_headers_while_playing", func(t *testing.T) {
		var headerRejects atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Cookie") != "seek_token=ok" || r.Header.Get("Referer") != "https://example.com/vod" {
				headerRejects.Add(1)
				http.Error(w, "missing headers", http.StatusForbidden)
				return
			}
			http.ServeFile(w, r, videoMKV)
		}))
		defer srv.Close()

		a := &app{preferences: playerPreferences{Rate: 1, Volume: 0}}
		defer a.close()

		ok := a.openPlaybackStream(catalog.Stream{
			Label:          "原画 MKV",
			Quality:        "original",
			URL:            srv.URL + "/three_scenes_proxied.mkv",
			Headers:        map[string]string{"Cookie": "seek_token=ok", "Referer": "https://example.com/vod"},
			Filename:       "three_scenes_proxied.mkv",
			NativePlayable: true,
			IsOriginal:     true,
		}, false)
		if !ok || a.player == nil {
			t.Fatalf("openPlaybackStream failed: %s", a.status)
		}
		mpvP, ok := a.player.(*mpvMediaPlayer)
		if !ok {
			t.Fatalf("expected *mpvMediaPlayer, got %T", a.player)
		}
		mpvP.SetMuted(true)
		_ = mpvP.p.SetLoop(true)
		spy := &spySeekPlayer{mediaPlayer: mpvP}
		a.player = spy

		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		waitMainReady(t, tt, a, mpvP)
		attachNativePreviewDanmaku(a, spy.Duration())
		tt.Frame()
		spy.resetCounts()

		dur := spy.Duration()
		axis, _ := tt.Find("播放进度")
		trackY := axis.Y + axis.H - 10

		// Hover 1.5s (Red), 4.5s (Green), 7.5s (Blue) while main player keeps playing!
		for _, sc := range []struct {
			tag  string
			frac float32
			ok   func(r, g, b float64) bool
		}{
			{"red", 1.5 / 9.0, func(r, g, b float64) bool { return r > 140 && r > g+60 && r > b+60 }},
			{"green", 4.5 / 9.0, func(r, g, b float64) bool { return g > 110 && g > r+45 && g > b+20 }},
			{"blue", 7.5 / 9.0, func(r, g, b float64) bool { return b > 140 && b > r+60 && b > g+40 }},
		} {
			x := axis.X + axis.W*sc.frac
			wantT := seekPreviewTimeForX(x-axis.X, axis.W, dur)
			tt.Move(x, trackY)
			fr := waitHoverFrame(t, tt, a, wantT, dur)
			img := tt.Image()
			sampleBox := image.Rect(int(fr.X+fr.W*0.25), int(fr.Y+fr.H*0.28), int(fr.X+fr.W*0.75), int(fr.Y+fr.H*0.62))
			if sc.tag == "green" {
				sampleBox = image.Rect(int(fr.X+fr.W*0.20), int(fr.Y+fr.H*0.12), int(fr.X+fr.W*0.80), int(fr.Y+fr.H*0.34))
			}
			pr, pg, pb := sampleRegionAvgRGB(img, sampleBox)
			t.Logf("proxied %s target=%s previewRGB=(%.1f,%.1f,%.1f) mainPos=%s playing=%v",
				sc.tag, wantT, pr, pg, pb, spy.Position(), spy.Playing())
			if !sc.ok(pr, pg, pb) {
				t.Fatalf("proxied %s: wrong preview color RGB=(%.1f,%.1f,%.1f)", sc.tag, pr, pg, pb)
			}
			if reviewDir != "" && sc.tag == "blue" {
				savePNG(t, filepath.Join(reviewDir, "proxied-1280x720-t3-blue.png"), img)
			}
		}

		if headerRejects.Load() != 0 {
			t.Fatalf("upstream rejected %d requests due to missing headers", headerRejects.Load())
		}
		if len(spy.seekSnapshot()) != 0 {
			t.Fatalf("hovering proxied stream caused main seeks: %v", spy.seekSnapshot())
		}
		if !spy.Playing() || spy.pauseCalls != 0 || spy.playCalls != 0 {
			t.Fatalf("hovering disturbed playing main video: playing=%v pauseCalls=%d playCalls=%d", spy.Playing(), spy.pauseCalls, spy.playCalls)
		}

		// Commit click at 7.5s -> 1 seek, remains playing!
		clickX := axis.X + axis.W*(7.5/9.0)
		wantCommit := seekPreviewTimeForX(clickX-axis.X, axis.W, dur)
		tt.ClickAt(clickX, trackY)
		seeks := spy.seekSnapshot()
		if len(seeks) != 1 || absDuration(seeks[0]-wantCommit) > 30*time.Millisecond {
			t.Fatalf("proxied click seeks=%v want 1 seek at %s", seeks, wantCommit)
		}
		if !spy.Playing() {
			t.Fatal("clicking seek bar paused a playing proxied video")
		}
	})

	// 3. Rapid sweep & media switch to a different 3-scene video (Yellow / Magenta / Cyan).
	t.Run("rapid_sweep_and_media_switch", func(t *testing.T) {
		a := &app{preferences: playerPreferences{Rate: 1, Volume: 0}}
		defer a.close()

		if !a.openLocalFile(videoRGB) || a.player == nil {
			t.Fatalf("openLocalFile #1 failed: %s", a.status)
		}
		mpv1 := a.player.(*mpvMediaPlayer)
		mpv1.SetMuted(true)
		tt := ui.NewTester(a.view, 1120, 760)
		waitMainReady(t, tt, a, mpv1)
		mpv1.Pause()
		tt.Frame()

		axis, _ := tt.Find("播放进度")
		trackY := axis.Y + axis.H - 10
		// Rapid sweep across 8 positions without waiting.
		for i := 1; i <= 8; i++ {
			tt.Move(axis.X+axis.W*float32(i)/9.0, trackY)
		}

		// Immediately switch to the Yellow/Magenta/Cyan video!
		if !a.openLocalFile(videoYMC) || a.player == nil {
			t.Fatalf("openLocalFile #2 failed: %s", a.status)
		}
		mpv2 := a.player.(*mpvMediaPlayer)
		mpv2.SetMuted(true)
		waitMainReady(t, tt, a, mpv2)
		mpv2.Pause()
		tt.Frame()

		dur := mpv2.Duration()
		axis, _ = tt.Find("播放进度")
		trackY = axis.Y + axis.H - 10
		// Hover at 4.5s -> MUST show Magenta from video #2, never Green from video #1!
		x := axis.X + axis.W*(4.5/9.0)
		wantT := seekPreviewTimeForX(x-axis.X, axis.W, dur)
		tt.Move(x, trackY)
		fr := waitHoverFrame(t, tt, a, wantT, dur)
		img := tt.Image()
		// Upper band of Magenta scene (avoiding middle white stripe).
		pr, pg, pb := sampleRegionAvgRGB(img, image.Rect(
			int(fr.X+fr.W*0.20),
			int(fr.Y+fr.H*0.12),
			int(fr.X+fr.W*0.80),
			int(fr.Y+fr.H*0.34),
		))
		t.Logf("after media switch 4.5s previewRGB=(%.1f,%.1f,%.1f) (expect Magenta: high R & B, low G)", pr, pg, pb)
		if pr < 130 || pb < 130 || pg > 90 {
			t.Fatalf("media switch showed wrong/stale frame RGB=(%.1f,%.1f,%.1f), want Magenta", pr, pg, pb)
		}
	})

	// 4. Rapid back-and-forth (Red -> Blue -> Red) and stationary HTTP 404 failure.
	t.Run("rapid_return_and_stationary_http_failure", func(t *testing.T) {
		a := &app{preferences: playerPreferences{Rate: 1, Volume: 0}}
		defer a.close()
		if !a.openLocalFile(videoRGB) || a.player == nil {
			t.Fatalf("openLocalFile failed: %s", a.status)
		}
		mpvP := a.player.(*mpvMediaPlayer)
		mpvP.SetMuted(true)
		spy := &spySeekPlayer{mediaPlayer: mpvP}
		a.player = spy

		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		waitMainReady(t, tt, a, mpvP)
		mpvP.Pause()
		tt.Frame()
		spy.resetCounts()

		dur := spy.Duration()
		axis, _ := tt.Find("播放进度")
		y := axis.Y + axis.H - 10
		redX := axis.X + axis.W*(1.5/9.0)
		blueX := axis.X + axis.W*(7.5/9.0)
		redTarget := seekPreviewTimeForX(redX-axis.X, axis.W, dur)
		tt.Move(redX, y)
		waitHoverFrame(t, tt, a, redTarget, dur)

		for _, delay := range []time.Duration{0, time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond} {
			time.Sleep(90 * time.Millisecond)
			tt.Move(blueX, y)
			time.Sleep(delay)
			tt.Move(redX, y)
			fr := waitHoverFrame(t, tt, a, redTarget, dur)
			pr, pg, pb := sampleRegionAvgRGB(tt.Image(), image.Rect(
				int(fr.X+fr.W*0.3), int(fr.Y+fr.H*0.38),
				int(fr.X+fr.W*0.7), int(fr.Y+fr.H*0.60),
			))
			if pb > pr+60 {
				t.Fatalf("rapid return delay=%v leaked Blue frame RGB=(%.1f,%.1f,%.1f) under Red target", delay, pr, pg, pb)
			}
		}
		if len(spy.seekSnapshot()) != 0 {
			t.Fatalf("rapid return hover caused main seeks: %v", spy.seekSnapshot())
		}

		// Proxied HTTP 404 stationary hover: must attempt once and stop retrying.
		var broken atomic.Bool
		var rejected atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if broken.Load() {
				rejected.Add(1)
				http.Error(w, "preview unavailable", http.StatusNotFound)
				return
			}
			http.ServeFile(w, r, videoMKV)
		}))
		defer srv.Close()

		a2 := &app{preferences: playerPreferences{Rate: 1, Volume: 0}}
		defer a2.close()
		if !a2.openPlaybackStream(catalog.Stream{
			URL: srv.URL + "/three_scenes_proxied.mkv", Filename: "three_scenes_proxied.mkv",
			NativePlayable: true, IsOriginal: true,
		}, false) {
			t.Fatalf("openPlaybackStream failed: %s", a2.status)
		}
		mpv2 := a2.player.(*mpvMediaPlayer)
		mpv2.SetMuted(true)
		spy2 := &spySeekPlayer{mediaPlayer: mpv2}
		a2.player = spy2
		tt2 := ui.NewTester(a2.view, 1280, 720)
		tt2.SetTitleBar(dragBar)
		waitMainReady(t, tt2, a2, mpv2)
		mpv2.Pause()
		tt2.Frame()
		spy2.resetCounts()
		broken.Store(true)
		for range 10 {
			tt2.Frame()
			time.Sleep(15 * time.Millisecond)
		}
		beforeRejects := rejected.Load()
		axis2, _ := tt2.Find("播放进度")
		hx, hy := axis2.X+axis2.W*0.75, axis2.Y+axis2.H-10
		tt2.Move(hx, hy)
		attempts := 0
		var lastStamp time.Time
		for start := time.Now(); time.Since(start) < 600*time.Millisecond; {
			tt2.Frame()
			a2.seekPreview.mu.Lock()
			st := a2.seekPreview.lastFetchStart
			a2.seekPreview.mu.Unlock()
			if !st.IsZero() && st != lastStamp {
				attempts++
				lastStamp = st
			}
			time.Sleep(15 * time.Millisecond)
		}
		afterRejects := rejected.Load()
		if attempts > 1 || afterRejects-beforeRejects > 3 {
			t.Fatalf("stationary failed hover retried: attempts=%d 404s=%d", attempts, afterRejects-beforeRejects)
		}
		if len(spy2.seekSnapshot()) != 0 {
			t.Fatalf("stationary failed hover caused seeks: %v", spy2.seekSnapshot())
		}
		tt2.ClickAt(hx, hy)
		if len(spy2.seekSnapshot()) != 1 {
			t.Fatalf("click after stationary HTTP failure seeks=%v, want 1", spy2.seekSnapshot())
		}
	})
}

var _ color.RGBA
