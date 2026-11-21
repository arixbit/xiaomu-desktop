package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/nas"
	"github.com/arixbit/xiaomu-desktop/internal/proxy"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"
)

// nasPlaying is the NAS file on show, kept so the play history can record
// it and resume it later.
type nasPlaying struct {
	Server string // address as saved, host or host:port
	Share  string
	Path   string // backslash path inside the share
	Name   string // display name
	Token  string // playback token issued for this file, revoked on stop
}

// nasBrowser is the 内网盘 tab: the saved server, the live connection and
// the directory on show. server, hasServer, client and connected are
// guarded by mu: the UI thread writes them, and the proxy's HTTP
// goroutines read them through openNASFile, so no field is ever read
// without the lock. dir and entries stay on the UI thread only. gen
// counts disconnects and removals: a connect whose apply runs after one
// must not resurrect the connection or save the server back.
type nasBrowser struct {
	mu        sync.Mutex
	server    nas.Server
	hasServer bool
	client    *nas.Client
	connected bool
	gen       int
	dir       string // backslash path, "" is the share root
	entries   []nas.Entry

	address, share, user, password, domain string

	// dial replaces the real SMB dial in tests.
	dial func(context.Context, nas.Server) (*nas.Client, error)
}

func (n *nasBrowser) fillForm(srv nas.Server) {
	n.address, n.share, n.user, n.password, n.domain =
		srv.Address, srv.Share, srv.User, srv.Password, srv.Domain
}

func (n *nasBrowser) closeClientLocked() {
	// The caller holds mu. Only the pointer is swapped out here; the
	// network close runs in the background, so disconnecting or closing
	// the window never waits on the NAS.
	client := n.client
	n.client = nil
	if client != nil {
		go func() { _ = client.Close() }()
	}
}

// dialNAS dials and mounts one server.
func (a *app) dialNAS(ctx context.Context, srv nas.Server) (*nas.Client, error) {
	if a.nas.dial != nil {
		return a.nas.dial(ctx, srv)
	}
	client := nas.New(srv)
	if err := client.Dial(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// nasClient returns the connected client for srv, dialing it if needed and
// replacing a client for another server. The match is on the whole server:
// a changed user, password or domain must not keep reusing the old
// connection, or the new credentials would be saved without ever being
// tried. Reconnecting therefore dials with the new credentials first and
// only saves them once the root listing succeeds.
//
// The dial runs outside the lock: a NAS that does not answer must not
// block the UI thread, which only needs this lock briefly to disconnect
// or close. The lock is only for the cache check and for installing the
// result.
func (a *app) nasClient(ctx context.Context, srv nas.Server) (*nas.Client, error) {
	a.nas.mu.Lock()
	if c := a.nas.client; c != nil && c.Server == srv {
		a.nas.mu.Unlock()
		return c, nil
	}
	a.nas.mu.Unlock()

	client, err := a.dialNAS(ctx, srv)
	if err != nil {
		return nil, err
	}

	a.nas.mu.Lock()
	if err := ctx.Err(); err != nil {
		// Cancelled while dialling: do not install the client.
		a.nas.mu.Unlock()
		go func() { _ = client.Close() }()
		return nil, err
	}
	if c := a.nas.client; c != nil && c.Server == srv {
		// Another caller connected the same server while we dialled.
		a.nas.mu.Unlock()
		go func() { _ = client.Close() }()
		return c, nil
	}
	old := a.nas.client
	a.nas.client = client
	a.nas.mu.Unlock()
	if old != nil {
		go func() { _ = old.Close() }()
	}
	return client, nil
}

// dropNASClient closes client if it is still the current one, for a
// connection that turned out unusable. Only the pointer is swapped out
// under the lock; the network close runs in the background, so a drop
// cannot hold the lock while the NAS is unresponsive.
func (a *app) dropNASClient(client *nas.Client) {
	a.nas.mu.Lock()
	if a.nas.client == client {
		a.nas.client = nil
		a.nas.mu.Unlock()
		go func() { _ = client.Close() }()
		return
	}
	a.nas.mu.Unlock()
}

func (a *app) closeNASClient() {
	a.nas.mu.Lock()
	defer a.nas.mu.Unlock()
	a.nas.closeClientLocked()
}

// dropNASClientOnError drops the client unless the failure is a
// cancelled request or a missing file: neither means the session is
// dead. A cancelled request is a seek or a replaced job, not a dead NAS,
// and dropping the client on it would close the session out from under
// the other Range requests still reading.
func (a *app) dropNASClientOnError(client *nas.Client, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	a.dropNASClient(client)
}

// startNASConnection connects to srv, lists its root and saves it.
func (a *app) startNASConnection(srv nas.Server) {
	if err := srv.Validate(); err != nil {
		a.status = err.Error()
		return
	}
	a.nas.mu.Lock()
	gen := a.nas.gen
	a.nas.mu.Unlock()
	a.run(jobNAS, "连接内网盘", func(ctx context.Context) (jobResult, error) {
		client, err := a.nasClient(ctx, srv)
		if err != nil {
			return jobResult{}, err
		}
		entries, err := client.ReadDir(ctx, "")
		if err != nil {
			a.dropNASClientOnError(client, err)
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			a.nas.mu.Lock()
			if a.nas.gen != gen {
				// Removed or disconnected while connecting: do not
				// resurrect the connection or save the server back.
				a.nas.mu.Unlock()
				return
			}
			a.nas.server, a.nas.hasServer = srv, true
			a.nas.connected = true
			a.nas.mu.Unlock()
			a.nas.dir, a.nas.entries = "", entries
			a.nas.fillForm(srv)
			if err := a.saveNAS(srv); err != nil {
				a.status = "保存内网盘失败：" + err.Error()
			}
		}}, nil
	}, nil)
}

// connectNAS connects with what the add form holds.
func (a *app) connectNAS() {
	a.startNASConnection(nas.Server{
		Address:  strings.TrimSpace(a.nas.address),
		Share:    strings.TrimSpace(a.nas.share),
		User:     strings.TrimSpace(a.nas.user),
		Password: a.nas.password,
		Domain:   strings.TrimSpace(a.nas.domain),
	})
}

// browseNAS lists dir inside the connected share.
func (a *app) browseNAS(dir string) {
	if !a.nas.connected {
		a.status = "内网盘未连接"
		return
	}
	srv := a.nas.server
	a.run(jobNAS, "打开文件夹", func(ctx context.Context) (jobResult, error) {
		client, err := a.nasClient(ctx, srv)
		if err != nil {
			return jobResult{}, err
		}
		entries, err := client.ReadDir(ctx, dir)
		if err != nil {
			// The session may be dead (NAS rebooted, idle timeout):
			// drop the client so the next attempt redials instead of
			// failing on the dead session forever. A cancelled
			// request or a missing folder is not a dead session.
			a.dropNASClientOnError(client, err)
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			a.nas.dir, a.nas.entries = dir, entries
		}}, nil
	}, nil)
}

// disconnectNAS drops the connection but keeps the saved server, so one
// tap reconnects. A NAS file on show is stopped first: otherwise the
// player keeps asking the proxy for ranges and the next request would
// silently redial the NAS that was just disconnected. The pending
// connect job is cancelled first, so its apply cannot set connected
// back on after this disconnect; the generation bump covers the same
// for an apply that is already on its way.
func (a *app) disconnectNAS() {
	a.cancelJob(jobNAS)
	if a.playingNAS != nil {
		a.leavePlayer()
	}
	// Mark disconnected before closing the client: a proxy request that
	// arrives after this point is refused instead of redialling.
	a.nas.mu.Lock()
	a.nas.connected = false
	a.nas.dir, a.nas.entries = "", nil
	a.nas.gen++
	a.nas.mu.Unlock()
	a.closeNASClient()
	if a.proxy != nil {
		a.proxy.RevokeAllNASFiles()
	}
	a.status = "已断开内网盘"
}

// removeNAS disconnects and forgets the saved server entirely. The
// pending connect job is cancelled by disconnectNAS, so its apply cannot
// run afterwards and write the server back into nas.json.
func (a *app) removeNAS() {
	a.disconnectNAS()
	a.nas.mu.Lock()
	a.nas.server = nas.Server{}
	a.nas.hasServer = false
	a.nas.mu.Unlock()
	a.nas.fillForm(nas.Server{})
	if err := a.removeSavedNAS(); err != nil {
		a.status = "移除内网盘失败：" + err.Error()
		return
	}
	a.status = "已移除内网盘"
}

// openNASFile opens one file for the proxy. It is called on the proxy's
// HTTP goroutines, so it snapshots the saved server and the connection
// state under the lock and only uses the snapshot: the UI thread may be
// removing or reconnecting the NAS at the same time. A disconnected NAS
// is refused here and never redialled: reconnecting is an explicit user
// action (opening the tab, tapping 重新连接).
func (a *app) openNASFile(ctx context.Context, server, share, path string) (proxy.NASFile, error) {
	a.nas.mu.Lock()
	saved, hasServer, connected := a.nas.server, a.nas.hasServer, a.nas.connected
	a.nas.mu.Unlock()
	if !hasServer || !connected || saved.Address != server || saved.Share != share {
		return nil, fmt.Errorf("内网盘未连接")
	}
	client, err := a.nasClient(ctx, saved)
	if err != nil {
		return nil, err
	}
	f, err := client.Open(ctx, path)
	if err != nil {
		// A dead session must not stick: drop the client so the next
		// request redials. The player only retries a few times before
		// giving up, so this cannot turn into a dial storm, and a NAS
		// that comes back is picked up again. A cancelled request or
		// a missing file is not a dead session.
		a.dropNASClientOnError(client, err)
		return nil, err
	}
	return f, nil
}

// revokePlayingNASToken revokes the playback token of the NAS file on
// show, if any. Called when playback of that file ends for any reason:
// leaving the player, switching to another title, disconnecting.
func (a *app) revokePlayingNASToken() {
	if a.playingNAS != nil && a.playingNAS.Token != "" && a.proxy != nil {
		a.proxy.RevokeNASFile(a.playingNAS.Token)
	}
}

// playNASFile plays one NAS file through the loopback proxy, like a
// direct URL, and records which NAS file it is for the play history.
// The proxy URL carries only a random token bound to this one file:
// no other path on the share can be read with it, and the token dies
// when playback stops.
func (a *app) playNASFile(server, share, path, name string) bool {
	a.prepareExternalPlayback()
	if a.proxy == nil {
		p, err := proxy.Start(nil)
		if err != nil {
			a.status = "代理启动失败: " + err.Error()
			return false
		}
		a.proxy = p
	}
	a.proxy.RegisterNASOpener(a.openNASFile)
	token, err := a.proxy.IssueNASFile(server, share, path)
	if err != nil {
		a.status = "无法生成播放令牌: " + err.Error()
		return false
	}
	wrapped := a.proxy.WrapNAS(token)
	if a.player != nil {
		a.player.Close()
		a.player = nil
	}
	opener := a.open
	if opener == nil {
		opener = video.OpenURL
	}
	player, err := opener(wrapped)
	if err != nil {
		a.proxy.RevokeNASFile(token)
		a.status = "无法打开: " + err.Error()
		a.syncNowPlaying()
		return false
	}
	a.adoptPlayer(player)
	a.playingNAS = &nasPlaying{Server: server, Share: share, Path: path, Name: name, Token: token}
	a.playingName = name
	a.playingSource, a.playingEpisode = "内网盘", "正片"
	a.playingLive, a.playingDirect = false, false
	a.status = "正在播放"
	a.syncNowPlaying()
	return true
}

// nasMatches reports whether the saved NAS is still the one a history
// entry was recorded from.
func (a *app) nasMatches(e historyEntry) bool {
	srv, _, ok := nas.ParseSMBURL(e.ConfigURL)
	if !ok {
		return false
	}
	a.nas.mu.Lock()
	defer a.nas.mu.Unlock()
	return a.nas.hasServer &&
		a.nas.server.Address == srv.Address && a.nas.server.Share == srv.Share
}

// resumeNAS continues a NAS history entry: the file is opened again from
// the same server and share, at the recorded position. A disconnected NAS
// is not redialled here: resuming is not a reconnect, the user reconnects
// explicitly and then resumes.
func (a *app) resumeNAS(e historyEntry) {
	if !a.nasMatches(e) {
		a.status = "这条记录的 NAS 已移除或更换，重新添加后可继续播放"
		return
	}
	a.nas.mu.Lock()
	srv, connected := a.nas.server, a.nas.connected
	a.nas.mu.Unlock()
	if !connected {
		a.status = "内网盘未连接，重新连接后可继续播放"
		return
	}
	a.cancelJob(jobTitle)
	a.run(jobPlay, "继续播放", func(ctx context.Context) (jobResult, error) {
		client, err := a.nasClient(ctx, srv)
		if err != nil {
			return jobResult{}, err
		}
		f, err := client.Open(ctx, e.VideoID)
		if err != nil {
			a.dropNASClientOnError(client, err)
			return jobResult{}, err
		}
		_ = f.Close()
		return jobResult{apply: func() {
			// The NAS may have been disconnected while the file was
			// being opened: do not start playback on a NAS that is
			// no longer connected.
			a.nas.mu.Lock()
			connected := a.nas.connected
			a.nas.mu.Unlock()
			if !connected {
				a.status = "内网盘未连接，重新连接后可继续播放"
				return
			}
			if a.playNASFile(srv.Address, srv.Share, e.VideoID, e.Name) {
				a.resumeAt = time.Duration(e.Position * float64(time.Second))
			}
		}}, nil
	}, nil)
}

func formatNASSize(size int64) string {
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(size)/(1<<30))
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

// nasView is the 内网盘 tab: an add form when nothing is saved, the saved
// server with a reconnect button when disconnected, and the folder
// listing when connected.
func (a *app) nasView(c *ui.Context) {
	n := &a.nas
	ui.Scroll(c).Fill().Gap(14).Children(func() {
		if !n.hasServer {
			ui.Column(c).FillWidth().Padding(18).Gap(12).Radius(22).Background(surface).Children(func() {
				ui.Text(c, "添加内网盘").FontSize(17).Bold()
				ui.Text(c, "只播 MP4 / MOV / M4V；MKV 等格式系统播放器播不了。").FontSize(13).TextColor(textMuted)
				ui.TextInput(c, &n.address).Label("服务器地址").Placeholder("192.168.1.10 或 192.168.1.10:445").FillWidth()
				ui.TextInput(c, &n.share).Label("共享名").Placeholder("例如 Movies").FillWidth()
				ui.TextInput(c, &n.user).Label("用户名").Placeholder("用户名").FillWidth()
				ui.TextInput(c, &n.password).Label("密码").Placeholder("密码").FillWidth().Password()
				ui.TextInput(c, &n.domain).Label("域（可选）").Placeholder("域（可选，留空用服务器默认）").FillWidth()
				if primaryButton(c, "连接并保存").Clicked() {
					a.connectNAS()
				}
			})
			return
		}
		ui.Column(c).FillWidth().Padding(18).Gap(12).Radius(22).Background(surface).Children(func() {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, "内网盘").FontSize(17).Bold()
					ui.Text(c, n.server.Address+" / "+n.server.Share).FontSize(13).TextColor(textMuted).Selectable()
				})
				if n.connected {
					if glassButton(c, "断开").Clicked() {
						a.disconnectNAS()
					}
				} else if primaryButton(c, "重新连接").Clicked() {
					a.startNASConnection(n.server)
				}
				if glassButton(c, "移除").Clicked() {
					a.removeNAS()
				}
			})
			if !n.connected {
				ui.Text(c, "已保存，点“重新连接”继续。").FontSize(13).TextColor(textMuted)
				return
			}
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				path := n.server.Share
				if n.dir != "" {
					path += " / " + strings.ReplaceAll(n.dir, "\\", " / ")
				}
				ui.Text(c, path).FontSize(13).TextColor(textMuted).Grow(1).SingleLine()
				if n.dir != "" && glassButton(c, "上级目录").Clicked() {
					a.browseNAS(nas.Parent(n.dir))
				}
			})
		})
		if !n.connected {
			return
		}
		if len(n.entries) == 0 {
			emptyState(c, "这个文件夹是空的", "没有可播的视频文件。")
			return
		}
		for _, e := range n.entries {
			entry := e
			row := ui.Row(c).Key("nas-"+entry.Name).FillWidth().Height(38).Padding(0, 12).
				Radius(11).AlignItems(ui.Center).Cursor(ui.CursorPointer)
			switch {
			case entry.IsDir:
				row.Label("打开 " + entry.Name)
				row.Children(func() {
					ui.Text(c, entry.Name).Grow(1).MinWidth(0).SingleLine()
					ui.Text(c, "文件夹").FontSize(13).TextColor(textMuted)
				})
				if row.Clicked() {
					a.browseNAS(nas.Join(n.dir, entry.Name))
				}
			case entry.Class == nas.ClassPlayable:
				row.Label("播放 " + entry.Name)
				row.Children(func() {
					ui.Text(c, entry.Name).Grow(1).MinWidth(0).SingleLine()
					ui.Text(c, formatNASSize(entry.Size)).FontSize(13).TextColor(textMuted)
				})
				if row.Clicked() {
					a.playNASFile(n.server.Address, n.server.Share, nas.Join(n.dir, entry.Name), entry.Name)
				}
			default:
				row.Label(entry.Name)
				row.Children(func() {
					ui.Text(c, entry.Name).Grow(1).MinWidth(0).SingleLine()
					ui.Text(c, "不支持播放").FontSize(13).TextColor(textMuted)
				})
				if row.Clicked() {
					ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(entry.Name), "."))
					a.status = ext + " 不支持播放，系统播放器只能播 MP4 / MOV / M4V"
				}
			}
		}
	})
}
