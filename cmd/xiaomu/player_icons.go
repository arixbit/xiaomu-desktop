package main

import "github.com/egoist/mygo/ui"

// Player HUD and main-shell SVG icons (24x24 viewBox, 1.8px rounded stroke matching navIcon*).
var (
	// Transport
	iconPrev = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M6 5.5v13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><path d="M18.2 6.2a.8.8 0 0 0-1.24-.66l-8.1 5.8a.8.8 0 0 0 0 1.32l8.1 5.8a.8.8 0 0 0 1.24-.66z" fill="currentColor"/></svg>`))
	iconPlay = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M8 5.4a.9.9 0 0 1 1.36-.77l10.2 6.6a.9.9 0 0 1 0 1.54l-10.2 6.6A.9.9 0 0 1 8 18.6z" fill="currentColor"/></svg>`))
	iconPause = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect x="6.5" y="5.5" width="3.8" height="13" rx="1.4" fill="currentColor"/><rect x="13.7" y="5.5" width="3.8" height="13" rx="1.4" fill="currentColor"/></svg>`))
	iconNext = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M5.8 6.2a.8.8 0 0 1 1.24-.66l8.1 5.8a.8.8 0 0 1 0 1.32l-8.1 5.8a.8.8 0 0 1-1.24-.66z" fill="currentColor"/><path d="M18 5.5v13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>`))

	// Volume
	iconVolumeHigh = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M4.5 9.5H8l4.5-3.8v12.6L8 14.5H4.5a1 1 0 0 1-1-1v-3a1 1 0 0 1 1-1z" fill="currentColor"/><path d="M15.8 8.8a4.5 4.5 0 0 1 0 6.4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><path d="M18.6 6.2a8 8 0 0 1 0 11.6" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`))
	iconVolumeMute = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M4.5 9.5H8l4.5-3.8v12.6L8 14.5H4.5a1 1 0 0 1-1-1v-3a1 1 0 0 1 1-1z" fill="currentColor"/><path d="M16 9.5l5 5m0-5l-5 5" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>`))

	// Unified Popover Icons
	iconAudioWave = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M5.5 9.5v5M10 5.5v13M14.5 8v8M19 10v4" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round"/></svg>`))
	iconCCLetters = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 12"><path d="M8.6 3.5a2.8 2.8 0 1 0 0 5M16.2 3.5a2.8 2.8 0 1 0 0 5" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round"/></svg>`))
	iconDanmakuSlash = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 18"><path d="M3 2.5L21 15.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`))
	iconMore = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="5.5" cy="12" r="1.75" fill="currentColor"/><circle cx="12" cy="12" r="1.75" fill="currentColor"/><circle cx="18.5" cy="12" r="1.75" fill="currentColor"/></svg>`))
	iconFullscreenEnter = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M4.5 9V5.8A1.3 1.3 0 0 1 5.8 4.5H9M15 4.5h3.2a1.3 1.3 0 0 1 1.3 1.3V9M19.5 15v3.2a1.3 1.3 0 0 1-1.3 1.3H15M9 19.5H5.8a1.3 1.3 0 0 1-1.3-1.3V15" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	iconFullscreenExit = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M9 4.5v3.2A1.3 1.3 0 0 1 7.7 9H4.5M19.5 9h-3.2A1.3 1.3 0 0 1 15 7.7V4.5M15 19.5v-3.2a1.3 1.3 0 0 1 1.3-1.3h3.2M4.5 15h3.2A1.3 1.3 0 0 1 9 16.3v3.2" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	iconStarFilled = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M12 3.6l2.55 5.17 5.7.83-4.12 4.02.97 5.68L12 16.62 6.9 19.3l.97-5.68-4.12-4.02 5.7-.83z" fill="currentColor" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>`))
	iconStarOutline = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M12 3.8l2.45 4.97 5.48.8-3.96 3.86.93 5.46L12 16.3l-4.9 2.59.93-5.46-3.96-3.86 5.48-.8z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/></svg>`))

	// Top HUD & Shell
	iconBack = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M14.8 5.8L8.6 12l6.2 6.2" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	iconPin = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M14.5 4.5l5 5-1.4 1.4-1.3-.5-3.4 3.4.5 2.6-1.3 1.3-3.5-3.5-4.3 4.3-1-1 4.3-4.3-3.5-3.5 1.3-1.3 2.6.5 3.4-3.4-.5-1.3z" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	iconPinFilled = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M14.5 4.5l5 5-1.4 1.4-1.3-.5-3.4 3.4.5 2.6-1.3 1.3-3.5-3.5-4.3 4.3-1-1 4.3-4.3-3.5-3.5 1.3-1.3 2.6.5 3.4-3.4-.5-1.3z" fill="currentColor" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	iconMiniPiP = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="14" rx="2.5" fill="none" stroke="currentColor" stroke-width="1.8"/><rect x="11.5" y="11" width="7" height="5.2" rx="1.2" fill="currentColor"/></svg>`))
	iconPanelList = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M4.5 7h15M4.5 12h15M4.5 17h11" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>`))
	iconClose = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M6.5 6.5l11 11M17.5 6.5l-11 11" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>`))
	iconRefresh = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M19.5 12a7.5 7.5 0 1 1-2.1-5.2L19.5 9" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/><path d="M19.5 4.8V9h-4.2" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	iconChevronRight = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M9.5 6.5L15 12l-5.5 5.5" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
	iconChevronDown  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M6.5 9.5L12 15l5.5-5.5" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>`))
)
