package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	marqueeLeadPause  = 180 * time.Millisecond
	marqueeEndPause   = 750 * time.Millisecond
	marqueeStartPause = 650 * time.Millisecond
	marqueeSpeedPxSec = float32(46)
	marqueeMinTravelS = 0.35
	marqueeTailMargin = float32(2)
)

var marqueeNewlineReplacer = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// hoverMarqueeState tracks the single currently hovered overflowing label so
// ping-pong horizontal scrolling stays smooth across view rebuilds and stops
// immediately when the pointer moves away.
type hoverMarqueeState struct {
	key        string
	text       string
	since      time.Time
	seen       bool
	lastOffset float32
	lastOver   float32
}

func (m *hoverMarqueeState) beginFrame() {
	if !m.seen {
		m.clear()
	}
	m.seen = false
}

func (m *hoverMarqueeState) clear() {
	m.key = ""
	m.text = ""
	m.since = time.Time{}
	m.lastOffset = 0
	m.lastOver = 0
}

func (m *hoverMarqueeState) clearIf(key string) {
	if m.key == key {
		m.clear()
	}
}

func (m *hoverMarqueeState) touch(key, text string, now time.Time) time.Time {
	m.seen = true
	if m.key != key || m.text != text || m.since.IsZero() {
		m.key = key
		m.text = text
		m.since = now
		m.lastOffset = 0
		m.lastOver = 0
	}
	return m.since
}

// marqueeOffset computes the horizontal scroll offset (in logical pixels) for
// an overflowing single-line label at elapsed time since hover started.
// Motion is a smooth ping-pong: brief hold at left -> scroll left to reveal
// hidden detail -> pause at right end -> scroll right back to start -> pause ->
// repeat.
func marqueeOffset(elapsed time.Duration, overflow float32) (offset float32, animate bool, wait time.Duration) {
	if overflow <= 0.5 {
		return 0, false, 0
	}
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed < marqueeLeadPause {
		return 0, false, marqueeLeadPause - elapsed
	}
	travel := overflow + marqueeTailMargin
	legSec := max(float64(travel)/float64(marqueeSpeedPxSec), marqueeMinTravelS)
	leg := time.Duration(legSec * float64(time.Second))
	if leg <= 0 {
		return 0, false, 0
	}
	cycle := leg + marqueeEndPause + leg + marqueeStartPause
	pos := (elapsed - marqueeLeadPause) % cycle
	switch {
	case pos < leg:
		return travel * float32(pos) / float32(leg), true, 0
	case pos < leg+marqueeEndPause:
		return travel, false, leg + marqueeEndPause - pos
	case pos < leg+marqueeEndPause+leg:
		back := pos - leg - marqueeEndPause
		return travel * (1 - float32(back)/float32(leg)), true, 0
	default:
		return 0, false, cycle - pos
	}
}

func marqueeSpan(text string, fontSize float32, bold bool, fg ui.Color) ui.Span {
	sp := ui.Span{
		Text:  marqueeNewlineReplacer.Replace(text),
		Size:  fontSize,
		Color: fg,
	}
	if bold {
		sp.Weight = 700
	}
	return sp
}

type marqueeTextOptions struct {
	key        string
	text       string
	fontSize   float32
	bold       bool
	color      ui.Color
	hovered    bool
	centered   bool
	grow       bool
	fillHeight bool
	fixedWidth float32
	knownWidth float32
}

// hoverMarqueeText renders a single-line label that stays ellipsized at rest
// and automatically pans left and right when hovered without clicking if the
// label overflows its slot.
func (a *app) hoverMarqueeText(c *ui.Context, o marqueeTextOptions) {
	renderStatic := func() {
		a.marquee.clearIf(o.key)
		t := ui.Text(c, o.text).FontSize(o.fontSize).TextColor(o.color).SingleLine().PassThrough()
		if o.bold {
			t.Bold()
		}
		if o.grow {
			t.Grow(1)
		}
	}
	if !o.hovered || c.Preferences().ReduceMotion {
		renderStatic()
		return
	}
	sp := marqueeSpan(o.text, o.fontSize, o.bold, o.color)
	textW, textH := c.MeasureText(0, sp)
	if o.knownWidth > 0 && textW <= o.knownWidth+0.5 {
		renderStatic()
		return
	}
	since := a.marquee.touch(o.key, o.text, c.Now())
	box := ui.Box(c).PassThrough()
	switch {
	case o.fixedWidth > 0:
		box.Width(o.fixedWidth)
	case o.grow:
		box.Grow(1).MinWidth(0)
	default:
		box.FillWidth()
	}
	if o.fillHeight {
		box.FillHeight()
	} else if textH > 0 {
		box.Height(textH)
	}
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if r.W <= 0 || r.H <= 0 {
			return
		}
		w, h := p.MeasureText(0, sp)
		y := r.Y + (r.H-h)/2
		overflow := w - r.W
		if overflow <= 0.5 {
			a.marquee.lastOffset = 0
			a.marquee.lastOver = 0
			x := r.X
			if o.centered {
				x = r.X + (r.W-w)/2
			}
			p.Clip(r, 0, func() {
				p.RichText(x, y, 0, sp)
			})
			return
		}
		offset, animate, wait := marqueeOffset(p.Now().Sub(since), overflow)
		a.marquee.lastOffset = offset
		a.marquee.lastOver = overflow
		p.Clip(r, 0, func() {
			p.RichText(r.X-offset, y, 0, sp)
		})
		if animate {
			p.AnimationFrameRate(60)
		} else if wait > 0 {
			p.After(wait)
		}
	})
}
