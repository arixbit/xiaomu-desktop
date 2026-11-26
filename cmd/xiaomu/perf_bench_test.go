package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/douban"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/netimage"
)

func makeBenchLibraryEvents(sites, perSite int) (*config.Config, []library.Event) {
	doc := &config.Config{URL: "https://example.com/config.json"}
	ext := strings.Repeat(`{"rule":"value"}`, 32) // ~512B typical rule/ext string
	events := make([]library.Event, sites)
	for site := range events {
		entry := library.Entry{
			Config: doc,
			Line:   "示例线路",
			Site: config.Site{
				Key:  fmt.Sprintf("site-%d", site),
				Name: fmt.Sprintf("站点 %d", site),
				API:  fmt.Sprintf("https://example.com/api/%d", site),
				Ext:  ext,
				Kind: config.KindJSON,
			},
		}
		videos := make([]catalog.Video, perSite)
		for i := range videos {
			id := fmt.Sprintf("%d-%d", site, i)
			name := fmt.Sprintf("合成剧集 %d", i%500)
			if i == 0 {
				name = "合成"
			}
			videos[i] = catalog.Video{ID: catalog.ID(id), Name: name}
		}
		events[site].Result = &library.Result{
			Target: library.Target{Entry: entry},
			Page:   catalog.Page{Videos: videos},
		}
	}
	return doc, events
}

func BenchmarkApplyLibraryEvents10k(b *testing.B) {
	doc, events := makeBenchLibraryEvents(100, 100)
	a := &app{library: libraryBrowser{root: doc, cached: map[string]*config.Config{}}}
	defer a.close()
	s := &a.library.search
	// Warm up slice capacity like repeated user refreshes.
	a.startScan(s, "合成", false)
	a.applyLibraryEvents(s, events)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		a.startScan(s, "合成", false)
		a.applyLibraryEvents(s, events)
	}
}

func BenchmarkCandidatesWith10kSearch(b *testing.B) {
	doc, events := makeBenchLibraryEvents(100, 100)
	a := &app{library: libraryBrowser{root: doc, cached: map[string]*config.Config{}}}
	defer a.close()
	s := &a.library.search
	a.startScan(s, "合成", false)
	a.applyLibraryEvents(s, events)
	a.title = &titlePage{
		name:   "合成",
		seeds:  append([]library.Item(nil), s.groups[0].items[:min(5, len(s.groups[0].items))]...),
		failed: map[string]string{},
		item:   s.groups[0].items[0],
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got := a.candidates()
		if len(got) == 0 {
			b.Fatal("empty candidates")
		}
	}
}

func BenchmarkLiveFilteredChannels2k(b *testing.B) {
	channels := make([]iptv.Channel, 2000)
	for i := range channels {
		channels[i] = iptv.Channel{
			Name:    fmt.Sprintf("CCTV-%04d 高清频道", i),
			Group:   fmt.Sprintf("分组 %d", i%20),
			Streams: []iptv.Stream{{URL: fmt.Sprintf("https://example.com/live/%d.m3u8", i)}},
		}
	}
	a := &app{nav: []routeKind{routeLive}}
	defer a.close()
	a.live.sources = []iptv.Source{{Name: "测试直播"}}
	a.live.selectedSource = 0
	a.live.channels = channels
	a.live.query = "cctv"
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tt.Frame()
	}
}

func BenchmarkEpisodeButtonLabel(b *testing.B) {
	samples := []string{"第1集", "第012集", "第36集 完结", "第40集 已完结", "05", "108", "正片", "番外篇 01"}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, s := range samples {
			_ = episodeButtonLabel(s)
		}
	}
}

func BenchmarkPosterCacheHit(b *testing.B) {
	a := &app{}
	defer a.close()
	var got ui.ImageSource
	tt := ui.NewTester(func(c *ui.Context) {
		got = a.poster(c, "")
	}, 100, 100)
	_ = got
	_ = tt
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = netimage.Load(nil, "https://example.com/nonexistent-bench.png", netimage.Options{MaxSide: posterMaxSide})
	}
}

func BenchmarkSearchSuggestions(b *testing.B) {
	a := &app{}
	defer a.close()
	for i := range 10 {
		a.searchHistory.queries = append(a.searchHistory.queries, fmt.Sprintf("历史片名 %d", i))
	}
	for i := range 24 {
		a.discovery.movie.feed.Subjects = append(a.discovery.movie.feed.Subjects, douban.Subject{ID: fmt.Sprint(i), Title: fmt.Sprintf("热播电影 %d", i)})
		a.discovery.tv.feed.Subjects = append(a.discovery.tv.feed.Subjects, douban.Subject{ID: fmt.Sprint(i + 100), Title: fmt.Sprintf("热播剧集 %d", i)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got := a.searchSuggestions()
		if len(got) == 0 {
			b.Fatal("empty suggestions")
		}
	}
}

func BenchmarkFavoritesView30x200(b *testing.B) {
	a := &app{nav: []routeKind{routeFavorites}}
	defer a.close()
	now := time.Now()
	for i := range 200 {
		a.history.entries = append(a.history.entries, historyEntry{
			Name:     fmt.Sprintf("剧集 %d", i),
			Episode:  fmt.Sprintf("第%d集", i%20+1),
			Position: 300,
			Duration: 2400,
			Updated:  now.Add(-time.Duration(i) * time.Minute),
		})
	}
	for i := range 30 {
		a.favorites.entries = append(a.favorites.entries, favoriteEntry{
			Name:          fmt.Sprintf("剧集 %d", i*5),
			EpisodeCount:  24,
			LatestEpisode: "第24集",
			Checked:       now,
		})
	}
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tt.Frame()
	}
}

func BenchmarkHistoryView200(b *testing.B) {
	a := &app{nav: []routeKind{routeHistory}}
	defer a.close()
	now := time.Now()
	for i := range 200 {
		a.history.entries = append(a.history.entries, historyEntry{
			Name:     fmt.Sprintf("历史影片 %03d", i),
			Episode:  "第1集",
			Position: 120,
			Duration: 1200,
			Updated:  now.Add(-time.Duration(i) * time.Hour),
		})
	}
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tt.Frame()
	}
}

func BenchmarkPlayerHUDMotionFrame(b *testing.B) {
	a := &app{nav: []routeKind{routeHome}}
	defer a.close()
	a.preferences = defaultPlayerPreferences()
	episodes := make([]catalog.Episode, 60)
	for i := range episodes {
		episodes[i] = catalog.Episode{Name: fmt.Sprintf("第%d集", i+1), ID: fmt.Sprintf("ep-%d", i+1)}
	}
	doc := &config.Config{URL: "https://example.com/config.json"}
	site := config.Site{Key: "bench-site", Name: "示例站", Type: 1, Kind: config.KindJSON, API: "https://example.com/api"}
	item := library.Item{
		Entry: library.Entry{Config: doc, Site: site},
		Video: catalog.Video{ID: "v1", Name: "基准测试影片"},
	}
	a.title = &titlePage{
		name:   "基准测试影片",
		item:   item,
		seeds:  []library.Item{item},
		failed: map[string]string{},
		detail: &catalog.Detail{
			Name:    "基准测试影片",
			Sources: []catalog.Source{{Name: "主线路", Episodes: episodes}},
		},
	}
	a.player = &unknownSizeMediaPlayer{playing: true, pos: 18 * time.Minute, dur: 45 * time.Minute, rate: 1, vol: 0.8}
	a.playingSource, a.playingEpisode = "主线路", "第1集"
	a.panelOpen = true
	tt := ui.NewTester(a.view, 1280, 760)
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%16 == 0 {
			a.showPlayerOSD(fmt.Sprintf("音量 %d%%", 50+(i/16)%50))
			tt.Frame()
		} else {
			tt.Advance(16 * time.Millisecond)
		}
	}
}

func BenchmarkNavTabSwitchFrame(b *testing.B) {
	a := &app{nav: []routeKind{routeHome}}
	defer a.close()
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	tabs := []routeKind{routeHome, routeFavorites, routeHistory, routeSources}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%16 == 0 {
			a.nav[0] = tabs[(i/16)%len(tabs)]
			tt.Frame()
		} else {
			tt.Advance(16 * time.Millisecond)
		}
	}
}

func TestTitleKeyMatchesStdLower(t *testing.T) {
	cases := []string{
		"", "   ", "《示例》", "  《三体》 ", "三体2", "Test Movie",
		"Étoile", "ＡＢＣ全角", "中文 Mix 123", "《》", "剧集·第一季",
	}
	for _, tc := range cases {
		want := strings.ToLower(strings.Trim(strings.TrimSpace(tc), "《》"))
		if got := titleKey(tc); got != want {
			t.Errorf("titleKey(%q) = %q, want %q", tc, got, want)
		}
	}
}

func TestEpisodeButtonLabelMatchesRegex(t *testing.T) {
	re := regexp.MustCompile(`^(?:第\s*)?([0-9]+)\s*集(?:\s*(?:已完结|完结))?$|^([0-9]+)$`)
	ref := func(name string) string {
		parts := re.FindStringSubmatch(strings.TrimSpace(name))
		if parts == nil {
			return name
		}
		if number, err := strconv.Atoi(parts[1] + parts[2]); err == nil {
			return strconv.Itoa(number)
		}
		return name
	}
	cases := []string{
		"", "  ", "0", "000", "01", "12", " 007 ", "第1集", "第 012 集", "12集",
		"第36集 完结", "第40集已完结", "第 40 集  已完结 ", "第1集 番外", "正片",
		"99999999999999999999", "第99999999999999999999集", "第集", "EP01",
	}
	for _, tc := range cases {
		if got, want := episodeButtonLabel(tc), ref(tc); got != want {
			t.Errorf("episodeButtonLabel(%q) = %q, want %q", tc, got, want)
		}
	}
}

func TestHistoryViewVirtualizesLargeHistory(t *testing.T) {
	a := &app{nav: []routeKind{routeHistory}}
	defer a.close()
	now := time.Now()
	for i := range 200 {
		a.history.entries = append(a.history.entries, historyEntry{
			Name:     fmt.Sprintf("历史影片 %03d", i),
			Pic:      fmt.Sprintf("https://example.com/poster/%03d.jpg", i),
			Episode:  "第1集",
			Position: 120,
			Duration: 1200,
			Updated:  now.Add(-time.Duration(i) * time.Hour),
		})
	}
	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("今天") || !tt.HasText("历史影片 000") {
		t.Fatalf("visible history entries missing: %v", tt.Texts())
	}
	if tt.HasText("历史影片 199") {
		t.Fatal("historyView built off-screen tail rows instead of virtualizing")
	}
}

func TestCandidatesIndexAndDirectMutationFallback(t *testing.T) {
	doc, events := makeBenchLibraryEvents(10, 10)
	a := &app{library: libraryBrowser{root: doc, cached: map[string]*config.Config{}}}
	defer a.close()
	s := &a.library.search
	a.startScan(s, "合成", false)
	a.applyLibraryEvents(s, events)
	a.title = &titlePage{name: "合成", failed: map[string]string{}}
	got := a.candidates()
	if len(got) != 10 {
		t.Fatalf("indexed candidates = %d, want 10", len(got))
	}
	extra := library.Item{
		Entry: library.Entry{Config: doc, Site: config.Site{Key: "extra", Name: "额外站点", API: "https://example.com/extra"}},
		Video: catalog.Video{ID: "extra-1", Name: "合成"},
	}
	s.items = append(s.items, extra)
	got = a.candidates()
	if len(got) != 11 {
		t.Fatalf("fallback after direct search.items append = %d, want 11", len(got))
	}
}

func TestLiveBrowserCacheInvalidation(t *testing.T) {
	var l liveBrowser
	l.channels = []iptv.Channel{
		{Name: "新闻台", Group: "新闻"},
		{Name: "体育台", Group: "体育"},
	}
	if groups := l.groups(); len(groups) != 2 || groups[0] != "新闻" || groups[1] != "体育" {
		t.Fatalf("groups = %v", groups)
	}
	l.group = "体育"
	if got := l.filteredChannels(false); len(got) != 1 || got[0] != 1 {
		t.Fatalf("filtered by group = %v", got)
	}
	l.group = ""
	l.query = "新闻"
	if got := l.filteredChannels(true); len(got) != 1 || got[0] != 0 {
		t.Fatalf("filtered by query = %v", got)
	}
	// Replacing channels directly must invalidate both caches.
	l.channels = []iptv.Channel{{Name: "电影台", Group: "影视"}}
	l.query = ""
	if groups := l.groups(); len(groups) != 1 || groups[0] != "影视" {
		t.Fatalf("groups after replacement = %v", groups)
	}
	if got := l.filteredChannels(true); len(got) != 1 || got[0] != 0 {
		t.Fatalf("filtered after replacement = %v", got)
	}
}
