package localart

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSidecarUsesSameStemAndSkipsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "示例.2024.1080p.mkv")
	data := fixturePNG(t, 2, 3)
	writeFixture(t, filepath.Join(dir, "other.png"), data)
	if _, err := Sidecar(video); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("absent sidecar returned %v", err)
	}
	writeFixture(t, filepath.Join(dir, "示例.2024.1080p.jpg"), []byte("invalid"))
	writeFixture(t, filepath.Join(dir, "示例.2024.1080p.PNG"), data)
	if got, err := Sidecar(video); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("sidecar returned %d bytes and %v", len(got), err)
	}
}

func TestReadRejectsImagesOutsideBudget(t *testing.T) {
	dir := t.TempDir()
	large := filepath.Join(dir, "large.png")
	writeFixture(t, large, fixturePNG(t, 2, 3))
	if err := os.Truncate(large, maxImageBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(large); err == nil {
		t.Fatal("oversized encoded data was accepted")
	}
	wide := filepath.Join(dir, "wide.png")
	writeFixture(t, wide, fixturePNG(t, maxImageDimension+1, 1))
	if _, err := Read(wide); err == nil {
		t.Fatal("oversized image dimensions were accepted")
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("non-file path was accepted")
	}
}

func TestCacheEvictsOldImageKeysAndKeepsNewPoster(t *testing.T) {
	dir := t.TempDir()
	oldest := filepath.Join(dir, strings.Repeat("a", 64))
	newer := filepath.Join(dir, strings.Repeat("b", 64))
	other := filepath.Join(dir, "keep.txt")
	for _, path := range []string{oldest, newer, other} {
		writeFixture(t, path, nil)
		if err := os.Truncate(path, cacheLimit/2); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldest, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	data := fixturePNG(t, 2, 3)
	key, err := Save(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("oldest entry should be evicted, got %v", err)
	}
	for _, path := range []string{newer, other} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := ReadCached(dir, key); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("cached poster returned %d bytes and %v", len(got), err)
	}
	if duplicate, err := Save(dir, data); err != nil || duplicate != key {
		t.Fatalf("saving the same poster returned (%q, %v), expected %q", duplicate, err, key)
	}
	if _, err := ReadCached(dir, "../"+key); err == nil {
		t.Fatal("cache accepted a traversal key")
	}
}

func fixturePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
