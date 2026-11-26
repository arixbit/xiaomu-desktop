package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/danmaku"
	"github.com/egoist/mygo/ui"
)

func TestMatchDanmakuServiceLabels(t *testing.T) {
	results := []danmaku.Episode{
		{ID: "301", AnimeID: "a", Anime: "示例片(2026)【国产剧】from iqiyi", Name: "【qiyi】 示例片第3集 剧情标题"},
		{ID: "302", AnimeID: "b", Anime: "示例片(2026)【电视剧】from 360", Name: "【qiyi】 第3集"},
		{ID: "312", AnimeID: "a", Anime: "示例片(2026)【国产剧】from iqiyi", Name: "【qiyi】 示例片第12集 剧情标题"},
	}
	got, ok := matchDanmakuEpisode(results, "示例片", "第03集", 2, "")
	if !ok || got.ID != "301" {
		t.Fatalf("service labels prevented automatic matching: %+v, matched=%v", got, ok)
	}
	got, ok = matchDanmakuEpisode(results, "示例片", "第03集", 2, "b")
	if !ok || got.ID != "302" {
		t.Fatalf("manual source preference was lost: %+v, matched=%v", got, ok)
	}
	results[1].Anime = "示例片(2025)【电视剧】from 360"
	if _, ok := matchDanmakuEpisode(results, "示例片", "第03集", 2, ""); ok {
		t.Fatal("different editions were selected without confirmation")
	}
}

func TestDanmakuDefaultServiceWithoutSourceTrack(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	var calls atomic.Int32
	server := danmakuTestServer(t, &calls)
	client, err := danmaku.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var requestedAddresses []string
	a.newDanmakuClient = func(address string) (*danmaku.Client, error) {
		requestedAddresses = append(requestedAddresses, address)
		return client, nil
	}
	a.startDanmaku()
	tt := ui.NewTester(a.view, 1120, 760)
	for _, label := range []string{"弹幕", "弹幕开关"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if len(requestedAddresses) != 1 || requestedAddresses[0] != "" || a.preferences.Danmaku.API != "" || len(a.danmaku.comments) != 2 {
		t.Fatalf("default service was not used automatically: addresses=%v comments=%d", requestedAddresses, len(a.danmaku.comments))
	}
	if err := tt.Click("换集/搜片"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"弹幕片名", "弹幕集数", "搜索弹幕"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("manual search is hidden: %s", label)
		}
	}
	if _, ok := tt.Find("弹幕服务地址"); ok {
		t.Fatal("ordinary playback requires a custom service address")
	}
}

func TestDanmakuMatchingYearMoviePrefixAndSpaces(t *testing.T) {
	// Leading 【platform】 prefix before plain episode numbers.
	if got := danmakuNumber("【bilibili1】 01 开端"); got != 1 {
		t.Fatalf("danmakuNumber with leading tag = %d, want 1", got)
	}
	// Multi-year disambiguation with detail year.
	editions := []danmaku.Episode{
		{ID: "anime-1", AnimeID: "a", Anime: "示例片 (2022) 【动漫】 from bilibili1", Name: "【B站】 第1集"},
		{ID: "drama-1", AnimeID: "b", Anime: "示例片 (2023) 【电视剧】 from qq", Name: "【腾讯】 第1集"},
	}
	if _, ok := matchDanmakuEpisodeWithMeta(editions, "示例片", "", "第1集", 0, false, ""); ok {
		t.Fatal("matched ambiguous editions without detail year")
	}
	got, ok := matchDanmakuEpisodeWithMeta(editions, "示例片", "2023", "第1集", 0, false, "")
	if !ok || got.ID != "drama-1" {
		t.Fatalf("detail year did not disambiguate: got=%+v ok=%v", got, ok)
	}
	// Space differences in title.
	spaced := []danmaku.Episode{
		{ID: "s2-1", AnimeID: "s2", Anime: "示例片 第二季 (2024) 【电视剧】 from qq", Name: "【腾讯】 第1集"},
	}
	got, ok = matchDanmakuEpisodeWithMeta(spaced, "示例片第二季", "2024", "第1集", 0, false, "")
	if !ok || got.ID != "s2-1" {
		t.Fatalf("title spacing prevented match: got=%+v ok=%v", got, ok)
	}
	// Single-episode movie matching against HD / 蓝光 labels.
	movies := []danmaku.Episode{
		{ID: "m-qq", AnimeID: "m:qq", Anime: "示例电影 (2023) 【电影】 from qq", Name: "【腾讯】 正片"},
		{ID: "m-qiyi", AnimeID: "m:qiyi", Anime: "示例电影 (2023) 【电影】 from qiyi", Name: "【爱奇艺】 正片"},
	}
	got, ok = matchDanmakuEpisodeWithMeta(movies, "示例电影", "2023", "HD", 0, true, "")
	if !ok || got.ID != "m-qq" {
		t.Fatalf("single-episode movie did not match: got=%+v ok=%v", got, ok)
	}
}

func TestDanmakuPreservesCandidatesAndShowsFailureStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/search/episodes":
			io.WriteString(w, `{"animes":[{"animeId":"a","animeTitle":"示例片 (2024) 【电视剧】 from qq","episodes":[{"episodeId":"101","episodeTitle":"【腾讯】 第1集"}]},{"animeId":"b","animeTitle":"示例片 (2024) 【电视剧】 from qiyi","episodes":[{"episodeId":"102","episodeTitle":"【爱奇艺】 第1集"}]}]}`)
		case "/api/v2/comment/101":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	a := newPlayerTestApp(t)
	a.preferences = defaultPlayerPreferences()
	a.preferences.Danmaku.API = server.URL
	a.startDanmaku()

	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("弹幕"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("已关闭") {
		t.Fatalf("disabled popover did not explain status: %v", tt.Texts())
	}
	if err := tt.Click("弹幕开关"); err != nil {
		t.Fatal(err)
	}
	if len(a.danmaku.episodes) != 2 {
		t.Fatalf("comment failure discarded fetched episodes: %+v", a.danmaku.episodes)
	}
	if got := a.danmakuButtonLabel(); got != "弹幕 · 失败" {
		t.Fatalf("button label on comment failure = %q, want 弹幕 · 失败", got)
	}
	if _, ok := tt.Find("重试获取弹幕"); !ok {
		t.Fatalf("retry button missing after failure: %v", tt.Texts())
	}
}

func TestDanmakuVarietySeasonPartDateAndFallback(t *testing.T) {
	// Date strings must not be parsed as episode numbers, while date keys are preserved.
	if got := danmakuNumber("2026-10-10期"); got != 0 {
		t.Fatalf("danmakuNumber treated date as episode: %d", got)
	}
	if got := danmakuNumber("20261010上"); got != 0 {
		t.Fatalf("danmakuNumber treated compact date as episode: %d", got)
	}
	if got := danmakuNumber("第三期上"); got != 3 {
		t.Fatalf("danmakuNumber failed Chinese episode numeral: %d", got)
	}
	if got := danmakuDateKey("【腾讯】 第3期上：正片 2026-10-10期"); got != "20261010" {
		t.Fatalf("danmakuDateKey = %q, want 20261010", got)
	}
	if got := danmakuPartTag("【芒果】 第12期：突围之战上海站"); got != "" {
		t.Fatalf("danmakuPartTag falsely matched city name: %q", got)
	}
	if got := danmakuPartTag("【腾讯】 第3期上：正片 2026-10-10期"); got != "上" {
		t.Fatalf("danmakuPartTag = %q, want 上", got)
	}

	// Chinese season title ("第四季") matches Arabic season title ("第4季"),
	// narrows main episode vs 加更/纯享, distinguishes 上/下 when specified, and
	// stays ambiguous when the playing episode does not specify 上/下.
	variety := []danmaku.Episode{
		{ID: "v-up", AnimeID: "v:qq", Anime: "示例综艺 第4季 (2026) 【综艺】 from qq", Name: "【腾讯】 第3期上：正片 2026-10-10期"},
		{ID: "v-down", AnimeID: "v:qq", Anime: "示例综艺 第4季 (2026) 【综艺】 from qq", Name: "【腾讯】 第3期下：正片 2026-10-10期"},
		{ID: "v-extra", AnimeID: "v:qq", Anime: "示例综艺 第4季 (2026) 【综艺】 from qq", Name: "【腾讯】 第3期加更：幕后 2026-10-10期"},
		{ID: "v-clip", AnimeID: "v:qq", Anime: "示例综艺 第4季 (2026) 【综艺】 from qq", Name: "【腾讯】 第3期上纯享·片段 2026-10-10期"},
	}
	if _, ok := matchDanmakuEpisodeWithMeta(variety, "示例综艺 第四季", "2026", "第3期", 2, false, ""); ok {
		t.Fatal("silently guessed between 第3期上 and 第3期下")
	}
	got, ok := matchDanmakuEpisodeWithMeta(variety, "示例综艺 第四季", "2026", "第3期上", 2, false, "")
	if !ok || got.ID != "v-up" {
		t.Fatalf("failed to match 第3期上: got=%+v ok=%v", got, ok)
	}
	got, ok = matchDanmakuEpisodeWithMeta(variety, "示例综艺 第四季", "2026", "第3期加更", 2, false, "")
	if !ok || got.ID != "v-extra" {
		t.Fatalf("failed to match 第3期加更: got=%+v ok=%v", got, ok)
	}

	// Single main cut + extra cuts should automatically pick the main episode.
	singleMain := []danmaku.Episode{
		{ID: "s-main", AnimeID: "s:imgo", Anime: "示例音综 (2025) 【综艺】 from imgo", Name: "【芒果】 第12期：突围之战 2025-08-01期"},
		{ID: "s-plus", AnimeID: "s:imgo", Anime: "示例音综 (2025) 【综艺】 from imgo", Name: "【芒果】 第12期加更：幕后 2025-08-01期"},
	}
	got, ok = matchDanmakuEpisodeWithMeta(singleMain, "示例音综", "2025", "第12期", 11, false, "")
	if !ok || got.ID != "s-main" {
		t.Fatalf("failed to prefer main cut over extra cut: got=%+v ok=%v", got, ok)
	}

	// Date-only variety episodes match by air date and fall back to full list when
	// filtered by an episode number that does not exist on date-only titles.
	dateOnly := []danmaku.Episode{
		{ID: "d-1", AnimeID: "d:imgo", Anime: "示例跑男 第三季 (2019) 【综艺】 from imgo", Name: "【芒果】 精彩开场 2019-07-05期"},
		{ID: "d-2", AnimeID: "d:imgo", Anime: "示例跑男 第三季 (2019) 【综艺】 from imgo", Name: "【芒果】 收官之战 2019-07-12期"},
	}
	got, ok = matchDanmakuEpisodeWithMeta(dateOnly, "示例跑男 第3季", "2019", "20190712", 1, false, "")
	if !ok || got.ID != "d-2" {
		t.Fatalf("failed to match date-only variety episode: got=%+v ok=%v", got, ok)
	}
	filtered, fellBack := filterDanmakuEpisodes(dateOnly, 3)
	if !fellBack || len(filtered) != 2 {
		t.Fatalf("date-only variety did not fall back on episode filter: filtered=%+v fellBack=%v", filtered, fellBack)
	}
}
