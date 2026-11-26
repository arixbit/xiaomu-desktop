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
		{"https://pan.baidu.com/share/init?surl=bd9900&pwd=abcd", ProviderBaidu, "1bd9900", "abcd"},
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
	if IsNativePlayableFilename(files[0].Name) {
		if play.URL != "https://dl.example.com/raw.mkv" {
			t.Fatalf("native platform should play original MKV directly without transcoding, got %q", play.URL)
		}
		if len(play.Streams) != 1 || !play.Streams[0].IsOriginal || !play.Streams[0].NativePlayable {
			t.Fatalf("expected single original MKV stream, got %+v", play.Streams)
		}
		if !strings.Contains(play.Headers["Cookie"], "__puus=dl_token_77") {
			t.Fatalf("expected download Set-Cookie merged into stream headers, got %q", play.Headers["Cookie"])
		}
	} else {
		if play.URL != "https://cdn.example.com/4k.m3u8" {
			t.Fatalf("non-native MKV fallback should pick 4K transcoded HLS, got %q", play.URL)
		}
		if len(play.Streams) != 3 {
			t.Fatalf("expected 3 fallback streams (4k, 1080p, original mkv), got %d: %+v", len(play.Streams), play.Streams)
		}
		if !strings.Contains(play.Headers["Cookie"], "__puus=cdn_token_99") {
			t.Fatalf("expected Set-Cookie merged into stream headers, got %q", play.Headers["Cookie"])
		}
	}

	cleaned, err := client.CleanupTemp(ctx)
	if err != nil || cleaned != 1 || len(*deleted) != 1 || (*deleted)[0] != "saved-fid-1" {
		t.Fatalf("CleanupTemp cleaned=%d deleted=%v err=%v", cleaned, *deleted, err)
	}
}

func TestQuarkClientDropsLowTranscodeAndSkipsPlayWhenOriginalNative(t *testing.T) {
	playCalls := 0
	var requestedResolutions string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/file/download":
			if r.Header.Get("X-Fail-Download") != "" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": []map[string]any{
					{"download_url": "https://dl.example.com/movie.mp4", "file_name": "movie.mp4", "size": 1024},
				},
			})
		case "/file/v2/play":
			playCalls++
			var body struct {
				Resolutions string `json:"resolutions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			requestedResolutions = body.Resolutions
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"video_list": []map[string]any{
						{"resolution": "low", "video_info": map[string]any{"url": "https://cdn.example.com/low.m3u8", "width": 640, "height": 360}},
						{"resolution": "4k", "video_info": map[string]any{"url": "https://cdn.example.com/4k.m3u8", "width": 3840, "height": 2160}},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	creds := Credentials{QuarkCookie: "__pus=valid_cookie"}
	client := NewQuarkClient(ClientOptions{
		Provider:    ProviderQuark,
		BaseURL:     srv.URL,
		HTTPClient:  srv.Client(),
		Credentials: &creds,
	})

	// Native original MP4 must not invoke /file/v2/play and must expose only the original stream.
	pb, err := client.ResolveSavedFile(context.Background(), "fid1", "movie.mp4", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if playCalls != 0 || len(pb.Streams) != 1 || !pb.Streams[0].IsOriginal || pb.URL != "https://dl.example.com/movie.mp4" {
		t.Fatalf("expected original-only playback without transcode API call: playCalls=%d pb=%+v", playCalls, pb)
	}

	// Fallback path (non-native extension like .iso) must never request or include low/normal ("流畅转码").
	pb2, err := client.ResolveSavedFile(context.Background(), "fid2", "disc.iso", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if playCalls != 1 || strings.Contains(requestedResolutions, "low") || strings.Contains(requestedResolutions, "normal") {
		t.Fatalf("unexpected fallback transcode request: calls=%d resolutions=%q", playCalls, requestedResolutions)
	}
	for _, s := range pb2.Streams {
		if s.Quality == "low" || s.Label == "流畅转码" {
			t.Fatalf("low transcode stream leaked into fallback: %+v", pb2.Streams)
		}
	}
}

func TestQuarkPurgesStaleTempAndRecycleBeforeSaveAndFormatsCapacityError(t *testing.T) {
	var mu sync.Mutex
	var purgedRecords []string
	var deletedFids []string
	saveCalls := 0
	alwaysFull := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/file/sort":
			if r.URL.Query().Get("pdir_fid") == "0" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"list": []map[string]any{{"fid": "temp-dir", "file_name": DefaultTempDirName, "dir": true, "file_type": 0}},
					},
				})
				return
			}
			mu.Lock()
			hasOld := len(deletedFids) == 0
			mu.Unlock()
			list := []map[string]any{}
			if hasOld {
				list = append(list, map[string]any{"fid": "old-4k-fid", "file_name": "old.4k.mkv", "size": 9000000000})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"list": list}})
		case "/file/delete":
			var req struct {
				Filelist []string `json:"filelist"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			deletedFids = append(deletedFids, req.Filelist...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
		case "/file/recycle/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "old-4k-fid", "record_id": "rec-old-4k"},
						{"fid": "user-personal-fid", "record_id": "rec-user-keep"},
					},
				},
			})
		case "/file/recycle/remove":
			var req struct {
				RecordList []string `json:"record_list"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			purgedRecords = append(purgedRecords, req.RecordList...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
		case "/share/sharepage/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"stoken": "st-ok"}})
		case "/share/sharepage/save":
			mu.Lock()
			saveCalls++
			full := alwaysFull
			mu.Unlock()
			if full {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 41016, "message": "capacity limit[{0}]"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"save_as": map[string]any{"save_as_top_fids": []string{"new-4k-fid"}}},
			})
		case "/file/download":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": []map[string]any{{"download_url": "https://dl.example.com/new.mkv", "file_name": "new.4k.mkv", "size": 1000}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	creds := Credentials{QuarkCookie: "__pus=ok"}
	client := NewQuarkClient(ClientOptions{Provider: ProviderQuark, BaseURL: srv.URL, HTTPClient: srv.Client(), Credentials: &creds})
	pb, err := client.ResolvePlayback(context.Background(), FileItem{
		Provider: ProviderQuark, ShareID: "s1", FileID: "f1", ShareFidToken: "tok1", Name: "new.4k.mkv", Size: 1000,
	})
	if err != nil || pb.URL != "https://dl.example.com/new.mkv" {
		t.Fatalf("ResolvePlayback = %+v err=%v", pb, err)
	}
	if len(deletedFids) != 1 || deletedFids[0] != "old-4k-fid" {
		t.Fatalf("stale temp file not deleted before save: %v", deletedFids)
	}
	if len(purgedRecords) != 1 || purgedRecords[0] != "rec-old-4k" {
		t.Fatalf("expected only our temp file purged from recycle bin (preserving user files): %v", purgedRecords)
	}

	// Verify persistent capacity limit surfaces a clear Chinese error with file size.
	mu.Lock()
	alwaysFull = true
	mu.Unlock()
	_, err = client.ResolvePlayback(context.Background(), FileItem{
		Provider: ProviderQuark, ShareID: "s2", FileID: "f2", ShareFidToken: "tok2", Name: "huge.4k.mkv", Size: 15 * 1024 * 1024 * 1024,
	})
	if err == nil || !strings.Contains(err.Error(), "夸克网盘剩余空间不足") || !strings.Contains(err.Error(), "15.0 GB") {
		t.Fatalf("expected friendly capacity limit error with size, got %v", err)
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

func TestWebSiteContentSearchPrefersDetailLinkAndSkipsHome(t *testing.T) {
	homeHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			homeHits++
			http.Error(w, "home should not be fetched by OpenWebSiteContent", http.StatusInternalServerError)
		case "/index.php/vod/search/page/1/wd/示例.html":
			// First pattern returns empty 200 (like Wogg); second pattern returns results.
			_, _ = w.Write([]byte(`<html><body><div class="empty">无结果</div></body></html>`))
		case "/vodsearch/示例----------1---.html":
			_, _ = w.Write([]byte(`<html><body>
				<div class="module-search-item">
					<div class="module-item-pic">
						<a href="/index.php/vod/play/id/139/sid/1/nid/1.html" title="立刻播放示例剧"><i class="icon-play"></i></a>
					</div>
					<div class="video-info-header">
						<a class="video-serial" href="/index.php/vod/detail/id/139.html" title="示例剧">4K</a>
						<h3><a href="/index.php/vod/detail/id/139.html" title="示例剧">示例剧</a></h3>
					</div>
				</div>
			</body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ws, err := OpenWebSiteContent(config.Site{
		Kind: config.KindPanWeb,
		Ext:  srv.URL + "/token.json$$$" + srv.URL + "$$$null$$$1",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := ws.Search(context.Background(), "示例", 1)
	if err != nil {
		t.Fatal(err)
	}
	if homeHits != 0 {
		t.Fatalf("OpenWebSiteContent unexpectedly fetched home %d times", homeHits)
	}
	if len(page.Videos) != 1 {
		t.Fatalf("expected 1 search result, got %+v", page.Videos)
	}
	if got := string(page.Videos[0].ID); !strings.HasSuffix(got, "/index.php/vod/detail/id/139.html") {
		t.Fatalf("expected detail URL, got %q (play URL shadowed detail)", got)
	}
	if page.Videos[0].Name != "示例剧" || page.Videos[0].Remarks != "4K" {
		t.Fatalf("unexpected video metadata: %+v", page.Videos[0])
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

func TestBaiduClientFullLifecycleAndFallback(t *testing.T) {
	var mu sync.Mutex
	var deletedPaths []string
	var failLocate bool
	var transferred bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/gettemplatevariable":
			if !strings.Contains(r.Header.Get("Cookie"), "BDUSS=valid_bduss") {
				_ = json.NewEncoder(w).Encode(map[string]any{"errno": -6})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errno":  0,
				"result": map[string]any{"bdstoken": "bds_tok_1"},
			})
		case "/share/verify":
			if err := r.ParseForm(); err != nil || r.FormValue("pwd") != "abcd" {
				_ = json.NewEncoder(w).Encode(map[string]any{"errno": -9})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "randsk": "randsk_ok"})
		case "/share/list":
			if !strings.Contains(r.Header.Get("Cookie"), "BDCLND=randsk_ok") {
				_ = json.NewEncoder(w).Encode(map[string]any{"errno": -9})
				return
			}
			if r.URL.Query().Get("root") == "1" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"errno":    0,
					"share_id": 10001,
					"uk":       20002,
					"list": []map[string]any{
						{"fs_id": 3001, "server_filename": "合集", "path": "/合集", "isdir": 1},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errno": 0,
				"list": []map[string]any{
					{"fs_id": 3010, "server_filename": "示例剧.E10.2160p.mkv", "path": "/合集/示例剧.E10.2160p.mkv", "size": 5000000, "isdir": 0},
					{"fs_id": 3002, "server_filename": "示例剧.E02.2160p.mkv", "path": "/合集/示例剧.E02.2160p.mkv", "size": 4000000, "isdir": 0},
				},
			})
		case "/api/create":
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0})
		case "/share/transfer":
			if r.URL.Query().Get("shareid") != "10001" || r.URL.Query().Get("from") != "20002" {
				_ = json.NewEncoder(w).Encode(map[string]any{"errno": -1})
				return
			}
			mu.Lock()
			transferred = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errno": 0,
				"extra": map[string]any{
					"list": []map[string]any{
						{"from": "/合集/示例剧.E02.2160p.mkv", "to": "/小幕临时播放/示例剧.E02.2160p.mkv", "to_fs_id": 9002},
					},
				},
			})
		case "/rest/2.0/pcs/file":
			mu.Lock()
			fail := failLocate
			mu.Unlock()
			if fail {
				_ = json.NewEncoder(w).Encode(map[string]any{"error_code": 31023, "error_msg": "blocked"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"urls": []map[string]any{
					{"url": "https://cdn.example.com/baidu-original.mkv"},
				},
			})
		case "/api/filemetas":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errno": 0,
				"info": []map[string]any{
					{"filename": "示例剧.E02.2160p.mkv", "dlink": "https://dlink.example.com/file/9002", "size": 4000000},
				},
			})
		case "/api/list":
			mu.Lock()
			hasFile := transferred
			mu.Unlock()
			list := []map[string]any{}
			if hasFile {
				list = append(list, map[string]any{"fs_id": 9002, "server_filename": "示例剧.E02.2160p.mkv", "path": "/小幕临时播放/示例剧.E02.2160p.mkv", "size": 4000000, "isdir": 0})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errno": 0,
				"list":  list,
			})
		case "/api/filemanager":
			_ = r.ParseForm()
			var list []string
			_ = json.Unmarshal([]byte(r.FormValue("filelist")), &list)
			mu.Lock()
			deletedPaths = append(deletedPaths, list...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Unauthenticated ResolvePlayback must trigger AuthRequiredError for ProviderBaidu.
	emptyCreds := Credentials{}
	unauth := NewBaiduClient(BaiduClientOptions{WebBase: srv.URL, PCSBase: srv.URL, HTTPClient: srv.Client(), Credentials: &emptyCreds})
	if _, err := unauth.ResolvePlayback(context.Background(), FileItem{Provider: ProviderBaidu, ShareID: "1bd123", FileID: "3002"}); err == nil {
		t.Fatal("expected AuthRequiredError for unauthenticated baidu playback")
	} else if p, ok := AuthRequiredProvider(err); !ok || p != ProviderBaidu {
		t.Fatalf("expected AuthRequiredError(baidu), got %v", err)
	}

	// Authenticated full lifecycle: CheckAuth -> ExpandShare -> ResolvePlayback (locatedownload) -> fallback (filemetas dlink) -> CleanupTemp.
	creds := Credentials{BaiduCookie: "BDUSS=valid_bduss; STOKEN=valid_stoken"}
	client := NewBaiduClient(BaiduClientOptions{WebBase: srv.URL, PCSBase: srv.URL, HTTPClient: srv.Client(), Credentials: &creds})
	ctx := context.Background()

	if auth, err := client.CheckAuth(ctx); err != nil || !auth.Valid {
		t.Fatalf("CheckAuth = %+v err=%v", auth, err)
	}
	ref, ok := ParseShareURL("https://pan.baidu.com/share/init?surl=bd123&pwd=abcd")
	if !ok {
		t.Fatal("ParseShareURL failed for share/init")
	}
	files, err := client.ExpandShare(ctx, ref)
	if err != nil || len(files) != 2 || files[0].Name != "示例剧.E02.2160p.mkv" || files[0].ShareFidToken != "10001:20002" {
		t.Fatalf("ExpandShare = %+v err=%v", files, err)
	}
	play, err := client.ResolvePlayback(ctx, files[0])
	if err != nil || play.URL != "https://cdn.example.com/baidu-original.mkv" || len(play.Streams) != 1 || !play.Streams[0].IsOriginal {
		t.Fatalf("ResolvePlayback locatedownload = %+v err=%v", play, err)
	}

	// Verify filemetas dlink fallback when locatedownload fails.
	mu.Lock()
	failLocate = true
	mu.Unlock()
	play2, err := client.ResolvePlayback(ctx, files[0])
	if err != nil || play2.URL != "https://dlink.example.com/file/9002" || play2.Headers["User-Agent"] != baiduDlinkUA {
		t.Fatalf("ResolvePlayback filemetas fallback = %+v err=%v", play2, err)
	}

	cleaned, err := client.CleanupTemp(ctx)
	if err != nil || cleaned != 1 || len(deletedPaths) != 1 || deletedPaths[0] != "/小幕临时播放/示例剧.E02.2160p.mkv" {
		t.Fatalf("CleanupTemp cleaned=%d deleted=%v err=%v", cleaned, deletedPaths, err)
	}
}

func TestBaiduExpiredAndPHPArrayErrorResponses(t *testing.T) {
	mode := "array_expired"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/gettemplatevariable":
			switch mode {
			case "array_expired":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"errno":-6,"result":[],"request_id":1}`))
			case "html_redirect":
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(`<!doctype html><html><body>login</body></html>`))
			default:
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"errno":0,"result":{"bdstoken":"tok-ok"}}`))
			}
		case "/api/create":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errno":0}`))
		case "/api/list":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errno":0,"list":[]}`))
		case "/share/transfer":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errno":-31,"extra":[],"show_msg":"capacity full"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	creds := Credentials{BaiduCookie: "BDUSS=expired; STOKEN=expired"}
	client := NewBaiduClient(BaiduClientOptions{WebBase: srv.URL, PCSBase: srv.URL, HTTPClient: srv.Client(), Credentials: &creds})
	ctx := context.Background()

	if _, err := client.CheckAuth(ctx); err == nil {
		t.Fatal("expected AuthRequiredError on errno -6 with array result")
	} else if p, ok := AuthRequiredProvider(err); !ok || p != ProviderBaidu {
		t.Fatalf("expected AuthRequiredError(baidu) on errno -6 array result, got %v", err)
	}

	mode = "html_redirect"
	if _, err := client.CheckAuth(ctx); err == nil {
		t.Fatal("expected AuthRequiredError on HTML login page")
	} else if p, ok := AuthRequiredProvider(err); !ok || p != ProviderBaidu {
		t.Fatalf("expected AuthRequiredError(baidu) on HTML response, got %v", err)
	}

	mode = "ok"
	_, err := client.ResolvePlayback(ctx, FileItem{
		Provider:      ProviderBaidu,
		ShareID:       "1bd123",
		FileID:        "3002",
		ShareFidToken: "10001:20002",
		Name:          "demo.mkv",
	})
	if err == nil || !strings.Contains(err.Error(), "剩余空间不足") {
		t.Fatalf("expected quota error when transfer returns extra:[], got %v", err)
	}
}

func TestQuarkCleanupDoesNotPurgeRenamedCachedFolder(t *testing.T) {
	var mu sync.Mutex
	folderName := "小幕临时播放"
	deleteCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/sort":
			mu.Lock()
			name := folderName
			mu.Unlock()
			if r.URL.Query().Get("pdir_fid") == "0" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": 200,
					"code":   0,
					"data": map[string]any{
						"list": []map[string]any{
							{"fid": "dir-1", "file_name": name, "dir": true, "file_type": 0},
						},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "precious-file", "file_name": "important.mkv", "dir": false},
					},
				},
			})
		case "/file/delete":
			mu.Lock()
			deleteCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "code": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	creds := Credentials{QuarkCookie: "k=v"}
	client := NewQuarkClient(ClientOptions{
		Provider:    ProviderQuark,
		BaseURL:     srv.URL,
		HTTPClient:  srv.Client(),
		Credentials: &creds,
	})

	ctx := context.Background()
	// Populate tempDirFid cache for "dir-1".
	fid, _, err := client.ensureTempDir(ctx)
	if err != nil || fid != "dir-1" {
		t.Fatalf("ensureTempDir = %q, err=%v", fid, err)
	}

	// User renames "dir-1" to "重要资料": CleanupTemp MUST re-check root and delete nothing.
	mu.Lock()
	folderName = "重要资料"
	mu.Unlock()
	cleaned, err := client.CleanupTemp(ctx)
	if err != nil || cleaned != 0 {
		t.Fatalf("CleanupTemp on renamed folder = %d, err=%v", cleaned, err)
	}
	mu.Lock()
	gotDeletes := deleteCalls
	mu.Unlock()
	if gotDeletes != 0 {
		t.Fatalf("CleanupTemp issued %d delete calls against renamed cached folder", gotDeletes)
	}
}

func TestIsNativePlayableFilenameFor(t *testing.T) {
	cases := []struct {
		name, goos, goarch string
		want               bool
	}{
		{"movie.mp4", "linux", "amd64", true},
		{"movie.mkv", "darwin", "arm64", true},
		{"movie.mkv", "windows", "amd64", true},
		{"movie.mkv", "windows", "arm64", true},
		{"movie.flv", "windows", "arm64", true},
		{"movie.mkv", "windows", "386", false},
		{"movie.mkv", "linux", "amd64", false},
		{"disc.iso", "windows", "arm64", false},
	}
	for _, tc := range cases {
		if got := isNativePlayableFilenameFor(tc.name, tc.goos, tc.goarch); got != tc.want {
			t.Errorf("isNativePlayableFilenameFor(%q, %q, %q) = %v, want %v", tc.name, tc.goos, tc.goarch, got, tc.want)
		}
	}
}
