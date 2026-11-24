package pan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/netproxy"
)

const (
	quarkBaseURL = "https://drive-pc.quark.cn/1/clouddrive"
	quarkReferer = "https://pan.quark.cn/"
	quarkUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"

	ucBaseURL = "https://pc-api.uc.cn/1/clouddrive"
	ucReferer = "https://drive.uc.cn/"
	ucUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) uc-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"

	maxShareDepth = 4
	maxShareDirs  = 20
	maxShareFiles = 300
	keepTempFiles = 3
)

// ClientOptions configures a Quark or UC cloud-drive client. Tests inject
// BaseURL and HTTPClient to exercise the full protocol offline.
type ClientOptions struct {
	Provider    Provider
	BaseURL     string
	Referer     string
	UserAgent   string
	Product     string
	HTTPClient  *http.Client
	Credentials *Credentials
}

type quarkClient struct {
	provider   Provider
	baseURL    string
	referer    string
	ua         string
	product    string
	httpClient *http.Client
	credsFn    func() Credentials

	mu          sync.Mutex
	stokenCache map[string]cachedStoken
	tempDirKey  string
	tempDirFid  string
	recentSaved []string
}

type cachedStoken struct {
	token   string
	expires time.Time
}

var (
	sharedClientsMu sync.Mutex
	sharedClients   = map[Provider]*quarkClient{}
)

func sharedQuarkClient(p Provider) *quarkClient {
	sharedClientsMu.Lock()
	defer sharedClientsMu.Unlock()
	if c, ok := sharedClients[p]; ok {
		return c
	}
	c := NewQuarkClient(ClientOptions{Provider: p})
	sharedClients[p] = c
	return c
}

// NewQuarkClient constructs a Quark or UC cloud-drive protocol client.
func NewQuarkClient(opt ClientOptions) *quarkClient {
	p := opt.Provider
	if p != ProviderUC {
		p = ProviderQuark
	}
	base, ref, ua, prod := quarkBaseURL, quarkReferer, quarkUA, "ucpro"
	if p == ProviderUC {
		base, ref, ua, prod = ucBaseURL, ucReferer, ucUA, "UCBrowser"
	}
	if opt.BaseURL != "" {
		base = strings.TrimRight(opt.BaseURL, "/")
	}
	if opt.Referer != "" {
		ref = opt.Referer
	}
	if opt.UserAgent != "" {
		ua = opt.UserAgent
	}
	if opt.Product != "" {
		prod = opt.Product
	}
	hc := opt.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: netproxy.Transport(), Timeout: 20 * time.Second}
	}
	credsFn := DefaultCredentials
	if opt.Credentials != nil {
		fixed := *opt.Credentials
		credsFn = func() Credentials { return fixed }
	}
	return &quarkClient{
		provider:    p,
		baseURL:     base,
		referer:     ref,
		ua:          ua,
		product:     prod,
		httpClient:  hc,
		credsFn:     credsFn,
		stokenCache: map[string]cachedStoken{},
	}
}

func (c *quarkClient) querySuffix() string {
	return "pr=" + url.QueryEscape(c.product) + "&fr=pc"
}

func (c *quarkClient) cookie() (string, error) {
	cookie := c.credsFn().Cookie(c.provider)
	if cookie == "" {
		return "", &AuthRequiredError{
			Provider: c.provider,
			Reason:   fmt.Sprintf("播放%s资源需要先登录账号", c.provider.Label()),
		}
	}
	return cookie, nil
}

func (c *quarkClient) doJSON(ctx context.Context, method, endpoint string, body any, needAuth bool, out any) ([]*http.Cookie, error) {
	var cookie string
	if needAuth {
		var err error
		cookie, err = c.cookie()
		if err != nil {
			return nil, err
		}
	} else {
		cookie = c.credsFn().Cookie(c.provider)
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	reqURL := c.baseURL + endpoint + sep + c.querySuffix()
	req, err := http.NewRequestWithContext(ctx, method, reqURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Referer", c.referer)
	req.Header.Set("Origin", strings.TrimRight(c.referer, "/"))
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if body != nil {
		req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.Cookies(), &AuthRequiredError{
			Provider: c.provider,
			Reason:   fmt.Sprintf("%s登录状态无效或已过期（HTTP %d），请重新登录", c.provider.Label(), resp.StatusCode),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Message != "" {
			return resp.Cookies(), fmt.Errorf("%s接口返回错误（HTTP %d / %d）：%s", c.provider.Label(), resp.StatusCode, apiErr.Code, apiErr.Message)
		}
		return resp.Cookies(), fmt.Errorf("%s接口返回 HTTP %d", c.provider.Label(), resp.StatusCode)
	}
	var envelope struct {
		Status  int             `json:"status"`
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return resp.Cookies(), fmt.Errorf("解析%s响应失败：%w", c.provider.Label(), err)
	}
	if envelope.Code != 0 {
		msg := strings.TrimSpace(envelope.Message)
		if msg == "" {
			msg = fmt.Sprintf("错误码 %d", envelope.Code)
		}
		if envelope.Code == 31001 || envelope.Code == 31002 || envelope.Code == 41008 {
			return resp.Cookies(), &AuthRequiredError{
				Provider: c.provider,
				Reason:   fmt.Sprintf("%s登录状态已失效（%s），请重新登录", c.provider.Label(), msg),
			}
		}
		return resp.Cookies(), fmt.Errorf("%s返回错误：%s", c.provider.Label(), msg)
	}
	if out != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return resp.Cookies(), fmt.Errorf("解析%s数据失败：%w", c.provider.Label(), err)
		}
	}
	return resp.Cookies(), nil
}

// CheckAuth verifies whether the configured cookie is accepted and reports VIP tier.
func (c *quarkClient) CheckAuth(ctx context.Context) (AuthStatus, error) {
	status := AuthStatus{Provider: c.provider}
	var member struct {
		MemberType string `json:"member_type"`
		VIPType    string `json:"vip_type"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/member?fetch_subscribe=true", nil, true, &member); err == nil {
		status.Valid = true
		mt := strings.ToUpper(strings.TrimSpace(member.MemberType))
		if mt == "" {
			mt = strings.ToUpper(strings.TrimSpace(member.VIPType))
		}
		switch {
		case strings.Contains(mt, "SVIP") || strings.Contains(mt, "SUPER") || strings.Contains(mt, "88VIP"):
			status.VIP = "SVIP"
			status.Summary = c.provider.Label() + "已验证 · SVIP（支持 4K/2K 转码与高速播放）"
		case strings.Contains(mt, "VIP"):
			status.VIP = "VIP"
			status.Summary = c.provider.Label() + "已验证 · VIP"
		default:
			status.VIP = "普通用户"
			status.Summary = c.provider.Label() + "已验证 · 普通账号（高画质转码受限）"
		}
		return status, nil
	}
	// Fallback check: list root directory with 1 item.
	var list struct {
		List []any `json:"list"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/file/sort?pdir_fid=0&_page=1&_size=1", nil, true, &list); err != nil {
		return status, err
	}
	status.Valid = true
	status.VIP = "已验证"
	status.Summary = c.provider.Label() + " Cookie 验证通过"
	return status, nil
}

func (c *quarkClient) shareToken(ctx context.Context, shareID, passcode string) (string, error) {
	key := shareID + "\x00" + passcode
	c.mu.Lock()
	if cached, ok := c.stokenCache[key]; ok && time.Now().Before(cached.expires) {
		c.mu.Unlock()
		return cached.token, nil
	}
	c.mu.Unlock()

	var resp struct {
		Stoken string `json:"stoken"`
	}
	payload := map[string]string{
		"pwd_id":   shareID,
		"passcode": passcode,
	}
	if _, err := c.doJSON(ctx, http.MethodPost, "/share/sharepage/token", payload, false, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Stoken) == "" {
		return "", fmt.Errorf("未获取到%s分享令牌，分享可能已失效或需要提取码", c.provider.Label())
	}
	c.mu.Lock()
	c.stokenCache[key] = cachedStoken{token: resp.Stoken, expires: time.Now().Add(10 * time.Minute)}
	c.mu.Unlock()
	return resp.Stoken, nil
}

// ExpandShare walks a share link recursively and returns naturally sorted video files.
func (c *quarkClient) ExpandShare(ctx context.Context, ref ShareRef) ([]FileItem, error) {
	stoken, err := c.shareToken(ctx, ref.ShareID, ref.Passcode)
	if err != nil {
		return nil, err
	}
	type dirTask struct {
		fid   string
		depth int
	}
	queue := []dirTask{{fid: "0", depth: 0}}
	visitedDirs := 0
	var files []FileItem

	for len(queue) > 0 && visitedDirs < maxShareDirs && len(files) < maxShareFiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		visitedDirs++

		for page := 1; page <= 5 && len(files) < maxShareFiles; page++ {
			endpoint := fmt.Sprintf("/share/sharepage/detail?pwd_id=%s&stoken=%s&pdir_fid=%s&_page=%d&_size=100&_sort=file_type:asc,file_name:asc",
				url.QueryEscape(ref.ShareID), url.QueryEscape(stoken), url.QueryEscape(cur.fid), page)
			var detail struct {
				List []struct {
					Fid           string `json:"fid"`
					FileName      string `json:"file_name"`
					Size          int64  `json:"size"`
					Dir           bool   `json:"dir"`
					FileType      int    `json:"file_type"`
					ShareFidToken string `json:"share_fid_token"`
				} `json:"list"`
			}
			if _, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, false, &detail); err != nil {
				if len(files) > 0 {
					break
				}
				return nil, err
			}
			for _, entry := range detail.List {
				isDir := entry.Dir || entry.FileType == 0
				if isDir {
					if cur.depth+1 < maxShareDepth && entry.Fid != "" {
						queue = append(queue, dirTask{fid: entry.Fid, depth: cur.depth + 1})
					}
					continue
				}
				if !IsVideoFilename(entry.FileName) {
					continue
				}
				files = append(files, FileItem{
					Provider:      c.provider,
					ShareID:       ref.ShareID,
					Passcode:      ref.Passcode,
					FileID:        entry.Fid,
					ShareFidToken: entry.ShareFidToken,
					Name:          entry.FileName,
					Size:          entry.Size,
				})
				if len(files) >= maxShareFiles {
					break
				}
			}
			if len(detail.List) < 100 {
				break
			}
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("此%s分享中没有找到可播放的视频文件", c.provider.Label())
	}
	sort.SliceStable(files, func(i, j int) bool {
		return NaturalLess(files[i].Name, files[j].Name)
	})
	return files, nil
}

func (c *quarkClient) ensureTempDir(ctx context.Context) (string, error) {
	creds := c.credsFn()
	dirName := creds.EffectiveTempDir()
	cookie, err := c.cookie()
	if err != nil {
		return "", err
	}
	cacheKey := cookie + "\x00" + dirName
	c.mu.Lock()
	if c.tempDirKey == cacheKey && c.tempDirFid != "" {
		fid := c.tempDirFid
		c.mu.Unlock()
		return fid, nil
	}
	c.mu.Unlock()

	var root struct {
		List []struct {
			Fid      string `json:"fid"`
			FileName string `json:"file_name"`
			Dir      bool   `json:"dir"`
			FileType int    `json:"file_type"`
		} `json:"list"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/file/sort?pdir_fid=0&_page=1&_size=100&_sort=file_type:asc", nil, true, &root); err != nil {
		return "", err
	}
	for _, entry := range root.List {
		if (entry.Dir || entry.FileType == 0) && entry.FileName == dirName && entry.Fid != "" {
			c.mu.Lock()
			c.tempDirKey, c.tempDirFid = cacheKey, entry.Fid
			c.mu.Unlock()
			return entry.Fid, nil
		}
	}
	var created struct {
		Fid string `json:"fid"`
	}
	payload := map[string]any{
		"pdir_fid":      "0",
		"file_name":     dirName,
		"dir_path":      "",
		"dir_init_lock": false,
	}
	if _, err := c.doJSON(ctx, http.MethodPost, "/file", payload, true, &created); err != nil {
		return "", fmt.Errorf("创建临时目录 %q 失败：%w", dirName, err)
	}
	if created.Fid == "" {
		return "", fmt.Errorf("创建临时目录 %q 未返回目录 ID", dirName)
	}
	c.mu.Lock()
	c.tempDirKey, c.tempDirFid = cacheKey, created.Fid
	c.mu.Unlock()
	return created.Fid, nil
}

func (c *quarkClient) saveShareFile(ctx context.Context, item FileItem) (string, error) {
	tempDirFid, err := c.ensureTempDir(ctx)
	if err != nil {
		return "", err
	}
	stoken, err := c.shareToken(ctx, item.ShareID, item.Passcode)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"fid_list":       []string{item.FileID},
		"fid_token_list": []string{item.ShareFidToken},
		"to_pdir_fid":    tempDirFid,
		"pwd_id":         item.ShareID,
		"stoken":         stoken,
		"pdir_fid":       "0",
		"scene":          "link",
	}
	var saveResp struct {
		TaskID string `json:"task_id"`
		SaveAs struct {
			SaveAsTopFids []string `json:"save_as_top_fids"`
		} `json:"save_as"`
	}
	if _, err := c.doJSON(ctx, http.MethodPost, "/share/sharepage/save", payload, true, &saveResp); err != nil {
		return "", fmt.Errorf("转存%s文件失败：%w", c.provider.Label(), err)
	}
	var savedFid string
	if len(saveResp.SaveAs.SaveAsTopFids) > 0 {
		savedFid = saveResp.SaveAs.SaveAsTopFids[0]
	} else if saveResp.TaskID != "" {
		savedFid, err = c.waitSaveTask(ctx, saveResp.TaskID)
		if err != nil {
			return "", err
		}
	}
	if savedFid == "" {
		return "", fmt.Errorf("转存%s文件未返回新文件 ID", c.provider.Label())
	}
	c.rememberSaved(savedFid)
	return savedFid, nil
}

func (c *quarkClient) waitSaveTask(ctx context.Context, taskID string) (string, error) {
	deadline := time.Now().Add(15 * time.Second)
	for retry := 0; retry < 30 && time.Now().Before(deadline); retry++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var taskResp struct {
			Status int `json:"status"`
			SaveAs struct {
				SaveAsTopFids []string `json:"save_as_top_fids"`
			} `json:"save_as"`
		}
		endpoint := fmt.Sprintf("/task?task_id=%s&retry_index=%d", url.QueryEscape(taskID), retry)
		if _, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, true, &taskResp); err != nil {
			return "", err
		}
		if taskResp.Status == 2 {
			if len(taskResp.SaveAs.SaveAsTopFids) > 0 {
				return taskResp.SaveAs.SaveAsTopFids[0], nil
			}
			return "", fmt.Errorf("转存完成但未获取到文件 ID")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(350 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("转存%s文件超时，请重试", c.provider.Label())
}

func (c *quarkClient) rememberSaved(fid string) {
	c.mu.Lock()
	c.recentSaved = append(c.recentSaved, fid)
	var evict []string
	if len(c.recentSaved) > keepTempFiles {
		evict = append(evict, c.recentSaved[:len(c.recentSaved)-keepTempFiles]...)
		c.recentSaved = c.recentSaved[len(c.recentSaved)-keepTempFiles:]
	}
	c.mu.Unlock()
	if len(evict) > 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_ = c.deleteFiles(ctx, evict)
		}()
	}
}

func (c *quarkClient) deleteFiles(ctx context.Context, fids []string) error {
	if len(fids) == 0 {
		return nil
	}
	payload := map[string]any{
		"action_type":  2,
		"filelist":     fids,
		"exclude_fids": []string{},
	}
	_, err := c.doJSON(ctx, http.MethodPost, "/file/delete", payload, true, nil)
	return err
}

// CleanupTemp removes all files saved inside Xiaomu's temporary playback folder.
func (c *quarkClient) CleanupTemp(ctx context.Context) (int, error) {
	if !c.credsFn().Has(c.provider) {
		return 0, nil
	}
	dirFid, err := c.ensureTempDir(ctx)
	if err != nil {
		return 0, err
	}
	var listing struct {
		List []struct {
			Fid string `json:"fid"`
		} `json:"list"`
	}
	endpoint := fmt.Sprintf("/file/sort?pdir_fid=%s&_page=1&_size=100", url.QueryEscape(dirFid))
	if _, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, true, &listing); err != nil {
		return 0, err
	}
	fids := make([]string, 0, len(listing.List))
	for _, entry := range listing.List {
		if entry.Fid != "" {
			fids = append(fids, entry.Fid)
		}
	}
	if len(fids) == 0 {
		return 0, nil
	}
	if err := c.deleteFiles(ctx, fids); err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.recentSaved = nil
	c.mu.Unlock()
	return len(fids), nil
}

// ResolvePlayback saves a shared file to the temp directory and fetches both
// transcoded HLS streams (4K/2K/1080P) and the original download direct link.
func (c *quarkClient) ResolvePlayback(ctx context.Context, item FileItem) (catalog.Playback, error) {
	savedFid, err := c.saveShareFile(ctx, item)
	if err != nil {
		return catalog.Playback{}, err
	}
	return c.ResolveSavedFile(ctx, savedFid, item.Name, item.Size)
}

// ResolveSavedFile fetches playback streams for a file already in the user's drive.
func (c *quarkClient) ResolveSavedFile(ctx context.Context, fid, filename string, size int64) (catalog.Playback, error) {
	baseCookie, err := c.cookie()
	if err != nil {
		return catalog.Playback{}, err
	}
	baseHeaders := map[string]string{
		"User-Agent": c.ua,
		"Referer":    c.referer,
		"Cookie":     baseCookie,
	}

	var streams []catalog.Stream

	// 1. Fetch transcoded HLS streams (natively playable by AVPlayer / Media Foundation).
	var playResp struct {
		VideoList []struct {
			Resolution string `json:"resolution"`
			VideoInfo  struct {
				URL    string `json:"url"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"video_info"`
		} `json:"video_list"`
	}
	playPayload := map[string]any{
		"fid":         fid,
		"resolutions": "normal,low,high,super,2k,4k",
		"supports":    "fmp4,m3u8",
	}
	var playErr error
	if cookies, err := c.doJSON(ctx, http.MethodPost, "/file/v2/play", playPayload, true, &playResp); err == nil {
		hdr := maps.Clone(baseHeaders)
		hdr["Cookie"] = mergeCookies(baseCookie, cookies)
		for _, v := range playResp.VideoList {
			u := strings.TrimSpace(v.VideoInfo.URL)
			if u == "" {
				continue
			}
			q := strings.ToLower(strings.TrimSpace(v.Resolution))
			streams = append(streams, catalog.Stream{
				Label:          transcodeLabel(q, v.VideoInfo.Width, v.VideoInfo.Height),
				Quality:        q,
				URL:            u,
				Headers:        maps.Clone(hdr),
				Filename:       "stream.m3u8",
				NativePlayable: true,
				IsHLS:          true,
			})
		}
	} else {
		playErr = err
	}
	sort.SliceStable(streams, func(i, j int) bool {
		return resolutionRank(streams[i].Quality) > resolutionRank(streams[j].Quality)
	})

	// 2. Fetch original download URL (works directly for MP4/MOV and for full-format backends).
	var dlResp []struct {
		DownloadURL string `json:"download_url"`
		FileName    string `json:"file_name"`
		Size        int64  `json:"size"`
	}
	var dlErr error
	if cookies, err := c.doJSON(ctx, http.MethodPost, "/file/download", map[string]any{"fids": []string{fid}}, true, &dlResp); err == nil && len(dlResp) > 0 {
		dl := dlResp[0]
		if dlURL := strings.TrimSpace(dl.DownloadURL); dlURL != "" {
			if filename == "" {
				filename = dl.FileName
			}
			if size == 0 {
				size = dl.Size
			}
			hdr := maps.Clone(baseHeaders)
			hdr["Cookie"] = mergeCookies(baseCookie, cookies)
			native := IsNativePlayableFilename(filename)
			label := "原画 " + ContainerLabel(filename)
			streams = append(streams, catalog.Stream{
				Label:          label,
				Quality:        "original",
				URL:            dlURL,
				Headers:        hdr,
				Filename:       filename,
				NativePlayable: native,
				IsOriginal:     true,
				Size:           size,
			})
		}
	} else {
		dlErr = err
	}

	if len(streams) == 0 {
		if err := errors.Join(playErr, dlErr); err != nil {
			return catalog.Playback{}, fmt.Errorf("未获取到%s播放地址：%w", c.provider.Label(), err)
		}
		return catalog.Playback{}, fmt.Errorf("未获取到%s播放地址，请检查账号权限或稍后重试", c.provider.Label())
	}

	picked := PickPreferredStream(streams, c.credsFn().EffectiveQuality())
	primary := streams[picked]
	return catalog.Playback{
		URL:      primary.URL,
		Headers:  maps.Clone(primary.Headers),
		Filename: primary.Filename,
		Streams:  streams,
	}, nil
}

func resolutionRank(q string) int {
	switch strings.ToLower(q) {
	case "4k", "uhd":
		return 60
	case "2k", "qhd":
		return 50
	case "super", "fhd", "1080p":
		return 40
	case "high", "hd", "720p":
		return 30
	case "normal", "sd":
		return 20
	case "low":
		return 10
	default:
		return 5
	}
}

func transcodeLabel(q string, w, h int) string {
	switch strings.ToLower(q) {
	case "4k", "uhd":
		return "4K 转码"
	case "2k", "qhd":
		return "2K 转码"
	case "super", "fhd", "1080p":
		return "1080P 转码"
	case "high", "hd", "720p":
		return "720P 转码"
	case "normal", "sd":
		return "标清转码"
	case "low":
		return "流畅转码"
	default:
		if w > 0 && h > 0 {
			return fmt.Sprintf("%dP 转码", h)
		}
		return strings.ToUpper(q) + " 转码"
	}
}

func mergeCookies(base string, extra []*http.Cookie) string {
	if len(extra) == 0 {
		return base
	}
	order := []string{}
	vals := map[string]string{}
	add := func(name, val string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, exists := vals[name]; !exists {
			order = append(order, name)
		}
		vals[name] = strings.TrimSpace(val)
	}
	for _, part := range strings.Split(base, ";") {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			add(k, v)
		}
	}
	for _, ck := range extra {
		if ck != nil && ck.Name != "" {
			add(ck.Name, ck.Value)
		}
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vals[k])
	}
	return strings.Join(out, "; ")
}
