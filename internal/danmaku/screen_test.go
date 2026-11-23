package danmaku

import (
	"math"
	"testing"
	"time"
)

func TestScreenUsesMediaTimeForPauseSeekAndResize(t *testing.T) {
	var screen Screen
	screen.Reset([]Comment{
		{Time: 0, Mode: 1, Text: "scroll"},
		{Time: 0, Mode: 5, Text: "top"},
		{Time: 0, Mode: 4, Text: "bottom"},
		{Time: 20 * time.Second, Mode: 1, Text: "later"},
	})
	measure := func(string) float32 { return 100 }
	for _, tc := range []struct {
		name  string
		at    time.Duration
		width float32
		count int
		x     float32
	}{
		{"start", 0, 800, 3, 800},
		{"playing", 2 * time.Second, 800, 3, 575},
		{"paused", 2 * time.Second, 800, 3, 575},
		{"seek back", time.Second, 800, 3, 687.5},
		{"resize", time.Second, 1000, 3, 890},
		{"fixed expired", 5 * time.Second, 800, 1, 237.5},
		{"seek forward", 21 * time.Second, 800, 1, 687.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := screen.Frame(tc.at, tc.width, 400, 20, 1, measure)
			if len(frame) != tc.count || math.Abs(float64(frame[0].X-tc.x)) > 0.01 {
				t.Fatalf("frame = %+v, want count=%d x=%v", frame, tc.count, tc.x)
			}
			if len(frame) == 3 {
				// Scrolling starts from row 0 (no permanent empty top band),
				// and fixed layers are emitted after scrolling.
				if frame[0].Mode != 1 || frame[0].Y != 0 || frame[1].Mode != 5 || frame[1].Y != 0 || frame[2].Mode != 4 || frame[2].Y <= frame[0].Y {
					t.Fatalf("unexpected layer placement: %+v", frame)
				}
			}
		})
	}
	screen.Reset(nil)
	if got := screen.Frame(0, 800, 400, 20, 1, measure); len(got) != 0 {
		t.Fatalf("old episode retained: %+v", got)
	}
}

func TestScreenAvoidsOverlappingAndCatchUp(t *testing.T) {
	var screen Screen
	screen.Reset([]Comment{
		{Mode: 1, Text: "short"},
		{Time: 2 * time.Second, Mode: 1, Text: "too wide"},
		{Time: 4 * time.Second, Mode: 1, Text: "next"},
	})
	measure := func(text string) float32 {
		if text == "too wide" {
			return 1000
		}
		return 100
	}
	frame := screen.Frame(4*time.Second, 800, 30, 20, 1, measure)
	if len(frame) != 2 || frame[0].Text != "short" || frame[1].Text != "next" {
		t.Fatalf("lane schedule = %+v", frame)
	}
	if frame[0].X+frame[0].Width+12 > frame[1].X {
		t.Fatalf("comments overlap: %+v", frame)
	}
}

func TestScreenBoundsDenseComments(t *testing.T) {
	comments := make([]Comment, maxComments)
	for i := range comments {
		comments[i] = Comment{Mode: 1, Text: "dense"}
	}
	var screen Screen
	screen.Reset(comments)
	measured := 0
	frame := screen.Frame(0, 800, 30, 20, 1, func(string) float32 {
		measured++
		return 100
	})
	if len(frame) != 1 || measured != 1 {
		t.Fatalf("dense timeline: visible=%d, measured=%d", len(frame), measured)
	}
}

func TestScreenKeepsFixedCommentsInShortAreas(t *testing.T) {
	comments := []Comment{
		{Time: 0, Mode: 1, Text: "scroll"},
		{Time: 0, Mode: 5, Text: "top"},
		{Time: 0, Mode: 4, Text: "bottom"},
	}
	measure := func(string) float32 { return 100 }
	for _, tc := range []struct {
		name   string
		height float32
	}{
		{"one row", 30},
		{"two rows", 60},
		{"three rows", 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var screen Screen
			screen.Reset(comments)
			frame := screen.Frame(0, 800, tc.height, 20, 0.5, measure)
			if len(frame) != 3 {
				t.Fatalf("dropped fixed comments in short area: %+v", frame)
			}
			lineHeight := float32(20 * 1.4)
			for _, sprite := range frame {
				if sprite.Y < 0 || sprite.Y+lineHeight > tc.height+0.01 {
					t.Fatalf("sprite out of vertical bounds [0, %v]: %+v", tc.height, sprite)
				}
			}
			if frame[0].Mode != 1 || frame[0].Y != 0 || frame[1].Mode != 5 || frame[2].Mode != 4 {
				t.Fatalf("unexpected scroll/fixed layering: %+v", frame)
			}
		})
	}
}

func TestScreenWidthIndependentSpeedAndNegativeTime(t *testing.T) {
	measure := func(string) float32 { return 100 }
	var narrow, wide Screen
	narrow.Reset([]Comment{{Time: 0, Mode: 1, Text: "scroll"}})
	wide.Reset([]Comment{{Time: 0, Mode: 1, Text: "scroll"}})
	a := narrow.Frame(2*time.Second, 800, 400, 20, 0.5, measure)
	b := wide.Frame(2*time.Second, 1600, 400, 20, 0.5, measure)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("missing sprites: narrow=%+v wide=%+v", a, b)
	}
	// Distance traveled from right edge in 2s should stay steady across widths
	// rather than doubling on a 2x wider screen.
	distNarrow := 800 - a[0].X
	distWide := 1600 - b[0].X
	if math.Abs(float64(distNarrow-distWide)) > 10 {
		t.Fatalf("wide screen scrolled too fast: narrow=%v wide=%v", distNarrow, distWide)
	}

	// Narrow screen with high speed still finds active fixed comments when
	// scrolling duration drops below fixedLifetime.
	var fixedScreen Screen
	fixedScreen.Reset([]Comment{{Time: 1 * time.Second, Mode: 5, Text: "top"}})
	if got := fixedScreen.Frame(4*time.Second, 200, 200, 20, 1.5, measure); len(got) != 1 {
		t.Fatalf("narrow high-speed lookback dropped fixed comment: %+v", got)
	}

	// Negative effective time waits for t=0 comments, and Next reports false at end.
	var offsetScreen Screen
	offsetScreen.Reset([]Comment{{Time: 0, Mode: 5, Text: "at zero"}})
	if got := offsetScreen.Frame(-3*time.Second, 800, 400, 20, 0.5, measure); len(got) != 0 {
		t.Fatalf("showed future comment at negative time: %+v", got)
	}
	if next, ok := offsetScreen.Next(); !ok || next != 0 {
		t.Fatalf("Next at negative time = %v, %v; want 0, true", next, ok)
	}
	if got := offsetScreen.Frame(0, 800, 400, 20, 0.5, measure); len(got) != 1 {
		t.Fatalf("t=0 comment did not appear once offset reached zero: %+v", got)
	}
	if _, ok := offsetScreen.Next(); ok {
		t.Fatal("Next reported true after the timeline ended")
	}
}

func TestScreenSetDensityCapsRowsAndGap(t *testing.T) {
	comments := make([]Comment, 20)
	for i := range comments {
		comments[i] = Comment{Time: 0, Mode: 1, Text: "dense"}
	}
	var screen Screen
	screen.SetDensity(3, 48)
	screen.Reset(comments)
	frame := screen.Frame(0, 800, 400, 20, 1, func(string) float32 { return 100 })
	if len(frame) != 3 {
		t.Fatalf("low density should cap visible rows to 3: got %d", len(frame))
	}
	screen.SetDensity(12, 12)
	frame = screen.Frame(0, 800, 400, 20, 1, func(string) float32 { return 100 })
	if len(frame) != 12 {
		t.Fatalf("normal density should allow 12 rows: got %d", len(frame))
	}
}
