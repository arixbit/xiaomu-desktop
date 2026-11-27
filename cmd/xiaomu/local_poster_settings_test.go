package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestLocalPosterSettingsSaveCancelAndClear(t *testing.T) {
	const token = "fixture-token"
	a := &app{
		nav:             []routeKind{routeHistory},
		preferences:     defaultPlayerPreferences(),
		preferencesPath: filepath.Join(t.TempDir(), "preferences.json"),
		autoNext:        true,
	}
	a.history.note(historyEntry{Name: "演示影片", Kind: historyKindSite})
	defer a.close()
	tt := ui.NewTester(a.view, 800, 520)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}

	click("封面设置")
	if !tt.HasText("This product uses the TMDB API but is not endorsed or certified by TMDB.") {
		t.Fatal("TMDB attribution missing")
	}
	click("TMDB Read Access Token")
	tt.Type("  " + token + "  ")
	click("取消")
	if a.preferences.TMDBToken != "" || a.localPoster.settings != (localPosterSettingsState{}) {
		t.Fatal("cancel retained the token draft")
	}

	click("封面设置")
	click("TMDB Read Access Token")
	tt.Type("  " + token + "  ")
	click("保存")
	prefs, err := readPlayerPreferences(a.preferencesPath)
	if err != nil || prefs.TMDBToken != token || !prefs.AutoNext {
		t.Fatal("trimmed token and existing settings should round-trip")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(a.preferencesPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("preferences permissions = %v, want 0600", info.Mode().Perm())
		}
	}

	click("封面设置")
	click("TMDB Read Access Token")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(0, ui.KeyBackspace)
	click("保存")
	prefs, err = readPlayerPreferences(a.preferencesPath)
	if err != nil || prefs.TMDBToken != "" || a.preferences.TMDBToken != "" {
		t.Fatal("saving an empty token should disable online matching")
	}
}

func TestLocalPosterSettingsSaveFailureKeepsPreviousToken(t *testing.T) {
	a := &app{
		nav:             []routeKind{routeHistory},
		preferences:     defaultPlayerPreferences(),
		preferencesPath: t.TempDir(), // Replacing a directory with JSON fails.
	}
	a.preferences.TMDBToken = "fixture-original"
	a.openLocalPosterSettings()
	a.localPoster.settings.token = "fixture-replacement"
	defer a.close()
	tt := ui.NewTester(a.view, 800, 520)
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if !a.localPoster.settings.open || !tt.HasText("保存失败，请检查应用数据目录权限。") {
		t.Fatal("save failure should leave the settings open with an error")
	}
	if a.preferences.TMDBToken != "fixture-original" {
		t.Fatal("failed save should retain the previous token")
	}
}

func TestLocalPosterSettingsWorkerNoTokenInput(t *testing.T) {
	a := &app{
		nav:             []routeKind{routeHistory},
		preferences:     defaultPlayerPreferences(),
		preferencesPath: filepath.Join(t.TempDir(), "preferences.json"),
	}
	a.localPoster.workerURL = "https://example.com"
	a.preferences.TMDBToken = "fixture-legacy"
	a.openLocalPosterSettings()
	defer a.close()
	tt := ui.NewTester(a.view, 800, 520)
	if _, ok := tt.Find("TMDB Read Access Token"); ok || a.localPoster.settings.token != "" {
		t.Fatal("Worker settings exposed the legacy personal token form")
	}
	if !tt.HasText("This product uses the TMDB API but is not endorsed or certified by TMDB.") {
		t.Fatal("TMDB attribution missing")
	}
	if err := tt.Click("自动匹配本地视频封面"); err != nil {
		t.Fatal(err)
	}
	prefs, err := readPlayerPreferences(a.preferencesPath)
	if err != nil || !prefs.DisableAutoLocalPoster || !a.localPosterOnlineAvailable() {
		t.Fatalf("automatic lookup should be disabled while manual search stays available: %v", err)
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if a.localPoster.settings.open || a.preferences.TMDBToken != "fixture-legacy" {
		t.Fatal("closing Worker settings should preserve the fallback preference")
	}
}
