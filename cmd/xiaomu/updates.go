package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/httpx"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	updatenative "github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
)

const updateReleaseDownloadBase = "https://github.com/arixbit/xiaomu-desktop/releases/latest/download/"

// darwinArchUpdateSource routes macOS update checks to the running CPU's
// single-architecture manifest (update-darwin-arm64.json / amd64.json),
// allowing legacy Universal installations upgraded to this bridge release to
// migrate seamlessly onto per-architecture builds on the next update.
type darwinArchUpdateSource struct {
	baseURL string
	arch    string
	client  *http.Client
	delays  []time.Duration
}

func newDarwinArchUpdateSource(arch string) *darwinArchUpdateSource {
	if arch != "arm64" && arch != "amd64" {
		return nil
	}
	return &darwinArchUpdateSource{
		baseURL: updateReleaseDownloadBase,
		arch:    arch,
		client:  httpx.NewStreamClient(30*time.Second, false),
		delays:  []time.Duration{time.Second, 2 * time.Second},
	}
}

func (s *darwinArchUpdateSource) Manifest(ctx context.Context, _ bool) (io.ReadCloser, error) {
	base := strings.TrimRight(s.baseURL, "/") + "/"
	primary := base + "update-darwin-" + s.arch + ".json"
	body, status, err := s.get(ctx, primary)
	if err == nil {
		return body, nil
	}
	if status == http.StatusNotFound {
		fallback := base + "update-darwin-universal.json"
		if fbBody, _, fbErr := s.get(ctx, fallback); fbErr == nil {
			return fbBody, nil
		}
	}
	return nil, err
}

func (s *darwinArchUpdateSource) Open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	body, _, err := s.get(ctx, rawURL)
	return body, err
}

func (s *darwinArchUpdateSource) get(ctx context.Context, rawURL string) (io.ReadCloser, int, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, 0, err
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isUpdateLoopback(u.Hostname())) {
		return nil, 0, fmt.Errorf("拒绝通过非 HTTPS 地址获取更新：%s", rawURL)
	}
	client := s.client
	if client == nil {
		client = httpx.SharedStreamClient()
	}
	for try := 0; ; try++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, 0, err
		}
		// Keep User-Agent strictly ASCII: GitHub's release asset CDN rejects
		// non-ASCII User-Agent headers on cache misses.
		req.Header.Set("User-Agent", "xiaomu-updater")
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return resp.Body, resp.StatusCode, nil
		}
		status := 0
		var attemptErr error
		retryable := false
		if err != nil {
			attemptErr = err
			retryable = ctx.Err() == nil
		} else {
			status = resp.StatusCode
			httpx.DrainAndClose(resp.Body)
			attemptErr = fmt.Errorf("GET %s: %s", rawURL, resp.Status)
			retryable = status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
		}
		if !retryable || try >= len(s.delays) {
			return nil, status, attemptErr
		}
		timer := time.NewTimer(s.delays[try])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, 0, ctx.Err()
		case <-timer.C:
		}
	}
}

func isUpdateLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// watchUpdates connects the existing background checks to the main window.
// Subscribe before reading so a check completed during startup is also shown.
func (a *app) watchUpdates() {
	refresh := func() { a.setAvailableUpdate(updater.AvailableVersion()) }
	a.window.OnClosed(updater.OnChange(refresh))
	refresh()
}

func (a *app) setAvailableUpdate(version string) {
	apply := func() {
		if !a.closed {
			a.availableUpdate = version
		}
	}
	if a.dispatch == nil {
		apply()
	} else {
		a.dispatch(apply)
	}
}

func updateButton(c *ui.Context, version string) ui.Element {
	p := paletteFor(c)
	label := "检查更新"
	if version != "" {
		label = "有新版本"
	}
	button := ui.ButtonBase(c.Key("side-act-update")).FillWidth().Height(34).Padding(0, 12).Gap(10).Radius(8).
		Justify(ui.Start).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	bg := ui.Transparent
	fg := p.textMuted
	if version != "" {
		bg = p.surfaceSubtle
		fg = p.text
	}
	if button.Hovered() {
		bg = p.surfaceHover
		fg = p.text
	}
	button.Background(button.AnimateColor("side-upd-bg", bg, animColor))
	applyPressPose(button)
	if version != "" {
		button.Tooltip("新版本 " + version + " 可用，点击查看更新")
	}
	button.Children(func() {
		ui.Icon(c, navIconUpdate).Size(16, 16).TextColor(fg).PassThrough()
		ui.Text(c, label).FontSize(13).TextColor(fg).SingleLine().ContentTransition(ctReplace).PassThrough()
		if version != "" {
			ui.Spacer(c)
			ui.Box(c).Size(7, 7).Radius(4).Background(p.accent).Label("新版本提示").PassThrough()
		}
	})
	return button
}

func updateRailButton(c *ui.Context, version string, height float32) ui.Element {
	p := paletteFor(c)
	label := "检查更新"
	if version != "" {
		label = "有新版本"
	}
	btn := ui.ButtonBase(c.Key("rail-update")).FillWidth().Height(height).Radius(9).
		Center().Cursor(ui.CursorPointer).Label(label).TextColor(p.textMuted)
	if version != "" {
		btn.Tooltip("新版本 " + version + " 可用，点击查看更新")
	} else {
		btn.Tooltip(label)
	}
	bg := ui.Transparent
	fg := p.textMuted
	if version != "" {
		bg = p.surfaceSubtle
		fg = p.text
	}
	if btn.Hovered() {
		bg = p.surfaceHover
		fg = p.text
	}
	btn.Background(btn.AnimateColor("rail-upd-bg", bg, animColor))
	applyPressPose(btn)
	btn.Children(func() {
		ui.Icon(c, navIconUpdate).Size(17, 17).TextColor(fg).PassThrough()
		if version != "" {
			ui.Box(c).Size(7, 7).Radius(4).Absolute().Right(10).Top(8).Background(p.accent).Label("新版本提示").PassThrough()
		}
	})
	return btn
}

// initUpdates runs before App.Run. The native plugin checks in the background
// and asks before installing, unless the user opts into automatic downloads.
func (a *app) initUpdates() {
	if runtime.GOOS == "darwin" {
		if src := newDarwinArchUpdateSource(runtime.GOARCH); src != nil {
			mygo.Updater.SetSource(src)
		}
	}
	mygo.Use(updatenative.New(updater.Options{Language: "zh-Hans"}))
	updater.OnInstalled(a.flushHistory)
	fileItems := []*mygo.MenuItem{
		{
			Label:       "打开视频文件…",
			Accelerator: "CmdOrCtrl+O",
			Click: func(*mygo.MenuItem, *mygo.Window) {
				a.deferUntilReady(a.openLocalFileDialog)
			},
		},
		{Role: mygo.RoleOpenRecent},
		mygo.Separator(),
	}
	if runtime.GOOS == "darwin" {
		fileItems = append(fileItems, &mygo.MenuItem{Role: mygo.RoleClose})
	} else {
		fileItems = append(fileItems, &mygo.MenuItem{Role: mygo.RoleQuit})
	}
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: "小幕", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			updater.MenuItem(),
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Label: "文件", Submenu: fileItems},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	}))
}
