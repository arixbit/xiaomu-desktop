package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	maxSeriesSkipSeconds = 3600
	localSkipKeyPrefix   = "local-dir:"
)

func localSkipDirKey(filePath string) string {
	if strings.TrimSpace(filePath) == "" {
		return ""
	}
	dir := filepath.Clean(filepath.Dir(filePath))
	if dir == "" || dir == "." {
		return ""
	}
	return localSkipKeyPrefix + filepath.ToSlash(dir)
}

func skipStoreKey(name string) string {
	if strings.HasPrefix(name, localSkipKeyPrefix) {
		return name
	}
	return titleKey(name)
}

func (a *app) activeSkipKey() (key string, isLocal bool) {
	if a.hasEpisodes() && a.title != nil && strings.TrimSpace(a.title.name) != "" {
		return a.title.name, false
	}
	if a.hasLocalPlaylist() {
		if key := localSkipDirKey(a.playingFilePath); key != "" {
			return key, true
		}
	}
	return "", false
}

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
	return s.entries[skipStoreKey(name)].normalized()
}

func (s *seriesSkipStore) set(name string, rule seriesSkipRule) error {
	if s.readErr != nil {
		return s.readErr
	}
	key := skipStoreKey(name)
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
	key, _ := a.activeSkipKey()
	if key == "" || a.running(jobPlay) {
		return
	}
	if a.skipPlayback.player != p {
		a.skipPlayback = seriesSkipPlayback{player: p}
	}
	if a.skipPlayback.openingHandled || p.Duration() <= 0 {
		return
	}
	rule := a.seriesSkips.get(key)
	if !rule.Enabled || rule.Opening == 0 {
		return
	}
	a.skipPlayback.openingHandled = true
	if target := rule.openingTarget(p.Position(), a.resumeAt, p.Duration()); target > 0 {
		a.resumeAt, a.resumeIssued = target, false
		a.showSkipUndo(target)
	}
}

func (a *app) skipEndingReady(p mediaPlayer) bool {
	key, _ := a.activeSkipKey()
	return key != "" && p.Ready() && p.Playing() && !p.Buffering() && p.Err() == nil &&
		a.resumeAt == 0 && a.seriesSkips.get(key).inEnding(p.Position(), p.Duration())
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

func formatSkipClock(sec int) string {
	if sec < 0 {
		sec = 0
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

type skipInputLocal struct {
	text   string
	synced int
	inited bool
}

func skipSecondsInput(c *ui.Context, key string, value *int, label string, compact bool) bool {
	inW := float32(42)
	fontSize := float32(12)
	padY, padX := float32(3), float32(6)
	unitSize := float32(10.5)
	if compact {
		inW = 36
		fontSize = 11.5
		padY, padX = 2, 5
		unitSize = 10
	}
	box := ui.Row(c.Key(key+"-wrap")).Radius(7).Padding(padY, padX).Gap(3).AlignItems(ui.Center).
		Background(ui.RGBA(12, 14, 22, 0.65)).Shrink(0)
	st := ui.Local(box, "skip-sec", func() skipInputLocal {
		return skipInputLocal{text: strconv.Itoa(*value), synced: *value, inited: true}
	})
	if !st.inited || st.synced != *value {
		st.text = strconv.Itoa(*value)
		st.synced = *value
		st.inited = true
	}
	changed := false
	focused := false
	box.Children(func() {
		in := ui.TextInputBase(c.Key(key), &st.text).
			Label(label).Width(inW).TextAlign(ui.End).
			FontSize(fontSize).TextColor(playerText).
			SingleLine().Keyboard(ui.KeyboardNumber).MaxLength(4)
		focused = in.Focused()
		if in.Changed() {
			trimmed := strings.TrimSpace(st.text)
			if trimmed != "" {
				if n, err := strconv.Atoi(trimmed); err == nil {
					clamped := min(max(n, 0), maxSeriesSkipSeconds)
					if clamped != *value {
						*value = clamped
						st.synced = clamped
						changed = true
					}
				}
			}
		}
		if in.Shortcut(0, ui.KeyUp) {
			next := min(*value+1, maxSeriesSkipSeconds)
			if next != *value {
				*value = next
				st.synced = next
				st.text = strconv.Itoa(next)
				changed = true
			}
		}
		if in.Shortcut(0, ui.KeyDown) {
			next := max(*value-1, 0)
			if next != *value {
				*value = next
				st.synced = next
				st.text = strconv.Itoa(next)
				changed = true
			}
		}
		if !focused {
			if strings.TrimSpace(st.text) == "" && *value != 0 {
				*value = 0
				st.synced = 0
				changed = true
			}
			st.text = strconv.Itoa(*value)
			st.synced = *value
		}
		ui.Text(c, "秒").FontSize(unitSize).TextColor(playerMutedText).SingleLine().PassThrough()
	})
	border := ui.RGBA(255, 255, 255, 0.16)
	if focused {
		border = playerCoralBorder
	}
	box.Border(1, box.AnimateColor("skip-in-bd", border, animColor))
	return changed
}

func skipStampButton(c *ui.Context, key, text, tooltip string, disabled, medium, compact bool) ui.Element {
	padY, padX := float32(4), float32(7)
	fontSize := float32(11.5)
	if compact {
		padY, padX = 2, 5
		fontSize = 10.5
	} else if medium {
		padY, padX = 3, 6
		fontSize = 11
	}
	btn := ui.ButtonBase(c.Key(key)).Padding(padY, padX).Radius(7).Center().
		Cursor(ui.CursorPointer).Label(text).Tooltip(tooltip).Disabled(disabled).Shrink(0)
	bg := ui.RGBA(255, 255, 255, 0.06)
	border := ui.RGBA(255, 255, 255, 0.13)
	fg := playerText
	if disabled {
		fg = ui.RGBA(248, 249, 255, 0.34)
		border = ui.RGBA(255, 255, 255, 0.08)
	} else if btn.Hovered() {
		bg = ui.RGBA(255, 255, 255, 0.14)
		border = ui.RGBA(255, 255, 255, 0.24)
	}
	fgAnim := animateButtonChrome(btn, "skip-stamp", bg, border, fg)
	applyPressPose(btn)
	btn.Children(func() {
		ui.Text(c, text).FontSize(fontSize).TextColor(fgAnim).SingleLine().PassThrough()
	})
	return btn
}

func (a *app) panelSeriesSkipsInline(c *ui.Context, panelContentH float32) {
	name, isLocal := a.activeSkipKey()
	if name == "" {
		return
	}
	rule := a.seriesSkips.get(name)
	changed := false
	stampedOpening := false
	dur := time.Duration(0)
	if a.player != nil {
		dur = a.player.Duration()
	}
	canStamp := a.player != nil && dur > 0

	compact := panelContentH <= 285
	medium := !compact && panelContentH <= 420
	headingSize := float32(13)
	rowSize := float32(12)
	metaSize := float32(11)
	editorGap := float32(7)
	foldPadY := float32(4)
	if compact {
		headingSize = 12
		rowSize = 11.5
		metaSize = 10.5
		editorGap = 3
		foldPadY = 2
	} else if medium {
		headingSize = 12.5
		rowSize = 12
		metaSize = 11
		editorGap = 4
		foldPadY = 3
	}

	summary := "已关闭"
	if rule.Enabled {
		if rule.Opening > 0 || rule.Ending > 0 {
			summary = formatSkipClock(rule.Opening) + " / " + formatSkipClock(rule.Ending)
		} else {
			summary = "待设时间"
		}
	}

	ui.Column(c.Key("panel-skip-inline")).FillWidth().Gap(editorGap).Shrink(0).Children(func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			foldBtn := ui.ButtonBase(c.Key("skip-fold-toggle")).Grow(1).MinWidth(0).
				Padding(foldPadY, 0).Gap(5).Justify(ui.Start).AlignItems(ui.Center).
				Cursor(ui.CursorPointer).Label("片头片尾").Expanded(a.panelSkipOpen)
			fg := playerText
			if foldBtn.Hovered() {
				fg = ui.RGB(255, 182, 159)
			}
			animFg := foldBtn.AnimateColor("skip-fold-fg", fg, animColor)
			applyPressPose(foldBtn)
			foldBtn.Children(func() {
				ui.Text(c, "片头片尾").FontSize(headingSize).Bold().TextColor(animFg).SingleLine().PassThrough()
				if !a.panelSkipOpen && !compact {
					ui.Spacer(c)
					ui.Text(c.Key("skip-summary"), summary).FontSize(metaSize).FontFeatures("tnum").
						TextColor(playerMutedText).SingleLine().Transition(transMicroBadge).PassThrough()
				}
				chev := ui.Icon(c.Key("skip-fold-chev"), iconChevronDown).Size(13, 13).TextColor(playerMutedText).PassThrough()
				applyChevronPose(chev, a.panelSkipOpen)
			})
			if foldBtn.Clicked() {
				a.panelSkipOpen = !a.panelSkipOpen
				a.panelRevealEpisode = true
			}
			if a.panelSkipOpen {
				ui.Row(c.Key("skip-auto-row")).Gap(5).AlignItems(ui.Center).Shrink(0).Transition(transMicroBadge).Children(func() {
					ui.Text(c, "自动跳过").FontSize(metaSize).TextColor(playerMutedText).SingleLine()
					if ui.Switch(c, &rule.Enabled).Label("启用本剧跳过").Changed() {
						changed = true
					}
				})
			}
		})

		if a.panelSkipOpen {
			editorPadY := float32(6)
			if compact {
				editorPadY = 2
			} else if medium {
				editorPadY = 3
			}
			ui.Column(c.Key("panel-skip-editor")).FillWidth().Gap(editorGap).Transition(transInlineReveal).Children(func() {
				ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, 0.08)).Shrink(0).PassThrough()
				ui.Column(c).FillWidth().Gap(editorGap).Padding(editorPadY, 0, editorPadY, 0).Children(func() {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "片头").FontSize(rowSize).TextColor(ui.RGBA(237, 238, 241, 0.92)).SingleLine().Grow(1)
						if skipSecondsInput(c, "skip-opening-input", &rule.Opening, "跳过片头秒数", compact) {
							changed = true
							if rule.Opening > 0 {
								rule.Enabled = true
							}
						}
						if skipStampButton(c, "skip-mark-intro", "片头到这", "将当前播放位置设为跳过片头时长", !canStamp, medium, compact).Clicked() {
							pos := min(int(math.Round(a.player.Position().Seconds())), maxSeriesSkipSeconds)
							if pos > 0 {
								rule.Opening, rule.Enabled, changed, stampedOpening = pos, true, true, true
								a.showPlayerOSD(fmt.Sprintf("已设片头 %d 秒", pos))
							}
						}
					})
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "片尾").FontSize(rowSize).TextColor(ui.RGBA(237, 238, 241, 0.92)).SingleLine().Grow(1)
						if skipSecondsInput(c, "skip-ending-input", &rule.Ending, "跳过片尾秒数", compact) {
							changed = true
							if rule.Ending > 0 {
								rule.Enabled = true
							}
						}
						if skipStampButton(c, "skip-mark-outro", "片尾从这", "将当前位置到片尾的剩余时长设为跳过片尾", !canStamp, medium, compact).Clicked() {
							rem := min(int(math.Round((dur - a.player.Position()).Seconds())), maxSeriesSkipSeconds)
							if rem > 0 {
								rule.Ending, rule.Enabled, changed = rem, true, true
								a.showPlayerOSD(fmt.Sprintf("已设片尾 %d 秒", rem))
							}
						}
					})
					hint := "填入秒数即启用，本剧各集沿用"
					if isLocal {
						hint = "填入秒数即启用，同目录各集沿用"
					}
					hintColor := playerMutedText
					if rule.Opening > 0 || rule.Ending > 0 {
						hint = "本剧各集沿用"
						if isLocal {
							hint = "同目录各集沿用"
						}
					}
					if rule.Enabled && dur > 0 && time.Duration(rule.Opening+rule.Ending)*time.Second >= dur {
						hint = "片头和片尾合计需短于本集时长"
						hintColor = ui.RGB(255, 198, 130)
					} else if rule.Enabled && rule.Ending > 0 && !a.autoNext {
						hint = "开启「播完自动下一集」后会跳过片尾"
					}
					noteSize := float32(11)
					if compact {
						noteSize = 10
					}
					ui.Text(c, hint).FontSize(noteSize).TextColor(hintColor).SingleLine().ContentTransition(ctReplace)
				})
			})
		}
	})

	if changed {
		if err := a.seriesSkips.set(name, rule); err != nil {
			a.status = "保存本剧跳过设置失败：" + err.Error()
		} else {
			a.skipPlayback.openingHandled = stampedOpening
		}
	}
}
