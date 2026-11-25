package subtitle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/httpx"
)

const (
	DefaultShooterBaseURL = "https://www.shooter.cn/api/subapi.php"
	DefaultASSRTBaseURL   = "https://api.assrt.net"

	maxSubtitleBytes = 10 << 20 // 10 MiB hard cap for downloaded subtitles / zips
	maxSearchBytes   = 2 << 20
	minHashFileSize  = 8192
	hashChunkSize    = 4096
)

// ErrTokenRequired is returned when ASSRT keyword search is invoked without a
// user-configured API token.
var ErrTokenRequired = errors.New("subtitle: 需要配置伪射手 (ASSRT) Token 才能按片名在线搜索")

// Candidate represents one online subtitle match returned by Shooter or ASSRT.
type Candidate struct {
	ID       string
	Source   string // "shooter" or "assrt"
	Title    string
	Lang     string
	Format   string // "srt", "ass", "ssa", "vtt", "zip"
	URL      string
	DetailID int
	DelayMS  int
}

// Client queries public online subtitle services (anonymous Shooter file-hash
// matching and optional user-token ASSRT search) and downloads matched files.
type Client struct {
	HTTP           *http.Client
	ShooterBaseURL string
	ASSRTBaseURL   string
	ASSRTToken     string
}

// NewClient constructs a subtitle Client using Xiaomu's pooled proxy-aware transport.
func NewClient(assrtToken string) *Client {
	return &Client{
		HTTP:           httpx.NewClient(15 * time.Second),
		ShooterBaseURL: DefaultShooterBaseURL,
		ASSRTBaseURL:   DefaultASSRTBaseURL,
		ASSRTToken:     strings.TrimSpace(assrtToken),
	}
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return httpx.NewClient(15 * time.Second)
}

func (c *Client) shooterURL() string {
	if c != nil && strings.TrimSpace(c.ShooterBaseURL) != "" {
		return strings.TrimSpace(c.ShooterBaseURL)
	}
	return DefaultShooterBaseURL
}

func (c *Client) assrtURL() string {
	if c != nil && strings.TrimSpace(c.ASSRTBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.ASSRTBaseURL), "/")
	}
	return DefaultASSRTBaseURL
}

// ComputeShooterHash calculates the standard Shooter/SPlayer 4-block MD5 file
// hash for a local video file. Files smaller than 8 KiB cannot be hashed.
func ComputeShooterHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	if size < minHashFileSize {
		return "", fmt.Errorf("视频文件过小（%d 字节），无法计算哈希", size)
	}
	offsets := [4]int64{
		4096,
		size / 3 * 2,
		size / 3,
		size - 8192,
	}
	var buf [hashChunkSize]byte
	hashes := make([]string, 4)
	for i, off := range offsets {
		if off < 0 {
			off = 0
		}
		if off >= size {
			off = max(0, size-hashChunkSize)
		}
		n, rerr := f.ReadAt(buf[:], off)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return "", rerr
		}
		sum := md5.Sum(buf[:n])
		hashes[i] = hex.EncodeToString(sum[:])
	}
	return strings.Join(hashes, ";"), nil
}

type shooterItem struct {
	Desc  string        `json:"Desc"`
	Delay int           `json:"Delay"`
	Files []shooterFile `json:"Files"`
}

type shooterFile struct {
	Ext  string `json:"Ext"`
	Link string `json:"Link"`
}

// SearchShooter queries Shooter's anonymous hash API for subtitles matching the
// local video's filehash. Only the basename of filename is sent (never full path).
func (c *Client) SearchShooter(ctx context.Context, fileHash, filename string) ([]Candidate, error) {
	fileHash = strings.TrimSpace(fileHash)
	if fileHash == "" {
		return nil, errors.New("subtitle: 空文件哈希")
	}
	base := filepath.Base(strings.TrimSpace(filename))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "video.mkv"
	}
	form := url.Values{
		"filehash": {fileHash},
		"pathinfo": {base},
		"format":   {"json"},
		"lang":     {"Chn"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.shooterURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Xiaomu/1.0")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer httpx.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("射手字幕服务返回 HTTP %d", resp.StatusCode)
	}
	data, _, err := httpx.ReadLimit(resp.Body, resp.ContentLength, maxSearchBytes)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(data)
	// Shooter returns a single 0xff byte or empty body when no match exists.
	if len(trimmed) == 0 || (len(trimmed) == 1 && trimmed[0] == 0xff) {
		return nil, nil
	}
	var items []shooterItem
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, nil
	}
	var out []Candidate
	seen := make(map[string]bool)
	for idx, item := range items {
		for _, f := range item.Files {
			link := strings.TrimSpace(f.Link)
			if !isHTTPURL(link) || seen[link] {
				continue
			}
			ext := normalizeSubExt(f.Ext)
			if ext == "" {
				ext = normalizeSubExt(filepath.Ext(link))
			}
			if !isSupportedSubtitleExt(ext) {
				continue
			}
			seen[link] = true
			title := strings.TrimSpace(item.Desc)
			if title == "" {
				title = fmt.Sprintf("射手哈希匹配 #%d", idx+1)
			}
			out = append(out, Candidate{
				ID:      link,
				Source:  "shooter",
				Title:   title,
				Lang:    "中文",
				Format:  ext,
				URL:     link,
				DelayMS: item.Delay,
			})
		}
	}
	return out, nil
}

type assrtSearchResp struct {
	Status int    `json:"status"`
	ErrMsg string `json:"errmsg"`
	Sub    struct {
		Subs []struct {
			ID         int    `json:"id"`
			NativeName string `json:"native_name"`
			VideoName  string `json:"videoname"`
			Subtype    string `json:"subtype"`
			Lang       struct {
				Desc string `json:"desc"`
			} `json:"lang"`
		} `json:"subs"`
	} `json:"sub"`
}

type assrtDetailResp struct {
	Status int    `json:"status"`
	ErrMsg string `json:"errmsg"`
	Sub    struct {
		Subs []struct {
			ID       int    `json:"id"`
			Filename string `json:"filename"`
			URL      string `json:"url"`
			FileList []struct {
				URL string `json:"url"`
				F   string `json:"f"`
			} `json:"filelist"`
		} `json:"subs"`
	} `json:"sub"`
}

// SearchASSRT searches ASSRT by title/keyword using the user's API token.
func (c *Client) SearchASSRT(ctx context.Context, query string) ([]Candidate, error) {
	token := strings.TrimSpace(c.ASSRTToken)
	if token == "" {
		return nil, ErrTokenRequired
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("subtitle: 请输入字幕搜索关键词")
	}
	u := fmt.Sprintf("%s/v1/sub/search?token=%s&q=%s&cnt=15&pos=0",
		c.assrtURL(), url.QueryEscape(token), url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Xiaomu/1.0")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer httpx.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ASSRT 服务返回 HTTP %d", resp.StatusCode)
	}
	data, _, err := httpx.ReadLimit(resp.Body, resp.ContentLength, maxSearchBytes)
	if err != nil {
		return nil, err
	}
	var parsed assrtSearchResp
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("解析 ASSRT 响应失败：%w", err)
	}
	if parsed.Status != 0 {
		msg := strings.TrimSpace(parsed.ErrMsg)
		if msg == "" {
			msg = fmt.Sprintf("错误码 %d", parsed.Status)
		}
		return nil, fmt.Errorf("ASSRT 搜索失败：%s", msg)
	}
	out := make([]Candidate, 0, len(parsed.Sub.Subs))
	for _, item := range parsed.Sub.Subs {
		if item.ID <= 0 {
			continue
		}
		title := strings.TrimSpace(item.NativeName)
		if title == "" {
			title = strings.TrimSpace(item.VideoName)
		}
		if title == "" {
			title = fmt.Sprintf("字幕 #%d", item.ID)
		}
		out = append(out, Candidate{
			ID:       fmt.Sprintf("assrt:%d", item.ID),
			Source:   "assrt",
			Title:    title,
			Lang:     strings.TrimSpace(item.Lang.Desc),
			Format:   strings.ToLower(strings.TrimSpace(item.Subtype)),
			DetailID: item.ID,
		})
	}
	return out, nil
}

func (c *Client) resolveASSRTDownload(ctx context.Context, detailID int) (string, string, error) {
	token := strings.TrimSpace(c.ASSRTToken)
	if token == "" {
		return "", "", ErrTokenRequired
	}
	u := fmt.Sprintf("%s/v1/sub/detail?token=%s&id=%d", c.assrtURL(), url.QueryEscape(token), detailID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Xiaomu/1.0")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", "", err
	}
	defer httpx.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("ASSRT 详情返回 HTTP %d", resp.StatusCode)
	}
	data, _, err := httpx.ReadLimit(resp.Body, resp.ContentLength, maxSearchBytes)
	if err != nil {
		return "", "", err
	}
	var parsed assrtDetailResp
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", "", fmt.Errorf("解析 ASSRT 详情失败：%w", err)
	}
	if parsed.Status != 0 || len(parsed.Sub.Subs) == 0 {
		msg := strings.TrimSpace(parsed.ErrMsg)
		if msg == "" {
			msg = "未找到可用字幕文件"
		}
		return "", "", errors.New(msg)
	}
	sub := parsed.Sub.Subs[0]
	type rankedFile struct {
		url   string
		ext   string
		score int
	}
	var choices []rankedFile
	for _, f := range sub.FileList {
		link := strings.TrimSpace(f.URL)
		if !isHTTPURL(link) {
			continue
		}
		ext := normalizeSubExt(filepath.Ext(f.F))
		if !isSupportedSubtitleExt(ext) {
			continue
		}
		choices = append(choices, rankedFile{
			url:   link,
			ext:   ext,
			score: scoreSubtitleName(f.F, ext),
		})
	}
	if len(choices) > 0 {
		slices.SortFunc(choices, func(a, b rankedFile) int {
			return b.score - a.score
		})
		return choices[0].url, choices[0].ext, nil
	}
	link := strings.TrimSpace(sub.URL)
	if !isHTTPURL(link) {
		return "", "", errors.New("字幕条目不含有效下载链接")
	}
	ext := normalizeSubExt(filepath.Ext(sub.Filename))
	if ext == "" {
		ext = normalizeSubExt(filepath.Ext(link))
	}
	return link, ext, nil
}

// Download fetches cand into destDir and returns the local subtitle file path
// ready to be passed to libmpv AddSubtitleFile. ZIP archives are safely unpacked
// in-memory and the highest-ranked Chinese subtitle entry is extracted.
func (c *Client) Download(ctx context.Context, cand Candidate, destDir string) (string, error) {
	dlURL := strings.TrimSpace(cand.URL)
	ext := normalizeSubExt(cand.Format)
	if dlURL == "" && cand.Source == "assrt" && cand.DetailID > 0 {
		var err error
		dlURL, ext, err = c.resolveASSRTDownload(ctx, cand.DetailID)
		if err != nil {
			return "", err
		}
	}
	if !isHTTPURL(dlURL) {
		return "", errors.New("subtitle: 无效的字幕下载链接")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Xiaomu/1.0")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer httpx.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载字幕返回 HTTP %d", resp.StatusCode)
	}
	data, exceeded, err := httpx.ReadLimit(resp.Body, resp.ContentLength, maxSubtitleBytes)
	if err != nil {
		return "", err
	}
	if exceeded {
		return "", fmt.Errorf("字幕文件超过 %d 字节上限", maxSubtitleBytes)
	}
	if len(data) == 0 {
		return "", errors.New("下载到的字幕内容为空")
	}

	if isZipArchive(data) || ext == "zip" {
		extracted, zipExt, err := extractBestSubtitleFromZip(data)
		if err != nil {
			return "", err
		}
		data = extracted
		ext = zipExt
	} else if !isSupportedSubtitleExt(ext) {
		ext = sniffSubtitleExt(data)
	}
	if !isSupportedSubtitleExt(ext) {
		return "", fmt.Errorf("不支持的字幕格式（仅支持 srt/ass/ssa/vtt/zip）")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(dlURL + "|" + ext))
	name := hex.EncodeToString(sum[:8]) + "." + ext
	outPath := filepath.Join(destDir, name)
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func normalizeSubExt(raw string) string {
	ext := strings.ToLower(strings.TrimSpace(raw))
	ext = strings.TrimPrefix(ext, ".")
	if i := strings.IndexAny(ext, "?#"); i >= 0 {
		ext = ext[:i]
	}
	return ext
}

func isSupportedSubtitleExt(ext string) bool {
	switch ext {
	case "srt", "ass", "ssa", "vtt":
		return true
	default:
		return false
	}
}

func isZipArchive(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04
}

func sniffSubtitleExt(data []byte) string {
	head := strings.TrimSpace(string(data[:min(len(data), 512)]))
	lower := strings.ToLower(head)
	switch {
	case strings.Contains(lower, "[script info]"):
		return "ass"
	case strings.HasPrefix(head, "WEBVTT"):
		return "vtt"
	case strings.Contains(head, "-->"):
		return "srt"
	default:
		return ""
	}
}

func scoreSubtitleName(name, ext string) int {
	lower := strings.ToLower(name)
	score := 0
	if ext == "ass" || ext == "ssa" {
		score += 10
	} else if ext == "srt" {
		score += 8
	}
	for _, kw := range []string{"简英", "中英", "双语", "chs&eng", "zh-hans", "chs", "sc", "简体", "中字", "中文"} {
		if strings.Contains(lower, kw) {
			score += 30
			break
		}
	}
	for _, kw := range []string{"cht", "tc", "zh-hant", "繁体", "繁體"} {
		if strings.Contains(lower, kw) {
			score += 20
			break
		}
	}
	return score
}

func extractBestSubtitleFromZip(data []byte) ([]byte, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("解压字幕包失败：%w", err)
	}
	type candidateEntry struct {
		file  *zip.File
		ext   string
		score int
	}
	var entries []candidateEntry
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(f.Name)
		if strings.HasPrefix(base, "._") || strings.Contains(f.Name, "__MACOSX/") {
			continue
		}
		ext := normalizeSubExt(filepath.Ext(base))
		if !isSupportedSubtitleExt(ext) {
			continue
		}
		entries = append(entries, candidateEntry{
			file:  f,
			ext:   ext,
			score: scoreSubtitleName(base, ext),
		})
	}
	if len(entries) == 0 {
		return nil, "", errors.New("压缩包中未找到支持的字幕文件（srt/ass/ssa/vtt）")
	}
	slices.SortFunc(entries, func(a, b candidateEntry) int {
		return b.score - a.score
	})
	best := entries[0]
	rc, err := best.file.Open()
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	content, err := io.ReadAll(io.LimitReader(rc, maxSubtitleBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(content) > maxSubtitleBytes {
		return nil, "", fmt.Errorf("解压后的字幕超过 %d 字节上限", maxSubtitleBytes)
	}
	return content, best.ext, nil
}
