package mediaserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	got, err := NormalizeBaseURL("https://emby.example.com:8096/emby/")
	if err != nil || got != "https://emby.example.com:8096/emby" {
		t.Fatalf("normalized = %q, %v", got, err)
	}
	if _, err := NormalizeBaseURL("emby.example.com:8096"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"",
		"ftp://emby.example.com",
		"https://user:secret@emby.example.com",
		"https://emby.example.com/library?token=secret",
		"https://emby.example.com/#frag",
	} {
		if _, err := NormalizeBaseURL(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestClassifyProduct(t *testing.T) {
	if ClassifyProduct("Jellyfin Server") != ProductJellyfin {
		t.Fatal("jellyfin")
	}
	if ClassifyProduct("Emby Server") != ProductEmby {
		t.Fatal("emby")
	}
	if ClassifyProduct("") != "" {
		t.Fatal("unknown")
	}
}

func TestConnectJellyfinCamelCaseAndStaticPlay(t *testing.T) {
	const token = "jellyfin-test-token"
	const password = "fixture-password"
	var sawPasswordInURL bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, password) || strings.Contains(r.URL.RawQuery, token) {
			sawPasswordInURL = true
		}
		switch {
		case r.URL.Path == "/System/Info/Public":
			io.WriteString(w, `{"serverName":"示例库","version":"10.10.0","productName":"Jellyfin Server","id":"server-1"}`)
		case r.URL.Path == "/Users/AuthenticateByName" && r.Method == http.MethodPost:
			if !strings.HasPrefix(r.Header.Get("Authorization"), "MediaBrowser ") {
				t.Errorf("jellyfin auth = %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("X-Emby-Token") != "" {
				t.Errorf("jellyfin login should not depend on X-Emby-Token: %q", r.Header.Get("X-Emby-Token"))
			}
			body, _ := io.ReadAll(r.Body)
			var req map[string]string
			if json.Unmarshal(body, &req) != nil || req["Username"] != "alice" || req["Pw"] != password {
				t.Errorf("login body = %s", body)
			}
			io.WriteString(w, `{"accessToken":"`+token+`","serverId":"server-1","user":{"name":"alice","id":"user-1"}}`)
		case r.URL.Path == "/Users/user-1/Views":
			if !strings.Contains(r.Header.Get("Authorization"), `Token="`+token+`"`) {
				t.Errorf("views auth = %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("X-Emby-Token") != "" {
				t.Errorf("views used legacy token header")
			}
			io.WriteString(w, `{"items":[
				{"name":"电影","id":"lib-movies","collectionType":"movies","type":"CollectionFolder","imageTags":{"primary":"p1"}},
				{"name":"电视直播","id":"lib-live","collectionType":"livetv","type":"CollectionFolder"},
				{"name":"音乐","id":"lib-music","collectionType":"music","type":"CollectionFolder"}
			],"totalRecordCount":3}`)
		case r.URL.Path == "/Users/user-1/Items" && r.URL.Query().Get("ParentId") == "lib-movies":
			if r.URL.Query().Get("SortBy") != SortName || r.URL.Query().Get("static") != "" {
				t.Errorf("items query = %s", r.URL.RawQuery)
			}
			io.WriteString(w, `{"items":[
				{"name":"示例电影","id":"movie-1","type":"Movie","productionYear":2024,"imageTags":{"Primary":"img"}},
				{"name":"主题曲","id":"song-1","type":"Audio"}
			],"totalRecordCount":2}`)
		case r.URL.Path == "/Users/user-1/Items/movie-1":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{
				"name":"示例电影","id":"movie-1","type":"Movie",
				"mediaSources":[
					{"id":"transcode","protocol":"File","container":"mkv","supportsDirectPlay":false,"supportsDirectStream":false,"path":"/media/nope.mkv"},
					{"id":"direct-1","protocol":"File","container":"mkv","supportsDirectPlay":true,"supportsDirectStream":true,"path":"/media/示例电影.mkv"}
				]
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	sess, err := Connect(ctx, srv.URL, "alice", password, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Product != ProductJellyfin || sess.UserID != "user-1" || sess.AccessToken != token || sess.ServerName != "示例库" {
		t.Fatalf("session = %+v", sess)
	}
	libs, err := Libraries(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 1 || libs[0].ID != "lib-movies" || !libs[0].Folder || strings.Contains(libs[0].ImageURL, token) {
		t.Fatalf("libraries = %+v", libs)
	}
	page, err := Children(ctx, sess, "lib-movies", SortName, 0, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Name != "示例电影" || !page.Items[0].Playable || page.Items[0].Year != "2024" {
		t.Fatalf("page = %+v", page)
	}
	if page.Items[0].ImageURL == "" || strings.Contains(page.Items[0].ImageURL, "api_key") || strings.Contains(page.Items[0].ImageURL, token) {
		t.Fatalf("image url = %q", page.Items[0].ImageURL)
	}
	play, err := DirectPlay(ctx, sess, "movie-1")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(play.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(u.Path, "/Videos/movie-1/stream.mkv") || u.Query().Get("static") != "true" || u.Query().Get("MediaSourceId") != "direct-1" {
		t.Fatalf("play url = %s", play.URL)
	}
	if strings.Contains(play.URL, token) || strings.Contains(strings.ToLower(play.URL), "api_key") || strings.Contains(play.URL, password) {
		t.Fatalf("secret leaked into url %s", play.URL)
	}
	if !strings.Contains(play.Headers["Authorization"], `Token="`+token+`"`) || play.Headers["X-Emby-Token"] != "" {
		t.Fatalf("play headers = %#v", play.Headers)
	}
	if sawPasswordInURL {
		t.Fatal("password or token appeared in a query string")
	}
	if strings.Contains(play.URL, "/media/") {
		t.Fatalf("filesystem path leaked into %s", play.URL)
	}
}

func TestConnectEmbyPascalCase(t *testing.T) {
	const token = "emby-test-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			io.WriteString(w, `{"ServerName":"Emby 示例","Version":"4.8.8.0","ProductName":"Emby Server","Id":"emb-1"}`)
		case "/Users/AuthenticateByName":
			auth := r.Header.Get("X-Emby-Authorization")
			if !strings.HasPrefix(auth, "Emby ") || !strings.Contains(auth, `Client="Xiaomu"`) {
				t.Errorf("emby auth = %q", auth)
			}
			if r.Header.Get("X-Emby-Token") != "" {
				t.Errorf("login must not send a token yet")
			}
			io.WriteString(w, `{"AccessToken":"`+token+`","ServerId":"emb-1","User":{"Name":"bob","Id":"user-9"}}`)
		case "/Users/user-9/Items/ep-1":
			if r.Header.Get("X-Emby-Token") != token || !strings.HasPrefix(r.Header.Get("X-Emby-Authorization"), "Emby ") {
				t.Errorf("playback info headers token=%q auth=%q", r.Header.Get("X-Emby-Token"), r.Header.Get("X-Emby-Authorization"))
			}
			io.WriteString(w, `{
				"Name":"第1集","Id":"ep-1","Type":"Episode","SeriesName":"示例剧","IndexNumber":1,"ParentIndexNumber":1,
				"MediaSources":[{"Id":"src-ep","Protocol":"File","Container":"mp4","SupportsDirectPlay":true,"Path":"/videos/ep.mp4"}]
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sess, err := Connect(context.Background(), srv.URL, "bob", "fixture-password", "device-emby")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Product != ProductEmby || sess.Username != "bob" || sess.AccessToken != token {
		t.Fatalf("%+v", sess)
	}
	play, err := DirectPlay(context.Background(), sess, "ep-1")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(play.URL)
	if !strings.HasSuffix(u.Path, "/Videos/ep-1/stream.mp4") || u.Query().Get("static") != "true" || u.Query().Get("MediaSourceId") != "src-ep" || u.Query().Get("PlaySessionId") == "" {
		t.Fatalf("url %s", play.URL)
	}
	if strings.Contains(play.URL, token) {
		t.Fatalf("token in url %s", play.URL)
	}
	if play.Headers["X-Emby-Token"] != token || !strings.Contains(play.Headers["X-Emby-Authorization"], `UserId="user-9"`) {
		t.Fatalf("headers %#v", play.Headers)
	}
}

func TestDirectPlayRefusesTranscodeOnly(t *testing.T) {
	const token = "token-transcode"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/user-1/Items/need-transcode" {
			io.WriteString(w, `{
				"Name":"需要转码","Id":"need-transcode","Type":"Movie",
				"MediaSources":[{"Id":"only","Protocol":"File","Container":"mkv","SupportsDirectPlay":false,"SupportsDirectStream":false}]
			}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	sess := Session{BaseURL: srv.URL, Product: ProductJellyfin, UserID: "user-1", AccessToken: token, DeviceID: "device-1"}
	_, err := DirectPlay(context.Background(), sess, "need-transcode")
	if !strings.Contains(errString(err), "转码") {
		t.Fatalf("err = %v", err)
	}
}

func TestDirectPlayFallsBackWhenMediaSourcesMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Name":"示例电影","Id":"movie-2","Type":"Movie"}`)
	}))
	defer srv.Close()
	sess := Session{BaseURL: srv.URL, Product: ProductEmby, UserID: "user-1", AccessToken: "token-fallback", DeviceID: "dev"}
	play, err := DirectPlay(context.Background(), sess, "movie-2")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(play.URL)
	if u.Query().Get("static") != "true" || u.Query().Get("MediaSourceId") != "movie-2" || strings.Contains(play.URL, "token-fallback") {
		t.Fatalf("url %s", play.URL)
	}
}

func TestAuthHeadersStayOffOtherHosts(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Emby-Token") != "" || r.Header.Get("X-Emby-Authorization") != "" {
			leaked = true
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Info/Public" {
			http.Redirect(w, r, other.URL+"/System/Info/Public", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer origin.Close()

	c, err := New(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PublicInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("auth headers followed a redirect to another host")
	}
}

func TestLoginBodyStaysOffOtherHosts(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	c, err := New(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Login(context.Background(), ProductJellyfin, "device-1", "user", "secret"); err == nil {
		t.Fatal("login followed a cross-host redirect with its body")
	}
	if hits.Load() != 0 {
		t.Fatal("login body reached another host")
	}
}

func TestUpgradedBase(t *testing.T) {
	cases := []struct {
		base, final, want string
		ok                bool
	}{
		{"http://media.example.com:8096", "https://media.example.com:8920/System/Info/Public", "https://media.example.com:8920", true},
		{"http://media.example.com/emby", "https://media.example.com/emby/System/Info/Public", "https://media.example.com/emby", true},
		{"http://media.example.com:8096", "http://media.example.com:8096/System/Info/Public", "", false},
		{"http://media.example.com:8096", "https://other.example.com/System/Info/Public", "", false},
		{"https://media.example.com:8920", "https://media.example.com:8920/System/Info/Public", "", false},
		{"http://media.example.com:8096", "https://media.example.com:8920/somewhere/else", "", false},
	}
	for _, tc := range cases {
		got, ok := upgradedBase(tc.base, tc.final)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("upgradedBase(%q, %q) = %q, %v; want %q, %v", tc.base, tc.final, got, ok, tc.want, tc.ok)
		}
	}
}

func TestConnectImplicitSchemeKeepsPasswordOffOtherHosts(t *testing.T) {
	var otherPosts atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			otherPosts.Add(1)
		}
		io.WriteString(w, `{"ProductName":"Jellyfin Server","ServerName":"示例","Id":"srv-1"}`)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	// No scheme typed: the probe follows to the other host, but the base
	// must not move there, and the password must never be posted to it.
	bare := strings.TrimPrefix(origin.URL, "http://")
	if _, err := Connect(context.Background(), bare, "user", "secret", "device-1"); err == nil {
		t.Fatal("connect succeeded through a cross-host redirect chain")
	}
	if otherPosts.Load() != 0 {
		t.Fatal("login password was posted to another host")
	}
}

func TestBadLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Info/Public" {
			io.WriteString(w, `{"ProductName":"Jellyfin Server","ServerName":"示例"}`)
			return
		}
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := Connect(context.Background(), srv.URL, "alice", "wrong-password", "dev")
	if err == nil || !strings.Contains(err.Error(), "用户名或密码") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "wrong-password") {
		t.Fatal("password leaked into the error")
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
