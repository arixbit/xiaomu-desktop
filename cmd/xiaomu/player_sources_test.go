package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/egoist/mygo/ui"
)

func playerWithTwoSites(t *testing.T, lines, episodes string) (*app, library.Item) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ids") != "" {
			fmt.Fprintf(w, `{"list":[{"vod_name":"示例片","vod_play_from":%q,"vod_play_url":%q}]}`, lines, episodes)
		} else {
			fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"7","vod_name":"示例片"},{"vod_id":"8","vod_name":"示例片续集"}]}`)
		}
	}))
	t.Cleanup(server.Close)
	a := newPlayerTestApp(t)
	doc := &config.Config{URL: server.URL + "/config"}
	for _, name := range []string{"甲站", "乙站"} {
		doc.Sites = append(doc.Sites, config.Site{Key: name, Name: name, Type: 1, Kind: config.KindJSON, API: server.URL + "/api"})
	}
	a.library = libraryBrowser{root: doc, cached: map[string]*config.Config{}}
	item := library.Item{Entry: library.Entry{Config: doc, Site: doc.Sites[0]}, Video: catalog.Video{ID: "7", Name: "示例片"}}
	a.title.item, a.title.seeds = item, []library.Item{item}
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		if strings.Contains(raw, "broken.mp4") {
			return nil, errors.New("测试线路已失效")
		}
		return ui.NewVideoPlayer(&timedVideo{})
	}
	item.Entry.Site = doc.Sites[1]
	return a, item
}

func TestSavedPlaybackDiscoversSwitchableSources(t *testing.T) {
	for _, tc := range []struct {
		name      string
		follow    bool
		preferred bool
	}{
		{name: "history"},
		{name: "history with a preferred site", preferred: true},
		{name: "following without history", follow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, other := playerWithTwoSites(t, "主线", "第1集$/one.mp4#第2集$/two.mp4")
			saved := a.title.item
			a.leavePlayer()
			a.library.search.query = "之前搜的片名"
			start, target := saved, other
			wantEpisode, wantAt := "第2集", 25*time.Second
			if tc.follow {
				a.openFollowing(favoriteEntry{Name: saved.Video.Name, ConfigURL: configURL(saved.Entry), SiteKey: saved.Entry.Site.Key, VideoID: string(saved.Video.ID)})
				wantEpisode, wantAt = "第1集", 0
			} else {
				if tc.preferred {
					if err := a.sourceMemory.record(saved.Video.Name, other, "主线", time.Second, true, time.Now()); err != nil {
						t.Fatal(err)
					}
					start, target = other, saved
				}
				a.resume(historyEntry{Name: saved.Video.Name, ConfigURL: configURL(saved.Entry), SiteKey: saved.Entry.Site.Key, VideoID: string(saved.Video.ID),
					Line: "主线", Episode: wantEpisode, Position: wantAt.Seconds(), Duration: 60})
			}
			old := a.player
			if old == nil || !a.title.isCurrent(start) || a.playingEpisode != wantEpisode || a.playbackPosition() != wantAt {
				t.Fatalf("saved playback changed during lookup: episode=%q at=%s status=%q", a.playingEpisode, a.playbackPosition(), a.status)
			}
			tt := ui.NewTester(a.view, 1120, 760)
			if err := tt.Click("选集"); err != nil {
				t.Fatalf("open episode panel: %v", err)
			}
			if err := tt.Click("选择来源"); err != nil {
				t.Fatalf("saved playback has no source menu: %v", tt.Texts())
			}
			if len(tt.Menu()) != 2 {
				t.Fatalf("expected only the two exact-title sources: %v", tt.Menu())
			}
			if err := tt.ChooseMenuItem(target.Entry.Label()); err != nil {
				t.Fatal(err)
			}
			if a.player == old || !a.title.isCurrent(target) || a.playingEpisode != wantEpisode || a.playbackPosition() < wantAt {
				t.Fatalf("switch did not continue the episode: episode=%q at=%s status=%q", a.playingEpisode, a.playbackPosition(), a.status)
			}
			if a.library.search.query != "之前搜的片名" {
				t.Fatal("playback lookup replaced the viewer's search")
			}
		})
	}
}

func TestSavedPlaybackShowsFastSourceWhileSlowLookupIsCanceledOnExit(t *testing.T) {
	a, _ := playerWithTwoSites(t, "主线", "第1集$/one.mp4#第2集$/two.mp4")
	saved := a.title.item
	a.leavePlayer()
	started, canceled := make(chan struct{}), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer slow.Close()
	defer slow.CloseClientConnections()
	doc := a.library.root
	doc.Sites = append(doc.Sites, config.Site{Key: "slow", Kind: config.KindJSON, API: slow.URL})
	updates := make(chan func(), 16)
	a.dispatch = func(fn func()) { updates <- fn }
	a.resume(historyEntry{Name: saved.Video.Name, ConfigURL: doc.URL, SiteKey: saved.Entry.Site.Key, VideoID: string(saved.Video.ID),
		Line: "主线", Episode: "第2集", Position: 25, Duration: 60})
	deadline := time.After(3 * time.Second)
	for a.player == nil {
		select {
		case fn := <-updates:
			fn()
		case <-deadline:
			t.Fatal("saved episode did not open")
		}
	}
	player := a.player
	select {
	case <-started:
	case <-deadline:
		t.Fatal("background lookup did not reach the slow site")
	}
	for len(a.candidates()) < 2 {
		select {
		case fn := <-updates:
			fn()
		case <-deadline:
			t.Fatal("fast source was held up by the slow site")
		}
	}
	if a.player != player || !a.title.isCurrent(saved) || a.playingEpisode != "第2集" || a.playbackPosition() != 25*time.Second {
		t.Fatal("a background result interrupted playback")
	}
	a.leavePlayer()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("leaving playback did not cancel the site request")
	}
}

func TestSavedPlaybackRechecksCachedMissAndTracksConfigRefresh(t *testing.T) {
	a, _ := playerWithTwoSites(t, "主线", "第1集$/one.mp4#第2集$/two.mp4")
	saved := a.title.item
	a.leavePlayer()
	var searches atomic.Int32
	newSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if searches.Add(1) == 1 {
			fmt.Fprint(w, `{"list":[]}`)
			return
		}
		fmt.Fprint(w, `{"list":[{"vod_id":"9","vod_name":"示例片"}]}`)
	}))
	defer newSite.Close()
	doc := a.library.root
	doc.Sites[1].API = newSite.URL
	a.startScan(&a.library.search, saved.Video.Name, false)
	a.resume(historyEntry{Name: saved.Video.Name, ConfigURL: doc.URL, SiteKey: saved.Entry.Site.Key, VideoID: string(saved.Video.ID),
		Line: "主线", Episode: "第1集", Position: 25, Duration: 60})
	if a.player == nil || len(a.candidates()) != 2 || searches.Load() != 2 {
		t.Fatalf("newly available site missing after a cached miss: candidates=%v requests=%d", a.candidates(), searches.Load())
	}
	player := a.player
	refreshed := *doc
	refreshed.Sites = doc.Sites[:1]
	a.applyLibraryConfigMode(doc.URL, &refreshed, nil, nil, nil, true)
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("选集"); err != nil {
		t.Fatalf("open episode panel: %v", err)
	}
	if !tt.HasText("暂未找到其他来源") || len(a.candidates()) != 1 || a.player != player || a.playbackPosition() < 25*time.Second {
		t.Fatalf("config refresh left an obsolete source or interrupted playback: %v", tt.Texts())
	}
}

func TestPlayerSwitchesKnownSourcesAndRecoversOnChosenLine(t *testing.T) {
	a, other := playerWithTwoSites(t, "主线$$$备用线", "02$/broken.mp4#01$/first.mp4$$$03$/third.mp4#2$/second.mp4")
	a.library.search.query = "示例"
	a.nav = []routeKind{routeHome, routeSearch, routeDetail}
	a.playFailure = &episodeFailure{line: "线路", episode: a.title.detail.Sources[0].Episodes[1], at: 24 * time.Second, err: errors.New("原线路失效")}
	tt := ui.NewTester(a.view, 800, 640)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %s: %v; screen: %v", label, err, tt.Texts())
		}
	}
	click("选集")
	if !tt.HasText("甲站") || !tt.HasText("线路") || tt.HasText("查找其他来源") || tt.HasText("换来源") {
		t.Fatalf("unexpected single-source panel: %v", tt.Texts())
	}
	// A search started before playback can still deliver another known source.
	a.library.search.items = append(a.library.search.items, other)
	tt.Frame()
	click("换来源")
	if len(tt.Menu()) != 2 {
		t.Fatalf("expected the two exact-title sources: %v", tt.Menu())
	}
	old := a.player
	if err := tt.ChooseMenuItem("乙站"); err != nil {
		t.Fatal(err)
	}
	if a.player != old || a.title.item.Entry.Site.Key != "甲站" || !tt.HasText("测试线路已失效") || !tt.HasText("备用线") {
		t.Fatalf("unavailable target line lost the player or alternate lines: %v", tt.Texts())
	}
	click("备用线")
	if a.player == old || a.player == nil || a.title.item.Entry.Site.Key != "乙站" || a.playingSource != "备用线" || a.playingEpisode != "2" || a.playbackPosition() < 24*time.Second {
		t.Fatalf("failed to resume the requested episode: site=%s line=%q episode=%q position=%s", a.title.item.Entry.Site.Key, a.playingSource, a.playingEpisode, a.playbackPosition())
	}
	if a.playFailure != nil || !tt.HasText("乙站") || !a.panelOpen || a.library.search.query != "示例" || a.route() != routeDetail {
		t.Fatalf("panel or original search changed after switching: %v", tt.Texts())
	}
}

func TestPlayerCanChooseEpisodeMissingFromAlternateSource(t *testing.T) {
	a, other := playerWithTwoSites(t, "新线路", "特别篇$/special.mp4")
	a.title.seeds = append(a.title.seeds, other)
	a.panelOpen = true
	a.playingEpisode, a.resumeAt = "第1集", 15*time.Second
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("选择来源"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("乙站"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("请选择要播放的分集") || !tt.HasText("特别篇") || a.title.item.Entry.Site.Key != "甲站" {
		t.Fatalf("missing episode was silently replaced or chooser disappeared: %v", tt.Texts())
	}
	if err := tt.Click("特别篇"); err != nil {
		t.Fatal(err)
	}
	if a.playingEpisode != "特别篇" || a.title.item.Entry.Site.Key != "乙站" || a.resumeAt != 0 || a.panelChoice != nil {
		t.Fatalf("manual episode choice did not start at the beginning: %s at %s", a.playingEpisode, a.resumeAt)
	}
}

func TestReturningToPlayingSourceDiscardsPendingDetail(t *testing.T) {
	a, other := playerWithTwoSites(t, "备用线", "01$/first.mp4")
	old := a.player
	results := make(chan func(), 1)
	a.dispatch = func(f func()) { results <- f }
	a.switchSource(other)
	select {
	case finish := <-results:
		// The response is queued on the UI thread as the viewer switches back.
		a.switchSource(a.title.item)
		finish()
	case <-time.After(3 * time.Second):
		t.Fatal("source detail did not arrive")
	}
	if a.player != old || a.panelChoice != nil || a.title.item.Entry.Site.Key != "甲站" || a.running(jobPlay) {
		t.Fatal("a late source detail displaced the viewer's last choice")
	}
}

func TestPlayerSwitchesFromPan4KFilenameToCMS1080PEpisode(t *testing.T) {
	a, other := playerWithTwoSites(t, "1080P线路", "第01集$/ep1.m3u8#第02集$/ep2.m3u8#第03集$/ep3.m3u8")
	a.title.detail.Sources = []catalog.Source{{
		Name: "夸克4K",
		Episodes: []catalog.Episode{
			{Name: "示例剧.S01E01.2026.2160p.WEB-DL.H265", ID: "pan1"},
			{Name: "示例剧.S01E02.2026.2160p.WEB-DL.H265", ID: "pan2"},
		},
	}}
	a.playingSource = "夸克4K"
	a.playingEpisode = "示例剧.S01E02.2026.2160p.WEB-DL.H265"
	a.resumeAt = 42 * time.Second
	a.title.seeds = append(a.title.seeds, other)
	a.panelOpen = true

	old := a.player
	a.switchSource(other)

	if a.player == old || a.player == nil {
		t.Fatalf("expected player to switch from 4K pan filename to 1080P CMS episode; panelChoice=%+v", a.panelChoice)
	}
	if a.title.item.Entry.Site.Key != "乙站" || a.playingSource != "1080P线路" || a.playingEpisode != "第02集" {
		t.Fatalf("unexpected playback after switch: site=%q line=%q ep=%q", a.title.item.Entry.Site.Key, a.playingSource, a.playingEpisode)
	}
	if a.resumeAt != 42*time.Second || a.panelChoice != nil {
		t.Fatalf("expected resume at 42s and cleared panelChoice, got resumeAt=%s choice=%+v", a.resumeAt, a.panelChoice)
	}
}

func TestPlayerSwitchesSingleMovieFromPan4KToCMS1080P(t *testing.T) {
	a, other := playerWithTwoSites(t, "1080P线路", "HD1080P$/movie1080.m3u8")
	a.title.detail.Sources = []catalog.Source{{
		Name:     "夸克4K",
		Episodes: []catalog.Episode{{Name: "示例电影.2026.2160p.4K.HDR", ID: "pan4k"}},
	}}
	a.playingSource = "夸克4K"
	a.playingEpisode = "示例电影.2026.2160p.4K.HDR"
	a.resumeAt = 120 * time.Second
	a.title.seeds = append(a.title.seeds, other)
	a.panelOpen = true

	old := a.player
	a.switchSource(other)

	if a.player == old || a.title.item.Entry.Site.Key != "乙站" || a.playingSource != "1080P线路" || a.playingEpisode != "HD1080P" {
		t.Fatalf("single-episode movie did not auto-switch: site=%q line=%q ep=%q choice=%+v", a.title.item.Entry.Site.Key, a.playingSource, a.playingEpisode, a.panelChoice)
	}
	if a.resumeAt != 120*time.Second {
		t.Fatalf("resumeAt = %s, want 120s", a.resumeAt)
	}
}

func TestPlayerSourceSwitchFailureShowsOSDAllowsRetryAndBackToCurrent(t *testing.T) {
	a, other := playerWithTwoSites(t, "1080P线路", "第1集$/broken.mp4")
	a.title.seeds = append(a.title.seeds, other)
	a.panelOpen = true
	old := a.player

	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("选择来源"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("乙站"); err != nil {
		t.Fatal(err)
	}
	if a.player != old || a.panelChoice == nil || !a.panelChoice.failed {
		t.Fatalf("expected failed switch keeping old player: choice=%+v", a.panelChoice)
	}
	if !strings.Contains(a.playerOSD.text, "切换失败") {
		t.Fatalf("expected switch failure OSD, got %q", a.playerOSD.text)
	}
	if !tt.HasText("仍在播放：甲站 · 线路") || !tt.HasText("回到当前") {
		t.Fatalf("expected active-source banner and back button: %v", tt.Texts())
	}

	// Failed source in dropdown must remain selectable for retry (ChooseMenuItem fails if disabled).
	if err := tt.Click("选择来源"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("乙站"); err != nil {
		t.Fatalf("failed source was not re-selectable for retry: %v", err)
	}
	// Clicking "回到当前" restores the panel to the playing source cleanly.
	if err := tt.Click("回到当前"); err != nil {
		t.Fatalf("click 回到当前: %v; texts=%v", err, tt.Texts())
	}
	if a.panelChoice != nil || a.player != old || a.title.item.Entry.Site.Key != "甲站" {
		t.Fatalf("回到当前 did not restore playing source: choice=%+v site=%q", a.panelChoice, a.title.item.Entry.Site.Key)
	}
}

func TestPlayerSameSourceUnmatchedLineShowsFeedbackAndKeepsCurrent(t *testing.T) {
	a := newPlayerTestApp(t)
	a.title.detail.Sources = []catalog.Source{
		{Name: "4K线路", Episodes: []catalog.Episode{{Name: "第1集", ID: "1"}, {Name: "第2集", ID: "2"}, {Name: "第3集", ID: "3"}}},
		{Name: "1080P线路", Episodes: []catalog.Episode{{Name: "正片", ID: "movie"}}},
	}
	a.playingSource, a.playingEpisode = "4K线路", "第3集"
	a.panelOpen = true
	old := a.player

	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("1080P线路"); err != nil {
		t.Fatal(err)
	}
	if a.player != old || !strings.Contains(a.playerOSD.text, "无对应分集") {
		t.Fatalf("expected unmatched line OSD keeping old player: osd=%q", a.playerOSD.text)
	}
	if !tt.HasText("仍在播放：4K线路") {
		t.Fatalf("expected inline notice that 4K线路 is still playing: %v", tt.Texts())
	}
	// Clicking back to the currently playing line does not restart playback.
	if err := tt.Click("4K线路"); err != nil {
		t.Fatal(err)
	}
	if a.player != old || a.panelSource != 0 {
		t.Fatalf("returning to playing line restarted player or lost panelSource: panelSource=%d", a.panelSource)
	}
}
