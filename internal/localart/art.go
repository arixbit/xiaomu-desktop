// Package localart reads sidecar posters and stores bounded on-disk artwork.
package localart

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"
)

const (
	maxImageBytes     = 6 << 20
	maxImagePixels    = 8 << 20
	maxImageDimension = 4096
)

var sidecarExtensions = [...]string{".jpg", ".jpeg", ".png", ".webp", ".JPG", ".JPEG", ".PNG", ".WEBP"}

// Sidecar finds a supported image next to a video with the same stem.
func Sidecar(videoPath string) ([]byte, error) {
	stem := strings.TrimSuffix(videoPath, filepath.Ext(videoPath))
	var firstErr error
	for _, ext := range sidecarExtensions {
		data, err := Read(stem + ext)
		if err == nil {
			return data, nil
		}
		if firstErr == nil && !errors.Is(err, fs.ErrNotExist) {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fs.ErrNotExist
}

// Read reads a regular raster image after checking its byte and pixel budgets.
func Read(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("封面必须是普通图片文件")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|preventBlocking, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, fmt.Errorf("封面文件已更改")
	}
	if info.Size() > maxImageBytes {
		return nil, fmt.Errorf("封面超过 %d 字节", maxImageBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if err := validate(data); err != nil {
		return nil, err
	}
	return data, nil
}

func validate(data []byte) error {
	if len(data) > maxImageBytes {
		return fmt.Errorf("封面超过 %d 字节", maxImageBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	switch format {
	case "jpeg", "png", "webp":
	default:
		return fmt.Errorf("封面格式不受支持")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageDimension || cfg.Height > maxImageDimension || cfg.Width > maxImagePixels/cfg.Height {
		return fmt.Errorf("封面尺寸 %d×%d 超出预算", cfg.Width, cfg.Height)
	}
	return nil
}
