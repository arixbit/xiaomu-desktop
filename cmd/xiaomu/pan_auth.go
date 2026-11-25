package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/egoist/mygo"
)

const panAuthRetryInterval = 4 * time.Second

type panLoginSession struct {
	mu       sync.Mutex
	provider pan.Provider
	win      *mygo.Window
	cancel   context.CancelFunc
	cleanup  func()
}

func (ls *panLoginSession) page() *mygo.Page {
	ls.mu.Lock()
	win := ls.win
	ls.mu.Unlock()
	if win == nil {
		return nil
	}
	return win.Page()
}

func (ls *panLoginSession) finishCleanup() {
	ls.mu.Lock()
	fn := ls.cleanup
	ls.cleanup = nil
	ls.mu.Unlock()
	if fn != nil {
		fn()
	}
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

func cookieValue(cookies []mygo.Cookie, name string) string {
	for _, c := range cookies {
		if strings.EqualFold(c.Name, name) && strings.TrimSpace(c.Value) != "" {
			return strings.TrimSpace(c.Value)
		}
	}
	return ""
}

func hasExactHostCookie(cookies []mygo.Cookie, name, host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	for _, c := range cookies {
		if !strings.EqualFold(c.Name, name) || strings.TrimSpace(c.Value) == "" {
			continue
		}
		if strings.TrimPrefix(strings.ToLower(strings.TrimSpace(c.Domain)), ".") == h {
			return true
		}
	}
	return false
}

func panLoginUserAgentSuffix() string {
	if runtime.GOOS == "darwin" {
		return "Version/18.0 Safari/605.1.15"
	}
	return ""
}

func allowedPanLoginURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if strings.EqualFold(rawURL, "about:blank") {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "http":
		return strings.TrimSpace(u.Host) != ""
	default:
		return false
	}
}

func shouldRetryAuthCheck(candidate, lastCandidate string, lastAttempt, now time.Time) bool {
	if candidate == "" {
		return false
	}
	if candidate != lastCandidate {
		return true
	}
	return now.Sub(lastAttempt) >= panAuthRetryInterval
}

func configurePanLoginPage(page *mygo.Page, parent *mygo.Window, uaSuffix string) {
	if page == nil {
		return
	}
	// Untrusted pages block cross-origin main-frame navigations unless
	// OnWillNavigate is registered. Official cloud-drive login flows redirect
	// across subdomains and OAuth providers (for example pan.baidu.com <->
	// passport.baidu.com, www.alipan.com <-> auth.alipan.com).
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if !allowedPanLoginURL(e.URL) {
			e.PreventDefault()
		}
	})
	page.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		if req.URL != "" && !allowedPanLoginURL(req.URL) {
			return nil
		}
		return &mygo.WindowOptions{
			Title:  "授权登录",
			Width:  960,
			Height: 700,
			Parent: parent,
			Page: mygo.PageOptions{
				Untrusted:       true,
				UserAgentSuffix: uaSuffix,
			},
		}
	})
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
	uaSuffix := panLoginUserAgentSuffix()
	title := "登录" + provider.Label()
	if onSuccess != nil {
		title += " · 完成后自动继续播放"
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:  title,
		Width:  1040,
		Height: 740,
		Parent: a.window,
		Page: mygo.PageOptions{
			Untrusted:       true,
			Session:         session,
			UserAgentSuffix: uaSuffix,
		},
	})
	if page := win.Page(); page != nil {
		configurePanLoginPage(page, win, uaSuffix)
	}
	offChild := mygo.App.OnWindowCreated(func(child *mygo.Window) {
		if child == nil || child.Parent() != win {
			return
		}
		if page := child.Page(); page != nil {
			configurePanLoginPage(page, win, uaSuffix)
		}
	})
	ls := &panLoginSession{provider: provider, win: win, cancel: cancel, cleanup: offChild}
	a.panLogin = ls
	waitMsg := fmt.Sprintf("请在弹出的%s官方页面完成登录，成功后将自动返回…", provider.Label())
	a.status = waitMsg

	win.OnClosed(func() {
		ls.finishCleanup()
		cancel()
		if a.dispatch != nil {
			a.dispatch(func() {
				if a.panLogin == ls {
					a.panLogin = nil
					if a.status == waitMsg {
						a.setNotice("已取消登录")
					}
				}
			})
		}
	})
	if page := win.Page(); page != nil {
		_ = page.LoadURL(spec.loginURL)
	}

	go a.watchPanLoginWindow(ctx, ls, session, spec, onSuccess)
}

func (a *app) watchPanLoginWindow(ctx context.Context, ls *panLoginSession, session *mygo.Session, spec panLoginSpec, onSuccess func()) {
	ticker := time.NewTicker(1200 * time.Millisecond)
	defer ticker.Stop()
	lastCandidate := ""
	var lastAttempt time.Time
	baiduBDUSS := ""
	var baiduBDUSSSeenAt time.Time
	baiduNudgedBDUSS := ""

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}

		var updated pan.Credentials
		var candidateKey string
		found := false

		if spec.useLocalAuth {
			page := ls.page()
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
			now := time.Now()
			if !shouldRetryAuthCheck(candidateKey, lastCandidate, lastAttempt, now) {
				continue
			}
			lastCandidate = candidateKey
			lastAttempt = now
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
			if ls.provider == pan.ProviderBaidu {
				if bduss := cookieValue(cookies, "BDUSS"); bduss != "" && !hasExactHostCookie(cookies, "STOKEN", "pan.baidu.com") {
					now := time.Now()
					if bduss != baiduBDUSS {
						baiduBDUSS = bduss
						baiduBDUSSSeenAt = now
					} else if baiduNudgedBDUSS != bduss && now.Sub(baiduBDUSSSeenAt) >= 3500*time.Millisecond {
						baiduNudgedBDUSS = bduss
						if page := ls.page(); page != nil {
							_ = page.LoadURL("https://pan.baidu.com/disk/main")
						}
					}
				}
			}
			if spec.readyCookie != nil && !spec.readyCookie(cookies) {
				continue
			}
			cookieHost := ""
			if ls.provider == pan.ProviderBaidu {
				cookieHost = "pan.baidu.com"
			}
			cookieStr := formatCookiesForHost(cookies, cookieHost)
			now := time.Now()
			if !shouldRetryAuthCheck(cookieStr, lastCandidate, lastAttempt, now) {
				continue
			}
			lastCandidate = cookieStr
			lastAttempt = now
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
			if a.closed || a.panLogin != ls {
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
	a.setNotice(provider.Label() + "已授权登录")
}

func (a *app) closePanLoginWindow() {
	ls := a.panLogin
	if ls == nil {
		return
	}
	a.panLogin = nil
	ls.finishCleanup()
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
