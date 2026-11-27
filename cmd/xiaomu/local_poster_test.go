package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/tmdb"
	"github.com/egoist/mygo/ui"
)

type posterSearchFunc func(context.Context, string, string, tmdb.Kind) ([]tmdb.Match, error)

func (f posterSearchFunc) Search(ctx context.Context, title, year string, kind tmdb.Kind) ([]tmdb.Match, error) {
	return f(ctx, title, year, kind)
}

func testLocalPosterPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 30, 45))
	for y := range 45 {
		for x := range 30 {
			img.SetRGBA(x, y, color.RGBA{R: 240, G: 40, B: 60, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testLocalPosterPlayback(t *testing.T, filename string) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &app{
		history:     historyStore{path: filepath.Join(dir, "data", "history.json")},
		preferences: defaultPlayerPreferences(),
		openLocal: func(string) (*ui.VideoPlayer, error) {
			return ui.NewVideoPlayer(&timedVideo{})
		},
	}
	t.Cleanup(a.close)
	return a, path
}

func TestLocalPosterSidecarSurvivesHistoryAndRestart(t *testing.T) {
	a, path := testLocalPosterPlayback(t, "示例电影.2024.mp4")
	if err := os.WriteFile(path[:len(path)-len(".mp4")]+".png", testLocalPosterPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	a.preferences.TMDBToken = "fixture-token"
	a.localPoster.newSearcher = func(string) localPosterSearcher {
		return posterSearchFunc(func(context.Context, string, string, tmdb.Kind) ([]tmdb.Match, error) {
			t.Fatal("same-name poster should avoid an online search")
			return nil, nil
		})
	}
	if !a.openLocalFile(path) {
		t.Fatalf("open local video: %s", a.status)
	}
	a.player.Seek(25 * time.Second)
	now := time.Now()
	a.recordHistory(a.player, now)
	first, ok := a.history.findLocalByPath(path)
	if !ok || first.LocalPoster == "" || first.Position != 25 {
		t.Fatalf("history after playback: %+v", first)
	}
	if src := a.historyPoster(nil, first); src == nil {
		t.Fatal("poster missing from shared history and continue-watching renderer")
	}
	a.recordHistory(a.player, now.Add(2*time.Second))
	a.recordLocalCompletion(a.player)
	a.leavePlayer()

	entries, err := readHistory(a.history.path)
	if err != nil || len(entries) != 1 || entries[0].LocalPoster != first.LocalPoster || !entries[0].finished() {
		t.Fatalf("history after restart: %+v (%v)", entries, err)
	}
	restarted := &app{history: historyStore{entries: entries, path: a.history.path}}
	if src := restarted.historyPoster(nil, entries[0]); src == nil {
		t.Fatal("poster did not load from disk after restart")
	}
}

func TestLocalPosterAutoAndManualMatch(t *testing.T) {
	a, path := testLocalPosterPlayback(t, "示例剧.S02E05.2024.1080p.mp4")
	data := testLocalPosterPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("TMDB token reached the poster server")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	defer server.Close()

	exact := tmdb.Match{ID: 1, Title: "示例剧", Year: "2024", Kind: tmdb.TV, PosterURL: server.URL + "/one"}
	same := tmdb.Match{ID: 2, Title: exact.Title, Year: exact.Year, Kind: tmdb.TV, PosterURL: server.URL + "/two"}
	calls := 0
	a.preferences.TMDBToken = "fixture-token"
	a.localPoster.newSearcher = func(token string) localPosterSearcher {
		if token != "fixture-token" {
			t.Fatal("lookup did not use the configured token")
		}
		return posterSearchFunc(func(_ context.Context, title, year string, kind tmdb.Kind) ([]tmdb.Match, error) {
			calls++
			if title != "示例剧" || year != "2024" {
				t.Fatalf("query contained more than the parsed title and year: %q %q", title, year)
			}
			if calls == 1 {
				if kind != tmdb.TV {
					t.Fatalf("explicit episode marker should search series, got %q", kind)
				}
				return []tmdb.Match{exact, same}, nil
			}
			if kind != "" {
				t.Fatalf("manual search should allow correction across media types, got %q", kind)
			}
			same.Title = "示例剧（另一个版本）"
			return []tmdb.Match{exact, same}, nil
		})
	}
	if !a.openLocalFile(path) {
		t.Fatalf("open local video: %s", a.status)
	}
	a.recordHistory(a.player, time.Now())
	a.leavePlayer()
	e, ok := a.history.findLocalByPath(path)
	if calls != 1 || !ok || e.LocalPoster != "" {
		t.Fatalf("ambiguous automatic match stored a cover: %d %+v", calls, e)
	}

	a.switchTab(routeHistory)
	tt := ui.NewTester(a.view, 850, 600)
	if err := tt.Click("匹配封面 " + e.Name); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("搜索"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(a.localPoster.matches) != 2 {
		t.Fatalf("manual matches = %d, calls = %d", len(a.localPoster.matches), calls)
	}
	if err := tt.Click("示例剧（另一个版本） 2024 剧集"); err != nil {
		t.Fatal(err)
	}
	e, _ = a.history.findLocalByPath(path)
	if e.LocalPoster == "" || !e.PosterManual || a.localPoster.open {
		t.Fatalf("manual result was not saved: %+v, open=%v", e, a.localPoster.open)
	}
	if !a.openLocalFile(path) {
		t.Fatal("resume local video")
	}
	a.recordLocalCompletion(a.player)
	entry, _ := a.history.findLocalByPath(path)
	if calls != 2 || entry.LocalPoster != e.LocalPoster || !entry.PosterManual {
		t.Fatalf("reopening replaced the chosen cover: %d %+v", calls, entry)
	}
}

func TestLocalPosterExactMatch(t *testing.T) {
	movie := tmdb.Match{ID: 1, Title: "示例电影", OriginalTitle: "Sample Film", Kind: tmdb.Movie, Year: "2024", PosterURL: "https://example.com/poster.jpg"}
	other := movie
	other.ID, other.Year, other.PosterURL = 2, "2026", ""
	if got, ok := exactLocalPoster("Sample Film", "2024", []tmdb.Match{other, movie}); !ok || got.ID != 1 {
		t.Fatalf("match by original title and year = %+v, %v", got, ok)
	}
	if _, ok := exactLocalPoster("示例电影", "", []tmdb.Match{movie, other}); ok {
		t.Fatal("same-name candidate without artwork incorrectly ignored")
	}
	if title, _, _ := localPosterFileQuery(filepath.Join("private", "第1集.mp4")); title != "" {
		t.Fatalf("episode-only filename became an automatic title: %q", title)
	}
}

func TestLocalPosterBareEpisodeWithMatchingFolder(t *testing.T) {
	const series = "示例古装连续剧"
	for _, tc := range []struct {
		folder, file, title string
		kind                tmdb.Kind
	}{
		{series, series + " 10.mp4", series, tmdb.TV},
		{"Sample.Show", "Sample.Show.03.1080p.mkv", "Sample Show", tmdb.TV},
		{"Movies", "Rocky 2.mp4", "Rocky 2", ""},
		{"Sample Show 2", "Sample Show 2.mp4", "Sample Show 2", ""},
		{"Movies", "1917.mp4", "1917", ""},
	} {
		t.Run(tc.file, func(t *testing.T) {
			title, year, kind := localPosterFileQuery(filepath.Join(tc.folder, tc.file))
			if title != tc.title || year != "" || kind != tc.kind {
				t.Fatalf("got %q %q %q, want %q with kind %q", title, year, kind, tc.title, tc.kind)
			}
		})
	}
}

func TestLocalPosterAutomaticDownloadAndOfflineReuse(t *testing.T) {
	a, path := testLocalPosterPlayback(t, "Sample.Film.2024.1080p.mp4")
	data := testLocalPosterPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	a.preferences.TMDBToken = "fixture-token"
	a.localPoster.newSearcher = func(string) localPosterSearcher {
		return posterSearchFunc(func(_ context.Context, title, year string, kind tmdb.Kind) ([]tmdb.Match, error) {
			if title != "Sample Film" || year != "2024" || kind != "" {
				t.Fatalf("unexpected movie query: %q %q %q", title, year, kind)
			}
			return []tmdb.Match{{ID: 7, Title: "示例电影", OriginalTitle: title, Year: year, Kind: tmdb.Movie, PosterURL: server.URL + "/film"}}, nil
		})
	}
	if !a.openLocalFile(path) {
		t.Fatalf("open local video: %s", a.status)
	}
	a.player.Seek(25 * time.Second)
	a.recordHistory(a.player, time.Now())
	a.leavePlayer()
	e, ok := a.history.findLocalByPath(path)
	if !ok || e.LocalPoster == "" || e.PosterManual {
		t.Fatalf("automatic result not recorded: %+v", e)
	}
	server.Close()
	b := &app{
		history:   historyStore{entries: []historyEntry{e}, path: a.history.path},
		openLocal: a.openLocal,
	}
	t.Cleanup(b.close)
	if !b.openLocalFile(path) || b.resumeAt != 25*time.Second || b.historyPoster(nil, e) == nil {
		t.Fatalf("offline resume did not reuse the cached poster or playback position: %v", b.resumeAt)
	}
}
