package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSavedSourceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "source.json")
	if got, err := readSavedSource(path); err != nil || got.URL != "" {
		t.Fatalf("missing source: %+v, %v", got, err)
	}
	want := savedSource{URL: "https://example.com/config.json", Assets: "/tmp/example-assets"}
	if err := writeSavedSource(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readSavedSource(path)
	if err != nil || got != want {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("saved source is readable by others: %v", info.Mode())
	}
	if err := writeSavedSource(path, savedSource{URL: "https://example.com/other.json"}); err != nil {
		t.Fatal(err)
	}
	got, err = readSavedSource(path)
	if err != nil || got.URL != "https://example.com/other.json" || got.Assets != "" {
		t.Fatalf("replacement: %+v, %v", got, err)
	}
}
