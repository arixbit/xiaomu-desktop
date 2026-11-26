package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// Unified Apple-grade motion language for Xiaomu.
//
// Core rules:
//  1. Motion explains causality (origin, destination, ownership, feedback), never decorates.
//  2. Five shared timing primitives (3 critically damped springs + Oklab color + NonMotion fade)
//     drive the entire desktop app so every state change shares one rhythm.
//  3. Asymmetric enter/exit: spring entrances are interruptible and velocity-preserving;
//     exits use brisk EaseIn (30–40% faster) to clear out of the user's way.
//  4. Paint-only AnimatePose & AnimateColor keep virtualized grids/lists (2k–10k items)
//     free of relayouts.
//  5. Direct manipulation (seek X tracking, window resize) never lags behind the pointer.
//  6. System ReduceMotion is honored everywhere: geometry/motion snaps cleanly while
//     Oklab color tinting and NonMotion opacity fades remain calm and readable.
var (
	// 1. Snappy micro-interactions: button/chip press, card hover lift, chevron rotate,
	//    seek-bar hover/drag swell, icon/text/numeric swaps.
	animSnap = ui.Animation{
		Spring: ui.SpringOptions{Duration: 160 * time.Millisecond, Bounce: 0},
		Oklab:  true,
	}

	// 2. Controls & overlays: Match indicator rails, popovers/menus, player HUD,
	//    toasts, floating pills and position avoidance.
	animControl = ui.Animation{
		Spring: ui.SpringOptions{Duration: 220 * time.Millisecond, Bounce: 0},
		Oklab:  true,
	}

	// 3. Spatial surfaces: side drawer/dock, dialog panels, push/pop routes,
	//    inline disclosures & collapses.
	animSurface = ui.Animation{
		Spring: ui.SpringOptions{Duration: 280 * time.Millisecond, Bounce: 0},
		Oklab:  true,
	}

	// 4. Perceptual color tinting (Oklab): bg/border/text/status dots.
	//    Continues smoothly under ReduceMotion.
	animColor = ui.Animation{
		Duration: 150 * time.Millisecond,
		Ease:     ui.EaseOut,
		Oklab:    true,
	}

	// 5. Pure opacity crossfades: poster/ambient decode reveal, calm pulses.
	//    Continues smoothly under ReduceMotion.
	animFade = ui.Animation{
		Duration:  160 * time.Millisecond,
		Ease:      ui.EaseOut,
		NonMotion: true,
	}
)

// Typed shared-element Match identities so independent indicator groups never cross-fly.
type (
	sidebarIndicatorMatch   struct{ rail bool }
	tabUnderlineMatch       struct{ group string }
	liveGroupIndicatorMatch struct{}
	segmentedThumbMatch     struct{ group string }
)

var (
	// Bottom-right status toast.
	transToast = ui.ElementTransition{
		Animation:    &animControl,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: 12, Opacity: 0},
		Exit:         &ui.Motion{Y: 8, Opacity: 0},
	}

	// Floating player pills (skip-undo, up-next) that smoothly avoid the HUD & side panel.
	transFloatingPill = ui.ElementTransition{
		Animation:    &animControl,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Position:     true,
		Colors:       true,
		Enter:        &ui.Motion{Y: 12, Opacity: 0},
		Exit:         &ui.Motion{Y: 8, Opacity: 0},
	}

	// Center player OSD capsule, loading state pill, and playback stats overlay.
	transOSD = ui.ElementTransition{
		Animation:    &animSnap,
		ExitDuration: 120 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Scale: 0.93, Opacity: 0},
		Exit:         &ui.Motion{Scale: 0.97, Opacity: 0},
	}

	// Player top header bar (interruptible spring in, crisp EaseIn out).
	transHUDTop = ui.ElementTransition{
		Animation:    &animControl,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: -10, Opacity: 0},
		Exit:         &ui.Motion{Y: -8, Opacity: 0},
	}

	// Player bottom controls dock.
	transHUDBottom = ui.ElementTransition{
		Animation:    &animControl,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: 12, Opacity: 0},
		Exit:         &ui.Motion{Y: 10, Opacity: 0},
	}

	// Floating side drawer (compact/fullscreen episodes panel & Danmaku live drawer).
	transSideDrawer = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 160 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{X: 28, Opacity: 0},
		Exit:         &ui.Motion{X: 20, Opacity: 0},
	}

	// Docked wide-screen side panel (animates Width only).
	transSideDock = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 170 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Width:        true,
		Enter:        &ui.Motion{Collapse: true, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Opacity: 0},
	}

	// Centered floating overlay panel mounted inside an already-existing parent
	// (e.g. player-failure-layer).
	transDialogPanel = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: 10, Scale: 0.97, Opacity: 0},
		Exit:         &ui.Motion{Y: 6, Scale: 0.98, Opacity: 0},
	}

	// Modal backdrop scrim (parented to overlay root so Enter & Exit run cleanly).
	transDialogScrim = ui.ElementTransition{
		Duration:     170 * time.Millisecond,
		Ease:         ui.EaseOut,
		ExitDuration: 130 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Opacity: 0},
		Exit:         &ui.Motion{Opacity: 0},
	}

	// Popovers & menus attached via AttachTo: Flip mirrors Y automatically when
	// MyGo flips a panel to the opposite edge of its anchor.
	transPopoverUp   = transPopover(6)
	transPopoverDown = transPopover(-6)

	// Player header tooltips & lightweight attached callouts.
	transTooltip = ui.ElementTransition{
		Duration:     130 * time.Millisecond,
		Ease:         ui.EaseOut,
		ExitDuration: 90 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: -4, Opacity: 0, Flip: true},
		Exit:         &ui.Motion{Opacity: 0},
	}

	// Seek hover preview card/pill: instant full-contrast arrival for zero-latency
	// scrubbing, subtle lift + crisp fade-out on pointer leave.
	transSeekPreview = ui.ElementTransition{
		Animation:    &animSnap,
		ExitDuration: 100 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: 3, Scale: 0.97, Opacity: 1},
		Exit:         &ui.Motion{Y: 3, Scale: 0.97, Opacity: 0},
	}

	// Micro badges, status dots, search clear button, collapsed summaries.
	transMicroBadge = ui.ElementTransition{
		Animation:    &animSnap,
		ExitDuration: 100 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Scale: 0.65, Opacity: 0},
		Exit:         &ui.Motion{Scale: 0.65, Opacity: 0},
	}

	// Peer tab / content crossfade in place (zero travel, fast for repeated switching).
	transTabPage = ui.ElementTransition{
		Duration:     150 * time.Millisecond,
		Ease:         ui.EaseOut,
		ExitDuration: 100 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Opacity: 0},
		Exit:         &ui.Motion{Opacity: 0},
	}

	// Hierarchical route push & pop.
	transRoutePush = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{X: 16, Opacity: 0},
		Exit:         &ui.Motion{X: -10, Opacity: 0},
	}

	transRoutePop = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{X: -12, Opacity: 0},
		Exit:         &ui.Motion{X: 16, Opacity: 0},
	}

	// Cinema player stage enter/exit continuity over the shell.
	transPlayerStage = ui.ElementTransition{
		Animation:    &animControl,
		ExitDuration: 160 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Scale: 0.988, Opacity: 0},
		Exit:         &ui.Motion{Scale: 0.992, Opacity: 0},
	}

	// Back button reveal in toolbar (slide+fade without zero-width collapse so it stays immediately clickable).
	transBackButton = ui.ElementTransition{
		Animation:    &animControl,
		ExitDuration: 120 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{X: -8, Opacity: 0},
		Exit:         &ui.Motion{X: -6, Opacity: 0},
	}

	// Inline banner / progress strip collapse reveal (for non-immediate-click strips).
	transInlineBanner = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Height:       true,
		Enter:        &ui.Motion{Collapse: true, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Opacity: 0},
	}

	// Interactive inline disclosure (skip editor, error details): enters without
	// zero-height collapse so controls inside are immediately clickable, and
	// collapses smoothly on close.
	transInlineReveal = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Height:       true,
		Enter:        &ui.Motion{Y: -6, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Opacity: 0},
	}

	// SwiftUI-style content transitions.
	ctNumeric   = ui.ContentTransition{Numeric: true, Animation: &animSnap}
	ctCountdown = ui.ContentTransition{Numeric: true, Down: true, Animation: &animSnap}
	ctReplace   = ui.ContentTransition{Animation: &animSnap}
	ctIcon      = ui.ContentTransition{Animation: &animSnap}
)

func transPopover(enterY float32) ui.ElementTransition {
	return ui.ElementTransition{
		Animation:    &animControl,
		ExitDuration: 110 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: enterY, Scale: 0.96, Opacity: 0, Flip: true},
		Exit:         &ui.Motion{Y: enterY * 0.6, Scale: 0.98, Opacity: 0, Flip: true},
	}
}

func transMatchIndicator(match any, vertical bool) ui.ElementTransition {
	t := ui.ElementTransition{
		Match:     match,
		Animation: &animControl,
		Position:  true,
		Colors:    true,
	}
	if vertical {
		t.Height = true
	} else {
		t.Width = true
	}
	return t
}

func transContinueCard(dir int) ui.ElementTransition {
	if dir == 0 {
		return transTabPage
	}
	sign := float32(1)
	if dir < 0 {
		sign = -1
	}
	return ui.ElementTransition{
		Animation:    &animControl,
		ExitDuration: 130 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{X: 12 * sign, Opacity: 0},
		Exit:         &ui.Motion{X: -10 * sign, Opacity: 0},
	}
}

func ctNumericDir(down bool) ui.ContentTransition {
	if down {
		return ctCountdown
	}
	return ctNumeric
}

// applyPressPose applies tactile paint-only press feedback to buttons & chips.
func applyPressPose(el ui.Element) ui.Element {
	pose := ui.Pose{Scale: 1}
	if el.Pressed() {
		pose.Scale = 0.97
	}
	return el.AnimatePose(pose, animSnap)
}

// applyCardPose applies tactile paint-only hover lift & press compress to cards.
// Safe inside virtualized GridViews and Scroll strips (zero relayout).
func applyCardPose(el ui.Element) ui.Element {
	pose := ui.Pose{Scale: 1}
	switch {
	case el.Pressed():
		pose = ui.Pose{Scale: 0.975}
	case el.Hovered():
		pose = ui.Pose{Scale: 1.015, Y: -2}
	}
	return el.AnimatePose(pose, animSnap)
}

// applyChevronPose smoothly rotates disclosure chevrons 0 <-> 180 degrees in
// paint phase (snaps automatically under ReduceMotion).
func applyChevronPose(el ui.Element, open bool) ui.Element {
	deg := float32(0)
	if open {
		deg = 180
	}
	return el.AnimatePose(ui.Pose{Rotate: deg}, animSnap)
}

// applyDialogPanelPose gives ui.DialogBase panels their subtle rise+scale
// entrance. Because DialogBase creates backdrop and panel on the same frame,
// ElementTransition.Enter on panel would be skipped; AnimatePose + one-shot
// Local runs reliably and stays still under ReduceMotion.
func applyDialogPanelPose(c *ui.Context, panel ui.Element) ui.Element {
	if c.Preferences().ReduceMotion {
		return panel
	}
	entered := ui.Local(panel, "dlg-in", func() bool { return false })
	pose := ui.Pose{}
	if !*entered {
		*entered = true
		pose = ui.Pose{Y: 10, Scale: 0.97}
		c.Invalidate()
	}
	return panel.AnimatePose(pose, animSurface)
}

// applyFadeInOut smoothly fades a persistent overlay handle in/out using paint-only Fade.
func applyFadeInOut(el ui.Element, visible bool) ui.Element {
	fade := float32(0)
	if !visible {
		fade = 1
	}
	return el.AnimatePose(ui.Pose{Fade: fade}, animFade)
}

// keyedDialog wraps ui.DialogBase with a pre-construction Context.Key so each
// dialog owns an isolated overlay identity (preventing cross-morphing with
// exiting popovers or dialogs) without breaking DialogBase's backdrop click
// dismissal or leaking Context.Key when closed.
func keyedDialog(c *ui.Context, key string, open *bool, fn func(backdrop, panel ui.Element)) {
	if !*open {
		return
	}
	ui.DialogBase(c.Key(key), open, fn)
}

// keyedPopover wraps ui.PopoverBase with a pre-construction Context.Key so
// mutually exclusive popovers never collide on the same overlay slot.
func keyedPopover(c *ui.Context, key string, anchor ui.Element, open *bool, fn func(panel ui.Element)) {
	if !*open {
		return
	}
	ui.PopoverBase(c.Key(key), anchor, open, fn)
}

// loadingIndicator renders a spinning activity indicator, falling back to a
// calm static dot when ReduceMotion is enabled (MyGo's ui.Spinner advances its
// spokes on a 75ms timer regardless of ReduceMotion).
func loadingIndicator(c *ui.Context, size float32, color ui.Color) ui.Element {
	if c.Preferences().ReduceMotion {
		d := max(float32(5), size*0.56)
		return ui.Box(c).Size(d, d).Radius(d/2).Background(color).Opacity(0.75).Shrink(0).PassThrough()
	}
	return ui.Spinner(c).Size(size, size).TextColor(color).Shrink(0).PassThrough()
}

// applyPulseOpacity drives a calm breathing opacity loop for transient active
// recovery states (never use on idle indicators, since Keyframes rebuilds at
// display rate). Skips automatically under ReduceMotion.
func applyPulseOpacity(c *ui.Context, el ui.Element, key string, active bool, minAlpha float32) ui.Element {
	if !active || c.Preferences().ReduceMotion {
		return el
	}
	alpha := el.Keyframes(key, []ui.Keyframe{
		{At: 0, Value: 1},
		{At: 1200 * time.Millisecond, Value: minAlpha, Ease: ui.EaseInOut},
	}, ui.KeyframeOptions{
		Repeat:    ui.RepeatForever,
		Alternate: true,
		NonMotion: true,
	})
	return el.Opacity(alpha)
}

// applyShakePose triggers a brief horizontal macOS-style error shake when run
// increments. Skips automatically under ReduceMotion.
func applyShakePose(c *ui.Context, el ui.Element, key string, run int) ui.Element {
	if run <= 0 || c.Preferences().ReduceMotion {
		return el
	}
	dx := el.Keyframes(key, []ui.Keyframe{
		{At: 0, Value: 0},
		{At: 48 * time.Millisecond, Value: -4, Ease: ui.EaseOut},
		{At: 110 * time.Millisecond, Value: 4, Ease: ui.EaseInOut},
		{At: 172 * time.Millisecond, Value: -2, Ease: ui.EaseInOut},
		{At: 220 * time.Millisecond, Value: 0, Ease: ui.EaseOut},
	}, ui.KeyframeOptions{
		Run: uint64(run),
	})
	if dx != 0 {
		el.Translate(dx, 0)
	}
	return el
}

// animateButtonChrome synchronizes background, border and foreground Oklab color
// transitions on interactive buttons, chips and navigation rows.
// Constant per-element keys avoid per-frame string concatenations in hot lists.
func animateButtonChrome(el ui.Element, _ string, bg, border, fg ui.Color) ui.Color {
	el.Background(el.AnimateColor("chrome-bg", bg, animColor)).
		Border(1, el.AnimateColor("chrome-bd", border, animColor))
	return el.AnimateColor("chrome-fg", fg, animColor)
}

// slidingSegmented renders a native-feeling segmented control with a spring-gliding
// active pill indicator powered by ElementTransition.Match.
func slidingSegmented(c *ui.Context, groupKey string, selected *int, labels ...string) ui.Element {
	seg := ui.SegmentedBase(c.Key("seg-"+groupKey), selected, len(labels))
	p := paletteFor(c)
	seg.Track.AlignItems(ui.Stretch).Padding(2).Radius(9).Background(p.surfaceSubtle).
		Border(1, p.border).Gap(2).Children(func() {
		for i, label := range labels {
			on := *selected == i
			opt := seg.Segment(i).Grow(1).Padding(5, 10).Radius(7).Center().
				Cursor(ui.CursorPointer).Label(label)
			fgTarget := p.textMuted
			if on {
				fgTarget = p.text
			} else if opt.Hovered() {
				fgTarget = ui.RGBA(244, 246, 251, 0.90)
			}
			fg := opt.AnimateColor("seg-fg", fgTarget, animColor)
			applyPressPose(opt)
			opt.Children(func() {
				if on {
					ui.Box(c.Key(fmt.Sprintf("seg-thumb-%s-%d", groupKey, i))).Absolute().Fill().Radius(7).
						Background(p.surfaceSelected).
						Border(1, p.borderControl).
						Transition(transMatchIndicator(segmentedThumbMatch{group: groupKey}, false)).
						PassThrough()
				}
				txt := ui.Text(c, label).FontSize(12).TextColor(fg).SingleLine().PassThrough()
				if on {
					txt.Bold()
				}
			})
		}
	})
	return seg.Track
}

// posterRevealAlpha returns the opacity for a poster inside box. Already-cached
// posters that arrive loaded on their first visible frame appear at 1.0 right
// away (no blink while scrolling virtualized grids); posters that decode after
// the placeholder mounted crossfade smoothly from 0 -> 1.
func posterRevealAlpha(box ui.Element, loaded bool) float32 {
	wasLoaded := ui.Local(box, "poster-was-loaded", func() bool { return loaded })
	if !loaded {
		*wasLoaded = false
		return 0
	}
	if !*wasLoaded {
		*wasLoaded = true
		box.Jump("poster-fade", 0)
	}
	return box.AnimateTo("poster-fade", 1, animFade)
}

func applyPosterImage(c *ui.Context, box ui.Element, poster ui.ImageSource) {
	alpha := posterRevealAlpha(box, poster != nil)
	if poster == nil {
		return
	}
	img := ui.Image(c, poster).Fill().Fit(ui.Cover)
	if alpha < 1 {
		img.Opacity(alpha)
	}
}

// applyAmbientPoster crossfades blurred ambient backdrop art in lockstep with
// poster decoding. The fade opacity sits on host so the blurred image itself
// stays static and cached across every frame of the reveal.
func applyAmbientPoster(c *ui.Context, host ui.Element, poster ui.ImageSource, targetOpacity, blur float32, mask ui.LinearGradient) {
	alpha := posterRevealAlpha(host, poster != nil)
	if poster == nil {
		return
	}
	if alpha < 1 {
		host.Opacity(alpha)
	}
	ui.Image(c, poster).Fill().Fit(ui.Cover).Blur(blur).Opacity(targetOpacity).Cache().MaskLinearGradient(mask).PassThrough()
}

func routeTransition(dir int) ui.ElementTransition {
	switch {
	case dir > 0:
		return transRoutePush
	case dir < 0:
		return transRoutePop
	default:
		return transTabPage
	}
}
