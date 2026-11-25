package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo/ui"
)

const maxSeriesSkipSeconds = 3600

// A title keeps its own skip settings across episodes and sources. Zero
// seconds disables either end independently; Enabled pauses both settings.
type seriesSkipRule struct {
	Enabled bool `json:"enabled"`
	Opening int  `json:"opening_seconds"`
	Ending  int  `json:"ending_seconds"`
}

func (r seriesSkipRule) normalized() seriesSkipRule {
	r.Opening = min(max(r.Opening, 0), maxSeriesSkipSeconds)
	r.Ending = min(max(r.Ending, 0), maxSeriesSkipSeconds)
	return r
}

// A malformed or overlapping setting must not skip an entire short video.
func (r seriesSkipRule) fits(duration time.Duration) bool {
	return r.Enabled && duration > time.Duration(r.Opening+r.Ending)*time.Second
}

func (r seriesSkipRule) openingTarget(position, resume, duration time.Duration) time.Duration {
	if !r.fits(duration) || r.Opening <= 0 {
		return 0
	}
	opening := time.Duration(r.Opening) * time.Second
	if max(position, resume) >= opening {
		return 0
	}
	return opening
}

func (r seriesSkipRule) inEnding(position, duration time.Duration) bool {
	return r.fits(duration) && r.Ending > 0 && position >= duration-time.Duration(r.Ending)*time.Second
}

type seriesSkipStore struct {
	entries map[string]seriesSkipRule
	path    string
	readErr error
}

func (s *seriesSkipStore) get(name string) seriesSkipRule {
	return s.entries[titleKey(name)].normalized()
}

func (s *seriesSkipStore) set(name string, rule seriesSkipRule) error {
	if s.readErr != nil {
		return s.readErr
	}
	key := titleKey(name)
	if key == "" {
		return nil
	}
	entries := make(map[string]seriesSkipRule, len(s.entries)+1)
	for name, old := range s.entries {
		entries[name] = old
	}
	entries[key] = rule.normalized()
	if s.path != "" {
		if err := writeJSONFile(s.path, entries); err != nil {
			return err
		}
	}
	s.entries = entries
	return nil
}

func readSeriesSkips(path string) (map[string]seriesSkipRule, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries map[string]seriesSkipRule
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("读取本剧跳过设置：%w", err)
	}
	for name, rule := range entries {
		entries[name] = rule.normalized()
	}
	return entries, nil
}

func (a *app) restoreSeriesSkips() {
	sourcePath, err := savedSourcePath()
	if err != nil {
		a.seriesSkips.readErr = err
		return
	}
	a.seriesSkips.path = filepath.Join(filepath.Dir(sourcePath), "series-skips.json")
	a.seriesSkips.entries, a.seriesSkips.readErr = readSeriesSkips(a.seriesSkips.path)
	if a.seriesSkips.readErr != nil {
		a.status = a.seriesSkips.readErr.Error()
	}
}

type seriesSkipPlayback struct {
	player         mediaPlayer
	openingHandled bool
}

func (a *app) applyOpeningSkip(p mediaPlayer) {
	if !a.hasEpisodes() || a.running(jobPlay) {
		return
	}
	if a.skipPlayback.player != p {
		a.skipPlayback = seriesSkipPlayback{player: p}
	}
	if a.skipPlayback.openingHandled || p.Duration() <= 0 {
		return
	}
	rule := a.seriesSkips.get(a.title.name)
	if !rule.Enabled || rule.Opening == 0 {
		return
	}
	a.skipPlayback.openingHandled = true
	if target := rule.openingTarget(p.Position(), a.resumeAt, p.Duration()); target > 0 {
		a.resumeAt, a.resumeIssued = target, false
		a.showSkipUndo()
	}
}

func (a *app) skipEndingReady(p mediaPlayer) bool {
	return a.hasEpisodes() && p.Ready() && p.Playing() && !p.Buffering() && p.Err() == nil &&
		a.resumeAt == 0 && a.seriesSkips.get(a.title.name).inEnding(p.Position(), p.Duration())
}

// Skipping the credits completes this episode even if the next episode's
// resolution fails. Resuming later must then select the following episode.
func (a *app) recordEpisodeCompletion(p mediaPlayer) {
	if !a.hasEpisodes() || p.Duration() <= 0 {
		return
	}
	t := a.title
	now := time.Now()
	a.history.note(historyEntry{
		Name: t.name, Pic: t.detail.Pic, ConfigURL: configURL(t.item.Entry),
		EntryKey: entryKey(t.item.Entry), SiteKey: t.item.Entry.Site.Key, Kind: historyKindSite, Origin: t.item.Entry.Label(),
		VideoID: string(t.item.Video.ID), Line: a.playingSource, Episode: a.playingEpisode,
		Position: p.Duration().Seconds(), Duration: p.Duration().Seconds(), Updated: now,
	})
	a.history.noted = now
	a.flushHistory()
}

func (a *app) panelSeriesSkips(c *ui.Context) {
	name := a.title.name
	rule := a.seriesSkips.get(name)
	changed := false
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "本剧跳过片头片尾").Grow(1).FontSize(13)
		changed = ui.Switch(c, &rule.Enabled).Label("启用本剧跳过").Changed()
	})
	canStamp := a.player != nil && a.player.Duration() > 0
	stampedOpening := false
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "片头 / 秒").FontSize(12).Grow(1)
		opening := float64(rule.Opening)
		if ui.NumberInput(c, &opening, 0, maxSeriesSkipSeconds, 1).Label("跳过片头秒数").Width(116).Changed() {
			rule.Opening, changed = int(math.Round(opening)), true
			if rule.Opening > 0 {
				rule.Enabled = true
			}
		}
		if canStamp {
			if glassButton(c, "片头到这").Label("将当前进度设为片头").Tooltip("将当前播放位置设为跳过片头时长").Padding(4, 8).Clicked() {
				pos := min(int(a.player.Position().Seconds()), maxSeriesSkipSeconds)
				if pos > 0 {
					rule.Opening, rule.Enabled, changed, stampedOpening = pos, true, true, true
					a.showPlayerOSD(fmt.Sprintf("已设片头 %d 秒", pos))
				}
			}
		}
	})
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "片尾 / 秒").FontSize(12).Grow(1)
		ending := float64(rule.Ending)
		if ui.NumberInput(c, &ending, 0, maxSeriesSkipSeconds, 1).Label("跳过片尾秒数").Width(116).Changed() {
			rule.Ending, changed = int(math.Round(ending)), true
			if rule.Ending > 0 {
				rule.Enabled = true
			}
		}
		if canStamp {
			if glassButton(c, "片尾从这").Label("将剩余时长设为片尾").Tooltip("将当前位置到片尾的剩余时长设为跳过片尾").Padding(4, 8).Clicked() {
				rem := min(int(math.Round((a.player.Duration() - a.player.Position()).Seconds())), maxSeriesSkipSeconds)
				if rem > 0 {
					rule.Ending, rule.Enabled, changed = rem, true, true
					a.showPlayerOSD(fmt.Sprintf("已设片尾 %d 秒", rem))
				}
			}
		}
	})
	hint := "本剧分集沿用；片尾随自动下一集跳过"
	if rule.Enabled && rule.Ending > 0 && !a.autoNext {
		hint = "已关闭「自动下一集」，片尾暂不会自动跳过"
	}
	ui.Text(c, hint).FontSize(11).TextColor(paletteFor(c).textMuted)
	if changed {
		if err := a.seriesSkips.set(name, rule); err != nil {
			a.status = "保存本剧跳过设置失败：" + err.Error()
		} else {
			a.skipPlayback.openingHandled = stampedOpening
		}
	}
}
