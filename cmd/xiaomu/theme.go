package main

import (
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// appPalette holds semantic colors and materials derived from the window's
// current Light/Dark appearance and desktop accessibility preferences
// (ReduceTransparency, HighContrast).
type appPalette struct {
	dark               bool
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
	darkBackdrop = ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: ui.Hex("#0b1530"), At: 0},
		{Color: ui.Hex("#1e2846"), At: 0.42},
		{Color: ui.Hex("#4d3047"), At: 0.76},
		{Color: ui.Hex("#1b1422"), At: 1},
	}}
	lightBackdrop = ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: ui.Hex("#eef2f8"), At: 0},
		{Color: ui.Hex("#f4f6fa"), At: 0.44},
		{Color: ui.Hex("#f9eff1"), At: 0.78},
		{Color: ui.Hex("#eff2f7"), At: 1},
	}}
)

func paletteFor(c *ui.Context) appPalette {
	prefs := c.Preferences()
	dark := true
	if th := c.Theme(); th != nil {
		dark = th.Dark
	}
	if dark {
		p := appPalette{
			dark:               true,
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
			accent:             ui.RGBA(255, 90, 64, 0.94),
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

	p := appPalette{
		dark:               false,
		reduceTransparency: prefs.ReduceTransparency,
		highContrast:       prefs.HighContrast,
		backdrop:           lightBackdrop,
		surface:            ui.RGBA(255, 255, 255, 0.80),
		surfaceSubtle:      ui.RGBA(15, 23, 42, 0.05),
		surfaceHover:       ui.RGBA(15, 23, 42, 0.07),
		surfaceSelected:    ui.RGBA(15, 23, 42, 0.12),
		cardPlaceholder:    ui.Hex("#e2e5ec"),
		border:             ui.RGBA(15, 23, 42, 0.09),
		text:               ui.Hex("#1d1d1f"),
		textMuted:          ui.RGBA(29, 29, 31, 0.64),
		accent:             ui.RGB(229, 72, 77),
		accentText:         ui.RGB(255, 255, 255),
		accentSoft:         ui.RGBA(229, 72, 77, 0.14),
		danger:             ui.RGB(217, 45, 32),
		dangerText:         ui.RGB(255, 255, 255),
		success:            ui.Hex("#1e8e4e"),
		warning:            ui.Hex("#c2410c"),
		chipSelectedBg:     ui.Hex("#1d1d1f"),
		chipSelectedText:   ui.RGB(255, 255, 255),
		dialogScrim:        ui.RGBA(15, 23, 42, 0.36),
		paneMat:            glass.Glass{Clarity: 0.55},
		controlMat:         glass.Glass{Style: glass.Clear, Interactive: true},
		primaryMat:         glass.Glass{Tint: ui.RGBA(229, 72, 77, 0.94), Interactive: true},
		dialogMat:          glass.Glass{Style: glass.Regular},
	}
	if prefs.ReduceTransparency {
		p.surface = ui.RGB(255, 255, 255)
		p.surfaceSubtle = ui.Hex("#ebedf2")
		p.surfaceHover = ui.Hex("#e0e4ec")
		p.surfaceSelected = ui.Hex("#d4d9e4")
		p.dialogScrim = ui.RGBA(15, 23, 42, 0.55)
	}
	if prefs.HighContrast {
		p.border = ui.RGBA(15, 23, 42, 0.28)
		p.textMuted = ui.RGBA(29, 29, 31, 0.84)
	}
	return p
}

// applySurface styles a card container with adaptive translucency and border.
func applySurface(el ui.Element, p appPalette, radius float32) ui.Element {
	el.Radius(radius).Background(p.surface)
	if !p.dark || p.highContrast || p.reduceTransparency {
		el.Border(1, p.border)
	}
	return el
}

// applyPane styles a glass pane (sidebar, welcome panel, dialogs) with an
// opaque high-legibility fallback when ReduceTransparency is enabled.
func applyPane(el ui.Element, p appPalette, mat glass.Glass) ui.Element {
	if p.reduceTransparency {
		bg := ui.Hex("#171923")
		if !p.dark {
			bg = ui.RGBA(255, 255, 255, 0.96)
		}
		return el.Background(bg).Border(1, p.border)
	}
	el.Material(mat)
	if p.highContrast {
		el.Border(1, p.border)
	}
	return el
}

func (a *app) syncWindowCaptionTheme(c *ui.Context) {
	if runtime.GOOS != "windows" || a.window == nil {
		return
	}
	dark := a.player != nil
	if !dark {
		if th := c.Theme(); th != nil {
			dark = th.Dark
		} else {
			dark = true
		}
	}
	if a.windowThemeKnown && a.windowDarkTheme == dark {
		return
	}
	a.windowThemeKnown = true
	a.windowDarkTheme = dark
	if dark {
		a.window.SetWindowButtonsTheme(mygo.ThemeDark)
	} else {
		a.window.SetWindowButtonsTheme(mygo.ThemeLight)
	}
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
