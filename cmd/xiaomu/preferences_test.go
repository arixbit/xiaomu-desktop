package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo/ui"
)

func TestPlaybackPreferencesSurviveRestartAndLivePlayback(t *testing.T) {
	a := newPlayerTestApp(t)
	a.preferencesPath = filepath.Join(t.TempDir(), "preferences.json")
	a.panelOpen = true
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("播放速度"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("1.25×"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("自动下一集"); err != nil {
		t.Fatal(err)
	}
	volume, ok := tt.Find("调节音量")
	if !ok {
		t.Fatal("no volume control")
	}
	tt.ClickAt(volume.X, volume.Y+volume.H/2) // persist zero, not a default sentinel
	prefs, err := readPlayerPreferences(a.preferencesPath)
	if err != nil || prefs.Rate != 1.25 || prefs.Volume != 0 || prefs.AutoNext {
		t.Fatalf("saved preferences: %+v, %v", prefs, err)
	}
	b := &app{preferences: prefs, autoNext: prefs.AutoNext}
	defer b.close()
	b.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	if !b.playURL("https://example.com/video.mp4", nil) || b.player.Volume() != 0 || b.player.Rate() != 1.25 {
		t.Fatal("preferences were not applied to a new player")
	}
	b.live.channels = []iptv.Channel{{Name: "本地频道", Streams: []iptv.Stream{{URL: "https://example.com/live.m3u8"}}}}
	b.playLiveChannel(0, 0)
	if !b.playingLive || b.player.Rate() != 1 || b.player.Volume() != 0 {
		t.Fatal("live playback should keep volume but use normal speed")
	}
	if !b.playURL("https://example.com/next.mp4", nil) || b.player.Rate() != 1.25 {
		t.Fatal("live playback overwrote the preferred VOD speed")
	}
}

func TestReadPlaybackPreferencesDefaultsAndInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if got, err := readPlayerPreferences(path); err != nil || got != defaultPlayerPreferences() {
		t.Fatalf("missing file: %+v %v", got, err)
	}
	defSub := defaultSubtitlePreferences()
	for _, tc := range []struct {
		data string
		want playerPreferences
		bad  bool
	}{
		{`{"auto_next":false}`, playerPreferences{Volume: 1, Rate: 1, Danmaku: defaultDanmakuPreferences(), Subtitle: defSub}, false},
		{`{"volume":0,"rate":2}`, playerPreferences{Volume: 0, Rate: 2, AutoNext: true, Danmaku: defaultDanmakuPreferences(), Subtitle: defSub}, false},
		{`{"volume":4,"rate":-1}`, defaultPlayerPreferences(), false},
		{`{"danmaku":{"enabled":true,"api":"https://example.com/api/v2","size":999,"opacity":-1}}`, playerPreferences{
			Volume: 1, Rate: 1, AutoNext: true,
			Danmaku:  danmakuPreferences{Enabled: true, API: "https://example.com/api/v2", Size: 22, Opacity: 0.85, Speed: 0.5, Area: 0.5, MergeRepeat: true},
			Subtitle: defSub,
		}, false},
		{`{"danmaku":{"speed":0.25,"area":0.75}}`, playerPreferences{
			Volume: 1, Rate: 1, AutoNext: true,
			Danmaku:  danmakuPreferences{Size: 22, Opacity: 0.85, Speed: 0.25, Area: 0.75, MergeRepeat: true},
			Subtitle: defSub,
		}, false},
		{`{"danmaku":{"density":"low","hide_top":true,"hide_bottom":true,"hide_color":true,"merge_repeat":false,"protect_subtitle":true,"blocked":" 剧透, 广告 "}}`, playerPreferences{
			Volume: 1, Rate: 1, AutoNext: true,
			Danmaku: danmakuPreferences{
				Size: 22, Opacity: 0.85, Speed: 0.5, Area: 0.5,
				Density: "low", HideTop: true, HideBottom: true, HideColor: true,
				MergeRepeat: false, ProtectSubtitle: true, Blocked: "剧透, 广告",
			},
			Subtitle: defSub,
		}, false},
		{`{"subtitle":{"scale":1.5,"pos":85,"prefer_chinese":false,"assrt_token":" tok123 "}}`, playerPreferences{
			Volume: 1, Rate: 1, AutoNext: true,
			Danmaku:  defaultDanmakuPreferences(),
			Subtitle: subtitlePreferences{Scale: 1.5, Pos: 85, PreferChinese: false, ASSRTToken: "tok123"},
		}, false},
		{`{"subtitle":{"scale":9,"pos":10}}`, playerPreferences{
			Volume: 1, Rate: 1, AutoNext: true,
			Danmaku:  defaultDanmakuPreferences(),
			Subtitle: subtitlePreferences{Scale: 1.0, Pos: 100, PreferChinese: true},
		}, false},
		{`{"danmaku":{"density":"bogus"}}`, defaultPlayerPreferences(), false},
		{`{"danmaku":{"speed":0,"area":2}}`, defaultPlayerPreferences(), false},
		{`{"danmaku":{"api":"  "}}`, defaultPlayerPreferences(), false},
		{`{`, defaultPlayerPreferences(), true},
	} {
		if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := readPlayerPreferences(path)
		if got != tc.want || (err != nil) != tc.bad {
			t.Fatalf("%s: %+v, %v", tc.data, got, err)
		}
	}
}
