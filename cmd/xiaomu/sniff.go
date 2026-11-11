package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
	"github.com/egoist/mygo"
)

//go:embed sniff.js
var sniffScript string

// sniffPage uses an isolated website session with no access to application Go
// methods. All frames report candidate media; Go only polls plain JSON values.
func (a *app) sniffPage(ctx context.Context, play catalog.Playback) (catalog.Playback, error) {
	if a.window == nil {
		return catalog.Playback{}, fmt.Errorf("网页取流需要打开小幕窗口")
	}
	session := mygo.NewEphemeralSession()
	ua := resolve.UserAgent
	for k, v := range play.Headers {
		if strings.EqualFold(k, "User-Agent") && v != "" {
			ua = v
		}
	}
	view, err := a.window.NewWebView(mygo.WebViewOptions{URL: play.URL, Page: mygo.PageOptions{
		Untrusted: true, Session: session, UserAgent: ua, PreloadScript: sniffScript,
		PreloadAllFrames: true, BackgroundThrottling: mygo.ThrottlingOff, Autoplay: mygo.AutoplayAllow,
	}})
	if err != nil {
		return catalog.Playback{}, err
	}
	defer view.Destroy()
	page := view.Page()
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		u, err := url.Parse(e.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			e.PreventDefault()
		}
	})
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return catalog.Playback{}, ctx.Err()
		case <-ticker.C:
			value, err := page.EvalContext(ctx, "JSON.stringify(window.__xiaomuMedia || [])")
			if err != nil {
				if ctx.Err() != nil {
					return catalog.Playback{}, ctx.Err()
				}
				continue
			}
			raw, ok := value.(string)
			if !ok {
				continue
			}
			var candidates []struct {
				URL, Referer string
				Headers      map[string]string
			}
			if json.Unmarshal([]byte(raw), &candidates) != nil {
				continue
			}
			for _, candidate := range candidates {
				u, err := url.Parse(candidate.URL)
				if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
					continue
				}
				headers := maps.Clone(play.Headers)
				if headers == nil {
					headers = map[string]string{}
				}
				for k, v := range candidate.Headers {
					headers[k] = v
				}
				headers["User-Agent"] = ua
				if candidate.Referer != "" {
					headers["Referer"] = candidate.Referer
				}
				if cookies, err := session.Cookies.List(mygo.CookieFilter{URL: candidate.URL}); err == nil && len(cookies) > 0 {
					var parts []string
					for _, cookie := range cookies {
						parts = append(parts, cookie.Name+"="+cookie.Value)
					}
					headers["Cookie"] = strings.Join(parts, "; ")
				}
				return catalog.Playback{URL: candidate.URL, Headers: headers}, nil
			}
		}
	}
}
