package main

import (
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
	for _, label := range []string{"弹幕片名", "弹幕集数", "搜索弹幕"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("manual search is hidden: %s", label)
		}
	}
	if _, ok := tt.Find("弹幕服务地址"); ok {
		t.Fatal("ordinary playback requires a custom service address")
	}
}
