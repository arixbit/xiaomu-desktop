package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// Unified Apple-grade motion language for Xiaomu.
//
// Rules:
//  1. Every animation explains a state transition (origin, destination, feedback).
//  2. Prefer paint-only AnimatePose for hover/press/cards so virtualized grids
//     never relayout.
//  3. In ElementTransition, always opt into explicit properties (Colors, Width,
//     Height) so MyGo never triggers unintended full-box Size relayouts.
//  4. Asymmetric timing: poised entrance (200–280ms), crisp exit (140–170ms).
//  5. Respect Preferences.ReduceMotion automatically (MyGo snaps geometry and
//     keeps NonMotion fades & Oklab color transitions smooth).
var (
	// Micro tactile feedback: button/chip press, card hover lift, icon/label swaps.
	animMicro = ui.Animation{
		Spring: ui.SpringOptions{Duration: 150 * time.Millisecond, Bounce: 0},
	}

	// Perceptual color tinting (hover/selected/armed danger). Animates under ReduceMotion.
	animColor = ui.Animation{
		Duration: 150 * time.Millisecond,
		Ease:     ui.EaseOut,
		Oklab:    true,
	}

	// Pure opacity crossfades (poster decode reveal, ReduceMotion-safe).
	animFade = ui.Animation{
		Duration:  160 * time.Millisecond,
		Ease:      ui.EaseOut,
		NonMotion: true,
	}

	// Surface & panel spring: side drawer, docked panel, dialogs, rail width.
	animSurface = ui.Animation{
		Spring: ui.SpringOptions{Duration: 280 * time.Millisecond, Bounce: 0},
	}

	// Floating toast / countdown pill spring: crisp settle with subtle life.
	animPill = ui.Animation{
		Spring: ui.SpringOptions{Duration: 240 * time.Millisecond, Bounce: 0.04},
	}

	// Player HUD reveal & dismiss (asymmetric).
	animHUDIn = ui.Animation{Duration: 220 * time.Millisecond, Ease: ui.EaseOut}
)

var (
	// Bottom-right status toast, undo bar, and auto-next countdown pill.
	transToast = ui.ElementTransition{
		Animation:    &animPill,
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: 14, Opacity: 0},
		Exit:         &ui.Motion{Y: 8, Opacity: 0},
	}

	// Center player OSD capsule (Play/Pause, ±10s, Volume, Speed, Danmaku).
	transOSD = ui.ElementTransition{
		Duration:     170 * time.Millisecond,
		Ease:         ui.EaseOut,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Scale: 0.92, Opacity: 0},
		Exit:         &ui.Motion{Scale: 0.96, Opacity: 0},
	}

	// Player top header bar (slides in gently from above).
	transHUDTop = ui.ElementTransition{
		Animation:    &animHUDIn,
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: -10, Opacity: 0},
		Exit:         &ui.Motion{Y: -8, Opacity: 0},
	}

	// Player bottom controls dock (slides in gently from below).
	transHUDBottom = ui.ElementTransition{
		Animation:    &animHUDIn,
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: 12, Opacity: 0},
		Exit:         &ui.Motion{Y: 10, Opacity: 0},
	}

	// Floating side drawer (compact/fullscreen episodes panel & Danmaku live drawer).
	transSideDrawer = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 170 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{X: 28, Opacity: 0},
		Exit:         &ui.Motion{X: 20, Opacity: 0},
	}

	// Docked wide-screen side panel (animates Width only).
	transSideDock = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 180 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Width:        true,
		Enter:        &ui.Motion{Collapse: true, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Opacity: 0},
	}

	// Modal dialogs: subtle lift & scale, crisp exit.
	transDialogPanel = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Y: 10, Scale: 0.97, Opacity: 0},
		Exit:         &ui.Motion{Y: 6, Scale: 0.98, Opacity: 0},
	}

	transDialogScrim = ui.ElementTransition{
		Duration:     180 * time.Millisecond,
		Ease:         ui.EaseOut,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Opacity: 0},
		Exit:         &ui.Motion{Opacity: 0},
	}

	// Route transitions: peer tabs crossfade in place; pushed pages glide subtly.
	transTabPage = ui.ElementTransition{
		Duration:     160 * time.Millisecond,
		Ease:         ui.EaseOut,
		ExitDuration: 110 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{Opacity: 0},
		Exit:         &ui.Motion{Opacity: 0},
	}

	transRoutePush = ui.ElementTransition{
		Duration:     220 * time.Millisecond,
		Ease:         ui.EaseOut,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{X: 16, Opacity: 0},
		Exit:         &ui.Motion{X: -10, Opacity: 0},
	}

	transRoutePop = ui.ElementTransition{
		Duration:     200 * time.Millisecond,
		Ease:         ui.EaseOut,
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{X: -12, Opacity: 0},
		Exit:         &ui.Motion{X: 16, Opacity: 0},
	}

	// Back button reveal in toolbar (slide+fade without zero-width collapse so it stays immediately clickable).
	transBackButton = ui.ElementTransition{
		Duration:     180 * time.Millisecond,
		Ease:         ui.EaseOut,
		ExitDuration: 130 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Colors:       true,
		Enter:        &ui.Motion{X: -8, Opacity: 0},
		Exit:         &ui.Motion{X: -6, Opacity: 0},
	}

	// Inline disclosure / failure banner reveal.
	transInlineBanner = ui.ElementTransition{
		Animation:    &animSurface,
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
		Height:       true,
		Enter:        &ui.Motion{Collapse: true, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Opacity: 0},
	}

	// SwiftUI-style content transitions.
	ctNumeric = ui.ContentTransition{Numeric: true, Animation: &animMicro}
	ctReplace = ui.ContentTransition{Animation: &animMicro}
)

// applyPressPose applies tactile paint-only press feedback to buttons & chips.
func applyPressPose(el ui.Element) ui.Element {
	pose := ui.Pose{Scale: 1}
	if el.Pressed() {
		pose.Scale = 0.97
	}
	return el.AnimatePose(pose, animMicro)
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
	return el.AnimatePose(pose, animMicro)
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
