package danmaku

import (
	"math"
	"slices"
	"time"
)

const scrollLifetime = 8 * time.Second
const fixedLifetime = 4 * time.Second

// Sprite is one comment positioned relative to the video overlay.
type Sprite struct {
	Comment
	X, Y, Width float32
}

type lane struct {
	at    time.Duration
	width float32
	used  bool
}

type activeComment struct {
	Comment
	width float32
	row   int
}

// Screen schedules bounded rows and derives every position from media time.
// Pause, buffering and playback speed therefore do not need a separate clock.
type Screen struct {
	comments            []Comment
	active              []activeComment
	visible             []Sprite
	scroll, top, bottom []lane
	next                int
	position            time.Duration
	width, height, size float32
	initialized         bool
}

// Reset replaces the sorted timeline and discards the previous episode.
func (s *Screen) Reset(comments []Comment) {
	*s = Screen{comments: comments}
}

// Frame returns visible comments. measure must use the requested font size.
// The returned slice belongs to Screen and is reused by the next frame.
func (s *Screen) Frame(at time.Duration, width, height, size float32, measure func(string) float32) []Sprite {
	if width <= 0 || height < size*1.4 || size <= 0 {
		return nil
	}
	if !s.initialized || at < s.position || at-s.position > time.Second || width != s.width || height != s.height || size != s.size {
		s.width, s.height, s.size = width, height, size
		s.initialized = true
		s.active = s.active[:0]
		rows := min(12, int(height/(size*1.4)))
		fixed := rows / 4
		s.scroll, s.top, s.bottom = make([]lane, rows-2*fixed), make([]lane, fixed), make([]lane, fixed)
		s.next, _ = slices.BinarySearchFunc(s.comments, max(at-scrollLifetime, 0), func(c Comment, t time.Duration) int {
			if c.Time < t {
				return -1
			}
			if c.Time > t {
				return 1
			}
			return 0
		})
	}
	s.position = at
	s.active = slices.DeleteFunc(s.active, func(c activeComment) bool { return at-c.Time >= lifetime(c.Mode) })
	for s.next < len(s.comments) && s.comments[s.next].Time <= at {
		comment := s.comments[s.next]
		s.next++
		if at-comment.Time >= lifetime(comment.Mode) {
			continue
		}
		s.schedule(comment, measure)
	}
	s.visible = s.visible[:0]
	lineHeight := size * 1.4
	for _, c := range s.active {
		age := float32((at - c.Time).Seconds())
		x := (width - c.width) / 2
		y := float32(c.row) * lineHeight
		switch c.Mode {
		case 1:
			x = width - (width+c.width)*age/float32(scrollLifetime.Seconds())
			y += float32(len(s.top)) * lineHeight
		case 4:
			y = height - float32(c.row+1)*lineHeight
		}
		s.visible = append(s.visible, Sprite{Comment: c.Comment, X: x, Y: y, Width: c.width})
	}
	return s.visible
}

func (s *Screen) schedule(comment Comment, measure func(string) float32) {
	rows := s.scroll
	if comment.Mode == 4 {
		rows = s.bottom
	} else if comment.Mode == 5 {
		rows = s.top
	}
	textWidth := float32(-1)
	for i := range rows {
		previous := rows[i]
		elapsed := float32((comment.Time - previous.at).Seconds())
		if previous.used {
			if comment.Mode != 1 {
				if comment.Time-previous.at < fixedLifetime {
					continue
				}
			} else if elapsed < float32(scrollLifetime.Seconds()) && (s.width+previous.width)*elapsed/8 < previous.width+12 {
				continue
			}
		}
		if textWidth < 0 {
			textWidth = measure(comment.Text)
			if textWidth <= 0 || math.IsNaN(float64(textWidth)) || math.IsInf(float64(textWidth), 0) {
				return
			}
		}
		if comment.Mode == 1 && previous.used && elapsed < 8 {
			// A wider, faster comment must not catch the previous one's tail.
			if s.width-(s.width+textWidth)*(8-elapsed)/8 < 12 {
				continue
			}
		}
		rows[i] = lane{at: comment.Time, width: textWidth, used: true}
		s.active = append(s.active, activeComment{Comment: comment, width: textWidth, row: i})
		return
	}
}

// Next returns the next comment time, or zero when the timeline has ended.
func (s *Screen) Next() time.Duration {
	if s.next < len(s.comments) {
		return s.comments[s.next].Time
	}
	return 0
}

func lifetime(mode int) time.Duration {
	if mode == 1 {
		return scrollLifetime
	}
	return fixedLifetime
}
