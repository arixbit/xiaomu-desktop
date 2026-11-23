package danmaku

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseBlockedTerms(t *testing.T) {
	got := ParseBlockedTerms(" 剧透，广告, SPOILER；剧透\nTest ")
	want := []string{"剧透", "广告", "spoiler", "test"}
	if len(got) != len(want) {
		t.Fatalf("terms = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("terms[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if terms := ParseBlockedTerms(" , ， ; "); len(terms) != 0 {
		t.Fatalf("expected empty terms, got %v", terms)
	}
	long := strings.Repeat("字", 80)
	if terms := ParseBlockedTerms(long); len(terms) != 1 || len([]rune(terms[0])) != 50 {
		t.Fatalf("long term was not bounded: %v", terms)
	}
}

func TestFilterTypesColorKeywordsAndMergeRepeat(t *testing.T) {
	source := []Comment{
		{Time: 0, Mode: 1, Color: 0xffffff, Text: "前方高能"},
		{Time: 1 * time.Second, Mode: 1, Color: 0xffffff, Text: "前方高能"},
		{Time: 2 * time.Second, Mode: 1, Color: 0xffffff, Text: " 前方高能 "},
		{Time: 4 * time.Second, Mode: 1, Color: 0xffffff, Text: "前方高能"},
		{Time: 5 * time.Second, Mode: 5, Color: 0xffffff, Text: "顶部评论"},
		{Time: 6 * time.Second, Mode: 4, Color: 0xffffff, Text: "底部评论"},
		{Time: 7 * time.Second, Mode: 1, Color: 0xff0000, Text: "红色弹幕"},
		{Time: 8 * time.Second, Mode: 1, Color: 0xffffff, Text: "这里有剧透内容"},
	}
	got := Filter(source, FilterOptions{
		HideTop:     true,
		HideBottom:  true,
		HideColor:   true,
		MergeRepeat: true,
		Blocked:     []string{"剧透"},
	})
	if len(got) != 2 {
		t.Fatalf("filtered = %+v, want 2 items", got)
	}
	if got[0].Text != "前方高能 ×3" || got[0].Time != 0 {
		t.Fatalf("first merged item = %+v", got[0])
	}
	if got[1].Text != "前方高能" || got[1].Time != 4*time.Second {
		t.Fatalf("second window item = %+v", got[1])
	}
	// Original slice must not be mutated.
	if source[0].Text != "前方高能" {
		t.Fatalf("source was mutated: %+v", source[0])
	}
}

func TestDensityProfile(t *testing.T) {
	comments := []Comment{
		{Time: 1 * time.Second, Mode: 1, Text: "a"},
		{Time: 50 * time.Second, Mode: 1, Text: "b"},
		{Time: 51 * time.Second, Mode: 1, Text: "c"},
		{Time: 52 * time.Second, Mode: 1, Text: "d"},
		{Time: 53 * time.Second, Mode: 1, Text: "e"},
		{Time: 120 * time.Second, Mode: 1, Text: "out-of-range"},
	}
	profile := DensityProfile(comments, 100*time.Second, 10)
	if len(profile) != 10 {
		t.Fatalf("profile len = %d, want 10", len(profile))
	}
	if math.Abs(float64(profile[5]-1)) > 0.001 {
		t.Fatalf("peak bucket 5 = %v, want 1.0 (profile=%v)", profile[5], profile)
	}
	if profile[0] <= 0 || profile[0] >= profile[5] {
		t.Fatalf("unexpected non-peak bucket 0: %v", profile)
	}
	if profile[9] != 0 {
		t.Fatalf("out-of-range comment leaked into bucket 9: %v", profile)
	}
	if got := DensityProfile(comments, 0, 10); got != nil {
		t.Fatalf("zero duration should return nil: %v", got)
	}
}
