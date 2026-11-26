package main

import (
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// appPalette holds Xiaomu's cinema dark colors and materials, honoring desktop
// accessibility preferences (ReduceTransparency, HighContrast).
type appPalette struct {
	reduceTransparency bool
	highContrast       bool

	backdrop         ui.LinearGradient
	sidebarBg        ui.Color
	sidebarBorder    ui.Color
	surface          ui.Color
	surfaceCardHover ui.Color
	surfaceInner     ui.Color
	controlBg        ui.Color
	controlHover     ui.Color
	surfaceSubtle    ui.Color
	surfaceHover     ui.Color
	surfaceSelected  ui.Color
	cardPlaceholder  ui.Color
	border           ui.Color
	borderControl    ui.Color
	divider          ui.Color

	text      ui.Color
	textMuted ui.Color
	textFaint ui.Color

	accent           ui.Color
	accentHover      ui.Color
	accentText       ui.Color
	accentSoft       ui.Color
	accentBorder     ui.Color
	accentLightText  ui.Color
	gold             ui.Color
	goldSoft         ui.Color
	goldBorder       ui.Color
	danger           ui.Color
	dangerSoft       ui.Color
	dangerBorder     ui.Color
	dangerText       ui.Color
	dangerLightText  ui.Color
	success          ui.Color
	successText      ui.Color
	warning          ui.Color
	chipSelectedBg   ui.Color
	chipSelectedText ui.Color
	dialogScrim      ui.Color

	surfaceMat      glass.Glass
	surfaceHoverMat glass.Glass
	searchMat       glass.Glass
	paneMat         glass.Glass
	controlMat      glass.Glass
	primaryMat      glass.Glass
	dialogMat       glass.Glass
}

var (
	brandAccentDark = ui.RGBA(255, 90, 64, 0.94)

	darkBackdrop = ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: ui.Hex("#0b1530"), At: 0},
		{Color: ui.Hex("#1e2846"), At: 0.42},
		{Color: ui.Hex("#4d3047"), At: 0.76},
		{Color: ui.Hex("#1b1422"), At: 1},
	}}

	// appScrollbar is Xiaomu's slim translucent scrollbar paired with scrollGutter
	// right padding so thumbs never cover cards, buttons, or right-aligned metadata.
	appScrollbar = ui.ScrollbarStyle{
		TrackWidth: 10,
		ThumbWidth: 5,
		Radius:     3,
		MinLength:  28,
		Thumb:      ui.RGBA(255, 255, 255, 0.22),
		Hover:      ui.RGBA(255, 255, 255, 0.42),
		Active:     ui.RGBA(255, 255, 255, 0.62),
	}
)

const scrollGutter float32 = 14

// appDarkTheme returns Xiaomu's always-dark cinema theme accented with warm
// coral while preserving desktop accessibility preferences (HighContrast, TextScale).
func appDarkTheme(c *ui.Context) *ui.Theme {
	th := ui.DarkTheme()
	th.Inverse = ui.RGB(24, 28, 40)
	th.InverseText = ui.Hex("#f4f6fb")
	if c != nil {
		th = th.WithPreferences(c.Preferences())
	}
	return th.WithAccent(brandAccentDark)
}

func paletteFor(c *ui.Context) appPalette {
	prefs := c.Preferences()
	p := appPalette{
		reduceTransparency: prefs.ReduceTransparency,
		highContrast:       prefs.HighContrast,
		backdrop:           darkBackdrop,
		sidebarBg:          ui.Transparent,
		sidebarBorder:      ui.RGBA(255, 255, 255, 0.08),
		surface:            ui.RGBA(10, 13, 24, 0.44),
		surfaceCardHover:   ui.RGBA(18, 22, 38, 0.56),
		surfaceInner:       ui.RGBA(255, 255, 255, 0.055),
		controlBg:          ui.RGBA(255, 255, 255, 0.085),
		controlHover:       ui.RGBA(255, 255, 255, 0.15),
		surfaceSubtle:      ui.RGBA(255, 255, 255, 0.05),
		surfaceHover:       ui.RGBA(255, 255, 255, 0.10),
		surfaceSelected:    ui.RGBA(255, 255, 255, 0.13),
		cardPlaceholder:    ui.RGBA(14, 17, 30, 0.56),
		border:             ui.RGBA(255, 255, 255, 0.09),
		borderControl:      ui.RGBA(255, 255, 255, 0.13),
		divider:            ui.RGBA(255, 255, 255, 0.08),
		text:               ui.Hex("#f4f6fb"),
		textMuted:          ui.RGBA(206, 212, 228, 0.74),
		textFaint:          ui.RGBA(168, 176, 198, 0.56),
		accent:             brandAccentDark,
		accentHover:        ui.RGB(255, 106, 80),
		accentText:         ui.RGB(255, 255, 255),
		accentSoft:         ui.RGBA(255, 90, 64, 0.18),
		accentBorder:       ui.RGBA(255, 90, 64, 0.55),
		accentLightText:    ui.RGB(255, 174, 154),
		gold:               ui.RGB(252, 206, 98),
		goldSoft:           ui.RGBA(252, 206, 98, 0.14),
		goldBorder:         ui.RGBA(252, 206, 98, 0.42),
		danger:             ui.RGBA(232, 69, 69, 0.92),
		dangerSoft:         ui.RGBA(239, 68, 68, 0.15),
		dangerBorder:       ui.RGBA(248, 113, 113, 0.45),
		dangerText:         ui.RGB(255, 255, 255),
		dangerLightText:    ui.RGB(254, 202, 202),
		success:            ui.Hex("#3fb96a"),
		successText:        ui.RGB(114, 218, 148),
		warning:            ui.RGB(245, 180, 102),
		chipSelectedBg:     ui.RGBA(255, 90, 64, 0.18),
		chipSelectedText:   ui.RGB(255, 174, 154),
		dialogScrim:        ui.RGBA(4, 7, 16, 0.68),
		surfaceMat:         glass.Glass{Style: glass.Clear, Tint: ui.RGBA(10, 14, 26, 0.56)},
		surfaceHoverMat:    glass.Glass{Style: glass.Clear, Tint: ui.RGBA(20, 26, 44, 0.62)},
		searchMat:          glass.Glass{Style: glass.Clear, Tint: ui.RGBA(9, 14, 28, 0.62)},
		paneMat:            glass.Glass{Style: glass.Clear, Tint: ui.RGBA(13, 17, 28, 0.84)},
		controlMat:         glass.Glass{Style: glass.Clear, Interactive: true},
		primaryMat:         glass.Glass{Tint: ui.RGBA(255, 90, 64, 0.92), Interactive: true},
		dialogMat:          glass.Glass{Style: glass.Clear, Tint: ui.RGBA(13, 17, 28, 0.88)},
	}
	if prefs.ReduceTransparency {
		p.sidebarBg = ui.Hex("#131620")
		p.sidebarBorder = ui.RGBA(255, 255, 255, 0.12)
		p.surface = ui.Hex("#141722")
		p.surfaceCardHover = ui.Hex("#1c202e")
		p.surfaceInner = ui.Hex("#1c202e")
		p.controlBg = ui.Hex("#222635")
		p.controlHover = ui.Hex("#2c3144")
		p.surfaceSubtle = ui.Hex("#1c202e")
		p.surfaceHover = ui.Hex("#282d3f")
		p.surfaceSelected = ui.Hex("#31374d")
		p.cardPlaceholder = ui.Hex("#1a1d28")
		p.dialogScrim = ui.RGBA(2, 6, 16, 0.80)
	}
	if prefs.HighContrast {
		p.sidebarBorder = ui.RGBA(255, 255, 255, 0.28)
		p.border = ui.RGBA(255, 255, 255, 0.28)
		p.borderControl = ui.RGBA(255, 255, 255, 0.34)
		p.divider = ui.RGBA(255, 255, 255, 0.20)
		p.textMuted = ui.RGBA(245, 245, 247, 0.90)
		p.textFaint = ui.RGBA(224, 228, 240, 0.76)
	}
	return p
}

// applySurface styles a major card container with the player's dark-tinted
// clear glass material, crisp hairline border, and soft elevation shadow.
func applySurface(el ui.Element, p appPalette, radius float32) ui.Element {
	if p.reduceTransparency {
		return el.Radius(radius).Background(p.surface).Border(1, p.border).
			Shadow(0, 8, 22, 0, ui.RGBA(0, 0, 0, 0.30))
	}
	return el.Radius(radius).Material(p.surfaceMat).Border(1, p.border).
		Shadow(0, 8, 22, 0, ui.RGBA(0, 0, 0, 0.28))
}

// applyInteractiveSurface styles a hoverable card container (favorites, history,
// live channels) using the same dark-tinted glass without clobbering Material
// with a flat Background call.
func applyInteractiveSurface(el ui.Element, p appPalette, radius float32, hovered bool) ui.Element {
	if p.reduceTransparency {
		bg := p.surface
		border := p.border
		if hovered {
			bg = p.surfaceCardHover
			border = p.borderControl
		}
		return el.Radius(radius).Background(bg).Border(1, border).
			Shadow(0, 8, 22, 0, ui.RGBA(0, 0, 0, 0.30))
	}
	mat := p.surfaceMat
	border := p.border
	shadow := ui.RGBA(0, 0, 0, 0.28)
	if hovered {
		mat = p.surfaceHoverMat
		border = p.borderControl
		shadow = ui.RGBA(0, 0, 0, 0.36)
	}
	return el.Radius(radius).Material(mat).Border(1, border).Shadow(0, 8, 22, 0, shadow)
}

// applyInnerSurface styles a nested tile or sub-card inside a primary surface card.
func applyInnerSurface(el ui.Element, p appPalette, radius float32) ui.Element {
	return el.Radius(radius).Background(p.surfaceInner).Border(1, p.border)
}

// applyPane styles an elevated floating pane (welcome card, dialogs, toasts)
// with the player panel's deeper dark-tinted glass, border, and shadow.
func applyPane(el ui.Element, p appPalette, mat glass.Glass) ui.Element {
	if p.reduceTransparency {
		return el.Background(ui.Hex("#151824")).Border(1, p.borderControl).
			Shadow(0, 14, 36, 0, ui.RGBA(0, 0, 0, 0.48))
	}
	return el.Material(mat).Border(1, p.borderControl).Shadow(0, 14, 36, 0, ui.RGBA(0, 0, 0, 0.48))
}

// Navigation icons for compact icon-rail sidebar (<820px width).
var (
	navIconHome            = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M4 10.5L12 4l8 6.5V19a1.5 1.5 0 0 1-1.5 1.5h-4.5v-5.5h-4v5.5H5.5A1.5 1.5 0 0 1 4 19z" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linejoin="round"/></svg>`))
	navIconLive            = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect x="3" y="6" width="18" height="12" rx="2.5" fill="none" stroke="currentColor" stroke-width="1.9"/><path d="M9.5 10l5 2-5 2z" fill="currentColor"/><path d="M8 21h8" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>`))
	navIconHistory         = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5" fill="none" stroke="currentColor" stroke-width="1.9"/><path d="M12 7.5v5l3.2 2" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	navIconFavorites       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M6.5 4.5h11A1.5 1.5 0 0 1 19 6v14l-7-4.5L5 20V6a1.5 1.5 0 0 1 1.5-1.5z" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linejoin="round"/></svg>`))
	navIconFavoritesFilled = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M6.5 4.5h11A1.5 1.5 0 0 1 19 6v14l-7-4.5L5 20V6a1.5 1.5 0 0 1 1.5-1.5z" fill="currentColor" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/></svg>`))
	navIconSources         = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><ellipse cx="12" cy="6.5" rx="7.5" ry="3" fill="none" stroke="currentColor" stroke-width="1.9"/><path d="M4.5 6.5v5.5c0 1.65 3.36 3 7.5 3s7.5-1.35 7.5-3V6.5" fill="none" stroke="currentColor" stroke-width="1.9"/><path d="M4.5 12v5.5c0 1.65 3.36 3 7.5 3s7.5-1.35 7.5-3V12" fill="none" stroke="currentColor" stroke-width="1.9"/></svg>`))
	navIconFile            = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M3.5 7.5A1.5 1.5 0 0 1 5 6h4.2l1.8 2H19a1.5 1.5 0 0 1 1.5 1.5v8A1.5 1.5 0 0 1 19 19H5a1.5 1.5 0 0 1-1.5-1.5z" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linejoin="round"/></svg>`))
	navIconLink            = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M10 14a4 4 0 0 1 0-5.66l2.34-2.34a4 4 0 1 1 5.66 5.66l-1.5 1.5" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/><path d="M14 10a4 4 0 0 1 0 5.66l-2.34 2.34a4 4 0 1 1-5.66-5.66l1.5-1.5" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>`))
	navIconUpdate          = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M12 4v11m0 0l-4-4m4 4l4-4M5 19h14" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
)

func tabIcon(kind routeKind) *ui.SVG {
	switch kind {
	case routeLive:
		return navIconLive
	case routeHistory:
		return navIconHistory
	case routeFavorites:
		return navIconFavorites
	case routeSources:
		return navIconSources
	default:
		return navIconHome
	}
}
