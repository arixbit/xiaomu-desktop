package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/douban"
	"github.com/egoist/mygo/ui"
)

func TestSearchHistoryRecordDedupesCapsAndSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search-history.json")
	s := searchHistoryStore{path: path}
	if err := s.record("  "); err != nil || len(s.queries) != 0 {
		t.Fatal("an empty query was recorded")
	}
	for _, q := range []string{"演示电影", "演示剧集", "《演示电影》", "其他"} {
		if err := s.record(q); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.queries) != 3 || s.queries[0] != "其他" || s.queries[1] != "《演示电影》" || s.queries[2] != "演示剧集" {
		t.Fatalf("dedupe or order wrong: %q", s.queries)
	}
	for i := 0; i < searchHistoryLimit; i++ {
		if err := s.record(fmt.Sprintf("片名 %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.queries) != searchHistoryLimit || s.queries[0] != fmt.Sprintf("片名 %d", searchHistoryLimit-1) {
		t.Fatalf("cap wrong: %q", s.queries)
	}
	saved, err := readSearchHistory(path)
	if err != nil || len(saved) != len(s.queries) || saved[0] != s.queries[0] {
		t.Fatalf("saved history: %q, %v", saved, err)
	}
}

func TestSearchHistoryReadHandlesMissingAndCorrupt(t *testing.T) {
	queries, err := readSearchHistory(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || queries != nil {
		t.Fatalf("missing file: %q, %v", queries, err)
	}
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSearchHistory(path); err == nil {
		t.Fatal("corrupt history read as valid")
	}
}

func TestSearchHistorySaveFailureKeepsOldQueries(t *testing.T) {
	s := searchHistoryStore{queries: []string{"旧片名"}, path: t.TempDir()}
	if err := s.record("新片名"); err == nil {
		t.Fatal("a save to a directory succeeded")
	}
	if len(s.queries) != 1 || s.queries[0] != "旧片名" {
		t.Fatalf("failed save appeared saved: %q", s.queries)
	}
}

func TestSuggestMatchesOrdering(t *testing.T) {
	candidates := []string{"演示电影", "我的演示", "演示剧集", "无关", "演示电影"}
	got := suggestMatches(candidates, "演示")
	if len(got) != 3 || got[0] != "演示电影" || got[1] != "演示剧集" || got[2] != "我的演示" {
		t.Fatalf("prefix before contains: %q", got)
	}
	if got := suggestMatches(candidates, "演示电影"); len(got) != 0 {
		t.Fatalf("exact query offered itself: %q", got)
	}
	if got := suggestMatches(candidates, ""); len(got) != 4 || got[0] != "演示电影" {
		t.Fatalf("empty query should show the first candidates: %q", got)
	}
	many := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		many = append(many, fmt.Sprintf("演示 %02d", i))
	}
	if got := suggestMatches(many, "演示"); len(got) != searchSuggestionLimit {
		t.Fatalf("popup not capped: %d", len(got))
	}
}

func TestSearchSuggestionsMergeHistoryAndDouban(t *testing.T) {
	a := &app{}
	a.searchHistory.queries = []string{"搜过的片"}
	a.discovery.movie.feed = douban.Feed{Subjects: []douban.Subject{{Title: "热播电影"}, {Title: "搜过的片"}}}
	a.discovery.tv.feed = douban.Feed{Subjects: []douban.Subject{{Title: "热播剧集"}}}
	got := a.searchSuggestions()
	if len(got) != 3 || got[0] != "搜过的片" || got[1] != "热播电影" || got[2] != "热播剧集" {
		t.Fatalf("history first, then hot titles, deduped: %q", got)
	}
}

// suggestionTestServer is one JSON CMS site whose search answers the demo
// title, for driving the toolbar from the UI tester.
func suggestionTestServer(t *testing.T) (*httptest.Server, *config.Config) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("ids") != "" {
			fmt.Fprintf(w, `{"list":[{"vod_id":"7","vod_name":"演示电影","vod_play_from":"直连","vod_play_url":"第1集$%s/clip.mp4"}]}`, srv.URL)
			return
		}
		if q.Get("wd") != "" {
			fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"7","vod_name":"演示电影"}]}`)
			return
		}
		fmt.Fprint(w, `{"list":[]}`)
	}))
	t.Cleanup(srv.Close)
	doc := &config.Config{URL: srv.URL + "/config", Sites: []config.Site{{Key: "site", Name: "示例来源", Type: 1, Kind: config.KindJSON, API: srv.URL + "/api"}}}
	return srv, doc
}

func newSuggestionTestApp(t *testing.T, doc *config.Config) *app {
	t.Helper()
	a := &app{searchHistory: searchHistoryStore{path: filepath.Join(t.TempDir(), "search-history.json")}}
	t.Cleanup(a.close)
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{}}
	a.searchHistory.queries = []string{"演示电影"}
	a.discovery.movie.feed = douban.Feed{Subjects: []douban.Subject{{Title: "演示剧集"}}}
	return a
}

func TestSearchSuggestionsChooseAndSearch(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("片名"); err != nil {
		t.Fatal(err)
	}
	tt.Type("演示")
	if !tt.HasText("演示电影") || !tt.HasText("演示剧集") {
		t.Fatalf("suggestions did not show: %v", tt.Texts())
	}
	// Down takes the first suggestion into the field; Enter after that
	// searches it. A plain Enter would have searched the typed text.
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	if a.route() != routeHome || a.browser.query != "演示电影" {
		t.Fatalf("choosing a suggestion did not fill it: route=%v query=%q", a.route(), a.browser.query)
	}
	tt.Key(0, ui.KeyEnter)
	if a.route() != routeSearch || a.library.search.query != "演示电影" {
		t.Fatalf("chosen suggestion did not search: route=%v query=%q", a.route(), a.library.search.query)
	}
	if len(a.library.search.items) != 1 {
		t.Fatalf("search results: %d", len(a.library.search.items))
	}
	if len(a.searchHistory.queries) == 0 || a.searchHistory.queries[0] != "演示电影" {
		t.Fatalf("chosen search was not recorded: %q", a.searchHistory.queries)
	}
}

func TestSearchResultClickOpensTitle(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("片名"); err != nil {
		t.Fatal(err)
	}
	tt.Type("演示电影")
	tt.Key(0, ui.KeyEnter)
	if a.route() != routeSearch {
		t.Fatalf("typed search did not run: route=%v", a.route())
	}
	// The click goes by coordinates: the poster image covers the card, so a
	// label click would land on the image rather than the card behind it.
	poster, ok := tt.Find("演示电影 · 示例来源")
	if !ok {
		t.Fatalf("result label missing: %v", tt.Texts())
	}
	tt.ClickAt(poster.X+poster.W/2, poster.Y+poster.H/2)
	if a.route() != routeDetail || a.title == nil || a.title.detail == nil {
		t.Fatalf("click did not open the result: route=%v title=%+v", a.route(), a.title)
	}
}

func TestSearchResultsTakeKeyboardFocus(t *testing.T) {
	_, doc := suggestionTestServer(t)
	a := newSuggestionTestApp(t, doc)
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("片名"); err != nil {
		t.Fatal(err)
	}
	tt.Type("演示电影")
	tt.Key(0, ui.KeyEnter)
	if a.route() != routeSearch || len(a.library.search.groups) != 1 {
		t.Fatalf("typed search did not run: route=%v groups=%d", a.route(), len(a.library.search.groups))
	}
	// The results grid took the focus: Down moves the choice without
	// opening it, Enter opens it, and Escape goes back to the results.
	tt.Key(0, ui.KeyDown)
	if a.library.search.selected != 0 || a.route() != routeSearch {
		t.Fatalf("Down did not move the choice: selected=%d route=%v", a.library.search.selected, a.route())
	}
	tt.Key(0, ui.KeyEnter)
	if a.route() != routeDetail || a.title == nil || a.title.detail == nil {
		t.Fatalf("Enter did not open the result: route=%v title=%+v", a.route(), a.title)
	}
	tt.Key(0, ui.KeyEscape)
	if a.route() != routeSearch {
		t.Fatalf("Escape did not return to the results: route=%v", a.route())
	}
}

func TestSearchHistoryIsRestoredBesideSavedSource(t *testing.T) {
	// The store itself keeps no config URL; the file only ever holds titles.
	path := filepath.Join(t.TempDir(), "search-history.json")
	if err := writeJSONFile(path, []string{"演示电影"}); err != nil {
		t.Fatal(err)
	}
	queries, err := readSearchHistory(path)
	if err != nil || len(queries) != 1 || queries[0] != "演示电影" {
		t.Fatalf("restored: %q, %v", queries, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "http") {
		t.Fatal("search history persisted an address")
	}
}
