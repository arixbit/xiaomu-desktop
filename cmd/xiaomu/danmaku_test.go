package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/egoist/mygo/ui"
)

func danmakuTestServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/api/v2/search/episodes":
			// Deliberately ignore the episode parameter to exercise local filtering.
			io.WriteString(w, `{"animes":[{"animeId":11,"animeTitle":"示例片","episodes":[{"episodeId":101,"episodeTitle":"第1话 示例"},{"episodeId":102,"episodeTitle":"第2话 示例"},{"episodeId":112,"episodeTitle":"第12话 示例"}]}]}`)
		case "/api/v2/comment/101", "/api/v2/comment/102", "/api/v2/comment/112":
			io.WriteString(w, `{"comments":[{"p":"0,5,16777215,user","m":"顶部示例"},{"p":"0,1,16777215,user","m":"滚动示例"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDanmakuSettingsDoNotRequireService(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.startDanmaku()
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("弹幕服务地址"); ok {
		t.Fatal("ordinary users are asked to supply a comment service")
	}
	if err := tt.Click("弹幕开关"); err != nil {
		t.Fatal(err)
	}
	if !a.preferences.Danmaku.Enabled || !tt.HasText("当前片源未提供弹幕，可尝试换源") {
		t.Fatalf("missing-source state is unclear: %v", tt.Texts())
	}
}

func TestDanmakuPlayerSettingsAndEpisodeTransition(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferencesPath = filepath.Join(t.TempDir(), "preferences.json")
	var calls atomic.Int32
	server := danmakuTestServer(t, &calls)
	a.startDanmaku()
	tt := ui.NewTester(a.view, 1120, 760)
	if calls.Load() != 0 {
		t.Fatal("contacted a service before configuration")
	}
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("手动匹配（可选）"); err != nil {
		t.Fatal(err)
	}
	a.danmaku.address = server.URL
	tt.Frame()
	if err := tt.Click("保存并匹配"); err != nil {
		t.Fatal(err)
	}
	if len(a.danmaku.comments) != 2 || a.danmaku.selected == nil || a.danmaku.selected.ID != "101" || calls.Load() != 2 {
		t.Fatalf("did not match current episode: %+v, calls=%d", a.danmaku.selected, calls.Load())
	}
	preferences, err := readPlayerPreferences(a.preferencesPath)
	if err != nil || !preferences.Danmaku.Enabled || preferences.Danmaku.API != server.URL || preferences.Volume != 1 {
		t.Fatalf("preferences = %+v, %v", preferences, err)
	}
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("弹幕画面"); !ok {
		t.Fatal("no comment overlay")
	}
	a.setFullScreen(true)
	tt.Frame()
	tt.Advance(4 * time.Second)
	if tt.HasText("返回") {
		t.Fatal("fullscreen controls did not hide")
	}
	if _, ok := tt.Find("弹幕画面"); !ok {
		t.Fatal("comments hid with fullscreen controls")
	}
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	if !a.playURL("https://example.com/second.mp4", nil) {
		t.Fatal(a.status)
	}
	if len(a.danmaku.comments) != 0 {
		t.Fatal("new player retained old comments")
	}
	a.danmaku.episodeQuery = "12"
	a.notePlayingEpisode("线路", a.title.detail.Sources[0].Episodes[1], 0)
	if a.danmaku.selected == nil || a.danmaku.selected.ID != "102" || a.danmaku.episodeQuery != "2" || calls.Load() != 4 {
		t.Fatalf("next episode = %+v, calls=%d", a.danmaku.selected, calls.Load())
	}
}

func TestDanmakuRejectsStaleJobResults(t *testing.T) {
	for _, action := range []string{"leave", "disable", "replace", "close", "edit episode", "edit title"} {
		t.Run(action, func(t *testing.T) {
			a := newPlayerTestApp(t)
			a.preferences = defaultPlayerPreferences()
			var calls atomic.Int32
			server := danmakuTestServer(t, &calls)
			a.preferences.Danmaku.Enabled, a.preferences.Danmaku.API = true, server.URL
			updates := make(chan func(), 1)
			a.dispatch = func(update func()) { updates <- update }
			a.startDanmaku()
			var finish func()
			select {
			case finish = <-updates:
			case <-time.After(3 * time.Second):
				t.Fatal("comment request did not finish")
			}
			switch action {
			case "leave":
				a.leavePlayer()
			case "disable":
				tt := ui.NewTester(a.view, 1120, 760)
				if err := tt.Click("弹幕"); err != nil {
					t.Fatal(err)
				}
				if err := tt.Click("弹幕开关"); err != nil {
					t.Fatal(err)
				}
				if a.preferences.Danmaku.Enabled {
					t.Fatal("switch did not disable comments")
				}
			case "replace":
				player, err := ui.NewVideoPlayer(&stillVideo{})
				if err != nil {
					t.Fatal(err)
				}
				a.adoptPlayer(player)
			case "close":
				a.close()
			case "edit episode", "edit title":
				tt := ui.NewTester(a.view, 1120, 760)
				if err := tt.Click("弹幕"); err != nil {
					t.Fatal(err)
				}
				label, text := "弹幕集数", "2"
				if action == "edit title" {
					label, text = "弹幕片名", "另一部片"
				}
				if err := tt.Click("手动匹配（可选）"); err != nil {
					t.Fatal(err)
				}
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				tt.Command("selectAll")
				tt.Type(text)
				if a.status != "" {
					t.Fatalf("canceled search left a loading message: %s", a.status)
				}
			}
			finish()
			if len(a.danmaku.comments) != 0 || a.danmaku.selected != nil || a.running(jobDanmaku) {
				t.Fatal("an abandoned job restored the previous episode's comments")
			}
		})
	}
}

func TestDanmakuCancelsInFlightRequest(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	t.Cleanup(server.Close)
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferences.Danmaku.Enabled, a.preferences.Danmaku.API = true, server.URL
	updates := make(chan func(), 1)
	a.dispatch = func(update func()) { updates <- update }
	a.startDanmaku()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	a.leavePlayer()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("leaving playback did not cancel the HTTP request")
	}
	select {
	case finish := <-updates:
		finish()
	case <-time.After(3 * time.Second):
		t.Fatal("canceled job did not finish")
	}
}

func TestMatchDanmakuEpisode(t *testing.T) {
	results := []danmaku.Episode{
		{ID: "11", AnimeID: "a", Anime: "示例片 (2026)", Name: "第01话 开始"},
		{ID: "12", AnimeID: "a", Anime: "示例片 (2026)", Name: "EP02: 继续"},
		{ID: "21", AnimeID: "b", Anime: "另一部片", Name: "第1话"},
	}
	for _, tc := range []struct {
		name, episode, preferred, want string
		index                          int
	}{
		{"numeric", "第01集", "", "11", 0},
		{"second", "2", "", "12", 1},
		{"missing", "第3集", "", "", 2},
		{"remember manual series", "第1集", "b", "21", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := matchDanmakuEpisode(results, "示例片", tc.episode, tc.index, tc.preferred)
			if ok != (tc.want != "") || ok && got.ID != tc.want {
				t.Fatalf("match = %+v, %v", got, ok)
			}
		})
	}
	results = append(results, danmaku.Episode{ID: "31", AnimeID: "c", Anime: "示例片", Name: "第1集"})
	if _, ok := matchDanmakuEpisode(results, "示例片", "第1集", 0, ""); ok {
		t.Fatal("silently chose between ambiguous sources")
	}
}

func TestDanmakuManualEpisodeSearch(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	var calls atomic.Int32
	server := danmakuTestServer(t, &calls)
	a.preferences.Danmaku.API = server.URL
	a.startDanmaku()
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("手动匹配（可选）"); err != nil {
		t.Fatal(err)
	}
	if a.danmaku.episodeQuery != "1" {
		t.Fatalf("did not default to the playing episode: %s", a.danmaku.episodeQuery)
	}
	if err := tt.Click("弹幕集数"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("2")
	if err := tt.Click("搜索弹幕"); err != nil {
		t.Fatal(err)
	}
	if len(a.danmaku.episodes) != 1 || !tt.HasText("示例片 · 第2话 示例") || tt.HasText("示例片 · 第1话 示例") || tt.HasText("示例片 · 第12话 示例") {
		t.Fatalf("episode filter did not restrict search results: %v", tt.Texts())
	}
	if err := tt.Click("示例片 · 第2话 示例"); err != nil {
		t.Fatal(err)
	}
	if a.danmaku.selected == nil || a.danmaku.selected.ID != "102" || a.danmaku.preferredAnime != "11" || a.playingEpisode != "第1集" {
		t.Fatalf("manual correction = %+v, playing %s", a.danmaku.selected, a.playingEpisode)
	}
	if err := tt.Click("弹幕集数"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("12")
	if len(a.danmaku.episodes) != 0 {
		t.Fatal("editing the episode left stale candidates visible")
	}
	if err := tt.Click("保存并匹配"); err != nil {
		t.Fatal(err)
	}
	if a.danmaku.selected == nil || a.danmaku.selected.ID != "112" {
		t.Fatalf("automatic matching ignored the manual episode: %+v", a.danmaku.selected)
	}
}

func TestDanmakuEpisodeSearchValidation(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []string
		valid bool
	}{
		{"", []string{"101", "102", "112"}, true},
		{"02", []string{"102"}, true},
		{"3", nil, true},
		{"0", nil, false},
		{"-1", nil, false},
		{"abc", nil, false},
	} {
		t.Run("episode="+tc.input, func(t *testing.T) {
			a := newPlayerTestApp(t)
			a.preferences = defaultPlayerPreferences()
			var calls atomic.Int32
			server := danmakuTestServer(t, &calls)
			a.preferences.Danmaku.API = server.URL
			a.startDanmaku()
			tt := ui.NewTester(a.view, 1120, 760)
			for _, label := range []string{"弹幕", "手动匹配（可选）", "弹幕集数"} {
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
			}
			tt.Command("selectAll")
			tt.Command("delete")
			if tc.input != "" {
				tt.Type(tc.input)
			}
			if err := tt.Click("搜索弹幕"); err != nil {
				t.Fatal(err)
			}
			wantCalls := int32(0)
			if tc.valid {
				wantCalls = 1
			} else if !tt.HasText("集数请输入正整数，留空可查看全部分集") {
				t.Fatalf("invalid episode did not show an error: %v", tt.Texts())
			}
			if calls.Load() != wantCalls || len(a.danmaku.episodes) != len(tc.want) {
				t.Fatalf("search = %+v, calls=%d", a.danmaku.episodes, calls.Load())
			}
			for i, id := range tc.want {
				if a.danmaku.episodes[i].ID != id {
					t.Fatalf("wrong episode returned: %+v", a.danmaku.episodes)
				}
			}
			if a.danmaku.selected != nil {
				t.Fatal("manual search silently selected a comment track")
			}
		})
	}
}

func TestDanmakuOverlayPassesVideoClicks(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferences.Danmaku.Enabled = true
	a.applyDanmaku(danmaku.Episode{ID: "101"}, []danmaku.Comment{{Mode: 5, Color: 0xffffff, Text: "示例弹幕"}})
	tt := ui.NewTester(a.view, 1120, 760)
	tt.ClickAt(500, 300)
	tt.Advance(600 * time.Millisecond)
	if a.player.Playing() {
		t.Fatal("the comment overlay swallowed the picture's pause gesture")
	}
}

func TestDanmakuDialogFitsMinimumWindow(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.danmaku.episodes = []danmaku.Episode{{ID: "101", Anime: "示例片", Name: "第1话"}}
	a.danmaku.message = "请选择对应的影片和分集"
	tt := ui.NewTester(a.view, 640, 420)
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"弹幕开关", "弹幕字号", "手动匹配（可选）", "完成"} {
		r, ok := tt.Find(label)
		if !ok || r.H < 12 || r.Y < 0 || r.Y+r.H > 420 {
			t.Fatalf("%s is outside the window: %+v", label, r)
		}
	}
}
