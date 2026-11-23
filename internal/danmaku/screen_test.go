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
		{"resize", time.Second, 1000, 3, 862.5},
		{"fixed expired", 5 * time.Second, 800, 1, 237.5},
		{"seek forward", 21 * time.Second, 800, 1, 687.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := screen.Frame(tc.at, tc.width, 400, 20, measure)
			if len(frame) != tc.count || math.Abs(float64(frame[0].X-tc.x)) > 0.01 {
				t.Fatalf("frame = %+v, want count=%d x=%v", frame, tc.count, tc.x)
			}
			if len(frame) == 3 && (frame[1].Y >= frame[0].Y || frame[2].Y <= frame[0].Y) {
				t.Fatalf("fixed and scrolling comments share rows: %+v", frame)
			}
		})
	}
	screen.Reset(nil)
	if got := screen.Frame(0, 800, 400, 20, measure); len(got) != 0 {
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
	frame := screen.Frame(4*time.Second, 800, 30, 20, measure)
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
	frame := screen.Frame(0, 800, 30, 20, func(string) float32 {
		measured++
		return 100
	})
	if len(frame) != 1 || measured != 1 {
		t.Fatalf("dense timeline: visible=%d, measured=%d", len(frame), measured)
	}
}
