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

	backdrop        ui.LinearGradient
	surface         ui.Color
	surfaceSubtle   ui.Color
	surfaceHover    ui.Color
	surfaceSelected ui.Color
	cardPlaceholder ui.Color
	border          ui.Color

	text      ui.Color
	textMuted ui.Color

	accent           ui.Color
	accentText       ui.Color
	accentSoft       ui.Color
	danger           ui.Color
	dangerText       ui.Color
	success          ui.Color
	warning          ui.Color
	chipSelectedBg   ui.Color
	chipSelectedText ui.Color
	dialogScrim      ui.Color

	paneMat    glass.Glass
	controlMat glass.Glass
	primaryMat glass.Glass
	dialogMat  glass.Glass
}

var (
	brandAccentDark = ui.RGBA(255, 90, 64, 0.94)

	darkBackdrop = ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: ui.Hex("#0b1530"), At: 0},
		{Color: ui.Hex("#1e2846"), At: 0.42},
		{Color: ui.Hex("#4d3047"), At: 0.76},
		{Color: ui.Hex("#1b1422"), At: 1},
	}}
)

// appDarkTheme returns Xiaomu's always-dark cinema theme accented with warm
// coral while preserving desktop accessibility preferences (HighContrast, TextScale).
func appDarkTheme(c *ui.Context) *ui.Theme {
	th := ui.DarkTheme()
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
		surface:            ui.RGBA(14, 16, 26, 0.58),
		surfaceSubtle:      ui.RGBA(255, 255, 255, 0.06),
		surfaceHover:       ui.RGBA(255, 255, 255, 0.10),
		surfaceSelected:    ui.RGBA(255, 255, 255, 0.16),
		cardPlaceholder:    ui.Hex("#24242c"),
		border:             ui.RGBA(255, 255, 255, 0.10),
		text:               ui.Hex("#f5f5f7"),
		textMuted:          ui.RGBA(243, 239, 231, 0.78),
		accent:             brandAccentDark,
		accentText:         ui.Hex("#1a0f0c"),
		accentSoft:         ui.RGBA(255, 90, 64, 0.22),
		danger:             ui.RGBA(232, 69, 69, 0.92),
		dangerText:         ui.RGB(255, 255, 255),
		success:            ui.Hex("#3fb96a"),
		warning:            ui.RGB(255, 166, 130),
		chipSelectedBg:     ui.RGB(243, 239, 231),
		chipSelectedText:   ui.RGB(9, 15, 31),
		dialogScrim:        ui.RGBA(2, 8, 22, 0.64),
		paneMat:            glass.Glass{Clarity: 0.5},
		controlMat:         glass.Glass{Style: glass.Clear, Interactive: true},
		primaryMat:         glass.Glass{Tint: ui.RGBA(255, 90, 64, 0.92), Interactive: true},
		dialogMat:          glass.Glass{Style: glass.Regular},
	}
	if prefs.ReduceTransparency {
		p.surface = ui.Hex("#1a1c26")
		p.surfaceSubtle = ui.Hex("#232634")
		p.surfaceHover = ui.Hex("#2d3142")
		p.surfaceSelected = ui.Hex("#383d52")
		p.dialogScrim = ui.RGBA(2, 6, 16, 0.78)
	}
	if prefs.HighContrast {
		p.border = ui.RGBA(255, 255, 255, 0.28)
		p.textMuted = ui.RGBA(245, 245, 247, 0.90)
	}
	return p
}

// applySurface styles a card container with adaptive translucency and border.
func applySurface(el ui.Element, p appPalette, radius float32) ui.Element {
	el.Radius(radius).Background(p.surface)
	if p.highContrast || p.reduceTransparency {
		el.Border(1, p.border)
	}
	return el
}

// applyPane styles a glass pane (sidebar, welcome panel, dialogs) with an
// opaque high-legibility fallback when ReduceTransparency is enabled.
func applyPane(el ui.Element, p appPalette, mat glass.Glass) ui.Element {
	if p.reduceTransparency {
		return el.Background(ui.Hex("#171923")).Border(1, p.border)
	}
	el.Material(mat)
	if p.highContrast {
		el.Border(1, p.border)
	}
	return el
}

// Navigation icons for compact icon-rail sidebar (<820px width).
var (
	navIconHome      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M4 10.5L12 4l8 6.5V19a1.5 1.5 0 0 1-1.5 1.5h-4.5v-5.5h-4v5.5H5.5A1.5 1.5 0 0 1 4 19z" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linejoin="round"/></svg>`))
	navIconLive      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect x="3" y="6" width="18" height="12" rx="2.5" fill="none" stroke="currentColor" stroke-width="1.9"/><path d="M9.5 10l5 2-5 2z" fill="currentColor"/><path d="M8 21h8" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>`))
	navIconHistory   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5" fill="none" stroke="currentColor" stroke-width="1.9"/><path d="M12 7.5v5l3.2 2" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	navIconFavorites = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M6.5 4.5h11A1.5 1.5 0 0 1 19 6v14l-7-4.5L5 20V6a1.5 1.5 0 0 1 1.5-1.5z" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linejoin="round"/></svg>`))
	navIconSources   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><ellipse cx="12" cy="6.5" rx="7.5" ry="3" fill="none" stroke="currentColor" stroke-width="1.9"/><path d="M4.5 6.5v5.5c0 1.65 3.36 3 7.5 3s7.5-1.35 7.5-3V6.5" fill="none" stroke="currentColor" stroke-width="1.9"/><path d="M4.5 12v5.5c0 1.65 3.36 3 7.5 3s7.5-1.35 7.5-3V12" fill="none" stroke="currentColor" stroke-width="1.9"/></svg>`))
	navIconFile      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M3.5 7.5A1.5 1.5 0 0 1 5 6h4.2l1.8 2H19a1.5 1.5 0 0 1 1.5 1.5v8A1.5 1.5 0 0 1 19 19H5a1.5 1.5 0 0 1-1.5-1.5z" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linejoin="round"/></svg>`))
	navIconLink      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M10 14a4 4 0 0 1 0-5.66l2.34-2.34a4 4 0 1 1 5.66 5.66l-1.5 1.5" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/><path d="M14 10a4 4 0 0 1 0 5.66l-2.34 2.34a4 4 0 1 1-5.66-5.66l1.5-1.5" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>`))
	navIconUpdate    = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M12 4v11m0 0l-4-4m4 4l4-4M5 19h14" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
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
