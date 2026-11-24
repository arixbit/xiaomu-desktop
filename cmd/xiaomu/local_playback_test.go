package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestMyGoJSONRegistersAllPlayableVideoExtensions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "mygo.json"))
	if err != nil {
		t.Fatalf("read mygo.json: %v", err)
	}
	var manifest struct {
		FileAssociations []struct {
			Ext      []string `json:"ext"`
			Name     string   `json:"name"`
			MimeType string   `json:"mimeType"`
			Role     string   `json:"role"`
		} `json:"fileAssociations"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse mygo.json: %v", err)
	}
	registered := map[string]bool{}
	for _, fa := range manifest.FileAssociations {
		if fa.Role != "Viewer" {
			t.Errorf("fileAssociation %q role = %q, want Viewer", fa.Name, fa.Role)
		}
		for _, ext := range fa.Ext {
			registered["."+strings.ToLower(ext)] = true
		}
	}
	for ext := range playableFileExts {
		if !registered[ext] {
			t.Errorf("mygo.json missing base video extension %s", ext)
		}
	}
	for ext := range mpvExtraFileExts {
		if !registered[ext] {
			t.Errorf("mygo.json missing mpv video extension %s", ext)
		}
	}
}

func TestNaturalLessAndDiscoverSiblingVideos(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"Show.S01E10.mp4",
		"Show.S01E02.mp4",
		"Show.S01E1.mp4",
		"Show.S01E02.srt",
		"._Show.S01E01.mp4",
		"notes.txt",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir.mp4"), 0o755); err != nil {
		t.Fatal(err)
	}

	ep2 := filepath.Join(dir, "Show.S01E02.mp4")
	got, idx := discoverSiblingVideos(ep2)
	want := []string{
		filepath.Join(dir, "Show.S01E1.mp4"),
		filepath.Join(dir, "Show.S01E02.mp4"),
		filepath.Join(dir, "Show.S01E10.mp4"),
	}
	if len(got) != len(want) || idx != 1 {
		t.Fatalf("discoverSiblingVideos = %v (idx=%d), want %v (idx=1)", got, idx, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLocalVideoHistoryContinueWatchingAndResume(t *testing.T) {
	dir := t.TempDir()
	ep1 := filepath.Join(dir, "第1集.mp4")
	ep2 := filepath.Join(dir, "第2集.mp4")
	for _, p := range []string{ep1, ep2} {
		if err := os.WriteFile(p, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	histPath := filepath.Join(t.TempDir(), "history.json")
	var opened []string
	a := &app{
		autoNext: true,
		history:  historyStore{path: histPath},
		openLocal: func(path string) (*ui.VideoPlayer, error) {
			opened = append(opened, path)
			return ui.NewVideoPlayer(&timedVideo{})
		},
	}
	t.Cleanup(a.close)

	if !a.openLocalFile(ep1) {
		t.Fatalf("openLocalFile failed: %s", a.status)
	}
	a.player.Seek(25 * time.Second)
	now := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	a.recordHistory(a.player, now)
	a.leavePlayer()

	if len(a.history.entries) != 1 {
		t.Fatalf("expected 1 local history entry, got %+v", a.history.entries)
	}
	entry := a.history.entries[0]
	if !entry.isLocal() || entry.VideoID != ep1 || entry.Position != 25 || entry.Duration != 60 {
		t.Fatalf("unexpected local history entry: %+v", entry)
	}
	// Online title lookup must ignore local file entries.
	if _, found := a.history.findByTitle(entry.Name); found {
		t.Fatal("findByTitle matched a local video entry")
	}

	// Continue watching strip renders on home even without an imported online library.
	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("继续观看") || !tt.HasText("第1集.mp4") {
		t.Fatalf("home continue watching missing local video: %v", tt.Texts())
	}

	// Clicking continue watching resumes at the saved position.
	if err := tt.Click("继续观看 第1集.mp4"); err != nil {
		t.Fatalf("click continue watching: %v (texts=%v)", err, tt.Texts())
	}
	if a.player == nil || !a.playingFile || a.playingFilePath != ep1 {
		t.Fatalf("did not resume local file: player=%v file=%v path=%q", a.player != nil, a.playingFile, a.playingFilePath)
	}
	if a.resumeAt != 25*time.Second && a.lastPosition != 25*time.Second {
		t.Fatalf("resume position lost: resumeAt=%v lastPosition=%v", a.resumeAt, a.lastPosition)
	}

	// Persists across restart, and opening the same file again resumes from saved position.
	a.leavePlayer()
	saved, err := readHistory(histPath)
	if err != nil || len(saved) != 1 || !saved[0].isLocal() {
		t.Fatalf("persisted local history = %+v err=%v", saved, err)
	}
	b := &app{
		history:   historyStore{entries: saved, path: histPath},
		openLocal: a.openLocal,
	}
	t.Cleanup(b.close)
	if !b.openLocalFile(ep1) || b.resumeAt != 25*time.Second {
		t.Fatalf("reopening local file did not restore position: resumeAt=%v status=%q", b.resumeAt, b.status)
	}
	b.leavePlayer()

	// Resuming a finished local episode automatically advances to the next sibling file.
	finished := entry
	finished.Position = finished.Duration
	b.resume(finished)
	if !b.playingFile || b.playingFilePath != ep2 || b.resumeAt != 0 {
		t.Fatalf("resuming finished local file did not advance to sibling: path=%q resumeAt=%v", b.playingFilePath, b.resumeAt)
	}
	b.leavePlayer()

	// Removing the file from disk: resuming checks the disk at once and
	// marks the history entry unavailable.
	if err := os.Remove(ep1); err != nil {
		t.Fatal(err)
	}
	b.resume(entry)
	if b.player != nil || !strings.Contains(b.status, "不存在") {
		t.Fatalf("deleted local file should not play: player=%v status=%q", b.player != nil, b.status)
	}
	if b.historyAvailable(entry) {
		t.Fatal("deleted local file reported as available")
	}
}

// Rows ask every frame; the disk is checked at most once per TTL.
func TestHistoryAvailableCachesLocalFileCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "第1集.mp4")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := historyEntry{Name: "第1集.mp4", Kind: historyKindLocal, VideoID: path}
	a := &app{}
	if !a.historyAvailable(entry) {
		t.Fatal("existing local file reported as unavailable")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !a.historyAvailable(entry) {
		t.Fatal("check within the TTL went to disk again")
	}
	a.localFileChecks[path] = localFileCheck{ok: true, at: time.Now().Add(-localFileCheckTTL)}
	if a.historyAvailable(entry) {
		t.Fatal("expired check did not see the deleted file")
	}
}

func TestLocalVideoSameDirectoryAutoNextAndControls(t *testing.T) {
	dir := t.TempDir()
	ep1 := filepath.Join(dir, "S01E01.mp4")
	ep2 := filepath.Join(dir, "S01E02.mp4")
	for _, p := range []string{ep1, ep2} {
		if err := os.WriteFile(p, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var opened []string
	fakeNP := &recordingNowPlaying{}
	a := &app{
		autoNext:   true,
		nowPlaying: fakeNP,
		openLocal: func(path string) (*ui.VideoPlayer, error) {
			opened = append(opened, path)
			return ui.NewVideoPlayer(&timedVideo{})
		},
	}
	t.Cleanup(a.close)

	if !a.openLocalFile(ep1) {
		t.Fatalf("open ep1: %s", a.status)
	}
	if !a.hasLocalPlaylist() || a.localPlaylistIdx != 0 {
		t.Fatalf("local playlist not initialized: %v idx=%d", a.localPlaylist, a.localPlaylistIdx)
	}
	if fakeNP.info.Commands&mygo.MediaNext == 0 || fakeNP.info.Commands&mygo.MediaPrevious != 0 {
		t.Fatalf("unexpected NowPlaying commands for first local file: %v", fakeNP.info.Commands)
	}

	// Render player screen: sibling panel and next button exist, then advance to end for auto-next.
	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("同目录视频") || !tt.HasText("S01E02.mp4") {
		t.Fatalf("local playlist panel missing: %v", tt.Texts())
	}

	// Advance into up-next countdown window (last 8 seconds of 60s timedVideo).
	a.player.Seek(55 * time.Second)
	tt.Frame()
	if !tt.HasText("立即播放") || !tt.HasText("暂不连播") {
		t.Fatalf("up-next countdown pill missing for local sibling: %v", tt.Texts())
	}

	// Advance past end to trigger automatic transition to S01E02.mp4.
	tt.Advance(6 * time.Second)
	tt.Frame()
	if a.playingFilePath != ep2 || a.localPlaylistIdx != 1 {
		t.Fatalf("local auto-next did not advance to ep2: path=%q idx=%d opened=%v", a.playingFilePath, a.localPlaylistIdx, opened)
	}
	if ep1Entry, ok := a.history.findLocalByPath(ep1); !ok || !ep1Entry.finished() {
		t.Fatalf("previous local file completion was not recorded: %+v", a.history.entries)
	}

	// MediaPrevious command switches back to S01E01.mp4.
	a.handleMediaCommand(mygo.MediaPrevious, 0)
	if a.playingFilePath != ep1 || a.localPlaylistIdx != 0 {
		t.Fatalf("MediaPrevious did not switch back to ep1: path=%q idx=%d", a.playingFilePath, a.localPlaylistIdx)
	}
}
