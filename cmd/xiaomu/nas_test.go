package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/nas"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type fakeNASFileInfo struct {
	name  string
	size  int64
	isDir bool
}

func (f fakeNASFileInfo) Name() string       { return f.name }
func (f fakeNASFileInfo) Size() int64        { return f.size }
func (f fakeNASFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeNASFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeNASFileInfo) IsDir() bool        { return f.isDir }
func (f fakeNASFileInfo) Sys() any           { return nil }

type fakeNASFile struct {
	data []byte
	off  int64
}

func (f *fakeNASFile) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeNASFile) Seek(ctx context.Context, off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.off = off
	case io.SeekCurrent:
		f.off += off
	case io.SeekEnd:
		f.off = int64(len(f.data)) + off
	}
	return f.off, nil
}

func (f *fakeNASFile) Stat(ctx context.Context) (os.FileInfo, error) {
	return fakeNASFileInfo{name: "file", size: int64(len(f.data))}, nil
}

func (f *fakeNASFile) Close(ctx context.Context) error { return nil }

type fakeNASShare struct {
	dirs  map[string][]os.FileInfo
	files map[string][]byte
}

func (s *fakeNASShare) ReadDir(ctx context.Context, dir string) ([]os.FileInfo, error) {
	entries, ok := s.dirs[dir]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return entries, nil
}

func (s *fakeNASShare) Open(ctx context.Context, name string) (nas.ShareFile, error) {
	data, ok := s.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return &fakeNASFile{data: data}, nil
}

func (s *fakeNASShare) Unmount(ctx context.Context) error { return nil }

type fakeNASSession struct{ share nas.Share }

func (s *fakeNASSession) Mount(ctx context.Context, share string) (nas.Share, error) {
	if share != "Movies" {
		return nil, fs.ErrNotExist
	}
	return s.share, nil
}

func (s *fakeNASSession) Close() error { return nil }

// fakeNASShareData is the fake NAS contents: a film folder, a playable
// file, an MKV the player cannot play and a text file that is never
// listed.
func fakeNASShareData() *fakeNASShare {
	return &fakeNASShare{
		dirs: map[string][]os.FileInfo{
			"": {
				fakeNASFileInfo{name: "电影", isDir: true},
				fakeNASFileInfo{name: "照片", isDir: true},
				fakeNASFileInfo{name: "示例.mp4", size: 12},
				fakeNASFileInfo{name: "示例.mkv", size: 20},
				fakeNASFileInfo{name: "纪录片.mov", size: 8},
				fakeNASFileInfo{name: "说明.txt", size: 10},
			},
			"电影": {
				fakeNASFileInfo{name: "正片.m4v", size: 6},
			},
		},
		files: map[string][]byte{
			"示例.mp4":    []byte("0123456789ab"),
			`电影\正片.m4v`: []byte("abcdef"),
		},
	}
}

// fakeNASDialWithShare dials a client over the given share.
func fakeNASDialWithShare(share nas.Share) func(context.Context, nas.Server) (*nas.Client, error) {
	return func(ctx context.Context, srv nas.Server) (*nas.Client, error) {
		if srv.Address != "192.168.1.10" || srv.Share != "Movies" {
			return nil, fmt.Errorf("找不到 NAS %s/%s", srv.Address, srv.Share)
		}
		client := nas.NewWithDial(srv, func(context.Context, nas.Server) (nas.Session, error) {
			return &fakeNASSession{share: share}, nil
		})
		if err := client.Dial(ctx); err != nil {
			return nil, err
		}
		return client, nil
	}
}

// fakeNASDial pretends 192.168.1.10/Movies is a NAS with a film folder, a
// playable file, an MKV the player cannot play and a text file that is
// never listed.
func fakeNASDial() func(context.Context, nas.Server) (*nas.Client, error) {
	return fakeNASDialWithShare(fakeNASShareData())
}

// cancelAwareNASShare fails an Open whose context is already cancelled,
// like a real share would when the request is gone.
type cancelAwareNASShare struct{ *fakeNASShare }

func (s *cancelAwareNASShare) Open(ctx context.Context, name string) (nas.ShareFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.fakeNASShare.Open(ctx, name)
}

func TestNASStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "nas.json")
	if _, ok, err := readSavedNAS(path); err != nil || ok {
		t.Fatalf("missing nas: ok=%v err=%v", ok, err)
	}
	want := nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest", Password: "secret"}
	if err := writeSavedNAS(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := readSavedNAS(path)
	if err != nil || !ok || got != want {
		t.Fatalf("round trip: %+v ok=%v err=%v", got, ok, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSavedNAS(path); err == nil {
		t.Fatal("a broken file was read as a saved NAS")
	}
}

// The NAS tab connects through the form, lists folders and files, plays
// a file through the proxy, records it in the history and resumes it.
func TestNASConnectBrowsePlayAndResume(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

	a := &app{}
	defer a.close()
	a.nas.dial = fakeNASDial()
	var played []string
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		resp, err := http.Get(raw)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		played = append(played, string(body))
		return ui.NewVideoPlayer(&stillVideo{})
	}
	tt := ui.NewTester(a.view, 1120, 760)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q: %v; status %s; texts %v", label, err, a.status, tt.Texts())
		}
	}
	click("内网盘")
	if !tt.HasText("添加内网盘") || !tt.HasText("只播 MP4 / MOV / M4V") {
		t.Fatalf("add form missing: %v", tt.Texts())
	}
	click("服务器地址")
	tt.Type("192.168.1.10")
	click("共享名")
	tt.Type("Movies")
	click("用户名")
	tt.Type("guest")
	click("密码")
	tt.Type("secret")
	click("连接并保存")
	if !a.nas.connected || len(a.nas.entries) != 5 {
		t.Fatalf("not connected: connected=%v entries=%v status=%s", a.nas.connected, a.nas.entries, a.status)
	}
	for _, want := range []string{"电影", "照片", "示例.mp4", "示例.mkv", "纪录片.mov", "不支持播放", "文件夹"} {
		if !tt.HasText(want) {
			t.Fatalf("listing misses %q: %v", want, tt.Texts())
		}
	}
	if tt.HasText("说明.txt") {
		t.Fatalf("a non-video file was listed: %v", tt.Texts())
	}
	// A file the player cannot play says so instead of playing.
	click("示例.mkv")
	if a.player != nil || a.status != "MKV 不支持播放，系统播放器只能播 MP4 / MOV / M4V" {
		t.Fatalf("unsupported file: player=%v status=%q", a.player != nil, a.status)
	}
	click("打开 电影")
	if len(a.nas.entries) != 1 || a.nas.entries[0].Name != "正片.m4v" || tt.HasText("示例.mp4") {
		t.Fatalf("subdirectory: %+v", a.nas.entries)
	}
	click("上级目录")
	if a.nas.dir != "" || len(a.nas.entries) != 5 {
		t.Fatalf("back to root: dir=%q entries=%v", a.nas.dir, a.nas.entries)
	}
	click("播放 示例.mp4")
	if a.player == nil || a.playingNAS == nil || a.playingNAS.Path != "示例.mp4" ||
		a.playingSource != "内网盘" || a.playingEpisode != "正片" || a.status != "正在播放" {
		t.Fatalf("play: player=%v nas=%+v source=%q episode=%q status=%q",
			a.player != nil, a.playingNAS, a.playingSource, a.playingEpisode, a.status)
	}
	if len(played) != 1 || played[0] != "0123456789ab" {
		t.Fatalf("proxy did not serve the NAS file: %v", played)
	}
	// The play is recorded like any other title.
	now := time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)
	a.recordHistory(a.player, now)
	if len(a.history.entries) != 1 {
		t.Fatalf("NAS play was not recorded: %+v", a.history.entries)
	}
	e := a.history.entries[0]
	if e.Name != "示例.mp4" || e.SiteKey != "nas" || e.ConfigURL != "smb://192.168.1.10/Movies" ||
		e.VideoID != "示例.mp4" || e.Line != "192.168.1.10" || e.Episode != "正片" || e.Origin != "内网盘" {
		t.Fatalf("NAS history entry: %+v", e)
	}
	// Resume from the history reopens the same file at its position.
	a.history.entries[0].Position, a.history.entries[0].Duration = 30, 600
	click("返回")
	if a.player != nil {
		t.Fatal("player did not close")
	}
	click("播放历史")
	if !tt.HasText("正片 · 看到 00:30 / 10:00") || !tt.HasText("内网盘") {
		t.Fatalf("history row: %v", tt.Texts())
	}
	click("继续播放 示例.mp4")
	if a.player == nil || a.playingNAS == nil || a.playingNAS.Path != "示例.mp4" {
		t.Fatalf("resume did not play: player=%v nas=%+v status=%s", a.player != nil, a.playingNAS, a.status)
	}
	if a.resumeAt != 30*time.Second && a.lastPosition != 30*time.Second {
		t.Fatalf("position not restored: resumeAt=%v last=%v", a.resumeAt, a.lastPosition)
	}
	if len(played) != 2 || played[1] != "0123456789ab" {
		t.Fatalf("resumed play did not fetch the file: %v", played)
	}
}

func TestNASResumeWhenServerChanged(t *testing.T) {
	a := &app{}
	defer a.close()
	a.nas.dial = fakeNASDial()
	a.nas.server = nas.Server{Address: "192.168.1.99", Share: "Movies"}
	a.nas.hasServer = true
	entry := historyEntry{
		Name: "示例.mp4", ConfigURL: "smb://192.168.1.10/Movies", SiteKey: "nas",
		Origin: "内网盘", VideoID: "示例.mp4", Line: "192.168.1.10",
		Episode: "正片", Position: 30, Duration: 600, Updated: time.Now(),
	}
	if a.historyAvailable(entry) {
		t.Fatal("an entry from another NAS reported available")
	}
	a.resume(entry)
	if a.player != nil || a.status != "这条记录的 NAS 已移除或更换，重新添加后可继续播放" {
		t.Fatalf("resumed from a replaced NAS: player=%v status=%q", a.player != nil, a.status)
	}
}

func TestNASDisconnectReconnectAndRemove(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

	a := &app{}
	defer a.close()
	a.nas.dial = fakeNASDial()
	tt := ui.NewTester(a.view, 1120, 760)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("click %q: %v; status %s; texts %v", label, err, a.status, tt.Texts())
		}
	}
	a.startNASConnection(nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"})
	if !a.nas.connected {
		t.Fatalf("connect: %s", a.status)
	}
	path, err := savedNASPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nas.json was not saved: %v", err)
	}
	click("内网盘")
	click("断开")
	if a.nas.connected || a.status != "已断开内网盘" {
		t.Fatalf("disconnect: connected=%v status=%q", a.nas.connected, a.status)
	}
	if !tt.HasText("重新连接") {
		t.Fatalf("reconnect missing: %v", tt.Texts())
	}
	click("重新连接")
	if !a.nas.connected || len(a.nas.entries) != 5 {
		t.Fatalf("reconnect: connected=%v entries=%v status=%s", a.nas.connected, a.nas.entries, a.status)
	}
	click("移除")
	if a.nas.hasServer || a.nas.connected {
		t.Fatal("remove kept the server")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("nas.json was not removed: %v", err)
	}
	if !tt.HasText("添加内网盘") {
		t.Fatalf("add form missing after remove: %v", tt.Texts())
	}
}

// openNASFile runs on the proxy's HTTP goroutines while the UI thread
// removes and reconnects the NAS. The snapshot in openNASFile and the
// client's own lock must keep this race-free and panic-free: this is the
// regression test for the crash found in review, where closing the client
// while a file was being opened nilled out the share under the opener.
func TestNASConcurrentOpenAndRemove(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

	a := &app{}
	defer a.close()
	a.nas.dial = fakeNASDial()
	srv := nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"}
	a.startNASConnection(srv)
	if !a.nas.connected {
		t.Fatalf("connect: %s", a.status)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					f, err := a.openNASFile(context.Background(), "192.168.1.10", "Movies", "示例.mp4")
					if err == nil {
						_ = f.Close()
					}
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		a.removeNAS()
		a.startNASConnection(srv)
	}
	close(stop)
	wg.Wait()
}

// Disconnecting while a NAS file is playing stops the player, and a proxy
// request after the disconnect is refused instead of silently dialling
// the NAS again.
func TestNASDisconnectStopsPlaybackAndRedial(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

	a := &app{}
	defer a.close()
	dials := 0
	base := fakeNASDial()
	a.nas.dial = func(ctx context.Context, srv nas.Server) (*nas.Client, error) {
		dials++
		return base(ctx, srv)
	}
	a.open = func(raw string) (*ui.VideoPlayer, error) {
		resp, err := http.Get(raw)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if _, err := io.ReadAll(resp.Body); err != nil {
			return nil, err
		}
		return ui.NewVideoPlayer(&stillVideo{})
	}
	srv := nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"}
	a.startNASConnection(srv)
	if !a.nas.connected {
		t.Fatalf("connect: %s", a.status)
	}
	if !a.playNASFile("192.168.1.10", "Movies", "示例.mp4", "示例.mp4") {
		t.Fatalf("play: %s", a.status)
	}
	if a.player == nil || a.playingNAS == nil {
		t.Fatal("NAS file did not start playing")
	}
	before := dials
	a.disconnectNAS()
	if a.player != nil || a.playingNAS != nil {
		t.Fatal("disconnect left the NAS file playing")
	}
	if _, err := a.openNASFile(context.Background(), "192.168.1.10", "Movies", "示例.mp4"); err == nil {
		t.Fatal("openNASFile after disconnect succeeded")
	}
	if dials != before {
		t.Fatalf("openNASFile redialled after disconnect: dials %d -> %d", before, dials)
	}
}

// flakyNASShare fails its first ReadDir calls, then behaves like the share
// it wraps: a NAS that drops its session and comes back.
type flakyNASShare struct {
	nas.Share
	failReads *int
}

func (s *flakyNASShare) ReadDir(ctx context.Context, dir string) ([]os.FileInfo, error) {
	if *s.failReads > 0 {
		*s.failReads--
		return nil, fmt.Errorf("会话已断开")
	}
	return s.Share.ReadDir(ctx, dir)
}

// A failed browse drops the client, so the next browse redials and
// succeeds instead of failing on the dead session forever.
func TestNASBrowseRecoversAfterFailure(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

	a := &app{}
	defer a.close()
	share := &fakeNASShare{
		dirs: map[string][]os.FileInfo{
			"": {
				fakeNASFileInfo{name: "电影", isDir: true},
				fakeNASFileInfo{name: "示例.mp4", size: 12},
			},
			"电影": {
				fakeNASFileInfo{name: "正片.m4v", size: 6},
			},
		},
		files: map[string][]byte{
			"示例.mp4":    []byte("0123456789ab"),
			`电影\正片.m4v`: []byte("abcdef"),
		},
	}
	failReads := 0
	dials := 0
	a.nas.dial = func(ctx context.Context, srv nas.Server) (*nas.Client, error) {
		dials++
		if srv.Address != "192.168.1.10" || srv.Share != "Movies" {
			return nil, fmt.Errorf("找不到 NAS %s/%s", srv.Address, srv.Share)
		}
		client := nas.NewWithDial(srv, func(context.Context, nas.Server) (nas.Session, error) {
			return &fakeNASSession{share: &flakyNASShare{Share: share, failReads: &failReads}}, nil
		})
		if err := client.Dial(ctx); err != nil {
			return nil, err
		}
		return client, nil
	}
	srv := nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"}
	a.startNASConnection(srv)
	if !a.nas.connected || dials != 1 {
		t.Fatalf("connect: connected=%v dials=%d status=%s", a.nas.connected, dials, a.status)
	}

	failReads = 1
	a.browseNAS("电影")
	if a.nas.dir != "" || dials != 1 {
		t.Fatalf("failed browse moved or redialled: dir=%q dials=%d status=%s", a.nas.dir, dials, a.status)
	}
	a.browseNAS("电影")
	if dials != 2 {
		t.Fatalf("second browse did not redial: dials=%d", dials)
	}
	if a.nas.dir != "电影" || len(a.nas.entries) != 1 || a.nas.entries[0].Name != "正片.m4v" {
		t.Fatalf("recovered browse: dir=%q entries=%v status=%s", a.nas.dir, a.nas.entries, a.status)
	}
}

// Resuming a NAS entry while disconnected reports "未连接" and does not
// silently dial the NAS: reconnecting is an explicit user action.
func TestNASResumeWhenDisconnected(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

	a := &app{}
	defer a.close()
	dials := 0
	base := fakeNASDial()
	a.nas.dial = func(ctx context.Context, srv nas.Server) (*nas.Client, error) {
		dials++
		return base(ctx, srv)
	}
	srv := nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"}
	a.startNASConnection(srv)
	if !a.nas.connected {
		t.Fatalf("connect: %s", a.status)
	}
	a.disconnectNAS()
	entry := historyEntry{
		Name: "示例.mp4", ConfigURL: "smb://192.168.1.10/Movies", SiteKey: "nas",
		Origin: "内网盘", VideoID: "示例.mp4", Line: "192.168.1.10",
		Episode: "正片", Position: 30, Duration: 600, Updated: time.Now(),
	}
	a.resume(entry)
	if a.player != nil || a.status != "内网盘未连接，重新连接后可继续播放" {
		t.Fatalf("resume while disconnected: player=%v status=%q", a.player != nil, a.status)
	}
	if dials != 1 {
		t.Fatalf("resume redialled while disconnected: dials=%d", dials)
	}
}

// Changing only the credentials must not keep reusing the old connection:
// the client is matched on the whole server, so a new password redials
// with it (and is only saved once the root listing succeeds). Identical
// credentials still reuse the connection.
func TestNASReconnectWithNewPasswordRedials(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

	a := &app{}
	defer a.close()
	dials := 0
	var lastDialed nas.Server
	base := fakeNASDial()
	a.nas.dial = func(ctx context.Context, srv nas.Server) (*nas.Client, error) {
		dials++
		lastDialed = srv
		return base(ctx, srv)
	}
	srv := nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest", Password: "old"}
	a.startNASConnection(srv)
	if !a.nas.connected || dials != 1 {
		t.Fatalf("connect: connected=%v dials=%d status=%s", a.nas.connected, dials, a.status)
	}
	// Same credentials: the connection is reused, no redial.
	a.startNASConnection(srv)
	if dials != 1 {
		t.Fatalf("identical reconnect redialled: dials=%d", dials)
	}
	// New password: must redial, and with the new password.
	srv.Password = "new"
	a.startNASConnection(srv)
	if dials != 2 {
		t.Fatalf("password change did not redial: dials=%d", dials)
	}
	if lastDialed.Password != "new" {
		t.Fatalf("redial used the old password: %q", lastDialed.Password)
	}
	if !a.nas.connected || a.nas.server.Password != "new" {
		t.Fatalf("reconnect with new password: connected=%v saved=%q status=%s",
			a.nas.connected, a.nas.server.Password, a.status)
	}
}

// A dial that never answers must not block disconnect: the old code held
// a.nas.mu across the dial, so disconnect waited on the UI thread until
// the job timed out.
func TestNASDisconnectWhileDialling(t *testing.T) {
	a := &app{}
	defer a.close()
	entered := make(chan struct{}, 1)
	a.nas.dial = func(ctx context.Context, srv nas.Server) (*nas.Client, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.startNASConnection(nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("connect never started")
	}
	start := time.Now()
	a.disconnectNAS()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("disconnect waited %v for the hung dial", took)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connect did not finish after disconnect")
	}
	a.nas.mu.Lock()
	connected, client := a.nas.connected, a.nas.client
	a.nas.mu.Unlock()
	if connected || client != nil {
		t.Fatalf("disconnect left the NAS connected: connected=%v client=%v", connected, client != nil)
	}
}

// Closing the window while a dial is hung must return at once too.
func TestNASCloseWhileDialling(t *testing.T) {
	a := &app{}
	entered := make(chan struct{}, 1)
	a.nas.dial = func(ctx context.Context, srv nas.Server) (*nas.Client, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.startNASConnection(nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("connect never started")
	}
	start := time.Now()
	a.close()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("close waited %v for the hung dial", took)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connect did not finish after close")
	}
}

// Cancelling one open request must not drop the client: other Range
// requests keep reading from the same session.
func TestNASCancelledOpenKeepsClient(t *testing.T) {
	a := &app{}
	defer a.close()
	a.nas.dial = fakeNASDialWithShare(&cancelAwareNASShare{fakeNASShare: fakeNASShareData()})
	a.startNASConnection(nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"})
	if !a.nas.connected {
		t.Fatalf("connect: %s", a.status)
	}
	a.nas.mu.Lock()
	client := a.nas.client
	a.nas.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.openNASFile(ctx, "192.168.1.10", "Movies", "示例.mp4"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open: %v, want context.Canceled", err)
	}
	a.nas.mu.Lock()
	kept := a.nas.client
	a.nas.mu.Unlock()
	if kept == nil || kept != client {
		t.Fatal("a cancelled open dropped the client")
	}
	f, err := a.openNASFile(context.Background(), "192.168.1.10", "Movies", "示例.mp4")
	if err != nil {
		t.Fatalf("open after a cancelled open: %v", err)
	}
	buf := make([]byte, 4)
	if n, err := f.ReadAt(buf, 0); err != nil || n != 4 || string(buf) != "0123" {
		t.Fatalf("read after a cancelled open: n=%d buf=%q err=%v", n, buf, err)
	}
	_ = f.Close()
}

// Removing the NAS while a reconnect is still in flight must stick: the
// old callback used to save the server back into nas.json afterwards.
func TestNASRemoveDuringReconnect(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

	a := &app{}
	defer a.close()
	entered := make(chan struct{}, 1)
	a.nas.dial = func(ctx context.Context, srv nas.Server) (*nas.Client, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		// Slow to answer, but cancellable: a real NAS still mounting.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		return fakeNASDial()(ctx, srv)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.startNASConnection(nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("connect never started")
	}
	a.removeNAS()
	path, err := savedNASPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("nas.json exists right after remove: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connect did not finish after remove")
	}
	// Give a late apply a chance to run: the removal must still hold.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reconnect recreated nas.json after remove: %v", err)
	}
	a.nas.mu.Lock()
	hasServer, connected := a.nas.hasServer, a.nas.connected
	a.nas.mu.Unlock()
	if hasServer || connected {
		t.Fatalf("reconnect resurrected the NAS: hasServer=%v connected=%v", hasServer, connected)
	}
	if a.status != "已移除内网盘" {
		t.Fatalf("status after remove during reconnect: %q", a.status)
	}
}

// Two folders' 01.mp4 are two history entries, not one overwriting the
// other.
func TestNASHistorySameNameDifferentDirs(t *testing.T) {
	a := &app{}
	defer a.close()
	share := &fakeNASShare{
		dirs: map[string][]os.FileInfo{
			"": {
				fakeNASFileInfo{name: "A", isDir: true},
				fakeNASFileInfo{name: "B", isDir: true},
			},
			"A": {fakeNASFileInfo{name: "01.mp4", size: 10}},
			"B": {fakeNASFileInfo{name: "01.mp4", size: 10}},
		},
		files: map[string][]byte{
			`A\01.mp4`: []byte("aaaaaaaaaa"),
			`B\01.mp4`: []byte("bbbbbbbbbb"),
		},
	}
	a.nas.dial = fakeNASDialWithShare(share)
	a.open = func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }
	a.startNASConnection(nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"})
	if !a.nas.connected {
		t.Fatalf("connect: %s", a.status)
	}
	now := time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)
	if !a.playNASFile("192.168.1.10", "Movies", `A\01.mp4`, "01.mp4") {
		t.Fatalf("play A/01.mp4: %s", a.status)
	}
	a.recordHistory(a.player, now)
	if !a.playNASFile("192.168.1.10", "Movies", `B\01.mp4`, "01.mp4") {
		t.Fatalf("play B/01.mp4: %s", a.status)
	}
	a.recordHistory(a.player, now.Add(2*time.Second))
	if len(a.history.entries) != 2 {
		t.Fatalf("same-name files: %d entries, want 2: %+v", len(a.history.entries), a.history.entries)
	}
	paths := map[string]bool{}
	for _, e := range a.history.entries {
		if e.Name != "01.mp4" || !e.isNAS() {
			t.Fatalf("same-name entry: %+v", e)
		}
		paths[e.VideoID] = true
	}
	if !paths[`A\01.mp4`] || !paths[`B\01.mp4`] {
		t.Fatalf("history kept the wrong files: %v", paths)
	}
}

// History written before Kind existed is still read right: a site whose
// key happens to be "nas" stays a site, an smb:// record stays NAS, and
// an explicit Kind wins over the address either way.
func TestHistoryNASKindMigration(t *testing.T) {
	site := historyEntry{
		SiteKey: "nas", ConfigURL: "https://example.com/config", Name: "示例片",
		VideoID: "7", Line: "线路", Episode: "第2集", Position: 30, Duration: 600,
	}
	if site.isNAS() {
		t.Fatal("a legacy site with key nas was read as NAS")
	}
	if site.dedupKey() != titleKey(site.Name) {
		t.Fatalf("legacy site dedup key: %q, want title key", site.dedupKey())
	}
	legacyNAS := historyEntry{
		SiteKey: "nas", ConfigURL: "smb://192.168.1.10/Movies", Name: "示例.mp4",
		VideoID: "示例.mp4", Line: "192.168.1.10", Episode: "正片", Position: 30, Duration: 600,
	}
	if !legacyNAS.isNAS() {
		t.Fatal("a legacy smb:// record was not read as NAS")
	}
	if legacyNAS.dedupKey() == titleKey(legacyNAS.Name) {
		t.Fatal("a legacy NAS record dedups by title")
	}
	if !(historyEntry{Kind: historyKindNAS, ConfigURL: "https://example.com/config"}).isNAS() {
		t.Fatal("Kind nas with a site address was not read as NAS")
	}
	if (historyEntry{Kind: historyKindSite, ConfigURL: "smb://192.168.1.10/Movies"}).isNAS() {
		t.Fatal("Kind site with an smb:// address was read as NAS")
	}

	// Resume must follow the same reading, not the key.
	a := &app{}
	defer a.close()
	a.resume(site)
	if a.status == "这条记录的 NAS 已移除或更换，重新添加后可继续播放" {
		t.Fatalf("a legacy site resumed down the NAS branch: status=%q", a.status)
	}
	if a.status != "这条记录的片源已移除，重新添加后可继续播放" {
		t.Fatalf("legacy site resume status: %q", a.status)
	}
	a.resume(legacyNAS)
	if a.status != "这条记录的 NAS 已移除或更换，重新添加后可继续播放" {
		t.Fatalf("legacy NAS resume status: %q player=%v", a.status, a.player != nil)
	}
}
