package pan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func TestParseAndExtractShareURLs(t *testing.T) {
	cases := []struct {
		in       string
		provider Provider
		shareID  string
		pwd      string
	}{
		{"https://pan.quark.cn/s/abc123DEF456", ProviderQuark, "abc123DEF456", ""},
		{"push://https://pan.quark.cn/s/abc123?pwd=9x8y", ProviderQuark, "abc123", "9x8y"},
		{"https://drive.uc.cn/s/uc987654 提取码: k9p2", ProviderUC, "uc987654", "k9p2"},
		{"https://www.alipan.com/s/ali555666", ProviderAli, "ali555666", ""},
		{"https://115.com/s/sw333444", Provider115, "sw333444", ""},
		{"https://cloud.189.cn/t/ty112233", ProviderTianYi, "ty112233", ""},
		{"https://cloud.189.cn/web/share?code=ty445566", ProviderTianYi, "ty445566", ""},
		{"https://pan.baidu.com/s/1bd7788", ProviderBaidu, "1bd7788", ""},
		{"https://pan.xunlei.com/s/xl9900", ProviderThunder, "xl9900", ""},
	}
	for _, tc := range cases {
		ref, ok := ParseShareURL(tc.in)
		if !ok {
			t.Fatalf("ParseShareURL(%q) failed", tc.in)
		}
		if ref.Provider != tc.provider || ref.ShareID != tc.shareID || ref.Passcode != tc.pwd {
			t.Fatalf("ParseShareURL(%q) = %+v, want provider=%s id=%s pwd=%s", tc.in, ref, tc.provider, tc.shareID, tc.pwd)
		}
	}
	if _, ok := ParseShareURL("https://example.com/video.mp4"); ok {
		t.Fatal("ordinary video URL must not be parsed as cloud share")
	}

	html := `
		<a href="https://pan.quark.cn/s/dup111">夸克4K</a>
		<a href="https://pan.quark.cn/s/dup111#/list/share">重复</a> 提取码：abcd
		<a href="https://drive.uc.cn/s/uc222?pwd=1234">UC4K</a>
	`
	refs := ExtractShareURLs(html)
	if len(refs) != 2 {
		t.Fatalf("extracted %d refs, want 2: %+v", len(refs), refs)
	}
	if refs[0].Provider != ProviderQuark || refs[0].ShareID != "dup111" || refs[0].Passcode != "abcd" {
		t.Fatalf("first ref = %+v", refs[0])
	}
	if refs[1].Provider != ProviderUC || refs[1].ShareID != "uc222" || refs[1].Passcode != "1234" {
		t.Fatalf("second ref = %+v", refs[1])
	}
}

func TestNaturalLessAndEpisodeEncoding(t *testing.T) {
	if !NaturalLess("S01E02.4K.mkv", "S01E10.4K.mkv") {
		t.Fatal("expected S01E02 < S01E10")
	}
	if !NaturalLess("第2集.mp4", "第10集.mp4") {
		t.Fatal("expected 第2集 < 第10集")
	}
	item := FileItem{
		Provider:      ProviderQuark,
		ShareID:       "sid1",
		Passcode:      "pwd1",
		FileID:        "fid1",
		ShareFidToken: "tok1",
		Name:          "示例剧.S01E01.2160p.mkv",
		Size:          4294967296,
	}
	encoded := EncodeEpisodeID(item)
	decoded, ok := DecodeEpisodeID(encoded)
	if !ok || decoded != item {
		t.Fatalf("round-trip failed: encoded=%s decoded=%+v", encoded, decoded)
	}
}

func TestCredentialsSaveLoadPermissionsAndMask(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pan_credentials.json")
	in := Credentials{
		QuarkCookie:      "  __pus=secret1234567890; kps=xyz  \n",
		UCCookie:         "uc_session=abcdef123456",
		TempDirName:      "测试临时目录",
		PreferredQuality: "4k",
	}
	if err := SaveCredentials(p, in); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Fatalf("credentials file mode = %o, want 0600", perm)
		}
	}
	got, err := LoadCredentials(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.QuarkCookie != "__pus=secret1234567890; kps=xyz" || got.EffectiveTempDir() != "测试临时目录" || got.EffectiveQuality() != "4k" {
		t.Fatalf("loaded = %+v", got)
	}
	masked := got.Masked(ProviderQuark)
	if strings.Contains(masked, "secret1234567890") {
		t.Fatalf("masked leaked secret: %s", masked)
	}
}

func newFakeQuarkServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var deleted []string
	tempDirCreated := false
	taskPolls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/member":
			if !strings.Contains(r.Header.Get("Cookie"), "valid_cookie") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"member_type": "SUPER_VIP"},
			})
		case "/share/sharepage/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"stoken": "stoken-ok"},
			})
		case "/share/sharepage/detail":
			pdir := r.URL.Query().Get("pdir_fid")
			if pdir == "0" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"list": []map[string]any{
							{"fid": "dir-s1", "file_name": "第一季", "dir": true, "file_type": 0},
							{"fid": "f-readme", "file_name": "说明.txt", "dir": false, "file_type": 1},
						},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "f-e10", "file_name": "示例剧.S01E10.2160p.mkv", "size": 5000000, "dir": false, "file_type": 1, "share_fid_token": "tok10"},
						{"fid": "f-e2", "file_name": "示例剧.S01E02.2160p.mkv", "size": 4000000, "dir": false, "file_type": 1, "share_fid_token": "tok2"},
					},
				},
			})
		case "/file/sort":
			if !strings.Contains(r.Header.Get("Cookie"), "valid_cookie") {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			pdir := r.URL.Query().Get("pdir_fid")
			mu.Lock()
			created := tempDirCreated
			mu.Unlock()
			if pdir == "0" {
				list := []map[string]any{}
				if created {
					list = append(list, map[string]any{"fid": "temp-dir-fid", "file_name": DefaultTempDirName, "dir": true, "file_type": 0})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"list": list}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"list": []map[string]any{{"fid": "saved-fid-1"}}},
			})
		case "/file":
			mu.Lock()
			tempDirCreated = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"fid": "temp-dir-fid"}})
		case "/share/sharepage/save":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"task_id": "task-1"}})
		case "/task":
			mu.Lock()
			taskPolls++
			polls := taskPolls
			mu.Unlock()
			if polls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"status": 0}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"status":  2,
					"save_as": map[string]any{"save_as_top_fids": []string{"saved-fid-1"}},
				},
			})
		case "/file/v2/play":
			http.SetCookie(w, &http.Cookie{Name: "__puus", Value: "cdn_token_99"})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"video_list": []map[string]any{
						{"resolution": "super", "video_info": map[string]any{"url": "https://cdn.example.com/1080p.m3u8", "width": 1920, "height": 1080}},
						{"resolution": "4k", "video_info": map[string]any{"url": "https://cdn.example.com/4k.m3u8", "width": 3840, "height": 2160}},
					},
				},
			})
		case "/file/download":
			http.SetCookie(w, &http.Cookie{Name: "__puus", Value: "dl_token_77"})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": []map[string]any{
					{"download_url": "https://dl.example.com/raw.mkv", "file_name": "示例剧.S01E02.2160p.mkv", "size": 4000000},
				},
			})
		case "/file/delete":
			var req struct {
				Filelist []string `json:"filelist"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			deleted = append(deleted, req.Filelist...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, &deleted
}

func TestQuarkClientFullLifecycle(t *testing.T) {
	srv, deleted := newFakeQuarkServer(t)
	defer srv.Close()

	creds := Credentials{QuarkCookie: "__pus=valid_cookie", PreferredQuality: "auto"}
	client := NewQuarkClient(ClientOptions{
		Provider:    ProviderQuark,
		BaseURL:     srv.URL,
		HTTPClient:  srv.Client(),
		Credentials: &creds,
	})

	ctx := context.Background()
	auth, err := client.CheckAuth(ctx)
	if err != nil || !auth.Valid || auth.VIP != "SVIP" {
		t.Fatalf("CheckAuth = %+v, err=%v", auth, err)
	}

	ref, _ := ParseShareURL("https://pan.quark.cn/s/share123")
	files, err := client.ExpandShare(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != "示例剧.S01E02.2160p.mkv" || files[1].Name != "示例剧.S01E10.2160p.mkv" {
		t.Fatalf("unexpected expanded files (natural sort): %+v", files)
	}

	play, err := client.ResolvePlayback(ctx, files[0])
	if err != nil {
		t.Fatal(err)
	}
	if play.URL != "https://cdn.example.com/4k.m3u8" {
		t.Fatalf("auto preference should pick 4K transcoded HLS for MKV source, got %q", play.URL)
	}
	if len(play.Streams) != 3 {
		t.Fatalf("expected 3 streams (4k, 1080p, original mkv), got %d: %+v", len(play.Streams), play.Streams)
	}
	if !strings.Contains(play.Headers["Cookie"], "__puus=cdn_token_99") {
		t.Fatalf("expected Set-Cookie merged into stream headers, got %q", play.Headers["Cookie"])
	}
	orig := play.Streams[2]
	if !orig.IsOriginal || orig.NativePlayable || !strings.Contains(orig.Headers["Cookie"], "__puus=dl_token_77") {
		t.Fatalf("unexpected original MKV stream: %+v", orig)
	}

	cleaned, err := client.CleanupTemp(ctx)
	if err != nil || cleaned != 1 || len(*deleted) != 1 || (*deleted)[0] != "saved-fid-1" {
		t.Fatalf("CleanupTemp cleaned=%d deleted=%v err=%v", cleaned, *deleted, err)
	}
}

func TestShareListAndWebSiteAdapters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list.txt":
			_, _ = w.Write([]byte("4K电影,#genre#\n星际示例$https://pan.quark.cn/s/movie4k\n科幻剧,三体示例,https://drive.uc.cn/s/uc4k\n"))
		case "/":
			_, _ = w.Write([]byte(`<html><body>
				<nav><a href="/vodshow/1--------1---.html">电影</a><a href="/vodshow/2--------1---.html">剧集</a></nav>
				<div class="module-item">
					<a href="/voddetail/42.html" title="示例4K大片"><img data-original="/img/42.jpg"></a>
					<div class="module-item-note">4K HDR</div>
				</div>
			</body></html>`))
		case "/index.php/vod/search/page/1/wd/示例.html":
			_, _ = w.Write([]byte(`<html><body>
				<div class="module-search-item">
					<a href="/voddetail/42.html" title="示例4K大片"></a>
				</div>
			</body></html>`))
		case "/voddetail/42.html":
			_, _ = w.Write([]byte(`<html><body><h1>示例4K大片</h1><a href="https://pan.quark.cn/s/site4k">夸克网盘</a></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// ShareList adapter test (both full-URL format and real bare share-ID format).
	sl, cats, err := OpenShareList(context.Background(), config.Site{
		API:  "csp_QuarkShare",
		Kind: config.KindPanShare,
		Ext:  srv.URL + "/token.json$$$" + srv.URL + "/list.txt$$$db$$$1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 2 {
		t.Fatalf("cats = %+v", cats)
	}
	found, err := sl.Search(context.Background(), "三体", 1)
	if err != nil || len(found.Videos) != 1 || found.Videos[0].Name != "三体示例" {
		t.Fatalf("sharelist search = %+v err=%v", found, err)
	}

	bareEntries := parseShareListText("self 我的夸克网盘\nabcdef123456 每日4K电影 1\nxyz987654?pwd=6666 科幻|星际穿越 1 DESC\n", ProviderQuark)
	if len(bareEntries) != 2 {
		t.Fatalf("bareEntries = %+v, want 2", bareEntries)
	}
	if bareEntries[0].Name != "每日4K电影" || bareEntries[0].Refs[0].ShareID != "abcdef123456" {
		t.Fatalf("bareEntries[0] = %+v", bareEntries[0])
	}
	if bareEntries[1].Category != "科幻" || bareEntries[1].Name != "星际穿越" || bareEntries[1].Refs[0].Passcode != "6666" {
		t.Fatalf("bareEntries[1] = %+v", bareEntries[1])
	}

	// Wogg-style WebSite adapter test (home + categories + search).
	ws, wcats, err := OpenWebSite(context.Background(), config.Site{
		Kind: config.KindPanWeb,
		Ext:  srv.URL + "/token.json$$$" + srv.URL + "/$$$null$$$1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wcats) != 2 {
		t.Fatalf("web cats = %+v", wcats)
	}
	home, err := ws.Home(context.Background())
	if err != nil || len(home.Videos) != 1 || home.Videos[0].Name != "示例4K大片" {
		t.Fatalf("web home = %+v err=%v", home, err)
	}
	searched, err := ws.Search(context.Background(), "示例", 1)
	if err != nil || len(searched.Videos) != 1 {
		t.Fatalf("web search = %+v err=%v", searched, err)
	}
}

func TestAliClientAuthExpandAndPlay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/account/token":
			var req struct {
				RefreshToken string `json:"refresh_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.RefreshToken != "valid_refresh" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":     "access_ok",
				"refresh_token":    "valid_refresh",
				"default_drive_id": "drive-1",
				"expires_in":       7200,
			})
		case "/v2/share_link/get_share_token":
			_ = json.NewEncoder(w).Encode(map[string]any{"share_token": "ali-stoken"})
		case "/adrive/v3/file/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"file_id": "af2", "name": "电影.S01E10.2160p.mkv", "type": "file", "size": 2000},
					{"file_id": "af1", "name": "电影.S01E02.2160p.mkv", "type": "file", "size": 1000},
				},
			})
		case "/v2/file/get_share_video_preview_play_info":
			if r.Header.Get("Authorization") != "Bearer access_ok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"video_preview_play_info": map[string]any{
					"live_transcoding_task_list": []map[string]any{
						{"template_id": "FHD", "status": "finished", "url": "https://cdn.example.com/ali-1080p.m3u8"},
						{"template_id": "UHD", "status": "finished", "url": "https://cdn.example.com/ali-4k.m3u8"},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Unauthenticated playback must signal AuthRequiredError for ProviderAli.
	emptyCreds := Credentials{}
	unauth := NewAliClient(AliClientOptions{APIBase: srv.URL, AuthBase: srv.URL, HTTPClient: srv.Client(), Credentials: &emptyCreds})
	_, err := unauth.ResolvePlayback(context.Background(), FileItem{Provider: ProviderAli, ShareID: "s1", FileID: "af1"})
	if p, ok := AuthRequiredProvider(err); !ok || p != ProviderAli {
		t.Fatalf("expected AuthRequiredError(ali), got err=%v", err)
	}

	// Authenticated flow expands files in natural order and picks 4K UHD stream.
	creds := Credentials{AliToken: "valid_refresh", PreferredQuality: "auto"}
	client := NewAliClient(AliClientOptions{APIBase: srv.URL, AuthBase: srv.URL, HTTPClient: srv.Client(), Credentials: &creds})
	ref, _ := ParseShareURL("https://www.alipan.com/s/aliShare123")
	files, err := client.ExpandShare(context.Background(), ref)
	if err != nil || len(files) != 2 || files[0].Name != "电影.S01E02.2160p.mkv" {
		t.Fatalf("ali expand = %+v err=%v", files, err)
	}
	play, err := client.ResolvePlayback(context.Background(), files[0])
	if err != nil || play.URL != "https://cdn.example.com/ali-4k.m3u8" || len(play.Streams) != 2 {
		t.Fatalf("ali play = %+v err=%v", play, err)
	}
}
