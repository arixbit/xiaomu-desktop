package main

import (
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo/ui"
)

type spySeekPlayer struct {
	mediaPlayer
	seekableSet bool
	seekable    bool

	mu          sync.Mutex
	seeks       []time.Duration
	playCalls   int
	pauseCalls  int
	volumeCalls int
	rateCalls   int
	muteCalls   int
}

func (s *spySeekPlayer) unwrapMediaPlayer() mediaPlayer { return s.mediaPlayer }

func (s *spySeekPlayer) Seek(d time.Duration) {
	s.mu.Lock()
	s.seeks = append(s.seeks, d)
	s.mu.Unlock()
	s.mediaPlayer.Seek(d)
}

func (s *spySeekPlayer) Play() {
	s.mu.Lock()
	s.playCalls++
	s.mu.Unlock()
	s.mediaPlayer.Play()
}

func (s *spySeekPlayer) Pause() {
	s.mu.Lock()
	s.pauseCalls++
	s.mu.Unlock()
	s.mediaPlayer.Pause()
}

func (s *spySeekPlayer) SetVolume(v float64) {
	s.mu.Lock()
	s.volumeCalls++
	s.mu.Unlock()
	s.mediaPlayer.SetVolume(v)
}

func (s *spySeekPlayer) SetRate(r float64) {
	s.mu.Lock()
	s.rateCalls++
	s.mu.Unlock()
	s.mediaPlayer.SetRate(r)
}

func (s *spySeekPlayer) SetMuted(m bool) {
	s.mu.Lock()
	s.muteCalls++
	s.mu.Unlock()
	s.mediaPlayer.SetMuted(m)
}

func (s *spySeekPlayer) Seekable() bool {
	if s.seekableSet {
		return s.seekable
	}
	if sp, ok := s.mediaPlayer.(seekableMediaPlayer); ok {
		return sp.Seekable()
	}
	return true
}

func (s *spySeekPlayer) seekSnapshot() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.seeks...)
}

func (s *spySeekPlayer) resetCounts() {
	s.mu.Lock()
	s.seeks = nil
	s.playCalls = 0
	s.pauseCalls = 0
	s.volumeCalls = 0
	s.rateCalls = 0
	s.muteCalls = 0
	s.mu.Unlock()
}

func makeSolidBitmap(w, h int, c color.RGBA) *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	return ui.NewBitmap(img)
}

type fakePreviewBackend struct {
	mu      sync.Mutex
	fetchFn func(ctx context.Context, media seekPreviewMedia, target, duration time.Duration) (ui.ImageSource, error)
	calls   []time.Duration
	closed  atomic.Int32
}

func (f *fakePreviewBackend) Fetch(ctx context.Context, media seekPreviewMedia, target, duration time.Duration, _, _, _ int) (func() ui.ImageSource, error) {
	f.mu.Lock()
	f.calls = append(f.calls, target)
	fn := f.fetchFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("no fetch function")
	}
	src, err := fn(ctx, media, target, duration)
	if err != nil {
		return nil, err
	}
	return func() ui.ImageSource { return src }, nil
}

func (f *fakePreviewBackend) Close() {
	f.closed.Add(1)
}

type seekPreviewFakeVideo struct {
	dur   time.Duration
	frame *image.RGBA
	sent  bool
}

func newSeekPreviewFakeVideo(w, h int, dur time.Duration) *seekPreviewFakeVideo {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{R: 28, G: 36, B: 52, A: 255}
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, bg)
		}
	}
	return &seekPreviewFakeVideo{dur: dur, frame: img}
}

func (v *seekPreviewFakeVideo) Next() (ui.VideoFrame, error) {
	if v.sent {
		return ui.VideoFrame{}, io.EOF
	}
	v.sent = true
	return ui.VideoFrame{Image: v.frame, Duration: v.dur}, nil
}

func (v *seekPreviewFakeVideo) Rewind() error           { v.sent = false; return nil }
func (v *seekPreviewFakeVideo) Duration() time.Duration { return v.dur }

func newSeekPreviewTestApp(t *testing.T, dur time.Duration, initialPos time.Duration, playing bool, withDanmaku bool) (*app, *spySeekPlayer, *fakePreviewBackend) {
	t.Helper()
	vp, err := ui.NewVideoPlayer(newSeekPreviewFakeVideo(640, 360, dur))
	if err != nil {
		t.Fatal(err)
	}
	base := wrapSystemPlayer(vp)
	if !playing {
		base.Pause()
	}
	base.Seek(initialPos)
	spy := &spySeekPlayer{mediaPlayer: base}

	sceneRed := makeSolidBitmap(160, 90, color.RGBA{R: 220, G: 40, B: 45, A: 255})
	sceneGreen := makeSolidBitmap(160, 90, color.RGBA{R: 35, G: 185, B: 85, A: 255})
	sceneBlue := makeSolidBitmap(160, 90, color.RGBA{R: 40, G: 105, B: 235, A: 255})
	backend := &fakePreviewBackend{
		fetchFn: func(ctx context.Context, _ seekPreviewMedia, target, duration time.Duration) (ui.ImageSource, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			frac := target.Seconds() / max(duration.Seconds(), 1)
			switch {
			case frac < 0.34:
				return sceneRed, nil
			case frac < 0.67:
				return sceneGreen, nil
			default:
				return sceneBlue, nil
			}
		},
	}

	a := &app{
		player:         spy,
		autoNext:       false,
		playingSource:  "线路",
		playingEpisode: "第1集",
		preferences:    defaultPlayerPreferences(),
	}
	a.seekPreview.newBackend = func() seekPreviewBackend { return backend }
	a.seekPreview.debounce = time.Millisecond
	a.setSeekPreviewMedia(seekPreviewMedia{target: "fixture://episode-1", isLocal: true})

	a.title = &titlePage{
		name:   "示例剧",
		failed: map[string]string{},
		detail: &catalog.Detail{
			Name: "示例剧",
			Sources: []catalog.Source{{
				Name: "线路",
				Episodes: []catalog.Episode{
					{Name: "第1集", ID: "ep1"},
					{Name: "第2集", ID: "ep2"},
				},
			}},
		},
	}
	if withDanmaku {
		a.preferences.Danmaku.Enabled = true
		a.applyDanmaku(danmaku.Episode{ID: "ep1"}, []danmaku.Comment{
			{Time: dur / 10, Mode: 1, Color: 0xffffff, Text: "第一段"},
			{Time: dur / 2, Mode: 1, Color: 0xffffff, Text: "高能"},
			{Time: dur / 2 + time.Second, Mode: 1, Color: 0xffffff, Text: "高能2"},
			{Time: dur * 4 / 5, Mode: 1, Color: 0xffffff, Text: "结尾"},
		})
	}
	t.Cleanup(a.close)
	return a, spy, backend
}

func waitForSeekPreviewReady(t *testing.T, tt *ui.Tester, a *app, wantTarget time.Duration, dur time.Duration) {
	t.Helper()
	wantBucket := seekPreviewBucket(wantTarget, dur)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		a.seekPreview.mu.Lock()
		ready := a.seekPreview.readyGen == a.seekPreview.gen &&
			a.seekPreview.readyBucket == wantBucket &&
			a.seekPreview.readySource != nil
		a.seekPreview.mu.Unlock()
		if ready {
			tt.Frame()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("seek preview did not finish decoding bucket=%d target=%s", wantBucket, wantTarget)
}

func TestSeekHoverPreviewPointerLifecycleAndSingleCommit(t *testing.T) {
	const dur = 46*time.Minute + 17*time.Second
	const startPos = 26*time.Minute + 49*time.Second

	for _, playing := range []bool{false, true} {
		name := "paused"
		if playing {
			name = "playing"
		}
		t.Run(name, func(t *testing.T) {
			a, spy, _ := newSeekPreviewTestApp(t, dur, startPos, playing, true)
			tt := ui.NewTester(a.view, 1120, 760)
			tt.SetTitleBar(dragBar)
			tt.Frame()

			dockBefore, hasDock := tt.Find("播放控制条")
			axis, ok := tt.Find("播放进度")
			if !ok {
				t.Fatal("missing 播放进度")
			}
			curve, ok := tt.Find("弹幕高能曲线")
			if !ok {
				t.Fatal("missing 弹幕高能曲线")
			}
			trackY := axis.Y + axis.H - 10
			curveY := curve.Y + curve.H/2

			// 1. Sweep across 3 separated positions: preview updates time & distinct frame, 0 seeks.
			type probe struct {
				frac   float32
				expect string
				check  func(color.RGBA) bool
			}
			probes := []probe{
				{0.15, "red", func(c color.RGBA) bool { return c.R > 180 && c.G < 80 && c.B < 80 }},
				{0.50, "green", func(c color.RGBA) bool { return c.G > 140 && c.R < 80 && c.B < 120 }},
				{0.82, "blue", func(c color.RGBA) bool { return c.B > 180 && c.R < 80 && c.G < 140 }},
			}
			for _, pr := range probes {
				x := axis.X + axis.W*pr.frac
				wantT := seekPreviewTimeForX(x-axis.X, axis.W, dur)
				tt.Move(x, trackY)
				waitForSeekPreviewReady(t, tt, a, wantT, dur)

				if !tt.HasText(playerClock(wantT)) {
					t.Fatalf("probe %s: missing preview time %s in texts=%v", pr.expect, playerClock(wantT), tt.Texts())
				}
				card, ok := tt.Find("进度预览")
				if !ok {
					t.Fatalf("probe %s: missing 进度预览 overlay", pr.expect)
				}
				if card.Y+card.H > axis.Y {
					t.Fatalf("probe %s: preview card overlaps curve/axis: card=%+v axis=%+v", pr.expect, card, axis)
				}
				// Verify progress bar / dock never shifts when preview appears.
				axisNow, _ := tt.Find("播放进度")
				if axisNow != axis {
					t.Fatalf("probe %s: preview shifted seek axis: before=%+v after=%+v", pr.expect, axis, axisNow)
				}
				if hasDock {
					dockNow, _ := tt.Find("播放控制条")
					if dockNow != dockBefore {
						t.Fatalf("probe %s: preview shifted dock: before=%+v after=%+v", pr.expect, dockBefore, dockNow)
					}
				}
				// Sample pixel inside preview frame (upper-middle, above bottom timestamp pill).
				img := tt.Image()
				px := img.RGBAAt(int(card.X+card.W/2), int(card.Y+card.H*0.35))
				if !pr.check(px) {
					t.Fatalf("probe %s: unexpected preview pixel %+v at card=%+v", pr.expect, px, card)
				}
			}

			if seeks := spy.seekSnapshot(); len(seeks) != 0 {
				t.Fatalf("hovering across 3 positions caused seeks: %v", seeks)
			}
			if spy.Playing() != playing || spy.playCalls != 0 || spy.pauseCalls != 0 || spy.volumeCalls != 0 || spy.rateCalls != 0 || spy.muteCalls != 0 {
				t.Fatalf("hover altered main player state: playing=%v want=%v calls=(play:%d pause:%d vol:%d rate:%d mute:%d)",
					spy.Playing(), playing, spy.playCalls, spy.pauseCalls, spy.volumeCalls, spy.rateCalls, spy.muteCalls)
			}

			// 2. Press & hold while dragging across positions: still 0 seeks until release.
			x1 := axis.X + axis.W*0.20
			x2 := axis.X + axis.W*0.60
			x3 := axis.X + axis.W*0.75
			tt.Press(x1, trackY)
			tt.Move(x2, trackY)
			tt.Move(x3, trackY)
			if seeks := spy.seekSnapshot(); len(seeks) != 0 {
				t.Fatalf("press/drag before release caused seeks: %v", seeks)
			}

			// 3a. Cancel press via CancelPress: 0 seeks, preview dismissed.
			tt.CancelPress()
			if seeks := spy.seekSnapshot(); len(seeks) != 0 {
				t.Fatalf("CancelPress caused seeks: %v", seeks)
			}
			if _, ok := tt.Find("进度预览"); ok {
				t.Fatal("CancelPress left preview visible")
			}

			// 3b. Press inside, drag far outside, release outside: 0 seeks, preview dismissed.
			tt.Press(x1, trackY)
			tt.Move(x2, axis.Y-120)
			if _, ok := tt.Find("进度预览"); ok {
				t.Fatal("dragging outside valid area did not hide preview")
			}
			tt.Release(x2, axis.Y-120)
			if seeks := spy.seekSnapshot(); len(seeks) != 0 {
				t.Fatalf("releasing outside valid area caused seeks: %v", seeks)
			}

			// 3c. Hover then move out: 0 seeks, preview hides.
			tt.Move(x2, trackY)
			if _, ok := tt.Find("进度预览"); !ok {
				t.Fatal("preview did not reopen on hover")
			}
			tt.Move(axis.X+axis.W/2, axis.Y-80)
			if _, ok := tt.Find("进度预览"); ok {
				t.Fatal("moving pointer out did not hide preview")
			}

			// 3d. Hover then Escape: hides preview, 0 seeks.
			tt.Move(x2, trackY)
			if _, ok := tt.Find("进度预览"); !ok {
				t.Fatal("preview did not show before Escape")
			}
			tt.Key(0, ui.KeyEscape)
			if _, ok := tt.Find("进度预览"); ok {
				t.Fatal("Escape did not dismiss hover preview")
			}
			if seeks := spy.seekSnapshot(); len(seeks) != 0 {
				t.Fatalf("Escape dismissal caused seeks: %v", seeks)
			}

			// 4. Valid click on danmaku highlight curve -> exactly 1 seek at clicked time.
			clickX := axis.X + axis.W*0.35
			wantClick := seekPreviewTimeForX(clickX-axis.X, axis.W, dur)
			tt.ClickAt(clickX, curveY)
			seeks := spy.seekSnapshot()
			if len(seeks) != 1 || absDuration(seeks[0]-wantClick) > 10*time.Millisecond {
				t.Fatalf("curve click seeks = %v, want single seek at %s", seeks, wantClick)
			}
			if spy.Playing() != playing {
				t.Fatalf("curve click changed playing state: got=%v want=%v", spy.Playing(), playing)
			}

			// 5. Valid drag & release on track -> exactly 1 seek at release position.
			spy.resetCounts()
			dragStart := axis.X + axis.W*0.25
			dragEnd := axis.X + axis.W*0.78
			wantDrag := seekPreviewTimeForX(dragEnd-axis.X, axis.W, dur)
			tt.Press(dragStart, trackY)
			tt.Move(axis.X+axis.W*0.50, trackY)
			tt.Move(dragEnd, trackY)
			if len(spy.seekSnapshot()) != 0 {
				t.Fatalf("seek occurred before drag release: %v", spy.seekSnapshot())
			}
			tt.Release(dragEnd, trackY)
			seeks = spy.seekSnapshot()
			if len(seeks) != 1 || absDuration(seeks[0]-wantDrag) > 10*time.Millisecond {
				t.Fatalf("drag release seeks = %v, want single seek at %s", seeks, wantDrag)
			}
			if spy.Playing() != playing {
				t.Fatalf("drag release changed playing state: got=%v want=%v", spy.Playing(), playing)
			}
		})
	}
}

func TestSeekHoverPreviewBoundariesDanmakuToggleFullscreenAndSmallWindow(t *testing.T) {
	const dur = 46*time.Minute + 17*time.Second
	const startPos = 10 * time.Minute

	cases := []struct {
		name        string
		w, h        int
		fullScreen  bool
		withDanmaku bool
	}{
		{"windowed-with-curve", 1280, 720, false, true},
		{"windowed-no-curve", 1280, 720, false, false},
		{"fullscreen-with-curve", 1280, 720, true, true},
		{"small-640x420-with-curve", 640, 420, false, true},
		{"small-640x420-no-curve", 640, 420, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, spy, _ := newSeekPreviewTestApp(t, dur, startPos, false, tc.withDanmaku)
			a.setFullScreen(tc.fullScreen)
			tt := ui.NewTester(a.view, tc.w, tc.h)
			if !tc.fullScreen {
				tt.SetTitleBar(dragBar)
			}
			tt.Frame()

			axis, ok := tt.Find("播放进度")
			if !ok {
				t.Fatalf("missing 播放进度 at %s", tc.name)
			}
			_, hasCurve := tt.Find("弹幕高能曲线")
			if hasCurve != tc.withDanmaku {
				t.Fatalf("danmaku curve visible=%v, want %v", hasCurve, tc.withDanmaku)
			}
			trackY := axis.Y + axis.H - 10

			// Left edge (00:00): card clamps inward and stays inside window.
			tt.Move(axis.X+1, trackY)
			cardL, ok := tt.Find("进度预览")
			if !ok {
				t.Fatal("left boundary did not show preview")
			}
			if cardL.X < axis.X-1 || cardL.X < 0 || cardL.Y < 0 || cardL.Y+cardL.H > axis.Y+0.5 {
				t.Fatalf("left boundary card out of bounds: card=%+v axis=%+v win=%dx%d", cardL, axis, tc.w, tc.h)
			}
			if !tt.HasText("00:00") {
				t.Fatalf("left boundary missing 00:00: %v", tt.Texts())
			}

			// Right edge (46:17): card clamps inward and stays inside window.
			tt.Move(axis.X+axis.W-1, trackY)
			cardR, ok := tt.Find("进度预览")
			if !ok {
				t.Fatal("right boundary did not show preview")
			}
			if cardR.X+cardR.W > axis.X+axis.W+1 || cardR.X+cardR.W > float32(tc.w)+0.5 || cardR.Y < 0 || cardR.Y+cardR.H > axis.Y+0.5 {
				t.Fatalf("right boundary card out of bounds: card=%+v axis=%+v win=%dx%d", cardR, axis, tc.w, tc.h)
			}

			// In fullscreen, hovering on the seek bar keeps controls visible past idle timeout.
			if tc.fullScreen {
				tt.Advance(4 * time.Second)
				if _, ok := tt.Find("播放进度"); !ok {
					t.Fatal("fullscreen controls hid while hovering seek bar")
				}
				if _, ok := tt.Find("进度预览"); !ok {
					t.Fatal("fullscreen preview hid while hovering seek bar")
				}
			}

			if len(spy.seekSnapshot()) != 0 {
				t.Fatalf("boundary hovers caused seeks: %v", spy.seekSnapshot())
			}
		})
	}
}

func TestSeekHoverPreviewKeyboardFocusAndVideoShortcuts(t *testing.T) {
	const dur = 2 * time.Minute
	const startPos = 40 * time.Second

	a, spy, _ := newSeekPreviewTestApp(t, dur, startPos, false, true)
	a.setFullScreen(true)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()

	// 1. Video canvas has initial focus -> Left/Right seek ±10s directly and Right-hold boosts 2x.
	tt.Key(0, ui.KeyLeft)
	if got := spy.Position(); got != 30*time.Second {
		t.Fatalf("video focused KeyLeft pos=%s, want 30s", got)
	}
	spy.Play()
	tt.Frame()
	tt.KeyDown(ui.KeyRight)
	tt.Advance(350 * time.Millisecond)
	if spy.Rate() != 2.0 || !tt.HasText("▶▶ 2× 倍速中") {
		t.Fatalf("video focused long-press Right did not boost 2x: rate=%v", spy.Rate())
	}
	tt.KeyUp(ui.KeyRight)
	if spy.Rate() != 1.0 {
		t.Fatalf("video focused Right release did not restore 1x: rate=%v", spy.Rate())
	}
	spy.Pause()
	spy.Seek(40 * time.Second)
	spy.resetCounts()
	tt.Frame()

	// 2. Focus seek bar via click at 40s (or Tab), then test keyboard preview.
	axis, ok := tt.Find("播放进度")
	if !ok {
		t.Fatal("missing 播放进度")
	}
	clickX := axis.X + axis.W*(40.0/120.0)
	tt.ClickAt(clickX, axis.Y+axis.H-10)
	// Move mouse away so pointer hover doesn't interfere with keyboard preview.
	tt.Move(20, 20)
	spy.resetCounts()
	if !tt.Focused("播放进度") {
		t.Fatal("clicking seek bar did not focus 播放进度")
	}

	// ArrowRight twice (+5s, +5s = 50s), ArrowLeft once (-5s = 45s) -> 0 seeks!
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyLeft)
	if len(spy.seekSnapshot()) != 0 {
		t.Fatalf("keyboard arrows on focused seek bar seeked prematurely: %v", spy.seekSnapshot())
	}
	if !a.seekPreview.active || absDuration(a.seekPreview.target-45*time.Second) > 20*time.Millisecond {
		t.Fatalf("keyboard preview target=%s active=%v, want ~45s", a.seekPreview.target, a.seekPreview.active)
	}
	if !tt.HasText("00:45") {
		t.Fatalf("keyboard preview time 00:45 missing: %v", tt.Texts())
	}

	// Escape hides keyboard preview without leaving fullscreen!
	tt.Key(0, ui.KeyEscape)
	if a.seekPreview.active || !a.fullScreen {
		t.Fatalf("Escape with keyboard preview failed: active=%v full=%v", a.seekPreview.active, a.fullScreen)
	}

	// ArrowRight again (+5s from ~40s -> ~45s), then Enter commits exactly 1 seek!
	tt.Key(0, ui.KeyRight)
	if len(spy.seekSnapshot()) != 0 || absDuration(a.seekPreview.target-45*time.Second) > 20*time.Millisecond {
		t.Fatalf("post-Esc ArrowRight unexpected: seeks=%v target=%s", spy.seekSnapshot(), a.seekPreview.target)
	}
	tt.Key(0, ui.KeyEnter)
	seeks := spy.seekSnapshot()
	if len(seeks) != 1 || absDuration(seeks[0]-45*time.Second) > 20*time.Millisecond || spy.Playing() {
		t.Fatalf("Enter commit unexpected: seeks=%v playing=%v", seeks, spy.Playing())
	}
	if a.seekPreview.active {
		t.Fatal("Enter commit did not close keyboard preview")
	}

	// Blur seek bar (click video canvas) hides keyboard preview and restores video arrows.
	tt.Key(0, ui.KeyRight)
	if !a.seekPreview.active {
		t.Fatal("keyboard preview did not reopen")
	}
	tt.ClickAt(500, 300)
	if a.seekPreview.active {
		t.Fatal("blurring seek bar did not hide keyboard preview")
	}
	tt.Advance(600 * time.Millisecond)
	spy.Pause()
	spy.Seek(45 * time.Second)
	spy.resetCounts()
	tt.Key(0, ui.KeyLeft)
	if got := spy.Position(); got != 35*time.Second {
		t.Fatalf("after blur, video canvas KeyLeft did not seek -10s: pos=%s", got)
	}
}

func TestSeekHoverPreviewSlowFailureAndStaleRejectionOnMediaSwitch(t *testing.T) {
	const dur = 60 * time.Second

	t.Run("click works while thumbnail still loading", func(t *testing.T) {
		a, spy, backend := newSeekPreviewTestApp(t, dur, 10*time.Second, false, false)
		started := make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		backend.fetchFn = func(ctx context.Context, _ seekPreviewMedia, _, _ time.Duration) (ui.ImageSource, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return makeSolidBitmap(160, 90, color.RGBA{R: 255, G: 255, B: 0, A: 255}), nil
			}
		}

		tt := ui.NewTester(a.view, 1120, 760)
		axis, _ := tt.Find("播放进度")
		x := axis.X + axis.W*0.50
		y := axis.Y + axis.H/2
		tt.Move(x, y)
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("background fetch did not start")
		}
		tt.Frame()
		if !tt.HasText("00:30") {
			t.Fatalf("loading state did not immediately display target time 00:30: %v", tt.Texts())
		}

		// Click while frame is still blocked loading -> commits immediately!
		tt.ClickAt(x, y)
		seeks := spy.seekSnapshot()
		if len(seeks) != 1 || absDuration(seeks[0]-30*time.Second) > 20*time.Millisecond {
			t.Fatalf("click during loading failed: seeks=%v", seeks)
		}
	})

	t.Run("failed fetch falls back to time pill and click still works", func(t *testing.T) {
		a, spy, backend := newSeekPreviewTestApp(t, dur, 10*time.Second, false, false)
		backend.fetchFn = func(context.Context, seekPreviewMedia, time.Duration, time.Duration) (ui.ImageSource, error) {
			return nil, errors.New("decode failed")
		}

		tt := ui.NewTester(a.view, 1120, 760)
		axis, _ := tt.Find("播放进度")
		x := axis.X + axis.W*0.75
		y := axis.Y + axis.H/2
		tt.Move(x, y)

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			a.seekPreview.mu.Lock()
			failed := a.seekPreview.failed
			a.seekPreview.mu.Unlock()
			if failed {
				tt.Frame()
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !tt.HasText("00:45") {
			t.Fatalf("failed fetch did not show fallback timestamp 00:45: %v", tt.Texts())
		}
		if _, ok := tt.Find("进度预览画面"); ok {
			t.Fatal("failed fetch unexpectedly rendered an image")
		}
		tt.ClickAt(x, y)
		seeks := spy.seekSnapshot()
		if len(seeks) != 1 || absDuration(seeks[0]-45*time.Second) > 20*time.Millisecond {
			t.Fatalf("click after failed fetch failed: seeks=%v", seeks)
		}
	})

	t.Run("media switch cancels old request and rejects stale result", func(t *testing.T) {
		a, _, backend := newSeekPreviewTestApp(t, dur, 10*time.Second, false, false)
		oldStarted := make(chan struct{})
		oldCanceled := make(chan struct{})
		oldBitmap := makeSolidBitmap(160, 90, color.RGBA{R: 255, G: 0, B: 255, A: 255})
		newBitmap := makeSolidBitmap(160, 90, color.RGBA{R: 0, G: 220, B: 220, A: 255})

		backend.fetchFn = func(ctx context.Context, media seekPreviewMedia, _, _ time.Duration) (ui.ImageSource, error) {
			if media.target == "fixture://episode-1" {
				select {
				case <-oldStarted:
				default:
					close(oldStarted)
				}
				<-ctx.Done()
				select {
				case <-oldCanceled:
				default:
					close(oldCanceled)
				}
				// Deliberately return a bitmap even after cancel to verify stale rejection!
				return oldBitmap, nil
			}
			return newBitmap, nil
		}

		tt := ui.NewTester(a.view, 1120, 760)
		axis, _ := tt.Find("播放进度")
		tt.Move(axis.X+axis.W*0.50, axis.Y+axis.H/2)
		select {
		case <-oldStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("old episode fetch did not start")
		}

		// Switch media generation (simulating episode/source change).
		a.setSeekPreviewMedia(seekPreviewMedia{target: "fixture://episode-2", isLocal: true})
		select {
		case <-oldCanceled:
		case <-time.After(2 * time.Second):
			t.Fatal("switching media did not cancel in-flight preview fetch")
		}
		time.Sleep(30 * time.Millisecond)
		tt.Frame()

		a.seekPreview.mu.Lock()
		stale := a.seekPreview.readySource != nil
		a.seekPreview.mu.Unlock()
		if stale {
			t.Fatal("stale result from old episode leaked into new episode state")
		}

		// Hovering on new episode loads new episode's cyan frame cleanly.
		x := axis.X + axis.W*0.60
		wantT := seekPreviewTimeForX(x-axis.X, axis.W, dur)
		tt.Move(x, axis.Y+axis.H/2)
		waitForSeekPreviewReady(t, tt, a, wantT, dur)
		card, ok := tt.Find("进度预览")
		if !ok {
			t.Fatal("new episode preview card missing")
		}
		px := tt.Image().RGBAAt(int(card.X+card.W/2), int(card.Y+card.H*0.35))
		if px.G < 180 || px.B < 180 || px.R > 80 {
			t.Fatalf("new episode preview rendered wrong color %+v (want cyan)", px)
		}
	})
}

func TestSeekHoverPreviewRespectsLiveUnknownDurationAndNonSeekableCatchup(t *testing.T) {
	t.Run("non-seekable catchup disables preview and seeking", func(t *testing.T) {
		a, spy, _ := newSeekPreviewTestApp(t, 10*time.Minute, 2*time.Minute, false, false)
		a.playingLive = true
		a.live.catchupProgram = &iptv.Program{Title: "不可定位回看"}
		spy.seekableSet = true
		spy.seekable = false

		tt := ui.NewTester(a.view, 1120, 760)
		axis, ok := tt.Find("播放进度")
		if !ok {
			t.Fatal("missing read-only progress bar")
		}
		tt.Move(axis.X+axis.W*0.5, axis.Y+axis.H/2)
		if _, ok := tt.Find("进度预览"); ok {
			t.Fatal("non-seekable catchup unexpectedly opened preview")
		}
		tt.ClickAt(axis.X+axis.W*0.5, axis.Y+axis.H/2)
		if len(spy.seekSnapshot()) != 0 {
			t.Fatalf("non-seekable catchup seeked on click: %v", spy.seekSnapshot())
		}
	})

	t.Run("seekable catchup enables preview and single-commit click", func(t *testing.T) {
		a, spy, _ := newSeekPreviewTestApp(t, 10*time.Minute, 2*time.Minute, false, false)
		a.playingLive = true
		a.live.catchupProgram = &iptv.Program{Title: "可定位回看"}
		spy.seekableSet = true
		spy.seekable = true

		tt := ui.NewTester(a.view, 1120, 760)
		axis, ok := tt.Find("播放进度")
		if !ok {
			t.Fatal("missing catchup seek bar")
		}
		x := axis.X + axis.W*0.5
		tt.Move(x, axis.Y+axis.H/2)
		if _, ok := tt.Find("进度预览"); !ok {
			t.Fatal("seekable catchup did not show preview")
		}
		if len(spy.seekSnapshot()) != 0 {
			t.Fatalf("hovering seekable catchup caused seek: %v", spy.seekSnapshot())
		}
		tt.ClickAt(x, axis.Y+axis.H/2)
		if seeks := spy.seekSnapshot(); len(seeks) != 1 || absDuration(seeks[0]-5*time.Minute) > 50*time.Millisecond {
			t.Fatalf("clicking seekable catchup seeks=%v, want 1 seek at 5m", seeks)
		}
	})

	t.Run("real-time live keeps live indicator without preview", func(t *testing.T) {
		a, _, _ := newSeekPreviewTestApp(t, 10*time.Minute, 2*time.Minute, true, false)
		a.playingLive = true
		a.live.catchupProgram = nil

		tt := ui.NewTester(a.view, 1120, 760)
		if _, ok := tt.Find("播放进度"); ok {
			t.Fatal("real-time live unexpectedly showed seek bar")
		}
		if !tt.HasText("直播") {
			t.Fatal("real-time live missing 直播 badge")
		}
	})
}

// mutableTexturePreviewBackend simulates a single-player decoder (like libmpv)
// whose live texture is overwritten in place whenever Fetch seeks to a new
// timestamp. Any stale getter returned earlier would read the new color unless
// seekPreviewWorker invalidates readySource before starting the next Fetch.
type mutableTexturePreviewBackend struct {
	mu          sync.RWMutex
	current     ui.ImageSource
	red         ui.ImageSource
	blue        ui.ImageSource
	gotBlue     chan struct{}
	releaseBlue chan struct{}
	heldBlue    atomic.Bool
	fetchCount  atomic.Int32
}

func (b *mutableTexturePreviewBackend) Fetch(ctx context.Context, _ seekPreviewMedia, target, duration time.Duration, _, _, _ int) (func() ui.ImageSource, error) {
	b.fetchCount.Add(1)
	frac := target.Seconds() / max(duration.Seconds(), 1)
	if frac > 0.6 {
		b.mu.Lock()
		b.current = b.blue
		b.mu.Unlock()
		if !b.heldBlue.Swap(true) {
			close(b.gotBlue)
			<-b.releaseBlue
		}
	} else {
		b.mu.Lock()
		b.current = b.red
		b.mu.Unlock()
	}
	return func() ui.ImageSource {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return b.current
	}, nil
}

func (b *mutableTexturePreviewBackend) Close() {}

func TestSeekHoverPreviewRapidReturnInvalidatesSharedMutableTexture(t *testing.T) {
	dur := 9 * time.Second
	a, spy, _ := newSeekPreviewTestApp(t, dur, time.Second, false, false)
	be := &mutableTexturePreviewBackend{
		red:         makeSolidBitmap(160, 90, color.RGBA{R: 204, G: 40, B: 54, A: 255}),
		blue:        makeSolidBitmap(160, 90, color.RGBA{R: 36, G: 99, B: 234, A: 255}),
		gotBlue:     make(chan struct{}),
		releaseBlue: make(chan struct{}),
	}
	a.seekPreview.newBackend = func() seekPreviewBackend { return be }
	a.seekPreview.debounce = 5 * time.Millisecond
	released := false
	defer func() {
		if !released {
			close(be.releaseBlue)
		}
	}()

	tt := ui.NewTester(a.view, 1120, 760)
	axis, ok := tt.Find("播放进度")
	if !ok {
		t.Fatal("missing 播放进度")
	}
	y := axis.Y + axis.H/2
	xRed := axis.X + axis.W*(1.5/9.0)
	xBlue := axis.X + axis.W*(7.5/9.0)
	redTarget := seekPreviewTimeForX(xRed-axis.X, axis.W, dur)

	// 1. Hover A (Red) and wait until ready.
	tt.Move(xRed, y)
	waitForSeekPreviewReady(t, tt, a, redTarget, dur)
	frame, ok := tt.Find("进度预览画面")
	if !ok {
		t.Fatal("missing initial red preview")
	}
	px := tt.Image().RGBAAt(int(frame.X+frame.W/2), int(frame.Y+frame.H*0.35))
	if px.R < 150 || px.B > 100 {
		t.Fatalf("initial preview at A was not red: %+v", px)
	}

	// 2. Move to B (Blue) so shared backend overwrites live texture to Blue,
	// then immediately move back to A before B finishes publishing.
	tt.Move(xBlue, y)
	select {
	case <-be.gotBlue:
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not reach Blue decode")
	}

	// Moving back to A must NOT reuse the invalidated Red readySource (which
	// would now read Blue from the shared backend).
	tt.Move(xRed, y)
	tt.Frame()
	if f, ok := tt.Find("进度预览画面"); ok {
		mid := tt.Image().RGBAAt(int(f.X+f.W/2), int(f.Y+f.H*0.35))
		if int(mid.B) > int(mid.R)+60 {
			t.Fatalf("stale Blue frame leaked while returning to Red target: %+v", mid)
		}
	}

	released = true
	close(be.releaseBlue)
	waitForSeekPreviewReady(t, tt, a, redTarget, dur)
	frame, ok = tt.Find("进度预览画面")
	if !ok {
		t.Fatal("missing re-decoded red preview after rapid return")
	}
	px = tt.Image().RGBAAt(int(frame.X+frame.W/2), int(frame.Y+frame.H*0.35))
	if px.R < 150 || px.B > 100 {
		t.Fatalf("preview after rapid return to A was wrong color %+v (want Red)", px)
	}
	if seeks := spy.seekSnapshot(); len(seeks) != 0 {
		t.Fatalf("rapid return hover triggered main seeks: %v", seeks)
	}

	// Clicking A still commits exactly 1 seek.
	tt.ClickAt(xRed, y)
	if seeks := spy.seekSnapshot(); len(seeks) != 1 || absDuration(seeks[0]-redTarget) > 30*time.Millisecond {
		t.Fatalf("click after rapid return seeks=%v want 1 seek at %s", seeks, redTarget)
	}
}

func TestSeekHoverPreviewStationaryFailureDoesNotRetry(t *testing.T) {
	dur := time.Minute
	a, spy, backend := newSeekPreviewTestApp(t, dur, 10*time.Second, false, false)
	var failCalls atomic.Int32
	green := makeSolidBitmap(160, 90, color.RGBA{R: 35, G: 185, B: 85, A: 255})
	backend.fetchFn = func(_ context.Context, _ seekPreviewMedia, target, _ time.Duration) (ui.ImageSource, error) {
		if target >= 40*time.Second {
			failCalls.Add(1)
			return nil, errors.New("upstream 404")
		}
		return green, nil
	}

	tt := ui.NewTester(a.view, 1120, 760)
	axis, ok := tt.Find("播放进度")
	if !ok {
		t.Fatal("missing 播放进度")
	}
	y := axis.Y + axis.H/2
	xFail := axis.X + axis.W*0.75
	tt.Move(xFail, y)

	// Wait for the single initial attempt to fail.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		a.seekPreview.mu.Lock()
		failed := a.seekPreview.failed && !a.seekPreview.loading
		a.seekPreview.mu.Unlock()
		if failed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := failCalls.Load(); got != 1 {
		t.Fatalf("initial failure attempts=%d, want 1", got)
	}

	// Keep pointer stationary at the exact failed location across many frames:
	// must NOT retry or flash loading again.
	loadingFrames := 0
	for range 25 {
		tt.Frame()
		if tt.HasText("加载画面…") {
			loadingFrames++
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := failCalls.Load(); got != 1 {
		t.Fatalf("stationary hover retried failed bucket: attempts=%d, want 1", got)
	}
	if loadingFrames != 0 {
		t.Fatalf("stationary failed hover flashed loading on %d frames", loadingFrames)
	}
	if !tt.HasText("00:45") {
		t.Fatalf("stationary failed hover lost fallback timestamp 00:45: %v", tt.Texts())
	}
	if len(spy.seekSnapshot()) != 0 {
		t.Fatalf("stationary failed hover caused main seeks: %v", spy.seekSnapshot())
	}

	// Clicking the failed location still commits exactly 1 seek.
	tt.ClickAt(xFail, y)
	if seeks := spy.seekSnapshot(); len(seeks) != 1 || absDuration(seeks[0]-45*time.Second) > 50*time.Millisecond {
		t.Fatalf("click on failed location seeks=%v want 1 seek at 45s", seeks)
	}

	// Moving to another bucket still recovers cleanly and shows its thumbnail.
	xOK := axis.X + axis.W*0.25
	okTarget := seekPreviewTimeForX(xOK-axis.X, axis.W, dur)
	tt.Move(xOK, y)
	waitForSeekPreviewReady(t, tt, a, okTarget, dur)
	if _, ok := tt.Find("进度预览画面"); !ok {
		t.Fatal("moving to healthy bucket after failed bucket did not render thumbnail")
	}
}

func TestSeekHoverPreviewWindowCloseNoRace(t *testing.T) {
	for i := range 8 {
		a, _, backend := newSeekPreviewTestApp(t, time.Minute, 10*time.Second, false, false)
		a.dispatch = func(func()) {}
		began := make(chan struct{})
		finish := make(chan struct{})
		orig := backend.fetchFn
		backend.fetchFn = func(ctx context.Context, m seekPreviewMedia, p, d time.Duration) (ui.ImageSource, error) {
			select {
			case <-began:
			default:
				close(began)
			}
			select {
			case <-finish:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return orig(ctx, m, p, d)
		}

		tt := ui.NewTester(a.view, 1120, 760)
		axis, ok := tt.Find("播放进度")
		if !ok {
			t.Fatal("missing 播放进度")
		}
		tt.Move(axis.X+axis.W*0.5, axis.Y+axis.H/2)
		select {
		case <-began:
		case <-time.After(2 * time.Second):
			t.Fatalf("iter %d: preview fetch did not start", i)
		}
		close(finish)
		if i%2 == 1 {
			time.Sleep(time.Duration(i) * time.Millisecond)
		}
		a.close()
	}
}

func TestSeekHoverPreviewRemoteStartsAsTimePillThenExpandsOnFrame(t *testing.T) {
	dur := 45 * time.Minute
	a, spy, backend := newSeekPreviewTestApp(t, dur, 2*time.Minute, true, true)
	a.setSeekPreviewMedia(seekPreviewMedia{target: "http://127.0.0.1:45678/s/abc/ep1.mp4", isLocal: false})

	started := make(chan struct{})
	release := make(chan struct{})
	readyBitmap := makeSolidBitmap(160, 90, color.RGBA{R: 35, G: 185, B: 85, A: 255})
	backend.fetchFn = func(ctx context.Context, _ seekPreviewMedia, _, _ time.Duration) (ui.ImageSource, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		select {
		case <-release:
			return readyBitmap, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	tt := ui.NewTester(a.view, 1120, 760)
	axis, ok := tt.Find("播放进度")
	if !ok {
		t.Fatal("missing 播放进度")
	}
	x := axis.X + axis.W*0.50
	y := axis.Y + axis.H/2
	wantTarget := seekPreviewTimeForX(x-axis.X, axis.W, dur)
	tt.Move(x, y)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("remote preview fetch did not start")
	}
	tt.Frame()

	// While remote fetch is in flight, show compact time pill instead of empty
	// "加载画面…" box.
	if tt.HasText("加载画面…") {
		t.Fatal("remote stream unexpectedly showed empty loading box while fetching")
	}
	if !tt.HasText(playerClock(wantTarget)) {
		t.Fatalf("remote time pill missing target %s: %v", playerClock(wantTarget), tt.Texts())
	}
	pill, ok := tt.Find("进度预览")
	if !ok || pill.H > 32 {
		t.Fatalf("remote loading preview should be compact time pill, got ok=%v rect=%+v", ok, pill)
	}

	// Once real frame arrives, preview expands to 16:9 thumbnail card.
	released = true
	close(release)
	wantBucket := seekPreviewBucketFor(wantTarget, dur, false)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		a.seekPreview.mu.Lock()
		ready := a.seekPreview.readyGen == a.seekPreview.gen &&
			a.seekPreview.readyBucket == wantBucket &&
			a.seekPreview.readySource != nil
		a.seekPreview.mu.Unlock()
		if ready {
			tt.Frame()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := tt.Find("进度预览画面"); !ok {
		t.Fatal("remote preview did not expand into thumbnail once frame arrived")
	}
	if len(spy.seekSnapshot()) != 0 {
		t.Fatalf("remote hover caused main seeks: %v", spy.seekSnapshot())
	}

	// Verify preview URL tagging and remote bucket coarsening helpers.
	if got := seekPreviewRequestURL("http://127.0.0.1:9999/s/id/v.mp4", false); got != "http://127.0.0.1:9999/s/id/v.mp4?preview=1" {
		t.Fatalf("unexpected loopback preview URL: %s", got)
	}
	if got := seekPreviewRequestURL("/tmp/local.mp4", true); got != "/tmp/local.mp4" {
		t.Fatalf("local path should not be modified: %s", got)
	}
	if got := seekPreviewRequestURL("rtsp://example.com/live", false); got != "rtsp://example.com/live" {
		t.Fatalf("non-loopback URL should not be modified: %s", got)
	}
	if seekPreviewBucketFor(195*time.Second, dur, false) != seekPreviewBucketFor(196*time.Second, dur, false) {
		t.Fatal("remote long video should coalesce 1s jitter into 3s bucket")
	}
}
