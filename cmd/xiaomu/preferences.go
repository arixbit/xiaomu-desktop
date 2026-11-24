package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

type subtitlePreferences struct {
	Scale         float64 `json:"scale"`
	Pos           int     `json:"pos"`
	PreferChinese bool    `json:"prefer_chinese"`
	ASSRTToken    string  `json:"assrt_token,omitempty"`
}

func defaultSubtitlePreferences() subtitlePreferences {
	return subtitlePreferences{Scale: 1.0, Pos: 100, PreferChinese: true}
}

func (p subtitlePreferences) normalized() subtitlePreferences {
	if math.IsNaN(p.Scale) || p.Scale < 0.5 || p.Scale > 2.0 {
		p.Scale = 1.0
	}
	if p.Pos < 50 || p.Pos > 100 {
		p.Pos = 100
	}
	p.ASSRTToken = strings.TrimSpace(p.ASSRTToken)
	return p
}

type playerPreferences struct {
	Volume   float64             `json:"volume"`
	Rate     float64             `json:"rate"`
	AutoNext bool                `json:"auto_next"`
	Danmaku  danmakuPreferences  `json:"danmaku"`
	Subtitle subtitlePreferences `json:"subtitle"`
}

func defaultPlayerPreferences() playerPreferences {
	return playerPreferences{
		Volume:   1,
		Rate:     1,
		AutoNext: true,
		Danmaku:  defaultDanmakuPreferences(),
		Subtitle: defaultSubtitlePreferences(),
	}
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
	p.Danmaku = p.Danmaku.normalized()
	p.Subtitle = p.Subtitle.normalized()
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
	a.preferences.Danmaku = a.preferences.Danmaku.normalized()
	a.preferences.Subtitle = a.preferences.Subtitle.normalized()
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

func (a *app) applyPlayerPreferences(p mediaPlayer) {
	if p == nil {
		return
	}
	a.ensurePreferences()
	p.SetVolume(a.preferences.Volume)
	p.SetRate(a.preferences.Rate)
	a.applySubtitlePreferences(p)
}
