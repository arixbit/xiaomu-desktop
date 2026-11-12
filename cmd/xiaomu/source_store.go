package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
)

// savedSource is a user-imported address. It lives only in the app's user
// data directory; the app ships with no source address.
type savedSource struct {
	URL     string `json:"url"`
	Assets  string `json:"assets,omitempty"`
	LiveURL string `json:"live_url,omitempty"`
}

func savedSourcePath() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "source.json"), nil
}

func readSavedSource(path string) (savedSource, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return savedSource{}, nil
	}
	if err != nil {
		return savedSource{}, err
	}
	var source savedSource
	if err := json.Unmarshal(data, &source); err != nil {
		return savedSource{}, fmt.Errorf("读取已保存片源: %w", err)
	}
	return source, nil
}

func writeSavedSource(path string, source savedSource) error {
	return writeJSONFile(path, source)
}

// writeJSONFile replaces path with v in one step, so a crash keeps the old file.
func writeJSONFile(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".source-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (a *app) saveImportedSource(raw string) error {
	path, err := savedSourcePath()
	if err != nil {
		return err
	}
	saved, err := readSavedSource(path)
	if err != nil {
		return err
	}
	saved.URL, saved.Assets = raw, a.browser.assets
	if a.live.url == "" {
		saved.LiveURL = ""
	}
	return writeSavedSource(path, saved)
}

func (a *app) saveLiveSource(raw string) error {
	path, err := savedSourcePath()
	if err != nil {
		return err
	}
	saved, err := readSavedSource(path)
	if err != nil {
		return err
	}
	saved.LiveURL = raw
	return writeSavedSource(path, saved)
}

func (a *app) restoreSavedSource() {
	path, err := savedSourcePath()
	if err != nil {
		a.status = "读取本机配置目录失败：" + err.Error()
		return
	}
	source, err := readSavedSource(path)
	if err != nil {
		a.status = err.Error()
		return
	}
	if source.LiveURL != "" {
		a.live.url, a.live.input, a.live.restore = source.LiveURL, source.LiveURL, true
	}
	if source.URL != "" {
		a.browser.configURL, a.browser.assets = source.URL, source.Assets
		a.readConfig(source.URL, true)
	} else if source.LiveURL != "" {
		a.switchTab(routeLive)
	}
}
