// Package nas talks to one SMB file share (a NAS or a shared folder on the
// local network) and serves its video files to the player through the
// loopback proxy. It only reads: it never writes, deletes or renames
// anything on the share.
//
// Only MP4/MOV/M4V play: the system player cannot play MKV or the other
// formats listed as unsupported, so those are shown as such instead of
// being offered as playable.
package nas

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	smb2 "github.com/hirochachacha/go-smb2/v2"
	"github.com/hirochachacha/go-smb2/v2/auth"
)

// Server is one saved NAS: its address, share and login. Address is a host
// or host:port; the port defaults to 445.
type Server struct {
	Address  string `json:"address"`
	Share    string `json:"share"`
	User     string `json:"user"`
	Password string `json:"password"`
	Domain   string `json:"domain,omitempty"`
}

// Validate reports a missing address or share name.
func (s Server) Validate() error {
	if strings.TrimSpace(s.Address) == "" {
		return fmt.Errorf("请填写服务器地址")
	}
	if strings.TrimSpace(s.Share) == "" {
		return fmt.Errorf("请填写共享名")
	}
	return nil
}

// URL is the smb:// address of the share, used as the history marker.
func (s Server) URL() string {
	return "smb://" + s.Address + "/" + s.Share
}

// ParseSMBURL splits smb://server/share/path into its server and the
// share-relative path (backslash-separated, "" for the share root).
func ParseSMBURL(raw string) (srv Server, path string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "smb" || u.Host == "" {
		return Server{}, "", false
	}
	rest := strings.Trim(u.Path, "/")
	if rest == "" {
		return Server{}, "", false
	}
	share, p, _ := strings.Cut(rest, "/")
	if share == "" {
		return Server{}, "", false
	}
	return Server{Address: u.Host, Share: share}, Clean(p), true
}

// Join appends a name to a share-relative directory.
func Join(dir, name string) string {
	if dir == "" {
		return Clean(name)
	}
	return Clean(dir + "\\" + name)
}

// Parent is the directory above path, "" for the share root.
func Parent(path string) string {
	path = Clean(path)
	if i := strings.LastIndex(path, "\\"); i >= 0 {
		return path[:i]
	}
	return ""
}

// Base is the last component of a share-relative path.
func Base(path string) string {
	path = Clean(path)
	if i := strings.LastIndex(path, "\\"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// Clean normalises a share-relative path: forward slashes become
// backslashes, and leading, trailing and doubled separators go. ".." is
// dropped outright, so a path can never climb out of the share.
func Clean(path string) string {
	path = strings.ReplaceAll(path, "/", "\\")
	parts := make([]string, 0, 4)
	for _, part := range strings.Split(path, "\\") {
		if part == "" || part == "." || part == ".." {
			continue
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "\\")
}

// Class is what the browser does with a file.
type Class int

const (
	ClassHidden      Class = iota // not a video: not listed
	ClassPlayable                 // MP4/MOV/M4V: the system player can play it
	ClassUnsupported              // a video the system player cannot play
)

var playableExts = map[string]bool{".mp4": true, ".mov": true, ".m4v": true}

var unsupportedExts = map[string]bool{
	".mkv": true, ".avi": true, ".rmvb": true, ".wmv": true, ".flv": true, ".ts": true,
}

// Classify sorts a name into playable, unsupported or hidden.
func Classify(name string) Class {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case playableExts[ext]:
		return ClassPlayable
	case unsupportedExts[ext]:
		return ClassUnsupported
	default:
		return ClassHidden
	}
}

// ContentType is the media type the proxy serves a playable file as.
func ContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mov":
		return "video/quicktime"
	case ".mp4", ".m4v":
		return "video/mp4"
	default:
		return ""
	}
}

// Entry is one row of a directory listing.
type Entry struct {
	Name  string
	IsDir bool
	Size  int64
	Class Class // Meaningless for directories; check IsDir first.
}

// Session, Share and ShareFile are the small surface of go-smb2 this package
// uses, so tests can substitute fakes without a real NAS.
type Session interface {
	Mount(ctx context.Context, share string) (Share, error)
	Close() error
}

type Share interface {
	ReadDir(ctx context.Context, dir string) ([]os.FileInfo, error)
	Open(ctx context.Context, name string) (ShareFile, error)
	Unmount(ctx context.Context) error
}

type ShareFile interface {
	ReadAt(ctx context.Context, b []byte, off int64) (int, error)
	Seek(ctx context.Context, off int64, whence int) (int64, error)
	Stat(ctx context.Context) (os.FileInfo, error)
	Close(ctx context.Context) error
}

type realSession struct{ s *smb2.Session }

func (r *realSession) Mount(ctx context.Context, share string) (Share, error) {
	sh, err := r.s.Mount(ctx, share)
	if err != nil {
		return nil, err
	}
	return &realShare{sh: sh}, nil
}

func (r *realSession) Close() error { return r.s.Close() }

type realShare struct{ sh *smb2.Share }

func (r *realShare) ReadDir(ctx context.Context, dir string) ([]os.FileInfo, error) {
	return r.sh.ReadDir(ctx, dir)
}

func (r *realShare) Open(ctx context.Context, name string) (ShareFile, error) {
	f, err := r.sh.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	return &realFile{f: f}, nil
}

func (r *realShare) Unmount(ctx context.Context) error { return r.sh.Unmount(ctx) }

type realFile struct{ f *smb2.File }

func (r *realFile) ReadAt(ctx context.Context, b []byte, off int64) (int, error) {
	return r.f.ReadAt(ctx, b, off)
}

func (r *realFile) Seek(ctx context.Context, off int64, whence int) (int64, error) {
	return r.f.Seek(ctx, off, whence)
}

func (r *realFile) Stat(ctx context.Context) (os.FileInfo, error) { return r.f.Stat(ctx) }

func (r *realFile) Close(ctx context.Context) error { return r.f.Close(ctx) }

type DialFunc func(ctx context.Context, srv Server) (Session, error)

func defaultDial(ctx context.Context, srv Server) (Session, error) {
	var domain *string
	if srv.Domain != "" {
		domain = &srv.Domain
	}
	d := &smb2.Dialer{Credentials: auth.NTLMCredential{
		User: srv.User, Password: srv.Password, Domain: domain,
	}}
	sess, err := d.Dial(ctx, srv.Address)
	if err != nil {
		return nil, err
	}
	return &realSession{s: sess}, nil
}

// Client is one mounted share. Dial it before use; Close it when done.
// The session and share are guarded by mu: Close only ever takes them
// out of the client, never invalidating a share an in-flight Open or
// ReadDir already holds, so closing while a file is being opened cannot
// crash on a nil share.
type Client struct {
	Server Server

	dial  DialFunc
	mu    sync.Mutex
	sess  Session
	share Share
}

// New returns a client that dials the real server.
func New(srv Server) *Client { return &Client{Server: srv, dial: defaultDial} }

// NewWithDial returns a client whose dial step is supplied by the caller;
// tests use it to inject a fake session.
func NewWithDial(srv Server, dial DialFunc) *Client {
	return &Client{Server: srv, dial: dial}
}

// Dial connects and mounts the share.
func (c *Client) Dial(ctx context.Context) error {
	if err := c.Server.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != nil {
		return nil
	}
	sess, err := c.dial(ctx, c.Server)
	if err != nil {
		return err
	}
	share, err := sess.Mount(ctx, c.Server.Share)
	if err != nil {
		_ = sess.Close()
		return err
	}
	c.sess, c.share = sess, share
	return nil
}

// shareSnapshot returns the mounted share, or an error when the client is
// not connected. The caller uses it without the lock.
func (c *Client) shareSnapshot() (Share, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.share == nil {
		return nil, fmt.Errorf("内网盘未连接")
	}
	return c.share, nil
}

// ReadDir lists dir ("", the share root, or a backslash path): directories
// first, then playable files, then unsupported ones, each alphabetical;
// other files are not listed.
func (c *Client) ReadDir(ctx context.Context, dir string) ([]Entry, error) {
	share, err := c.shareSnapshot()
	if err != nil {
		return nil, err
	}
	infos, err := share.ReadDir(ctx, Clean(dir))
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(infos))
	for _, info := range infos {
		name := info.Name()
		if name == "." || name == ".." {
			continue
		}
		if info.IsDir() {
			entries = append(entries, Entry{Name: name, IsDir: true, Class: ClassPlayable})
			continue
		}
		class := Classify(name)
		if class == ClassHidden {
			continue
		}
		entries = append(entries, Entry{Name: name, Size: info.Size(), Class: class})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		if entries[i].Class != entries[j].Class {
			return entries[i].Class < entries[j].Class
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

// Open opens a file for reading through the proxy. Directories are
// rejected: browse them with ReadDir instead.
func (c *Client) Open(ctx context.Context, path string) (*File, error) {
	share, err := c.shareSnapshot()
	if err != nil {
		return nil, err
	}
	path = Clean(path)
	if path == "" {
		return nil, fmt.Errorf("请选择一个文件")
	}
	f, err := share.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat(ctx)
	if err != nil {
		_ = f.Close(ctx)
		return nil, err
	}
	if info.IsDir() {
		_ = f.Close(ctx)
		return nil, fmt.Errorf("%s 是文件夹", Base(path))
	}
	return &File{f: f, size: info.Size()}, nil
}

// Close unmounts the share and closes the session. Closing the client
// does not close files already opened from it. The share and session are
// taken out under the lock and released outside it, so an Open or ReadDir
// that already holds the share keeps working on its own reference.
func (c *Client) Close() error {
	c.mu.Lock()
	share, sess := c.share, c.sess
	c.share, c.sess = nil, nil
	c.mu.Unlock()
	var err error
	if share != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		uerr := share.Unmount(ctx)
		cancel()
		if uerr != nil {
			err = uerr
		}
	}
	if sess != nil {
		if cerr := sess.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}

// File is one open NAS file, read through the proxy with ReadAt. Size is
// known from opening, so the proxy can answer Range requests without
// another round trip.
type File struct {
	f    ShareFile
	size int64
	off  int64
}

// ReadAt reads len(p) bytes from off.
func (f *File) ReadAt(p []byte, off int64) (int, error) {
	return f.f.ReadAt(context.Background(), p, off)
}

// Seek moves the read position for Read.
func (f *File) Seek(off int64, whence int) (int64, error) {
	pos, err := f.f.Seek(context.Background(), off, whence)
	if err == nil {
		f.off = pos
	}
	return pos, err
}

// Read reads from the current position.
func (f *File) Read(p []byte) (int, error) {
	n, err := f.ReadAt(p, f.off)
	f.off += int64(n)
	return n, err
}

// Size is the file's length in bytes.
func (f *File) Size() int64 { return f.size }

// Close closes the file. It does not close the client.
func (f *File) Close() error { return f.f.Close(context.Background()) }
