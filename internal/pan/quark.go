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

func (c *quarkClient) ensureTempDir(ctx context.Context) (fid string, createdNew bool, err error) {
	creds := c.credsFn()
	dirName := creds.EffectiveTempDir()
	cookie, err := c.cookie()
	if err != nil {
		return "", false, err
	}
	cacheKey := cookie + "\x00" + dirName
	c.mu.Lock()
	if c.tempDirKey == cacheKey && c.tempDirFid != "" {
		fid = c.tempDirFid
		c.mu.Unlock()
		return fid, false, nil
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
		return "", false, err
	}
	for _, entry := range root.List {
		if (entry.Dir || entry.FileType == 0) && entry.FileName == dirName && entry.Fid != "" {
			c.mu.Lock()
			c.tempDirKey, c.tempDirFid = cacheKey, entry.Fid
			c.mu.Unlock()
			return entry.Fid, false, nil
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
		return "", false, fmt.Errorf("创建临时目录 %q 失败：%w", dirName, err)
	}
	if created.Fid == "" {
		return "", false, fmt.Errorf("创建临时目录 %q 未返回目录 ID", dirName)
	}
	c.mu.Lock()
	c.tempDirKey, c.tempDirFid = cacheKey, created.Fid
	c.mu.Unlock()
	return created.Fid, true, nil
}

type quarkTempFile struct {
	Fid      string `json:"fid"`
	FileName string `json:"file_name"`
	Size     int64  `json:"size"`
}

func (c *quarkClient) listTempFiles(ctx context.Context, dirFid string) ([]quarkTempFile, error) {
	var listing struct {
		List []quarkTempFile `json:"list"`
	}
	endpoint := fmt.Sprintf("/file/sort?pdir_fid=%s&_page=1&_size=100", url.QueryEscape(dirFid))
	if _, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, true, &listing); err != nil {
		return nil, err
	}
	return listing.List, nil
}

func (c *quarkClient) saveShareFile(ctx context.Context, item FileItem) (string, error) {
	tempDirFid, createdNew, err := c.ensureTempDir(ctx)
	if err != nil {
		return "", err
	}
	cleanedBeforeSave := false
	if !createdNew {
		if existing, listErr := c.listTempFiles(ctx, tempDirFid); listErr == nil && len(existing) > 0 {
			var stale []string
			for _, f := range existing {
				if f.Fid == "" {
					continue
				}
				if item.Name != "" && f.FileName == item.Name && (item.Size == 0 || f.Size == item.Size) {
					return f.Fid, nil
				}
				stale = append(stale, f.Fid)
			}
			if len(stale) > 0 {
				if c.deleteAndPurge(ctx, stale) == nil {
					cleanedBeforeSave = true
				}
			}
		}
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
	attemptSave := func() (string, error) {
		var saveResp struct {
			TaskID string `json:"task_id"`
			SaveAs struct {
				SaveAsTopFids []string `json:"save_as_top_fids"`
			} `json:"save_as"`
		}
		if _, err := c.doJSON(ctx, http.MethodPost, "/share/sharepage/save", payload, true, &saveResp); err != nil {
			return "", err
		}
		if len(saveResp.SaveAs.SaveAsTopFids) > 0 {
			return saveResp.SaveAs.SaveAsTopFids[0], nil
		}
		if saveResp.TaskID != "" {
			return c.waitSaveTask(ctx, saveResp.TaskID)
		}
		return "", fmt.Errorf("转存%s文件未返回新文件 ID", c.provider.Label())
	}

	savedFid, err := attemptSave()
	if err != nil && isCapacityLimitErr(err) {
		// Re-check and purge any remaining temp files (and give Quark quota
		// accounting a brief moment to catch up after deletion) before failing.
		if !cleanedBeforeSave {
			_, _ = c.CleanupTemp(ctx)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		savedFid, err = attemptSave()
	}
	if err != nil {
		if isCapacityLimitErr(err) {
			return "", formatCapacityLimitErr(c.provider, item.Size)
		}
		return "", fmt.Errorf("转存%s文件失败：%w", c.provider.Label(), err)
	}
	if savedFid == "" {
		return "", fmt.Errorf("转存%s文件未返回新文件 ID", c.provider.Label())
	}
	return savedFid, nil
}

func isCapacityLimitErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "capacity limit") ||
		strings.Contains(msg, "space not enough") ||
		strings.Contains(msg, "容量不足") ||
		strings.Contains(msg, "空间不足")
}

func formatCapacityLimitErr(p Provider, size int64) error {
	sizeHint := ""
	if size > 0 {
		sizeHint = fmt.Sprintf("（本集约 %s）", formatByteSize(size))
	}
	return fmt.Errorf("%s剩余空间不足%s。请先到%s清空回收站或释放空间，也可换体积更小的版本或其他网盘线路", p.Label(), sizeHint, p.Label())
}

func formatByteSize(size int64) string {
	const (
		gb = 1024 * 1024 * 1024
		mb = 1024 * 1024
	)
	if size >= gb {
		return fmt.Sprintf("%.1f GB", float64(size)/float64(gb))
	}
	return fmt.Sprintf("%.0f MB", float64(size)/float64(mb))
}

func (c *quarkClient) waitSaveTask(ctx context.Context, taskID string) (string, error) {
	deadline := time.Now().Add(15 * time.Second)
	for retry := 0; retry < 30 && time.Now().Before(deadline); retry++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var taskResp struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
			SaveAs  struct {
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
		if taskResp.Status == 3 {
			if msg := strings.TrimSpace(taskResp.Message); msg != "" {
				return "", fmt.Errorf("%s返回错误：%s", c.provider.Label(), msg)
			}
			return "", fmt.Errorf("转存%s文件失败", c.provider.Label())
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(350 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("转存%s文件超时，请重试", c.provider.Label())
}

func (c *quarkClient) waitTaskDone(ctx context.Context, taskID string) {
	if taskID == "" {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for retry := 0; retry < 12 && time.Now().Before(deadline); retry++ {
		if ctx.Err() != nil {
			return
		}
		var taskResp struct {
			Status int `json:"status"`
		}
		endpoint := fmt.Sprintf("/task?task_id=%s&retry_index=%d", url.QueryEscape(taskID), retry)
		if _, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, true, &taskResp); err != nil {
			return
		}
		if taskResp.Status == 2 || taskResp.Status == 3 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
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
	var resp struct {
		TaskID string `json:"task_id"`
	}
	if _, err := c.doJSON(ctx, http.MethodPost, "/file/delete", payload, true, &resp); err != nil {
		return err
	}
	if resp.TaskID != "" {
		c.waitTaskDone(ctx, resp.TaskID)
	}
	return nil
}

// purgeRecycleFids permanently deletes ONLY the specified temporary file IDs
// from the cloud drive recycle bin so quota is released immediately without
// ever touching the user's own unrelated recycle bin items.
func (c *quarkClient) purgeRecycleFids(ctx context.Context, fids []string) {
	if len(fids) == 0 {
		return
	}
	fidSet := make(map[string]bool, len(fids))
	for _, f := range fids {
		if f != "" {
			fidSet[f] = true
		}
	}
	var listResp struct {
		List []struct {
			Fid      string `json:"fid"`
			RecordID string `json:"record_id"`
		} `json:"list"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/file/recycle/list?_page=1&_size=50&_set_pdir_fid=0", nil, true, &listResp); err != nil {
		return
	}
	var records []string
	for _, entry := range listResp.List {
		if fidSet[entry.Fid] && entry.RecordID != "" {
			records = append(records, entry.RecordID)
		}
	}
	if len(records) == 0 {
		return
	}
	_, _ = c.doJSON(ctx, http.MethodPost, "/file/recycle/remove", map[string]any{
		"select_mode": 2,
		"record_list": records,
	}, true, nil)
}

func (c *quarkClient) deleteAndPurge(ctx context.Context, fids []string) error {
	if err := c.deleteFiles(ctx, fids); err != nil {
		return err
	}
	c.purgeRecycleFids(ctx, fids)
	return nil
}

// findTempDirFresh resolves the configured temporary folder directly from the
// drive root without trusting any cached fid, so renaming or moving a previous
// folder can never cause CleanupTemp to delete files from a renamed folder.
func (c *quarkClient) findTempDirFresh(ctx context.Context) (fid string, found bool, err error) {
	creds := c.credsFn()
	dirName := creds.EffectiveTempDir()
	cookie, err := c.cookie()
	if err != nil {
		return "", false, err
	}
	cacheKey := cookie + "\x00" + dirName
	var root struct {
		List []struct {
			Fid      string `json:"fid"`
			FileName string `json:"file_name"`
			Dir      bool   `json:"dir"`
			FileType int    `json:"file_type"`
		} `json:"list"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/file/sort?pdir_fid=0&_page=1&_size=100&_sort=file_type:asc", nil, true, &root); err != nil {
		return "", false, err
	}
	for _, entry := range root.List {
		if (entry.Dir || entry.FileType == 0) && entry.FileName == dirName && entry.Fid != "" {
			c.mu.Lock()
			c.tempDirKey, c.tempDirFid = cacheKey, entry.Fid
			c.mu.Unlock()
			return entry.Fid, true, nil
		}
	}
	c.mu.Lock()
	if c.tempDirKey == cacheKey {
		c.tempDirKey, c.tempDirFid = "", ""
	}
	c.mu.Unlock()
	return "", false, nil
}

// CleanupTemp removes all files saved inside Xiaomu's temporary playback folder
// and permanently purges those deleted temp files from the recycle bin.
func (c *quarkClient) CleanupTemp(ctx context.Context) (int, error) {
	if !c.credsFn().Has(c.provider) {
		return 0, nil
	}
	dirFid, found, err := c.findTempDirFresh(ctx)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	entries, err := c.listTempFiles(ctx, dirFid)
	if err != nil {
		return 0, err
	}
	fids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Fid != "" {
			fids = append(fids, entry.Fid)
		}
	}
	if len(fids) == 0 {
		return 0, nil
	}
	if err := c.deleteAndPurge(ctx, fids); err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.recentSaved = nil
	c.mu.Unlock()
	return len(fids), nil
}

// ResolvePlayback saves a shared file to the temp directory and fetches its
// original direct stream (falling back to HD+ transcoded HLS only when the
// original file is unavailable or cannot be played natively on this platform).
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

	// 1. Always fetch the original file direct URL first: cloud-drive playback
	// is for watching the original file (4K HDR, original audio & subtitles).
	var dlResp []struct {
		DownloadURL string `json:"download_url"`
		FileName    string `json:"file_name"`
		Size        int64  `json:"size"`
	}
	var dlErr error
	var originalStream *catalog.Stream
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
			stream := catalog.Stream{
				Label:          "原画 " + ContainerLabel(filename),
				Quality:        "original",
				URL:            dlURL,
				Headers:        hdr,
				Filename:       filename,
				NativePlayable: native,
				IsOriginal:     true,
				Size:           size,
			}
			originalStream = &stream
			if native {
				return catalog.Playback{
					URL:      stream.URL,
					Headers:  maps.Clone(stream.Headers),
					Filename: stream.Filename,
					Streams:  []catalog.Stream{stream},
				}, nil
			}
		}
	} else {
		dlErr = err
	}

	// 2. Fallback only when the original URL failed or the current OS backend
	// cannot play the container natively (e.g. MKV on Windows before libmpv).
	// Never request low/normal ("流畅转码" / "标清转码").
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
		"resolutions": "high,super,2k,4k",
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
			if isLowTranscodeQuality(q) {
				continue
			}
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
	if originalStream != nil {
		streams = append(streams, *originalStream)
	}

	if len(streams) == 0 {
		if err := errors.Join(dlErr, playErr); err != nil {
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

func isLowTranscodeQuality(q string) bool {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "low", "ld", "normal", "sd":
		return true
	default:
		return false
	}
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
