package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

const sourceMemoryLimit = 200

func sourceMemoryItemKey(item library.Item) string {
	return entryKey(item.Entry) + "\x00" + string(item.Video.ID)
}

// A remembered source names the site and title, never an expiring media URL.
type rememberedSource struct {
	ConfigURL string `json:"config_url,omitempty"`
	EntryKey  string `json:"entry_key,omitempty"`
	SiteKey   string `json:"site_key,omitempty"`
	VideoID   string `json:"video_id,omitempty"`
	Line      string `json:"line,omitempty"`
}

func rememberedItem(item library.Item, line string) rememberedSource {
	return rememberedSource{ConfigURL: configURL(item.Entry), EntryKey: entryKey(item.Entry), SiteKey: item.Entry.Site.Key, VideoID: string(item.Video.ID), Line: line}
}

func (s rememberedSource) matches(item library.Item) bool {
	if s.VideoID == "" || s.VideoID != string(item.Video.ID) {
		return false
	}
	return s.EntryKey != "" && s.EntryKey == entryKey(item.Entry) || s.ConfigURL != "" && s.ConfigURL == configURL(item.Entry) && s.SiteKey != "" && s.SiteKey == item.Entry.Site.Key
}

type sourcePlaybackRecord struct {
	StartMS             int64     `json:"start_ms,omitempty"`
	Successes           int       `json:"successes,omitempty"`
	Failures            int       `json:"failures,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	LastSuccess         time.Time `json:"last_success,omitempty"`
	LastFailure         time.Time `json:"last_failure,omitempty"`
}

type sourceMemoryEntry struct {
	Preferred rememberedSource                `json:"preferred"`
	Records   map[string]sourcePlaybackRecord `json:"records,omitempty"`
	Updated   time.Time                       `json:"updated"`
}

type sourceMemoryStore struct {
	entries map[string]sourceMemoryEntry
	path    string
	readErr error
}

func (s *sourceMemoryStore) record(name string, item library.Item, line string, elapsed time.Duration, success bool, now time.Time) error {
	if s.readErr != nil {
		return s.readErr
	}
	key := titleKey(name)
	if key == "" || item.Video.ID == "" || entryKey(item.Entry) == "" {
		return nil
	}
	entries := make(map[string]sourceMemoryEntry, len(s.entries)+1)
	for k, e := range s.entries {
		entries[k] = e
	}
	e := entries[key]
	records := make(map[string]sourcePlaybackRecord, len(e.Records)+1)
	for k, r := range e.Records {
		records[k] = r
	}
	r := records[sourceMemoryItemKey(item)]
	if success {
		r.StartMS, r.Successes, r.ConsecutiveFailures, r.LastSuccess = max(0, elapsed.Milliseconds()), r.Successes+1, 0, now
		e.Preferred = rememberedItem(item, line)
	} else {
		r.Failures, r.ConsecutiveFailures, r.LastFailure = r.Failures+1, r.ConsecutiveFailures+1, now
	}
	records[sourceMemoryItemKey(item)] = r
	e.Records, e.Updated = records, now
	entries[key] = e
	if len(entries) > sourceMemoryLimit {
		oldest := ""
		for k, candidate := range entries {
			if k != key && (oldest == "" || candidate.Updated.Before(entries[oldest].Updated)) {
				oldest = k
			}
		}
		delete(entries, oldest)
	}
	if s.path != "" {
		if err := writeJSONFile(s.path, entries); err != nil {
			return err
		}
	}
	s.entries = entries
	return nil
}

func readSourceMemory(path string) (map[string]sourceMemoryEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries map[string]sourceMemoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("读取播放来源记录：%w", err)
	}
	return entries, nil
}

func (a *app) restoreSourceMemory() {
	path, err := savedSourcePath()
	if err != nil {
		a.sourceMemory.readErr = err
		return
	}
	a.sourceMemory.path = filepath.Join(filepath.Dir(path), "source-playback.json")
	a.sourceMemory.entries, a.sourceMemory.readErr = readSourceMemory(a.sourceMemory.path)
	if a.sourceMemory.readErr != nil {
		a.status = a.sourceMemory.readErr.Error()
	}
}

// When its configuration is already loaded the previous source is available
// immediately, including when a search result only contains a different site.
func (a *app) rememberedCandidate(name, pic string) (library.Item, bool) {
	s := a.sourceMemory.entries[titleKey(name)].Preferred
	if s.VideoID == "" || !a.configAvailable(s.ConfigURL) {
		return library.Item{}, false
	}
	doc := a.library.cached[s.ConfigURL]
	if root := a.library.root; root != nil && root.URL == s.ConfigURL {
		doc = root
	}
	if doc == nil {
		return library.Item{}, false
	}
	entry, ok := findSite(doc, s.EntryKey, s.SiteKey)
	if !ok {
		return library.Item{}, false
	}
	return library.Item{Entry: entry, Video: catalog.Video{ID: catalog.ID(s.VideoID), Name: name, Pic: pic}}, true
}

func (a *app) preferredLine(name string, item library.Item, detail catalog.Detail) int {
	s := a.sourceMemory.entries[titleKey(name)].Preferred
	if s.matches(item) {
		for i, line := range detail.Sources {
			if line.Name == s.Line {
				return i
			}
		}
	}
	return 0
}

// rememberedEpisode prefers the working line, then checks other lines for
// the same named or numbered episode. It never guesses from a list index.
func rememberedEpisode(detail catalog.Detail, preferredLine, name string) (line int, episode catalog.Episode, ok bool) {
	for i, source := range detail.Sources {
		if source.Name == preferredLine {
			if _, episode, ok := matchEpisode(catalog.Detail{Sources: []catalog.Source{source}}, name, -1); ok {
				return i, episode, true
			}
		}
	}
	return matchEpisode(detail, name, -1)
}

// An attempt belongs to one page and native player. Native readiness alone
// does not prove playback: a prepared player must also advance its position.
type sourcePlaybackAttempt struct {
	title     *titlePage
	item      library.Item
	line      string
	episode   string
	player    *ui.VideoPlayer
	started   time.Time
	position  time.Duration
	sampled   bool
	succeeded bool
	failed    bool
}

func (s *sourcePlaybackAttempt) observe(ready, buffering, playing bool, position time.Duration, err error) (success, failure bool) {
	if err != nil {
		if !s.failed {
			s.failed = true
			return false, true
		}
		return false, false
	}
	if s.succeeded || s.failed || !ready || buffering || !playing {
		return false, false
	}
	if !s.sampled || position < s.position {
		s.sampled, s.position = true, position
		return false, false
	}
	if position-s.position < 100*time.Millisecond {
		return false, false
	}
	s.succeeded = true
	return true, false
}

func (a *app) beginSourceAttempt(item library.Item, line, episode string) {
	if entryKey(item.Entry) == "" || item.Video.ID == "" {
		a.sourceAttempt = sourcePlaybackAttempt{}
		return
	}
	a.sourceAttempt = sourcePlaybackAttempt{title: a.title, item: item, line: line, episode: episode, started: time.Now()}
}

func (a *app) recordSourceAttempt(success bool, now time.Time) {
	s := &a.sourceAttempt
	if s.title == nil {
		return
	}
	// Best effort, like interface timing: storage errors never interrupt video.
	_ = a.sourceMemory.record(s.title.name, s.item, s.line, now.Sub(s.started), success, now)
}

func (a *app) failSourceAttempt() {
	if a.sourceAttempt.title == a.title && !a.sourceAttempt.failed {
		a.sourceAttempt.failed = true
		a.recordSourceAttempt(false, time.Now())
	}
}

func (a *app) observeSourcePlayback(now time.Time, p *ui.VideoPlayer, err error) {
	s := &a.sourceAttempt
	if s.title == nil || s.title != a.title || p != s.player || a.playingSource != s.line || a.playingEpisode != s.episode {
		return
	}
	if err == nil && (a.resumeAt > 0 || a.resumeIssued) {
		s.sampled = false
		return
	}
	success, failure := s.observe(p.Ready(), p.Buffering(), p.Playing(), p.Position(), err)
	if success || failure {
		a.recordSourceAttempt(success, now)
	}
}
