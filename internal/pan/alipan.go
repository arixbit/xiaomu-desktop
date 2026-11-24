package pan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/netproxy"
)

const (
	aliAPIBase  = "https://api.aliyundrive.com"
	aliAuthBase = "https://auth.aliyundrive.com"
	aliReferer  = "https://www.alipan.com/"
	aliUA       = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

// AliClientOptions configures an Alipan (阿里云盘) client. Tests supply
// APIBase and AuthBase pointing at a local httptest.Server.
type AliClientOptions struct {
	APIBase     string
	AuthBase    string
	HTTPClient  *http.Client
	Credentials *Credentials
}

type aliClient struct {
	apiBase    string
	authBase   string
	httpClient *http.Client
	credsFn    func() Credentials

	mu           sync.Mutex
	refreshToken string
	accessToken  string
	driveID      string
	tokenExpiry  time.Time
	shareTokens  map[string]cachedStoken
}

var (
	sharedAliOnce sync.Once
	sharedAli     *aliClient
)

func sharedAliClient() *aliClient {
	sharedAliOnce.Do(func() {
		sharedAli = NewAliClient(AliClientOptions{})
	})
	return sharedAli
}

// NewAliClient constructs a native Alipan protocol client.
func NewAliClient(opt AliClientOptions) *aliClient {
	apiBase := aliAPIBase
	if opt.APIBase != "" {
		apiBase = strings.TrimRight(opt.APIBase, "/")
	}
	authBase := aliAuthBase
	if opt.AuthBase != "" {
		authBase = strings.TrimRight(opt.AuthBase, "/")
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
	return &aliClient{
		apiBase:     apiBase,
		authBase:    authBase,
		httpClient:  hc,
		credsFn:     credsFn,
		shareTokens: map[string]cachedStoken{},
	}
}

func (c *aliClient) ensureAccessToken(ctx context.Context) (accessToken, driveID string, err error) {
	creds := c.credsFn()
	rt := strings.TrimSpace(creds.AliToken)
	if rt == "" && strings.TrimSpace(creds.AliAccessToken) == "" {
		return "", "", &AuthRequiredError{
			Provider: ProviderAli,
			Reason:   "播放阿里云盘资源需要先登录阿里云盘账号",
		}
	}

	c.mu.Lock()
	if c.refreshToken == rt && c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		at, did := c.accessToken, c.driveID
		c.mu.Unlock()
		return at, did, nil
	}
	c.mu.Unlock()

	if rt == "" {
		return strings.TrimSpace(creds.AliAccessToken), strings.TrimSpace(creds.AliDriveID), nil
	}

	payload, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": rt,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authBase+"/v2/account/token", bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", aliUA)
	req.Header.Set("Referer", aliReferer)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest {
		return "", "", &AuthRequiredError{
			Provider: ProviderAli,
			Reason:   "阿里云盘登录授权已过期，请重新登录",
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("阿里云盘刷新授权失败：HTTP %d", resp.StatusCode)
	}
	var tokenResp struct {
		AccessToken    string `json:"access_token"`
		RefreshToken   string `json:"refresh_token"`
		DefaultDriveID string `json:"default_drive_id"`
		ExpiresIn      int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tokenResp); err != nil {
		return "", "", err
	}
	if tokenResp.AccessToken == "" {
		return "", "", &AuthRequiredError{
			Provider: ProviderAli,
			Reason:   "阿里云盘授权无效，请重新登录",
		}
	}
	ttl := time.Duration(max(tokenResp.ExpiresIn-120, 300)) * time.Second
	did := tokenResp.DefaultDriveID
	if did == "" {
		did = creds.AliDriveID
	}
	c.mu.Lock()
	c.refreshToken = rt
	c.accessToken = tokenResp.AccessToken
	c.driveID = did
	c.tokenExpiry = time.Now().Add(ttl)
	c.mu.Unlock()
	return tokenResp.AccessToken, did, nil
}

func (c *aliClient) postJSON(ctx context.Context, endpoint string, body any, headers map[string]string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", aliUA)
	req.Header.Set("Referer", aliReferer)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &AuthRequiredError{
			Provider: ProviderAli,
			Reason:   fmt.Sprintf("阿里云盘登录状态无效或权限不足（HTTP %d），请重新登录", resp.StatusCode),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errDoc struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &errDoc) == nil && errDoc.Message != "" {
			return fmt.Errorf("阿里云盘返回错误：%s", errDoc.Message)
		}
		return fmt.Errorf("阿里云盘返回 HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *aliClient) CheckAuth(ctx context.Context) (AuthStatus, error) {
	_, _, err := c.ensureAccessToken(ctx)
	if err != nil {
		return AuthStatus{Provider: ProviderAli}, err
	}
	return AuthStatus{
		Provider: ProviderAli,
		Valid:    true,
		VIP:      "已登录",
		Summary:  "阿里云盘已登录",
	}, nil
}

func (c *aliClient) shareToken(ctx context.Context, shareID, passcode string) (string, error) {
	key := shareID + "\x00" + passcode
	c.mu.Lock()
	if cached, ok := c.shareTokens[key]; ok && time.Now().Before(cached.expires) {
		c.mu.Unlock()
		return cached.token, nil
	}
	c.mu.Unlock()

	var resp struct {
		ShareToken string `json:"share_token"`
	}
	err := c.postJSON(ctx, "/v2/share_link/get_share_token", map[string]string{
		"share_id":  shareID,
		"share_pwd": passcode,
	}, nil, &resp)
	if err != nil {
		return "", err
	}
	if resp.ShareToken == "" {
		return "", fmt.Errorf("未获取到阿里云盘分享令牌，分享可能已失效或需要提取码")
	}
	c.mu.Lock()
	c.shareTokens[key] = cachedStoken{token: resp.ShareToken, expires: time.Now().Add(10 * time.Minute)}
	c.mu.Unlock()
	return resp.ShareToken, nil
}

func (c *aliClient) ExpandShare(ctx context.Context, ref ShareRef) ([]FileItem, error) {
	stoken, err := c.shareToken(ctx, ref.ShareID, ref.Passcode)
	if err != nil {
		return nil, err
	}
	type dirTask struct {
		fid   string
		depth int
	}
	queue := []dirTask{{fid: "root", depth: 0}}
	visited := 0
	var files []FileItem

	for len(queue) > 0 && visited < maxShareDirs && len(files) < maxShareFiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		visited++

		var resp struct {
			Items []struct {
				FileID string `json:"file_id"`
				Name   string `json:"name"`
				Type   string `json:"type"`
				Size   int64  `json:"size"`
			} `json:"items"`
		}
		err := c.postJSON(ctx, "/adrive/v3/file/list", map[string]any{
			"share_id":        ref.ShareID,
			"parent_file_id":  cur.fid,
			"limit":           100,
			"order_by":        "name",
			"order_direction": "ASC",
		}, map[string]string{"x-share-token": stoken}, &resp)
		if err != nil {
			if len(files) > 0 {
				break
			}
			return nil, err
		}
		for _, item := range resp.Items {
			if item.Type == "folder" {
				if cur.depth+1 < maxShareDepth && item.FileID != "" {
					queue = append(queue, dirTask{fid: item.FileID, depth: cur.depth + 1})
				}
				continue
			}
			if !IsVideoFilename(item.Name) {
				continue
			}
			files = append(files, FileItem{
				Provider: ProviderAli,
				ShareID:  ref.ShareID,
				Passcode: ref.Passcode,
				FileID:   item.FileID,
				Name:     item.Name,
				Size:     item.Size,
			})
			if len(files) >= maxShareFiles {
				break
			}
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("此阿里云盘分享中没有找到可播放的视频文件")
	}
	sort.SliceStable(files, func(i, j int) bool {
		return NaturalLess(files[i].Name, files[j].Name)
	})
	return files, nil
}

func (c *aliClient) ResolvePlayback(ctx context.Context, item FileItem) (catalog.Playback, error) {
	accessToken, _, err := c.ensureAccessToken(ctx)
	if err != nil {
		return catalog.Playback{}, err
	}
	stoken, err := c.shareToken(ctx, item.ShareID, item.Passcode)
	if err != nil {
		return catalog.Playback{}, err
	}
	var preview struct {
		VideoPreviewPlayInfo struct {
			LiveTranscodingTaskList []struct {
				TemplateID string `json:"template_id"`
				Status     string `json:"status"`
				URL        string `json:"url"`
			} `json:"live_transcoding_task_list"`
		} `json:"video_preview_play_info"`
	}
	err = c.postJSON(ctx, "/v2/file/get_share_video_preview_play_info", map[string]any{
		"share_id":       item.ShareID,
		"file_id":        item.FileID,
		"category":       "live_transcoding",
		"template_id":    "",
		"get_preview_url": true,
	}, map[string]string{
		"Authorization": "Bearer " + accessToken,
		"x-share-token": stoken,
	}, &preview)
	if err != nil {
		return catalog.Playback{}, err
	}
	baseHeaders := map[string]string{
		"User-Agent": aliUA,
		"Referer":    aliReferer,
	}
	var streams []catalog.Stream
	for _, task := range preview.VideoPreviewPlayInfo.LiveTranscodingTaskList {
		u := strings.TrimSpace(task.URL)
		if u == "" {
			continue
		}
		q := strings.ToLower(strings.TrimSpace(task.TemplateID))
		streams = append(streams, catalog.Stream{
			Label:          transcodeLabel(q, 0, 0),
			Quality:        q,
			URL:            u,
			Headers:        maps.Clone(baseHeaders),
			Filename:       "stream.m3u8",
			NativePlayable: true,
			IsHLS:          true,
		})
	}
	sort.SliceStable(streams, func(i, j int) bool {
		return resolutionRank(streams[i].Quality) > resolutionRank(streams[j].Quality)
	})
	if len(streams) == 0 {
		return catalog.Playback{}, fmt.Errorf("未获取到阿里云盘转码播放地址，请稍后重试或确认账号权限")
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
