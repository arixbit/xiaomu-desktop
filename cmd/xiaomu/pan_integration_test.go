package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestAListMP4PlaysThroughIsolatedProxySession(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{
					"total": 1,
					"content": []map[string]any{
						{"name": "示例4K电影.mp4", "is_dir": false, "size": 1024},
					},
				},
			})
		case "/api/fs/get":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{
					"name":    "示例4K电影.mp4",
					"raw_url": srv.URL + "/media/demo.mp4",
					"size":    1024,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &app{}
	defer a.close()

	var openedURL string
	a.open = func(localURL string) (*ui.VideoPlayer, error) {
		openedURL = localURL
		return ui.NewVideoPlayer(&stillVideo{})
	}

	extJSON, _ := json.Marshal(map[string]string{"公开盘": srv.URL})
	cfg := &config.Config{
		URL: srv.URL + "/cfg.json",
		Sites: []config.Site{{
			Key:  "alist",
			Name: "公开AList",
			Type: 3,
			API:  "csp_Alist",
			Ext:  string(extJSON),
			Kind: config.KindAList,
		}},
	}
	entry := library.Entry{Site: cfg.Sites[0], Config: cfg}
	a.openSite(entry)
	if len(a.browser.results.Videos) != 1 {
		t.Fatalf("expected 1 AList video, got %+v (status=%s)", a.browser.results.Videos, a.status)
	}
	v := a.browser.results.Videos[0]
	a.openTitle(v.Name, v.Pic, []library.Item{{Entry: entry, Video: v}})
	if a.title == nil || a.title.detail == nil || len(a.title.detail.Sources) != 1 {
		t.Fatalf("expected detail opened: title=%+v status=%s", a.title, a.status)
	}

	line := a.title.detail.Sources[0]
	a.playEpisode(line.Name, line.Episodes[0])
	if a.playFailure != nil || a.player == nil {
		t.Fatalf("expected AList MP4 playback, got failure=%+v status=%s", a.playFailure, a.status)
	}
	if !strings.HasPrefix(openedURL, "http://127.0.0.1:") || !strings.Contains(openedURL, "/s/") || !strings.HasSuffix(openedURL, ".mp4") {
		t.Fatalf("expected isolated proxy session MP4 URL, got %q", openedURL)
	}
}

func TestPanCredentialsPersistAndPlaybackStreamSwitch(t *testing.T) {
	dir := t.TempDir()
	credsPath := filepath.Join(dir, "pan_credentials.json")
	prev := pan.DefaultCredentials()
	defer pan.SetDefaultCredentials(prev)

	a := &app{persist: true}
	a.panUI.path = credsPath
	a.applyAuthorizedCredentials(pan.ProviderQuark, pan.Credentials{
		QuarkCookie:      "__pus=test_secret_cookie_123",
		TempDirName:      "我的测试临时目录",
		PreferredQuality: "4k",
	})

	b := &app{persist: true}
	b.panUI.path = credsPath
	b.restorePanCredentials()
	if b.panUI.creds.QuarkCookie != "__pus=test_secret_cookie_123" || b.panUI.creds.EffectiveQuality() != "4k" {
		t.Fatalf("restored credentials mismatch: %+v", b.panUI.creds)
	}
	if rt, at, did, ok := parseAlipanLocalStorageToken(`{"refresh_token":"rt1","access_token":"at1","default_drive_id":"d1"}`); !ok || rt != "rt1" || at != "at1" || did != "d1" {
		t.Fatalf("parseAlipanLocalStorageToken failed: %q %q %q %v", rt, at, did, ok)
	}

	// Verify ReadPlayback accepts push:// cloud-drive links without error.
	pb, err := catalog.ReadPlayback(`{"url":"push://https://pan.quark.cn/s/abc123","danmaku":"https://example.com/dm.xml"}`)
	if err != nil {
		t.Fatalf("ReadPlayback rejected push:// share link: %v", err)
	}
	if pb.PanShare != "push://https://pan.quark.cn/s/abc123" || pb.Danmaku != "https://example.com/dm.xml" {
		t.Fatalf("unexpected PanShare playback: %+v", pb)
	}

	// Verify in-player quality switch preserves position and switches across streams cleanly.
	mediaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "__pus=test_secret_cookie_123" {
			http.Error(w, "missing cookie", http.StatusForbidden)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:5.0,\nseg.ts\n"))
			return
		}
		_, _ = w.Write([]byte("RAW-MP4"))
	}))
	defer mediaSrv.Close()

	var openedURLs []string
	a.open = func(localURL string) (*ui.VideoPlayer, error) {
		openedURLs = append(openedURLs, localURL)
		resp, err := http.Get(localURL)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}
		return ui.NewVideoPlayer(&stillVideo{})
	}
	defer a.close()

	streams := []catalog.Stream{
		{
			Label:          "4K 转码",
			Quality:        "4k",
			URL:            mediaSrv.URL + "/4k.m3u8",
			Headers:        map[string]string{"Cookie": "__pus=test_secret_cookie_123"},
			Filename:       "stream.m3u8",
			NativePlayable: true,
			IsHLS:          true,
		},
		{
			Label:          "2K 转码",
			Quality:        "2k",
			URL:            mediaSrv.URL + "/2k.m3u8",
			Headers:        map[string]string{"Cookie": "__pus=test_secret_cookie_123"},
			Filename:       "stream.m3u8",
			NativePlayable: true,
			IsHLS:          true,
		},
		{
			Label:          "原画 MP4",
			Quality:        "original",
			URL:            mediaSrv.URL + "/raw.mp4",
			Headers:        map[string]string{"Cookie": "__pus=test_secret_cookie_123"},
			Filename:       "movie.mp4",
			NativePlayable: true,
			IsOriginal:     true,
		},
	}
	if !a.openEpisodeMedia(catalog.Playback{URL: streams[0].URL, Streams: streams}) {
		t.Fatalf("openEpisodeMedia failed: %s", a.status)
	}
	a.lastPosition = 42 * time.Second
	a.switchPlayingStream(1)
	if a.playingStreamIdx != 1 || a.resumeAt != 42*time.Second || len(openedURLs) != 2 {
		t.Fatalf("stream switch failed: idx=%d resumeAt=%v opened=%d status=%s", a.playingStreamIdx, a.resumeAt, len(openedURLs), a.status)
	}
	a.switchPlayingStream(2)
	if a.playingStreamIdx != 2 || len(openedURLs) != 3 || !strings.HasSuffix(openedURLs[2], "/movie.mp4") {
		t.Fatalf("expected original MP4 stream session URL, got idx=%d urls=%v status=%s", a.playingStreamIdx, openedURLs, a.status)
	}
}

func TestExpandDetailSharesPreservesWorkingEpisodeFlow(t *testing.T) {
	detail := catalog.Detail{
		Name: "普通影片",
		Sources: []catalog.Source{{
			Name:     "线路1",
			Episodes: []catalog.Episode{{Name: "第1集", ID: "https://example.com/1.m3u8"}},
		}},
	}
	out, handled, err := pan.ExpandDetailShares(context.Background(), detail)
	if err != nil || handled || len(out.Sources) != 1 {
		t.Fatalf("unexpected result: out=%+v handled=%v err=%v", out, handled, err)
	}
}

func TestPlayUnauthenticatedPanPromptsLoginAndResumesAutomatically(t *testing.T) {
	prev := pan.DefaultCredentials()
	defer pan.SetDefaultCredentials(prev)
	pan.SetDefaultCredentials(pan.Credentials{})

	mediaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:5.0,\nseg.ts\n"))
	}))
	defer mediaSrv.Close()

	a := &app{persist: true}
	a.panUI.path = filepath.Join(t.TempDir(), "pan_credentials.json")
	defer a.close()

	resumed := false
	playCalls := 0
	sess := &recoverySession{
		play: func(_ context.Context, _ string, _ catalog.Episode) (catalog.Playback, error) {
			playCalls++
			if !pan.DefaultCredentials().Has(pan.ProviderQuark) {
				return catalog.Playback{}, &pan.AuthRequiredError{Provider: pan.ProviderQuark, Reason: "播放夸克网盘资源需要先登录账号"}
			}
			return catalog.Playback{
				URL: mediaSrv.URL + "/4k.m3u8",
				Streams: []catalog.Stream{{
					Label:          "4K 转码",
					Quality:        "4k",
					URL:            mediaSrv.URL + "/4k.m3u8",
					Filename:       "stream.m3u8",
					NativePlayable: true,
					IsHLS:          true,
				}},
			}, nil
		},
	}
	a.open = func(string) (*ui.VideoPlayer, error) {
		resumed = true
		return ui.NewVideoPlayer(&stillVideo{})
	}
	var loginTriggered pan.Provider
	a.startPanLogin = func(p pan.Provider, onSuccess func()) {
		loginTriggered = p
		a.applyAuthorizedCredentials(p, pan.Credentials{QuarkCookie: "__pus=authorized_from_browser"})
		if onSuccess != nil {
			onSuccess()
		}
	}

	a.nav = []routeKind{routeHome, routeDetail}
	a.title = &titlePage{
		name:    "示例4K网盘影片",
		session: sess,
		failed:  map[string]string{},
		detail: &catalog.Detail{
			Name: "示例4K网盘影片",
			Sources: []catalog.Source{{
				Name:     "夸克网盘",
				Episodes: []catalog.Episode{{Name: "第1集", ID: "ep-quark-1"}},
			}},
		},
	}

	a.playEpisode("夸克网盘", a.title.detail.Sources[0].Episodes[0])
	if !a.panUI.promptOpen || a.panUI.promptProvider != pan.ProviderQuark {
		t.Fatalf("expected login prompt for quark, got open=%v provider=%s failure=%+v", a.panUI.promptOpen, a.panUI.promptProvider, a.playFailure)
	}

	tt := ui.NewTester(a.view, 960, 640)
	if err := tt.Click("打开夸克网盘官方登录页"); err != nil {
		t.Fatalf("click login prompt button: %v (texts=%v)", err, tt.Texts())
	}
	if loginTriggered != pan.ProviderQuark || !resumed || playCalls != 2 || !a.panUI.creds.Has(pan.ProviderQuark) {
		t.Fatalf("login+resume incomplete: triggered=%s resumed=%v playCalls=%d hasCred=%v", loginTriggered, resumed, playCalls, a.panUI.creds.Has(pan.ProviderQuark))
	}
}

func TestSourcesPageSurfacesPanAndAListSitesDirectly(t *testing.T) {
	a := &app{}
	defer a.close()
	doc := &config.Config{
		URL: "https://example.com/line.json",
		Sites: []config.Site{
			{Key: "drpy", Name: "普通站点", Type: 3, API: "https://example.com/drpy2.js", Ext: "https://example.com/rule.js", Kind: config.KindDRPY2},
			{Key: "wogg", Name: "玩偶4K示例", Type: 3, API: "csp_Wogg", Ext: "https://wogg.example.com", Kind: config.KindPanWeb},
			{Key: "alist", Name: "公开AList示例", Type: 3, API: "csp_Alist", Ext: `{"盘":"https://alist.example.com"}`, Kind: config.KindAList},
		},
	}
	a.library.root = doc
	// Even when the line is collapsed (open=false), pan/AList sites are surfaced in the pan card.
	a.library.documents = []libraryDocument{{doc: doc, line: "示例线路", open: false}}
	a.nav = []routeKind{routeSources}

	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	if !tt.HasText("已识别的网盘 / AList 站点（2 个，点击直接浏览）") {
		t.Fatalf("pan sites summary missing: %v", tt.Texts())
	}
	if !tt.HasText("玩偶4K示例") || !tt.HasText("公开AList示例") {
		t.Fatalf("pan site buttons missing while line is collapsed: %v", tt.Texts())
	}
}

func TestFormatCookiesForHostKeepsPanBaiduSTOKEN(t *testing.T) {
	cookies := []mygo.Cookie{
		{Name: "BDUSS", Value: "bduss_ok", Domain: ".baidu.com"},
		{Name: "STOKEN", Value: "pan_stoken_ok", Domain: "pan.baidu.com"},
		{Name: "STOKEN", Value: "passport_wrong", Domain: "passport.baidu.com"},
		{Name: "STOKEN", Value: "parent_wrong", Domain: ".baidu.com"},
	}
	got := formatCookiesForHost(cookies, "pan.baidu.com")
	if !strings.Contains(got, "BDUSS=bduss_ok") || !strings.Contains(got, "STOKEN=pan_stoken_ok") || strings.Contains(got, "wrong") {
		t.Fatalf("unexpected baidu cookie header: %q", got)
	}
}
