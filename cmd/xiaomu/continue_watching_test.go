package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
	a.dismissContinueWatching("《示例7》")
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
