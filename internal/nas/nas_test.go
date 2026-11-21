package nas

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"reflect"
	"sync"
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
