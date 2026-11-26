package main

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

func TestMarqueeOffsetPingPong(t *testing.T) {
	if off, anim, wait := marqueeOffset(time.Second, 0.4); off != 0 || anim || wait != 0 {
		t.Fatalf("small overflow should stay static: off=%.2f anim=%v wait=%v", off, anim, wait)
	}

	const overflow = float32(92)
	travel := overflow + marqueeTailMargin
	leg := time.Duration(float64(travel) / float64(marqueeSpeedPxSec) * float64(time.Second))

	off, anim, wait := marqueeOffset(0, overflow)
	if off != 0 || anim || wait != marqueeLeadPause {
		t.Fatalf("lead pause at 0: got off=%.2f anim=%v wait=%v", off, anim, wait)
	}

	offMid, animMid, _ := marqueeOffset(marqueeLeadPause+leg/2, overflow)
	if !animMid || offMid <= travel*0.4 || offMid >= travel*0.6 {
		t.Fatalf("mid forward leg: got off=%.2f anim=%v travel=%.2f", offMid, animMid, travel)
	}

	offEnd, animEnd, waitEnd := marqueeOffset(marqueeLeadPause+leg+100*time.Millisecond, overflow)
	if animEnd || offEnd != travel || waitEnd <= 0 {
		t.Fatalf("end pause: got off=%.2f anim=%v wait=%v travel=%.2f", offEnd, animEnd, waitEnd, travel)
	}

	offBack, animBack, _ := marqueeOffset(marqueeLeadPause+leg+marqueeEndPause+leg/2, overflow)
	if !animBack || offBack <= travel*0.4 || offBack >= travel*0.6 {
		t.Fatalf("mid return leg: got off=%.2f anim=%v travel=%.2f", offBack, animBack, travel)
	}
	offBackLate, animBackLate, _ := marqueeOffset(marqueeLeadPause+leg+marqueeEndPause+leg*3/4, overflow)
	if !animBackLate || offBackLate >= offBack {
		t.Fatalf("return leg must move right back toward 0: mid=%.2f late=%.2f", offBack, offBackLate)
	}

	offStartPause, animStartPause, waitStartPause := marqueeOffset(marqueeLeadPause+2*leg+marqueeEndPause+100*time.Millisecond, overflow)
	if animStartPause || offStartPause != 0 || waitStartPause <= 0 {
		t.Fatalf("start pause: got off=%.2f anim=%v wait=%v", offStartPause, animStartPause, waitStartPause)
	}
}

func regionDiffers(a, b *image.RGBA, r ui.Rect) bool {
	if a == nil || b == nil {
		return false
	}
	ab, bb := a.Bounds(), b.Bounds()
	x0 := max(ab.Min.X, int(r.X)+2)
	y0 := max(ab.Min.Y, int(r.Y)+2)
	x1 := min(ab.Max.X, int(r.X+r.W)-2)
	y1 := min(ab.Max.Y, int(r.Y+r.H)-2)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if x >= bb.Min.X && x < bb.Max.X && y >= bb.Min.Y && y < bb.Max.Y {
				if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
					return true
				}
			}
		}
	}
	return false
}

func saveTestPNG(t *testing.T, path string, img *image.RGBA) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFloatingPanelHoverMarqueeScrollsLongEpisodeWithoutClicking(t *testing.T) {
	a, _ := newFloatingPanelFixture(t, 14, 3, 10)
	longSourceNames := []string{
		"示例线路 · 影视 | 超清直连线路超长名称展示测试",
		"示例来源 B · 备用超清无广高速直连测试线路",
		"示例来源 C · 极速直连线路",
	}
	doc := &config.Config{URL: "https://example.com/config.json"}
	// 11–14 rune variety-show episode names produce the 2-column grid where
	// shorter labels ("先导片") fit cleanly across OS fallback fonts and longer
	// labels overflow.
	episodes := []catalog.Episode{
		{Name: "20261027陪录1期完整", ID: "https://example.com/v1.mp4"},
		{Name: "20261105候场花絮未播", ID: "https://example.com/v2.mp4"},
		{Name: "20261105陪录1期加更", ID: "https://example.com/v3.mp4"},
		{Name: "20261105候场区采访集", ID: "https://example.com/v4.mp4"},
		{Name: "20261118陪录1期连麦", ID: "https://example.com/v5.mp4"},
		{Name: "20261118候场区游戏篇", ID: "https://example.com/v6.mp4"},
		{Name: "20261118候场区1", ID: "https://example.com/v7.mp4"},
		{Name: "先导片", ID: "https://example.com/v8.mp4"},
		{Name: "20261120初见特别企划", ID: "https://example.com/v9.mp4"},
		{Name: "20261120第1期上完整", ID: "https://example.com/v10.mp4"},
		{Name: "20261120第1期下加更", ID: "https://example.com/v11.mp4"},
		{Name: "20261121加更幕后专访", ID: "https://example.com/v12.mp4"},
		{Name: "20261122特别企划实录", ID: "https://example.com/v13.mp4"},
		{Name: "20261126超前营业看点", ID: "https://example.com/v14.mp4"},
	}
	if got := episodeGridColumns(episodes); got != 2 {
		t.Fatalf("episodeGridColumns = %d, want 2", got)
	}
	seeds := make([]library.Item, len(longSourceNames))
	for i, name := range longSourceNames {
		site := config.Site{Key: fmt.Sprintf("long-src-%d", i), Name: name, Type: 1, Kind: config.KindJSON, API: "https://example.com/api"}
		doc.Sites = append(doc.Sites, site)
		seeds[i] = library.Item{
			Entry: library.Entry{Config: doc, Site: site},
			Video: catalog.Video{ID: catalog.ID(fmt.Sprintf("show-%d", i)), Name: "示例综艺节目"},
		}
	}
	playedCh := make(chan string, 4)
	a.title = &titlePage{
		name:   "示例综艺节目",
		item:   seeds[0],
		seeds:  seeds,
		failed: map[string]string{},
		session: &recoverySession{play: func(_ context.Context, line string, ep catalog.Episode) (catalog.Playback, error) {
			playedCh <- line + "/" + ep.Name
			return catalog.Playback{URL: ep.ID}, nil
		}},
		detail: &catalog.Detail{
			Name:    "示例综艺节目",
			Sources: []catalog.Source{{Name: "直连线路", Episodes: episodes}},
		},
	}
	a.playingSource = "直连线路"
	a.playingEpisode = episodes[9].Name
	a.panelOpen = true
	a.panelSource = 0

	tt := ui.NewTester(a.view, 1470, 1115)
	tt.SetTitleBar(dragBar)
	tt.Frame()

	// Short label ("先导片") fits completely and must not scroll on hover.
	shortRect, ok := tt.Find("先导片")
	if !ok || shortRect.W <= 0 {
		t.Fatalf("expected visible short episode button, got %+v ok=%v", shortRect, ok)
	}
	tt.Move(shortRect.X+shortRect.W/2, shortRect.Y+shortRect.H/2)
	tt.Advance(1200 * time.Millisecond)
	if a.marquee.lastOffset != 0 {
		t.Fatalf("short episode should not scroll on hover: offset=%.2f", a.marquee.lastOffset)
	}
	if len(playedCh) != 0 {
		t.Fatalf("hover must not click episode, got %q", <-playedCh)
	}

	// Long overflowing label must start scrolling on hover without clicking.
	longLabel := episodes[0].Name
	longRect, ok := tt.Find(longLabel)
	if !ok || longRect.W <= 0 {
		t.Fatalf("expected visible long episode button %q, got %+v ok=%v", longLabel, longRect, ok)
	}
	tt.Move(longRect.X+longRect.W/2, longRect.Y+longRect.H/2)
	imgStart := tt.Image()
	if a.marquee.lastOffset != 0 {
		t.Fatalf("expected initial lead-in pause at offset 0, got %.2f", a.marquee.lastOffset)
	}

	tt.Advance(700 * time.Millisecond)
	midOffset := a.marquee.lastOffset
	overflow := a.marquee.lastOver
	if overflow <= 5 {
		t.Fatalf("expected %q to overflow button width %.2f, got overflow=%.2f", longLabel, longRect.W, overflow)
	}
	if midOffset <= 5 {
		t.Fatalf("expected hovered long episode to scroll left after 700ms, got offset=%.2f (overflow=%.2f)", midOffset, overflow)
	}
	imgMid := tt.Image()
	if !regionDiffers(imgStart, imgMid, longRect) {
		t.Fatal("expected rendered pixels inside hovered long episode button to move while scrolling")
	}
	if dir := os.Getenv("XIAOMU_NATIVE_REVIEW_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		saveTestPNG(t, filepath.Join(dir, "native-1470x1115-long-episodes-hover.png"), imgMid)
	}

	// Button geometry must stay fixed while scrolling.
	afterRect, ok := tt.Find(longLabel)
	if !ok || afterRect != longRect {
		t.Fatalf("button geometry shifted while scrolling: before=%+v after=%+v", longRect, afterRect)
	}

	// Advance to the end of the forward leg and verify it reaches the right tail,
	// then reverses direction back toward 0.
	travel := overflow + marqueeTailMargin
	leg := time.Duration(float64(travel) / float64(marqueeSpeedPxSec) * float64(time.Second))
	tt.Advance(leg)
	if a.marquee.lastOffset < travel-1 {
		t.Fatalf("expected marquee to reach end travel %.2f, got %.2f", travel, a.marquee.lastOffset)
	}
	tt.Advance(marqueeEndPause + leg/2)
	if a.marquee.lastOffset >= travel-2 {
		t.Fatalf("expected marquee to scroll back toward start after end pause, got %.2f (travel=%.2f)", a.marquee.lastOffset, travel)
	}
	if len(playedCh) != 0 {
		t.Fatalf("hover scrolling must not trigger playback click, got %q", <-playedCh)
	}

	// Leaving the button stops scrolling and clears state immediately.
	tt.Move(4, 4)
	if a.marquee.key != "" || a.marquee.lastOffset != 0 {
		t.Fatalf("expected marquee to reset after pointer leaves, got key=%q offset=%.2f", a.marquee.key, a.marquee.lastOffset)
	}

	// Overflowing source selector trigger also scrolls on hover without opening the menu.
	srcRect, ok := tt.Find("选择来源")
	if !ok || srcRect.W <= 0 {
		t.Fatalf("expected visible source selector trigger, got %+v ok=%v", srcRect, ok)
	}
	tt.Move(srcRect.X+srcRect.W/2, srcRect.Y+srcRect.H/2)
	tt.Advance(700 * time.Millisecond)
	if a.marquee.lastOffset <= 5 {
		t.Fatalf("expected overflowing source selector to scroll on hover, got offset=%.2f (overflow=%.2f)", a.marquee.lastOffset, a.marquee.lastOver)
	}
	if a.playerPopovers.source {
		t.Fatal("hovering source selector must not open dropdown menu")
	}

	// Clicking the overflowing episode still selects and plays it cleanly.
	tt.ClickAt(longRect.X+longRect.W/2, longRect.Y+longRect.H/2)
	select {
	case got := <-playedCh:
		if got != "直连线路/"+longLabel {
			t.Fatalf("expected clicking %q to play it, got %q", longLabel, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for click on %q to trigger playback", longLabel)
	}
}

func TestFloatingPanelHoverMarqueeSingleColumnUltraLongTitle(t *testing.T) {
	a, _ := newFloatingPanelFixture(t, 4, 1, 1)
	ultraLong := "20261120第1期上：超长副标题幕后花絮完整未播加更特别企划版"
	episodes := []catalog.Episode{
		{Name: ultraLong, ID: "https://example.com/u1.mp4"},
		{Name: "20261120第1期下：巅峰舞台对决完整实录加更版", ID: "https://example.com/u2.mp4"},
	}
	if got := episodeGridColumns(episodes); got != 1 {
		t.Fatalf("episodeGridColumns = %d, want 1", got)
	}
	a.title.detail = &catalog.Detail{
		Name:    "示例超长综艺",
		Sources: []catalog.Source{{Name: "直连", Episodes: episodes}},
	}
	a.playingSource = "直连"
	a.playingEpisode = ultraLong
	a.panelOpen = true
	a.panelSource = 0

	tt := ui.NewTester(a.view, 1280, 720)
	tt.SetTitleBar(dragBar)
	tt.Frame()

	r, ok := tt.Find(ultraLong)
	if !ok || r.W <= 0 {
		t.Fatalf("expected single-column long episode button, got %+v ok=%v", r, ok)
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(900 * time.Millisecond)
	if a.marquee.lastOver <= 20 || a.marquee.lastOffset <= 10 {
		t.Fatalf("expected ultra-long 1-column episode to scroll on hover: over=%.2f offset=%.2f", a.marquee.lastOver, a.marquee.lastOffset)
	}
}

func TestLocalPlaylistUsesUnifiedFloatingStageAndHoverMarquee(t *testing.T) {
	dir := t.TempDir()
	files := make([]string, 16)
	for i := range files {
		name := fmt.Sprintf("[DEMO][Example.Show.Season1][E%03d(1080P)][KO_CN_SUB].mp4", i+1)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
		files[i] = p
	}

	const dur = 82*time.Minute + 11*time.Second
	a := &app{
		autoNext:    true,
		preferences: defaultPlayerPreferences(),
		openLocal: func(string) (*ui.VideoPlayer, error) {
			vp, err := ui.NewVideoPlayer(newCinema16x9Video(1280, 720, dur))
			if err != nil {
				return nil, err
			}
			vp.Pause()
			vp.Seek(3*time.Minute + 48*time.Second)
			return vp, nil
		},
	}
	t.Cleanup(a.close)

	if !a.openLocalFile(files[11]) {
		t.Fatalf("open local ep 12 failed: %s", a.status)
	}
	if a.localPlaylistIdx != 11 {
		t.Fatalf("localPlaylistIdx = %d, want 11", a.localPlaylistIdx)
	}

	tt := ui.NewTester(a.view, 1470, 1115)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if a.panelOpen {
		t.Fatal("local playlist panel should start collapsed by default")
	}
	if err := tt.Click("列表"); err != nil {
		t.Fatalf("open local playlist via 列表: %v", err)
	}
	tt.Advance(350 * time.Millisecond)

	video, okV := tt.Find("视频画面")
	panel, okP := tt.Find("悬浮列表面板")
	if !okV || !okP {
		t.Fatalf("missing unified floating stage elements: video=%v panel=%v", okV, okP)
	}
	vw, vh := a.player.Size()
	pic := playerPictureRect(video, vw, vh)
	if dTop, dBot := absf(panel.Y-pic.Y), absf((panel.Y+panel.H)-(pic.Y+pic.H)); dTop > 1.0 || dBot > 1.0 {
		t.Fatalf("local playlist floating panel misaligned: Δtop=%.2f Δbottom=%.2f (pic=%+v panel=%+v)", dTop, dBot, pic, panel)
	}
	t.Logf("local 1470x1115: pic=(%.2f,%.2f %.2fx%.2f) panel=(%.2f,%.2f %.2fx%.2f)",
		pic.X, pic.Y, pic.W, pic.H, panel.X, panel.Y, panel.W, panel.H)

	// Hover the first long local filename row without clicking.
	firstLabel := "播放 " + filepath.Base(files[0])
	rowRect, ok := tt.Find(firstLabel)
	if !ok || rowRect.H <= 0 {
		t.Fatalf("missing visible local row %q: %+v", firstLabel, rowRect)
	}
	tt.Move(rowRect.X+rowRect.W/2, rowRect.Y+rowRect.H/2)
	imgStart := tt.Image()
	tt.Advance(700 * time.Millisecond)
	if a.marquee.lastOver <= 10 || a.marquee.lastOffset <= 5 {
		t.Fatalf("expected long local filename to hover-scroll: over=%.2f offset=%.2f", a.marquee.lastOver, a.marquee.lastOffset)
	}
	imgMid := tt.Image()
	if !regionDiffers(imgStart, imgMid, rowRect) {
		t.Fatal("expected pixels inside hovered local playlist row to scroll")
	}
	if a.localPlaylistIdx != 11 {
		t.Fatalf("hover must not switch local file, idx=%d", a.localPlaylistIdx)
	}
	if dir := os.Getenv("XIAOMU_NATIVE_REVIEW_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		saveTestPNG(t, filepath.Join(dir, "native-1470x1115-local-playlist-hover.png"), imgMid)
	}

	// Pointer leaves -> marquee resets cleanly.
	tt.Move(4, 4)
	if a.marquee.key != "" || a.marquee.lastOffset != 0 {
		t.Fatalf("expected marquee reset when leaving local row: key=%q offset=%.2f", a.marquee.key, a.marquee.lastOffset)
	}

	// Clicking the row still switches local file cleanly.
	tt.ClickAt(rowRect.X+rowRect.W/2, rowRect.Y+rowRect.H/2)
	if a.localPlaylistIdx != 0 {
		t.Fatalf("clicking local row did not switch to index 0, got %d", a.localPlaylistIdx)
	}
}

func TestLiveChannelsUsesUnifiedFloatingStageAndHoverMarquee(t *testing.T) {
	a := &app{
		nav:         []routeKind{routeLive},
		preferences: defaultPlayerPreferences(),
	}
	a.live.channels = []iptv.Channel{
		{Name: "示例超清体育赛事频道 4K HDR 超长测试名称一", Streams: []iptv.Stream{{URL: "https://example.com/c1.m3u8"}}},
		{Name: "示例综合频道二", Streams: []iptv.Stream{{URL: "https://example.com/c2.m3u8"}}},
	}
	a.open = func(string) (*ui.VideoPlayer, error) {
		vp, err := ui.NewVideoPlayer(newCinema16x9Video(1280, 720, 0))
		if err != nil {
			return nil, err
		}
		vp.Pause()
		return vp, nil
	}
	t.Cleanup(a.close)

	a.playLiveChannel(0, 0)
	tt := ui.NewTester(a.view, 1280, 720)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if a.panelOpen {
		t.Fatal("live channels panel should start collapsed by default")
	}
	if err := tt.Click("频道"); err != nil {
		t.Fatalf("open live channels via 频道: %v", err)
	}
	tt.Advance(350 * time.Millisecond)

	video, okV := tt.Find("视频画面")
	panel, okP := tt.Find("悬浮频道面板")
	back, _ := tt.Find("返回")
	dock, _ := tt.Find("播放控制条")
	if !okV || !okP {
		t.Fatalf("missing unified live floating stage elements: video=%v panel=%v", okV, okP)
	}
	if video.W != 1280 || video.H != 720 {
		t.Fatalf("live video did not fill window: %+v", video)
	}
	if panel.Y < back.Y+back.H || panel.Y+panel.H > dock.Y {
		t.Fatalf("live channel floating panel overlapped header/dock: panel=%+v header=%+v dock=%+v", panel, back, dock)
	}

	chRect, ok := tt.Find("切换到 " + a.live.channels[0].Name)
	if !ok || chRect.H <= 0 {
		t.Fatalf("missing live channel row: %+v", chRect)
	}
	tt.Move(chRect.X+chRect.W/2, chRect.Y+chRect.H/2)
	tt.Advance(700 * time.Millisecond)
	if a.marquee.lastOver <= 10 || a.marquee.lastOffset <= 5 {
		t.Fatalf("expected long live channel name to hover-scroll: over=%.2f offset=%.2f", a.marquee.lastOver, a.marquee.lastOffset)
	}
}
