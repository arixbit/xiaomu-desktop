package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/egoist/mygo/ui"
)

func TestContinueWatchingDismissalSurvivesRecordingAndRestart(t *testing.T) {
	a := &app{history: historyStore{path: filepath.Join(t.TempDir(), "history.json")}}
	for i := range 8 {
		a.history.note(historyEntry{Name: fmt.Sprintf("示例%d", i), Position: 30, Duration: 600})
	}
	if got := continueWatchingEntries(a.history.entries); len(got) != 6 || got[0].Name != "示例7" || got[5].Name != "示例2" {
		t.Fatalf("recent titles = %+v", got)
	}
	a.setContinueWatchingHidden(titleKey("《示例7》"), true)
	a.history.note(historyEntry{Name: "示例7", Position: 31, Duration: 600})
	a.flushHistory()
	entries, err := readHistory(a.history.path)
	if err != nil || len(entries) != 8 || !entries[0].HomeHidden {
		t.Fatalf("dismissal did not preserve the history across restart: %+v, %v", entries, err)
	}
	b := &app{history: historyStore{entries: entries, path: a.history.path}}
	if got := continueWatchingEntries(b.history.entries); len(got) != 6 || got[0].Name != "示例6" {
		t.Fatalf("dismissed title returned: %+v", got)
	}
	b.revealContinueWatching("示例7")
	entries, err = readHistory(a.history.path)
	if err != nil || entries[0].HomeHidden {
		t.Fatalf("explicit replay did not restore the shortcut: %+v, %v", entries, err)
	}
}

func TestHomeContinueWatchingResumesEpisodeDirectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"list":[{"vod_id":7,"vod_name":"示例片","vod_play_from":"线路","vod_play_url":"第1集$https://example.com/one.mp4#第2集$https://example.com/two.mp4"}]}`)
	}))
	defer srv.Close()
	a := &app{}
	defer a.close()
	a.library.root = &config.Config{URL: "https://example.com/config", Sites: []config.Site{{Key: "site", Type: 1, Kind: config.KindJSON, API: srv.URL}}}
	a.library.cached = map[string]*config.Config{a.library.root.URL: a.library.root}
	a.history.note(historyEntry{Name: "示例片", ConfigURL: a.library.root.URL, SiteKey: "site", VideoID: "7", Line: "线路", Episode: "第2集", Position: 30, Duration: 600})
	opened := 0
	a.open = func(string) (*ui.VideoPlayer, error) {
		opened++
		return ui.NewVideoPlayer(&timedVideo{})
	}
	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("继续观看") {
		t.Fatal("home shortcut missing")
	}
	if err := tt.Click("从继续观看移除 示例片"); err != nil {
		t.Fatal(err)
	}
	if opened != 0 || tt.HasText("继续观看") || len(a.history.entries) != 1 {
		t.Fatal("dismissal played or deleted the title")
	}
	a.revealContinueWatching("示例片")
	tt.Frame()
	if err := tt.Click("继续观看 示例片"); err != nil {
		t.Fatal(err)
	}
	if opened != 1 || a.player == nil || a.playingEpisode != "第2集" || a.playbackPosition().Seconds() < 30 {
		t.Fatalf("wrong resume target: opened=%d episode=%s at=%s status=%s", opened, a.playingEpisode, a.playbackPosition(), a.status)
	}
}

func TestContinueWatchingHiddenWhenEmptyAndDisabledForRemovedSource(t *testing.T) {
	a := &app{}
	a.library.root = &config.Config{URL: "https://example.com/current"}
	tt := ui.NewTester(a.view, 640, 420)
	if tt.HasText("继续观看") {
		t.Fatal("empty history showed a strip")
	}
	a.history.note(historyEntry{Name: "旧片", ConfigURL: "https://example.com/removed", Episode: "第8集"})
	tt.Frame()
	if !tt.HasText("片源已移除") {
		t.Fatal("removed source was not explained")
	}
	_ = tt.Click("继续观看 旧片")
	if a.player != nil || a.running(jobPlay) {
		t.Fatal("removed source was requested")
	}
}

func TestHomeH1SplitFocusSwitcherAndZeroEpisodeFollowing(t *testing.T) {
	a := &app{}
	defer a.close()
	doc := &config.Config{URL: "https://example.com/config"}
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{doc.URL: doc}}

	// Two in-progress titles: 剧乙 is newer (index 0) and followed with new episodes; 剧甲 is unfollowed.
	a.history.note(historyEntry{Name: "剧甲", ConfigURL: doc.URL, Episode: "第3集", Position: 600, Duration: 2400, Origin: "示例站"})
	a.history.note(historyEntry{Name: "剧乙", ConfigURL: doc.URL, Episode: "第4集", Position: 900, Duration: 2700, Origin: "示例站"})

	// Two followed titles: 剧乙 (has new episodes) and 剧丙 (0 episodes watched, unwatched).
	now := time.Now()
	a.favorites.entries = []favoriteEntry{
		{Name: "剧丙", ConfigURL: doc.URL, EpisodeCount: 28, LatestEpisode: "第28集", Checked: now},
		{Name: "剧乙", ConfigURL: doc.URL, EpisodeCount: 16, LatestEpisode: "第16集", Checked: now},
	}

	tt := ui.NewTester(a.view, 1120, 760)
	cwHeading, ok := tt.Find("继续观看")
	if !ok {
		t.Fatalf("missing continue watching heading: %v", tt.Texts())
	}
	favHeading, ok := tt.Find("我的追剧")
	if !ok {
		t.Fatalf("missing favorites heading: %v", tt.Texts())
	}
	// Wide window uses H1 split: favorites sits to the right of continue watching on the exact same header row.
	if favHeading.X <= cwHeading.X+cwHeading.W || favHeading.Y-cwHeading.Y > 0.5 || cwHeading.Y-favHeading.Y > 0.5 {
		t.Fatalf("wide home split headers misaligned: cw=%+v fav=%+v", cwHeading, favHeading)
	}
	cwPoster, ok := tt.Find("继续观看 剧乙")
	if !ok {
		t.Fatal("continue watching spotlight poster missing")
	}
	// Left spotlight card (14px pad + 1px border around 162px poster = 192px) must match
	// the two stacked 92px right cards + 8px gap = 192px at both top and bottom.
	leftTop := cwPoster.Y - 15
	leftBottom := cwPoster.Y + cwPoster.H + 15
	if leftBottom-leftTop != 192 {
		t.Fatalf("unexpected spotlight card height: %v", leftBottom-leftTop)
	}
	if !tt.HasText("1/2") || !tt.HasText("已追 · 有新集") || !tt.HasText("其他在看：") {
		t.Fatalf("focus card missing switcher/badge/other-watching: %v", tt.Texts())
	}
	// Unwatched followed title shows 未开始 without fabricating a progress bar.
	if !tt.HasText("未开始") {
		t.Fatalf("zero-episode followed title missing 未开始 badge: %v", tt.Texts())
	}
	if _, hasFakeProgress := tt.Find("观看进度 剧丙"); hasFakeProgress {
		t.Fatal("unwatched followed title must not fabricate a progress bar")
	}
	// Switcher cycles to the second continue-watching title (unfollowed, so no 已追 · 有新集).
	if err := tt.Click("下一部继续观看"); err != nil {
		t.Fatalf("click next switcher: %v", err)
	}
	if a.homeResumeIdx != 1 || !tt.HasText("2/2") || tt.HasText("已追 · 有新集") {
		t.Fatalf("switcher did not advance cleanly: idx=%d texts=%v", a.homeResumeIdx, tt.Texts())
	}
	// Clicking the quick-switch item in 其他在看 jumps straight back to 剧乙.
	if err := tt.Click("切换继续观看 剧乙"); err != nil {
		t.Fatalf("click other watching pick: %v", err)
	}
	if a.homeResumeIdx != 0 || !tt.HasText("已追 · 有新集") {
		t.Fatalf("quick pick did not return to 剧乙: idx=%d texts=%v", a.homeResumeIdx, tt.Texts())
	}

	// Narrow window stacks continue watching above favorites instead of cramping side-by-side.
	tt.SetSize(640, 480)
	cwNarrow, _ := tt.Find("继续观看")
	favNarrow, _ := tt.Find("我的追剧")
	if favNarrow.Y <= cwNarrow.Y+cwNarrow.H {
		t.Fatalf("narrow home should stack sections vertically: cw=%+v fav=%+v", cwNarrow, favNarrow)
	}

	// When continue watching is empty, home renders favorites without an empty continue-watching placeholder.
	a.history.entries = nil
	tt.SetSize(1120, 760)
	if tt.HasText("继续观看") || !tt.HasText("我的追剧") {
		t.Fatalf("empty continue watching should hide completely when only favorites exist: %v", tt.Texts())
	}
}

func TestContinueWatchingAmbientLayerStaysBehindCTA(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 8; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 180, G: 90, B: 60, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	a.history.note(historyEntry{
		Name:      "海报实景测试",
		Pic:       srv.URL + "/poster.png",
		ConfigURL: doc.URL,
		SiteKey:   doc.Sites[0].Key,
		Episode:   "第01集",
		Position:  120,
		Duration:  2700,
		Origin:    "测试站",
	})
	a.favorites.entries = []favoriteEntry{{Name: "右侧追剧样本", Pic: srv.URL + "/poster.png", EpisodeCount: 12, LatestEpisode: "第12集"}}

	tt := ui.NewTester(a.view, 1120, 760)
	posterLoaded := false
	var lastFrame image.Image
	var lastBtn ui.Rect
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		tt.Advance(25 * time.Millisecond)
		posterRect, okP := tt.Find("继续观看 海报实景测试")
		btn, okB := tt.Find("续播 海报实景测试")
		if okP && okB {
			frame := tt.Image()
			lastFrame, lastBtn = frame, btn
			pr, _, _, _ := frame.At(int(posterRect.X+posterRect.W/2), int(posterRect.Y+posterRect.H/2)).RGBA()
			if uint8(pr>>8) >= 140 {
				posterLoaded = true
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !posterLoaded {
		t.Fatal("loopback poster image did not finish loading into continue watching card")
	}
	// Resting CTA is calm neutral (not red); hovering lights it up to vivid coral,
	// proving both resting restraint and that the poster ambient layer stays behind (ZIndex -1).
	restR, restG, restB, _ := lastFrame.At(int(lastBtn.X+6), int(lastBtn.Y+6)).RGBA()
	if uint8(restR>>8) > 120 {
		t.Fatalf("resting continue watching CTA should be calm neutral, not red: rgb=(%d,%d,%d)", uint8(restR>>8), uint8(restG>>8), uint8(restB>>8))
	}
	tt.Move(lastBtn.X+lastBtn.W/2, lastBtn.Y+lastBtn.H/2)
	tt.Advance(250 * time.Millisecond)
	hoverFrame := tt.Image()
	r, g, b, _ := hoverFrame.At(int(lastBtn.X+6), int(lastBtn.Y+6)).RGBA()
	r8, g8, b8 := uint8(r>>8), uint8(g>>8), uint8(b>>8)
	if r8 < 210 || g8 > 130 || b8 > 110 {
		t.Fatalf("hovered continue watching coral CTA dimmed/covered by ambient veil: rgb=(%d,%d,%d) rect=%+v", r8, g8, b8, lastBtn)
	}
	if dir := os.Getenv("XIAOMU_UX_VERIFY_DIR"); dir != "" {
		if f, err := os.Create(filepath.Join(dir, "home-real-poster.png")); err == nil {
			_ = png.Encode(f, lastFrame)
			_ = f.Close()
		}
	}
}
