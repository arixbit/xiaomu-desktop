package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	if err := tt.Click("服务设置（可选）"); err != nil {
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
	for _, action := range []string{"leave", "disable", "replace", "close", "close current", "edit episode", "edit title"} {
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
				a.adoptPlayer(wrapSystemPlayer(player))
			case "close":
				a.close()
			case "close current":
				a.closeCurrentPlayer()
			case "edit episode", "edit title":
				tt := ui.NewTester(a.view, 1120, 760)
				if err := tt.Click("弹幕"); err != nil {
					t.Fatal(err)
				}
				label, text := "弹幕集数", "2"
				if action == "edit title" {
					label, text = "弹幕片名", "另一部片"
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
	if err := tt.Click("服务设置（可选）"); err != nil {
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
			for _, label := range []string{"弹幕", "弹幕集数"} {
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
	for _, label := range []string{"弹幕开关", "弹幕字号", "弹幕滚动速度", "弹幕显示区域", "完成"} {
		r, ok := tt.Find(label)
		if !ok || r.H < 12 || r.Y < 0 || r.Y+r.H > 420 {
			t.Fatalf("%s is outside the window: %+v", label, r)
		}
	}
	viewport, ok := tt.Find("弹幕设置内容")
	if !ok {
		t.Fatal("settings cannot scroll")
	}
	tt.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, 200)
	if err := tt.Click("服务设置（可选）"); err != nil {
		t.Fatal(err)
	}
	if !a.danmaku.advanced {
		t.Fatal("service settings are unreachable in the small window")
	}
	tt.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, 200)
	r, ok := tt.Find("弹幕服务地址")
	if !ok || r.Y < viewport.Y || r.Y+r.H > viewport.Y+viewport.H {
		t.Fatalf("service address cannot scroll into view: %+v, viewport=%+v", r, viewport)
	}
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	if a.danmaku.open {
		t.Fatal("the close button scrolled out of reach")
	}
}

func TestDanmakuManualSelectionEnablesOverlayAndPersists(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferencesPath = filepath.Join(t.TempDir(), "preferences.json")
	var calls atomic.Int32
	server := danmakuTestServer(t, &calls)
	a.preferences.Danmaku.API = server.URL
	a.startDanmaku()
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("搜索弹幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("示例片 · 第1话 示例"); err != nil {
		t.Fatal(err)
	}
	if !a.preferences.Danmaku.Enabled {
		t.Fatal("selecting an episode manually did not enable danmaku")
	}
	prefs, err := readPlayerPreferences(a.preferencesPath)
	if err != nil || !prefs.Danmaku.Enabled {
		t.Fatalf("enabled preference was not saved: %+v, err=%v", prefs.Danmaku, err)
	}
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("弹幕画面"); !ok {
		t.Fatal("comment overlay did not appear after manual selection")
	}
}

func TestDanmakuNumberParsesTaggedEpisodesWithoutFalsePositives(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{
		{"【腾讯】剧名 EP03", 3},
		{"【腾讯】剧名EP03", 3},
		{"【qiyi】 剧名 E03 剧情", 3},
		{"剧名 S01E03", 3},
		{"Show.S02E12.1080p", 12},
		{"Show.S01.E04", 4},
		{"EP-05 特别篇", 5},
		{"Episode 09", 9},
		{"E03中字", 3},
		{"【qiyi】 剧名第3集", 3},
		{"Type2 测试", 0},
		{"Phase3", 0},
		{"Love2026", 0},
		{"Movie 1080p", 0},
		{"e2e 测试", 0},
		{"Plus2E3", 0},
		{"e-100 型号", 0},
	} {
		if got := danmakuNumber(tc.name); got != tc.want {
			t.Errorf("danmakuNumber(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestDanmakuJobLeavesPlayerStatusQuietAndUsesDanmakuErrors(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		io.WriteString(w, `{"animes":[]}`)
	}))
	t.Cleanup(server.Close)
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferences.Danmaku.Enabled, a.preferences.Danmaku.API = true, server.URL
	a.status = "正在播放"
	updates := make(chan func(), 1)
	a.dispatch = func(update func()) { updates <- update }
	a.startDanmaku()
	<-started
	tt := ui.NewTester(a.view, 1120, 760)
	if a.status != "正在播放" || tt.HasText("匹配弹幕…") || tt.HasText("加载弹幕…") {
		t.Fatalf("background danmaku job polluted player status: status=%q texts=%v", a.status, tt.Texts())
	}
	close(release)
	(<-updates)()
	if a.status != "正在播放" {
		t.Fatalf("danmaku completion altered player status: %q", a.status)
	}
	a.failDanmaku(context.DeadlineExceeded)
	if strings.Contains(a.danmaku.message, "站点") || !strings.Contains(a.danmaku.message, "弹幕") {
		t.Fatalf("timeout error used site wording: %q", a.danmaku.message)
	}
}

func TestMatchDanmakuEpisodeSkipsIndexFallbackWhenUnknown(t *testing.T) {
	results := []danmaku.Episode{
		{ID: "11", AnimeID: "a", Anime: "示例片", Name: "第1集"},
		{ID: "12", AnimeID: "a", Anime: "示例片", Name: "第2集"},
	}
	if got, ok := matchDanmakuEpisode(results, "示例片", "番外篇", -1, ""); ok {
		t.Fatalf("matched an episode without a known episode index: %+v", got)
	}
	if got, ok := matchDanmakuEpisode(results, "示例片", "番外篇", 0, ""); !ok || got.ID != "11" {
		t.Fatalf("valid episode index fallback stopped working: %+v, ok=%v", got, ok)
	}
}

func TestHasEpisodesExcludesLocalFilePlayback(t *testing.T) {
	a := newPlayerTestApp(t)
	if !a.hasEpisodes() {
		t.Fatal("episode player should report episodes")
	}
	a.playingFile = true
	if a.hasEpisodes() {
		t.Fatal("local file playback should not report title episodes")
	}
	if !a.canUseDanmaku() {
		t.Fatal("local file playback should allow danmaku search and overlay")
	}
}

func TestParseLocalDanmakuMeta(t *testing.T) {
	cases := []struct {
		file    string
		title   string
		year    string
		episode int
	}{
		{"示例剧.S01E05.2023.2160p.WEB-DL.H265.DTS-HD.mkv", "示例剧", "2023", 5},
		{"示例电影2.2023.4K.HDR.Remux.mkv", "示例电影2", "2023", 0},
		{"[字幕组] 示例番剧 - 12 [1080p][简繁内封].mkv", "示例番剧", "", 12},
		{"示例古装 第2季 第08集 4K.mp4", "示例古装 第2季", "", 8},
		{"Sample.Movie.Two.2024.2160p.BluRay.REMUX.HEVC.TrueHD.7.1.Atmos.mkv", "Sample Movie Two", "2024", 0},
		{"示例剧集.EP14.2023.1080p.mkv", "示例剧集", "2023", 14},
	}
	for _, tc := range cases {
		title, year, ep := parseLocalDanmakuMeta(tc.file)
		if title != tc.title || year != tc.year || ep != tc.episode {
			t.Fatalf("parseLocalDanmakuMeta(%q) = (%q, %q, %d), want (%q, %q, %d)",
				tc.file, title, year, ep, tc.title, tc.year, tc.episode)
		}
	}
}

func TestLocalFileDanmakuSearchAndToggle(t *testing.T) {
	var calls atomic.Int32
	server := danmakuTestServer(t, &calls)
	a := &app{
		preferences: defaultPlayerPreferences(),
		openLocal: func(string) (*ui.VideoPlayer, error) {
			return ui.NewVideoPlayer(&timedVideo{})
		},
	}
	a.preferences.Danmaku.API = server.URL
	t.Cleanup(a.close)

	if !a.openLocalFile("/tmp/示例片.S01E01.2024.2160p.HEVC.mp4") {
		t.Fatalf("openLocalFile failed: status=%q", a.status)
	}
	if a.danmaku.query != "示例片" || a.danmaku.episodeQuery != "1" {
		t.Fatalf("local danmaku prefill = (%q, %q), want (%q, %q)", a.danmaku.query, a.danmaku.episodeQuery, "示例片", "1")
	}

	tt := ui.NewTester(a.view, 1120, 760)
	if !tt.HasText("弹幕") {
		t.Fatalf("local file playback missing danmaku button: texts=%v", tt.Texts())
	}
	// Press D to enable and automatically match danmaku from the cleaned filename.
	tt.Key(0, ui.KeyD)
	if !a.preferences.Danmaku.Enabled || len(a.danmaku.comments) != 2 || calls.Load() != 2 {
		t.Fatalf("local file D toggle did not load danmaku: enabled=%v comments=%d calls=%d msg=%q",
			a.preferences.Danmaku.Enabled, len(a.danmaku.comments), calls.Load(), a.danmaku.message)
	}
	// Opening the danmaku dialog should work cleanly with a.title == nil.
	if err := tt.Click("弹幕"); err != nil {
		t.Fatalf("click local danmaku button failed: %v", err)
	}
	if !a.danmaku.open || !tt.HasText("弹幕设置") {
		t.Fatalf("danmaku dialog did not open for local file: open=%v texts=%v", a.danmaku.open, tt.Texts())
	}
}

func TestDanmakuOffsetControlsAndResetOnEpisodeChange(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferences.Danmaku.Enabled = true
	a.applyDanmaku(danmaku.Episode{ID: "101"}, []danmaku.Comment{{Time: 5 * time.Second, Mode: 5, Color: 0xffffff, Text: "五秒处弹幕"}})
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"弹幕时间偏移 提前 5s", "弹幕时间偏移 推迟 1s"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if a.danmaku.offset != 4*time.Second || !tt.HasText("提前 4s") {
		t.Fatalf("offset = %s, texts=%v", a.danmaku.offset, tt.Texts())
	}
	if err := tt.Click("弹幕时间偏移 推迟 5s"); err != nil {
		t.Fatal(err)
	}
	if a.danmaku.offset != -time.Second || !tt.HasText("推迟 1s") {
		t.Fatalf("negative offset = %s, texts=%v", a.danmaku.offset, tt.Texts())
	}
	if err := tt.Click("重置弹幕时间偏移"); err != nil {
		t.Fatal(err)
	}
	if a.danmaku.offset != 0 || !tt.HasText("0s") {
		t.Fatalf("reset failed: offset=%s", a.danmaku.offset)
	}
	a.danmaku.offset = 5 * time.Second
	a.stopDanmaku()
	if a.danmaku.offset != 0 {
		t.Fatalf("episode transition kept old offset: %s", a.danmaku.offset)
	}
}

func TestDanmakuFiltersDensityAndHighlightCurve(t *testing.T) {
	a := newKeyboardTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferencesPath = filepath.Join(t.TempDir(), "preferences.json")
	a.preferences.Danmaku.Enabled = true
	a.applyDanmaku(danmaku.Episode{ID: "101"}, []danmaku.Comment{
		{Time: 0, Mode: 1, Color: 0xffffff, Text: "前方高能"},
		{Time: time.Second, Mode: 1, Color: 0xffffff, Text: "前方高能"},
		{Time: 0, Mode: 5, Color: 0xffffff, Text: "顶部弹幕"},
		{Time: 0, Mode: 4, Color: 0xffffff, Text: "底部弹幕"},
		{Time: 0, Mode: 1, Color: 0xff0000, Text: "彩色弹幕"},
		{Time: 0, Mode: 1, Color: 0xffffff, Text: "这里有剧透"},
	})
	tt := ui.NewTester(a.view, 1120, 760)
	if _, ok := tt.Find("弹幕高能曲线"); !ok {
		t.Fatal("highlight curve did not appear for timed VOD comments")
	}
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"屏蔽顶部", "屏蔽底部", "屏蔽彩色", "防挡字幕"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if err := tt.Click("稀疏"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("弹幕屏蔽词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("剧透")
	prefs, err := readPlayerPreferences(a.preferencesPath)
	if err != nil || !prefs.Danmaku.HideTop || !prefs.Danmaku.HideBottom || !prefs.Danmaku.HideColor || !prefs.Danmaku.ProtectSubtitle || prefs.Danmaku.Density != "low" || prefs.Danmaku.Blocked != "剧透" {
		t.Fatalf("saved filter preferences = %+v, err=%v", prefs.Danmaku, err)
	}
	frame := a.danmaku.screen.Frame(time.Second, 800, 400, 20, 0.5, func(string) float32 { return 100 })
	if len(frame) != 1 || frame[0].Text != "前方高能 ×2" {
		t.Fatalf("filters and repeat merge did not apply live: %+v", frame)
	}
	// Turning off filters restores original comments without re-fetching.
	for _, label := range []string{"屏蔽顶部", "屏蔽底部", "屏蔽彩色", "合并刷屏"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	frame = a.danmaku.screen.Frame(time.Second, 800, 400, 20, 0.5, func(string) float32 { return 100 })
	if len(frame) != 5 {
		t.Fatalf("disabling filters did not restore comments: %+v", frame)
	}
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	a.preferences.Danmaku.Enabled = false
	tt.Frame()
	if _, ok := tt.Find("弹幕高能曲线"); ok {
		t.Fatal("highlight curve stayed visible while danmaku is disabled")
	}
}

func TestDanmakuEnterSubmitsSearchAndButtonShowsStatus(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	var calls atomic.Int32
	server := danmakuTestServer(t, &calls)
	a.preferences.Danmaku.API = server.URL
	a.startDanmaku()
	tt := ui.NewTester(a.view, 1120, 760)
	if a.danmakuButtonLabel() != "弹幕" {
		t.Fatalf("disabled label = %q", a.danmakuButtonLabel())
	}
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("弹幕集数"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("2")
	tt.Key(0, ui.KeyEnter)
	if calls.Load() != 1 || len(a.danmaku.episodes) != 1 || a.danmaku.episodes[0].ID != "102" {
		t.Fatalf("Enter in episode field did not trigger search: calls=%d episodes=%+v", calls.Load(), a.danmaku.episodes)
	}
	a.preferences.Danmaku.Enabled = true
	if got := a.danmakuButtonLabel(); got != "弹幕 · 待选" {
		t.Fatalf("ambiguous/pending label = %q, want 弹幕 · 待选", got)
	}
	if err := tt.Click("示例片 · 第2话 示例"); err != nil {
		t.Fatal(err)
	}
	if got := a.danmakuButtonLabel(); got != "弹幕 · 开" {
		t.Fatalf("loaded label = %q, want 弹幕 · 开", got)
	}
	// Verify loading state label while an in-flight request is active.
	started, release := make(chan struct{}), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		io.WriteString(w, `{"animes":[]}`)
	}))
	t.Cleanup(slow.Close)
	a.preferences.Danmaku.API = slow.URL
	updates := make(chan func(), 1)
	a.dispatch = func(update func()) { updates <- update }
	a.searchDanmaku(true)
	<-started
	if got := a.danmakuButtonLabel(); got != "弹幕 · 加载中" {
		t.Fatalf("loading label = %q, want 弹幕 · 加载中", got)
	}
	close(release)
	(<-updates)()
}
