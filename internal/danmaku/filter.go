package danmaku

import (
	"fmt"
	"strings"
	"time"
)

const repeatMergeWindow = 3 * time.Second
const maxBlockedTerms = 100

// FilterOptions controls comment visibility and spam reduction.
type FilterOptions struct {
	HideTop     bool
	HideBottom  bool
	HideColor   bool
	MergeRepeat bool
	Blocked     []string
}

// ParseBlockedTerms splits user-entered keywords by commas, semicolons, or
// whitespace, lowercasing, deduplicating, and bounding them.
func ParseBlockedTerms(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	if len(fields) == 0 {
		return nil
	}
	terms := make([]string, 0, min(len(fields), maxBlockedTerms))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		term := strings.ToLower(strings.TrimSpace(field))
		if term == "" {
			continue
		}
		if runes := []rune(term); len(runes) > 50 {
			term = string(runes[:50])
		}
		if _, dup := seen[term]; dup {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
		if len(terms) == maxBlockedTerms {
			break
		}
	}
	return terms
}

// Filter applies type/color/keyword filters and optional short-window duplicate
// merging to a time-sorted slice of comments without mutating the input.
func Filter(comments []Comment, opt FilterOptions) []Comment {
	if len(comments) == 0 {
		return nil
	}
	blocked := make([]string, 0, len(opt.Blocked))
	for _, term := range opt.Blocked {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" {
			blocked = append(blocked, term)
		}
	}
	if !opt.HideTop && !opt.HideBottom && !opt.HideColor && !opt.MergeRepeat && len(blocked) == 0 {
		return comments
	}
	out := make([]Comment, 0, len(comments))
	var recent map[string]int
	var counts []int
	if opt.MergeRepeat {
		recent = make(map[string]int, min(len(comments), 256))
		counts = make([]int, 0, len(comments))
	}
	for _, c := range comments {
		if opt.HideTop && c.Mode == 5 || opt.HideBottom && c.Mode == 4 || opt.HideColor && c.Color != 0xffffff {
			continue
		}
		if len(blocked) > 0 && containsBlocked(c.Text, blocked) {
			continue
		}
		if opt.MergeRepeat {
			key := strings.ToLower(strings.TrimSpace(c.Text))
			if idx, ok := recent[key]; ok && c.Time-out[idx].Time <= repeatMergeWindow && c.Time >= out[idx].Time {
				counts[idx]++
				continue
			}
			recent[key] = len(out)
			counts = append(counts, 1)
		}
		out = append(out, c)
	}
	if opt.MergeRepeat {
		for i, n := range counts {
			if n > 1 {
				out[i].Text = fmt.Sprintf("%s ×%d", out[i].Text, n)
			}
		}
	}
	return out
}

func containsBlocked(text string, blocked []string) bool {
	lower := strings.ToLower(text)
	for _, term := range blocked {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

// DensityProfile buckets comments across [0, duration] into normalized [0, 1]
// heights for a highlight curve above the progress bar.
func DensityProfile(comments []Comment, duration time.Duration, buckets int) []float32 {
	if duration <= 0 || buckets <= 0 || len(comments) == 0 {
		return nil
	}
	counts := make([]int, buckets)
	total := 0
	for _, c := range comments {
		if c.Time < 0 || c.Time > duration {
			continue
		}
		i := int(float64(c.Time) / float64(duration) * float64(buckets))
		if i >= buckets {
			i = buckets - 1
		}
		counts[i]++
		total++
	}
	if total == 0 {
		return nil
	}
	smoothed := make([]float32, buckets)
	var peak float32
	for i := range buckets {
		cur := float32(counts[i])
		prev, next := cur, cur
		if i > 0 {
			prev = float32(counts[i-1])
		}
		if i+1 < buckets {
			next = float32(counts[i+1])
		}
		v := 0.2*prev + 0.6*cur + 0.2*next
		smoothed[i] = v
		if v > peak {
			peak = v
		}
	}
	if peak <= 0 {
		return nil
	}
	for i := range smoothed {
		smoothed[i] /= peak
	}
	return smoothed
}
