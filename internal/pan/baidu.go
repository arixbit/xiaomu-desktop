package pan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/netproxy"
)

const (
	baiduWebBase    = "https://pan.baidu.com"
	baiduPCSBase    = "https://pcs.baidu.com"
	baiduReferer    = "https://pan.baidu.com/"
	baiduWebUA      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	baiduLocateUA   = "netdisk;P2SP;3.0.10.22;netdisk;11.12.3;PC;PC-Windows"
	baiduDlinkUA    = "pan.baidu.com"
	baiduWebAppID   = "250528"
	baiduCommonArgs = "channel=chunlei&web=1&app_id=250528&clienttype=0"
)

// BaiduClientOptions configures a Baidu Netdisk (百度网盘) client. Tests supply
// WebBase and PCSBase pointing at a local httptest.Server.
type BaiduClientOptions struct {
	WebBase     string
	PCSBase     string
	HTTPClient  *http.Client
	Credentials *Credentials
}

type baiduClient struct {
	webBase    string
	pcsBase    string
	httpClient *http.Client
	credsFn    func() Credentials

	mu           sync.Mutex
	bdstokenKey  string
	bdstoken     string
	bdstokenExp  time.Time
	tempDirKey   string
	tempDirReady bool
	randskCache  map[string]cachedStoken
	recentSaved  []string
}

var (
	sharedBaiduOnce sync.Once
	sharedBaidu     *baiduClient

	baiduShareIDPattern = regexp.MustCompile(`"shareid"\s*:\s*"?(\d+)"?`)
	baiduUKPattern      = regexp.MustCompile(`"uk"\s*:\s*"?(\d+)"?`)
)

func sharedBaiduClient() *baiduClient {
	sharedBaiduOnce.Do(func() {
		sharedBaidu = NewBaiduClient(BaiduClientOptions{})
	})
	return sharedBaidu
}

// NewBaiduClient constructs a native Baidu Netdisk protocol client.
func NewBaiduClient(opt BaiduClientOptions) *baiduClient {
	webBase := baiduWebBase
	if opt.WebBase != "" {
		webBase = strings.TrimRight(opt.WebBase, "/")
	}
	pcsBase := baiduPCSBase
	if opt.PCSBase != "" {
		pcsBase = strings.TrimRight(opt.PCSBase, "/")
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
	return &baiduClient{
		webBase:     webBase,
		pcsBase:     pcsBase,
		httpClient:  hc,
		credsFn:     credsFn,
		randskCache: map[string]cachedStoken{},
	}
}

func (c *baiduClient) cookie() (string, error) {
	cookie := c.credsFn().Cookie(ProviderBaidu)
	if cookie == "" {
		return "", &AuthRequiredError{
			Provider: ProviderBaidu,
			Reason:   "播放百度网盘资源需要先登录账号",
		}
	}
	return cookie, nil
}

func baiduShortURL(shareID string) string {
	s := strings.TrimSpace(shareID)
	if len(s) > 1 && s[0] == '1' {
		return s[1:]
	}
	return s
}

func baiduSharePageURL(shareID string) string {
	s := strings.TrimSpace(shareID)
	if !strings.HasPrefix(s, "1") {
		s = "1" + s
	}
	return baiduWebBase + "/s/" + s
}

func (c *baiduClient) doReq(ctx context.Context, method, fullURL string, form url.Values, referer, ua, extraCookie string, needAuth bool) ([]byte, []*http.Cookie, int, error) {
	var cookie string
	if needAuth {
		var err error
		cookie, err = c.cookie()
		if err != nil {
			return nil, nil, 0, err
		}
	} else {
		cookie = c.credsFn().Cookie(ProviderBaidu)
	}
	if extraCookie != "" {
		if cookie != "" {
			cookie += "; " + extraCookie
		} else {
			cookie = extraCookie
		}
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, nil, 0, err
	}
	if ua == "" {
		ua = baiduWebUA
	}
	if referer == "" {
		referer = baiduReferer
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.Cookies(), resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return raw, resp.Cookies(), resp.StatusCode, &AuthRequiredError{
			Provider: ProviderBaidu,
			Reason:   fmt.Sprintf("百度网盘登录状态无效或已过期（HTTP %d），请重新登录", resp.StatusCode),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, resp.Cookies(), resp.StatusCode, fmt.Errorf("百度网盘接口返回 HTTP %d", resp.StatusCode)
	}
	if needAuth {
		if (resp.Request != nil && strings.Contains(strings.ToLower(resp.Request.URL.Host), "passport.baidu.com")) ||
			strings.HasPrefix(strings.TrimSpace(string(raw)), "<") {
			return raw, resp.Cookies(), resp.StatusCode, &AuthRequiredError{
				Provider: ProviderBaidu,
				Reason:   "百度网盘登录状态无效或已过期，请重新登录",
			}
		}
	}
	return raw, resp.Cookies(), resp.StatusCode, nil
}

func (c *baiduClient) ensureBDSToken(ctx context.Context) (string, error) {
	cookie, err := c.cookie()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.bdstokenKey == cookie && c.bdstoken != "" && time.Now().Before(c.bdstokenExp) {
		tok := c.bdstoken
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	endpoint := c.webBase + "/api/gettemplatevariable?fields=" + url.QueryEscape(`["bdstoken"]`) + "&" + baiduCommonArgs
	raw, _, _, err := c.doReq(ctx, http.MethodGet, endpoint, nil, baiduReferer, baiduWebUA, "", true)
	if err != nil {
		return "", err
	}
	var resp struct {
		Errno  int             `json:"errno"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("解析百度网盘授权状态失败：%w", err)
	}
	if resp.Errno == -6 {
		return "", &AuthRequiredError{
			Provider: ProviderBaidu,
			Reason:   "百度网盘登录状态无效或已过期，请重新登录",
		}
	}
	if resp.Errno != 0 {
		return "", fmt.Errorf("获取百度网盘令牌失败（错误码 %d）", resp.Errno)
	}
	var result struct {
		BDSToken string `json:"bdstoken"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil || strings.TrimSpace(result.BDSToken) == "" {
		return "", &AuthRequiredError{
			Provider: ProviderBaidu,
			Reason:   "百度网盘登录状态无效或已过期，请重新登录",
		}
	}
	tok := strings.TrimSpace(result.BDSToken)
	c.mu.Lock()
	c.bdstokenKey = cookie
	c.bdstoken = tok
	c.bdstokenExp = time.Now().Add(10 * time.Minute)
	c.mu.Unlock()
	return tok, nil
}

// CheckAuth verifies whether the configured Baidu cookie is valid on pan.baidu.com.
func (c *baiduClient) CheckAuth(ctx context.Context) (AuthStatus, error) {
	if _, err := c.ensureBDSToken(ctx); err != nil {
		return AuthStatus{Provider: ProviderBaidu}, err
	}
	return AuthStatus{
		Provider: ProviderBaidu,
		Valid:    true,
		VIP:      "已登录",
		Summary:  "百度网盘已登录",
	}, nil
}

func (c *baiduClient) verifyShare(ctx context.Context, shareID, passcode string) (string, error) {
	passcode = strings.TrimSpace(passcode)
	if passcode == "" {
		return "", nil
	}
	surl := baiduShortURL(shareID)
	key := surl + "\x00" + passcode
	c.mu.Lock()
	if cached, ok := c.randskCache[key]; ok && time.Now().Before(cached.expires) {
		c.mu.Unlock()
		return cached.token, nil
	}
	c.mu.Unlock()

	endpoint := fmt.Sprintf("%s/share/verify?surl=%s&t=%d&%s", c.webBase, url.QueryEscape(surl), time.Now().UnixMilli(), baiduCommonArgs)
	form := url.Values{
		"pwd":       {passcode},
		"vcode":     {""},
		"vcode_str": {""},
	}
	referer := c.webBase + "/share/init?surl=" + url.QueryEscape(surl)
	raw, cookies, _, err := c.doReq(ctx, http.MethodPost, endpoint, form, referer, baiduWebUA, "", false)
	if err != nil {
		return "", err
	}
	var resp struct {
		Errno  int    `json:"errno"`
		Randsk string `json:"randsk"`
		ErrMsg string `json:"err_msg"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("解析百度网盘分享验证响应失败：%w", err)
	}
	if resp.Errno != 0 {
		return "", baiduShareErrno(resp.Errno, resp.ErrMsg)
	}
	randsk := strings.TrimSpace(resp.Randsk)
	if randsk == "" {
		for _, ck := range cookies {
			if strings.EqualFold(ck.Name, "BDCLND") && strings.TrimSpace(ck.Value) != "" {
				randsk = strings.TrimSpace(ck.Value)
				break
			}
		}
	}
	if randsk != "" {
		c.mu.Lock()
		c.randskCache[key] = cachedStoken{token: randsk, expires: time.Now().Add(10 * time.Minute)}
		c.mu.Unlock()
	}
	return randsk, nil
}

func baiduShareErrno(errno int, msg string) error {
	switch errno {
	case -6:
		return &AuthRequiredError{Provider: ProviderBaidu, Reason: "百度网盘登录已失效，请重新登录"}
	case -9, -12:
		return fmt.Errorf("百度网盘分享需要提取码或提取码错误")
	case -62:
		return fmt.Errorf("百度网盘分享触发验证码限制，请稍后重试")
	case -7, 105:
		return fmt.Errorf("百度网盘分享链接已失效或不存在")
	case -31, 111, 31112:
		return fmt.Errorf("百度网盘剩余空间不足，请先清理网盘空间或回收站，或换其他线路")
	default:
		if strings.TrimSpace(msg) != "" {
			return fmt.Errorf("百度网盘返回错误（%d）：%s", errno, msg)
		}
		return fmt.Errorf("百度网盘返回错误码 %d", errno)
	}
}

type baiduFlexID string

func (f *baiduFlexID) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*f = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		*f = baiduFlexID(strings.TrimSpace(str))
		return nil
	}
	*f = baiduFlexID(s)
	return nil
}

type baiduShareEntry struct {
	FsID           baiduFlexID `json:"fs_id"`
	ServerFilename string      `json:"server_filename"`
	Path           string      `json:"path"`
	Size           int64       `json:"size"`
	IsDir          int         `json:"isdir"`
}

func (c *baiduClient) listSharePage(ctx context.Context, surl, randsk, shareID, uk, dir string, root bool, page int) (outShareID, outUK string, list []baiduShareEntry, err error) {
	q := url.Values{
		"web":        {"1"},
		"app_id":     {baiduWebAppID},
		"channel":    {"chunlei"},
		"clienttype": {"0"},
		"shorturl":   {surl},
		"page":       {strconv.Itoa(page)},
		"num":        {"100"},
		"order":      {"name"},
		"desc":       {"0"},
	}
	if root {
		q.Set("root", "1")
	} else {
		q.Set("root", "0")
		q.Set("dir", dir)
	}
	if shareID != "" {
		q.Set("shareid", shareID)
	}
	if uk != "" {
		q.Set("uk", uk)
	}
	extraCookie := ""
	if randsk != "" {
		extraCookie = "BDCLND=" + randsk
	}
	endpoint := c.webBase + "/share/list?" + q.Encode()
	raw, _, _, err := c.doReq(ctx, http.MethodGet, endpoint, nil, baiduSharePageURL(surl), baiduWebUA, extraCookie, false)
	if err != nil {
		return "", "", nil, err
	}
	var resp struct {
		Errno    int               `json:"errno"`
		ErrMsg   string            `json:"err_msg"`
		ShareID  baiduFlexID       `json:"share_id"`
		ShareID2 baiduFlexID       `json:"shareid"`
		UK       baiduFlexID       `json:"uk"`
		List     []baiduShareEntry `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", nil, fmt.Errorf("解析百度网盘分享目录失败：%w", err)
	}
	if resp.Errno != 0 {
		return "", "", nil, baiduShareErrno(resp.Errno, resp.ErrMsg)
	}
	sid := string(resp.ShareID)
	if sid == "" {
		sid = string(resp.ShareID2)
	}
	return sid, string(resp.UK), resp.List, nil
}

func (c *baiduClient) scrapeShareMeta(ctx context.Context, shareID, randsk string) (sid, uk string) {
	extraCookie := ""
	if randsk != "" {
		extraCookie = "BDCLND=" + randsk
	}
	s := strings.TrimSpace(shareID)
	if !strings.HasPrefix(s, "1") {
		s = "1" + s
	}
	raw, _, _, err := c.doReq(ctx, http.MethodGet, c.webBase+"/s/"+url.PathEscape(s), nil, baiduReferer, baiduWebUA, extraCookie, false)
	if err != nil {
		return "", ""
	}
	text := string(raw)
	if m := baiduShareIDPattern.FindStringSubmatch(text); len(m) >= 2 {
		sid = m[1]
	}
	if m := baiduUKPattern.FindStringSubmatch(text); len(m) >= 2 {
		uk = m[1]
	}
	return sid, uk
}

// ExpandShare recursively lists video files inside a Baidu Netdisk share.
func (c *baiduClient) ExpandShare(ctx context.Context, ref ShareRef) ([]FileItem, error) {
	randsk, err := c.verifyShare(ctx, ref.ShareID, ref.Passcode)
	if err != nil {
		return nil, err
	}
	surl := baiduShortURL(ref.ShareID)
	shareID, uk, rootList, err := c.listSharePage(ctx, surl, randsk, "", "", "", true, 1)
	if err != nil {
		return nil, err
	}
	if shareID == "" || uk == "" {
		s2, u2 := c.scrapeShareMeta(ctx, ref.ShareID, randsk)
		if shareID == "" {
			shareID = s2
		}
		if uk == "" {
			uk = u2
		}
	}
	metaToken := ""
	if shareID != "" && uk != "" {
		metaToken = shareID + ":" + uk
	}

	type dirTask struct {
		dir   string
		depth int
	}
	var queue []dirTask
	var files []FileItem
	collect := func(entries []baiduShareEntry, depth int) {
		for _, e := range entries {
			if e.IsDir == 1 {
				if depth+1 < maxShareDepth && e.Path != "" {
					queue = append(queue, dirTask{dir: e.Path, depth: depth + 1})
				}
				continue
			}
			if !IsVideoFilename(e.ServerFilename) || e.FsID == "" {
				continue
			}
			files = append(files, FileItem{
				Provider:      ProviderBaidu,
				ShareID:       ref.ShareID,
				Passcode:      ref.Passcode,
				FileID:        string(e.FsID),
				ShareFidToken: metaToken,
				Name:          e.ServerFilename,
				Size:          e.Size,
			})
			if len(files) >= maxShareFiles {
				return
			}
		}
	}
	collect(rootList, 0)

	visited := 1
	for len(queue) > 0 && visited < maxShareDirs && len(files) < maxShareFiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		visited++
		_, _, subList, subErr := c.listSharePage(ctx, surl, randsk, shareID, uk, cur.dir, false, 1)
		if subErr != nil {
			if len(files) > 0 {
				break
			}
			return nil, subErr
		}
		collect(subList, cur.depth)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("此百度网盘分享中没有找到可播放的视频文件")
	}
	sort.SliceStable(files, func(i, j int) bool {
		return NaturalLess(files[i].Name, files[j].Name)
	})
	return files, nil
}

func (c *baiduClient) tempDirPath() string {
	name := strings.Trim(strings.ReplaceAll(c.credsFn().EffectiveTempDir(), "\\", "/"), "/")
	if name == "" {
		name = DefaultTempDirName
	}
	return "/" + name
}

func (c *baiduClient) ensureTempDir(ctx context.Context, bdstoken string) (string, error) {
	cookie, err := c.cookie()
	if err != nil {
		return "", err
	}
	dirPath := c.tempDirPath()
	cacheKey := cookie + "\x00" + dirPath
	c.mu.Lock()
	if c.tempDirKey == cacheKey && c.tempDirReady {
		c.mu.Unlock()
		return dirPath, nil
	}
	c.mu.Unlock()

	endpoint := fmt.Sprintf("%s/api/create?a=commit&%s&bdstoken=%s", c.webBase, baiduCommonArgs, url.QueryEscape(bdstoken))
	form := url.Values{
		"path":       {dirPath},
		"isdir":      {"1"},
		"block_list": {"[]"},
		"rtype":      {"0"},
	}
	raw, _, _, err := c.doReq(ctx, http.MethodPost, endpoint, form, baiduReferer, baiduWebUA, "", true)
	if err != nil {
		return "", err
	}
	var resp struct {
		Errno int `json:"errno"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("创建百度网盘临时目录失败：%w", err)
	}
	// errno -8 means directory already exists.
	if resp.Errno != 0 && resp.Errno != -8 {
		return "", baiduShareErrno(resp.Errno, "")
	}
	c.mu.Lock()
	c.tempDirKey = cacheKey
	c.tempDirReady = true
	c.mu.Unlock()
	return dirPath, nil
}

func (c *baiduClient) transferShareFile(ctx context.Context, item FileItem, bdstoken, tempDir string) (savedPath, savedFsID string, err error) {
	// Reuse the already-transferred file if identical, otherwise clear older
	// temporary files before saving so large 4K files do not exhaust quota.
	if existing, listErr := c.listDirFiles(ctx, tempDir); listErr == nil && len(existing) > 0 {
		var stale []string
		for _, e := range existing {
			if e.Path == "" {
				continue
			}
			if item.Name != "" && e.ServerFilename == item.Name && (item.Size == 0 || e.Size == item.Size) {
				return e.Path, string(e.FsID), nil
			}
			stale = append(stale, e.Path)
		}
		if len(stale) > 0 {
			_ = c.deletePaths(ctx, stale, bdstoken)
		}
	}

	randsk, err := c.verifyShare(ctx, item.ShareID, item.Passcode)
	if err != nil {
		return "", "", err
	}
	shareID, uk, ok := strings.Cut(item.ShareFidToken, ":")
	if !ok || shareID == "" || uk == "" {
		surl := baiduShortURL(item.ShareID)
		shareID, uk, _, err = c.listSharePage(ctx, surl, randsk, "", "", "", true, 1)
		if err != nil {
			return "", "", err
		}
		if shareID == "" || uk == "" {
			s2, u2 := c.scrapeShareMeta(ctx, item.ShareID, randsk)
			if shareID == "" {
				shareID = s2
			}
			if uk == "" {
				uk = u2
			}
		}
		if shareID == "" || uk == "" {
			return "", "", fmt.Errorf("无法获取百度网盘分享元数据（shareid/uk）")
		}
	}

	endpoint := fmt.Sprintf("%s/share/transfer?shareid=%s&from=%s&ondup=newcopy&async=1&%s&bdstoken=%s",
		c.webBase, url.QueryEscape(shareID), url.QueryEscape(uk), baiduCommonArgs, url.QueryEscape(bdstoken))
	form := url.Values{
		"fsidlist": {"[" + item.FileID + "]"},
		"path":     {tempDir},
	}
	if randsk != "" {
		sekey := randsk
		if unescaped, uerr := url.QueryUnescape(randsk); uerr == nil && unescaped != "" {
			sekey = unescaped
		}
		form.Set("sekey", sekey)
	}
	extraCookie := ""
	if randsk != "" {
		extraCookie = "BDCLND=" + randsk
	}
	raw, _, _, err := c.doReq(ctx, http.MethodPost, endpoint, form, baiduSharePageURL(item.ShareID), baiduWebUA, extraCookie, true)
	if err != nil {
		return "", "", fmt.Errorf("转存百度网盘文件失败：%w", err)
	}
	var resp struct {
		Errno   int             `json:"errno"`
		Extra   json.RawMessage `json:"extra"`
		ShowMsg string          `json:"show_msg"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", fmt.Errorf("解析百度网盘转存结果失败：%w", err)
	}
	if resp.Errno != 0 && resp.Errno != 12 {
		return "", "", baiduShareErrno(resp.Errno, resp.ShowMsg)
	}
	var extra struct {
		List []struct {
			From     string      `json:"from"`
			To       string      `json:"to"`
			ToFsID   baiduFlexID `json:"to_fs_id"`
			FromFsID baiduFlexID `json:"from_fs_id"`
		} `json:"list"`
	}
	if len(resp.Extra) > 0 {
		_ = json.Unmarshal(resp.Extra, &extra)
	}
	if len(extra.List) > 0 {
		savedPath = strings.TrimSpace(extra.List[0].To)
		savedFsID = string(extra.List[0].ToFsID)
	}
	if savedPath == "" && item.Name != "" {
		savedPath = path.Join(tempDir, path.Base(item.Name))
	}
	if savedPath == "" {
		return "", "", fmt.Errorf("转存百度网盘文件未返回目标路径")
	}
	c.rememberSaved(savedPath, bdstoken)
	return savedPath, savedFsID, nil
}

func (c *baiduClient) rememberSaved(filePath, bdstoken string) {
	c.mu.Lock()
	c.recentSaved = append(c.recentSaved, filePath)
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
			_ = c.deletePaths(ctx, evict, bdstoken)
		}()
	}
}

func (c *baiduClient) deletePaths(ctx context.Context, paths []string, bdstoken string) error {
	if len(paths) == 0 {
		return nil
	}
	if bdstoken == "" {
		var err error
		bdstoken, err = c.ensureBDSToken(ctx)
		if err != nil {
			return err
		}
	}
	filelist, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/api/filemanager?opera=delete&async=2&onnest=fail&%s&bdstoken=%s",
		c.webBase, baiduCommonArgs, url.QueryEscape(bdstoken))
	form := url.Values{"filelist": {string(filelist)}}
	_, _, _, err = c.doReq(ctx, http.MethodPost, endpoint, form, baiduReferer, baiduWebUA, "", true)
	return err
}

func (c *baiduClient) listDirFiles(ctx context.Context, dirPath string) ([]baiduShareEntry, error) {
	q := url.Values{
		"dir":        {dirPath},
		"order":      {"time"},
		"desc":       {"1"},
		"num":        {"100"},
		"page":       {"1"},
		"web":        {"1"},
		"app_id":     {baiduWebAppID},
		"channel":    {"chunlei"},
		"clienttype": {"0"},
	}
	raw, _, _, err := c.doReq(ctx, http.MethodGet, c.webBase+"/api/list?"+q.Encode(), nil, baiduReferer, baiduWebUA, "", true)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Errno int               `json:"errno"`
		List  []baiduShareEntry `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.Errno == -9 {
		return nil, nil
	}
	if resp.Errno != 0 {
		return nil, baiduShareErrno(resp.Errno, "")
	}
	return resp.List, nil
}

// CleanupTemp deletes all temporary playback files inside Xiaomu's Baidu Netdisk folder.
func (c *baiduClient) CleanupTemp(ctx context.Context) (int, error) {
	if !c.credsFn().Has(ProviderBaidu) {
		return 0, nil
	}
	bdstoken, err := c.ensureBDSToken(ctx)
	if err != nil {
		return 0, err
	}
	entries, err := c.listDirFiles(ctx, c.tempDirPath())
	if err != nil {
		return 0, err
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Path != "" {
			paths = append(paths, e.Path)
		}
	}
	if len(paths) == 0 {
		return 0, nil
	}
	if err := c.deletePaths(ctx, paths, bdstoken); err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.recentSaved = nil
	c.mu.Unlock()
	return len(paths), nil
}

func (c *baiduClient) locateDownload(ctx context.Context, savedPath string) (string, error) {
	q := url.Values{
		"method":     {"locatedownload"},
		"app_id":     {baiduWebAppID},
		"ver":        {"4.0"},
		"dtype":      {"1"},
		"esl":        {"1"},
		"ehps":       {"0"},
		"check_blue": {"1"},
		"clienttype": {"17"},
		"path":       {savedPath},
	}
	raw, _, _, err := c.doReq(ctx, http.MethodGet, c.pcsBase+"/rest/2.0/pcs/file?"+q.Encode(), nil, baiduReferer, baiduLocateUA, "", true)
	if err != nil {
		return "", err
	}
	var resp struct {
		ErrorCode int    `json:"error_code"`
		ErrorMsg  string `json:"error_msg"`
		URLs      []struct {
			URL string `json:"url"`
		} `json:"urls"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", err
	}
	if resp.ErrorCode != 0 {
		if resp.ErrorCode == 31023 || resp.ErrorCode == 31066 || resp.ErrorCode == 110 {
			return "", fmt.Errorf("百度网盘直链获取失败（%d）：%s", resp.ErrorCode, resp.ErrorMsg)
		}
		return "", fmt.Errorf("百度网盘直链接口返回错误（%d）：%s", resp.ErrorCode, resp.ErrorMsg)
	}
	for _, u := range resp.URLs {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(u.URL)), "https://") {
			return strings.TrimSpace(u.URL), nil
		}
	}
	for _, u := range resp.URLs {
		if s := strings.TrimSpace(u.URL); s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("百度网盘未返回直链地址")
}

func (c *baiduClient) filemetasDlink(ctx context.Context, savedPath, savedFsID string) (dlink, filename string, size int64, err error) {
	if savedFsID == "" {
		entries, listErr := c.listDirFiles(ctx, path.Dir(savedPath))
		if listErr != nil {
			return "", "", 0, listErr
		}
		for _, e := range entries {
			if e.Path == savedPath {
				savedFsID = string(e.FsID)
				break
			}
		}
		if savedFsID == "" && len(entries) > 0 {
			// Match by filename suffix if Baidu renamed duplicate copy.
			base := path.Base(savedPath)
			for _, e := range entries {
				if e.ServerFilename == base || strings.HasPrefix(e.ServerFilename, strings.TrimSuffix(base, path.Ext(base))) {
					savedFsID = string(e.FsID)
					break
				}
			}
		}
		if savedFsID == "" {
			return "", "", 0, fmt.Errorf("未在临时目录中找到已转存的百度网盘文件")
		}
	}
	q := url.Values{
		"dlink":      {"1"},
		"fsids":      {"[" + savedFsID + "]"},
		"web":        {"1"},
		"app_id":     {baiduWebAppID},
		"channel":    {"chunlei"},
		"clienttype": {"0"},
	}
	raw, _, _, err := c.doReq(ctx, http.MethodGet, c.webBase+"/api/filemetas?"+q.Encode(), nil, baiduReferer, baiduWebUA, "", true)
	if err != nil {
		return "", "", 0, err
	}
	var resp struct {
		Errno int `json:"errno"`
		Info  []struct {
			Filename string `json:"filename"`
			Dlink    string `json:"dlink"`
			Size     int64  `json:"size"`
		} `json:"info"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", 0, err
	}
	if resp.Errno != 0 {
		return "", "", 0, baiduShareErrno(resp.Errno, "")
	}
	if len(resp.Info) == 0 || strings.TrimSpace(resp.Info[0].Dlink) == "" {
		return "", "", 0, fmt.Errorf("百度网盘 filemetas 未返回原画直链")
	}
	return strings.TrimSpace(resp.Info[0].Dlink), resp.Info[0].Filename, resp.Info[0].Size, nil
}

// ResolvePlayback transfers the shared file into the temp folder and returns its
// original direct stream (no low-bitrate cloud transcoding).
func (c *baiduClient) ResolvePlayback(ctx context.Context, item FileItem) (catalog.Playback, error) {
	cookie, err := c.cookie()
	if err != nil {
		return catalog.Playback{}, err
	}
	bdstoken, err := c.ensureBDSToken(ctx)
	if err != nil {
		return catalog.Playback{}, err
	}
	tempDir, err := c.ensureTempDir(ctx, bdstoken)
	if err != nil {
		return catalog.Playback{}, err
	}
	savedPath, savedFsID, err := c.transferShareFile(ctx, item, bdstoken, tempDir)
	if err != nil {
		return catalog.Playback{}, err
	}

	filename := item.Name
	if filename == "" {
		filename = path.Base(savedPath)
	}
	size := item.Size

	var streamURL, streamUA string
	var locateErr, metaErr error

	if u, err := c.locateDownload(ctx, savedPath); err == nil && u != "" {
		streamURL = u
		streamUA = baiduLocateUA
	} else {
		locateErr = err
		if dlink, metaName, metaSize, err2 := c.filemetasDlink(ctx, savedPath, savedFsID); err2 == nil && dlink != "" {
			streamURL = dlink
			streamUA = baiduDlinkUA
			if filename == "" {
				filename = metaName
			}
			if size == 0 {
				size = metaSize
			}
		} else {
			metaErr = err2
		}
	}

	if streamURL == "" {
		return catalog.Playback{}, fmt.Errorf("未获取到百度网盘原画播放地址：%w", errors.Join(locateErr, metaErr))
	}

	headers := map[string]string{
		"User-Agent": streamUA,
		"Referer":    baiduReferer,
		"Cookie":     cookie,
	}
	stream := catalog.Stream{
		Label:          "原画 " + ContainerLabel(filename),
		Quality:        "original",
		URL:            streamURL,
		Headers:        headers,
		Filename:       filename,
		NativePlayable: IsNativePlayableFilename(filename),
		IsOriginal:     true,
		Size:           size,
	}
	return catalog.Playback{
		URL:      stream.URL,
		Headers:  maps.Clone(headers),
		Filename: stream.Filename,
		Streams:  []catalog.Stream{stream},
	}, nil
}
