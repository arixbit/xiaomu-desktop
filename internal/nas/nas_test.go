package nas

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseSMBURL(t *testing.T) {
	tests := []struct {
		raw   string
		addr  string
		share string
		path  string
		ok    bool
	}{
		{"smb://192.168.1.10/Movies", "192.168.1.10", "Movies", "", true},
		{"smb://192.168.1.10:445/Movies/电影/示例.mp4", "192.168.1.10:445", "Movies", `电影\示例.mp4`, true},
		{"smb://192.168.1.10/Movies/电影/示例.mp4", "192.168.1.10", "Movies", `电影\示例.mp4`, true},
		{"smb://192.168.1.10/Movies/", "192.168.1.10", "Movies", "", true},
		{"smb://192.168.1.10/", "", "", "", false},
		{"smb://192.168.1.10", "", "", "", false},
		{"https://192.168.1.10/Movies", "", "", "", false},
		{"smb://", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, tt := range tests {
		srv, path, ok := ParseSMBURL(tt.raw)
		if ok != tt.ok || srv.Address != tt.addr || srv.Share != tt.share || path != tt.path {
			t.Errorf("ParseSMBURL(%q) = %+v %q %v, want %q/%q %q %v",
				tt.raw, srv, path, ok, tt.addr, tt.share, tt.path, tt.ok)
		}
	}
	if srv := (Server{Address: "192.168.1.10", Share: "Movies"}); srv.URL() != "smb://192.168.1.10/Movies" {
		t.Fatalf("URL() = %q", srv.URL())
	}
}

func TestPaths(t *testing.T) {
	if got := Join("", "电影"); got != "电影" {
		t.Fatalf("Join root: %q", got)
	}
	if got := Join(`电影`, "示例.mp4"); got != `电影\示例.mp4` {
		t.Fatalf("Join: %q", got)
	}
	if got := Join(`电影/`, "/示例.mp4"); got != `电影\示例.mp4` {
		t.Fatalf("Join slashes: %q", got)
	}
	if got := Parent(`电影\示例.mp4`); got != "电影" {
		t.Fatalf("Parent: %q", got)
	}
	if got := Parent("电影"); got != "" {
		t.Fatalf("Parent root: %q", got)
	}
	if got := Base(`电影\示例.mp4`); got != "示例.mp4" {
		t.Fatalf("Base: %q", got)
	}
	if got := Clean(`/电影//示例.mp4/`); got != `电影\示例.mp4` {
		t.Fatalf("Clean: %q", got)
	}
	if got := Clean(""); got != "" {
		t.Fatalf("Clean empty: %q", got)
	}
	if got := Clean(`..\电影\示例.mp4`); got != `电影\示例.mp4` {
		t.Fatalf("Clean leading ..: %q", got)
	}
	if got := Clean(`电影\..\示例.mp4`); got != `电影\示例.mp4` {
		t.Fatalf("Clean middle ..: %q", got)
	}
	if got := Clean(`电影\示例.mp4\..`); got != `电影\示例.mp4` {
		t.Fatalf("Clean trailing ..: %q", got)
	}
	if got := Clean(`..\..`); got != "" {
		t.Fatalf("Clean only ..: %q", got)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name  string
		class Class
	}{
		{"示例.mp4", ClassPlayable},
		{"示例.MP4", ClassPlayable},
		{"示例.mov", ClassPlayable},
		{"示例.m4v", ClassPlayable},
		{"示例.mkv", ClassUnsupported},
		{"示例.avi", ClassUnsupported},
		{"示例.rmvb", ClassUnsupported},
		{"示例.wmv", ClassUnsupported},
		{"示例.flv", ClassUnsupported},
		{"示例.ts", ClassUnsupported},
		{"示例.MKV", ClassUnsupported},
		{"说明.txt", ClassHidden},
		{"封面.jpg", ClassHidden},
		{"示例", ClassHidden},
		{".mp4", ClassPlayable},
	}
	for _, tt := range tests {
		if got := Classify(tt.name); got != tt.class {
			t.Errorf("Classify(%q) = %v, want %v", tt.name, got, tt.class)
		}
	}
	if ContentType("a.mp4") != "video/mp4" || ContentType("a.m4v") != "video/mp4" ||
		ContentType("a.mov") != "video/quicktime" || ContentType("a.mkv") != "" {
		t.Fatal("ContentType mismatch")
	}
}

func TestServerValidate(t *testing.T) {
	if err := (Server{Address: "192.168.1.10", Share: "Movies"}).Validate(); err != nil {
		t.Fatalf("valid server: %v", err)
	}
	if err := (Server{Share: "Movies"}).Validate(); err == nil {
		t.Fatal("missing address passed validation")
	}
	if err := (Server{Address: "192.168.1.10"}).Validate(); err == nil {
		t.Fatal("missing share passed validation")
	}
}

// fakeFileInfo is one directory entry of the fake share.
type fakeFileInfo struct {
	name  string
	size  int64
	isDir bool
}

func (f fakeFileInfo) Name() string      { return f.name }
func (f fakeFileInfo) Size() int64       { return f.size }
func (f fakeFileInfo) Mode() os.FileMode { return 0 }
func (f fakeFileInfo) ModTime() time.Time {
	return time.Time{}
}
func (f fakeFileInfo) IsDir() bool { return f.isDir }
func (f fakeFileInfo) Sys() any    { return nil }

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
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = off
	case io.SeekCurrent:
		pos = f.off + off
	case io.SeekEnd:
		pos = int64(len(f.data)) + off
	default:
		return 0, errors.New("bad whence")
	}
	if pos < 0 {
		return 0, errors.New("negative position")
	}
	f.off = pos
	return pos, nil
}

func (f *fakeNASFile) Stat(ctx context.Context) (os.FileInfo, error) {
	return fakeFileInfo{name: "示例.mp4", size: int64(len(f.data))}, nil
}

func (f *fakeNASFile) Close(ctx context.Context) error { return nil }

type fakeShare struct {
	dirs  map[string][]os.FileInfo
	files map[string][]byte
}

func (s *fakeShare) ReadDir(ctx context.Context, dir string) ([]os.FileInfo, error) {
	entries, ok := s.dirs[dir]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return entries, nil
}

func (s *fakeShare) Open(ctx context.Context, name string) (ShareFile, error) {
	data, ok := s.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return &fakeNASFile{data: data}, nil
}

func (s *fakeShare) Unmount(ctx context.Context) error { return nil }

type fakeSession struct{ share *fakeShare }

func (s *fakeSession) Mount(ctx context.Context, share string) (Share, error) {
	if share != "Movies" {
		return nil, fs.ErrNotExist
	}
	return s.share, nil
}

func (s *fakeSession) Close() error { return nil }

func fakeClient(t *testing.T) *Client {
	t.Helper()
	share := &fakeShare{
		dirs: map[string][]os.FileInfo{
			"": {
				fakeFileInfo{name: "说明.txt", size: 10},
				fakeFileInfo{name: "电影", isDir: true},
				fakeFileInfo{name: "示例.mkv", size: 20},
				fakeFileInfo{name: "示例.mp4", size: 12},
				fakeFileInfo{name: "照片", isDir: true},
				fakeFileInfo{name: "纪录片.mov", size: 8},
			},
			"电影": {
				fakeFileInfo{name: "正片.m4v", size: 6},
			},
		},
		files: map[string][]byte{
			"示例.mp4":    []byte("0123456789ab"),
			`电影\正片.m4v`: []byte("abcdef"),
		},
	}
	srv := Server{Address: "192.168.1.10", Share: "Movies", User: "guest"}
	return NewWithDial(srv, func(ctx context.Context, s Server) (Session, error) {
		if s.Address != srv.Address {
			return nil, errors.New("wrong server")
		}
		return &fakeSession{share: share}, nil
	})
}

func TestClientReadDirOrdersAndFilters(t *testing.T) {
	c := fakeClient(t)
	if err := c.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := c.ReadDir(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name)
	}
	// Directories first, then playable, then unsupported; the .txt is hidden.
	// Within a group the order is by name.
	want := []string{"照片", "电影", "示例.mp4", "纪录片.mov", "示例.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadDir = %v, want %v", got, want)
	}
	if !entries[0].IsDir || entries[2].Class != ClassPlayable || entries[4].Class != ClassUnsupported {
		t.Fatalf("entry kinds wrong: %+v", entries)
	}
	sub, err := c.ReadDir(context.Background(), "电影")
	if err != nil || len(sub) != 1 || sub[0].Name != "正片.m4v" {
		t.Fatalf("subdirectory: %v %v", sub, err)
	}
	if _, err := c.ReadDir(context.Background(), "没有这个目录"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
}

func TestClientOpenReadsAndSeeks(t *testing.T) {
	c := fakeClient(t)
	if err := c.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}
	f, err := c.Open(context.Background(), "示例.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Size() != 12 {
		t.Fatalf("size = %d", f.Size())
	}
	buf := make([]byte, 4)
	if n, err := f.ReadAt(buf, 8); err != nil || n != 4 || string(buf) != "89ab" {
		t.Fatalf("ReadAt = %d %q %v", n, buf, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	all := make([]byte, 12)
	if n, err := io.ReadFull(f, all); err != nil || n != 12 || string(all) != "0123456789ab" {
		t.Fatalf("Read = %d %q %v", n, all, err)
	}
	if pos, err := f.Seek(-2, io.SeekEnd); err != nil || pos != 10 {
		t.Fatalf("SeekEnd = %d %v", pos, err)
	}
	if _, err := c.Open(context.Background(), "没有这个文件.mp4"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestClientDialValidatesAndMounts(t *testing.T) {
	c := NewWithDial(Server{}, func(context.Context, Server) (Session, error) {
		return &fakeSession{}, nil
	})
	if err := c.Dial(context.Background()); err == nil {
		t.Fatal("a server without address or share dialed")
	}
	c = NewWithDial(Server{Address: "192.168.1.10", Share: "Wrong"}, func(context.Context, Server) (Session, error) {
		return &fakeSession{share: &fakeShare{}}, nil
	})
	if err := c.Dial(context.Background()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("wrong share: %v", err)
	}
	if _, err := c.ReadDir(context.Background(), ""); err == nil {
		t.Fatal("ReadDir before Dial succeeded")
	}
}

// Closing the client while other goroutines are opening files and listing
// directories must neither race nor panic: Close only takes the share out
// of the client, it never nils one an in-flight call already holds. This
// is the regression test for the crash found in review, where a close
// during Open nilled the share under the opener.
func TestClientCloseDuringOpen(t *testing.T) {
	c := fakeClient(t)
	if err := c.Dial(context.Background()); err != nil {
		t.Fatal(err)
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
					f, err := c.Open(context.Background(), "示例.mp4")
					if err == nil {
						_ = f.Close()
					}
					_, _ = c.ReadDir(context.Background(), "")
				}
			}
		}()
	}
	for i := 0; i < 100; i++ {
		_ = c.Close()
		if err := c.Dial(context.Background()); err != nil {
			t.Fatalf("redial: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

// blockingNASFile waits for release before answering a read, unless its
// context is cancelled first: a NAS that is slow to answer.
type blockingNASFile struct {
	data    []byte
	release chan struct{}
}

func (f *blockingNASFile) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-f.release:
	}
	if off < 0 || off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *blockingNASFile) Seek(ctx context.Context, off int64, whence int) (int64, error) {
	return off, nil
}

func (f *blockingNASFile) Stat(ctx context.Context) (os.FileInfo, error) {
	return fakeFileInfo{name: "慢.mp4", size: int64(len(f.data))}, nil
}

func (f *blockingNASFile) Close(ctx context.Context) error { return nil }

// blockingOpenShare serves a fresh blocking file per Open.
type blockingOpenShare struct {
	*fakeShare
	data    []byte
	release chan struct{}
}

func (s *blockingOpenShare) Open(ctx context.Context, name string) (ShareFile, error) {
	return &blockingNASFile{data: s.data, release: s.release}, nil
}

type blockingSession struct{ share Share }

func (s *blockingSession) Mount(ctx context.Context, share string) (Share, error) {
	return s.share, nil
}

func (s *blockingSession) Close() error { return nil }

type fileCloseProbe struct {
	fakeNASFile
	cancelStat context.CancelFunc
	called     bool
	ctxErr     error
	deadline   time.Time
}

func (f *fileCloseProbe) Stat(ctx context.Context) (os.FileInfo, error) {
	if f.cancelStat != nil {
		f.cancelStat()
		return nil, ctx.Err()
	}
	return f.fakeNASFile.Stat(ctx)
}

func (f *fileCloseProbe) Close(ctx context.Context) error {
	f.called = true
	f.ctxErr = ctx.Err()
	f.deadline, _ = ctx.Deadline()
	return nil
}

type fileCloseProbeShare struct {
	fakeShare
	file *fileCloseProbe
}

func (s *fileCloseProbeShare) Open(context.Context, string) (ShareFile, error) {
	return s.file, nil
}

func TestNASFileCleanupUsesIndependentDeadline(t *testing.T) {
	for _, name := range []string{"stat canceled", "request canceled after open"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			probe := &fileCloseProbe{}
			if name == "stat canceled" {
				probe.cancelStat = cancel
			}
			share := &fileCloseProbeShare{file: probe}
			client := NewWithDial(Server{Address: "example.com", Share: "Movies"}, func(context.Context, Server) (Session, error) {
				return &blockingSession{share: share}, nil
			})
			if err := client.Dial(ctx); err != nil {
				t.Fatal(err)
			}
			defer client.Close()

			file, err := client.Open(ctx, "movie.mp4")
			if probe.cancelStat != nil {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Open after canceled Stat = %v, want context.Canceled", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if remaining := time.Until(probe.deadline); !probe.called || probe.ctxErr != nil || remaining <= 0 || remaining > 5*time.Second {
				t.Fatalf("Close called=%v context error=%v remaining=%v, want independent deadline", probe.called, probe.ctxErr, remaining)
			}
		})
	}
}

// A read that is still waiting on the NAS ends when its request is
// cancelled, instead of hanging until the NAS answers.
func TestFileReadCancelledByContext(t *testing.T) {
	release := make(chan struct{})
	share := &blockingOpenShare{
		fakeShare: &fakeShare{dirs: map[string][]os.FileInfo{}, files: map[string][]byte{}},
		data:      []byte("0123456789"),
		release:   release,
	}
	srv := Server{Address: "192.168.1.10", Share: "Movies", User: "guest"}
	c := NewWithDial(srv, func(ctx context.Context, s Server) (Session, error) {
		return &blockingSession{share: share}, nil
	})
	if err := c.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f, err := c.Open(ctx, "慢.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 4)
		_, err := f.ReadAt(buf, 0)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("read finished before cancel: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled read: %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled read did not return")
	}

	// A live request still reads fine once the NAS answers.
	close(release)
	f2, err := c.Open(context.Background(), "慢.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	buf := make([]byte, 4)
	n, err := f2.ReadAt(buf, 0)
	if err != nil || n != 4 || string(buf) != "0123" {
		t.Fatalf("live read: n=%d buf=%q err=%v", n, buf, err)
	}
}

// A dial that is cancelled while the NAS is not answering returns the
// cancellation and installs nothing; a later dial still connects.
func TestClientDialCancelledWhileDialling(t *testing.T) {
	entered := make(chan struct{}, 1)
	var calls atomic.Int32
	share := &fakeShare{
		dirs:  map[string][]os.FileInfo{"": {fakeFileInfo{name: "示例.mp4", size: 12}}},
		files: map[string][]byte{"示例.mp4": []byte("0123456789ab")},
	}
	srv := Server{Address: "192.168.1.10", Share: "Movies", User: "guest"}
	c := NewWithDial(srv, func(ctx context.Context, s Server) (Session, error) {
		if calls.Add(1) == 1 {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &fakeSession{share: share}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Dial(ctx) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("dial never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled dial: %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled dial did not return")
	}
	if err := c.Dial(context.Background()); err != nil {
		t.Fatalf("dial after cancel: %v", err)
	}
	if _, err := c.ReadDir(context.Background(), ""); err != nil {
		t.Fatalf("ReadDir after redial: %v", err)
	}
}
