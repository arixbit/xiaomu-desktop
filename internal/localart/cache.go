package localart

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const cacheLimit = 80 << 20

var saveMu sync.Mutex

// Save stores a poster under a content hash and returns its cache key.
func Save(dir string, data []byte) (string, error) {
	if err := validate(data); err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	key := hex.EncodeToString(digest[:])

	saveMu.Lock()
	defer saveMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, key)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("封面缓存位置不可用")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	budget := cacheLimit - int64(len(data))
	if existing, err := ReadCached(dir, key); err == nil && bytes.Equal(existing, data) {
		return key, prune(dir, key, budget)
	}
	f, err := os.CreateTemp(dir, ".poster-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", err
	}
	if err := prune(dir, key, budget); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return key, nil
}

// ReadCached loads a poster by its cache key; keys cannot contain a path.
func ReadCached(dir, key string) ([]byte, error) {
	if !validKey(key) {
		return nil, fmt.Errorf("封面缓存键无效")
	}
	return Read(filepath.Join(dir, key))
}

func validKey(key string) bool {
	return len(key) == sha256.Size*2 && !strings.ContainsFunc(key, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f')
	})
}

func prune(dir, keep string, limit int64) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []fs.FileInfo
	var size int64
	for _, entry := range entries {
		if !validKey(entry.Name()) || !entry.Type().IsRegular() || entry.Name() == keep {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files = append(files, info)
			size += info.Size()
		}
	}
	slices.SortFunc(files, func(a, b fs.FileInfo) int { return a.ModTime().Compare(b.ModTime()) })
	for _, info := range files {
		if size <= limit {
			break
		}
		if err := os.Remove(filepath.Join(dir, info.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		size -= info.Size()
	}
	return nil
}
