package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	click("换源与选集")
	if !tt.HasText("甲站") || !tt.HasText("线路") || tt.HasText("查找其他来源") || tt.HasText("换来源") {
		t.Fatalf("unexpected single-source panel: %v", tt.Texts())
	}
	// A search started before playback can still deliver another known source.
	a.library.search.items = append(a.library.search.items, other)
	tt.Frame()
	click("换来源")
	click("选择来源")
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
	if a.playFailure != nil || !tt.HasText("乙站") || !tt.HasText("收起选集") || a.library.search.query != "示例" || a.route() != routeDetail {
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
