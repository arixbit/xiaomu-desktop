package danmaku

import (
	"math"
	"testing"
	"time"
)

func TestScreenScrollSpeedAndLifetime(t *testing.T) {
	var screen Screen
	screen.Reset([]Comment{{Mode: 1, Text: "scroll"}, {Mode: 5, Text: "fixed"}})
	measure := func(string) float32 { return 100 }
	for _, tc := range []struct {
		name  string
		at    time.Duration
		speed float32
		count int
		x     float32
	}{
		{"old speed", 2 * time.Second, 1, 2, 575},
		{"half speed immediately", 2 * time.Second, 0.5, 2, 687.5},
		{"paused at half speed", 2 * time.Second, 0.5, 2, 687.5},
		{"quarter speed", 2 * time.Second, 0.25, 2, 743.75},
		{"fixed comments expire normally", 5 * time.Second, 0.5, 1, 518.75},
		{"slow comment survives past eight seconds", 12 * time.Second, 0.5, 1, 125},
		{"normal speed expires", 12 * time.Second, 1, 0, 0},
		{"slow track restored after speed change", 12 * time.Second, 0.25, 1, 462.5},
		{"slow track expires", 32 * time.Second, 0.25, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := screen.Frame(tc.at, 800, 400, 20, tc.speed, measure)
			if len(got) != tc.count || len(got) > 0 && math.Abs(float64(got[0].X-tc.x)) > 0.01 {
				t.Fatalf("frame = %+v, want count=%d x=%v", got, tc.count, tc.x)
			}
		})
	}
}

func TestScreenCollisionRulesFollowScrollSpeed(t *testing.T) {
	for _, speed := range []float32{0.25, 0.5, 1, 1.5} {
		var screen Screen
		delay := func(seconds float64) time.Duration {
			return time.Duration(seconds / float64(speed) * float64(time.Second))
		}
		screen.Reset([]Comment{
			{Mode: 1, Text: "short"},
			{Time: delay(2), Mode: 1, Text: "too wide"},
			{Time: delay(4), Mode: 1, Text: "next"},
		})
		frame := screen.Frame(delay(4), 800, 30, 20, speed, func(text string) float32 {
			if text == "too wide" {
				return 1000
			}
			return 100
		})
		if len(frame) != 2 || frame[0].Text != "short" || frame[1].Text != "next" || frame[0].X+frame[0].Width+12 > frame[1].X {
			t.Fatalf("speed=%v: overlapping or missing comments: %+v", speed, frame)
		}
	}
}
