package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/tmdb"
)

func TestLocalPosterSeriesIdentityRequiresExactTVMatch(t *testing.T) {
	tv := tmdb.Match{ID: 42, Kind: tmdb.TV, Title: "示例剧", OriginalTitle: "Example Show", Year: "2018"}
	for _, tc := range []struct {
		filename string
		want     int
	}{
		{"Example.Show.2018.S01E01.mp4", 42},
		{"示例剧.S01E10.mp4", 42},
		{"Example.Show.2020.S01E01.mp4", 0},
		{"Another.Show.S01E01.mp4", 0},
		{"Example.Show.2018.mp4", 0},
	} {
		if got := localPosterSeriesID(tc.filename, tv); got != tc.want {
			t.Errorf("series ID for %q = %d, want %d", tc.filename, got, tc.want)
		}
	}
	tv.Kind = tmdb.Movie
	if got := localPosterSeriesID("Example.Show.2018.S01E01.mp4", tv); got != 0 {
		t.Fatalf("movie match unexpectedly became a series ID: %d", got)
	}
}

func TestLocalPosterSeriesHistoryReuse(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "Example.Show.2018.S01E01.mp4")
	second := filepath.Join(dir, "Example.Show.2018.S01E02.mp4")
	otherYear := filepath.Join(dir, "Example.Show.2020.S01E03.mp4")
	otherFolder := filepath.Join(dir, "different", filepath.Base(second))
	h := historyStore{path: filepath.Join(dir, "history.json")}
	h.note(historyEntry{Kind: historyKindLocal, VideoID: first, LocalPoster: "cover", PosterSeriesID: 42})
	h.note(historyEntry{Kind: historyKindLocal, VideoID: first, Position: 24})
	if err := h.flush(); err != nil {
		t.Fatal(err)
	}
	entries, err := readHistory(h.path)
	if err != nil {
		t.Fatal(err)
	}
	h.entries = entries
	title, year, _ := localPosterFileQuery(second)
	if got, ok := h.findLocalSeriesPoster(second, title, year); !ok || got.LocalPoster != "cover" || got.PosterSeriesID != 42 {
		t.Fatalf("episode did not find its saved series cover: %+v", got)
	}
	for _, path := range []string{first, otherYear, otherFolder} {
		title, year, _ := localPosterFileQuery(path)
		if got, ok := h.findLocalSeriesPoster(path, title, year); ok {
			t.Errorf("unexpected series cover for %s: %+v", path, got)
		}
	}
	h.note(historyEntry{
		Kind: historyKindLocal, VideoID: filepath.Join(dir, "Example.Show.2018.S01E04.mp4"),
		LocalPoster: "different-cover", PosterSeriesID: 99,
	})
	if _, ok := h.findLocalSeriesPoster(second, title, year); ok {
		t.Fatal("conflicting series IDs should not be reused")
	}
}

func TestLocalPosterSeriesPlaybackReusesDownloadAfterRestart(t *testing.T) {
	a, first := testLocalPosterPlayback(t, "Example.Show.2018.S01E01.mp4")
	second := filepath.Join(filepath.Dir(first), "Example.Show.2018.S01E02.mp4")
	if err := os.WriteFile(second, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	poster := testLocalPosterPNG(t)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(poster)
	}))
	defer server.Close()

	lookups := 0
	a.preferences.TMDBToken = "fixture-token"
	a.localPoster.newSearcher = func(string) localPosterSearcher {
		return posterSearchFunc(func(context.Context, string, string, tmdb.Kind) ([]tmdb.Match, error) {
			lookups++
			return []tmdb.Match{{
				ID: 42, Kind: tmdb.TV, Title: "Example Show", Year: "2018", PosterURL: server.URL + "/poster",
			}}, nil
		})
	}
	if !a.openLocalFile(first) {
		t.Fatalf("open first episode: %s", a.status)
	}
	a.recordLocalCompletion(a.player)
	a.leavePlayer()
	saved, err := readHistory(a.history.path)
	if err != nil {
		t.Fatal(err)
	}
	firstEntry, _ := a.history.findLocalByPath(first)
	if firstEntry.PosterSeriesID != 42 || firstEntry.LocalPoster == "" {
		t.Fatalf("first episode did not record its series cover: %+v", firstEntry)
	}

	b := &app{
		history:     historyStore{entries: saved, path: a.history.path},
		preferences: a.preferences,
		openLocal:   a.openLocal,
	}
	b.localPoster.newSearcher = a.localPoster.newSearcher
	t.Cleanup(b.close)
	if !b.openLocalFile(second) {
		t.Fatalf("open second episode: %s", b.status)
	}
	b.recordLocalCompletion(b.player)
	secondEntry, _ := b.history.findLocalByPath(second)
	if secondEntry.PosterSeriesID != 42 || secondEntry.LocalPoster != firstEntry.LocalPoster {
		t.Fatalf("second episode did not reuse first cover: %+v", secondEntry)
	}
	if lookups != 1 || downloads.Load() != 1 {
		t.Fatalf("series requested %d searches and %d image downloads, want one of each", lookups, downloads.Load())
	}
}
