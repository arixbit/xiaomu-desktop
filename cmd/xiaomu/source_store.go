package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo"
)

// savedSource is a user-imported address. It lives only in the app's user
// data directory; the app ships with no source address.
type savedSource struct {
	URL     string `json:"url"`
	Assets  string `json:"assets,omitempty"`
	LiveURL string `json:"live_url,omitempty"`
}

// cachedConfig preserves config.Config fields that carry json:"-" so a
// locally cached depot or line restores identically without hitting the network.
type cachedConfig struct {
	Config    config.Config `json:"config"`
	FromDepot bool          `json:"from_depot,omitempty"`
	Line      string        `json:"line,omitempty"`
	Lines     []config.Line `json:"lines,omitempty"`
}

// cachedSourceConfig is the local snapshot of the imported VOD configuration,
// any depot sub-documents loaded from it, and any channels/EPG loaded from its lives.
type cachedSourceConfig struct {
	URL          string                    `json:"url"`
	Root         *cachedConfig             `json:"root"`
	Docs         map[string]*cachedConfig  `json:"docs,omitempty"`
	LiveChannels map[string][]iptv.Channel `json:"live_channels,omitempty"`
	LiveEPG      map[string]string         `json:"live_epg,omitempty"`
}

// cachedLiveSource is the local snapshot of a separately imported live source
// and any external playlists/EPG resolved from it.
type cachedLiveSource struct {
	URL      string                    `json:"url"`
	Sources  []iptv.Source             `json:"sources"`
	Channels map[string][]iptv.Channel `json:"channels,omitempty"`
	EPG      map[string]string         `json:"epg,omitempty"`
}

func savedSourcePath() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "source.json"), nil
}

func (a *app) resolvedSourcePath() (string, error) {
	if a.sourcePath != "" {
		return a.sourcePath, nil
	}
	return savedSourcePath()
}

func configCachePath(sourcePath string) string {
	return filepath.Join(filepath.Dir(sourcePath), "source-config.json")
}

func liveCachePath(sourcePath string) string {
	return filepath.Join(filepath.Dir(sourcePath), "source-live.json")
}

func removeFileIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func snapshotConfig(doc *config.Config) *cachedConfig {
	if doc == nil {
		return nil
	}
	return &cachedConfig{
		Config:    *doc,
		FromDepot: doc.FromDepot,
		Line:      doc.Line,
		Lines:     append([]config.Line(nil), doc.Lines...),
	}
}

func (c *cachedConfig) toConfig() *config.Config {
	if c == nil {
		return nil
	}
	doc := c.Config
	doc.FromDepot = c.FromDepot
	doc.Line = c.Line
	doc.Lines = append([]config.Line(nil), c.Lines...)
	if len(doc.Sites) > 0 {
		doc.Sites = append([]config.Site(nil), doc.Sites...)
		for i := range doc.Sites {
			if doc.Sites[i].Kind == "" || doc.Sites[i].Kind.Unsupported() {
				doc.Sites[i].Kind = config.Classify(doc.Sites[i].Type, doc.Sites[i].API, doc.Sites[i].Ext, "")
			}
		}
	}
	return &doc
}

func snapshotSourceConfig(raw string, root *config.Config, docs map[string]*config.Config, liveChannels map[string][]iptv.Channel, liveEPG map[string]string) cachedSourceConfig {
	snap := cachedSourceConfig{
		URL:          raw,
		Root:         snapshotConfig(root),
		LiveChannels: cloneLiveChannelsMap(liveChannels),
		LiveEPG:      cloneStringMap(liveEPG),
	}
	if len(docs) > 0 {
		copied := make(map[string]*cachedConfig, len(docs))
		for k, doc := range docs {
			if doc == nil || doc == root {
				continue
			}
			copied[k] = snapshotConfig(doc)
		}
		if len(copied) > 0 {
			snap.Docs = copied
		}
	}
	return snap
}

func (c cachedSourceConfig) restore() (*config.Config, map[string]*config.Config, map[string][]iptv.Channel, map[string]string) {
	root := c.Root.toConfig()
	docs := map[string]*config.Config{}
	byURL := map[string]*config.Config{}
	if root != nil && root.URL != "" {
		docs[root.URL] = root
		byURL[root.URL] = root
	}
	keys := make([]string, 0, len(c.Docs))
	for k := range c.Docs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		snap := c.Docs[k]
		if snap == nil {
			continue
		}
		if snap.Config.URL != "" {
			if existing := byURL[snap.Config.URL]; existing != nil {
				docs[k] = existing
				continue
			}
		}
		doc := snap.toConfig()
		docs[k] = doc
		if doc.URL != "" {
			byURL[doc.URL] = doc
			if docs[doc.URL] == nil {
				docs[doc.URL] = doc
			}
		}
	}
	return root, docs, cloneLiveChannelsMap(c.LiveChannels), cloneStringMap(c.LiveEPG)
}

func readCachedSourceConfig(path, expectedURL string) (cachedSourceConfig, bool) {
	if expectedURL == "" {
		return cachedSourceConfig{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cachedSourceConfig{}, false
	}
	var cached cachedSourceConfig
	if err := json.Unmarshal(data, &cached); err != nil {
		return cachedSourceConfig{}, false
	}
	if cached.URL != expectedURL || cached.Root == nil {
		return cachedSourceConfig{}, false
	}
	if len(cached.Root.Config.Sites) == 0 && len(cached.Root.Lines) == 0 {
		return cachedSourceConfig{}, false
	}
	return cached, true
}

func readCachedLiveSource(path, expectedURL string) (cachedLiveSource, bool) {
	if expectedURL == "" {
		return cachedLiveSource{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cachedLiveSource{}, false
	}
	var cached cachedLiveSource
	if err := json.Unmarshal(data, &cached); err != nil {
		return cachedLiveSource{}, false
	}
	if cached.URL != expectedURL || len(cached.Sources) == 0 {
		return cachedLiveSource{}, false
	}
	return cached, true
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

func (a *app) saveConfigCache() error {
	if a.browser.configURL == "" || a.library.root == nil {
		return nil
	}
	path, err := a.resolvedSourcePath()
	if err != nil {
		return err
	}
	snap := snapshotSourceConfig(a.browser.configURL, a.library.root, a.library.cached, a.library.liveChannels, a.library.liveEPG)
	return writeJSONFile(configCachePath(path), snap)
}

func (a *app) saveLiveCache() error {
	if a.live.url == "" || len(a.live.sources) == 0 {
		return nil
	}
	path, err := a.resolvedSourcePath()
	if err != nil {
		return err
	}
	snap := cachedLiveSource{
		URL:      a.live.url,
		Sources:  a.live.sources,
		Channels: cloneLiveChannelsMap(a.live.cachedChannels),
		EPG:      cloneStringMap(a.live.cachedEPG),
	}
	return writeJSONFile(liveCachePath(path), snap)
}

func (a *app) saveImportedSource(raw string) error {
	path, err := a.resolvedSourcePath()
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
	if err := writeSavedSource(path, saved); err != nil {
		return err
	}
	if raw == "" {
		return removeFileIfExists(configCachePath(path))
	}
	return a.saveConfigCache()
}

func (a *app) saveLiveSource(raw string) error {
	path, err := a.resolvedSourcePath()
	if err != nil {
		return err
	}
	saved, err := readSavedSource(path)
	if err != nil {
		return err
	}
	saved.LiveURL = raw
	if err := writeSavedSource(path, saved); err != nil {
		return err
	}
	if raw == "" {
		return removeFileIfExists(liveCachePath(path))
	}
	return a.saveLiveCache()
}

func (a *app) restoreSavedSource() {
	path, err := a.resolvedSourcePath()
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
		a.live.url, a.live.input = source.LiveURL, source.LiveURL
		if cached, ok := readCachedLiveSource(liveCachePath(path), source.LiveURL); ok {
			a.live.cachedChannels = cloneLiveChannelsMap(cached.Channels)
			a.live.cachedEPG = cloneStringMap(cached.EPG)
			a.setLiveSources(cached.Sources)
			a.live.restore = false
		} else {
			a.live.restore = true
		}
	}
	if source.URL != "" {
		a.browser.configURL, a.browser.assets = source.URL, source.Assets
		if cached, ok := readCachedSourceConfig(configCachePath(path), source.URL); ok {
			a.applyCachedConfig(source.URL, cached)
		} else {
			a.readConfig(source.URL, true)
		}
	} else if source.LiveURL != "" {
		a.switchTab(routeLive)
	}
}
