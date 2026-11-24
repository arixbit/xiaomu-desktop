package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/egoist/mygo"
)

type panLoginSession struct {
	mu       sync.Mutex
	provider pan.Provider
	win      *mygo.Window
	cancel   context.CancelFunc
}

type panLoginSpec struct {
	loginURL     string
	cookieDomain string
	useLocalAuth bool // Alipan stores token JSON in localStorage
	readyCookie  func(cookies []mygo.Cookie) bool
}

func loginSpecFor(p pan.Provider) (panLoginSpec, bool) {
	switch p {
	case pan.ProviderQuark:
		return panLoginSpec{
			loginURL:     "https://pan.quark.cn/",
			cookieDomain: "quark.cn",
			readyCookie: func(cookies []mygo.Cookie) bool {
				return hasCookie(cookies, "__pus")
			},
		}, true
	case pan.ProviderUC:
		return panLoginSpec{
			loginURL:     "https://drive.uc.cn/",
			cookieDomain: "uc.cn",
			readyCookie: func(cookies []mygo.Cookie) bool {
				return hasCookie(cookies, "__pus") || hasCookie(cookies, "uc_session") || hasCookie(cookies, "kps")
			},
		}, true
	case pan.ProviderAli:
		return panLoginSpec{
			loginURL:     "https://www.alipan.com/",
			useLocalAuth: true,
		}, true
	case pan.ProviderBaidu:
		return panLoginSpec{
			loginURL:     "https://pan.baidu.com/",
			cookieDomain: "baidu.com",
			readyCookie: func(cookies []mygo.Cookie) bool {
				return hasCookie(cookies, "BDUSS") && hasCookieForHost(cookies, "STOKEN", "pan.baidu.com")
			},
		}, true
	case pan.Provider115:
		return panLoginSpec{
			loginURL:     "https://115.com/",
			cookieDomain: "115.com",
			readyCookie: func(cookies []mygo.Cookie) bool {
				return hasCookie(cookies, "UID") && hasCookie(cookies, "CID") && hasCookie(cookies, "SEID")
			},
		}, true
	default:
		return panLoginSpec{}, false
	}
}

func hasCookie(cookies []mygo.Cookie, name string) bool {
	for _, c := range cookies {
		if strings.EqualFold(c.Name, name) && strings.TrimSpace(c.Value) != "" {
			return true
		}
	}
	return false
}

func cookieMatchesHost(domain, host string) bool {
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	h := strings.ToLower(strings.TrimSpace(host))
	if d == "" || h == "" {
		return false
	}
	return h == d || strings.HasSuffix(h, "."+d)
}

func hasCookieForHost(cookies []mygo.Cookie, name, host string) bool {
	for _, c := range cookies {
		if strings.EqualFold(c.Name, name) && strings.TrimSpace(c.Value) != "" && cookieMatchesHost(c.Domain, host) {
			return true
		}
	}
	return false
}

func formatCookieHeader(cookies []mygo.Cookie) string {
	return formatCookiesForHost(cookies, "")
}

// formatCookiesForHost serializes cookies applicable to host, giving exact-host
// cookies precedence over parent-domain cookies (critical for pan.baidu.com's
// STOKEN vs passport.baidu.com's STOKEN).
func formatCookiesForHost(cookies []mygo.Cookie, host string) string {
	seen := map[string]string{}
	specificity := map[string]int{}
	order := make([]string, 0, len(cookies))
	for _, c := range cookies {
		name := strings.TrimSpace(c.Name)
		val := strings.TrimSpace(c.Value)
		if name == "" || val == "" {
			continue
		}
		if host != "" && !cookieMatchesHost(c.Domain, host) {
			continue
		}
		spec := len(strings.TrimPrefix(strings.TrimSpace(c.Domain), "."))
		if _, ok := seen[name]; !ok {
			order = append(order, name)
			seen[name] = val
			specificity[name] = spec
			continue
		}
		if spec >= specificity[name] {
			seen[name] = val
			specificity[name] = spec
		}
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, name+"="+seen[name])
	}
	return strings.Join(parts, "; ")
}

func parseAlipanLocalStorageToken(raw string) (refreshToken, accessToken, driveID string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "", "", "", false
	}
	var doc struct {
		RefreshToken   string `json:"refresh_token"`
		AccessToken    string `json:"access_token"`
		DefaultDriveID string `json:"default_drive_id"`
	}
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return "", "", "", false
	}
	doc.RefreshToken = strings.TrimSpace(doc.RefreshToken)
	doc.AccessToken = strings.TrimSpace(doc.AccessToken)
	doc.DefaultDriveID = strings.TrimSpace(doc.DefaultDriveID)
	if doc.RefreshToken == "" && doc.AccessToken == "" {
		return "", "", "", false
	}
	return doc.RefreshToken, doc.AccessToken, doc.DefaultDriveID, true
}

// promptPanLogin shows the interactive login prompt and remembers what action
// to resume automatically once the user finishes signing in.
func (a *app) promptPanLogin(provider pan.Provider, reason string, onSuccess func()) {
	a.panUI.promptProvider = provider
	a.panUI.promptReason = strings.TrimSpace(reason)
	if a.panUI.promptReason == "" {
		a.panUI.promptReason = fmt.Sprintf("播放此%s资源需要先登录账号。", provider.Label())
	}
	a.panUI.onAuthSuccess = onSuccess
	a.panUI.promptOpen = true
}

// beginPanWebLogin opens an isolated native browser window to the cloud drive's
// official login page, polls for valid authorization, persists it locally, closes
// the login window and runs onSuccess automatically.
func (a *app) beginPanWebLogin(provider pan.Provider, onSuccess func()) {
	a.panUI.promptOpen = false
	if a.startPanLogin != nil {
		a.startPanLogin(provider, onSuccess)
		return
	}
	spec, ok := loginSpecFor(provider)
	if !ok {
		a.status = fmt.Sprintf("暂不支持 %s 一键登录", provider.Label())
		return
	}
	if a.window == nil {
		a.status = "打开登录窗口需要运行桌面界面"
		return
	}

	a.closePanLoginWindow()

	session := mygo.NewEphemeralSession()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:  "登录" + provider.Label() + " · 登录成功后将自动关闭并继续播放",
		URL:    spec.loginURL,
		Width:  1040,
		Height: 740,
		Parent: a.window,
		Page: mygo.PageOptions{
			Untrusted: true,
			Session:   session,
		},
	})
	ls := &panLoginSession{provider: provider, win: win, cancel: cancel}
	a.panLogin = ls
	a.status = fmt.Sprintf("请在弹出的%s官方页面完成登录，成功后将自动返回…", provider.Label())

	win.OnClosed(func() {
		cancel()
		if a.dispatch != nil {
			a.dispatch(func() {
				if a.panLogin == ls {
					a.panLogin = nil
				}
			})
		}
	})

	go a.watchPanLoginWindow(ctx, ls, session, spec, onSuccess)
}

func (a *app) watchPanLoginWindow(ctx context.Context, ls *panLoginSession, session *mygo.Session, spec panLoginSpec, onSuccess func()) {
	ticker := time.NewTicker(1200 * time.Millisecond)
	defer ticker.Stop()
	lastCandidate := ""

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if a.closed {
			return
		}

		var updated pan.Credentials
		var candidateKey string
		found := false

		if spec.useLocalAuth {
			page := ls.win.Page()
			if page == nil {
				continue
			}
			val, err := page.EvalContext(ctx, "localStorage.getItem('token') || ''")
			if err != nil {
				continue
			}
			raw, _ := val.(string)
			rt, at, did, ok := parseAlipanLocalStorageToken(raw)
			if !ok {
				continue
			}
			candidateKey = rt + "|" + at
			if candidateKey == lastCandidate {
				continue
			}
			lastCandidate = candidateKey
			updated = pan.DefaultCredentials()
			updated.AliToken = rt
			updated.AliAccessToken = at
			updated.AliDriveID = did
			// Validate with Alipan API before accepting.
			checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			client := pan.NewAliClient(pan.AliClientOptions{Credentials: &updated})
			_, err = client.CheckAuth(checkCtx)
			cancel()
			if err != nil {
				continue
			}
			found = true
		} else {
			cookies, err := session.Cookies.List(mygo.CookieFilter{Domain: spec.cookieDomain})
			if err != nil || len(cookies) == 0 {
				continue
			}
			if spec.readyCookie != nil && !spec.readyCookie(cookies) {
				continue
			}
			cookieHost := ""
			if ls.provider == pan.ProviderBaidu {
				cookieHost = "pan.baidu.com"
			}
			cookieStr := formatCookiesForHost(cookies, cookieHost)
			if cookieStr == "" || cookieStr == lastCandidate {
				continue
			}
			lastCandidate = cookieStr
			updated = pan.DefaultCredentials()
			switch ls.provider {
			case pan.ProviderQuark:
				updated.QuarkCookie = cookieStr
				checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				_, err = pan.NewQuarkClient(pan.ClientOptions{Provider: pan.ProviderQuark, Credentials: &updated}).CheckAuth(checkCtx)
				cancel()
				if err != nil {
					continue
				}
			case pan.ProviderUC:
				updated.UCCookie = cookieStr
				checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				_, err = pan.NewQuarkClient(pan.ClientOptions{Provider: pan.ProviderUC, Credentials: &updated}).CheckAuth(checkCtx)
				cancel()
				if err != nil {
					continue
				}
			case pan.ProviderBaidu:
				updated.BaiduCookie = cookieStr
				checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				_, err = pan.NewBaiduClient(pan.BaiduClientOptions{Credentials: &updated}).CheckAuth(checkCtx)
				cancel()
				if err != nil {
					continue
				}
			case pan.Provider115:
				updated.P115Cookie = cookieStr
			}
			found = true
		}

		if !found {
			continue
		}

		finish := func() {
			if a.closed {
				return
			}
			a.applyAuthorizedCredentials(ls.provider, updated)
			a.closePanLoginWindow()
			if onSuccess != nil {
				onSuccess()
			}
		}
		if a.dispatch != nil {
			a.dispatch(finish)
		} else {
			finish()
		}
		return
	}
}

func (a *app) applyAuthorizedCredentials(provider pan.Provider, updated pan.Credentials) {
	a.panUI.creds = updated
	pan.SetDefaultCredentials(updated)
	if err := a.persistPanCredentials(); err != nil {
		a.status = fmt.Sprintf("%s登录成功，但保存到本机失败：%v", provider.Label(), err)
		return
	}
	a.playFailure = nil
	a.status = provider.Label() + "已授权登录"
}

func (a *app) closePanLoginWindow() {
	ls := a.panLogin
	if ls == nil {
		return
	}
	a.panLogin = nil
	ls.mu.Lock()
	cancel, win := ls.cancel, ls.win
	ls.cancel, ls.win = nil, nil
	ls.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if win != nil {
		win.Close()
	}
}
