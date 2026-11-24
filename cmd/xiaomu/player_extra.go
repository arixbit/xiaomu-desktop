package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/subtitle"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

const (
	rightHoldThreshold = 300 * time.Millisecond
	maxSubtitleDelay   = 60 * time.Second
	miniWindowWidth    = 480
	miniWindowHeight   = 270
	defaultMinWinW     = 640
	defaultMinWinH     = 420
	miniMinWinW        = 320
	miniMinWinH        = 180
	chineseSLangList   = "zh,chi,zho,chs,zh-CN,zh-Hans,cht,zh-TW,zh-Hant,zh-HK"
)

type videoAspectMode int

const (
	aspectOriginal videoAspectMode = iota
	aspect16x9
	aspect4x3
	aspect21x9
	aspectFill
)

func (m videoAspectMode) label() string {
	switch m {
	case aspect16x9:
		return "16:9"
	case aspect4x3:
		return "4:3"
	case aspect21x9:
		return "21:9"
	case aspectFill:
		return "铺满裁切"
	default:
		return "原始比例"
	}
}

type sleepTimerMode int

const (
	sleepOff sleepTimerMode = iota
	sleepDuration
	sleepAfterEpisode
)

type rightHoldState struct {
	down      bool
	boosted   bool
	downAt    time.Time
	prevRate  float64
	boostRate float64
}

type subtitleOnlineState struct {
	open       bool
	tokenOpen  bool
	tokenInput string
	query      string
	message    string
	failed     bool
	results    []subtitle.Candidate
}

type playerStatsCache struct {
	sampledAt time.Time
	lines     []string
}

type playerExtraState struct {
	pinned          bool
	miniMode        bool
	miniSavedBounds mygo.Rectangle
	miniSavedPinned bool

	aspect   videoAspectMode
	rotation int // 0, 90, 180, 270
	loop     bool

	rightHold rightHoldState

	statsOpen  bool
	statsCache playerStatsCache

	sleepMode     sleepTimerMode
	sleepDeadline time.Time
	sleepPending  time.Duration // set before first frame with c.Now()

	subDelay           time.Duration
	autoChineseApplied bool
	subOnline          subtitleOnlineState

	// Testable hooks (wired to real system defaults when nil).
	captureScreenshot func() ([]byte, error)
	screenshotDir     func() (string, error)
	newSubtitleClient func(token string) *subtitle.Client
	subtitleCacheDir  string
}

func (a *app) resetPerVideoExtraState() {
	e := &a.playerExtra
	e.aspect = aspectOriginal
	e.rotation = 0
	e.loop = false
	e.rightHold = rightHoldState{}
	e.statsCache = playerStatsCache{}
	e.subDelay = 0
	e.autoChineseApplied = false
	e.subOnline.results = nil
	e.subOnline.message = ""
	e.subOnline.failed = false
	e.subOnline.query = ""
}

func (a *app) leavePlayerExtra() {
	a.cancelJob(jobSubtitle)
	a.releaseRightKeyHold(a.player)
	if a.playerExtra.miniMode {
		a.exitMiniMode()
	} else if a.playerExtra.pinned {
		a.playerExtra.pinned = false
		if a.window != nil {
			a.window.SetAlwaysOnTop(false)
		}
	}
	a.playerExtra.statsOpen = false
	a.playerExtra.subOnline.open = false
	a.clearSleepTimer()
	a.resetPerVideoExtraState()
}

// --- Always-on-top & Mini Window (摸鱼/画中画) ---

func (a *app) toggleAlwaysOnTop() {
	a.playerExtra.pinned = !a.playerExtra.pinned
	if a.window != nil {
		a.window.SetAlwaysOnTop(a.playerExtra.pinned)
	}
	if a.playerExtra.pinned {
		a.showPlayerOSD("窗口已置顶")
	} else {
		a.showPlayerOSD("已取消置顶")
	}
}

func (a *app) toggleMiniMode() {
	if a.playerExtra.miniMode {
		a.exitMiniMode()
		a.showPlayerOSD("已退出迷你小窗")
		return
	}
	a.enterMiniMode()
	a.showPlayerOSD("迷你置顶小窗")
}

func (a *app) enterMiniMode() {
	if a.playerExtra.miniMode {
		return
	}
	if a.fullScreen {
		a.setFullScreen(false)
		if a.window != nil {
			a.window.SetFullScreen(false)
		}
	}
	a.panelOpen, a.panelTouched = false, false
	a.playerOverlay.keepVisible = false
	a.playerExtra.miniSavedPinned = a.playerExtra.pinned
	a.playerExtra.miniMode = true
	a.playerExtra.pinned = true
	if a.window != nil {
		bounds := a.window.Bounds()
		a.playerExtra.miniSavedBounds = bounds
		a.window.SetMinimumSize(miniMinWinW, miniMinWinH)
		a.window.SetAlwaysOnTop(true)
		work := mygo.Screen.DisplayMatching(bounds).WorkArea
		target := mygo.Rectangle{
			X:      bounds.X,
			Y:      bounds.Y,
			Width:  miniWindowWidth,
			Height: miniWindowHeight,
		}
		if work.Width > 0 && work.Height > 0 {
			target.X = work.X + work.Width - miniWindowWidth - 24
			target.Y = work.Y + work.Height - miniWindowHeight - 24
			target = windowBoundsInWorkArea(target, work)
		}
		a.window.SetBounds(target)
	}
}

func (a *app) exitMiniMode() {
	if !a.playerExtra.miniMode {
		return
	}
	a.playerExtra.miniMode = false
	a.playerExtra.pinned = a.playerExtra.miniSavedPinned
	if a.window != nil {
		a.window.SetMinimumSize(defaultMinWinW, defaultMinWinH)
		a.window.SetAlwaysOnTop(a.playerExtra.pinned)
		if b := a.playerExtra.miniSavedBounds; b.Width > 0 && b.Height > 0 {
			a.window.SetBounds(b)
		}
	}
	a.playerExtra.miniSavedBounds = mygo.Rectangle{}
}

func (a *app) playerMiniDragStrip(c *ui.Context, height float32, showHandle bool) {
	if height <= 0 {
		return
	}
	bar := c.TitleBar()
	strip := ui.Row(c).Key("hud-mini-drag").Absolute().Top(0).Left(0).Right(0).Height(height).
		Padding(0, bar.Right, 0, bar.Left).Justify(ui.Center).AlignItems(ui.Center).
		DragWindow().Label("拖动窗口（迷你小窗）")
	if !showHandle {
		return
	}
	strip.Children(func() {
		ui.Box(c).Width(36).Height(4).Radius(2).Background(ui.RGBA(248, 249, 255, 0.45))
	})
}

func (a *app) playerMiniControls(c *ui.Context, p mediaPlayer) {
	ui.Row(c).Key("hud-mini").Absolute().Left(10).Right(10).Bottom(10).Justify(ui.Center).
		Transition(transHUDBottom).Children(func() {
		ui.Row(c).FillWidth().Padding(6, 10).Gap(8).Radius(18).AlignItems(ui.Center).
			Material(glass.Glass{Style: glass.Clear, Tint: ui.RGBA(9, 15, 31, 0.74)}).
			TextColor(playerText).Children(func() {
			playLabel := "暂停"
			if !p.Playing() {
				playLabel = "播放"
			}
			if playerGlassButton(c, playLabel).Padding(4, 9).Clicked() {
				togglePlayback(p)
				a.syncNowPlayingState()
			}
			if !a.isLiveStream() && p.Duration() > 0 {
				pos := p.Position().Seconds()
				sl := ui.Slider(c, &pos, 0, p.Duration().Seconds()).Label("小窗播放进度").Grow(1)
				if btn, pressed := sl.PressedButton(); pressed && btn == 0 {
					a.playerOverlay.keepVisible = true
				}
				if sl.Changed() {
					p.Seek(time.Duration(pos * float64(time.Second)))
					a.syncNowPlayingState()
				}
			} else {
				ui.Spacer(c)
			}
			if playerGlassButton(c, "还原").Padding(4, 9).Label("退出迷你小窗").Tooltip("退出迷你小窗 (T)").Clicked() {
				a.toggleMiniMode()
			}
			if playerGlassButton(c, "返回").Padding(4, 9).Label("关闭播放").Clicked() {
				a.leavePlayer()
			}
		})
	})
}

// --- Aspect / Crop, Rotation, Loop ---

func (a *app) playerVideoFit() ui.Fit {
	if a.playerExtra.aspect == aspectFill {
		return ui.Cover
	}
	return ui.Contain
}

func (a *app) setVideoAspect(p mediaPlayer, mode videoAspectMode) {
	a.playerExtra.aspect = mode
	if ap, ok := p.(advancedMediaPlayer); ok {
		switch mode {
		case aspect16x9:
			_ = ap.SetPropertyString("video-aspect-override", "16:9")
			_ = ap.SetPropertyString("panscan", "0")
		case aspect4x3:
			_ = ap.SetPropertyString("video-aspect-override", "4:3")
			_ = ap.SetPropertyString("panscan", "0")
		case aspect21x9:
			_ = ap.SetPropertyString("video-aspect-override", "21:9")
			_ = ap.SetPropertyString("panscan", "0")
		case aspectFill:
			_ = ap.SetPropertyString("video-aspect-override", "-1")
			_ = ap.SetPropertyString("panscan", "1")
		default:
			_ = ap.SetPropertyString("video-aspect-override", "-1")
			_ = ap.SetPropertyString("panscan", "0")
		}
	}
	a.showPlayerOSD("画面：" + mode.label())
}

func (a *app) rotateVideo(p mediaPlayer) {
	a.playerExtra.rotation = (a.playerExtra.rotation + 90) % 360
	if ap, ok := p.(advancedMediaPlayer); ok {
		_ = ap.SetPropertyString("video-rotate", strconv.Itoa(a.playerExtra.rotation))
	}
	if a.playerExtra.rotation == 0 {
		a.showPlayerOSD("画面已恢复正常方向")
	} else {
		a.showPlayerOSD(fmt.Sprintf("画面旋转 %d°", a.playerExtra.rotation))
	}
}

func (a *app) toggleLoop(p mediaPlayer) {
	if a.playingLive {
		return
	}
	a.playerExtra.loop = !a.playerExtra.loop
	if ap, ok := p.(advancedMediaPlayer); ok {
		_ = ap.SetLoop(a.playerExtra.loop)
	}
	if a.playerExtra.loop {
		a.showPlayerOSD("单集循环已开")
	} else {
		a.showPlayerOSD("单集循环已关")
	}
}

// --- Long-press Right Arrow 2x Speed ---

func (a *app) releaseRightKeyHold(p mediaPlayer) {
	rh := &a.playerExtra.rightHold
	if !rh.down {
		return
	}
	if rh.boosted && p != nil {
		restore := rh.prevRate
		if restore <= 0 {
			restore = 1
		}
		p.SetRate(restore)
	}
	*rh = rightHoldState{}
}

func (a *app) handleRightKeyHold(c *ui.Context, p mediaPlayer, pressed, repeated bool) {
	rh := &a.playerExtra.rightHold
	if a.isLiveStream() || p == nil || p.Duration() <= 0 {
		a.releaseRightKeyHold(p)
		return
	}
	held := c.KeyDown(ui.KeyRight)

	if pressed && !repeated {
		rh.down = true
		rh.boosted = false
		rh.downAt = c.Now()
		c.After(rightHoldThreshold)
	}

	if !rh.down {
		return
	}

	if held {
		elapsed := c.Now().Sub(rh.downAt)
		if !rh.boosted && (elapsed >= rightHoldThreshold || repeated) {
			prev := p.Rate()
			if prev <= 0 {
				prev = 1
			}
			target := 2.0
			if prev >= 2.0 {
				target = 3.0
			}
			rh.boosted = true
			rh.prevRate = prev
			rh.boostRate = target
			p.SetRate(target)
			a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
		} else if !rh.boosted {
			if rem := rightHoldThreshold - elapsed; rem > 0 {
				c.After(rem)
			}
		} else {
			a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
		}
		return
	}

	// Key released (or window lost focus).
	if rh.boosted {
		a.releaseRightKeyHold(p)
		return
	}
	*rh = rightHoldState{}
	p.Seek(min(max(p.Position()+10*time.Second, 0), p.Duration()))
	a.syncNowPlayingState()
	a.showPlayerOSD("快进 10 秒")
	a.playerOverlay.hideAt = c.Now().Add(playerOverlayIdle)
}

func (a *app) rightHoldBoostPill(c *ui.Context) {
	rh := &a.playerExtra.rightHold
	if !rh.boosted {
		return
	}
	label := fmt.Sprintf("▶▶ %.0f× 倍速中", rh.boostRate)
	ui.Row(c.Key("right-hold-pill")).Absolute().Top(76).Left(0).Right(0).Justify(ui.Center).PassThrough().
		Transition(transOSD).Children(func() {
		ui.Row(c).Padding(7, 15).Radius(16).
			Material(glass.Glass{Style: glass.Regular, Tint: ui.RGBA(12, 16, 28, 0.82)}).
			TextColor(playerText).PassThrough().Children(func() {
			ui.Text(c, label).FontSize(13).Bold().SingleLine()
		})
	})
}

// --- One-Click Screenshot ---

func (a *app) capturePlayerScreenshot() {
	if a.closed || a.player == nil {
		return
	}
	capture := a.playerExtra.captureScreenshot
	if capture == nil {
		if a.window == nil {
			a.status = "当前窗口不支持截图"
			return
		}
		win := a.window
		capture = win.CapturePage
	}
	dirFn := a.playerExtra.screenshotDir
	if dirFn == nil {
		dirFn = func() (string, error) {
			if d, err := mygo.App.Path(mygo.PathPictures); err == nil && d != "" {
				return filepath.Join(d, "Xiaomu"), nil
			}
			if d, err := mygo.App.Path(mygo.PathDesktop); err == nil && d != "" {
				return filepath.Join(d, "Xiaomu"), nil
			}
			return filepath.Join(os.TempDir(), "xiaomu-screenshots"), nil
		}
	}
	title := sanitizeScreenshotName(a.currentPlaybackTitle())
	now := time.Now()
	stamp := fmt.Sprintf("%s-%03d", now.Format("20060102-150405"), now.Nanosecond()/int(time.Millisecond))
	stem := fmt.Sprintf("xiaomu-%s-%s", title, stamp)

	dispatch := a.dispatch
	run := func() {
		pngBytes, err := capture()
		var outPath string
		if err == nil {
			if len(pngBytes) == 0 {
				err = errors.New("未捕获到画面数据")
			} else {
				var dir string
				dir, err = dirFn()
				if err == nil {
					if err = os.MkdirAll(dir, 0o755); err == nil {
						outPath, err = writeUniqueScreenshot(dir, stem, pngBytes)
					}
				}
			}
		}
		apply := func() {
			if a.closed {
				return
			}
			if err != nil {
				a.status = "截图失败：" + err.Error()
				return
			}
			a.setNotice("截图已保存：" + filepath.Base(outPath))
			a.showPlayerOSD("已保存截图")
		}
		if dispatch == nil {
			apply()
		} else {
			dispatch(apply)
		}
	}
	if dispatch == nil {
		run()
	} else {
		go run()
	}
}

func writeUniqueScreenshot(dir, stem string, data []byte) (string, error) {
	for i := 0; i < 1000; i++ {
		name := stem + ".png"
		if i > 0 {
			name = fmt.Sprintf("%s-%d.png", stem, i)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil {
			_ = os.Remove(path)
			return "", werr
		}
		if cerr != nil {
			_ = os.Remove(path)
			return "", cerr
		}
		return path, nil
	}
	return "", errors.New("截图文件重名过多")
}

func (a *app) currentPlaybackTitle() string {
	switch {
	case a.playingFile && a.playingName != "":
		return strings.TrimSuffix(a.playingName, filepath.Ext(a.playingName))
	case a.playingLive && a.live.playingChannel >= 0 && a.live.playingChannel < len(a.live.channels):
		return a.live.channels[a.live.playingChannel].Name
	case !a.playingDirect && a.title != nil && a.title.name != "":
		if a.playingEpisode != "" {
			return a.title.name + "-" + a.playingEpisode
		}
		return a.title.name
	default:
		return "capture"
	}
}

func sanitizeScreenshotName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "capture"
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\n', '\r', '\t':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " ._")
	if out == "" {
		return "capture"
	}
	runes := []rune(out)
	if len(runes) > 40 {
		out = string(runes[:40])
	}
	return out
}

// --- Playback Info Overlay (Stats for Nerds) ---

func (a *app) toggleStatsOverlay() {
	a.playerExtra.statsOpen = !a.playerExtra.statsOpen
	a.playerExtra.statsCache = playerStatsCache{}
	if a.playerExtra.statsOpen {
		a.showPlayerOSD("播放信息已开")
	} else {
		a.showPlayerOSD("播放信息已关")
	}
}

func (a *app) buildPlaybackStatsLines(p mediaPlayer) []string {
	engine := "系统播放器"
	if _, ok := p.(advancedMediaPlayer); ok {
		engine = "libmpv"
	}
	lines := []string{"引擎：" + engine}

	w, h := p.Size()
	var vCodec string
	var fps float64
	if ap, ok := p.(advancedMediaPlayer); ok {
		for _, tr := range ap.VideoTracks() {
			if tr.Selected || vCodec == "" {
				vCodec = formatTrackCodec(tr.Codec)
				if tr.FPS > 0 {
					fps = tr.FPS
				}
				if w == 0 && tr.Width > 0 {
					w, h = tr.Width, tr.Height
				}
			}
		}
		if fps <= 0 {
			if raw, err := ap.GetPropertyString("estimated-vf-fps"); err == nil {
				if v, perr := strconv.ParseFloat(strings.TrimSpace(raw), 64); perr == nil && v > 0 {
					fps = v
				}
			}
		}
	}
	var videoParts []string
	if w > 0 && h > 0 {
		videoParts = append(videoParts, fmt.Sprintf("%d×%d", w, h))
	}
	if vCodec != "" {
		videoParts = append(videoParts, vCodec)
	}
	if fps > 0 {
		videoParts = append(videoParts, fmt.Sprintf("%.2f fps", fps))
	}
	if len(videoParts) > 0 {
		lines = append(lines, "视频："+strings.Join(videoParts, " · "))
	}

	if tp, ok := p.(trackCapablePlayer); ok {
		hw := strings.TrimSpace(tp.CurrentHWDec())
		if hw == "" || strings.EqualFold(hw, "no") {
			lines = append(lines, "解码：软件解码")
		} else {
			lines = append(lines, "硬解："+hw)
		}
		for _, tr := range tp.AudioTracks() {
			if tr.Selected {
				var audioParts []string
				if codec := formatTrackCodec(tr.Codec); codec != "" {
					audioParts = append(audioParts, codec)
				}
				if ch := formatTrackChannels(tr.Channels); ch != "" {
					audioParts = append(audioParts, ch)
				}
				if tr.SampleRate > 0 {
					audioParts = append(audioParts, fmt.Sprintf("%.1f kHz", float64(tr.SampleRate)/1000))
				}
				if len(audioParts) > 0 {
					lines = append(lines, "音频："+strings.Join(audioParts, " · "))
				}
				break
			}
		}
	}

	var extra []string
	if rate := p.Rate(); rate > 0 && math.Abs(rate-1) > 0.01 {
		extra = append(extra, strconv.FormatFloat(rate, 'f', -1, 64)+"×")
	}
	if a.playerExtra.aspect != aspectOriginal {
		extra = append(extra, a.playerExtra.aspect.label())
	}
	if a.playerExtra.rotation != 0 {
		extra = append(extra, fmt.Sprintf("旋转 %d°", a.playerExtra.rotation))
	}
	if a.playerExtra.loop {
		extra = append(extra, "单集循环")
	}
	if a.playerExtra.subDelay != 0 {
		extra = append(extra, "字幕 "+formatSubtitleDelay(a.playerExtra.subDelay))
	}
	if len(extra) > 0 {
		lines = append(lines, "状态："+strings.Join(extra, " · "))
	}
	if !a.playingFile && a.speedText != "" && a.speedText != "—" {
		lines = append(lines, "网速："+a.speedText)
	}
	return lines
}

func (a *app) playbackStatsOverlay(c *ui.Context, p mediaPlayer) {
	if !a.playerExtra.statsOpen || a.playerExtra.miniMode {
		return
	}
	now := c.Now()
	if a.playerExtra.statsCache.sampledAt.IsZero() || now.Sub(a.playerExtra.statsCache.sampledAt) >= time.Second {
		a.playerExtra.statsCache = playerStatsCache{
			sampledAt: now,
			lines:     a.buildPlaybackStatsLines(p),
		}
	}
	c.After(time.Second)
	top := float32(72)
	if bar := c.TitleBar(); bar.Bottom+56 > top {
		top = bar.Bottom + 56
	}
	ui.Column(c.Key("playback-stats")).Absolute().Top(top).Left(18).Padding(10, 14).Gap(4).Radius(14).
		Material(glass.Glass{Style: glass.Regular, Tint: ui.RGBA(10, 14, 26, 0.80)}).
		TextColor(playerText).PassThrough().Label("播放信息浮层").Children(func() {
		ui.Text(c, "播放信息").FontSize(12).Bold().TextColor(ui.RGBA(248, 249, 255, 0.92))
		for _, line := range a.playerExtra.statsCache.lines {
			ui.Text(c, line).FontSize(11).TextColor(ui.RGBA(228, 234, 250, 0.86)).SingleLine()
		}
	})
}

// --- Sleep Timer (定时关闭) ---

func (a *app) setSleepTimerMinutes(minutes int) {
	if minutes <= 0 {
		a.clearSleepTimer()
		a.showPlayerOSD("已取消定时关闭")
		return
	}
	a.playerExtra.sleepMode = sleepDuration
	a.playerExtra.sleepDeadline = time.Time{}
	a.playerExtra.sleepPending = time.Duration(minutes) * time.Minute
	a.showPlayerOSD(fmt.Sprintf("%d 分钟后暂停播放", minutes))
}

func (a *app) setSleepAfterEpisode() {
	if a.playingLive {
		return
	}
	a.playerExtra.sleepMode = sleepAfterEpisode
	a.playerExtra.sleepDeadline = time.Time{}
	a.playerExtra.sleepPending = 0
	a.showPlayerOSD("播完本集后暂停")
}

func (a *app) clearSleepTimer() {
	a.playerExtra.sleepMode = sleepOff
	a.playerExtra.sleepDeadline = time.Time{}
	a.playerExtra.sleepPending = 0
}

func (a *app) sleepTimerBadge(now time.Time) string {
	switch a.playerExtra.sleepMode {
	case sleepAfterEpisode:
		return "定时 · 播完本集"
	case sleepDuration:
		rem := a.playerExtra.sleepPending
		if !a.playerExtra.sleepDeadline.IsZero() {
			rem = a.playerExtra.sleepDeadline.Sub(now)
		}
		if rem < 0 {
			rem = 0
		}
		secs := int(math.Ceil(rem.Seconds()))
		return fmt.Sprintf("定时 %02d:%02d", secs/60, secs%60)
	default:
		return ""
	}
}

func (a *app) checkSleepTimer(c *ui.Context, p mediaPlayer) {
	if a.playerExtra.sleepMode != sleepDuration {
		return
	}
	now := c.Now()
	if a.playerExtra.sleepDeadline.IsZero() && a.playerExtra.sleepPending > 0 {
		a.playerExtra.sleepDeadline = now.Add(a.playerExtra.sleepPending)
		a.playerExtra.sleepPending = 0
	}
	if a.playerExtra.sleepDeadline.IsZero() {
		return
	}
	rem := a.playerExtra.sleepDeadline.Sub(now)
	if rem <= 0 {
		a.clearSleepTimer()
		if p != nil && p.Playing() {
			p.Pause()
			a.syncNowPlayingState()
		}
		if a.fullScreen {
			a.setFullScreen(false)
			if a.window != nil {
				a.window.SetFullScreen(false)
			}
		}
		a.setNotice("定时关闭已触发，已暂停播放")
		a.showPlayerOSD("定时已到 · 已暂停")
		return
	}
	if !a.fullScreen || rem <= 5*time.Second {
		c.After(min(time.Second, rem))
	} else {
		c.After(rem)
	}
}

// --- Subtitle Preferences, Timing Offset, Chinese Auto-Select, Online Matching ---

func formatSubtitleDelay(d time.Duration) string {
	sec := d.Seconds()
	if math.Abs(sec) < 0.05 {
		return "0.0s"
	}
	if sec > 0 {
		return fmt.Sprintf("+%.1fs", sec)
	}
	return fmt.Sprintf("%.1fs", sec)
}

func (a *app) adjustSubtitleDelay(p mediaPlayer, delta time.Duration) {
	if a.playingLive || p == nil {
		return
	}
	next := a.playerExtra.subDelay + delta
	next = min(max(next, -maxSubtitleDelay), maxSubtitleDelay)
	// Round to nearest 100ms to prevent floating point drift.
	next = time.Duration(math.Round(next.Seconds()*10)) * 100 * time.Millisecond
	a.playerExtra.subDelay = next
	if ap, ok := p.(advancedMediaPlayer); ok {
		_ = ap.SetPropertyString("sub-delay", fmt.Sprintf("%.1f", next.Seconds()))
	}
	a.showPlayerOSD("字幕偏移 " + formatSubtitleDelay(next))
}

func (a *app) resetSubtitleDelay(p mediaPlayer) {
	a.playerExtra.subDelay = 0
	if ap, ok := p.(advancedMediaPlayer); ok {
		_ = ap.SetPropertyString("sub-delay", "0")
	}
	a.showPlayerOSD("字幕偏移已重置")
}

func (a *app) applySubtitlePreferences(p mediaPlayer) {
	ap, ok := p.(advancedMediaPlayer)
	if !ok {
		return
	}
	a.ensurePreferences()
	sp := a.preferences.Subtitle.normalized()
	_ = ap.SetPropertyString("sub-scale", fmt.Sprintf("%.2f", sp.Scale))
	_ = ap.SetPropertyString("sub-pos", strconv.Itoa(sp.Pos))
	if sp.PreferChinese {
		_ = ap.SetPropertyString("slang", chineseSLangList)
	} else {
		_ = ap.SetPropertyString("slang", "")
	}
	if a.playerExtra.subDelay != 0 {
		_ = ap.SetPropertyString("sub-delay", fmt.Sprintf("%.1f", a.playerExtra.subDelay.Seconds()))
	}
}

func chineseTrackRank(tr mediaTrack) int {
	lang := strings.ToLower(strings.TrimSpace(tr.Lang))
	title := strings.ToLower(strings.TrimSpace(tr.Title))
	switch lang {
	case "zh-cn", "zh-hans", "chs", "sc":
		return 4
	case "zh", "chi", "zho":
		if strings.Contains(title, "繁") || strings.Contains(title, "cht") || strings.Contains(title, "tc") {
			return 2
		}
		return 3
	case "zh-tw", "zh-hk", "zh-hant", "cht", "tc":
		return 2
	}
	for _, kw := range []string{"简", "中英", "简英", "双语", "chs", "zh-hans", "中文", "中字"} {
		if strings.Contains(title, kw) {
			return 3
		}
	}
	for _, kw := range []string{"繁", "cht", "zh-hant"} {
		if strings.Contains(title, kw) {
			return 2
		}
	}
	return 0
}

func (a *app) maybeAutoSelectChineseSubtitle(p mediaPlayer) {
	if a.playerExtra.autoChineseApplied || a.playingLive {
		return
	}
	tp, ok := p.(trackCapablePlayer)
	if !ok {
		a.playerExtra.autoChineseApplied = true
		return
	}
	tracks := tp.SubtitleTracks()
	if len(tracks) == 0 {
		return
	}
	a.playerExtra.autoChineseApplied = true
	a.ensurePreferences()
	if !a.preferences.Subtitle.PreferChinese {
		return
	}
	bestRank := 0
	var bestID int64
	for _, tr := range tracks {
		rank := chineseTrackRank(tr)
		if tr.Selected && tp.SubtitleVisible() && rank > 0 {
			return // already on a Chinese subtitle track
		}
		if rank > bestRank {
			bestRank = rank
			bestID = tr.ID
		}
	}
	if bestRank > 0 {
		_ = tp.SelectSubtitleTrack(bestID)
	}
}

func (a *app) subtitleClient() *subtitle.Client {
	a.ensurePreferences()
	token := a.preferences.Subtitle.ASSRTToken
	if a.playerExtra.newSubtitleClient != nil {
		return a.playerExtra.newSubtitleClient(token)
	}
	return subtitle.NewClient(token)
}

func (a *app) subtitleDownloadDir() string {
	if a.playerExtra.subtitleCacheDir != "" {
		return a.playerExtra.subtitleCacheDir
	}
	return filepath.Join(os.TempDir(), "xiaomu-subtitles")
}

func (a *app) openSubtitleDialog() {
	a.ensurePreferences()
	so := &a.playerExtra.subOnline
	so.open = true
	so.tokenInput = a.preferences.Subtitle.ASSRTToken
	if strings.TrimSpace(so.query) == "" {
		title, _, _ := a.danmakuTargetMeta()
		if title == "" {
			title = a.currentPlaybackTitle()
		}
		so.query = title
	}
}

func (a *app) searchOnlineSubtitlesByHash() {
	if !a.playingFile || a.playingFilePath == "" {
		a.playerExtra.subOnline.failed = true
		a.playerExtra.subOnline.message = "仅本地视频支持射手哈希匹配，网盘或在线视频请使用关键词搜索"
		return
	}
	filePath := a.playingFilePath
	fileName := a.playingName
	client := a.subtitleClient()
	so := &a.playerExtra.subOnline
	so.failed = false
	so.results = nil
	so.message = "正在计算文件哈希并匹配射手字幕…"
	a.run(jobSubtitle, "", func(ctx context.Context) (jobResult, error) {
		hash, err := subtitle.ComputeShooterHash(filePath)
		if err != nil {
			return jobResult{}, err
		}
		cands, err := client.SearchShooter(ctx, hash, fileName)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			so.results = cands
			so.failed = false
			if len(cands) == 0 {
				so.message = "射手哈希未匹配到中文字幕，可尝试下方关键词搜索"
			} else {
				so.message = fmt.Sprintf("哈希匹配到 %d 条字幕，点击即可加载", len(cands))
			}
		}}, nil
	}, func(err error) {
		so.failed = true
		so.message = "射手匹配失败：" + err.Error()
	})
}

func (a *app) searchOnlineSubtitlesByKeyword() {
	so := &a.playerExtra.subOnline
	q := strings.TrimSpace(so.query)
	if q == "" {
		so.failed = true
		so.message = "请输入要搜索字幕的片名"
		return
	}
	client := a.subtitleClient()
	so.failed = false
	so.results = nil
	so.message = "正在搜索在线字幕…"
	a.run(jobSubtitle, "", func(ctx context.Context) (jobResult, error) {
		cands, err := client.SearchASSRT(ctx, q)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			so.results = cands
			so.failed = false
			if len(cands) == 0 {
				so.message = "未找到匹配的在线字幕，试试精简关键词"
			} else {
				so.message = fmt.Sprintf("找到 %d 条在线字幕，点击即可下载加载", len(cands))
			}
		}}, nil
	}, func(err error) {
		so.failed = true
		if errors.Is(err, subtitle.ErrTokenRequired) {
			so.tokenOpen = true
			so.message = "请展开下方「伪射手 Token 设置」填入个人 Token 后再按片名搜索（本地视频可直接点「射手哈希匹配」）"
			return
		}
		so.message = "在线搜索失败：" + err.Error()
	})
}

func (a *app) loadOnlineSubtitle(cand subtitle.Candidate) {
	if a.player == nil {
		return
	}
	if _, ok := a.player.(trackCapablePlayer); !ok {
		return
	}
	client := a.subtitleClient()
	destDir := a.subtitleDownloadDir()
	so := &a.playerExtra.subOnline
	so.failed = false
	so.message = "正在下载并加载字幕…"
	a.run(jobSubtitle, "", func(ctx context.Context) (jobResult, error) {
		path, err := client.Download(ctx, cand, destDir)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			tp, ok := a.player.(trackCapablePlayer)
			if !ok {
				return
			}
			if err := tp.AddSubtitleFile(path); err != nil {
				so.failed = true
				so.message = "加载字幕失败：" + err.Error()
				return
			}
			if cand.DelayMS != 0 {
				a.playerExtra.subDelay = time.Duration(cand.DelayMS) * time.Millisecond
				if ap, ok := a.player.(advancedMediaPlayer); ok {
					_ = ap.SetPropertyString("sub-delay", fmt.Sprintf("%.1f", a.playerExtra.subDelay.Seconds()))
				}
			}
			so.failed = false
			so.message = "已加载在线字幕：" + cand.Title
			a.setNotice("已加载在线字幕：" + cand.Title)
			a.showPlayerOSD("已加载在线字幕")
		}}, nil
	}, func(err error) {
		so.failed = true
		so.message = "下载字幕失败：" + err.Error()
	})
}

func (a *app) subtitleDialog(c *ui.Context, p mediaPlayer) {
	so := &a.playerExtra.subOnline
	if !so.open {
		return
	}
	tp, ok := p.(trackCapablePlayer)
	if !ok {
		so.open = false
		return
	}
	a.ensurePreferences()
	sp := &a.preferences.Subtitle
	ui.Themed(c, ui.DarkTheme()).Absolute().Left(0).Right(0).Top(0).Bottom(0).
		Background(ui.Transparent).PassThrough().Children(func() {
		pal := paletteFor(c)
		ui.DialogBase(c, &so.open, func(backdrop, panel ui.Element) {
			backdrop.Background(ui.RGBA(2, 8, 22, 0.24)).Transition(transDialogScrim)
			_, height := c.Size()
			panel.Width(548).MaxWidth(548).Height(min(float32(680), height-32)).
				Padding(18, 8, 16, 20).Gap(10).Radius(24).
				Material(glass.Glass{Style: glass.Regular, Tint: ui.RGBA(18, 22, 38, 0.86)}).
				TextColor(playerText).Transition(transDialogPanel)
			ui.Row(c).FillWidth().Padding(0, 16, 0, 0).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "字幕设置与在线匹配").FontSize(20).Bold().Grow(1)
				ui.Text(c, "[ / ] 微调偏移").FontSize(12).TextColor(pal.textMuted)
			})
			ui.Scroll(c).Label("字幕设置内容").FillWidth().Grow(1).MinHeight(0).
				Padding(4, 16, 4, 0).Gap(10).
				ScrollbarStyle(danmakuScrollbar).ScrollbarInsets(4, 0, 4, 0).
				Children(func() {
					// 1. Offset, Style & Language Preference
					danmakuSection(c, func() {
						ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "时间偏移").Width(64)
							for _, step := range []struct {
								label string
								delta time.Duration
							}{
								{"-2s", -2 * time.Second},
								{"-0.5s", -500 * time.Millisecond},
								{"+0.5s", 500 * time.Millisecond},
								{"+2s", 2 * time.Second},
							} {
								if glassButton(c, step.label).Label("字幕偏移 " + step.label).Padding(5, 9).Clicked() {
									a.adjustSubtitleDelay(p, step.delta)
								}
							}
							ui.Text(c, formatSubtitleDelay(a.playerExtra.subDelay)).FontSize(13).
								TextColor(pal.textMuted).TextAlign(ui.End).Grow(1).ContentTransition(ctNumeric)
							if glassButton(c, "重置").Label("重置字幕偏移").Padding(5, 9).
								Disabled(a.playerExtra.subDelay == 0).Clicked() {
								a.resetSubtitleDelay(p)
							}
						})
						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "字号缩放").Width(64)
							if ui.Slider(c, &sp.Scale, 0.5, 2.0).Label("字幕字号缩放").Grow(1).Changed() {
								a.applySubtitlePreferences(p)
								a.savePreferences()
							}
							ui.Text(c, fmt.Sprintf("%.0f%%", sp.Scale*100)).Width(46).TextAlign(ui.End).
								TextColor(pal.textMuted).ContentTransition(ctNumeric)
						})
						posFloat := float64(sp.Pos)
						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "垂直位置").Width(64)
							if ui.Slider(c, &posFloat, 50, 100).Label("字幕垂直位置").Grow(1).Changed() {
								sp.Pos = int(math.Round(posFloat))
								a.applySubtitlePreferences(p)
								a.savePreferences()
							}
							ui.Text(c, fmt.Sprintf("%d%%", sp.Pos)).Width(46).TextAlign(ui.End).
								TextColor(pal.textMuted).ContentTransition(ctNumeric)
						})
						ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "优先自动选择中文字幕").Grow(1)
							if ui.Switch(c, &sp.PreferChinese).Label("优先中文字幕").Changed() {
								a.applySubtitlePreferences(p)
								a.savePreferences()
							}
						})
					})

					// 2. Local & Track quick actions
					danmakuSection(c, func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "当前字幕轨").Bold().Grow(1)
							if glassButton(c, "加载本地字幕…").Padding(5, 10).Clicked() {
								a.openSubtitleFileDialog()
							}
						})
						tracks := tp.SubtitleTracks()
						visible := tp.SubtitleVisible()
						if len(tracks) == 0 {
							ui.Text(c, "当前视频无内嵌或已加载字幕").FontSize(12).TextColor(pal.textMuted)
						} else {
							ui.Column(c).FillWidth().Gap(6).Children(func() {
								for i, tr := range tracks {
									selected := tr.Selected && visible
									label := formatSubtitleTrackLabel(tr, i)
									if danmakuEpisodeButton(c.Key(fmt.Sprintf("subdlg-%d", tr.ID)), label, selected).Clicked() {
										if err := tp.SelectSubtitleTrack(tr.ID); err != nil {
											so.failed = true
											so.message = "切换字幕失败：" + err.Error()
										}
									}
								}
							})
						}
					})

					// 3. Online Subtitle Matching (Shooter Hash + User-Token ASSRT)
					danmakuSection(c, func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "在线匹配字幕").Bold().Grow(1)
							if a.playingFile {
								if glassButton(c, "射手哈希匹配").Label("射手哈希自动匹配").
									Padding(5, 10).Disabled(a.running(jobSubtitle)).Clicked() {
									a.searchOnlineSubtitlesByHash()
								}
							}
						})
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							qInput := ui.TextInput(c, &so.query).Label("在线字幕片名").Placeholder("输入片名关键词（如 沙丘 2024）").Grow(1)
							doSearch := glassButton(c, "搜索字幕").Disabled(a.running(jobSubtitle)).Clicked()
							if !a.running(jobSubtitle) && qInput.Submitted() {
								doSearch = true
							}
							if doSearch {
								a.searchOnlineSubtitlesByKeyword()
							}
						})
						if so.message != "" {
							msgColor := pal.textMuted
							if so.failed {
								msgColor = ui.RGB(255, 166, 130)
							}
							ui.Text(c, so.message).FontSize(12).TextColor(msgColor)
						}
						if len(so.results) > 0 {
							ui.Column(c).FillWidth().Gap(6).Children(func() {
								for i, cand := range so.results {
									srcLabel := "射手"
									if cand.Source == "assrt" {
										srcLabel = "ASSRT"
									}
									parts := []string{cand.Title}
									if cand.Lang != "" {
										parts = append(parts, cand.Lang)
									}
									if cand.Format != "" {
										parts = append(parts, strings.ToUpper(cand.Format))
									}
									parts = append(parts, srcLabel)
									if danmakuEpisodeButton(c.Key(fmt.Sprintf("subcand-%d", i)), strings.Join(parts, " · "), false).
										Disabled(a.running(jobSubtitle)).Clicked() {
										a.loadOnlineSubtitle(cand)
									}
								}
							})
						}
						tokPrefix := "▸ "
						if so.tokenOpen {
							tokPrefix = "▾ "
						}
						if glassButton(c, tokPrefix+"伪射手 (ASSRT) Token 设置（可选）").
							Label("伪射手 Token 设置").FillWidth().Clicked() {
							so.tokenOpen = !so.tokenOpen
						}
						if so.tokenOpen {
							ui.Text(c, "本地文件可直接免登录使用射手哈希匹配；按片名在线搜索需填写个人 ASSRT Token（仅保存在本机）。").
								FontSize(12).TextColor(pal.textMuted)
							ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
								ui.TextInput(c, &so.tokenInput).Label("ASSRT Token").Placeholder("粘贴个人 ASSRT API Token").Grow(1)
								if primaryButton(c, "保存 Token").Clicked() {
									sp.ASSRTToken = strings.TrimSpace(so.tokenInput)
									a.savePreferences()
									so.failed = false
									if sp.ASSRTToken == "" {
										so.message = "已清空 ASSRT Token"
									} else {
										so.message = "已保存 ASSRT Token，现在可以按片名搜索字幕"
									}
								}
							})
						}
					})
				})
			ui.Row(c).FillWidth().Padding(0, 16, 0, 0).Justify(ui.End).Children(func() {
				if glassButton(c, "完成").Clicked() {
					so.open = false
				}
			})
		})
	})
}

// --- More Controls Menu (画面比例/旋转/循环/定时关闭/截图/播放信息) ---

func (a *app) morePlayerControls(c *ui.Context, p mediaPlayer) {
	ui.MenuButton(c, "更多", func(m *ui.Menu) {
		for _, mode := range []videoAspectMode{aspectOriginal, aspect16x9, aspect4x3, aspect21x9, aspectFill} {
			if m.Item("画面 · " + mode.label()).ID(fmt.Sprintf("aspect-%d", mode)).Radio(a.playerExtra.aspect == mode).Chosen() {
				a.setVideoAspect(p, mode)
			}
		}
		if _, ok := p.(advancedMediaPlayer); ok {
			rotLabel := "旋转 90° (R)"
			if a.playerExtra.rotation != 0 {
				rotLabel = fmt.Sprintf("旋转 90° (当前 %d°)", a.playerExtra.rotation)
			}
			if m.Item(rotLabel).ID("video-rotate").Chosen() {
				a.rotateVideo(p)
			}
		}
		m.Separator()
		if !a.playingLive {
			if m.Item("单集循环 (L)").ID("loop-toggle").Checked(a.playerExtra.loop).Chosen() {
				a.toggleLoop(p)
			}
			m.Separator()
		}
		if m.Item("定时关闭 · 关闭").ID("sleep-0").Radio(a.playerExtra.sleepMode == sleepOff).Chosen() {
			a.setSleepTimerMinutes(0)
		}
		for _, mins := range []int{15, 30, 45, 60} {
			if m.Item(fmt.Sprintf("定时关闭 · %d 分钟", mins)).ID(fmt.Sprintf("sleep-%d", mins)).Chosen() {
				a.setSleepTimerMinutes(mins)
			}
		}
		if !a.playingLive {
			if m.Item("定时关闭 · 播完本集").ID("sleep-ep").Radio(a.playerExtra.sleepMode == sleepAfterEpisode).Chosen() {
				a.setSleepAfterEpisode()
			}
		}
		m.Separator()
		if m.Item("一键截图 (S)").ID("capture-screenshot").Chosen() {
			a.capturePlayerScreenshot()
		}
		if m.Item("播放信息 (I)").ID("toggle-stats").Checked(a.playerExtra.statsOpen).Chosen() {
			a.toggleStatsOverlay()
		}
	}).Label("更多播放控制")
}
