//go:build darwin || linux

package localart

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadRejectsFifoAndSymlink(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo.png")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(fifo); err == nil {
		t.Fatal("FIFO was accepted")
	}
	valid := filepath.Join(dir, "valid.png")
	writeFixture(t, valid, fixturePNG(t, 2, 3))
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link); err == nil {
		t.Fatal("symlink was accepted")
	}
}
