package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

type cinema16x9Video struct {
	w, h     int
	dur      time.Duration
	frame    *image.RGBA
	sent     bool
}

var (
	demoCinemaOnce  sync.Once
	demoCinemaImage *image.RGBA
)

func newCinema16x9Video(w, h int, dur time.Duration) *cinema16x9Video {
	// Synthesize a high-contrast cinema-toned 16:9 still frame locally so edge
	// alignment and glass translucency are easy to inspect in screenshots.
	demoCinemaOnce.Do(func() {
		fw, fh := 1280, 720
		img := image.NewRGBA(image.Rect(0, 0, fw, fh))
		for y := range fh {
			ny := float64(y) / float64(max(fh-1, 1))
			for x := range fw {
				nx := float64(x) / float64(max(fw-1, 1))
				dx1, dy1 := nx-0.55, ny-0.42
				key := math.Exp(-(dx1*dx1*4.5 + dy1*dy1*5.0))
				dx2, dy2 := nx-0.22, ny-0.45
				windowGlow := math.Exp(-(dx2*dx2*14.0 + dy2*dy2*8.0))
				vig := 1.0 - 0.35*(math.Pow(nx-0.5, 2)+math.Pow(ny-0.5, 2))*2.6
				r := (36 + 150*key + 95*windowGlow - 18*ny) * vig
				g := (44 + 108*key + 86*windowGlow - 14*ny) * vig
				b := (56 + 74*key + 80*windowGlow + 20*(1-nx)) * vig
				clamp8 := func(v float64) uint8 {
					if v < 0 {
						return 0
					}
					if v > 255 {
						return 255
					}
					return uint8(v)
				}
				img.SetRGBA(x, y, color.RGBA{R: clamp8(r), G: clamp8(g), B: clamp8(b), A: 255})
			}
		}
		demoCinemaImage = img
	})
	return &cinema16x9Video{w: demoCinemaImage.Bounds().Dx(), h: demoCinemaImage.Bounds().Dy(), dur: dur, frame: demoCinemaImage}
}

func (v *cinema16x9Video) Next() (ui.VideoFrame, error) {
	if v.sent {
		return ui.VideoFrame{}, io.EOF
	}
	v.sent = true
	return ui.VideoFrame{Image: v.frame, Duration: v.dur}, nil
}

func (v *cinema16x9Video) Rewind() error         { v.sent = false; return nil }
func (v *cinema16x9Video) Duration() time.Duration { return v.dur }

func makeFixtureEpisodes(count int) []catalog.Episode {
	eps := make([]catalog.Episode, count)
	for i := range count {
		eps[i] = catalog.Episode{
			Name: fmt.Sprintf("第%d集", i+1),
			ID:   fmt.Sprintf("https://example.com/ep-%02d.mp4", i+1),
		}
	}
	return eps
}

func newFloatingPanelFixture(t *testing.T, epCount, sourceCount, activeEp int) (*app, *[]string) {
	t.Helper()
	const dur = 46*time.Minute + 17*time.Second
	vp, err := ui.NewVideoPlayer(newCinema16x9Video(640, 360, dur))
	if err != nil {
		t.Fatal(err)
	}
	p := wrapSystemPlayer(vp)
	p.Pause()
	p.Seek(26*time.Minute + 49*time.Second)

	doc := &config.Config{URL: "https://example.com/config.json"}
	seeds := make([]library.Item, sourceCount)
	for i := range sourceCount {
		name := fmt.Sprintf("示例来源 %c", 'A'+i)
		site := config.Site{Key: fmt.Sprintf("src-%d", i), Name: name, Type: 1, Kind: config.KindJSON, API: "https://example.com/api"}
		doc.Sites = append(doc.Sites, site)
		seeds[i] = library.Item{
			Entry: library.Entry{Config: doc, Site: site},
			Video: catalog.Video{ID: catalog.ID(fmt.Sprintf("v-%d", i)), Name: "示例古装剧"},
		}
	}

	eps := makeFixtureEpisodes(epCount)
	if activeEp < 1 || activeEp > epCount {
		activeEp = 1
	}
	var played []string
	a := &app{
		player:         p,
		autoNext:       true,
		playingSource:  "hnm3u8",
		playingEpisode: fmt.Sprintf("第%d集", activeEp),
		preferences:    defaultPlayerPreferences(),
		seriesSkips:    seriesSkipStore{path: filepath.Join(t.TempDir(), "series-skips.json")},
		panelSource:    1,
		panelSkipOpen:  true,
	}
	a.open = func(string) (*ui.VideoPlayer, error) {
		nv, err := ui.NewVideoPlayer(newCinema16x9Video(640, 360, dur))
		if err != nil {
			return nil, err
		}
		nv.Pause()
		return nv, nil
	}
	a.title = &titlePage{
		name:   "示例古装剧",
		item:   seeds[0],
		seeds:  seeds,
		failed: map[string]string{},
		session: &recoverySession{play: func(_ context.Context, line string, ep catalog.Episode) (catalog.Playback, error) {
			played = append(played, line+"/"+ep.Name)
			return catalog.Playback{URL: ep.ID}, nil
		}},
		detail: &catalog.Detail{
			Name: "示例古装剧",
			Sources: []catalog.Source{
				{Name: "hnyun", Episodes: eps},
				{Name: "hnm3u8", Episodes: eps},
			},
		},
	}
	t.Cleanup(a.close)
	return a, &played
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func TestFloatingEpisodePanelGeometryAtRequiredWindowSizes(t *testing.T) {
	cases := []struct {
		name      string
		w, h      int
		wantAside float32
	}{
		{"1470x1115", 1470, 1115, 280},
		{"1280x720", 1280, 720, 256},
		{"640x420", 640, 420, 224},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newFloatingPanelFixture(t, 60, 10, 14)
			tt := ui.NewTester(a.view, tc.w, tc.h)
			tt.SetTitleBar(dragBar)
			tt.Frame()
			videoBefore, ok := tt.Find("视频画面")
			if !ok {
				t.Fatal("missing 视频画面 before open")
			}
			if videoBefore.W != float32(tc.w) || videoBefore.H != float32(tc.h) {
				t.Fatalf("%s video did not fill window before open: got %+v want %dx%d", tc.name, videoBefore, tc.w, tc.h)
			}
			if err := tt.Click("选集"); err != nil {
				t.Fatalf("open panel via 选集: %v", err)
			}
			tt.Advance(350 * time.Millisecond)

			video, ok := tt.Find("视频画面")
			if !ok {
				t.Fatal("missing 视频画面")
			}
			if video != videoBefore {
				t.Fatalf("%s opening floating panel resized/squashed video: before=%+v after=%+v", tc.name, videoBefore, video)
			}
			vw, vh := a.player.Size()
			pic := playerPictureRect(video, vw, vh)

			panel, ok := tt.Find("悬浮选集面板")
			if !ok {
				t.Fatalf("missing 悬浮选集面板 at %s", tc.name)
			}
			back, ok := tt.Find("返回")
			if !ok {
				t.Fatalf("missing 返回 at %s", tc.name)
			}
			dock, ok := tt.Find("播放控制条")
			if !ok {
				t.Fatalf("missing 播放控制条 at %s", tc.name)
			}

			topDelta := absf(panel.Y - pic.Y)
			botDelta := absf((panel.Y + panel.H) - (pic.Y + pic.H))
			t.Logf("%s: pic=(%.2f,%.2f %.2fx%.2f) panel=(%.2f,%.2f %.2fx%.2f) Δtop=%.2f Δbottom=%.2f headerY=%.2f dockY=%.2f..%.2f",
				tc.name, pic.X, pic.Y, pic.W, pic.H, panel.X, panel.Y, panel.W, panel.H,
				topDelta, botDelta, back.Y, dock.Y, dock.Y+dock.H)

			if absf(panel.W-tc.wantAside) > 1.0 {
				t.Fatalf("%s panel width = %.2f, want %.2f", tc.name, panel.W, tc.wantAside)
			}
			if back.Y > dragBar.Bottom+12 {
				t.Fatalf("%s header sank below top of window: back.Y=%.2f titleBarBottom=%.2f", tc.name, back.Y, dragBar.Bottom)
			}
			if gap := float32(tc.h) - (dock.Y + dock.H); gap < -0.5 || gap > 24 {
				t.Fatalf("%s dock not anchored near window bottom: dockBottom=%.2f winH=%d gap=%.2f", tc.name, dock.Y+dock.H, tc.h, gap)
			}
			if panel.Y < back.Y+back.H {
				t.Fatalf("%s panel overlapped header: panelY=%.2f headerBottom=%.2f", tc.name, panel.Y, back.Y+back.H)
			}
			if panel.Y+panel.H > dock.Y {
				t.Fatalf("%s panel overlapped dock: panelBottom=%.2f dockY=%.2f", tc.name, panel.Y+panel.H, dock.Y)
			}
			// When the letterboxed video picture sits cleanly inside header..dock (e.g. 1470x1115),
			// the floating panel aligns top and bottom directly with the picture.
			if pic.Y >= back.Y+back.H+10 && pic.Y+pic.H <= dock.Y-10 {
				if topDelta > 1.0 || botDelta > 1.0 {
					t.Fatalf("%s alignment drift exceeds 1px: Δtop=%.2f Δbottom=%.2f (pic=%+v panel=%+v)",
						tc.name, topDelta, botDelta, pic, panel)
				}
			}
			if panel.X+panel.W > float32(tc.w)+0.5 {
				t.Fatalf("%s panel overflows window right: panelRight=%.2f winW=%d", tc.name, panel.X+panel.W, tc.w)
			}
		})
	}
}

func TestFloatingPanel14And60EpisodesEqualHeightFiveColumnsAndClick60(t *testing.T) {
	a14, _ := newFloatingPanelFixture(t, 14, 10, 14)
	tt14 := ui.NewTester(a14.view, 1280, 720)
	tt14.SetTitleBar(dragBar)
	tt14.Frame()
	if err := tt14.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt14.Advance(350 * time.Millisecond)
	p14, ok := tt14.Find("悬浮选集面板")
	if !ok {
		t.Fatal("14-ep panel missing")
	}

	a60, played := newFloatingPanelFixture(t, 60, 10, 14)
	tt60 := ui.NewTester(a60.view, 1280, 720)
	tt60.SetTitleBar(dragBar)
	tt60.Frame()
	if err := tt60.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt60.Advance(350 * time.Millisecond)
	p60, ok := tt60.Find("悬浮选集面板")
	if !ok {
		t.Fatal("60-ep panel missing")
	}

	if absf(p14.H-p60.H) > 1.0 {
		t.Fatalf("14-ep and 60-ep panel heights differ: h14=%.2f h60=%.2f", p14.H, p60.H)
	}

	// Verify 5 episodes per row.
	var row1 [5]ui.Rect
	for i := range 5 {
		r, ok := tt60.Find(fmt.Sprintf("%d", i+1))
		if !ok {
			t.Fatalf("episode %d missing", i+1)
		}
		row1[i] = r
		if i > 0 {
			if absf(r.Y-row1[0].Y) > 1.0 {
				t.Fatalf("episode %d not on row 1: r=%+v row1[0]=%+v", i+1, r, row1[0])
			}
			if r.X <= row1[i-1].X+row1[i-1].W {
				t.Fatalf("episode %d overlaps previous column: prev=%+v cur=%+v", i+1, row1[i-1], r)
			}
		}
	}
	r6, ok := tt60.Find("6")
	if !ok {
		t.Fatal("episode 6 missing")
	}
	if r6.Y <= row1[0].Y+row1[0].H || absf(r6.X-row1[0].X) > 1.0 {
		t.Fatalf("episode 6 did not wrap to column 1 of row 2: r6=%+v r1=%+v", r6, row1[0])
	}

	// Verify active episode 14 is visible inside the scroll viewport upon open.
	epList, ok := tt60.Find("选集列表")
	if !ok {
		t.Fatal("episode list viewport missing")
	}
	r14, ok := tt60.Find("14")
	if !ok || r14.Y < epList.Y-1 || r14.Y+r14.H > epList.Y+epList.H+1 {
		t.Fatalf("active episode 14 not visible in viewport: ep=%+v viewport=%+v", r14, epList)
	}

	// Verify active episode 45 is auto-revealed on open as well.
	a45, _ := newFloatingPanelFixture(t, 60, 10, 45)
	tt45 := ui.NewTester(a45.view, 1280, 720)
	tt45.SetTitleBar(dragBar)
	tt45.Frame()
	if err := tt45.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt45.Advance(350 * time.Millisecond)
	epList45, ok := tt45.Find("选集列表")
	if !ok {
		t.Fatal("episode list viewport missing for ep 45")
	}
	r45, ok := tt45.Find("45")
	if !ok || r45.Y < epList45.Y-1 || r45.Y+r45.H > epList45.Y+epList45.H+1 {
		t.Fatalf("active episode 45 was not scrolled into view on open: r45=%+v viewport=%+v", r45, epList45)
	}

	// Scroll down inside the 60-episode list and actually click episode 60.
	tt60.Scroll(epList.X+epList.W/2, epList.Y+epList.H/2, 0, -600)
	r60, ok := tt60.Find("60")
	if !ok || r60.H <= 0 || r60.Y < epList.Y-1 || r60.Y+r60.H > epList.Y+epList.H+1 {
		// Try opposite scroll sign if platform convention is inverted.
		tt60.Scroll(epList.X+epList.W/2, epList.Y+epList.H/2, 0, 1200)
		r60, ok = tt60.Find("60")
	}
	if !ok || r60.H <= 0 || r60.Y < epList.Y-1 || r60.Y+r60.H > epList.Y+epList.H+1 {
		t.Fatalf("episode 60 did not scroll into view: r60=%+v viewport=%+v", r60, epList)
	}
	tt60.ClickAt(r60.X+r60.W/2, r60.Y+r60.H/2)
	if a60.playingEpisode != "第60集" || len(*played) == 0 || (*played)[len(*played)-1] != "hnm3u8/第60集" {
		t.Fatalf("clicking episode 60 did not play it: playing=%q played=%v", a60.playingEpisode, *played)
	}
}

func TestFloatingPanel10SourcesMenuCappedScrollableZeroShiftAndIndependentLines(t *testing.T) {
	a, played := newFloatingPanelFixture(t, 60, 10, 14)
	tt := ui.NewTester(a.view, 1280, 720)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if err := tt.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(350 * time.Millisecond)

	// Switch line independently without changing source.
	if err := tt.Click("hnyun"); err != nil {
		t.Fatalf("click line hnyun: %v", err)
	}
	if a.playingSource != "hnyun" || a.title.item.Entry.Label() != "示例来源 A" {
		t.Fatalf("line click mutated source or failed: source=%q line=%q played=%v",
			a.title.item.Entry.Label(), a.playingSource, *played)
	}

	picBefore, _ := tt.Find("视频画面")
	panelBefore, _ := tt.Find("悬浮选集面板")
	dockBefore, _ := tt.Find("播放控制条")

	if err := tt.Click("选择来源"); err != nil {
		t.Fatalf("open source menu: %v", err)
	}
	menuItems := tt.Menu()
	if len(menuItems) != 10 || menuItems[9] != "示例来源 J" {
		t.Fatalf("expected 10 sources ending in 示例来源 J, got %v", menuItems)
	}

	// Opening the source menu must not shift picture, panel, or dock.
	picOpen, _ := tt.Find("视频画面")
	panelOpen, _ := tt.Find("悬浮选集面板")
	dockOpen, _ := tt.Find("播放控制条")
	if picOpen != picBefore || panelOpen != panelBefore || dockOpen != dockBefore {
		t.Fatalf("source menu shifted layout:\n pic:   %+v -> %+v\n panel: %+v -> %+v\n dock:  %+v -> %+v",
			picBefore, picOpen, panelBefore, panelOpen, dockBefore, dockOpen)
	}

	// Verify the menu height is capped (item 10 starts below visible menu bounds before scrolling).
	itemA, okA := tt.Find("示例来源 A")
	itemJ, okJ := tt.Find("示例来源 J")
	if !okA || !okJ {
		t.Fatalf("source menu items not found: A=%v J=%v", okA, okJ)
	}
	if itemJ.Y-itemA.Y <= 244 {
		t.Fatalf("10-item menu is unexpectedly tiny: A=%+v J=%+v", itemA, itemJ)
	}

	// Escape closes ONLY the source menu while keeping the episode panel open.
	tt.Key(0, ui.KeyEscape)
	if len(tt.Menu()) != 0 || !a.panelOpen {
		t.Fatalf("Escape did not close only source menu: menu=%v panelOpen=%v", tt.Menu(), a.panelOpen)
	}

	// Re-open source menu, scroll inside it to item 10, and click item 10 directly.
	if err := tt.Click("选择来源"); err != nil {
		t.Fatal(err)
	}
	itemA2, okA2 := tt.Find("示例来源 A")
	if !okA2 {
		t.Fatal("reopened source menu missing item A")
	}
	tt.Scroll(itemA2.X+itemA2.W/2, itemA2.Y+40, 0, 500)
	itemJ2, okJ2 := tt.Find("示例来源 J")
	if !okJ2 || itemJ2.H <= 0 {
		tt.Scroll(itemA2.X+itemA2.W/2, itemA2.Y+40, 0, -1000)
		itemJ2, okJ2 = tt.Find("示例来源 J")
	}
	if !okJ2 || itemJ2.H <= 0 {
		t.Fatalf("10th source did not scroll into clickable view: itemJ=%+v", itemJ2)
	}
	tt.ClickAt(itemJ2.X+itemJ2.W/2, itemJ2.Y+itemJ2.H/2)
	// switchSource creates a panelChoice targeting 示例来源 J (offline API has no server, so it records target choice).
	if a.panelChoice == nil && a.title.item.Entry.Label() != "示例来源 J" {
		t.Fatalf("clicking 10th source did not trigger switchSource: choice=%+v item=%q", a.panelChoice, a.title.item.Entry.Label())
	}
	if a.panelChoice != nil && a.panelChoice.item.Entry.Label() != "示例来源 J" {
		t.Fatalf("panelChoice targeted wrong source: %q", a.panelChoice.item.Entry.Label())
	}
}

func TestFloatingPanelSourceMenuStaysInsidePanelAt640x420(t *testing.T) {
	a, _ := newFloatingPanelFixture(t, 60, 10, 14)
	tt := ui.NewTester(a.view, 640, 420)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if err := tt.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(350 * time.Millisecond)

	// Scroll panel-pickers-scroll back to the top so the source selector trigger is clickable.
	epList, ok := tt.Find("选集列表")
	if !ok {
		t.Fatal("missing 选集列表 at 640x420")
	}
	tt.Scroll(epList.X+epList.W/2, epList.Y+epList.H/2, 0, -600)
	srcRect, ok := tt.Find("选择来源")
	if !ok || srcRect.H <= 0 {
		tt.Scroll(epList.X+epList.W/2, epList.Y+epList.H/2, 0, 1200)
		srcRect, ok = tt.Find("选择来源")
	}
	if !ok || srcRect.H <= 0 {
		t.Fatalf("source trigger not reachable at 640x420: %+v", srcRect)
	}
	tt.ClickAt(srcRect.X+srcRect.W/2, srcRect.Y+srcRect.H/2)
	tt.Advance(200 * time.Millisecond)

	if menu := tt.Menu(); len(menu) != 10 || menu[9] != "示例来源 J" {
		t.Fatalf("expected 10 sources at 640x420, got %v", menu)
	}
	panelRect, okP := tt.Find("悬浮选集面板")
	menuRect, okM := tt.Find("来源菜单")
	if !okP || !okM || menuRect.H <= 0 {
		t.Fatalf("missing bounds at 640x420: panel=%+v(%v) menu=%+v(%v)", panelRect, okP, menuRect, okM)
	}
	if menuRect.Y+menuRect.H > panelRect.Y+panelRect.H+0.5 {
		t.Fatalf("640x420 source menu bottom %.2f exceeded floating panel bottom %.2f (panel=%+v src=%+v menu=%+v)",
			menuRect.Y+menuRect.H, panelRect.Y+panelRect.H, panelRect, srcRect, menuRect)
	}
	t.Logf("640x420 normal 16:9: panelBottom=%.2f srcBottom=%.2f menu=%.2f..%.2f (h=%.2f)",
		panelRect.Y+panelRect.H, srcRect.Y+srcRect.H, menuRect.Y, menuRect.Y+menuRect.H, menuRect.H)

	// Scroll inside the capped menu and click the 10th source.
	tt.Scroll(menuRect.X+menuRect.W/2, menuRect.Y+menuRect.H/2, 0, 600)
	itemJ, okJ := tt.Find("示例来源 J")
	if !okJ || itemJ.H <= 0 || itemJ.Y+itemJ.H > menuRect.Y+menuRect.H+1 {
		tt.Scroll(menuRect.X+menuRect.W/2, menuRect.Y+menuRect.H/2, 0, -1200)
		itemJ, okJ = tt.Find("示例来源 J")
	}
	if !okJ || itemJ.H <= 0 || itemJ.Y < menuRect.Y-1 || itemJ.Y+itemJ.H > menuRect.Y+menuRect.H+1 {
		t.Fatalf("10th source not scrolled inside capped 640x420 menu: itemJ=%+v menu=%+v", itemJ, menuRect)
	}
	tt.ClickAt(itemJ.X+itemJ.W/2, itemJ.Y+itemJ.H/2)
	if a.panelChoice == nil && a.title.item.Entry.Label() != "示例来源 J" {
		t.Fatalf("clicking 10th source at 640x420 did not trigger switchSource: choice=%+v item=%q", a.panelChoice, a.title.item.Entry.Label())
	}
	if a.panelChoice != nil && a.panelChoice.item.Entry.Label() != "示例来源 J" {
		t.Fatalf("panelChoice targeted wrong source at 640x420: %q", a.panelChoice.item.Entry.Label())
	}
}

func TestFloatingPanelSkipInlineCollapseReopenStampsPauseAndSmallWindow(t *testing.T) {
	a, _ := newFloatingPanelFixture(t, 60, 10, 14)
	tt := ui.NewTester(a.view, 1280, 720)
	tt.SetTitleBar(dragBar)
	tt.Frame()

	// First open: skip editor is default expanded.
	if err := tt.Click("选集"); err != nil {
		t.Fatal(err)
	}
	if !a.panelSkipOpen {
		t.Fatal("skip editor was not expanded on first open")
	}
	if _, ok := tt.Find("跳过片头秒数"); !ok {
		t.Fatal("opening seconds input missing on first open")
	}

	// Collapse manually.
	if err := tt.Click("片头片尾"); err != nil {
		t.Fatalf("collapse skip editor: %v", err)
	}
	if a.panelSkipOpen {
		t.Fatal("clicking 片头片尾 did not collapse skip editor")
	}
	if _, ok := tt.Find("跳过片头秒数"); ok {
		t.Fatal("opening seconds input still shown while collapsed")
	}
	if !tt.HasText("已关闭") {
		t.Fatalf("collapsed skip summary missing: %v", tt.Texts())
	}

	// Close episode panel and reopen: skip editor must default expand again.
	if err := tt.Click("收起选集"); err != nil {
		t.Fatalf("close episode panel: %v", err)
	}
	if a.panelOpen {
		t.Fatal("panel stayed open after clicking 收起选集")
	}
	if err := tt.Click("选集"); err != nil {
		t.Fatalf("reopen episode panel: %v", err)
	}
	if !a.panelSkipOpen {
		t.Fatal("skip editor did not re-expand when reopening episode panel")
	}
	if _, ok := tt.Find("跳过片头秒数"); !ok {
		t.Fatal("opening seconds input missing after reopen")
	}

	// Stamp opening ("片头到这") at 26:49 (1609s) and ending ("片尾从这") at 44:17 (120s remaining).
	a.player.Seek(95 * time.Second)
	tt.Frame()
	if err := tt.Click("片头到这"); err != nil {
		t.Fatalf("click 片头到这: %v", err)
	}
	if got := a.seriesSkips.get(a.title.name); !got.Enabled || got.Opening != 95 {
		t.Fatalf("片头到这 did not save & enable: %+v", got)
	}

	a.player.Seek(a.player.Duration() - 120*time.Second)
	tt.Frame()
	if err := tt.Click("片尾从这"); err != nil {
		t.Fatalf("click 片尾从这: %v", err)
	}
	if got := a.seriesSkips.get(a.title.name); !got.Enabled || got.Opening != 95 || got.Ending != 120 {
		t.Fatalf("片尾从这 did not save & enable: %+v", got)
	}

	// Manual pause retains both opening and ending durations.
	if err := tt.Click("启用本剧跳过"); err != nil {
		t.Fatalf("pause skip switch: %v", err)
	}
	if got := a.seriesSkips.get(a.title.name); got.Enabled || got.Opening != 95 || got.Ending != 120 {
		t.Fatalf("manual pause lost durations or stayed enabled: %+v", got)
	}

	// Typing a positive number into outro re-enables automatically and persists to disk.
	outR, ok := tt.Find("跳过片尾秒数")
	if !ok {
		t.Fatal("ending input missing")
	}
	tt.ClickAt(outR.X+outR.W/2, outR.Y+outR.H/2)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("135")
	if got := a.seriesSkips.get(a.title.name); !got.Enabled || got.Opening != 95 || got.Ending != 135 {
		t.Fatalf("typing positive outro did not re-enable & save: %+v", got)
	}
	saved, err := readSeriesSkips(a.seriesSkips.path)
	if err != nil || saved[titleKey(a.title.name)] != (seriesSkipRule{Enabled: true, Opening: 95, Ending: 135}) {
		t.Fatalf("persisted skip rule mismatch: %+v err=%v", saved, err)
	}

	// Switching to episode 15 carries over the series skip rule (opening auto-skip + ending trigger).
	if err := tt.Click("15"); err != nil {
		t.Fatalf("switch to episode 15: %v", err)
	}
	tt.Frame()
	if a.playingEpisode != "第15集" {
		t.Fatalf("expected episode 15, got %q", a.playingEpisode)
	}
	if a.player.Position() != 95*time.Second {
		t.Fatalf("episode 15 did not inherit 95s opening skip: pos=%s", a.player.Position())
	}
	a.player.Play()
	a.player.Seek(a.player.Duration() - 130*time.Second)
	if !a.skipEndingReady(a.player) {
		t.Fatal("episode 15 did not honor inherited 135s ending skip rule")
	}

	// Small window 640x420 (windowed + fullscreen): active episode and both skip inputs remain directly operable.
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("640x420-full-%v", full), func(t *testing.T) {
			sa, _ := newFloatingPanelFixture(t, 60, 10, 14)
			sa.setFullScreen(full)
			stt := ui.NewTester(sa.view, 640, 420)
			if !full {
				stt.SetTitleBar(dragBar)
			}
			stt.Frame()
			if err := stt.Click("选集"); err != nil {
				t.Fatalf("open panel at 640x420 full=%v: %v", full, err)
			}
			stt.Advance(350 * time.Millisecond)

			panel, ok := stt.Find("悬浮选集面板")
			if !ok {
				t.Fatal("panel missing at 640x420")
			}
			epList, ok := stt.Find("选集列表")
			if !ok {
				t.Fatal("episode list missing at 640x420")
			}
			ep14, ok := stt.Find("14")
			if !ok || ep14.H <= 0 || ep14.Y < epList.Y-1 || ep14.Y+ep14.H > epList.Y+epList.H+1 {
				t.Fatalf("active episode 14 not visible in 640x420 panel: ep14=%+v list=%+v panel=%+v", ep14, epList, panel)
			}
			for _, label := range []string{"跳过片头秒数", "跳过片尾秒数", "片头到这", "片尾从这", "自动下一集"} {
				r, ok := stt.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 || r.Y < panel.Y || r.Y+r.H > panel.Y+panel.H+1 {
					t.Fatalf("control %q out of bounds at 640x420 full=%v: r=%+v panel=%+v", label, full, r, panel)
				}
			}
			// Directly edit opening seconds in 640x420.
			inR, _ := stt.Find("跳过片头秒数")
			stt.ClickAt(inR.X+inR.W/2, inR.Y+inR.H/2)
			stt.Key(ui.Cmd, ui.KeyA)
			stt.Type("88")
			if got := sa.seriesSkips.get(sa.title.name); !got.Enabled || got.Opening != 88 {
				t.Fatalf("direct edit in 640x420 failed: %+v", got)
			}
		})
	}
}

func TestFloatingPanelFullscreenDoesNotResizeVideo(t *testing.T) {
	a, _ := newFloatingPanelFixture(t, 60, 10, 14)
	a.setFullScreen(true)
	tt := ui.NewTester(a.view, 1280, 720)
	videoBefore, ok := tt.Find("视频画面")
	if !ok || videoBefore.W != 1280 || videoBefore.H != 720 {
		t.Fatalf("fullscreen video rect = %+v, want 1280x720", videoBefore)
	}
	if err := tt.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(350 * time.Millisecond)
	videoAfter, _ := tt.Find("视频画面")
	if videoAfter != videoBefore {
		t.Fatalf("fullscreen floating panel resized video: before=%+v after=%+v", videoBefore, videoAfter)
	}
	if _, ok := tt.Find("悬浮选集面板"); !ok {
		t.Fatal("fullscreen floating panel did not render")
	}
}

func TestFloatingPanelAlignsAcrossVideoAspectModes(t *testing.T) {
	for _, mode := range []videoAspectMode{aspectOriginal, aspect16x9, aspect4x3, aspect21x9, aspectFill} {
		t.Run(mode.label(), func(t *testing.T) {
			a, _ := newFloatingPanelFixture(t, 60, 10, 14)
			a.playerExtra.aspect = mode
			tt := ui.NewTester(a.view, 1280, 720)
			tt.SetTitleBar(dragBar)
			tt.Frame()
			videoBefore, ok := tt.Find("视频画面")
			if !ok {
				t.Fatal("missing 视频画面 before open")
			}
			if err := tt.Click("选集"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(350 * time.Millisecond)

			video, ok := tt.Find("视频画面")
			if !ok {
				t.Fatal("missing 视频画面")
			}
			if video != videoBefore {
				t.Fatalf("aspect %s: opening floating panel resized video: before=%+v after=%+v", mode.label(), videoBefore, video)
			}
			panel, ok := tt.Find("悬浮选集面板")
			if !ok {
				t.Fatal("missing 悬浮选集面板")
			}
			back, _ := tt.Find("返回")
			dock, _ := tt.Find("播放控制条")
			if panel.Y < back.Y+back.H || panel.Y+panel.H > dock.Y {
				t.Fatalf("aspect %s: panel overlapped header/dock: panel=%+v header=%+v dock=%+v", mode.label(), panel, back, dock)
			}
		})
	}
}

func TestFloatingPanelNativeReviewSnapshots(t *testing.T) {
	outDir := os.Getenv("XIAOMU_NATIVE_REVIEW_DIR")
	if outDir == "" {
		return
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	save := func(name string, tt *ui.Tester) {
		t.Helper()
		f, err := os.Create(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	attachDemoDanmaku := func(a *app) {
		a.preferences.Danmaku.Enabled = true
		dur := a.player.Duration()
		comments := make([]danmaku.Comment, 0, 140)
		// Create a natural multi-peak highlight curve across the episode plus visible comments at 26:49.
		for i := range 120 {
			sec := float64(i) * dur.Seconds() / 120.0
			weight := 1
			if math.Abs(sec-dur.Seconds()*0.22) < 90 || math.Abs(sec-dur.Seconds()*0.72) < 120 {
				weight = 3
			}
			for k := range weight {
				comments = append(comments, danmaku.Comment{
					Time:  time.Duration((sec + float64(k)*2) * float64(time.Second)),
					Mode:  1,
					Color: 0xffffff,
					Text:  fmt.Sprintf("示例弹幕 %d", i+1),
				})
			}
		}
		base := 26*time.Minute + 49*time.Second
		comments = append(comments,
			danmaku.Comment{Time: base - 2*time.Second, Mode: 1, Color: 0xffffff, Text: "这段对话有意思"},
			danmaku.Comment{Time: base - 3*time.Second, Mode: 1, Color: 0xffffff, Text: "前面那段终于对上了"},
			danmaku.Comment{Time: base - 1*time.Second, Mode: 1, Color: 0xffffff, Text: "这句我记住了"},
			danmaku.Comment{Time: base - 500*time.Millisecond, Mode: 1, Color: 0xffffff, Text: "看到这里才明白"},
		)
		a.applyDanmaku(danmaku.Episode{ID: "demo", Anime: "示例古装剧", Name: "第14集"}, comments)
	}

	// 1. 1470x1115 (60 eps, ep 14 active) + sources open + closed
	a, _ := newFloatingPanelFixture(t, 60, 10, 14)
	attachDemoDanmaku(a)
	tt := ui.NewTester(a.view, 1470, 1115)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if err := tt.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(350 * time.Millisecond)
	save("native-1470x1115.png", tt)

	if err := tt.Click("选择来源"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(200 * time.Millisecond)
	save("native-1470x1115-sources-open.png", tt)
	tt.Key(0, ui.KeyEscape)
	tt.Advance(200 * time.Millisecond)

	if err := tt.Click("收起选集"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(350 * time.Millisecond)
	save("native-1470x1115-closed.png", tt)

	// 2. 1470x1115 short series (14 eps)
	a14, _ := newFloatingPanelFixture(t, 14, 10, 14)
	attachDemoDanmaku(a14)
	tt14 := ui.NewTester(a14.view, 1470, 1115)
	tt14.SetTitleBar(dragBar)
	tt14.Frame()
	if err := tt14.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt14.Advance(350 * time.Millisecond)
	save("native-1470x1115-short-14.png", tt14)

	// 3. 1280x720 desktop standard window
	a1280, _ := newFloatingPanelFixture(t, 60, 10, 14)
	attachDemoDanmaku(a1280)
	tt1280 := ui.NewTester(a1280.view, 1280, 720)
	tt1280.SetTitleBar(dragBar)
	tt1280.Frame()
	if err := tt1280.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt1280.Advance(350 * time.Millisecond)
	save("native-1280x720.png", tt1280)

	// 4. 640x420 minimum desktop window
	a640, _ := newFloatingPanelFixture(t, 60, 10, 14)
	attachDemoDanmaku(a640)
	tt640 := ui.NewTester(a640.view, 640, 420)
	tt640.SetTitleBar(dragBar)
	tt640.Frame()
	if err := tt640.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt640.Advance(350 * time.Millisecond)
	save("native-640x420.png", tt640)

	// 5. 1280x720 fullscreen with panel open
	aFS, _ := newFloatingPanelFixture(t, 60, 10, 14)
	attachDemoDanmaku(aFS)
	aFS.setFullScreen(true)
	ttFS := ui.NewTester(aFS.view, 1280, 720)
	ttFS.Frame()
	if err := ttFS.Click("选集"); err != nil {
		t.Fatal(err)
	}
	ttFS.Advance(350 * time.Millisecond)
	save("native-1280x720-fullscreen.png", ttFS)
}
