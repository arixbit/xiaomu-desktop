// Package pan implements native, Android-free cloud-drive share parsing,
// credential storage and playback stream resolution for Xiaomu.
package pan

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
)

// Provider identifies a cloud drive supported or recognized by Xiaomu.
type Provider string

const (
	ProviderQuark   Provider = "quark"
	ProviderUC      Provider = "uc"
	ProviderAli     Provider = "ali"
	Provider115     Provider = "115"
	ProviderTianYi  Provider = "tianyi"
	ProviderThunder Provider = "thunder"
	ProviderBaidu   Provider = "baidu"
)

// Label returns the human-readable Chinese display name of a provider.
func (p Provider) Label() string {
	switch p {
	case ProviderQuark:
		return "夸克网盘"
	case ProviderUC:
		return "UC网盘"
	case ProviderAli:
		return "阿里云盘"
	case Provider115:
		return "115网盘"
	case ProviderTianYi:
		return "天翼云盘"
	case ProviderThunder:
		return "迅雷云盘"
	case ProviderBaidu:
		return "百度网盘"
	default:
		return string(p)
	}
}

// Supported reports whether this build has a native resolver for the provider.
func (p Provider) Supported() bool {
	return p == ProviderQuark || p == ProviderUC || p == ProviderAli || p == ProviderBaidu
}

// LoginURL returns the official web login page for provider.
func (p Provider) LoginURL() string {
	switch p {
	case ProviderQuark:
		return "https://pan.quark.cn/"
	case ProviderUC:
		return "https://drive.uc.cn/"
	case ProviderAli:
		return "https://www.alipan.com/"
	case ProviderBaidu:
		return "https://pan.baidu.com/"
	case Provider115:
		return "https://115.com/"
	case ProviderTianYi:
		return "https://cloud.189.cn/"
	default:
		return ""
	}
}

// AuthRequiredError signals that playback or listing requires the user to
// sign in to Provider via its official web login page.
type AuthRequiredError struct {
	Provider Provider
	Reason   string
}

func (e *AuthRequiredError) Error() string {
	if strings.TrimSpace(e.Reason) != "" {
		return e.Reason
	}
	return fmt.Sprintf("播放此%s资源需要先登录账号", e.Provider.Label())
}

// AuthRequiredProvider extracts the provider from an error chain if login is needed.
func AuthRequiredProvider(err error) (Provider, bool) {
	if err == nil {
		return "", false
	}
	var authErr *AuthRequiredError
	if asAuthRequired(err, &authErr) && authErr != nil {
		return authErr.Provider, true
	}
	return "", false
}

func asAuthRequired(err error, target **AuthRequiredError) bool {
	for err != nil {
		if ae, ok := err.(*AuthRequiredError); ok {
			*target = ae
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ShareRef is one parsed cloud-drive share link.
type ShareRef struct {
	Provider Provider
	ShareID  string
	Passcode string
	RawURL   string
}

// Key uniquely identifies a share link regardless of query noise.
func (r ShareRef) Key() string {
	return string(r.Provider) + ":" + r.ShareID
}

// Credentials holds user-authorized cloud-drive sessions saved locally.
// Credentials must never be logged or committed.
type Credentials struct {
	QuarkCookie      string `json:"quark_cookie,omitempty"`
	UCCookie         string `json:"uc_cookie,omitempty"`
	AliToken         string `json:"ali_token,omitempty"` // refresh_token from alipan web login
	AliAccessToken   string `json:"ali_access_token,omitempty"`
	AliDriveID       string `json:"ali_drive_id,omitempty"`
	BaiduCookie      string `json:"baidu_cookie,omitempty"`
	P115Cookie       string `json:"p115_cookie,omitempty"`
	TempDirName      string `json:"temp_dir_name,omitempty"`
	PreferredQuality string `json:"preferred_quality,omitempty"` // auto, 4k, 2k, 1080p, original
}

// DefaultTempDirName is the dedicated temporary folder created inside the user's
// drive for share playback and cleaned up automatically.
const DefaultTempDirName = "小幕临时播放"

// EffectiveTempDir returns the configured temp folder name or the default.
func (c Credentials) EffectiveTempDir() string {
	if s := strings.TrimSpace(c.TempDirName); s != "" {
		return s
	}
	return DefaultTempDirName
}

// EffectiveQuality returns the normalized preferred quality setting.
func (c Credentials) EffectiveQuality() string {
	switch strings.ToLower(strings.TrimSpace(c.PreferredQuality)) {
	case "4k", "2k", "1080p", "original":
		return strings.ToLower(strings.TrimSpace(c.PreferredQuality))
	default:
		return "auto"
	}
}

// Cookie returns the trimmed credential string for provider.
func (c Credentials) Cookie(p Provider) string {
	switch p {
	case ProviderQuark:
		return sanitizeCookie(c.QuarkCookie)
	case ProviderUC:
		return sanitizeCookie(c.UCCookie)
	case ProviderAli:
		return strings.TrimSpace(c.AliToken)
	case ProviderBaidu:
		return sanitizeCookie(c.BaiduCookie)
	case Provider115:
		return sanitizeCookie(c.P115Cookie)
	default:
		return ""
	}
}

// Clear removes stored authorization for provider.
func (c *Credentials) Clear(p Provider) {
	switch p {
	case ProviderQuark:
		c.QuarkCookie = ""
	case ProviderUC:
		c.UCCookie = ""
	case ProviderAli:
		c.AliToken = ""
		c.AliAccessToken = ""
		c.AliDriveID = ""
	case ProviderBaidu:
		c.BaiduCookie = ""
	case Provider115:
		c.P115Cookie = ""
	}
}

// Has reports whether a non-empty credential is configured for provider.
func (c Credentials) Has(p Provider) bool {
	return c.Cookie(p) != ""
}

// Masked returns a safe display summary without exposing secret contents.
func (c Credentials) Masked(p Provider) string {
	if !c.Has(p) {
		return "未登录"
	}
	return "已登录"
}

func sanitizeCookie(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "\r", "")
	raw = strings.ReplaceAll(raw, "\n", "")
	return raw
}

var (
	defaultCredsMu sync.RWMutex
	defaultCreds   Credentials
)

// DefaultCredentials returns the process-wide user credentials loaded on startup.
func DefaultCredentials() Credentials {
	defaultCredsMu.RLock()
	defer defaultCredsMu.RUnlock()
	return defaultCreds
}

// SetDefaultCredentials updates the process-wide credentials used by pan adapters.
func SetDefaultCredentials(c Credentials) {
	defaultCredsMu.Lock()
	defaultCreds = c
	defaultCredsMu.Unlock()
}

// LoadCredentials reads credentials from disk. Missing files return empty credentials.
func LoadCredentials(filePath string) (Credentials, error) {
	data, err := os.ReadFile(filePath)
	if os.IsNotExist(err) {
		return Credentials{}, nil
	}
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return Credentials{}, fmt.Errorf("读取网盘凭证失败：%w", err)
	}
	return c, nil
}

// SaveCredentials atomically writes credentials with owner-only 0600 permissions.
func SaveCredentials(filePath string, c Credentials) error {
	c.QuarkCookie = sanitizeCookie(c.QuarkCookie)
	c.UCCookie = sanitizeCookie(c.UCCookie)
	c.AliToken = strings.TrimSpace(c.AliToken)
	c.AliAccessToken = strings.TrimSpace(c.AliAccessToken)
	c.AliDriveID = strings.TrimSpace(c.AliDriveID)
	c.BaiduCookie = sanitizeCookie(c.BaiduCookie)
	c.P115Cookie = sanitizeCookie(c.P115Cookie)
	c.TempDirName = strings.TrimSpace(c.TempDirName)
	c.PreferredQuality = c.EffectiveQuality()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pan-creds-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filePath)
}

// AuthStatus describes the result of validating one provider's credential.
type AuthStatus struct {
	Provider Provider
	Valid    bool
	VIP      string // e.g. "SVIP", "VIP", "普通用户"
	Summary  string
}

// FileItem is one video file discovered inside a cloud-drive share.
type FileItem struct {
	Provider      Provider
	ShareID       string
	Passcode      string
	FileID        string
	ShareFidToken string
	Name          string
	Size          int64
}

// EncodeEpisodeID encodes a FileItem into a self-contained episode ID without secrets.
func EncodeEpisodeID(item FileItem) string {
	q := url.Values{}
	q.Set("sid", item.ShareID)
	q.Set("fid", item.FileID)
	if item.ShareFidToken != "" {
		q.Set("tok", item.ShareFidToken)
	}
	if item.Passcode != "" {
		q.Set("pwd", item.Passcode)
	}
	if item.Name != "" {
		q.Set("name", item.Name)
	}
	if item.Size > 0 {
		q.Set("size", strconv.FormatInt(item.Size, 10))
	}
	return "pan://" + string(item.Provider) + "?" + q.Encode()
}

// DecodeEpisodeID parses a pan:// episode ID created by EncodeEpisodeID.
func DecodeEpisodeID(raw string) (FileItem, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "pan") || u.Host == "" {
		return FileItem{}, false
	}
	q := u.Query()
	sid := q.Get("sid")
	fid := q.Get("fid")
	if sid == "" || fid == "" {
		return FileItem{}, false
	}
	size, _ := strconv.ParseInt(q.Get("size"), 10, 64)
	return FileItem{
		Provider:      Provider(strings.ToLower(u.Host)),
		ShareID:       sid,
		Passcode:      q.Get("pwd"),
		FileID:        fid,
		ShareFidToken: q.Get("tok"),
		Name:          q.Get("name"),
		Size:          size,
	}, true
}

var (
	shareURLPattern = regexp.MustCompile(`(?i)(?:push://|push:)?(https?://(?:pan\.quark\.cn/s/[A-Za-z0-9_-]+|drive\.uc\.cn/s/[A-Za-z0-9_-]+|(?:www\.)?(?:alipan|aliyundrive)\.com/s/[A-Za-z0-9_-]+|(?:115|115cdn|anxia)\.com/s/[A-Za-z0-9_-]+|cloud\.189\.cn/(?:t/[A-Za-z0-9_-]+|web/share\?code=[A-Za-z0-9_-]+)|pan\.xunlei\.com/s/[A-Za-z0-9_-]+|pan\.baidu\.com/(?:s/[A-Za-z0-9_-]+|share/init\?[^\s"'<>]*surl=[A-Za-z0-9_-]+))[^\s"'<>]*)`)
	passcodePattern = regexp.MustCompile(`(?i)(?:提取码|密码|访问码|pwd|code)\s*[:：=]?\s*([A-Za-z0-9]{4,8})`)
)

// ParseShareURL parses a single cloud-drive share link (optionally prefixed with push://
// or followed by a passcode note such as "提取码: abcd").
func ParseShareURL(raw string) (ShareRef, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ShareRef{}, false
	}
	s = strings.TrimPrefix(s, "push://")
	s = strings.TrimPrefix(s, "push:")
	s = strings.TrimSpace(s)

	m := shareURLPattern.FindStringSubmatch(s)
	if len(m) < 2 {
		return ShareRef{}, false
	}
	cleanURL := strings.TrimRight(m[1], ".,;:!?)]}，。；：！？】）》")
	u, err := url.Parse(cleanURL)
	if err != nil || u.Host == "" {
		return ShareRef{}, false
	}
	host := strings.ToLower(u.Hostname())
	var provider Provider
	var shareID string
	switch host {
	case "pan.quark.cn":
		provider = ProviderQuark
		shareID = shareIDFromPath(u.Path, "/s/")
	case "drive.uc.cn":
		provider = ProviderUC
		shareID = shareIDFromPath(u.Path, "/s/")
	case "alipan.com", "www.alipan.com", "aliyundrive.com", "www.aliyundrive.com":
		provider = ProviderAli
		shareID = shareIDFromPath(u.Path, "/s/")
	case "115.com", "115cdn.com", "anxia.com":
		provider = Provider115
		shareID = shareIDFromPath(u.Path, "/s/")
	case "cloud.189.cn":
		provider = ProviderTianYi
		if id := shareIDFromPath(u.Path, "/t/"); id != "" {
			shareID = id
		} else {
			shareID = strings.TrimSpace(u.Query().Get("code"))
		}
	case "pan.xunlei.com":
		provider = ProviderThunder
		shareID = shareIDFromPath(u.Path, "/s/")
	case "pan.baidu.com":
		provider = ProviderBaidu
		shareID = shareIDFromPath(u.Path, "/s/")
		if shareID == "" {
			if surl := strings.TrimSpace(u.Query().Get("surl")); surl != "" {
				if !strings.HasPrefix(surl, "1") {
					shareID = "1" + surl
				} else {
					shareID = surl
				}
			}
		}
	default:
		return ShareRef{}, false
	}
	if shareID == "" {
		return ShareRef{}, false
	}
	pwd := strings.TrimSpace(u.Query().Get("pwd"))
	if pwd == "" {
		pwd = strings.TrimSpace(u.Query().Get("password"))
	}
	if pwd == "" {
		// Match passcode annotations only outside the URL itself so query keys
		// like cloud.189.cn's ?code=<shareID> are not mistaken for passcodes.
		outside := strings.Replace(s, cleanURL, " ", 1)
		if pm := passcodePattern.FindStringSubmatch(outside); len(pm) >= 2 {
			pwd = pm[1]
		}
	}
	return ShareRef{
		Provider: provider,
		ShareID:  shareID,
		Passcode: pwd,
		RawURL:   cleanURL,
	}, true
}

func shareIDFromPath(p, prefix string) string {
	i := strings.Index(p, prefix)
	if i < 0 {
		return ""
	}
	rest := p[i+len(prefix):]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// ExtractShareURLs extracts unique cloud-drive share links from text or HTML.
func ExtractShareURLs(text string) []ShareRef {
	matches := shareURLPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]int, len(matches))
	var out []ShareRef
	for _, loc := range matches {
		end := loc[1]
		tailEnd := min(len(text), end+48)
		snippet := text[loc[0]:tailEnd]
		ref, ok := ParseShareURL(snippet)
		if !ok {
			continue
		}
		if idx, dup := seen[ref.Key()]; dup {
			if out[idx].Passcode == "" && ref.Passcode != "" {
				out[idx].Passcode = ref.Passcode
			}
			continue
		}
		seen[ref.Key()] = len(out)
		out = append(out, ref)
	}
	return out
}

// IsPanLink reports whether raw is a pan:// episode ID, push:// link, or cloud share URL.
func IsPanLink(raw string) bool {
	if _, ok := DecodeEpisodeID(raw); ok {
		return true
	}
	_, ok := ParseShareURL(raw)
	return ok
}

var videoExts = map[string]bool{
	".mp4":  true,
	".mkv":  true,
	".mov":  true,
	".m4v":  true,
	".m3u8": true,
	".ts":   true,
	".avi":  true,
	".flv":  true,
	".webm": true,
	".wmv":  true,
	".rmvb": true,
	".mpg":  true,
	".mpeg": true,
	".m2ts": true,
	".iso":  false,
}

var nativePlayableExts = map[string]bool{
	".mp4":  true,
	".mov":  true,
	".m4v":  true,
	".m3u8": true,
}

var mpvPlayableExts = map[string]bool{
	".mkv":  true,
	".webm": true,
	".flv":  true,
	".ts":   true,
	".m2ts": true,
	".avi":  true,
	".wmv":  true,
	".mpg":  true,
	".mpeg": true,
	".rmvb": true,
}

// IsVideoFilename reports whether name has a common video file extension.
func IsVideoFilename(name string) bool {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(name)))
	return videoExts[ext]
}

// IsNativePlayableFilename reports whether Xiaomu's built-in player natively
// supports the container format on the current platform (full-format mygo-player
// on macOS and Windows amd64, system decoder fallback elsewhere).
func IsNativePlayableFilename(name string) bool {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(name)))
	if nativePlayableExts[ext] {
		return true
	}
	return (runtime.GOOS == "darwin" || (runtime.GOOS == "windows" && runtime.GOARCH == "amd64")) && mpvPlayableExts[ext]
}

// ContainerLabel returns a short uppercase container label such as "MKV" or "MP4".
func ContainerLabel(filename string) string {
	ext := strings.TrimPrefix(strings.ToUpper(path.Ext(strings.TrimSpace(filename))), ".")
	if ext == "" {
		return "原画"
	}
	return ext
}

// DisplayEpisodeName returns a clean episode title derived from a filename.
func DisplayEpisodeName(filename string) string {
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/"))
	ext := path.Ext(base)
	if ext != "" && IsVideoFilename(base) {
		base = strings.TrimSuffix(base, ext)
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return filename
	}
	return base
}

// NaturalLess compares strings with embedded decimal numbers in numeric order,
// so "S01E2" sorts before "S01E10" and "第2集" before "第10集".
func NaturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		da := isDigit(a[i])
		db := isDigit(b[j])
		if da && db {
			naStart, nbStart := i, j
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			sa := strings.TrimLeft(a[naStart:i], "0")
			sb := strings.TrimLeft(b[nbStart:j], "0")
			if len(sa) != len(sb) {
				return len(sa) < len(sb)
			}
			if sa != sb {
				return sa < sb
			}
			// Same numeric value: shorter leading-zero representation first.
			if (i - naStart) != (j - nbStart) {
				return (i - naStart) < (j - nbStart)
			}
			continue
		}
		ca, cb := toLowerASCII(a[i]), toLowerASCII(b[j])
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func toLowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// PickPreferredStream chooses the best stream index according to the user's
// quality preference and native player compatibility.
func PickPreferredStream(streams []catalog.Stream, preference string) int {
	if len(streams) == 0 {
		return -1
	}
	pref := strings.ToLower(strings.TrimSpace(preference))
	if pref == "original" {
		for i, s := range streams {
			if s.IsOriginal {
				return i
			}
		}
	}
	// Match explicit quality target if available and natively playable.
	if pref == "4k" || pref == "2k" || pref == "1080p" {
		target := pref
		if pref == "1080p" {
			target = "super"
		}
		for i, s := range streams {
			if strings.EqualFold(s.Quality, target) && s.NativePlayable {
				return i
			}
		}
	}
	// Default: prefer natively playable original stream first, then highest
	// natively playable stream.
	for i, s := range streams {
		if s.IsOriginal && s.NativePlayable {
			return i
		}
	}
	for i, s := range streams {
		if s.NativePlayable {
			return i
		}
	}
	return 0
}
