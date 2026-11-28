package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/mediaserver"
	"github.com/egoist/mygo/ui"
)

func TestMediaPosterRequestUsesHeaderNotURL(t *testing.T) {
	const token = "poster-test-token"
	var gotAuth, gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "image/svg+xml")
		io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="2" height="2"></svg>`)
	}))
	defer srv.Close()

	a := &app{}
	a.media.servers = []savedMediaServer{{
		ID:          "server-1",
		Name:        "示例库",
		BaseURL:     srv.URL,
		Product:     string(mediaserver.ProductJellyfin),
		UserID:      "user-1",
		Username:    "alice",
		AccessToken: token,
		DeviceID:    "device-1",
	}}
	a.syncMediaPosterAuth()
	t.Cleanup(resetMediaPosterAuth)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/Items/movie-1/Images/Primary?maxHeight=480&quality=80", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (posterRoundTripper{}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(gotAuth, `Token="`+token+`"`) {
		t.Fatalf("poster auth = %q", gotAuth)
	}
	if strings.Contains(gotURL, token) || strings.Contains(strings.ToLower(gotURL), "api_key") {
		t.Fatalf("token leaked into image url %s", gotURL)
	}
}

func TestMediaPosterAuthDoesNotCollide(t *testing.T) {
	a := &app{}
	a.media.servers = []savedMediaServer{
		{ID: "acct-a", BaseURL: "https://media.example.com:8096", Product: string(mediaserver.ProductJellyfin), UserID: "user-a", AccessToken: "token-acct-a", DeviceID: "dev-a"},
		{ID: "acct-b", BaseURL: "https://media.example.com:8096", Product: string(mediaserver.ProductJellyfin), UserID: "user-b", AccessToken: "token-acct-b", DeviceID: "dev-b"},
		{ID: "emby-path", BaseURL: "https://media.example.com:8096/emby", Product: string(mediaserver.ProductEmby), UserID: "user-e", AccessToken: "token-emby-path", DeviceID: "dev-e"},
		{ID: "jf-path", BaseURL: "https://media.example.com:8096/jellyfin", Product: string(mediaserver.ProductJellyfin), UserID: "user-j", AccessToken: "token-jf-path", DeviceID: "dev-j"},
		{ID: "other-port", BaseURL: "https://media.example.com:8920", Product: string(mediaserver.ProductJellyfin), UserID: "user-p", AccessToken: "token-other-port", DeviceID: "dev-p"},
	}
	a.syncMediaPosterAuth()
	t.Cleanup(resetMediaPosterAuth)

	assertPosterToken(t, "https://media.example.com:8096/Items/1/Images/Primary?xiaomu_server=acct-a", "token-acct-a")
	assertPosterToken(t, "https://media.example.com:8096/Items/1/Images/Primary?xiaomu_server=acct-b", "token-acct-b")
	assertPosterToken(t, "https://media.example.com:8096/emby/Items/1/Images/Primary", "token-emby-path")
	assertPosterToken(t, "https://media.example.com:8096/jellyfin/Items/1/Images/Primary?maxHeight=480", "token-jf-path")
	assertPosterToken(t, "https://media.example.com:8920/Items/1/Images/Primary", "token-other-port")
	if headers := posterHeadersFor(t, "https://media.example.com:8096/Items/1/Images/Primary"); len(headers) != 0 {
		t.Fatalf("two accounts on one base must not share a guessed token: %#v", headers)
	}
	if headers := posterHeadersFor(t, "https://other.example.com/Items/1/Images/Primary?xiaomu_server=acct-a"); len(headers) != 0 {
		t.Fatalf("server id on another host leaked auth: %#v", headers)
	}
	if headers := posterHeadersFor(t, "http://media.example.com:8096/Items/1/Images/Primary?xiaomu_server=acct-a"); len(headers) != 0 {
		t.Fatalf("http request used the https server token: %#v", headers)
	}
	if headers := posterHeadersFor(t, "https://media.example.com:8096/emby/Items/1/Images/Primary?xiaomu_server=acct-a"); len(headers) != 0 {
		t.Fatalf("root account token was sent to the /emby server: %#v", headers)
	}

	var gotAuth, gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotURL = r.URL.RequestURI()
		w.Header().Set("Content-Type", "image/svg+xml")
		io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="2" height="2"></svg>`)
	}))
	defer srv.Close()
	b := &app{}
	b.media.servers = []savedMediaServer{{
		ID: "local-1", BaseURL: srv.URL, Product: string(mediaserver.ProductJellyfin),
		UserID: "user-1", AccessToken: "token-local-1", DeviceID: "dev-1",
	}, {
		ID: "local-2", BaseURL: srv.URL, Product: string(mediaserver.ProductJellyfin),
		UserID: "user-2", AccessToken: "token-local-2", DeviceID: "dev-2",
	}}
	b.syncMediaPosterAuth()
	raw := stampMediaPosterURL(srv.URL+"/Items/movie-1/Images/Primary?maxHeight=480&quality=80", "local-2")
	if strings.Contains(raw, "token-local-2") {
		t.Fatalf("stamp put the token in the url: %s", raw)
	}
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (posterRoundTripper{}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(gotAuth, `Token="token-local-2"`) || strings.Contains(gotAuth, "token-local-1") {
		t.Fatalf("upstream auth = %q", gotAuth)
	}
	if strings.Contains(gotURL, "xiaomu_server") || strings.Contains(gotURL, "token-local") {
		t.Fatalf("selector or token leaked upstream: %s", gotURL)
	}
}

func posterHeadersFor(t *testing.T, raw string) map[string]string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return mediaPosterHeaders(u)
}

func assertPosterToken(t *testing.T, raw, token string) {
	t.Helper()
	headers := posterHeadersFor(t, raw)
	blob := headers["Authorization"] + "\n" + headers["X-Emby-Token"] + "\n" + headers["X-Emby-Authorization"]
	if !strings.Contains(blob, token) {
		t.Fatalf("url %s auth = %#v, want %s", raw, headers, token)
	}
	for _, other := range []string{"token-acct-a", "token-acct-b", "token-emby-path", "token-jf-path", "token-other-port"} {
		if other != token && strings.Contains(blob, other) {
			t.Fatalf("url %s also carried %s: %#v", raw, other, headers)
		}
	}
}

func resetMediaPosterAuth() {
	mediaPosterMu.Lock()
	mediaPosterIdx = mediaPosterIndex{}
	mediaPosterMu.Unlock()
}

func TestMediaServerConnectBrowseAndDirectPlay(t *testing.T) {
	const (
		token    = "ui-test-token"
		password = "fixture-password"
	)
	var opened []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, password) || strings.Contains(r.URL.RawQuery, token) {
			http.Error(w, "secret in url", http.StatusBadRequest)
			return
		}
		switch {
		case r.URL.Path == "/System/Info/Public":
			io.WriteString(w, `{"serverName":"示例库","version":"10.10.0","productName":"Jellyfin Server","id":"server-1"}`)
		case r.URL.Path == "/Users/AuthenticateByName":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["Username"] != "alice" || body["Pw"] != password || !strings.HasPrefix(r.Header.Get("Authorization"), "MediaBrowser ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"accessToken":"`+token+`","serverId":"server-1","user":{"name":"alice","id":"user-1"}}`)
		case r.URL.Path == "/Users/user-1/Views":
			if !strings.Contains(r.Header.Get("Authorization"), token) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"items":[
				{"name":"故事片","id":"lib-1","collectionType":"movies","type":"CollectionFolder"},
				{"name":"直播","id":"lib-live","collectionType":"livetv","type":"CollectionFolder"}
			],"totalRecordCount":2}`)
		case r.URL.Path == "/Users/user-1/Items" && r.URL.Query().Get("ParentId") == "lib-1":
			io.WriteString(w, `{"items":[{"name":"海边示例","id":"movie-1","type":"Movie","productionYear":2024}],"totalRecordCount":1}`)
		case r.URL.Path == "/Users/user-1/Items/movie-1":
			io.WriteString(w, `{"name":"海边示例","id":"movie-1","type":"Movie","mediaSources":[{"id":"src-1","protocol":"File","container":"mkv","supportsDirectPlay":true,"path":"/media/private/movie.mkv"}]}`)
		case r.URL.Path == "/Sessions/Logout":
			w.WriteHeader(http.StatusNoContent)
		default:
			if strings.Contains(r.URL.Path, "/Images/") {
				w.Header().Set("Content-Type", "image/svg+xml")
				io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="2" height="2"></svg>`)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	a := &app{nav: []routeKind{routeSources}, persist: true}
	defer a.close()
	a.media.path = filepath.Join(dir, "media_servers.json")
	a.history.path = filepath.Join(dir, "history.json")
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		opened = append(opened, raw)
		if a.proxy == nil || !strings.Contains(a.proxy.Header("Authorization"), token) {
			t.Errorf("proxy auth = %q", a.proxy.Header("Authorization"))
		}
		return nil, nil
	}

	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()
	if err := tt.Click("媒体库"); err != nil {
		t.Fatalf("click 媒体库: %v texts=%v", err, tt.Texts())
	}
	if err := tt.Click("添加服务器"); err != nil {
		t.Fatalf("click 添加服务器: %v texts=%v", err, tt.Texts())
	}
	if err := tt.Click("服务器地址"); err != nil {
		t.Fatal(err)
	}
	tt.Type(srv.URL)
	if err := tt.Click("用户名"); err != nil {
		t.Fatal(err)
	}
	tt.Type("alice")
	if err := tt.Click("密码"); err != nil {
		t.Fatal(err)
	}
	tt.Type(password)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if a.media.addErr != "" || len(a.media.servers) != 1 {
		t.Fatalf("connect err=%q servers=%d status=%s", a.media.addErr, len(a.media.servers), a.status)
	}
	if !tt.HasText("示例库") || !tt.HasText("Jellyfin") || !tt.HasText("alice") {
		t.Fatalf("server row missing: %v", tt.Texts())
	}
	data, err := os.ReadFile(a.media.path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(a.media.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("credential file mode = %o, want 0600", info.Mode().Perm())
		}
	}
	if strings.Contains(string(data), password) || !strings.Contains(string(data), token) {
		t.Fatalf("saved file leaked the password or dropped the token: %s", data)
	}
	if strings.Contains(a.media.addPass, password) {
		t.Fatal("password stayed in the form")
	}

	if err := tt.Click("进入 示例库"); err != nil {
		t.Fatalf("click 进入: %v texts=%v", err, tt.Texts())
	}
	if a.route() != routeMedia || !tt.HasText("故事片") {
		t.Fatalf("libraries route=%v texts=%v err=%s", a.route(), tt.Texts(), a.media.err)
	}
	for _, it := range a.media.items {
		if it.CollectionType == "livetv" {
			t.Fatalf("live TV library was shown: %+v", a.media.items)
		}
	}
	lib, ok := tt.Find("故事片")
	if !ok {
		t.Fatal("library card missing")
	}
	tt.ClickAt(lib.X+lib.W/2, lib.Y+lib.H/2)
	movie, ok := tt.Find("海边示例")
	if !ok {
		t.Fatalf("movie missing: %v err=%s", tt.Texts(), a.media.err)
	}
	tt.ClickAt(movie.X+movie.W/2, movie.Y+movie.H/2)
	if len(opened) != 1 || a.status != "正在播放" || !a.playingMedia || a.playingMediaItem != "movie-1" {
		t.Fatalf("play opened=%d status=%s media=%v item=%s err=%s", len(opened), a.status, a.playingMedia, a.playingMediaItem, a.media.err)
	}
	target := playedURL(t, opened[0])
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(u.Path, "/Videos/movie-1/stream.mkv") || u.Query().Get("static") != "true" || u.Query().Get("MediaSourceId") != "src-1" {
		t.Fatalf("direct play url = %s", target)
	}
	if strings.Contains(target, token) || strings.Contains(target, password) || strings.Contains(target, "/media/private") {
		t.Fatalf("direct play url leaked a secret or filesystem path: %s", target)
	}

	a.player = &unknownSizeMediaPlayer{pos: 15 * time.Second, dur: time.Minute}
	a.recordHistory(a.player, time.Now())
	a.flushHistory()
	if len(a.history.entries) != 1 || a.history.entries[0].Kind != historyKindMedia || a.history.entries[0].VideoID != "movie-1" {
		t.Fatalf("history = %+v", a.history.entries)
	}
	hist, err := os.ReadFile(a.history.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hist), token) || strings.Contains(string(hist), password) {
		t.Fatalf("history leaked a secret: %s", hist)
	}
	a.leavePlayer()
	a.resume(a.history.entries[0])
	if len(opened) != 2 || a.resumeAt != 15*time.Second || a.playingMediaItem != "movie-1" {
		t.Fatalf("resume opened=%d at=%s item=%s status=%s", len(opened), a.resumeAt, a.playingMediaItem, a.status)
	}

	a.leavePlayer()
	tt.Frame()
	if err := tt.Click("返回"); err != nil {
		t.Fatalf("back to libraries: %v texts=%v", err, tt.Texts())
	}
	if !tt.HasText("故事片") || tt.HasText("海边示例") {
		t.Fatalf("did not return to libraries: %v", tt.Texts())
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatalf("back to sources: %v", err)
	}
	if a.route() != routeSources {
		t.Fatalf("route = %v", a.route())
	}
	id := a.media.servers[0].ID
	a.removeMediaServer(id)
	if a.historyAvailable(a.history.entries[0]) {
		t.Fatal("removed server still looks available")
	}
}

func playedURL(t *testing.T, opened string) string {
	t.Helper()
	u, err := url.Parse(opened)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := url.QueryUnescape(u.Query().Get("url"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
