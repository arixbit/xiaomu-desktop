package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/ui"
)

func TestSeriesSkipRuleRespectsResumeAndShortEpisodes(t *testing.T) {
	rule := seriesSkipRule{Enabled: true, Opening: 90, Ending: 120}
	for _, tc := range []struct {
		name                             string
		position, resume, duration, want time.Duration
	}{
		{"new episode", 0, 0, 30 * time.Minute, 90 * time.Second},
		{"resume inside opening", 0, 45 * time.Second, 30 * time.Minute, 90 * time.Second},
		{"resume after opening", 0, 5 * time.Minute, 30 * time.Minute, 0},
		{"already past opening", 2 * time.Minute, 0, 30 * time.Minute, 0},
		{"unknown duration", 0, 0, 0, 0},
		{"short video", 0, 0, 3 * time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rule.openingTarget(tc.position, tc.resume, tc.duration); got != tc.want {
				t.Fatalf("opening target = %s, want %s", got, tc.want)
			}
		})
	}
	if rule.inEnding(27*time.Minute, 30*time.Minute) || !rule.inEnding(28*time.Minute, 30*time.Minute) || rule.inEnding(time.Minute, time.Minute) {
		t.Fatal("credits boundary or short-video guard is incorrect")
	}
	rule.Enabled = false
	if rule.inEnding(29*time.Minute, 30*time.Minute) || rule.openingTarget(0, 0, 30*time.Minute) != 0 {
		t.Fatal("disabled rule still skips")
	}
}

func TestSeriesSkipPersistenceAndSaveFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "series-skips.json")
	s := seriesSkipStore{path: path}
	want := seriesSkipRule{Enabled: true, Opening: 90, Ending: 120}
	if err := s.set(" 《示例剧》 ", want); err != nil {
		t.Fatal(err)
	}
	entries, err := readSeriesSkips(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := seriesSkipStore{entries: entries}
	if got := restarted.get("示例剧"); got != want || restarted.get("其他剧").Enabled {
		t.Fatalf("settings leaked or were lost: %+v", got)
	}
	s.path = t.TempDir()
	if err := s.set("示例剧", seriesSkipRule{}); err == nil || s.get("示例剧") != want {
		t.Fatal("failed save changed live settings")
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = path
	s.entries, s.readErr = readSeriesSkips(path)
	if err := s.set("示例剧", want); err == nil {
		t.Fatal("corrupt settings were overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{" {
		t.Fatal("corrupt file changed")
	}
}

func TestOpeningSkipOncePerEpisodeAndCanBeDisabled(t *testing.T) {
	a := newKeyboardTestApp(t)
	if err := a.seriesSkips.set(a.title.name, seriesSkipRule{Enabled: true, Opening: 10}); err != nil {
		t.Fatal(err)
	}
	a.player.Pause()
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	if a.player.Position() != 10*time.Second {
		t.Fatalf("opening was not skipped: %s", a.player.Position())
	}
	a.player.Seek(0)
	tt.Frame()
	if a.player.Position() != 0 {
		t.Fatal("manually rewinding triggered another skip")
	}
	if err := a.seriesSkips.set(a.title.name, seriesSkipRule{Opening: 10}); err != nil {
		t.Fatal(err)
	}
	vp, err := ui.NewVideoPlayer(&timedVideo{})
	if err != nil {
		t.Fatal(err)
	}
	p := wrapSystemPlayer(vp)
	a.player.Close()
	a.player = p
	p.Pause()
	tt.Frame()
	if p.Position() != 0 {
		t.Fatal("disabled setting still skipped a new episode")
	}
}

func TestEndingSkipAdvancesOnlyOnceAndKeepsCompletionOnFailure(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.autoNext = true
	_ = a.seriesSkips.set(a.title.name, seriesSkipRule{Enabled: true, Ending: 10})
	requests := 0
	a.title.session = &recoverySession{play: func(_ context.Context, _ string, e catalog.Episode) (catalog.Playback, error) {
		requests++
		if e.Name != "第2集" {
			t.Errorf("advanced to %s", e.Name)
		}
		return catalog.Playback{}, errors.New("fixture failure")
	}}
	a.player.Seek(51 * time.Second)
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	if requests != 1 || a.playFailure == nil || len(a.history.entries) != 1 || !a.history.entries[0].finished() {
		t.Fatalf("credits retry/completion: requests=%d failure=%v history=%+v", requests, a.playFailure, a.history.entries)
	}
	tt.Advance(2 * time.Second)
	if requests != 1 || !a.history.entries[0].finished() {
		t.Fatal("credits triggered again or periodic recording overwrote completion")
	}
}

func TestEndingSkipHonorsAutoNextOffAndLastEpisode(t *testing.T) {
	for _, last := range []bool{false, true} {
		a := newKeyboardTestApp(t)
		a.autoNext = last
		if last {
			a.playingEpisode = "第2集"
		}
		_ = a.seriesSkips.set(a.title.name, seriesSkipRule{Enabled: true, Ending: 10})
		a.player.Seek(51 * time.Second)
		a.advanceEpisode(a.player, time.Now())
		if a.autoNextPending || len(a.history.entries) != 0 {
			t.Fatal("credits completed without an enabled next episode")
		}
	}
}

func TestSkipSettingsCanBeEditedAndDisabledInSmallPlayer(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			a := newKeyboardTestApp(t)
			a.setFullScreen(full)
			tt := ui.NewTester(a.view, 640, 420)
			if err := tt.Click("选集"); err != nil {
				t.Fatal(err)
			}
			if r, ok := tt.Find("自动下一集"); !ok || r.H <= 0 {
				t.Fatal("auto next control hidden in small player")
			}
			r, ok := tt.Find("跳过片头秒数")
			if !ok || r.H <= 0 {
				t.Fatal("opening seconds input hidden")
			}
			if outR, ok := tt.Find("跳过片尾秒数"); !ok || outR.H <= 0 {
				t.Fatal("ending seconds input hidden")
			}
			tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
			tt.Key(ui.Cmd, ui.KeyA)
			tt.Type("10")
			if got := a.seriesSkips.get(a.title.name); !got.Enabled || got.Opening != 10 {
				t.Fatalf("setting not enabled and saved on positive input: %+v", got)
			}
			if err := tt.Click("启用本剧跳过"); err != nil {
				t.Fatal(err)
			}
			if got := a.seriesSkips.get(a.title.name); got.Enabled || got.Opening != 10 {
				t.Fatalf("disable erased settings or stayed enabled: %+v", got)
			}
		})
	}
}

func TestLocalPlaylistSeriesSkipOpeningEndingAndPersistence(t *testing.T) {
	dir := t.TempDir()
	ep1 := filepath.Join(dir, "S01E01.mp4")
	ep2 := filepath.Join(dir, "S01E02.mp4")
	ep3 := filepath.Join(dir, "S01E03.mp4")
	for _, p := range []string{ep1, ep2, ep3} {
		if err := os.WriteFile(p, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	skipPath := filepath.Join(t.TempDir(), "series-skips.json")
	a := &app{
		autoNext:    true,
		preferences: defaultPlayerPreferences(),
		seriesSkips: seriesSkipStore{path: skipPath},
		openLocal: func(string) (*ui.VideoPlayer, error) {
			return ui.NewVideoPlayer(&timedVideo{})
		},
	}
	t.Cleanup(a.close)

	if !a.openLocalFile(ep1) {
		t.Fatalf("open ep1: %s", a.status)
	}
	a.player.Pause()

	tt := ui.NewTester(a.view, 1280, 720)
	tt.SetTitleBar(dragBar)
	tt.Frame()
	if err := tt.Click("列表"); err != nil {
		t.Fatalf("open local playlist panel: %v", err)
	}
	tt.Advance(300 * time.Millisecond)

	if !tt.HasText("片头片尾") || !tt.HasText("片头到这") || !tt.HasText("片尾从这") || !tt.HasText("填入秒数即启用，同目录各集沿用") {
		t.Fatalf("local playlist missing inline skip controls: %v", tt.Texts())
	}

	// Stamp opening at 15s and ending at 50s (10s remaining of 60s timedVideo).
	a.player.Seek(15 * time.Second)
	tt.Frame()
	if err := tt.Click("片头到这"); err != nil {
		t.Fatalf("click 片头到这: %v", err)
	}
	a.player.Seek(50 * time.Second)
	tt.Frame()
	if err := tt.Click("片尾从这"); err != nil {
		t.Fatalf("click 片尾从这: %v", err)
	}

	key := localSkipDirKey(ep1)
	if key == "" {
		t.Fatal("expected non-empty local directory skip key")
	}
	want := seriesSkipRule{Enabled: true, Opening: 15, Ending: 10}
	if got := a.seriesSkips.get(key); got != want {
		t.Fatalf("local skip rule = %+v, want %+v", got, want)
	}
	reloaded, err := readSeriesSkips(skipPath)
	if err != nil {
		t.Fatalf("reload series skips: %v", err)
	}
	if got := reloaded[key]; got != want {
		t.Fatalf("persisted local skip rule = %+v, want %+v", got, want)
	}

	// While playing 5s before the 50s ending skip cutoff (at 45s), up-next countdown pill appears early.
	a.player.Seek(45 * time.Second)
	a.player.Play()
	tt.Frame()
	if !tt.HasText("立即播放") || !tt.HasText("暂不连播") {
		t.Fatalf("up-next pill did not honor local ending skip: %v", tt.Texts())
	}

	// Reaching 50s triggers ending skip, records completion, advances to ep2, and skips ep2's opening to 15s.
	a.player.Seek(50 * time.Second)
	tt.Frame()
	if a.playingFilePath != ep2 || a.localPlaylistIdx != 1 {
		t.Fatalf("ending skip did not advance local playlist to ep2: path=%q idx=%d", a.playingFilePath, a.localPlaylistIdx)
	}
	if ep1Entry, ok := a.history.findLocalByPath(ep1); !ok || !ep1Entry.finished() {
		t.Fatalf("ep1 completion was not recorded on ending skip: %+v", a.history.entries)
	}
	tt.Frame()
	if pos := a.player.Position(); pos < 14*time.Second || pos > 17*time.Second {
		t.Fatalf("ep2 did not inherit opening skip to 15s: pos=%v", pos)
	}
	if !tt.HasText("已跳过片头 15 秒") || !tt.HasText("回看片头") {
		t.Fatalf("ep2 opening skip undo pill missing skipped seconds: %v", tt.Texts())
	}
	if err := tt.Click("回看片头"); err != nil {
		t.Fatalf("click 回看片头: %v", err)
	}
	if pos := a.player.Position(); pos != 0 {
		t.Fatalf("clicking 回看片头 did not rewind local ep2 to 0: pos=%v", pos)
	}

	// Manual disable keeps saved opening/ending durations.
	if err := tt.Click("启用本剧跳过"); err != nil {
		t.Fatalf("toggle skip off: %v", err)
	}
	if got := a.seriesSkips.get(key); got.Enabled || got.Opening != 15 || got.Ending != 10 {
		t.Fatalf("disabling local skip erased durations: %+v", got)
	}
}
