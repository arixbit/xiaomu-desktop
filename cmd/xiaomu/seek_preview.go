package main

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	mpvplayer "github.com/arixbit/mygo-player"
	"github.com/egoist/mygo/ui"
)

const (
	seekPreviewRenderW         = 320
	seekPreviewRenderH         = 180
	seekPreviewDefaultDebounce = 40 * time.Millisecond
	seekPreviewRemoteDebounce  = 180 * time.Millisecond
	seekPreviewLocalTimeout    = 3 * time.Second
	seekPreviewRemoteTimeout   = 6 * time.Second
	seekPreviewIdleCloseDelay  = 20 * time.Second
	seekPreviewKeyStep         = 5 * time.Second
)

type seekPreviewMode uint8

const (
	seekPreviewNone seekPreviewMode = iota
	seekPreviewPointer
	seekPreviewKeyboard
)

// seekPreviewMedia describes the active playback media address and options
// reused by the independent preview decoder without disturbing main playback.
type seekPreviewMedia struct {
	target  string
	opts    mpvplayer.OpenOptions
	isLocal bool
}

// seekPreviewBackend decodes preview frames on a background worker and returns
// a UI-thread getter for the ready ui.ImageSource.
type seekPreviewBackend interface {
	Fetch(ctx context.Context, media seekPreviewMedia, target, duration time.Duration, width, height, rotate int) (func() ui.ImageSource, error)
	Close()
}

// seekPreviewState manages playback progress bar hover/drag/keyboard preview
// interaction on the UI thread and serialized on-demand frame decoding off it.
type seekPreviewState struct {
	// UI-thread interaction state.
	active          bool
	mode            seekPreviewMode
	target          time.Duration
	dragging        bool
	dragValid       bool
	dragCanceled    bool
	suppressed      bool
	suppressX       float32
	suppressY       float32
	lastPointerX    float32
	lastPointerY    float32
	lastPointerOver bool
	axisRect        ui.Rect
	idleCloseAt     time.Time

	// Testable hooks (nil uses production defaults).
	newBackend func() seekPreviewBackend
	debounce   time.Duration

	// Thread-safe coordination with the single background decode worker.
	mu             sync.Mutex
	gen            uint64
	media          seekPreviewMedia
	desiredValid   bool
	desiredGen     uint64
	desiredBucket  int64
	desiredTarget  time.Duration
	desiredDur     time.Duration
	desiredRotate  int
	desiredAt      time.Time
	lastFetchStart time.Time

	loading      bool
	failed       bool
	failedGen    uint64
	failedBucket int64
	failedRotate int
	readyGen     uint64
	readyBucket  int64
	readyRotate  int
	readyTarget  time.Duration
	readySource  func() ui.ImageSource

	workerRunning bool
	wakeCh        chan struct{}
	stopCh        chan struct{}
	workerDone    chan struct{}
	cancelReq     context.CancelFunc
	cancelMedia   context.CancelFunc
	dispatchFn    func(func())
	invalidateFn  func()
}

func (a *app) canSeekPlayer(p mediaPlayer) bool {
	if p == nil || a.isLiveStream() || p.Duration() <= 0 {
		return false
	}
	if sp, ok := p.(seekableMediaPlayer); ok {
		return sp.Seekable()
	}
	return true
}

func (a *app) hasSeekPreview() bool {
	return a.seekPreview.active || a.seekPreview.dragging
}

func (a *app) setSeekPreviewMedia(media seekPreviewMedia) {
	s := &a.seekPreview
	s.mu.Lock()
	s.gen++
	s.media = media
	s.desiredValid = false
	s.loading = false
	s.failed = false
	s.failedGen = 0
	s.failedBucket = 0
	s.failedRotate = 0
	s.readyGen = 0
	s.readyBucket = 0
	s.readyRotate = 0
	s.readyTarget = 0
	s.readySource = nil
	cancelReq := s.cancelReq
	cancelMedia := s.cancelMedia
	wake := s.wakeCh
	s.mu.Unlock()

	if cancelReq != nil {
		cancelReq()
	}
	if cancelMedia != nil {
		cancelMedia()
	}
	signalNonBlocking(wake)
	wasOver := s.lastPointerOver
	px, py := s.lastPointerX, s.lastPointerY
	s.clearUIState()
	if wasOver {
		s.suppressed = true
		s.suppressX, s.suppressY = px, py
	}
}

// clearSeekPreview hides any active hover/keyboard preview UI without tearing
// down the reusable preview media configuration.
func (a *app) clearSeekPreview() {
	s := &a.seekPreview
	s.clearUIState()
	s.mu.Lock()
	s.desiredValid = false
	s.loading = false
	s.failed = false
	s.failedGen = 0
	s.failedBucket = 0
	s.failedRotate = 0
	cancelReq := s.cancelReq
	s.mu.Unlock()
	if cancelReq != nil {
		cancelReq()
	}
}

// dismissSeekPreview handles Escape: hides preview, cancels any in-progress
// drag, and suppresses immediate hover re-open until the pointer moves.
func (a *app) dismissSeekPreview() {
	s := &a.seekPreview
	wasDragging := s.dragging
	wasPointerOver := s.lastPointerOver
	px, py := s.lastPointerX, s.lastPointerY
	a.clearSeekPreview()
	if wasDragging {
		s.dragCanceled = true
	}
	if wasPointerOver {
		s.suppressed = true
		s.suppressX, s.suppressY = px, py
	}
}

// resetSeekPreview cancels all in-flight preview work, clears UI state, and
// shuts down the preview worker & mpv instance cleanly.
func (a *app) resetSeekPreview() {
	s := &a.seekPreview
	s.clearUIState()

	s.mu.Lock()
	s.gen++
	s.media = seekPreviewMedia{}
	s.desiredValid = false
	s.loading = false
	s.failed = false
	s.failedGen = 0
	s.failedBucket = 0
	s.failedRotate = 0
	s.readyGen = 0
	s.readyBucket = 0
	s.readyRotate = 0
	s.readyTarget = 0
	s.readySource = nil
	cancelReq := s.cancelReq
	cancelMedia := s.cancelMedia
	stopCh := s.stopCh
	done := s.workerDone
	running := s.workerRunning
	s.workerRunning = false
	s.stopCh = nil
	s.wakeCh = nil
	s.workerDone = nil
	s.dispatchFn = nil
	s.invalidateFn = nil
	s.mu.Unlock()

	if cancelReq != nil {
		cancelReq()
	}
	if cancelMedia != nil {
		cancelMedia()
	}
	if running && stopCh != nil {
		close(stopCh)
		if done != nil {
			<-done
		}
	}
}

func (s *seekPreviewState) clearUIState() {
	s.active = false
	s.mode = seekPreviewNone
	s.target = 0
	s.dragging = false
	s.dragValid = false
	s.dragCanceled = false
	s.suppressed = false
	s.idleCloseAt = time.Time{}
}

func (a *app) canDecodeSeekPreviewFrames(p mediaPlayer) bool {
	if a.seekPreview.newBackend != nil {
		return true
	}
	if !mpvBackendSupported() || p == nil {
		return false
	}
	type unwrapper interface {
		unwrapMediaPlayer() mediaPlayer
	}
	for {
		u, ok := p.(unwrapper)
		if !ok {
			break
		}
		p = u.unwrapMediaPlayer()
	}
	if _, ok := p.(*mpvMediaPlayer); !ok {
		return false
	}
	a.seekPreview.mu.Lock()
	hasTarget := a.seekPreview.media.target != ""
	a.seekPreview.mu.Unlock()
	return hasTarget
}

func seekPreviewBucketStep(duration time.Duration) time.Duration {
	switch {
	case duration <= 30*time.Second:
		return 250 * time.Millisecond
	case duration <= 5*time.Minute:
		return 500 * time.Millisecond
	default:
		return time.Second
	}
}

func seekPreviewBucket(target, duration time.Duration) int64 {
	return seekPreviewBucketFor(target, duration, true)
}

func seekPreviewBucketFor(target, duration time.Duration, isLocal bool) int64 {
	if target < 0 {
		target = 0
	}
	if duration > 0 && target > duration {
		target = duration
	}
	step := seekPreviewBucketStep(duration)
	if !isLocal && duration > 5*time.Minute {
		step = 3 * time.Second
	}
	if step <= 0 {
		return 0
	}
	return int64(math.Round(float64(target) / float64(step)))
}

func seekPreviewTimeForX(x, width float32, duration time.Duration) time.Duration {
	if width <= 0 || duration <= 0 {
		return 0
	}
	if x <= 1 {
		return 0
	}
	if x >= width-1 {
		return duration
	}
	frac := float64(x) / float64(width)
	d := time.Duration(math.Round(frac * float64(duration)))
	if d < 0 {
		return 0
	}
	if d > duration {
		return duration
	}
	return d
}

func seekPreviewPointerInReleaseArea(x, y, w, h float32) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	return x >= -4 && x <= w+4 && y >= -6 && y <= h+6
}

func (a *app) requestSeekPreviewFrame(c *ui.Context, p mediaPlayer, target, duration time.Duration) {
	if !a.canDecodeSeekPreviewFrames(p) || duration <= 0 {
		return
	}
	rotate := a.playerExtra.rotation
	var inv func()
	if c != nil {
		inv = c.Services().Invalidate
	}

	s := &a.seekPreview
	s.mu.Lock()
	if inv != nil {
		s.invalidateFn = inv
	}
	s.dispatchFn = a.dispatch
	gen := s.gen
	bucket := seekPreviewBucketFor(target, duration, s.media.isLocal)
	// Already have the exact frame ready for this generation, bucket, and rotation.
	if s.readyGen == gen && s.readyBucket == bucket && s.readyRotate == rotate && s.readySource != nil && !s.failed {
		s.desiredValid = false
		s.loading = false
		s.mu.Unlock()
		return
	}
	// Already failed for this exact generation, bucket, and rotation while
	// remaining on the same target: stay on the time-only fallback instead of
	// retrying on every frame.
	if s.failed && s.failedGen == gen && s.failedBucket == bucket && s.failedRotate == rotate {
		s.mu.Unlock()
		return
	}
	// Already queued/fetching the exact same bucket and rotation for this generation.
	if s.desiredValid && s.desiredGen == gen && s.desiredBucket == bucket && s.desiredRotate == rotate && s.loading {
		s.mu.Unlock()
		return
	}
	s.desiredValid = true
	s.desiredGen = gen
	s.desiredBucket = bucket
	s.desiredTarget = target
	s.desiredDur = duration
	s.desiredRotate = rotate
	s.desiredAt = time.Now()
	s.loading = true
	s.failed = false
	s.failedGen = 0
	s.failedBucket = 0
	s.failedRotate = 0

	cancelReq := s.cancelReq
	if !s.workerRunning {
		s.workerRunning = true
		s.wakeCh = make(chan struct{}, 1)
		s.stopCh = make(chan struct{})
		s.workerDone = make(chan struct{})
		wakeCh := s.wakeCh
		stopCh := s.stopCh
		doneCh := s.workerDone
		go a.seekPreviewWorker(wakeCh, stopCh, doneCh)
	}
	wakeCh := s.wakeCh
	s.mu.Unlock()

	if cancelReq != nil {
		cancelReq()
	}
	signalNonBlocking(wakeCh)
}

func (a *app) notifySeekPreviewUI(stopCh <-chan struct{}) {
	select {
	case <-stopCh:
		return
	default:
	}
	s := &a.seekPreview
	s.mu.Lock()
	running := s.workerRunning
	dispatch := s.dispatchFn
	inv := s.invalidateFn
	s.mu.Unlock()
	if !running {
		return
	}
	if dispatch != nil {
		dispatch(func() {})
	}
	if inv != nil {
		inv()
	}
}

func (a *app) seekPreviewWorker(wakeCh, stopCh, doneCh chan struct{}) {
	defer close(doneCh)

	var backend seekPreviewBackend
	var backendGen uint64
	defer func() {
		if backend != nil {
			backend.Close()
		}
	}()

	s := &a.seekPreview
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		s.mu.Lock()
		valid := s.desiredValid
		gen := s.desiredGen
		curGen := s.gen
		bucket := s.desiredBucket
		target := s.desiredTarget
		dur := s.desiredDur
		rotate := s.desiredRotate
		media := s.media
		desiredAt := s.desiredAt
		lastStart := s.lastFetchStart
		debounce := s.debounce
		factory := s.newBackend
		if !valid && backend != nil && backendGen != curGen {
			// Media changed while idle: drop the old backend immediately.
			old := backend
			backend = nil
			backendGen = 0
			s.mu.Unlock()
			old.Close()
			continue
		}
		s.mu.Unlock()

		if !valid || gen != curGen {
			select {
			case <-stopCh:
				return
			case <-wakeCh:
			}
			continue
		}

		// Coalesce rapid hover movements: first hover starts immediately,
		// rapid consecutive sweeps wait briefly for pointer to settle.
		if debounce == 0 && factory == nil {
			if media.isLocal {
				debounce = seekPreviewDefaultDebounce
			} else {
				debounce = seekPreviewRemoteDebounce
			}
		}
		if debounce > 0 && !lastStart.IsZero() && time.Since(lastStart) < debounce*2 {
			wait := debounce - time.Since(desiredAt)
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-stopCh:
					timer.Stop()
					return
				case <-wakeCh:
					timer.Stop()
					continue
				case <-timer.C:
				}
			}
		}

		// Re-read latest desired target after coalesce wait.
		s.mu.Lock()
		if !s.desiredValid || s.desiredGen != s.gen {
			s.mu.Unlock()
			continue
		}
		gen = s.desiredGen
		bucket = s.desiredBucket
		target = s.desiredTarget
		dur = s.desiredDur
		rotate = s.desiredRotate
		media = s.media
		factory = s.newBackend
		s.lastFetchStart = time.Now()
		s.mu.Unlock()

		if backend != nil && backendGen != gen {
			backend.Close()
			backend = nil
			backendGen = 0
		}
		if backend == nil {
			if factory != nil {
				backend = factory()
			} else if mpvBackendSupported() {
				backend = newMPVSeekPreviewBackend()
			}
			backendGen = gen
		}
		if backend == nil {
			s.mu.Lock()
			if s.gen == gen && s.desiredGen == gen && s.desiredBucket == bucket && s.desiredRotate == rotate {
				s.desiredValid = false
				s.loading = false
				s.failed = true
				s.failedGen = gen
				s.failedBucket = bucket
				s.failedRotate = rotate
			}
			s.mu.Unlock()
			a.notifySeekPreviewUI(stopCh)
			continue
		}

		timeout := seekPreviewRemoteTimeout
		if media.isLocal {
			timeout = seekPreviewLocalTimeout
		}
		reqCtx, cancelReq := context.WithTimeout(context.Background(), timeout)
		s.mu.Lock()
		// Re-check generation before registering cancel so a concurrent reset
		// cannot miss cancelling this request.
		if s.gen != gen || !s.desiredValid || s.desiredGen != gen || s.desiredBucket != bucket || s.desiredRotate != rotate {
			s.mu.Unlock()
			cancelReq()
			continue
		}
		// Invalidate any previously ready frame before Fetch mutates a shared
		// single-player backend (such as libmpv) to a different target, so a
		// rapid pointer return to the old bucket cannot display the new frame.
		if s.readyGen != gen || s.readyBucket != bucket || s.readyRotate != rotate {
			s.readyGen = 0
			s.readyBucket = 0
			s.readyRotate = 0
			s.readyTarget = 0
			s.readySource = nil
		}
		s.cancelReq = cancelReq
		s.mu.Unlock()

		srcGetter, err := backend.Fetch(reqCtx, media, target, dur, seekPreviewRenderW, seekPreviewRenderH, rotate)
		cancelReq()

		s.mu.Lock()
		s.cancelReq = nil
		// Drop stale or superseded results unconditionally: old targets and old
		// media must never overwrite a newer preview state.
		if s.gen != gen || !s.desiredValid || s.desiredGen != gen || s.desiredBucket != bucket || s.desiredRotate != rotate {
			s.mu.Unlock()
			continue
		}
		s.desiredValid = false
		s.loading = false
		if err != nil || srcGetter == nil {
			s.failed = true
			s.failedGen = gen
			s.failedBucket = bucket
			s.failedRotate = rotate
			if s.readyGen != gen || s.readyBucket != bucket || s.readyRotate != rotate {
				s.readySource = nil
			}
		} else {
			s.failed = false
			s.failedGen = 0
			s.failedBucket = 0
			s.failedRotate = 0
			s.readyGen = gen
			s.readyBucket = bucket
			s.readyRotate = rotate
			s.readyTarget = target
			s.readySource = srcGetter
		}
		s.mu.Unlock()
		a.notifySeekPreviewUI(stopCh)
	}
}

func (a *app) commitSeekTarget(p mediaPlayer, target, duration time.Duration) {
	if p == nil || !a.canSeekPlayer(p) || duration <= 0 {
		return
	}
	if target < 0 {
		target = 0
	} else if target > duration {
		target = duration
	}
	a.resumeAt, a.resumeIssued = 0, false
	a.episodeEnd = playbackEnd{}
	p.Seek(target)
	a.syncNowPlayingState()
}

// playerSeekBar renders the combined danmaku highlight curve + progress track
// with hover/drag/keyboard preview and commit-on-release seeking.
func (a *app) playerSeekBar(c *ui.Context, p mediaPlayer, duration time.Duration) {
	canSeek := a.canSeekPlayer(p)
	s := &a.seekPreview
	now := c.Now()

	// Reclaim idle preview decoder memory after prolonged inactivity.
	if !s.active && !s.dragging && !s.idleCloseAt.IsZero() {
		if !now.Before(s.idleCloseAt) {
			s.idleCloseAt = time.Time{}
			a.stopIdleSeekPreviewWorker()
		} else {
			c.After(s.idleCloseAt.Sub(now))
		}
	}

	if !canSeek {
		if s.active || s.dragging {
			a.clearSeekPreview()
		}
		a.renderReadOnlySeekBar(c, p, duration)
		return
	}

	// Capture UI invalidation and dispatch callbacks proactively so
	// HandleInput-initiated fetches can always wake the view cleanly without
	// reading app fields off the UI thread.
	s.mu.Lock()
	s.dispatchFn = a.dispatch
	s.invalidateFn = c.Services().Invalidate
	s.mu.Unlock()

	hasCurve := a.seekPreviewHasDanmakuCurve(p)
	pos := p.Position()
	if pos < 0 {
		pos = 0
	} else if pos > duration {
		pos = duration
	}
	posFrac := float32(min(max(pos.Seconds()/duration.Seconds(), 0), 1))

	seekAxis := ui.Column(c.Key("player-seek-bar")).
		Grow(1).MinWidth(0).Gap(2).
		Focusable().FocusRing(false).
		Role(ui.RoleSlider).
		Range(0, duration.Seconds(), pos.Seconds()).
		Step(seekPreviewKeyStep.Seconds()).
		Label("播放进度").
		Cursor(ui.CursorPointer)

	// Record laid-out bounds for both HandleInput coordinate conversion and
	// floating overlay positioning.
	axisBounds := seekAxis.Bounds()
	if axisBounds.W > 0 && axisBounds.H > 0 {
		s.axisRect = axisBounds
	}

	seekAxis.HandleInput(func(ev ui.InputEvent) bool {
		if p != a.player || a.closed || !a.canSeekPlayer(p) {
			return false
		}
		w, h := s.axisRect.W, s.axisRect.H
		if w <= 0 {
			w = seekAxis.Bounds().W
		}
		if h <= 0 {
			h = seekAxis.Bounds().H
		}
		switch ev.Kind {
		case ui.InputPointerDown:
			if ev.Button != 0 || ev.Mods != 0 {
				return false
			}
			s.dragging = true
			s.dragCanceled = false
			s.suppressed = false
			s.dragValid = seekPreviewPointerInReleaseArea(ev.X, ev.Y, w, h)
			s.lastPointerX, s.lastPointerY, s.lastPointerOver = ev.X, ev.Y, true
			if s.dragValid {
				s.active = true
				s.mode = seekPreviewPointer
				s.target = seekPreviewTimeForX(ev.X, w, duration)
				s.idleCloseAt = time.Time{}
				a.requestSeekPreviewFrame(nil, p, s.target, duration)
			}
			a.playerOverlay.keepVisible = true
			return true

		case ui.InputPointerMove:
			if s.dragging {
				if s.dragCanceled {
					return true
				}
				s.lastPointerX, s.lastPointerY = ev.X, ev.Y
				valid := seekPreviewPointerInReleaseArea(ev.X, ev.Y, w, h)
				s.dragValid = valid
				if valid {
					s.active = true
					s.mode = seekPreviewPointer
					s.target = seekPreviewTimeForX(ev.X, w, duration)
					s.idleCloseAt = time.Time{}
					a.requestSeekPreviewFrame(nil, p, s.target, duration)
				} else {
					s.active = false
				}
				a.playerOverlay.keepVisible = true
				return true
			}
			return false

		case ui.InputPointerUp:
			if !s.dragging || ev.Button != 0 {
				return false
			}
			canceled := ev.Canceled || s.dragCanceled
			valid := !canceled && seekPreviewPointerInReleaseArea(ev.X, ev.Y, w, h)
			releaseTarget := seekPreviewTimeForX(ev.X, w, duration)
			s.dragging = false
			s.dragValid = false
			s.dragCanceled = false
			if valid {
				a.commitSeekTarget(p, releaseTarget, duration)
				// Keep pointer preview synced if cursor remains over the bar;
				// next frame's PointerPosition check hides it if outside.
				if ev.X >= 0 && ev.X <= w && ev.Y >= 0 && ev.Y <= h {
					s.active = true
					s.mode = seekPreviewPointer
					s.target = releaseTarget
				} else {
					s.active = false
					s.mode = seekPreviewNone
					s.idleCloseAt = ev.Time.Add(seekPreviewIdleCloseDelay)
				}
			} else {
				s.active = false
				s.mode = seekPreviewNone
				s.idleCloseAt = ev.Time.Add(seekPreviewIdleCloseDelay)
				if canceled {
					s.suppressed = true
					s.suppressX, s.suppressY = ev.X, ev.Y
				}
			}
			return true
		}
		return false
	})

	// Keyboard preview while the seek bar has keyboard focus.
	focused := seekAxis.Focused()
	if seekAxis.FocusLeft() {
		if s.mode == seekPreviewKeyboard {
			a.clearSeekPreview()
			s.idleCloseAt = now.Add(seekPreviewIdleCloseDelay)
		}
	}
	stepPreview := func(delta time.Duration) {
		base := pos
		if s.active {
			base = s.target
		}
		next := min(max(base+delta, 0), duration)
		s.active = true
		s.mode = seekPreviewKeyboard
		s.suppressed = false
		s.target = next
		s.idleCloseAt = time.Time{}
		a.playerOverlay.keepVisible = true
		a.requestSeekPreviewFrame(c, p, next, duration)
	}
	if seekAxis.Shortcut(0, ui.KeyLeft) {
		stepPreview(-seekPreviewKeyStep)
	}
	if seekAxis.Shortcut(0, ui.KeyRight) {
		stepPreview(seekPreviewKeyStep)
	}
	if seekAxis.Shortcut(0, ui.KeyHome) {
		stepPreview(-duration)
	}
	if seekAxis.Shortcut(0, ui.KeyEnd) {
		stepPreview(duration)
	}
	if seekAxis.Shortcut(0, ui.KeyEnter) {
		if s.active {
			commitTarget := s.target
			a.commitSeekTarget(p, commitTarget, duration)
			a.clearSeekPreview()
			s.idleCloseAt = now.Add(seekPreviewIdleCloseDelay)
		}
	}
	if !focused && s.mode == seekPreviewKeyboard {
		a.clearSeekPreview()
		s.idleCloseAt = now.Add(seekPreviewIdleCloseDelay)
	}

	// Pointer hover tracking across both the danmaku highlight curve and track.
	px, py, over := seekAxis.PointerPosition()
	if s.dragging {
		a.playerOverlay.keepVisible = true
		if s.active {
			a.requestSeekPreviewFrame(c, p, s.target, duration)
		}
	} else if over {
		moved := !s.lastPointerOver || px != s.lastPointerX || py != s.lastPointerY
		s.lastPointerX, s.lastPointerY, s.lastPointerOver = px, py, true
		if s.suppressed {
			if seekAbsf(px-s.suppressX) > 1 || seekAbsf(py-s.suppressY) > 1 {
				s.suppressed = false
			}
		}
		if !s.suppressed && (moved || s.mode != seekPreviewKeyboard) {
			w := seekAxis.Bounds().W
			if w <= 0 {
				w = s.axisRect.W
			}
			if w > 0 {
				s.active = true
				s.mode = seekPreviewPointer
				s.target = seekPreviewTimeForX(px, w, duration)
				s.idleCloseAt = time.Time{}
				a.playerOverlay.keepVisible = true
				a.requestSeekPreviewFrame(c, p, s.target, duration)
			}
		}
	} else {
		s.lastPointerOver = false
		s.suppressed = false
		if s.mode == seekPreviewPointer {
			a.clearSeekPreview()
			s.idleCloseAt = now.Add(seekPreviewIdleCloseDelay)
		}
	}

	if s.active && s.mode == seekPreviewKeyboard {
		a.playerOverlay.keepVisible = true
	}

	accent := c.Theme().Accent
	focusVis := seekAxis.FocusVisible()
	hoverActive := over || s.dragging || s.active
	hoverTarget := float32(0)
	if hoverActive {
		hoverTarget = 1
	}
	dragTarget := float32(0)
	if s.dragging {
		dragTarget = 1
	}
	hoverT := seekAxis.AnimateTo("seek-hover", hoverTarget, animSnap)
	dragT := seekAxis.AnimateTo("seek-drag", dragTarget, animSnap)

	seekAxis.Children(func() {
		if hasCurve {
			a.danmakuHighlightCurve(c, p, duration)
		}
		ui.Box(c.Key("player-seek-track")).FillWidth().Height(20).PassThrough().Draw(func(pt *ui.Painter, r ui.Rect) {
			if r.W <= 0 || r.H <= 0 {
				return
			}
			// Smooth spring thickness/size on hover & drag while keeping X tracking 100% direct.
			trackH := 4 + 1.5*hoverT
			knobD := 11 + 1.5*hoverT + 1.5*dragT
			cy := r.Y + r.H/2
			rail := ui.Rect{X: r.X, Y: cy - trackH/2, W: r.W, H: trackH}
			pt.Fill(rail, ui.RGBA(255, 255, 255, 0.24), trackH/2)

			playedW := r.W * posFrac
			if playedW > 0 {
				pt.Fill(ui.Rect{X: r.X, Y: rail.Y, W: playedW, H: trackH}, accent, trackH/2)
			}

			// Subtle hover range hint without moving the real playback knob.
			if s.active {
				previewFrac := float32(min(max(s.target.Seconds()/duration.Seconds(), 0), 1))
				hx := r.X + r.W*previewFrac
				if hx > r.X+playedW {
					alpha := float32(0.18) * max(hoverT, 0.35)
					pt.Fill(ui.Rect{X: r.X + playedW, Y: rail.Y, W: hx - (r.X + playedW), H: trackH}, ui.RGBA(255, 255, 255, alpha), trackH/2)
				}
			}

			kx := r.X + r.W*posFrac
			knob := ui.Rect{X: kx - knobD/2, Y: cy - knobD/2, W: knobD, H: knobD}
			pt.Shadow(knob, knobD/2, 0, 1, 4, 0, ui.RGBA(0, 0, 0, 0.42))
			pt.Fill(knob, ui.RGB(250, 250, 252), knobD/2)

			if focusVis {
				pt.FocusRing(ui.Rect{X: r.X - 2, Y: cy - 8, W: r.W + 4, H: 16}, [4]float32{8, 8, 8, 8})
			}
		})
	})

	// Paint the vertical hover guide line across both curve and track without
	// affecting layout or intercepting input.
	seekAxis.DrawOver(func(pt *ui.Painter, r ui.Rect) {
		if r.W > 0 && r.H > 0 {
			s.axisRect = r
		}
		if !s.active || r.W <= 0 || r.H <= 0 || duration <= 0 {
			return
		}
		previewFrac := float32(min(max(s.target.Seconds()/duration.Seconds(), 0), 1))
		gx := r.X + r.W*previewFrac
		gx = min(max(gx, r.X), r.X+r.W-1)
		top := r.Y + 1
		bot := r.Y + r.H - 4
		if bot > top {
			guideAlpha := float32(0.78) * max(hoverT, 0.45)
			pt.Fill(ui.Rect{X: gx, Y: top, W: 1.25, H: bot - top}, ui.RGBA(255, 255, 255, guideAlpha), 0.5)
		}
	})

	if s.active {
		a.renderSeekPreviewOverlay(c, p, duration, seekAxis.Bounds())
	}
}

func (a *app) seekPreviewHasDanmakuCurve(p mediaPlayer) bool {
	return p != nil && !a.isLiveStream() && a.canUseDanmaku() &&
		a.preferences.Danmaku.Enabled && len(a.danmaku.comments) > 0 && p.Duration() > 0
}

func (a *app) renderReadOnlySeekBar(c *ui.Context, p mediaPlayer, duration time.Duration) {
	pos := p.Position()
	if pos < 0 {
		pos = 0
	} else if pos > duration {
		pos = duration
	}
	posFrac := float32(min(max(pos.Seconds()/duration.Seconds(), 0), 1))
	accent := c.Theme().Accent
	ui.Column(c.Key("player-seek-readonly")).Grow(1).MinWidth(0).Gap(2).Label("播放进度").Children(func() {
		if a.seekPreviewHasDanmakuCurve(p) {
			a.danmakuHighlightCurve(c, p, duration)
		}
		ui.Box(c).FillWidth().Height(20).PassThrough().Draw(func(pt *ui.Painter, r ui.Rect) {
			if r.W <= 0 || r.H <= 0 {
				return
			}
			trackH := float32(4)
			cy := r.Y + r.H/2
			rail := ui.Rect{X: r.X, Y: cy - trackH/2, W: r.W, H: trackH}
			pt.Fill(rail, ui.RGBA(255, 255, 255, 0.18), trackH/2)
			if w := r.W * posFrac; w > 0 {
				pt.Fill(ui.Rect{X: r.X, Y: rail.Y, W: w, H: trackH}, accent.Alpha(0.75), trackH/2)
			}
		})
	})
}

func (a *app) renderSeekPreviewOverlay(c *ui.Context, p mediaPlayer, duration time.Duration, axisRect ui.Rect) {
	s := &a.seekPreview
	if axisRect.W <= 0 || axisRect.H <= 0 {
		axisRect = s.axisRect
	}
	if axisRect.W <= 0 || axisRect.H <= 0 || duration <= 0 {
		return
	}

	winW, _ := c.Size()
	target := s.target
	timeText := playerClock(target)

	var (
		imgSrc  ui.ImageSource
		loading bool
		failed  bool
		isLocal bool
	)
	canDecode := a.canDecodeSeekPreviewFrames(p)
	if canDecode {
		rotate := a.playerExtra.rotation
		s.mu.Lock()
		gen := s.gen
		isLocal = s.media.isLocal
		bucket := seekPreviewBucketFor(target, duration, isLocal)
		if s.readyGen == gen && s.readyBucket == bucket && s.readyRotate == rotate && s.readySource != nil {
			getter := s.readySource
			s.mu.Unlock()
			imgSrc = getter()
		} else {
			loading = s.loading
			failed = s.failed && s.failedGen == gen && s.failedBucket == bucket && s.failedRotate == rotate
			s.mu.Unlock()
		}
	}

	previewFrac := float32(min(max(target.Seconds()/duration.Seconds(), 0), 1))
	hoverX := axisRect.X + axisRect.W*previewFrac

	// Remote streams start as a compact time pill and expand into a thumbnail
	// card only once a real frame is ready, avoiding an empty loading box on
	// slow networks while keeping local files snappy.
	showThumbCard := imgSrc != nil || (canDecode && isLocal && loading && !failed)
	if showThumbCard {
		cardW := min(max(winW*0.16, 128), 188)
		if axisRect.W >= 96 && cardW > axisRect.W {
			cardW = axisRect.W
		}
		cardW = float32(math.Round(float64(cardW)))
		cardH := float32(math.Round(float64(cardW * 9 / 16)))

		minX := axisRect.X
		maxX := axisRect.X + max(float32(0), axisRect.W-cardW)
		cardX := min(max(hoverX-cardW/2, minX), maxX)
		if winW > 0 {
			cardX = min(max(cardX, 8), max(float32(8), winW-8-cardW))
		}
		cardY := max(float32(8), axisRect.Y-cardH-20)
		cardX = float32(math.Round(float64(cardX)))
		cardY = float32(math.Round(float64(cardY)))

		ui.Overlay(c, func() {
			card := ui.Box(c.Key("seek-preview-card")).Absolute().
				Left(cardX).Top(cardY).Size(cardW, cardH).
				Radius(9).Clip().
				Background(ui.RGB(18, 22, 32)).
				Shadow(0, 10, 26, -4, ui.RGBA(0, 0, 0, 0.64)).
				PassThrough().Label("进度预览").Transition(transSeekPreview)
			card.Children(func() {
				if imgSrc != nil {
					ui.Image(c.Key("seek-preview-frame"), imgSrc).Fill().Fit(ui.Contain).
						Background(ui.RGB(10, 14, 22)).PassThrough().Label("进度预览画面")
				} else {
					ui.Row(c).Fill().Center().Gap(6).Padding(0, 8, 20, 8).PassThrough().Children(func() {
						loadingIndicator(c, 11, ui.RGBA(248, 249, 255, 0.56))
						ui.Text(c, "加载画面…").FontSize(11).TextColor(ui.RGBA(248, 249, 255, 0.56)).SingleLine().PassThrough()
					})
				}
				ui.Row(c).Absolute().Bottom(5).Left(0).Right(0).Justify(ui.Center).PassThrough().Children(func() {
					ui.Row(c).Padding(2, 7).Radius(5).
						Background(ui.RGBA(12, 16, 26, 0.86)).
						Border(1, ui.RGBA(255, 255, 255, 0.16)).
						PassThrough().Children(func() {
						ui.Text(c, timeText).FontSize(11.5).Bold().FontFeatures("tnum").TextColor(playerText).SingleLine().PassThrough()
					})
				})
			})
			card.DrawOver(func(pt *ui.Painter, r ui.Rect) {
				pt.Stroke(ui.Rect{X: r.X + 0.5, Y: r.Y + 0.5, W: r.W - 1, H: r.H - 1}, ui.RGBA(255, 255, 255, 0.52), 8.5, 1)
			})
		})
		return
	}

	// Time-only fallback pill (e.g., frame unavailable, failed, or non-mpv backend).
	pillW := float32(62)
	if len(timeText) > 5 {
		pillW = 74
	}
	pillH := float32(26)
	minX := axisRect.X
	maxX := axisRect.X + max(float32(0), axisRect.W-pillW)
	pillX := min(max(hoverX-pillW/2, minX), maxX)
	if winW > 0 {
		pillX = min(max(pillX, 8), max(float32(8), winW-8-pillW))
	}
	pillY := max(float32(8), axisRect.Y-pillH-18)
	pillX = float32(math.Round(float64(pillX)))
	pillY = float32(math.Round(float64(pillY)))

	ui.Overlay(c, func() {
		ui.Row(c.Key("seek-preview-pill")).Absolute().
			Left(pillX).Top(pillY).MinWidth(pillW).Height(pillH).
			Padding(0, 9).Center().Radius(8).
			Background(ui.RGBA(16, 20, 32, 0.94)).
			Border(1, ui.RGBA(255, 255, 255, 0.30)).
			Shadow(0, 6, 18, -4, ui.RGBA(0, 0, 0, 0.52)).
			PassThrough().Label("进度预览").Transition(transSeekPreview).Children(func() {
			ui.Text(c, timeText).FontSize(12).Bold().FontFeatures("tnum").TextColor(playerText).SingleLine().PassThrough()
		})
	})
}

func (a *app) stopIdleSeekPreviewWorker() {
	s := &a.seekPreview
	s.mu.Lock()
	if !s.workerRunning || s.desiredValid {
		s.mu.Unlock()
		return
	}
	stopCh := s.stopCh
	s.workerRunning = false
	s.stopCh = nil
	s.wakeCh = nil
	s.workerDone = nil
	s.failed = false
	s.failedGen = 0
	s.failedBucket = 0
	s.failedRotate = 0
	s.readySource = nil
	s.readyGen = 0
	s.readyBucket = 0
	s.readyRotate = 0
	s.mu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
}

func signalNonBlocking(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func seekAbsf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// mpvSeekPreviewBackend manages a single muted, paused, small-viewport
// mygo-player (libmpv) instance dedicated exclusively to seek preview frames.
type mpvSeekPreviewBackend struct {
	mu           sync.RWMutex
	p            *mpvplayer.Player
	stateCh      chan struct{}
	openedTarget string
	openedRotate int
	renderW      int
	renderH      int
	lastPos      time.Duration
	frameSeq     uint64
	fileLoaded   bool
	hasRendered  bool
	closed       bool
}

func newMPVSeekPreviewBackend() *mpvSeekPreviewBackend {
	return &mpvSeekPreviewBackend{}
}

func (b *mpvSeekPreviewBackend) Close() {
	b.mu.Lock()
	b.closed = true
	b.frameSeq++
	p := b.p
	b.p = nil
	b.openedTarget = ""
	b.fileLoaded = false
	b.hasRendered = false
	b.mu.Unlock()
	if p != nil {
		_ = p.Close()
	}
}

func (b *mpvSeekPreviewBackend) resetPlayer() {
	b.mu.Lock()
	b.frameSeq++
	p := b.p
	b.p = nil
	b.openedTarget = ""
	b.openedRotate = 0
	b.fileLoaded = false
	b.hasRendered = false
	b.mu.Unlock()
	if p != nil {
		_ = p.Close()
	}
}

func (b *mpvSeekPreviewBackend) imageSourceFor(p *mpvplayer.Player, seq uint64) func() ui.ImageSource {
	return func() ui.ImageSource {
		b.mu.RLock()
		defer b.mu.RUnlock()
		if b.closed || b.p != p || p == nil || !b.hasRendered || b.frameSeq != seq {
			return nil
		}
		return p.ImageSource()
	}
}

func clampPreviewDecodeTarget(target, duration time.Duration) time.Duration {
	if target < 0 {
		target = 0
	}
	if duration > 300*time.Millisecond && target > duration-200*time.Millisecond {
		target = duration - 200*time.Millisecond
	}
	return target
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func previewPositionMatches(pos, target, duration time.Duration, eof, isLocal bool) bool {
	if eof && duration > 0 && target >= duration-time.Second {
		return true
	}
	var tol time.Duration
	if isLocal {
		tol = max(600*time.Millisecond, duration/40)
		if tol > 2*time.Second {
			tol = 2 * time.Second
		}
	} else {
		tol = max(600*time.Millisecond, duration/30)
		if tol > 5*time.Second {
			tol = 5 * time.Second
		}
	}
	return absDuration(pos-target) <= tol
}

// seekPreviewRequestURL tags loopback proxy URLs with preview=1 so the proxy
// skips heavy multi-chunk cloud-drive prefetch and uses a shorter idle timeout
// without disturbing main playback.
func seekPreviewRequestURL(raw string, isLocal bool) string {
	if isLocal || !strings.HasPrefix(raw, "http://127.0.0.1:") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Get("preview") == "1" {
		return raw
	}
	q.Set("preview", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

func mpvLivePosition(p *mpvplayer.Player, fallback time.Duration) time.Duration {
	raw, err := p.GetPropertyString("time-pos")
	if err != nil || raw == "" {
		return fallback
	}
	sec, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(sec) || math.IsInf(sec, 0) || sec < 0 {
		return fallback
	}
	return time.Duration(sec * float64(time.Second))
}

func (b *mpvSeekPreviewBackend) Fetch(ctx context.Context, media seekPreviewMedia, target, duration time.Duration, width, height, rotate int) (func() ui.ImageSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if media.target == "" {
		return nil, errors.New("seek preview: empty media target")
	}

	width &^= 1
	height &^= 1
	if width < 64 {
		width = seekPreviewRenderW
	}
	if height < 36 {
		height = seekPreviewRenderH
	}
	decodeTarget := clampPreviewDecodeTarget(target, duration)

	b.mu.RLock()
	closed := b.closed
	p := b.p
	openedTarget := b.openedTarget
	openedRotate := b.openedRotate
	renderW, renderH := b.renderW, b.renderH
	lastPos := b.lastPos
	frameSeq := b.frameSeq
	fileLoaded := b.fileLoaded
	hasRendered := b.hasRendered
	stateCh := b.stateCh
	b.mu.RUnlock()
	if closed {
		return nil, errors.New("seek preview: backend closed")
	}

	needOpen := p == nil || openedTarget != media.target
	if needOpen {
		b.resetPlayer()
		stateCh = make(chan struct{}, 1)
		keepOpen := true
		extra := map[string]string{
			"aid":                    "no",
			"sid":                    "no",
			"sub-auto":               "no",
			"cache":                  "yes",
			"cache-secs":             "3",
			"demuxer-readahead-secs": "1",
			"demuxer-max-bytes":      "16MiB",
			"demuxer-max-back-bytes": "2MiB",
			"vd-lavc-fast":           "yes",
			"vd-lavc-skiploopfilter": "all",
		}
		if !media.isLocal {
			// Land on nearest keyframe for remote streams instead of decoding
			// an entire GOP frame-by-frame to an exact millisecond.
			extra["hr-seek"] = "no"
		}
		newP, err := mpvplayer.New(mpvplayer.Options{
			KeepOpen:     &keepOpen,
			StartMuted:   true,
			AudioOutput:  "null",
			ExtraOptions: extra,
			OnStateChange: func() {
				signalNonBlocking(stateCh)
			},
		})
		if err != nil {
			return nil, err
		}
		newP.SetRenderSize(width, height)
		if rotate != 0 {
			_ = newP.SetVideoRotate(rotate)
		}

		openOpts := media.opts
		openOpts.Paused = true
		openOpts.Subtitles = nil
		openOpts.StartTime = decodeTarget
		openTarget := seekPreviewRequestURL(media.target, media.isLocal)
		if err := newP.Open(openTarget, openOpts); err != nil {
			_ = newP.Close()
			return nil, err
		}

		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			_ = newP.Close()
			return nil, errors.New("seek preview: backend closed")
		}
		b.frameSeq++
		frameSeq = b.frameSeq
		b.p = newP
		b.stateCh = stateCh
		b.openedTarget = media.target
		b.openedRotate = rotate
		b.renderW, b.renderH = width, height
		b.fileLoaded = false
		b.hasRendered = false
		b.mu.Unlock()
		p = newP
		fileLoaded = false
		hasRendered = false
	} else {
		if renderW != width || renderH != height {
			p.SetRenderSize(width, height)
			b.mu.Lock()
			b.renderW, b.renderH = width, height
			b.mu.Unlock()
		}
		if openedRotate != rotate {
			_ = p.SetVideoRotate(rotate)
			b.mu.Lock()
			b.openedRotate = rotate
			b.mu.Unlock()
		}
	}

	if !fileLoaded {
		pos, ok, err := waitForMPVPreviewLoad(ctx, p, stateCh)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				b.resetPlayer()
			}
			return nil, err
		}
		b.mu.Lock()
		b.fileLoaded = true
		b.mu.Unlock()
		if ok {
			snap := p.Snapshot()
			if previewPositionMatches(pos, decodeTarget, duration, snap.EOF, media.isLocal) {
				b.mu.Lock()
				b.lastPos = pos
				b.hasRendered = true
				seq := b.frameSeq
				b.mu.Unlock()
				return b.imageSourceFor(p, seq), nil
			}
		}
		// Initial start time differed from requested target; fall through to explicit seek.
		hasRendered = false
	} else if hasRendered && p.FramesRendered() > 0 && absDuration(lastPos-decodeTarget) <= 80*time.Millisecond {
		if seeking, _ := p.GetPropertyString("seeking"); seeking != "yes" {
			livePos := mpvLivePosition(p, lastPos)
			if absDuration(livePos-decodeTarget) <= 120*time.Millisecond {
				return b.imageSourceFor(p, frameSeq), nil
			}
		}
	}

	// Invalidate rendered state before sending Seek so a canceled seek can
	// never reuse stale lastPos or expose a mutated texture under an old seq.
	b.mu.Lock()
	b.frameSeq++
	b.hasRendered = false
	seq := b.frameSeq
	b.mu.Unlock()

	beforeFrames := p.FramesRendered()
	var seekErr error
	if !media.isLocal {
		// Setting time-pos respects hr-seek=no (fast keyframe seek) rather than
		// mygo-player.Seek's absolute+exact full-GOP decode.
		seekErr = p.SetPropertyString("time-pos", strconv.FormatFloat(decodeTarget.Seconds(), 'f', 3, 64))
		if seekErr != nil {
			seekErr = p.Seek(decodeTarget)
		}
	} else {
		seekErr = p.Seek(decodeTarget)
	}
	if seekErr != nil {
		b.resetPlayer()
		return nil, seekErr
	}
	pos, err := waitForMPVPreviewSeek(ctx, p, stateCh, beforeFrames, decodeTarget, duration, media.isLocal)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			b.resetPlayer()
		}
		return nil, err
	}
	b.mu.Lock()
	b.lastPos = pos
	b.hasRendered = true
	b.mu.Unlock()
	return b.imageSourceFor(p, seq), nil
}

func waitForMPVPreviewLoad(ctx context.Context, p *mpvplayer.Player, stateCh <-chan struct{}) (time.Duration, bool, error) {
	ticker := time.NewTicker(8 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		snap := p.Snapshot()
		if snap.Err != nil {
			return 0, false, snap.Err
		}
		loaded := snap.State == mpvplayer.StatePaused || snap.State == mpvplayer.StateEnded || (snap.VideoWidth > 0 && p.FramesRendered() > 0)
		if loaded && p.FramesRendered() > 0 {
			seeking, _ := p.GetPropertyString("seeking")
			if seeking != "yes" {
				return mpvLivePosition(p, snap.Position), true, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case <-stateCh:
		case <-ticker.C:
		}
	}
}

func waitForMPVPreviewSeek(ctx context.Context, p *mpvplayer.Player, stateCh <-chan struct{}, beforeFrames uint64, target, duration time.Duration, isLocal bool) (time.Duration, error) {
	ticker := time.NewTicker(8 * time.Millisecond)
	defer ticker.Stop()

	var frameArrivedAt time.Time
	var lastSeenFrames uint64
	var settledSameFrameAt time.Time
	var lastPropPoll time.Time

	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		snap := p.Snapshot()
		if snap.Err != nil {
			return 0, snap.Err
		}
		frames := p.FramesRendered()
		// Avoid hammering libmpv's core thread with synchronous property RPCs
		// every 8ms while waiting for network I/O; query immediately once a new
		// frame renders, or at a modest 48ms cadence for same-frame settlement.
		shouldPollProps := frames > beforeFrames || lastPropPoll.IsZero() || time.Since(lastPropPoll) >= 48*time.Millisecond
		if shouldPollProps {
			lastPropPoll = time.Now()
			seeking, _ := p.GetPropertyString("seeking")
			livePos := mpvLivePosition(p, snap.Position)
			posOK := previewPositionMatches(livePos, target, duration, snap.EOF, isLocal)

			if seeking != "yes" {
				if frames > beforeFrames {
					if posOK {
						return livePos, nil
					}
					if frameArrivedAt.IsZero() || frames != lastSeenFrames {
						frameArrivedAt = time.Now()
						lastSeenFrames = frames
					} else if time.Since(frameArrivedAt) >= 350*time.Millisecond {
						return livePos, nil
					}
				} else if posOK && frames > 0 {
					if settledSameFrameAt.IsZero() {
						settledSameFrameAt = time.Now()
					} else if time.Since(settledSameFrameAt) >= 160*time.Millisecond {
						return livePos, nil
					}
				}
			} else {
				frameArrivedAt = time.Time{}
				lastSeenFrames = 0
				settledSameFrameAt = time.Time{}
			}
		}

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-stateCh:
		case <-ticker.C:
		}
	}
}
