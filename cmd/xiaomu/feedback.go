package main

import (
	"fmt"
	"math"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

const (
	noticeDuration     = 3500 * time.Millisecond
	undoNoticeDuration = 5000 * time.Millisecond
	armedConfirmTTL    = 4000 * time.Millisecond
	playerOSDDuration  = 1100 * time.Millisecond
	skipUndoDuration   = 4500 * time.Millisecond
	upNextWindow       = 8 * time.Second
)

type toastFeedback struct {
	seq       uint64
	msg       string
	ttl       time.Duration
	expiresAt time.Time
	undoLabel string
	undoFn    func()
}

type armedConfirmState struct {
	key   string
	until time.Time
}

type playerOSDState struct {
	text  string
	ttl   time.Duration
	until time.Time
}

type skipUndoState struct {
	active bool
	until  time.Time
}

// setNotice shows a brief non-error status toast that auto-dismisses.
func (a *app) setNotice(msg string) {
	a.status = msg
	a.toast = toastFeedback{
		seq: a.toast.seq + 1,
		msg: msg,
		ttl: noticeDuration,
	}
}

// setUndoNotice shows an action-completed toast with a one-click Undo button.
func (a *app) setUndoNotice(msg, undoLabel string, undoFn func()) {
	a.status = msg
	a.toast = toastFeedback{
		seq:       a.toast.seq + 1,
		msg:       msg,
		ttl:       undoNoticeDuration,
		undoLabel: undoLabel,
		undoFn:    undoFn,
	}
}

// clearStatus clears the status line and any pending toast/undo action.
func (a *app) clearStatus() {
	a.status = ""
	a.toast = toastFeedback{seq: a.toast.seq + 1}
}

// syncToastState keeps a.toast aligned with direct a.status writes so errors
// never inherit a previous notice's TTL or Undo button, and expires active
// notices cleanly on c.Now().
func (a *app) syncToastState(c *ui.Context, busy bool) {
	if a.toast.msg != a.status {
		a.toast.msg = ""
		a.toast.ttl = 0
		a.toast.expiresAt = time.Time{}
		a.toast.undoLabel = ""
		a.toast.undoFn = nil
		return
	}
	if !busy && a.status != "" && a.toast.ttl > 0 {
		now := c.Now()
		if a.toast.expiresAt.IsZero() {
			a.toast.expiresAt = now.Add(a.toast.ttl)
		}
		if !now.Before(a.toast.expiresAt) {
			a.clearStatus()
		} else {
			c.After(a.toast.expiresAt.Sub(now))
		}
	}
}

// clearArmed resets any pending two-step destructive confirmation.
func (a *app) clearArmed() {
	a.armed = armedConfirmState{}
}

// armedDangerButton renders a two-step confirmation button for destructive
// bulk actions (clear history, remove source, logout). First tap arms the
// button in danger red for 4 seconds; second tap confirms and returns true.
func (a *app) armedDangerButton(c *ui.Context, key, label, confirmLabel string) bool {
	p := paletteFor(c)
	now := c.Now()
	armed := a.armed.key == key && now.Before(a.armed.until)
	if a.armed.key == key && !armed {
		a.armed = armedConfirmState{}
	}
	if armed {
		c.After(a.armed.until.Sub(now))
	}

	shownLabel := label
	if armed {
		shownLabel = confirmLabel
	}

	btn := ui.Button(c.Key("armed-"+key), "").Label(shownLabel).Radius(20).Padding(9, 16)
	targetBg := p.surfaceSubtle
	targetText := p.text
	if armed {
		targetBg = p.danger
		targetText = p.dangerText
	} else if btn.Hovered() {
		targetBg = p.surfaceHover
	}
	bg := btn.AnimateColor("armed-bg", targetBg, animColor)
	fg := btn.AnimateColor("armed-fg", targetText, animColor)
	btn.Background(bg).TextColor(fg)
	if !armed {
		btn.Border(1, p.border)
	}
	applyPressPose(btn)
	btn.Children(func() {
		ui.Text(c, shownLabel).SingleLine().ContentTransition(ctReplace)
	})
	if btn.Clicked() {
		if armed {
			a.armed = armedConfirmState{}
			return true
		}
		a.armed = armedConfirmState{
			key:   key,
			until: now.Add(armedConfirmTTL),
		}
		c.After(armedConfirmTTL)
	}
	return false
}

// showPlayerOSD displays a brief centered capsule over the video for immediate
// tactile confirmation of keyboard/remote/control actions.
func (a *app) showPlayerOSD(text string) {
	if text == "" {
		return
	}
	a.playerOSD = playerOSDState{
		text: text,
		ttl:  playerOSDDuration,
	}
}

func (a *app) playerOSDPill(c *ui.Context) {
	if a.playerOSD.text == "" {
		return
	}
	now := c.Now()
	if a.playerOSD.until.IsZero() && a.playerOSD.ttl > 0 {
		a.playerOSD.until = now.Add(a.playerOSD.ttl)
	}
	if !a.playerOSD.until.IsZero() && !now.Before(a.playerOSD.until) {
		a.playerOSD = playerOSDState{}
		return
	}
	if !a.playerOSD.until.IsZero() {
		c.After(a.playerOSD.until.Sub(now))
	}
	ui.Row(c.Key("player-osd")).Absolute().Top(0).Bottom(0).Left(0).Right(0).Center().PassThrough().
		Transition(transOSD).Children(func() {
		ui.Row(c).Padding(10, 18).Radius(18).
			Material(glass.Glass{Style: glass.Regular, Tint: ui.RGBA(12, 16, 28, 0.78)}).
			TextColor(playerText).PassThrough().Children(func() {
			ui.Text(c, a.playerOSD.text).FontSize(16).Bold().SingleLine().ContentTransition(ctReplace)
		})
	})
}

func (a *app) showSkipUndo() {
	a.skipUndo = skipUndoState{active: true}
}

func (a *app) skipUndoPill(c *ui.Context, p mediaPlayer) {
	if !a.skipUndo.active {
		return
	}
	now := c.Now()
	if a.skipUndo.until.IsZero() {
		a.skipUndo.until = now.Add(skipUndoDuration)
	}
	if !now.Before(a.skipUndo.until) {
		a.skipUndo = skipUndoState{}
		return
	}
	c.After(a.skipUndo.until.Sub(now))
	ui.Row(c.Key("skip-undo")).Absolute().Left(24).Bottom(128).Padding(8, 12).Gap(10).Radius(16).
		Material(glass.Glass{Style: glass.Regular, Tint: ui.RGBA(12, 16, 28, 0.80)}).
		TextColor(playerText).AlignItems(ui.Center).Transition(transToast).Children(func() {
		ui.Text(c, "已跳过片头").FontSize(13).SingleLine()
		btn := ui.Button(c, "回看片头").Radius(12).Padding(4, 10).
			Material(glass.Glass{Style: glass.Clear, Interactive: true}).TextColor(playerText)
		applyPressPose(btn)
		if btn.Clicked() {
			a.skipUndo = skipUndoState{}
			a.resumeAt, a.resumeIssued = 0, false
			p.Seek(0)
			a.syncNowPlayingState()
			a.showPlayerOSD("已回到片头")
		}
	})
}

// upNextTarget returns the next episode and remaining seconds until auto-next
// when the viewer is actively playing into the final seconds of an episode.
func (a *app) upNextTarget(p mediaPlayer) (lineName string, next catalog.Episode, remaining time.Duration, ok bool) {
	if !a.hasEpisodes() || !a.autoNext || a.autoNextPending || a.autoNextCanceled ||
		a.running(jobPlay) || a.playFailure != nil || a.resumeAt != 0 ||
		(a.panelChoice != nil && a.panelOpen) ||
		p == nil || !p.Ready() || !p.Playing() || p.Buffering() || p.Err() != nil {
		return "", catalog.Episode{}, 0, false
	}
	dur := p.Duration()
	if dur <= 15*time.Second {
		return "", catalog.Episode{}, 0, false
	}
	src, ep, found := a.currentEpisode()
	if !found {
		return "", catalog.Episode{}, 0, false
	}
	episodes := a.title.detail.Sources[src].Episodes
	if ep+1 >= len(episodes) {
		return "", catalog.Episode{}, 0, false
	}
	endTarget := dur
	if rule := a.seriesSkips.get(a.title.name); rule.fits(dur) && rule.Ending > 0 {
		endTarget = dur - time.Duration(rule.Ending)*time.Second
	}
	rem := endTarget - p.Position()
	if rem <= 0 || rem > upNextWindow {
		return "", catalog.Episode{}, 0, false
	}
	return a.title.detail.Sources[src].Name, episodes[ep+1], rem, true
}

func (a *app) upNextPill(c *ui.Context, p mediaPlayer, showControls bool) {
	lineName, next, rem, ok := a.upNextTarget(p)
	if !ok {
		return
	}
	secs := max(1, int(math.Ceil(rem.Seconds())))
	c.After(500 * time.Millisecond)
	bottom := float32(28)
	if showControls {
		bottom = 128
	}
	ui.Row(c.Key("up-next-pill")).Absolute().Right(24).Bottom(bottom).Padding(10, 14).Gap(10).Radius(18).
		Material(glass.Glass{Style: glass.Regular, Tint: ui.RGBA(12, 16, 28, 0.84)}).
		TextColor(playerText).AlignItems(ui.Center).Transition(transToast).Children(func() {
		ui.Text(c, fmt.Sprintf("%d 秒后播放 %s", secs, next.Name)).FontSize(13).SingleLine().ContentTransition(ctNumeric)
		playBtn := ui.Button(c, "立即播放").Radius(12).Padding(5, 11).
			Material(glass.Glass{Tint: ui.RGBA(255, 90, 64, 0.92), Interactive: true}).TextColor(ui.Hex("#1a0f0c"))
		applyPressPose(playBtn)
		if playBtn.Clicked() {
			a.recordEpisodeCompletion(p)
			a.autoNextPending = true
			a.playEpisode(lineName, next)
		}
		cancelBtn := ui.Button(c, "暂不连播").Label("取消自动连播").Radius(12).Padding(5, 10).
			Material(glass.Glass{Style: glass.Clear, Interactive: true}).TextColor(playerText)
		applyPressPose(cancelBtn)
		if cancelBtn.Clicked() {
			a.autoNextCanceled = true
			a.showPlayerOSD("本集已取消自动连播")
		}
	})
}
