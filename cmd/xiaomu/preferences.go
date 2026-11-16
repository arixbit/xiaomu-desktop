package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

type playerPreferences struct {
	Volume   float64 `json:"volume"`
	Rate     float64 `json:"rate"`
	AutoNext bool    `json:"auto_next"`
}

func defaultPlayerPreferences() playerPreferences {
	return playerPreferences{Volume: 1, Rate: 1, AutoNext: true}
}

func readPlayerPreferences(path string) (playerPreferences, error) {
	p := defaultPlayerPreferences()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return defaultPlayerPreferences(), fmt.Errorf("读取播放偏好：%w", err)
	}
	if math.IsNaN(p.Volume) || p.Volume < 0 || p.Volume > 1 {
		p.Volume = 1
	}
	switch p.Rate {
	case 1, 1.25, 1.5, 2:
	default:
		p.Rate = 1
	}
	return p, nil
}

func (a *app) restorePreferences() {
	a.preferences = defaultPlayerPreferences()
	a.autoNext = a.preferences.AutoNext
	sourcePath, err := savedSourcePath()
	if err != nil {
		return
	}
	a.preferencesPath = filepath.Join(filepath.Dir(sourcePath), "preferences.json")
	a.preferences, err = readPlayerPreferences(a.preferencesPath)
	a.autoNext = a.preferences.AutoNext
	if err != nil {
		a.status = err.Error()
	}
}

func (a *app) ensurePreferences() {
	if a.preferences.Rate == 0 {
		a.preferences = defaultPlayerPreferences()
	}
}

func (a *app) savePreferences() {
	a.ensurePreferences()
	a.preferences.AutoNext = a.autoNext
	if a.preferencesPath != "" {
		if err := writeJSONFile(a.preferencesPath, a.preferences); err != nil {
			a.status = "保存播放偏好失败：" + err.Error()
		}
	}
}

func (a *app) applyPlayerPreferences(p *ui.VideoPlayer) {
	if p == nil {
		return
	}
	a.ensurePreferences()
	p.SetVolume(a.preferences.Volume)
	p.SetRate(a.preferences.Rate)
}
