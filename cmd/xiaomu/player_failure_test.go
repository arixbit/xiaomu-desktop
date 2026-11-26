package main

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/egoist/mygo/ui"
)

// unknownSizeMediaPlayer simulates a stream that failed before decoding any
// video frame, so dimensions are unknown (0x0) and Ready() stays false.
type unknownSizeMediaPlayer struct {
	playing bool
	pos     time.Duration
	dur     time.Duration
	rate    float64
	vol     float64
	muted   bool
	err     error
}

func (p *unknownSizeMediaPlayer) Play()                      { p.playing = true }
func (p *unknownSizeMediaPlayer) Pause()                     { p.playing = false }
func (p *unknownSizeMediaPlayer) Playing() bool              { return p.playing }
func (p *unknownSizeMediaPlayer) Ended() bool                { return false }
func (p *unknownSizeMediaPlayer) Ready() bool                { return false }
func (p *unknownSizeMediaPlayer) Buffering() bool            { return false }
func (p *unknownSizeMediaPlayer) Size() (int, int)           { return 0, 0 }
func (p *unknownSizeMediaPlayer) Duration() time.Duration    { return p.dur }
func (p *unknownSizeMediaPlayer) Position() time.Duration    { return p.pos }
func (p *unknownSizeMediaPlayer) Seek(d time.Duration)       { p.pos = d }
func (p *unknownSizeMediaPlayer) Rate() float64              { return p.rate }
func (p *unknownSizeMediaPlayer) SetRate(r float64)          { p.rate = r }
func (p *unknownSizeMediaPlayer) Volume() float64            { return p.vol }
func (p *unknownSizeMediaPlayer) SetVolume(v float64)        { p.vol = v }
func (p *unknownSizeMediaPlayer) SetMuted(m bool)            { p.muted = m }
func (p *unknownSizeMediaPlayer) Err() error                 { return p.err }
func (p *unknownSizeMediaPlayer) Close()                     {}
func (p *unknownSizeMediaPlayer) View(c *ui.Context) ui.Element {
	return ui.Box(c).Background(ui.RGB(9, 10, 13))
}

func newFailureFixture(t *testing.T, sourceCount int, lineNames []string, epCount int, failedEp int, resumeAt time.Duration, rawErr string, unknownSize bool) (*app, *httptest.Server) {
	t.Helper()
	const dur = 46*time.Minute + 17*time.Second

	var p mediaPlayer
	if unknownSize {
		p = &unknownSizeMediaPlayer{pos: resumeAt, dur: dur, rate: 1, vol: 0.7}
	} else {
		vp, err := ui.NewVideoPlayer(newCinema16x9Video(1280, 720, dur))
		if err != nil {
			t.Fatal(err)
		}
		p = wrapSystemPlayer(vp)
		p.Pause()
		if resumeAt > 0 {
			p.Seek(resumeAt)
		}
	}

	eps := makeFixtureEpisodes(epCount)
	if failedEp < 1 || failedEp > epCount {
		failedEp = 1
	}
	if len(lineNames) == 0 {
		lineNames = []string{"bfzym3u8"}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site := r.URL.Query().Get("site")
		var parts []string
		for i := range epCount {
			parts = append(parts, fmt.Sprintf("第%d集$/%s/ep-%02d.mp4", i+1, site, i+1))
		}
		fmt.Fprintf(w, `{"list":[{"vod_name":"示例剧集","vod_play_from":"高清$$$备用","vod_play_url":%q}]}`,
			strings.Join(parts, "#")+"$$$"+strings.Join(parts, "#"))
	}))
	t.Cleanup(srv.Close)

	doc := &config.Config{URL: srv.URL + "/config.json"}
	seeds := make([]library.Item, sourceCount)
	for i := range sourceCount {
		name := fmt.Sprintf("示例来源 %c", 'A'+i)
		siteKey := fmt.Sprintf("site-%d", i)
		site := config.Site{
			Key:  siteKey,
			Name: name,
			Type: 1,
			Kind: config.KindJSON,
			API:  srv.URL + "/api?site=" + siteKey,
		}
		doc.Sites = append(doc.Sites, site)
		seeds[i] = library.Item{
			Entry: library.Entry{Config: doc, Site: site},
			Video: catalog.Video{ID: catalog.ID(fmt.Sprintf("v-%d", i)), Name: "示例剧集"},
		}
	}

	sources := make([]catalog.Source, len(lineNames))
	for i, ln := range lineNames {
		sources[i] = catalog.Source{Name: ln, Episodes: eps}
	}

	a := &app{
		player:           p,
		autoNext:         true,
		playingSource:    lineNames[0],
		playingEpisode:   eps[failedEp-1].Name,
		resumeAt:         resumeAt,
		lastPosition:     resumeAt,
		preferences:      defaultPlayerPreferences(),
		seriesSkips:      seriesSkipStore{path: filepath.Join(t.TempDir(), "series-skips.json")},
		panelOpen:        false,
		panelTouched:     true,
		panelSource:      0,
		panelSkipOpen:    true,
		nav:              []routeKind{routeHome, routeDetail},
		newDanmakuClient: func(string) (*danmaku.Client, error) { return nil, errors.New("offline test fixture") },
	}
	a.open = func(string) (*ui.VideoPlayer, error) {
		nv, err := ui.NewVideoPlayer(newCinema16x9Video(1280, 720, dur))
		if err != nil {
			return nil, err
		}
		nv.Pause()
		return nv, nil
	}
	a.title = &titlePage{
		name:   "示例剧集",
		item:   seeds[0],
		seeds:  seeds,
		failed: map[string]string{},
		session: &recoverySession{play: func(_ context.Context, _ string, ep catalog.Episode) (catalog.Playback, error) {
			return catalog.Playback{URL: ep.ID}, nil
		}},
		detail: &catalog.Detail{
			Name:    "示例剧集",
			Sources: sources,
		},
	}
	if rawErr != "" {
		a.playFailure = &episodeFailure{
			line:    lineNames[0],
			episode: eps[failedEp-1],
			at:      resumeAt,
			err:     errors.New(rawErr),
		}
	}
	t.Cleanup(a.close)
	return a, srv
}

func TestPlayerFailureDefaultCollapsedCenteredAndZeroProgress(t *testing.T) {
	const rawErr = "mpv playback: loading failed (-13)"
	resumeAt := 26*time.Minute + 49*time.Second

	for _, tc := range []struct {
		name        string
		w, h        int
		unknownSize bool
		fullscreen  bool
	}{
		{"1280x720-known-size", 1280, 720, false, false},
		{"1280x720-unknown-size", 1280, 720, true, false},
		{"640x420-unknown-size", 640, 420, true, false},
		{"1280x720-fullscreen", 1280, 720, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, resumeAt, rawErr, tc.unknownSize)
			if tc.fullscreen {
				a.setFullScreen(true)
			}
			tt := ui.NewTester(a.view, tc.w, tc.h)
			if !tc.fullscreen {
				tt.SetTitleBar(dragBar)
			}
			tt.Frame()
			tt.Advance(350 * time.Millisecond)

			if !tt.HasText("这条线路播不了") {
				t.Fatalf("missing heading: %v", tt.Texts())
			}
			if !tt.HasText("第14集") || !tt.HasText("已看到 26:49") {
				t.Fatalf("missing failed episode or progress context: %v", tt.Texts())
			}
			if tt.HasText(rawErr) {
				t.Fatalf("raw error must be collapsed by default: %v", tt.Texts())
			}

			card, ok := tt.Find("播放失败提示")
			if !ok {
				t.Fatalf("failure card accessibility container missing: %v", tt.Texts())
			}
			pic, ok := tt.Find("视频画面")
			if !ok {
				t.Fatal("missing 视频画面")
			}
			header, ok := tt.Find("返回")
			if !ok {
				t.Fatal("missing 返回")
			}
			dock, ok := tt.Find("播放控制条")
			if !ok {
				t.Fatal("missing 播放控制条")
			}

			if !tc.fullscreen {
				picCX, picCY := pic.X+pic.W/2, pic.Y+pic.H/2
				cardCX, cardCY := card.X+card.W/2, card.Y+card.H/2
				if absf(cardCX-picCX) > 2.0 || absf(cardCY-picCY) > 2.0 {
					t.Fatalf("card not centered in picture: card=%+v (center %.1f,%.1f) pic=%+v (center %.1f,%.1f)",
						card, cardCX, cardCY, pic, picCX, picCY)
				}
			}
			if card.Y < header.Y+header.H-0.5 || card.Y+card.H > dock.Y+0.5 {
				t.Fatalf("card overlaps header or dock: card=%+v header=%+v dock=%+v", card, header, dock)
			}
			if tc.w >= 1280 && absf(card.W-374) > 8 {
				t.Fatalf("desktop card width = %.1f, want ~374", card.W)
			}
			if card.W > pic.W+0.5 || card.H > pic.H+0.5 {
				t.Fatalf("card overflows picture bounds: card=%+v pic=%+v", card, pic)
			}
		})
	}

	// Real player error arriving on a.view triggers checkPlayback and preserves unapplied resumeAt.
	t.Run("live-player-error-preserves-unapplied-resume", func(t *testing.T) {
		a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, 0, "", true)
		up := a.player.(*unknownSizeMediaPlayer)
		up.pos = 0
		up.err = errors.New("mpv playback: demuxer failed (-4)")
		a.resumeAt = 18*time.Minute + 5*time.Second
		a.lastPosition = 0
		a.playFailure = nil

		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()

		if a.playFailure == nil || a.playFailure.at != 18*time.Minute+5*time.Second {
			t.Fatalf("checkPlayback did not capture unapplied resumeAt: %+v", a.playFailure)
		}
		if !tt.HasText("这条线路播不了") || !tt.HasText("第14集 · 已看到 18:05") {
			t.Fatalf("overlay missing captured episode/time: %v", tt.Texts())
		}
		if tt.HasText("demuxer failed") {
			t.Fatalf("raw error should remain collapsed on first error: %v", tt.Texts())
		}
	})

	// Zero progress omits "已看到".
	t.Run("zero-progress-omits-time", func(t *testing.T) {
		a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, 0, rawErr, true)
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()
		if !tt.HasText("第14集") {
			t.Fatalf("missing episode label at zero progress: %v", tt.Texts())
		}
		if tt.HasText("已看到") {
			t.Fatalf("zero progress should omit 已看到 time: %v", tt.Texts())
		}
	})

	// ReduceTransparency preference keeps the overlay readable and intact.
	t.Run("reduce-transparency", func(t *testing.T) {
		a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, resumeAt, rawErr, true)
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetPreferences(ui.Preferences{ReduceTransparency: true, HighContrast: true})
		tt.SetTitleBar(dragBar)
		tt.Frame()
		if _, ok := tt.Find("播放失败提示"); !ok {
			t.Fatal("failure card missing under ReduceTransparency")
		}
	})
}

func TestPlayerFailureDetailsExpandCopyAndLongErrorWrapping(t *testing.T) {
	longErr := "mpv playback: loading failed (-13) upstream=https://example.com/hls/very/long/segment/path/without/spaces/0123456789abcdef0123456789abcdef/index.m3u8?token=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	for _, size := range [][2]int{{1280, 720}, {640, 420}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, 26*time.Minute+49*time.Second, longErr, true)
			tt := ui.NewTester(a.view, size[0], size[1])
			tt.SetTitleBar(dragBar)
			tt.Frame()
			tt.Advance(350 * time.Millisecond)

			if tt.HasText(longErr) {
				t.Fatal("raw error should be hidden before expanding 错误详情")
			}
			if err := tt.Click("错误详情"); err != nil {
				t.Fatalf("expand 错误详情: %v; texts=%v", err, tt.Texts())
			}
			tt.Advance(250 * time.Millisecond)

			if !tt.HasText("示例来源 A · bfzym3u8") {
				t.Fatalf("expanded details missing source/line route: %v", tt.Texts())
			}
			if !tt.HasText(longErr) {
				t.Fatalf("expanded details missing raw error: %v", tt.Texts())
			}

			// Error scroll box stays bounded within card width.
			card, _ := tt.Find("播放失败提示")
			errBox, okErr := tt.Find("错误原文")
			if !okErr {
				t.Fatal("missing 错误原文 scroll container")
			}
			if errBox.X+errBox.W > card.X+card.W+1 || errBox.Y+errBox.H > card.Y+card.H+1 {
				t.Fatalf("error box overflowed card: errBox=%+v card=%+v", errBox, card)
			}

			// Copy succeeds -> clipboard receives exact rawErr and label becomes 已复制.
			if err := tt.Click("复制"); err != nil {
				t.Fatalf("click 复制: %v; texts=%v", err, tt.Texts())
			}
			if got := tt.Clipboard(); got != longErr {
				t.Fatalf("clipboard = %q, want %q", got, longErr)
			}
			if !tt.HasText("已复制") {
				t.Fatalf("copy success did not show 已复制: %v", tt.Texts())
			}

			// Top and bottom controls stay accessible while details are expanded.
			header, okH := tt.Find("返回")
			dock, okD := tt.Find("播放控制条")
			if !okH || !okD {
				t.Fatalf("header/dock missing when details expanded: header=%v dock=%v", okH, okD)
			}
			if card.Y < header.Y+header.H-0.5 || card.Y+card.H > dock.Y+0.5 {
				t.Fatalf("expanded card overlapped header/dock at %dx%d: card=%+v header=%+v dock=%+v",
					size[0], size[1], card, header, dock)
			}
			if err := tt.Click("选集"); err != nil {
				t.Fatalf("选集 not clickable with expanded details: %v", err)
			}
			if !a.panelOpen {
				t.Fatal("选集 click did not open panel while details expanded")
			}
		})
	}

	// Clipboard failure must NOT claim 已复制.
	t.Run("clipboard-write-error", func(t *testing.T) {
		a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, 90*time.Second, "mpv playback: loading failed (-13)", true)
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.FailClipboard(errors.New("clipboard locked"))
		tt.Frame()
		if err := tt.Click("错误详情"); err != nil {
			t.Fatal(err)
		}
		if err := tt.Click("复制"); err != nil {
			t.Fatal(err)
		}
		if tt.HasText("已复制") {
			t.Fatalf("failed clipboard write falsely displayed 已复制: %v", tt.Texts())
		}
		if tt.Clipboard() != "" {
			t.Fatalf("failed clipboard write still populated clipboard: %q", tt.Clipboard())
		}
	})

	// Escape collapses details when panel/menu are closed; switching episode resets details.
	t.Run("esc-collapses-details-and-episode-change-resets", func(t *testing.T) {
		a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, 90*time.Second, "mpv playback: loading failed (-13)", true)
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()
		if err := tt.Click("错误详情"); err != nil {
			t.Fatal(err)
		}
		if !tt.HasText("mpv playback: loading failed (-13)") {
			t.Fatal("details did not expand")
		}
		tt.Key(0, ui.KeyEscape)
		if tt.HasText("mpv playback: loading failed (-13)") || a.player == nil {
			t.Fatalf("Esc should collapse details without leaving player: texts=%v", tt.Texts())
		}

		// Expand again, then switch to episode 15 which fails -> details must reset to collapsed.
		if err := tt.Click("错误详情"); err != nil {
			t.Fatal(err)
		}
		a.title.session = &recoverySession{play: func(_ context.Context, _ string, _ catalog.Episode) (catalog.Playback, error) {
			return catalog.Playback{}, errors.New("第15集失效")
		}}
		a.playEpisode("bfzym3u8", a.title.detail.Sources[0].Episodes[14])
		tt.Frame()
		if tt.HasText("第15集失效") {
			t.Fatalf("switching episode did not reset expanded details: %v", tt.Texts())
		}
		if !tt.HasText("第15集") {
			t.Fatalf("switched episode failure missing new episode label: %v", tt.Texts())
		}
	})
}

func TestPlayerFailureOneClickSourceSwitch10CandidatesAndPanelLifecycle(t *testing.T) {
	resumeAt := 26*time.Minute + 49*time.Second

	for _, tc := range []struct {
		name           string
		w, h           int
		panelStartOpen bool
		wantOpenAfter  bool
	}{
		{"desktop-panel-closed-auto-closes", 1280, 720, false, false},
		{"desktop-panel-manual-open-stays-open", 1280, 720, true, true},
		{"small-640x420-panel-closed-auto-closes", 640, 420, false, false},
		{"small-640x420-panel-manual-open-stays-open", 640, 420, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, resumeAt, "mpv playback: loading failed (-13)", true)
			// Simulate old episode still on screen while episode 14 failed with unapplied resumeAt.
			a.playingEpisode = "第1集"
			a.resumeAt = 0
			a.lastPosition = 5 * time.Second
			a.panelOpen = tc.panelStartOpen
			a.panelTouched = true

			tt := ui.NewTester(a.view, tc.w, tc.h)
			tt.SetTitleBar(dragBar)
			tt.Frame()
			tt.Advance(350 * time.Millisecond)

			// Context displays the failed episode & resume position, NOT old 第1集 / 00:05.
			if !tt.HasText("第14集") || !tt.HasText("已看到 26:49") {
				t.Fatalf("failure context should show failed episode 14 at 26:49, got: %v", tt.Texts())
			}

			// One click on 换来源 opens panel AND expands the 10-source menu immediately.
			if err := tt.Click("换来源"); err != nil {
				t.Fatalf("click 换来源: %v; texts=%v", err, tt.Texts())
			}
			tt.Advance(350 * time.Millisecond)

			menu := tt.Menu()
			if len(menu) != 10 || menu[9] != "示例来源 J" {
				t.Fatalf("one-click 换来源 did not open 10-source menu: menu=%v panelOpen=%v", menu, a.panelOpen)
			}
			panelRect, okPanel := tt.Find("悬浮选集面板")
			srcRect, okSrc := tt.Find("选择来源")
			menuRect, okMenu := tt.Find("来源菜单")
			if !okPanel || !okSrc || !okMenu || menuRect.H <= 0 {
				t.Fatalf("missing bounds after 换来源: panel=%+v(%v) src=%+v(%v) menu=%+v(%v)", panelRect, okPanel, srcRect, okSrc, menuRect, okMenu)
			}
			if menuRect.Y < srcRect.Y+srcRect.H-0.5 {
				t.Fatalf("source menu overlapped trigger: src=%+v menu=%+v", srcRect, menuRect)
			}
			if menuRect.Y+menuRect.H > panelRect.Y+panelRect.H+0.5 {
				t.Fatalf("source menu bottom %.2f exceeded floating panel bottom %.2f (panel=%+v src=%+v menu=%+v)",
					menuRect.Y+menuRect.H, panelRect.Y+panelRect.H, panelRect, srcRect, menuRect)
			}
			t.Logf("%s: panelBottom=%.2f srcBottom=%.2f menu=%.2f..%.2f (h=%.2f)",
				tc.name, panelRect.Y+panelRect.H, srcRect.Y+srcRect.H, menuRect.Y, menuRect.Y+menuRect.H, menuRect.H)

			// Escape closes ONLY the source menu first, keeping panel open.
			tt.Key(0, ui.KeyEscape)
			if len(tt.Menu()) != 0 || !a.panelOpen {
				t.Fatalf("Esc did not close only source menu: menu=%v panelOpen=%v", tt.Menu(), a.panelOpen)
			}

			// Re-open with one click on 换来源 again, scroll inside menu to 10th source, and click 示例来源 J.
			if err := tt.Click("换来源"); err != nil {
				t.Fatalf("second click 换来源: %v", err)
			}
			tt.Advance(200 * time.Millisecond)
			if len(tt.Menu()) != 10 {
				t.Fatalf("re-clicking 换来源 with panel open did not expand menu: menu=%v", tt.Menu())
			}

			itemB, okB := tt.Find("示例来源 B")
			if !okB || itemB.H <= 0 {
				t.Fatalf("missing 示例来源 B inside menu: %+v", itemB)
			}
			tt.Scroll(itemB.X+itemB.W/2, itemB.Y+itemB.H/2, 0, 600)
			itemJ, okJ := tt.Find("示例来源 J")
			if !okJ || itemJ.H <= 0 {
				tt.Scroll(itemB.X+itemB.W/2, itemB.Y+itemB.H/2, 0, -1200)
				itemJ, okJ = tt.Find("示例来源 J")
			}
			if !okJ || itemJ.H <= 0 {
				t.Fatalf("10th source 示例来源 J not scrolled into clickable view: itemJ=%+v", itemJ)
			}
			tt.ClickAt(itemJ.X+itemJ.W/2, itemJ.Y+itemJ.H/2)
			tt.Advance(350 * time.Millisecond)

			if a.playFailure != nil {
				t.Fatalf("recovery did not clear playFailure: %+v", a.playFailure)
			}
			if a.title.item.Entry.Label() != "示例来源 J" {
				t.Fatalf("source = %q, want 示例来源 J", a.title.item.Entry.Label())
			}
			if a.playingEpisode != "第14集" {
				t.Fatalf("playingEpisode = %q, want 第14集", a.playingEpisode)
			}
			if a.playbackPosition() != resumeAt {
				t.Fatalf("resume position = %s, want %s", a.playbackPosition(), resumeAt)
			}
			if a.panelOpen != tc.wantOpenAfter {
				t.Fatalf("panelOpen = %v, want %v", a.panelOpen, tc.wantOpenAfter)
			}
		})
	}
}

func TestPlayerFailureSwitchSourceUnmatchedAndLineFailureKeepPanelOpen(t *testing.T) {
	resumeAt := 26*time.Minute + 49*time.Second

	t.Run("unmatched-episode-keeps-panel-for-manual-pick", func(t *testing.T) {
		a, _ := newFailureFixture(t, 2, []string{"bfzym3u8"}, 60, 14, resumeAt, "原线路失效", true)
		unmatchedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"list":[{"vod_name":"示例剧集","vod_play_from":"特别线","vod_play_url":"特别篇$/special.mp4"}]}`)
		}))
		t.Cleanup(unmatchedSrv.Close)
		a.title.seeds[1].Entry.Site.API = unmatchedSrv.URL + "/api"

		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()

		if err := tt.Click("换来源"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("示例来源 B"); err != nil {
			t.Fatal(err)
		}
		tt.Advance(250 * time.Millisecond)

		if !a.panelOpen || !tt.HasText("请选择要播放的分集") || !tt.HasText("特别篇") {
			t.Fatalf("unmatched source did not keep panel open for manual episode pick: open=%v texts=%v", a.panelOpen, tt.Texts())
		}
		if err := tt.Click("特别篇"); err != nil {
			t.Fatalf("pick unmatched episode 特别篇: %v", err)
		}
		tt.Advance(250 * time.Millisecond)
		if a.playFailure != nil || a.playingEpisode != "特别篇" || a.title.item.Entry.Label() != "示例来源 B" || a.panelOpen {
			t.Fatalf("manual episode recovery failed or did not collapse temporary panel: failure=%+v ep=%q src=%q open=%v",
				a.playFailure, a.playingEpisode, a.title.item.Entry.Label(), a.panelOpen)
		}
	})

	t.Run("candidate-line-failure-keeps-panel-for-backup-line", func(t *testing.T) {
		a, _ := newFailureFixture(t, 2, []string{"bfzym3u8"}, 60, 14, resumeAt, "原线路失效", true)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"list":[{"vod_name":"示例剧集","vod_play_from":"主线$$$备用线","vod_play_url":"第14集$/broken.mp4$$$第14集$/ok.mp4"}]}`)
		}))
		t.Cleanup(srv.Close)
		a.title.seeds[1].Entry.Site.API = srv.URL + "/api"
		origOpen := a.open
		a.open = func(raw string) (*ui.VideoPlayer, error) {
			if strings.Contains(raw, "broken.mp4") {
				return nil, errors.New("主线已失效")
			}
			return origOpen(raw)
		}

		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()

		if err := tt.Click("换来源"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("示例来源 B"); err != nil {
			t.Fatal(err)
		}
		tt.Advance(250 * time.Millisecond)

		if !a.panelOpen || !tt.HasText("主线已失效") || !tt.HasText("备用线") {
			t.Fatalf("failed candidate line did not keep panel open with backup line: open=%v texts=%v", a.panelOpen, tt.Texts())
		}
		if err := tt.Click("备用线"); err != nil {
			t.Fatalf("click backup line 备用线: %v", err)
		}
		tt.Advance(250 * time.Millisecond)
		if a.playFailure != nil || a.title.item.Entry.Label() != "示例来源 B" || a.playingSource != "备用线" || a.playingEpisode != "第14集" || a.playbackPosition() != resumeAt {
			t.Fatalf("backup line recovery failed: failure=%+v src=%q line=%q ep=%q at=%s",
				a.playFailure, a.title.item.Entry.Label(), a.playingSource, a.playingEpisode, a.playbackPosition())
		}
		if a.panelOpen {
			t.Fatal("temporary recovery panel did not collapse after recovering on backup line")
		}
	})
}

func TestPlayerFailureInPlaceRetryDedupRefreshAndStaleProtection(t *testing.T) {
	resumeAt := 26*time.Minute + 49*time.Second
	a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, resumeAt, "mpv playback: loading failed (-13)", true)

	// Gate async jobs via dispatch + channels.
	type req struct {
		done chan struct{}
		err  error
	}
	requests := make(chan req, 4)
	dispatchCh := make(chan func(), 16)
	a.dispatch = func(fn func()) { dispatchCh <- fn }

	var attempt atomic.Int32
	a.title.session = &recoverySession{play: func(ctx context.Context, _ string, ep catalog.Episode) (catalog.Playback, error) {
		n := attempt.Add(1)
		r := req{done: make(chan struct{})}
		if n == 1 {
			r.err = errors.New("HTTP 503 上游临时不可用")
		}
		requests <- r
		select {
		case <-r.done:
		case <-ctx.Done():
			return catalog.Playback{}, ctx.Err()
		}
		if r.err != nil {
			return catalog.Playback{}, r.err
		}
		return catalog.Playback{URL: ep.ID}, nil
	}}
	// Clear Video.ID so loadEpisode exercises session.Play directly in this async test.
	a.title.item.Video.ID = ""

	tt := ui.NewTester(a.view, 1280, 720)
	tt.SetTitleBar(dragBar)
	tt.Frame()

	// Expand details and copy before retrying.
	if err := tt.Click("错误详情"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("复制"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("已复制") {
		t.Fatal("expected 已复制 before retry")
	}

	// Click 重试 twice rapidly: only ONE effective request is launched.
	if err := tt.Click("重试"); err != nil {
		t.Fatalf("first click 重试: %v", err)
	}
	_ = tt.Click("重试")
	a.retryEpisode() // direct call must also dedup while in flight

	var r1 req
	select {
	case r1 = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not start request")
	}
	if attempt.Load() != 1 {
		t.Fatalf("rapid retry produced %d requests, want 1", attempt.Load())
	}

	// In-place retrying state: keeps card, keeps expanded details, shows 正在重试当前线路….
	tt.Frame()
	if !tt.HasText("正在重试当前线路…") {
		t.Fatalf("missing in-place retry heading: %v", tt.Texts())
	}
	if !tt.HasText("mpv playback: loading failed (-13)") {
		t.Fatalf("expanded details collapsed during in-place retry: %v", tt.Texts())
	}

	// Finish first retry with failure -> heading updates to 重试后仍然播不了, details stay open with new error.
	close(r1.done)
	select {
	case fn := <-dispatchCh:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("first retry finish did not arrive")
	}
	tt.Frame()
	if !tt.HasText("重试后仍然播不了") {
		t.Fatalf("retry failure heading missing: %v", tt.Texts())
	}
	if !tt.HasText("HTTP 503 上游临时不可用") {
		t.Fatalf("expanded details did not update to latest error: %v", tt.Texts())
	}
	if tt.HasText("已复制") {
		t.Fatalf("copied state was not reset after new error: %v", tt.Texts())
	}

	// Second retry succeeds -> failure card closes and resumes at 26:49 on 第14集.
	if err := tt.Click("重试"); err != nil {
		t.Fatalf("second click 重试: %v", err)
	}
	var r2 req
	select {
	case r2 = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("second retry did not start")
	}
	close(r2.done)
	select {
	case fn := <-dispatchCh:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("second retry finish did not arrive")
	}
	tt.Frame()
	if a.playFailure != nil || tt.HasText("重试后仍然播不了") || tt.HasText("这条线路播不了") {
		t.Fatalf("successful retry did not close failure card: failure=%+v texts=%v", a.playFailure, tt.Texts())
	}
	if a.playingEpisode != "第14集" || a.playbackPosition() != resumeAt {
		t.Fatalf("successful retry lost episode/position: ep=%q at=%s", a.playingEpisode, a.playbackPosition())
	}

	// Stale late retry result after switching episode must NOT overwrite episode 15.
	a.playFailure = &episodeFailure{
		line:    "bfzym3u8",
		episode: a.title.detail.Sources[0].Episodes[13],
		at:      resumeAt,
		err:     errors.New("再次失效"),
	}
	tt.Frame()
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	var r3 req
	select {
	case r3 = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("third retry did not start")
	}
	// Switch to episode 15 while old retry is still pending.
	a.playEpisode("bfzym3u8", a.title.detail.Sources[0].Episodes[14])
	var r4 req
	select {
	case r4 = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("episode 15 request did not start")
	}
	close(r4.done)
	close(r3.done)
	for range 2 {
		select {
		case fn := <-dispatchCh:
			fn()
		case <-time.After(2 * time.Second):
			t.Fatal("pending job did not finish")
		}
	}
	if a.playingEpisode != "第15集" || a.playFailure != nil {
		t.Fatalf("late retry result overwrote switched episode: ep=%q failure=%+v", a.playingEpisode, a.playFailure)
	}

	// Stale late retry result after switching source must NOT overwrite new source.
	a.playFailure = &episodeFailure{
		line:    "bfzym3u8",
		episode: a.title.detail.Sources[0].Episodes[14],
		at:      resumeAt,
		err:     errors.New("换源前失效"),
	}
	tt.Frame()
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	var rSwitch req
	select {
	case rSwitch = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("retry before source switch did not start")
	}
	// Switch to another source while the old retry is still in flight.
	a.switchSource(a.title.seeds[1])
	close(rSwitch.done)
	// Drain all queued dispatches (late canceled retry + detail load + play resolve).
	deadline := time.After(3 * time.Second)
	for a.title.item.Entry.Label() != "示例来源 B" || a.running(jobPlay) {
		select {
		case fn := <-dispatchCh:
			fn()
		case <-deadline:
			t.Fatalf("source switch did not complete: src=%q running=%v failure=%+v",
				a.title.item.Entry.Label(), a.running(jobPlay), a.playFailure)
		}
	}
	// Drain any remaining late callback from the canceled retry.
	for len(dispatchCh) > 0 {
		(<-dispatchCh)()
	}
	if a.title.item.Entry.Label() != "示例来源 B" || a.playFailure != nil || a.playingEpisode != "第15集" || a.playbackPosition() != resumeAt {
		t.Fatalf("late retry overwrote source switch: src=%q ep=%q at=%s failure=%+v",
			a.title.item.Entry.Label(), a.playingEpisode, a.playbackPosition(), a.playFailure)
	}

	// Stale late retry result after leaving player must NOT restore player or failure.
	a.playFailure = &episodeFailure{
		line:    a.playingSource,
		episode: a.title.detail.Sources[0].Episodes[14],
		at:      10 * time.Second,
		err:     errors.New("离开前失效"),
	}
	a.title.item.Video.ID = ""
	a.title.session = &recoverySession{play: func(ctx context.Context, _ string, ep catalog.Episode) (catalog.Playback, error) {
		r := req{done: make(chan struct{}), err: errors.New("迟到的失败")}
		requests <- r
		select {
		case <-r.done:
		case <-ctx.Done():
			return catalog.Playback{}, ctx.Err()
		}
		return catalog.Playback{}, r.err
	}}
	tt.Frame()
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	var r5 req
	select {
	case r5 = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("fifth retry did not start")
	}
	a.leavePlayer()
	close(r5.done)
	select {
	case fn := <-dispatchCh:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("canceled retry did not flush")
	}
	if a.player != nil || a.playFailure != nil {
		t.Fatalf("late retry result corrupted state after leave: player=%v failure=%+v", a.player, a.playFailure)
	}
}

func TestPlayerFailureRetryRefreshesExpiredCMSFromPlayer(t *testing.T) {
	var detailReads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			if detailReads.Add(1) == 1 {
				fmt.Fprint(w, `{"list":[{"vod_name":"示例剧集","vod_play_from":"主线","vod_play_url":"第14集$/expired.m3u8"}]}`)
				return
			}
			fmt.Fprint(w, `{"list":[{"vod_name":"示例剧集","vod_play_from":"主线","vod_play_url":"第14集$/fresh.m3u8"}]}`)
		case "/fresh.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\nseg.ts\n#EXT-X-ENDLIST\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	item := library.Item{
		Entry: library.Entry{Site: config.Site{Key: "cms", Name: "示例来源 A", Type: 1, Kind: config.KindJSON, API: upstream.URL + "/api"}},
		Video: catalog.Video{ID: "14", Name: "示例剧集"},
	}
	session, detail, err := openItem(t.Context(), item, "")
	if err != nil {
		t.Fatal(err)
	}
	resumeAt := 12*time.Minute + 34*time.Second
	a := &app{
		player:           &unknownSizeMediaPlayer{pos: resumeAt, dur: 45 * time.Minute, rate: 1, vol: 0.7},
		playingSource:    "主线",
		playingEpisode:   "第14集",
		resumeAt:         resumeAt,
		lastPosition:     resumeAt,
		nav:              []routeKind{routeHome, routeDetail},
		title:            &titlePage{name: "示例剧集", item: item, seeds: []library.Item{item}, session: session, detail: &detail, failed: map[string]string{}},
		newDanmakuClient: func(string) (*danmaku.Client, error) { return nil, errors.New("offline test fixture") },
	}
	defer a.close()
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		resp, err := http.Get(raw)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, errors.New(resp.Status)
		}
		return ui.NewVideoPlayer(newCinema16x9Video(1280, 720, 45*time.Minute))
	}
	a.playFailure = &episodeFailure{
		line:    "主线",
		episode: detail.Sources[0].Episodes[0],
		at:      resumeAt,
		err:     errors.New("无法打开：404 Not Found"),
	}

	tt := ui.NewTester(a.view, 1280, 720)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	if detailReads.Load() != 2 || a.playFailure != nil || a.playingEpisode != "第14集" || a.playbackPosition() != resumeAt {
		t.Fatalf("player retry did not refresh CMS URL and keep resumeAt: reads=%d failure=%+v ep=%q at=%s",
			detailReads.Load(), a.playFailure, a.playingEpisode, a.playbackPosition())
	}
}

func TestPlayerFailureSingleSourceAuthLineAndSearchActions(t *testing.T) {
	t.Run("single-source-no-change-source", func(t *testing.T) {
		a, _ := newFailureFixture(t, 1, []string{"bfzym3u8"}, 14, 5, 90*time.Second, "mpv playback: failed", true)
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()
		if tt.HasText("换来源") {
			t.Fatalf("single source should not show 换来源: %v", tt.Texts())
		}
		if !tt.HasText("重试") {
			t.Fatalf("single source missing 重试: %v", tt.Texts())
		}
	})

	t.Run("pan-auth-preserved", func(t *testing.T) {
		a, _ := newFailureFixture(t, 2, []string{"夸克原画"}, 14, 3, 45*time.Second, "需要登录夸克网盘", true)
		a.playFailure.authProvider = pan.ProviderQuark
		loginCalled := false
		a.startPanLogin = func(provider pan.Provider, onSuccess func()) {
			loginCalled = provider == pan.ProviderQuark
			if onSuccess != nil {
				onSuccess()
			}
		}
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()
		if !tt.HasText("需要登录夸克网盘") || !tt.HasText("登录夸克网盘") {
			t.Fatalf("pan auth heading/button missing: %v", tt.Texts())
		}
		if err := tt.Click("登录夸克网盘"); err != nil {
			t.Fatal(err)
		}
		if !loginCalled || a.playFailure != nil || a.playingEpisode != "第3集" || a.playbackPosition() != 45*time.Second {
			t.Fatalf("pan login recovery failed: called=%v failure=%+v ep=%q at=%s",
				loginCalled, a.playFailure, a.playingEpisode, a.playbackPosition())
		}
	})

	t.Run("multiple-lines-switch-preserved", func(t *testing.T) {
		a, _ := newFailureFixture(t, 1, []string{"主线", "备用线"}, 14, 4, 80*time.Second, "主线超时", true)
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()
		if err := tt.Click("换下一条线路"); err != nil {
			t.Fatalf("click 换下一条线路: %v", err)
		}
		if a.playFailure != nil || a.playingSource != "备用线" || a.playingEpisode != "第4集" || a.playbackPosition() != 80*time.Second {
			t.Fatalf("line switch failed: failure=%+v line=%q ep=%q at=%s",
				a.playFailure, a.playingSource, a.playingEpisode, a.playbackPosition())
		}
	})

	t.Run("search-other-sources-preserved", func(t *testing.T) {
		a, _ := newFailureFixture(t, 1, []string{"主线"}, 14, 2, 30*time.Second, "单源超时", true)
		a.library = libraryBrowser{root: a.title.item.Entry.Config, cached: map[string]*config.Config{}}
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()
		if !tt.HasText("查找其他来源") {
			t.Fatalf("missing 查找其他来源 button: %v", tt.Texts())
		}
	})

	t.Run("keyboard-operates-buttons-and-back-works", func(t *testing.T) {
		a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, 26*time.Minute+49*time.Second, "mpv playback: loading failed (-13)", true)
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()

		// Tab until 错误详情 has focus, then press Enter to expand.
		foundDetailsFocus := false
		for range 30 {
			tt.Key(0, ui.KeyTab)
			if tt.Focused("错误详情") {
				foundDetailsFocus = true
				break
			}
		}
		if !foundDetailsFocus {
			t.Fatal("could not reach 错误详情 via Tab key")
		}
		tt.Key(0, ui.KeyEnter)
		if !tt.HasText("mpv playback: loading failed (-13)") {
			t.Fatalf("Enter on focused 错误详情 did not expand details: %v", tt.Texts())
		}
		// Back button leaves player cleanly even when failure card is visible.
		if err := tt.Click("返回"); err != nil {
			t.Fatalf("click 返回 from failure state: %v", err)
		}
		if a.player != nil || a.playFailure != nil {
			t.Fatalf("returning from player left failure state: player=%v failure=%+v", a.player, a.playFailure)
		}
	})
}

func TestPlayerFailureNativeReviewSnapshots(t *testing.T) {
	outDir := os.Getenv("XIAOMU_FAILURE_REVIEW_DIR")
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
	attachCurve := func(a *app) {
		a.preferences.Danmaku.Enabled = true
		dur := 46*time.Minute + 17*time.Second
		comments := make([]danmaku.Comment, 0, 120)
		for i := range 120 {
			sec := float64(i) * dur.Seconds() / 120.0
			comments = append(comments, danmaku.Comment{
				Time:  time.Duration(sec * float64(time.Second)),
				Mode:  1,
				Color: 0xffffff,
				Text:  fmt.Sprintf("示例弹幕 %d", i+1),
			})
		}
		a.applyDanmaku(danmaku.Episode{ID: "demo", Anime: "示例剧集", Name: "第14集"}, comments)
	}

	resumeAt := 26*time.Minute + 49*time.Second
	const rawErr = "mpv playback: loading failed (-13)"

	// 1. Desktop 1280x720 (unknown video size, matching prototype preview.png / details.png):
	//    default collapsed, details expanded, one-click source switch open.
	a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, resumeAt, rawErr, true)
	attachCurve(a)
	tt := ui.NewTester(a.view, 1280, 720)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	tt.Advance(350 * time.Millisecond)
	save("native-1280x720-default.png", tt)

	if err := tt.Click("错误详情"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	save("native-1280x720-details.png", tt)

	// Collapse details back before capturing one-click source switch state.
	if err := tt.Click("错误详情"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("换来源"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(350 * time.Millisecond)
	if len(tt.Menu()) != 10 {
		t.Fatalf("1280x720 one-click 换来源 menu count = %d, want 10", len(tt.Menu()))
	}
	save("native-1280x720-source-menu.png", tt)

	if err := tt.ChooseMenuItem("示例来源 J"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(350 * time.Millisecond)
	if a.playFailure != nil || a.title.item.Entry.Label() != "示例来源 J" || a.playingEpisode != "第14集" || a.playbackPosition() != resumeAt || a.panelOpen {
		t.Fatalf("1280x720 switch verification failed: source=%q episode=%q resumeAt=%s panelOpen=%v failure=%+v",
			a.title.item.Entry.Label(), a.playingEpisode, a.playbackPosition(), a.panelOpen, a.playFailure)
	}
	t.Logf("1280x720 switch verified: source=%q episode=%q resumeAt=%s panelOpen=%v",
		a.title.item.Entry.Label(), a.playingEpisode, a.playbackPosition(), a.panelOpen)

	// 2. Small window 640x420: default, details expanded, one-click source switch open.
	aSmall, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, resumeAt, rawErr, true)
	ttSmall := ui.NewTester(aSmall.view, 640, 420)
	ttSmall.SetTitleBar(dragBar)
	ttSmall.Frame()
	ttSmall.Advance(350 * time.Millisecond)
	save("native-640x420-default.png", ttSmall)

	if err := ttSmall.Click("错误详情"); err != nil {
		t.Fatal(err)
	}
	ttSmall.Advance(250 * time.Millisecond)
	save("native-640x420-details.png", ttSmall)

	// Collapse details before one-click 换来源 so the small-window screenshot reflects default -> 换来源.
	if err := ttSmall.Click("错误详情"); err != nil {
		t.Fatal(err)
	}
	if err := ttSmall.Click("换来源"); err != nil {
		t.Fatal(err)
	}
	ttSmall.Advance(350 * time.Millisecond)
	if len(ttSmall.Menu()) != 10 {
		t.Fatalf("640x420 one-click 换来源 menu count = %d, want 10", len(ttSmall.Menu()))
	}
	save("native-640x420-source-menu.png", ttSmall)

	if err := ttSmall.ChooseMenuItem("示例来源 J"); err != nil {
		t.Fatal(err)
	}
	ttSmall.Advance(350 * time.Millisecond)
	if aSmall.playFailure != nil || aSmall.title.item.Entry.Label() != "示例来源 J" || aSmall.playingEpisode != "第14集" || aSmall.playbackPosition() != resumeAt || aSmall.panelOpen {
		t.Fatalf("640x420 switch verification failed: source=%q episode=%q resumeAt=%s panelOpen=%v failure=%+v",
			aSmall.title.item.Entry.Label(), aSmall.playingEpisode, aSmall.playbackPosition(), aSmall.panelOpen, aSmall.playFailure)
	}
	t.Logf("640x420 switch verified: source=%q episode=%q resumeAt=%s panelOpen=%v",
		aSmall.title.item.Entry.Label(), aSmall.playingEpisode, aSmall.playbackPosition(), aSmall.panelOpen)
}

func TestPlayerFailureDisplaysFailedRouteAndTracksSourceAndLineSwitches(t *testing.T) {
	t.Run("format-route-label", func(t *testing.T) {
		entry := library.Entry{
			Line: "主仓线路",
			Site: config.Site{Key: "s1", Name: "示例站点"},
		}
		for _, tc := range []struct {
			name     string
			entry    library.Entry
			line     string
			multiple bool
			want     string
		}{
			{"distinct-line", entry, "极速m3u8", false, "主仓线路 · 示例站点 · 极速m3u8"},
			{"duplicate-site-name", entry, "示例站点", false, "主仓线路 · 示例站点"},
			{"single-generic-line", entry, "线路", false, "主仓线路 · 示例站点"},
			{"multiple-with-generic-line", entry, "线路", true, "主仓线路 · 示例站点 · 线路"},
			{"line-only", library.Entry{}, "bfzym3u8", false, "bfzym3u8"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := formatRouteLabel(tc.entry, tc.line, tc.multiple); got != tc.want {
					t.Fatalf("formatRouteLabel() = %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("collapsed-card-shows-failed-route-immediately", func(t *testing.T) {
		a, _ := newFailureFixture(t, 10, []string{"bfzym3u8"}, 60, 14, 26*time.Minute+49*time.Second, "mpv playback: loading failed (-13)", true)
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()
		if !tt.HasText("示例来源 A · bfzym3u8") {
			t.Fatalf("collapsed failure card should display failed source and line without expanding details: %v", tt.Texts())
		}
		if tt.HasText("mpv playback: loading failed (-13)") {
			t.Fatalf("raw error should remain collapsed by default: %v", tt.Texts())
		}
	})

	t.Run("switch-to-another-failing-source-updates-card-route-retry-and-next-line", func(t *testing.T) {
		resumeAt := 26*time.Minute + 49*time.Second
		a, _ := newFailureFixture(t, 2, []string{"bfzym3u8"}, 60, 14, resumeAt, "A来源原线路失效", true)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"list":[{"vod_name":"示例剧集","vod_play_from":"主线$$$备用线","vod_play_url":"第14集$/broken-b.mp4$$$第14集$/ok-b.mp4"}]}`)
		}))
		t.Cleanup(srv.Close)
		a.title.seeds[1].Entry.Site.API = srv.URL + "/api"

		var brokenOpens atomic.Int32
		origOpen := a.open
		a.open = func(raw string) (*ui.VideoPlayer, error) {
			if strings.Contains(raw, "broken-b.mp4") {
				n := brokenOpens.Add(1)
				return nil, fmt.Errorf("B来源主线失效 #%d", n)
			}
			return origOpen(raw)
		}

		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()

		// Initially displays Source A · bfzym3u8.
		if !tt.HasText("示例来源 A · bfzym3u8") {
			t.Fatalf("initial card missing Source A route: %v", tt.Texts())
		}

		// Switch to Source B whose 主线 also fails.
		if err := tt.Click("换来源"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("示例来源 B"); err != nil {
			t.Fatal(err)
		}
		tt.Advance(250 * time.Millisecond)

		// Center failure card now clearly shows Source B · 主线 (not stale Source A).
		if !tt.HasText("示例来源 B · 主线") {
			t.Fatalf("failure card did not update to failed switched source/line: %v", tt.Texts())
		}
		if _, ok := tt.Find("当前线路 示例来源 B · 主线"); !ok {
			t.Fatalf("failure route badge did not update to 示例来源 B · 主线: texts=%v", tt.Texts())
		}
		// Right panel should not claim Source A is "仍在播放" while Source A itself failed.
		if tt.HasText("仍在播放：") {
			t.Fatalf("panel should not claim failed source is still playing: %v", tt.Texts())
		}
		// Expanding details shows Source B's error, not Source A's.
		if err := tt.Click("错误详情"); err != nil {
			t.Fatal(err)
		}
		if !tt.HasText("B来源主线失效 #1") || tt.HasText("A来源原线路失效") {
			t.Fatalf("expanded details did not show Source B error: %v", tt.Texts())
		}

		// Clicking 重试 retries Source B · 主线 (not Source A), updating error to #2.
		if err := tt.Click("重试"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if brokenOpens.Load() != 2 {
			t.Fatalf("retry on failed switched source opened broken-b %d times, want 2", brokenOpens.Load())
		}
		if !tt.HasText("重试后仍然播不了") || !tt.HasText("B来源主线失效 #2") {
			t.Fatalf("retry of failed switched source did not update heading/error: %v", tt.Texts())
		}

		// Clicking 换下一条线路 advances Source B from 主线 to 备用线 and recovers playback.
		if err := tt.Click("换下一条线路"); err != nil {
			t.Fatalf("click 换下一条线路 on failed switched source: %v; texts=%v", err, tt.Texts())
		}
		tt.Advance(250 * time.Millisecond)
		if a.playFailure != nil || a.title.item.Entry.Label() != "示例来源 B" || a.playingSource != "备用线" || a.playbackPosition() != resumeAt {
			t.Fatalf("换下一条线路 did not recover on Source B backup line: failure=%+v src=%q line=%q at=%s",
				a.playFailure, a.title.item.Entry.Label(), a.playingSource, a.playbackPosition())
		}
	})

	t.Run("switch-succeeds-then-new-source-fails-playback-shows-new-route", func(t *testing.T) {
		resumeAt := 26*time.Minute + 49*time.Second
		a, _ := newFailureFixture(t, 2, []string{"bfzym3u8"}, 60, 14, resumeAt, "A来源失效", true)
		tt := ui.NewTester(a.view, 1280, 720)
		tt.SetTitleBar(dragBar)
		tt.Frame()

		if !tt.HasText("示例来源 A · bfzym3u8") {
			t.Fatalf("initial card missing Source A route: %v", tt.Texts())
		}

		// Switch to Source B (whose 高清 opens successfully).
		if err := tt.Click("换来源"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("示例来源 B"); err != nil {
			t.Fatal(err)
		}
		tt.Advance(250 * time.Millisecond)
		if a.title.item.Entry.Label() != "示例来源 B" || a.playFailure != nil {
			t.Fatalf("switch to Source B did not succeed: src=%q failure=%+v", a.title.item.Entry.Label(), a.playFailure)
		}

		// Simulate async libmpv playback failure on Source B.
		a.player = &unknownSizeMediaPlayer{pos: resumeAt, dur: 46 * time.Minute, rate: 1, vol: 0.7, err: errors.New("mpv playback: loading failed (-13)")}
		tt.Frame()
		if !tt.HasText("这条线路播不了") || !tt.HasText("示例来源 B · 高清") {
			t.Fatalf("async playback failure on switched source did not display new route: %v", tt.Texts())
		}
	})

	t.Run("long-route-name-stays-within-card-bounds", func(t *testing.T) {
		for _, size := range [][2]int{{1280, 720}, {640, 420}} {
			a, _ := newFailureFixture(t, 2, []string{"超长线路标识-极速蓝光4K-HDR-备用节点"}, 60, 14, 90*time.Second, "mpv playback: failed", true)
			a.title.item.Entry.Line = "超长多仓主线路名称示例"
			a.title.item.Entry.Site.Name = "超长影视片源站点名称示例"
			tt := ui.NewTester(a.view, size[0], size[1])
			tt.SetTitleBar(dragBar)
			tt.Frame()
			tt.Advance(250 * time.Millisecond)

			card, okC := tt.Find("播放失败提示")
			badge, okB := tt.Find("当前线路 超长多仓主线路名称示例 · 超长影视片源站点名称示例 · 超长线路标识-极速蓝光4K-HDR-备用节点")
			if !okC || !okB {
				t.Fatalf("%dx%d missing card(%v) or route badge(%v): texts=%v", size[0], size[1], okC, okB, tt.Texts())
			}
			if badge.X+badge.W > card.X+card.W+1 {
				t.Fatalf("%dx%d long route badge overflowed card: badge=%+v card=%+v", size[0], size[1], badge, card)
			}
		}
	})
}
