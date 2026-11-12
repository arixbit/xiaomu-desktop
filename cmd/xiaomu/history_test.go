package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

func TestHistoryStoreKeepsNewestEntryOfEachTitle(t *testing.T) {
	var h historyStore
	h.note(historyEntry{Name: "示例", Episode: "第1集"})
	h.note(historyEntry{Name: "别的片"})
	h.note(historyEntry{Name: "《示例》", Episode: "第2集"})
	if len(h.entries) != 2 || h.entries[0].Episode != "第2集" || h.entries[1].Name != "别的片" {
		t.Fatalf("same title was not replaced and moved first: %+v", h.entries)
	}
	h.remove(5)
	h.remove(-1)
	if len(h.entries) != 2 {
		t.Fatal("removing a missing row changed the history")
	}
	h.remove(0)
	if len(h.entries) != 1 || h.entries[0].Name != "别的片" {
		t.Fatalf("remove: %+v", h.entries)
	}
	for i := range historyLimit + 5 {
		h.note(historyEntry{Name: fmt.Sprintf("片%d", i)})
	}
	if len(h.entries) != historyLimit || h.entries[0].Name != fmt.Sprintf("片%d", historyLimit+4) {
		t.Fatalf("limit: %d entries, first %q", len(h.entries), h.entries[0].Name)
	}
}

func TestHistoryFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if entries, err := readHistory(path); err != nil || entries != nil {
		t.Fatalf("missing file: %v %v", entries, err)
	}
	memory := historyStore{}
	memory.note(historyEntry{Name: "示例"})
	if err := memory.flush(); err != nil {
		t.Fatalf("memory-only history should not write: %v", err)
	}
	want := historyEntry{
		Name: "示例", ConfigURL: "https://example.com/config.json", EntryKey: "key", SiteKey: "a",
		VideoID: "7", Line: "线路一", Episode: "第2集", Position: 30, Duration: 600,
		Updated: time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC),
	}
	h := historyStore{path: path}
	h.note(want)
	if err := h.flush(); err != nil || h.dirty {
		t.Fatalf("flush: %v dirty=%v", err, h.dirty)
	}
	got, err := readHistory(path)
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("read back %+v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHistory(path); err == nil {
		t.Fatal("a broken file was read as empty history")
	}
}

func TestHistoryProgress(t *testing.T) {
	tests := []struct {
		name  string
		entry historyEntry
		want  string
	}{
		{"watching", historyEntry{Episode: "第2集", Position: 30, Duration: 600}, "第2集 · 看到 00:30 / 10:00"},
		{"unknown length", historyEntry{Position: 30}, "正片 · 看到 00:30"},
		{"finished", historyEntry{Episode: "第2集", Position: 597, Duration: 600}, "第2集 · 已看完"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.progress(); got != tt.want {
				t.Fatalf("progress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResumePoint(t *testing.T) {
	detail := catalog.Detail{Sources: []catalog.Source{
		{Name: "线路一", Episodes: []catalog.Episode{{Name: "第1集"}, {Name: "第2集"}, {Name: "第3集"}}},
		{Name: "线路二", Episodes: []catalog.Episode{{Name: "01"}, {Name: "02"}}},
	}}
	tests := []struct {
		name    string
		entry   historyEntry
		line    int
		episode string
		at      time.Duration
		ok      bool
	}{
		{"same episode", historyEntry{Line: "线路一", Episode: "第2集", Position: 30, Duration: 600}, 0, "第2集", 30 * time.Second, true},
		{"other line", historyEntry{Line: "线路二", Episode: "02", Position: 30}, 1, "02", 30 * time.Second, true},
		{"finished moves on", historyEntry{Line: "线路一", Episode: "第2集", Position: 598, Duration: 600}, 0, "第3集", 0, true},
		{"finished last stays", historyEntry{Line: "线路一", Episode: "第3集", Position: 600, Duration: 600}, 0, "第3集", 0, true},
		{"line renamed", historyEntry{Line: "旧线路", Episode: "第2集", Position: 30}, 0, "第2集", 30 * time.Second, true},
		{"episode renamed", historyEntry{Line: "旧线路", Episode: "2", Position: 30}, 0, "第2集", 30 * time.Second, true},
		{"nothing matches", historyEntry{Line: "旧线路", Episode: "正片", Position: 30}, 0, "第1集", 30 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, episode, at, ok := resumePoint(detail, tt.entry)
			if line != tt.line || episode.Name != tt.episode || at != tt.at || ok != tt.ok {
				t.Fatalf("got line=%d episode=%q at=%v ok=%v", line, episode.Name, at, ok)
			}
		})
	}
	if _, _, _, ok := resumePoint(catalog.Detail{}, historyEntry{Episode: "第1集"}); ok {
		t.Fatal("a title without episodes has nowhere to resume")
	}
}

func TestFindEntry(t *testing.T) {
	doc := &config.Config{URL: "https://example.com/config.json", Sites: []config.Site{
		{Key: "a", API: "https://example.com/a"},
		{Key: "b", API: "https://example.com/b"},
	}}
	recorded := library.Entry{Site: doc.Sites[1], Config: doc}
	tests := []struct {
		name  string
		entry historyEntry
		want  string
		ok    bool
	}{
		{"same site", historyEntry{EntryKey: recorded.Key(), SiteKey: "b"}, "b", true},
		{"address changed", historyEntry{EntryKey: "stale", SiteKey: "b"}, "b", true},
		{"site gone", historyEntry{EntryKey: "stale", SiteKey: "c"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, ok := findEntry(doc, tt.entry)
			if ok != tt.ok || entry.Site.Key != tt.want {
				t.Fatalf("got %q ok=%v", entry.Site.Key, ok)
			}
		})
	}
}

func TestGroupTitles(t *testing.T) {
	doc := &config.Config{URL: "https://example.com/config.json"}
	item := func(site, id, name string) library.Item {
		return library.Item{
			Entry: library.Entry{Site: config.Site{Key: site, Name: site}, Config: doc},
			Video: catalog.Video{ID: catalog.ID(id), Name: name},
		}
	}
	groups := groupTitles([]library.Item{
		item("甲", "1", "示例"),
		item("甲", "2", "EXAMPLE"),
		item("乙", "3", " 《示例》 "),
		item("乙", "4", "example"),
		item("甲", "5", ""),
		item("乙", "6", ""),
	})
	var got []string
	for _, g := range groups {
		got = append(got, fmt.Sprintf("%d:%s", len(g.items), g.origin()))
	}
	want := []string{"2:2 个来源", "2:2 个来源", "1:甲", "1:乙"}
	if !reflect.DeepEqual(got, want) || groups[0].name != "示例" {
		t.Fatalf("groups %v (first %q), want %v", got, groups[0].name, want)
	}
}

// A play-history row starts the recorded episode at its position, asking the
// site again for the media address; once the source is removed it cannot.
func TestResumeFromHistory(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case r.URL.Path == "/config":
			fmt.Fprint(w, `{"sites":[{"key":"a","name":"来源甲","type":1,"api":"/a"}]}`)
		case r.URL.Path == "/two.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			fmt.Fprint(w, "episode two")
		case query.Get("ac") == "list":
			fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
		case query.Get("ids") == "7":
			fmt.Fprintf(w, `{"list":[{"vod_name":"示例","vod_play_from":"线路一","vod_play_url":%q}]}`,
				"第1集$"+srv.URL+"/one.mp4#第2集$"+srv.URL+"/two.mp4")
		default:
			fmt.Fprint(w, `{"pagecount":1,"list":[]}`)
		}
	}))
	defer srv.Close()
	a := &app{}
	defer a.close()
	a.loadConfig(srv.URL + "/config")
	root := a.library.root
	if root == nil {
		t.Fatalf("config did not load: %s", a.status)
	}
	entry := library.Entry{Site: root.Sites[0], Config: root}
	a.history.note(historyEntry{
		Name: "示例", ConfigURL: configURL(entry), EntryKey: entryKey(entry), SiteKey: "a", Origin: entry.Label(),
		VideoID: "7", Line: "线路一", Episode: "第2集", Position: 30, Duration: 600, Updated: time.Now(),
	})
	var played []string
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		resp, err := http.Get(raw)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		played = append(played, string(body))
		return ui.NewVideoPlayer(&stillVideo{})
	}
	tt := ui.NewTester(a.view, 1120, 760)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q: %v; status %s; texts %v", label, err, a.status, tt.Texts())
		}
	}
	click("播放历史")
	if !tt.HasText("第2集 · 看到 00:30 / 10:00") || !tt.HasText("来源甲") {
		t.Fatalf("history row: %v", tt.Texts())
	}
	click("继续播放 示例")
	if a.player == nil || a.title == nil || a.title.name != "示例" {
		t.Fatalf("resume did not open the player: status %s", a.status)
	}
	if len(played) != 1 || played[0] != "episode two" || a.playingSource != "线路一" || a.playingEpisode != "第2集" {
		t.Fatalf("played %v at %s/%s", played, a.playingSource, a.playingEpisode)
	}
	if a.resumeAt != 30*time.Second && a.lastPosition != 30*time.Second {
		t.Fatalf("position not restored: resumeAt=%v last=%v", a.resumeAt, a.lastPosition)
	}
	click("返回")
	if a.player != nil || a.title != nil || a.route() != routeHistory {
		t.Fatalf("back from a resumed title: route=%v title=%v", a.route(), a.title)
	}
	a.removeConfig()
	tt.Frame()
	if !tt.HasText("片源已移除") {
		t.Fatalf("removed source not marked: %v", tt.Texts())
	}
	_ = tt.Click("继续播放 示例") // disabled: nothing should play
	a.resume(a.history.entries[0])
	if len(played) != 1 || a.player != nil {
		t.Fatalf("an entry without its source played: %v", played)
	}
	click("移除 示例")
	if len(a.history.entries) != 0 || !tt.HasText("还没有播放记录") {
		t.Fatalf("remove: %v", tt.Texts())
	}
}

func TestRecordHistoryNotesOftenAndSavesNowAndThen(t *testing.T) {
	a := newPlayerTestApp(t)
	path := filepath.Join(t.TempDir(), "history.json")
	a.history.path = path
	saved := func() string {
		t.Helper()
		entries, err := readHistory(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			return fmt.Sprint(len(entries), " entries")
		}
		return entries[0].Episode
	}
	start := time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)
	a.recordHistory(a.player, start)
	if len(a.history.entries) != 1 || saved() != "第1集" {
		t.Fatalf("first note should be saved at once: %+v", a.history.entries)
	}
	a.playingEpisode = "第2集"
	a.recordHistory(a.player, start.Add(500*time.Millisecond))
	if a.history.entries[0].Episode != "第1集" {
		t.Fatal("noted more than once a second")
	}
	a.recordHistory(a.player, start.Add(2*time.Second))
	if a.history.entries[0].Episode != "第2集" || saved() != "第1集" {
		t.Fatalf("noted %q, saved %q", a.history.entries[0].Episode, saved())
	}
	a.recordHistory(a.player, start.Add(historySaveGap+time.Second))
	if saved() != "第2集" {
		t.Fatalf("not saved after %v", historySaveGap)
	}
	a.playingLive = true
	a.history.entries = nil
	a.recordHistory(a.player, start.Add(time.Minute))
	if len(a.history.entries) != 0 {
		t.Fatal("live playback was recorded")
	}
}
