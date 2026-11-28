package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/mediaserver"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const mediaPageSize = 40

// savedMediaServer is one user-added Emby or Jellyfin login. It lives only in
// the app's user-data directory. The password is never stored; the access
// token is.
type savedMediaServer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	Product     string `json:"product"`
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	AccessToken string `json:"access_token"`
	ServerID    string `json:"server_id,omitempty"`
	DeviceID    string `json:"device_id"`
}

func (s savedMediaServer) session() mediaserver.Session {
	return mediaserver.Session{
		BaseURL:     s.BaseURL,
		Product:     mediaserver.Product(s.Product),
		ServerName:  s.Name,
		UserID:      s.UserID,
		Username:    s.Username,
		AccessToken: s.AccessToken,
		ServerID:    s.ServerID,
		DeviceID:    s.DeviceID,
	}
}

func (s savedMediaServer) origin() string {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = "媒体服务器"
	}
	switch mediaserver.Product(s.Product) {
	case mediaserver.ProductJellyfin:
		return name + " · Jellyfin"
	case mediaserver.ProductEmby:
		return name + " · Emby"
	default:
		return name
	}
}

func (s savedMediaServer) productLabel() string {
	switch mediaserver.Product(s.Product) {
	case mediaserver.ProductJellyfin:
		return "Jellyfin"
	case mediaserver.ProductEmby:
		return "Emby"
	default:
		return "Emby / Jellyfin"
	}
}

type mediaCrumb struct {
	ID    string
	Title string
	Sort  string
}

type mediaServerUI struct {
	path       string
	servers    []savedMediaServer
	addOpen    bool
	addWasOpen bool
	addURL     string
	addUser    string
	addPass    string
	addErr     string
	replaceID  string

	activeID string
	stack    []mediaCrumb
	items    []mediaserver.Item
	total    int
	start    int
	err      string
	selected int
	grid     ui.GridState
}

func mediaServersPath() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "media_servers.json"), nil
}

func (a *app) restoreMediaServers() {
	path := a.media.path
	if path == "" {
		var err error
		path, err = mediaServersPath()
		if err != nil {
			return
		}
		a.media.path = path
	}
	servers, err := readMediaServers(path)
	if err != nil {
		a.status = err.Error()
		return
	}
	a.media.servers = servers
	a.syncMediaPosterAuth()
}

func (a *app) saveMediaServers() error {
	a.syncMediaPosterAuth()
	if a.media.path == "" && !a.persist {
		return nil
	}
	path := a.media.path
	if path == "" {
		var err error
		path, err = mediaServersPath()
		if err != nil {
			return err
		}
		a.media.path = path
	}
	return writeMediaServers(path, a.media.servers)
}

func readMediaServers(path string) ([]savedMediaServer, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Servers []savedMediaServer `json:"servers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("读取媒体服务器失败：%w", err)
	}
	out := make([]savedMediaServer, 0, len(file.Servers))
	for _, s := range file.Servers {
		s.BaseURL = strings.TrimSpace(s.BaseURL)
		s.AccessToken = strings.TrimSpace(s.AccessToken)
		s.UserID = strings.TrimSpace(s.UserID)
		if s.ID == "" || s.BaseURL == "" || s.AccessToken == "" || s.UserID == "" {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func writeMediaServers(path string, servers []savedMediaServer) error {
	data, err := json.MarshalIndent(struct {
		Servers []savedMediaServer `json:"servers"`
	}{Servers: servers}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".media-servers-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *app) mediaServer(id string) (savedMediaServer, bool) {
	for _, s := range a.media.servers {
		if s.ID == id {
			return s, true
		}
	}
	return savedMediaServer{}, false
}

func (a *app) upsertMediaServer(s savedMediaServer) {
	if s.ID == "" {
		s.ID = newLocalID()
	}
	for i := range a.media.servers {
		same := a.media.servers[i].ID == s.ID || (a.media.servers[i].BaseURL == s.BaseURL && a.media.servers[i].UserID == s.UserID)
		if !same {
			continue
		}
		s.ID = a.media.servers[i].ID
		if s.DeviceID == "" {
			s.DeviceID = a.media.servers[i].DeviceID
		}
		a.media.servers[i] = s
		if err := a.saveMediaServers(); err != nil {
			a.status = "保存媒体服务器失败：" + err.Error()
		}
		return
	}
	a.media.servers = append(a.media.servers, s)
	if err := a.saveMediaServers(); err != nil {
		a.status = "保存媒体服务器失败：" + err.Error()
	}
}

func (a *app) removeMediaServer(id string) {
	var removed savedMediaServer
	kept := a.media.servers[:0]
	for _, s := range a.media.servers {
		if s.ID == id {
			removed = s
			continue
		}
		kept = append(kept, s)
	}
	a.media.servers = kept
	if err := a.saveMediaServers(); err != nil {
		a.status = "移除服务器失败：" + err.Error()
		return
	}
	if removed.AccessToken != "" {
		srv := removed
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = mediaserver.Logout(ctx, srv.session())
		}()
	}
	name := removed.Name
	if name == "" {
		name = "媒体服务器"
	}
	a.setNotice("已移除「" + name + "」")
}

func (a *app) openAddMediaServer(id string) {
	a.media.addErr = ""
	a.media.addPass = ""
	a.media.replaceID = ""
	a.media.addURL = ""
	a.media.addUser = ""
	if srv, ok := a.mediaServer(id); ok {
		a.media.replaceID = srv.ID
		a.media.addURL = srv.BaseURL
		a.media.addUser = srv.Username
	}
	a.media.addOpen = true
}

func (a *app) submitMediaServer() {
	if a.running(jobSite) {
		return
	}
	user := strings.TrimSpace(a.media.addUser)
	pass := a.media.addPass
	base, err := mediaserver.NormalizeBaseURL(a.media.addURL)
	if err != nil {
		a.media.addErr = err.Error()
		return
	}
	if user == "" {
		a.media.addErr = "请填写用户名"
		return
	}
	deviceID := ""
	replaceID := a.media.replaceID
	if prev, ok := a.mediaServer(replaceID); ok {
		deviceID = prev.DeviceID
	}
	a.media.addErr = ""
	a.run(jobSite, "连接媒体服务器", func(ctx context.Context) (jobResult, error) {
		sess, err := mediaserver.Connect(ctx, base, user, pass, deviceID)
		if err != nil {
			return jobResult{}, err
		}
		saved := savedMediaServer{
			ID:          replaceID,
			Name:        sess.ServerName,
			BaseURL:     sess.BaseURL,
			Product:     string(sess.Product),
			UserID:      sess.UserID,
			Username:    sess.Username,
			AccessToken: sess.AccessToken,
			ServerID:    sess.ServerID,
			DeviceID:    sess.DeviceID,
		}
		return jobResult{apply: func() {
			a.upsertMediaServer(saved)
			a.media.addOpen = false
			a.media.addPass = ""
			a.media.addErr = ""
			a.sourcesTab = 2
			a.setNotice("已连接「" + saved.Name + "」")
		}}, nil
	}, func(err error) {
		a.media.addErr = strings.TrimPrefix(jobError("连接", err), "连接失败：")
		if a.media.addErr == "" {
			a.media.addErr = "连接失败"
		}
	})
}

func (a *app) openMediaServer(id string) {
	srv, ok := a.mediaServer(id)
	if !ok {
		return
	}
	title := srv.Name
	if title == "" {
		title = "媒体库"
	}
	a.media.activeID = id
	a.media.stack = []mediaCrumb{{Title: title, Sort: mediaserver.SortName}}
	a.media.start = 0
	a.media.items = nil
	a.media.total = 0
	a.media.err = ""
	a.media.selected = -1
	if a.route() != routeMedia {
		a.push(routeMedia)
	}
	a.loadMediaBrowse()
}

func (a *app) mediaAscend() bool {
	if len(a.media.stack) <= 1 {
		return false
	}
	a.media.stack = a.media.stack[:len(a.media.stack)-1]
	a.media.start = 0
	a.media.err = ""
	a.clearArmed()
	a.loadMediaBrowse()
	return true
}

func (a *app) loadMediaBrowse() {
	srv, ok := a.mediaServer(a.media.activeID)
	if !ok || len(a.media.stack) == 0 {
		a.media.err = "这台服务器已移除"
		a.media.items = nil
		return
	}
	crumb := a.media.stack[len(a.media.stack)-1]
	start := a.media.start
	active := srv.ID
	a.run(jobSite, "读取媒体库", func(ctx context.Context) (jobResult, error) {
		var (
			items []mediaserver.Item
			total int
			err   error
		)
		if crumb.ID == "" {
			items, err = mediaserver.Libraries(ctx, srv.session())
			total = len(items)
		} else {
			var page mediaserver.Page
			page, err = mediaserver.Children(ctx, srv.session(), crumb.ID, crumb.Sort, start, mediaPageSize)
			items, total = page.Items, page.Total
		}
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if a.media.activeID != active {
				return
			}
			a.media.items = items
			a.media.total = total
			a.media.err = ""
			a.media.selected = -1
		}}, nil
	}, func(err error) {
		if a.media.activeID != active {
			return
		}
		a.media.items = nil
		a.media.err = jobError("读取媒体库", err)
	})
}

func (a *app) openMediaItem(it mediaserver.Item) {
	if it.Folder {
		sort := mediaserver.SortName
		if it.Type == "Series" || it.Type == "Season" {
			sort = mediaserver.SortIndex
		}
		title := it.Name
		if title == "" {
			title = "文件夹"
		}
		a.media.stack = append(a.media.stack, mediaCrumb{ID: it.ID, Title: title, Sort: sort})
		a.media.start = 0
		a.loadMediaBrowse()
		return
	}
	if !it.Playable {
		return
	}
	srv, ok := a.mediaServer(a.media.activeID)
	if !ok {
		a.status = "这台服务器已移除"
		return
	}
	name, episode := mediaHistoryTitle(it)
	a.playMediaServerItem(srv, it.ID, name, episode, stampMediaPosterURL(it.ImageURL, srv.ID), srv.origin(), 0)
}

func (a *app) playMediaServerItem(srv savedMediaServer, itemID, name, episode, pic, origin string, at time.Duration) {
	a.run(jobPlay, "准备播放", func(ctx context.Context) (jobResult, error) {
		play, err := mediaserver.DirectPlay(ctx, srv.session(), itemID)
		if err != nil {
			return jobResult{}, err
		}
		return jobResult{apply: func() {
			if !a.playURL(play.URL, play.Headers) {
				return
			}
			a.playingMedia = true
			a.playingMediaServer = srv.ID
			a.playingMediaUser = srv.UserID
			a.playingMediaItem = itemID
			a.playingMediaName = name
			a.playingMediaEpisode = episode
			a.playingMediaPic = pic
			a.playingMediaOrigin = origin
			a.playingName = name
			if episode != "" && episode != "正片" {
				a.playingName = strings.TrimSpace(name + " · " + episode)
			}
			a.resumeAt = at
			a.revealContinueWatchingKey(mediaHistoryKey(srv.ID, srv.UserID, itemID))
			a.syncNowPlaying()
		}}, nil
	}, func(err error) {
		a.status = jobError("播放", err)
	})
}

func (a *app) resumeMedia(e historyEntry) {
	srv, ok := a.mediaServer(e.SiteKey)
	if !ok {
		a.status = "这台媒体服务器已移除，重新添加后可继续播放"
		return
	}
	if e.MediaUser != srv.UserID {
		a.status = "这条记录是另一个账号的，换回原账号后可继续播放"
		return
	}
	var at time.Duration
	if !e.finished() && e.Position > 0 {
		at = time.Duration(e.Position * float64(time.Second))
	}
	a.playMediaServerItem(srv, e.VideoID, e.Name, e.Episode, e.Pic, e.Origin, at)
}

func (a *app) clearPlayingMedia() {
	a.playingMedia = false
	a.playingMediaServer = ""
	a.playingMediaUser = ""
	a.playingMediaItem = ""
	a.playingMediaName = ""
	a.playingMediaEpisode = ""
	a.playingMediaPic = ""
	a.playingMediaOrigin = ""
}

func mediaHistoryTitle(it mediaserver.Item) (name, episode string) {
	if it.Type == "Episode" {
		name = it.SeriesName
		if name == "" {
			name = it.Name
		}
		code := mediaItemRemark(it)
		episode = strings.TrimSpace(strings.TrimSpace(code + " " + it.Name))
		if episode == "" {
			episode = "正片"
		}
		return name, episode
	}
	if it.Name == "" {
		return it.ID, "正片"
	}
	return it.Name, "正片"
}

func mediaItemRemark(it mediaserver.Item) string {
	if it.Type != "Episode" {
		return ""
	}
	if it.ParentIndexNumber > 0 && it.IndexNumber > 0 {
		return fmt.Sprintf("S%02dE%02d", it.ParentIndexNumber, it.IndexNumber)
	}
	if it.IndexNumber > 0 {
		return fmt.Sprintf("第%d集", it.IndexNumber)
	}
	return ""
}

func mediaTypeLabel(it mediaserver.Item) string {
	switch it.Type {
	case "Movie":
		return "电影"
	case "Series":
		return "剧集"
	case "Season":
		if it.IndexNumber > 0 {
			return fmt.Sprintf("第%d季", it.IndexNumber)
		}
		return "季"
	case "Episode":
		return "剧集"
	case "CollectionFolder":
		switch strings.ToLower(it.CollectionType) {
		case "movies":
			return "电影库"
		case "tvshows":
			return "剧集库"
		case "homevideos":
			return "家庭视频"
		case "boxsets":
			return "合集"
		default:
			return "媒体库"
		}
	case "Folder", "BoxSet":
		return "文件夹"
	default:
		if it.Folder {
			return "文件夹"
		}
		return ""
	}
}

func mediaItemVideo(it mediaserver.Item, serverID string) catalog.Video {
	return catalog.Video{
		ID:      catalog.ID(it.ID),
		Name:    it.Name,
		Pic:     stampMediaPosterURL(it.ImageURL, serverID),
		Year:    it.Year,
		Type:    mediaTypeLabel(it),
		Remarks: mediaItemRemark(it),
	}
}

func (m mediaServerUI) crumbPath() string {
	parts := make([]string, 0, len(m.stack))
	for _, c := range m.stack {
		if c.Title != "" {
			parts = append(parts, c.Title)
		}
	}
	return strings.Join(parts, " / ")
}

func (a *app) sourcesMediaTab(c *ui.Context) {
	p := paletteFor(c)
	ui.Scroll(c).Key("sources-scroll-media").Grow(1).FillWidth().MinHeight(0).Padding(0, scrollGutter, 0, 0).Gap(12).ScrollbarStyle(appScrollbar).Children(func() {
		card := ui.Column(c).FillWidth().Padding(16, 18).Gap(12)
		applySurface(card, p, 13).Children(func() {
			ui.Text(c, "我的服务器").FontSize(15.5).Bold()
			ui.Text(c, "连接你自己的 Emby 或 Jellyfin。小幕不内置媒体库，地址和登录只保存在本机。登录后可以浏览节目，并在播放器里直接播放原文件。").
				FontSize(12.5).TextColor(p.textMuted)
			if len(a.media.servers) == 0 {
				ui.Text(c, "还没有服务器。点右上角「添加服务器」，填写地址、用户名和密码。").
					FontSize(13).TextColor(p.textFaint)
				return
			}
			for _, srv := range a.media.servers {
				a.mediaServerRow(c, srv)
			}
		})
	})
}

func (a *app) mediaServerRow(c *ui.Context, srv savedMediaServer) {
	p := paletteFor(c)
	row := ui.Row(c).Key("media-server-"+srv.ID).FillWidth().Padding(10, 12).Gap(10).AlignItems(ui.Center)
	applyInnerSurface(row, p, 10).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, srv.Name).FontSize(14).Bold().SingleLine()
			meta := srv.productLabel()
			if srv.Username != "" {
				meta += " · " + srv.Username
			}
			ui.Text(c, meta).FontSize(12).TextColor(p.textMuted).SingleLine()
			if srv.BaseURL != "" {
				ui.Text(c, srv.BaseURL).FontSize(11.5).TextColor(p.textFaint).SingleLine().Selectable()
			}
		})
		if glassButton(c, "进入").Label("进入 " + srv.Name).Clicked() {
			a.openMediaServer(srv.ID)
		}
		if glassButton(c, "重新登录").Label("重新登录 " + srv.Name).Clicked() {
			a.openAddMediaServer(srv.ID)
		}
		if a.armedDangerButton(c, "media-remove-"+srv.ID, "移除", "确认移除？") {
			a.removeMediaServer(srv.ID)
		}
	})
}

func (a *app) mediaServerDialog(c *ui.Context) {
	m := &a.media
	if m.addOpen && !m.addWasOpen {
		m.addWasOpen = true
	}
	p := paletteFor(c)
	_, winH := c.Size()
	keyedDialog(c, "dlg-media-server", &m.addOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(p.dialogScrim).Transition(transDialogScrim)
		panel.Width(520).MaxWidth(520).MaxHeight(max(float32(280), winH-32)).Padding(24).Gap(12).Radius(26).TextColor(p.text)
		applyPane(panel, p, p.dialogMat)
		applyDialogPanelPose(c, panel)
		title := "添加服务器"
		if m.replaceID != "" {
			title = "重新登录"
		}
		ui.Text(c, title).FontSize(24).Bold()
		ui.Text(c, "填写你自己的 Emby 或 Jellyfin 地址。密码只用于这次登录，不会写入磁盘；保存的是服务器返回的访问令牌。").
			TextColor(p.textMuted)
		if ui.TextInput(c, &m.addURL).Label("服务器地址").Placeholder("https://emby.example.com:8096").FillWidth().AutoFocus().Submitted() && !a.running(jobSite) {
			a.submitMediaServer()
		}
		if ui.TextInput(c, &m.addUser).Label("用户名").Placeholder("用户名").FillWidth().Submitted() && !a.running(jobSite) {
			a.submitMediaServer()
		}
		if ui.TextInput(c, &m.addPass).Label("密码").Placeholder("没有密码可留空").Password().FillWidth().Submitted() && !a.running(jobSite) {
			a.submitMediaServer()
		}
		if m.addErr != "" {
			ui.Text(c.Key("media-add-err"), m.addErr).FontSize(13).TextColor(p.warning).Transition(transInlineBanner)
		}
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if glassButton(c, "取消").Clicked() {
				m.addOpen = false
			}
			label := "连接"
			if a.running(jobSite) {
				label = "连接中…"
			}
			if primaryButton(c, label).Disabled(a.running(jobSite)).Clicked() {
				a.submitMediaServer()
			}
		})
	})
	if !m.addOpen && m.addWasOpen {
		m.addWasOpen = false
		m.addPass = ""
		m.addErr = ""
	}
}

func (a *app) mediaLibraryView(c *ui.Context) {
	p := paletteFor(c)
	m := &a.media
	ui.Column(c).Fill().Gap(12).Children(func() {
		if path := m.crumbPath(); path != "" {
			ui.Text(c, path).FontSize(12.5).TextColor(p.textMuted).SingleLine()
		}
		ui.Row(c).FillWidth().Padding(0, scrollGutter, 0, 0).Gap(10).AlignItems(ui.Center).Children(func() {
			state := "选择一个库。"
			if a.running(jobSite) {
				state = "正在读取…"
			} else if m.err != "" {
				state = m.err
			} else if len(m.stack) > 0 && m.stack[len(m.stack)-1].ID != "" {
				state = fmt.Sprintf("第 %d 页", m.start/mediaPageSize+1)
			} else if m.total > 0 {
				state = fmt.Sprintf("%d 个库", m.total)
			}
			ui.Text(c, state).FontSize(12.5).TextColor(p.textMuted).Grow(1)
			inFolder := len(m.stack) > 0 && m.stack[len(m.stack)-1].ID != ""
			if inFolder {
				if glassButton(c, "上一页").Padding(5, 12).Disabled(m.start <= 0).Clicked() {
					a.media.start = max(0, a.media.start-mediaPageSize)
					a.loadMediaBrowse()
				}
				if glassButton(c, "下一页").Padding(5, 12).Disabled(m.start+len(m.items) >= m.total).Clicked() {
					a.media.start += mediaPageSize
					a.loadMediaBrowse()
				}
			}
		})
		if len(m.items) == 0 {
			if a.running(jobSite) {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					loadingIndicator(c, 14, p.textMuted)
					ui.Text(c, "正在读取媒体库…").FontSize(13).TextColor(p.textMuted)
				})
				return
			}
			title, hint := "这里还没有影片", "这个目录里没有可浏览的视频。"
			if m.err != "" {
				title, hint = "没有读到内容", m.err
			} else if len(m.stack) <= 1 {
				title, hint = "没有可浏览的影视库", "音乐、照片和直播库不会出现在这里。"
			}
			emptyState(c, title, hint)
			return
		}
		m.grid.Selected = &m.selected
		m.grid.Label = func(i int) string {
			if i < 0 || i >= len(m.items) {
				return ""
			}
			return m.items[i].Name
		}
		m.grid.Key = func(i int) any {
			if i < 0 || i >= len(m.items) {
				return i
			}
			return m.items[i].ID
		}
		grid := ui.GridView(c, &m.grid, len(m.items), 152, 252, func(i int) {
			item := m.items[i]
			card := a.videoCard(c, mediaItemVideo(item, m.activeID), "", false)
			if card.Clicked() {
				a.openMediaItem(item)
			}
		}).Gap(14).Padding(0, scrollGutter, 0, 0).ScrollbarStyle(appScrollbar).Grow(1).FillWidth()
		if grid.Submitted() && m.selected >= 0 && m.selected < len(m.items) {
			item := m.items[m.selected]
			m.selected = -1
			a.openMediaItem(item)
		}
	})
}

func newLocalID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}

// mediaPosterServerQuery selects which saved server should authorize a poster
// request. It is not a credential. The image loader strips it before the
// request leaves the machine, and the access token stays in a header.
const mediaPosterServerQuery = "xiaomu_server"

// Poster images are loaded by the shared image client, which cannot take
// per-URL headers. Tokens stay out of the image URL. A host:port is not an
// identity: two accounts, or two servers under different path prefixes, can
// share one. Lookup uses the saved server id when the URL carries one, and
// otherwise a unique scheme + host + port + path prefix.
var (
	mediaPosterMu  sync.RWMutex
	mediaPosterIdx mediaPosterIndex
)

type posterServer struct {
	id      string
	session mediaserver.Session
	scheme  string
	host    string
	path    string
}

type mediaPosterIndex struct {
	byID    map[string]posterServer
	ordered []posterServer
}

func (a *app) syncMediaPosterAuth() {
	idx := mediaPosterIndex{
		byID: make(map[string]posterServer, len(a.media.servers)),
	}
	for _, s := range a.media.servers {
		srv, ok := posterServerFrom(s)
		if !ok {
			continue
		}
		if _, exists := idx.byID[s.ID]; exists {
			continue
		}
		idx.byID[s.ID] = srv
		idx.ordered = append(idx.ordered, srv)
	}
	mediaPosterMu.Lock()
	mediaPosterIdx = idx
	mediaPosterMu.Unlock()
}

func posterServerFrom(s savedMediaServer) (posterServer, bool) {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.AccessToken) == "" {
		return posterServer{}, false
	}
	u, err := url.Parse(strings.TrimSpace(s.BaseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return posterServer{}, false
	}
	return posterServer{
		id:      s.ID,
		session: s.session(),
		scheme:  strings.ToLower(u.Scheme),
		host:    strings.ToLower(u.Host),
		path:    cleanPosterPath(u.EscapedPath()),
	}, true
}

func cleanPosterPath(p string) string {
	p = pathUnescape(strings.TrimRight(p, "/"))
	if p == "/" || p == "." {
		return ""
	}
	return p
}

func pathUnescape(p string) string {
	if decoded, err := url.PathUnescape(p); err == nil {
		return decoded
	}
	return p
}

func (idx mediaPosterIndex) moreSpecific(u *url.URL, srv posterServer) bool {
	for _, other := range idx.ordered {
		if other.id == srv.id || other.scheme != srv.scheme || other.host != srv.host {
			continue
		}
		if len(other.path) > len(srv.path) && other.matches(u) {
			return true
		}
	}
	return false
}

func (s posterServer) matches(u *url.URL) bool {
	if u == nil || !strings.EqualFold(u.Scheme, s.scheme) || !strings.EqualFold(u.Host, s.host) {
		return false
	}
	path := cleanPosterPath(u.EscapedPath())
	if s.path == "" {
		return true
	}
	return path == s.path || strings.HasPrefix(path, s.path+"/")
}

func stampMediaPosterURL(raw, serverID string) string {
	raw = strings.TrimSpace(raw)
	serverID = strings.TrimSpace(serverID)
	if raw == "" || serverID == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return raw
	}
	q := u.Query()
	q.Set(mediaPosterServerQuery, serverID)
	u.RawQuery = q.Encode()
	return u.String()
}

func lookupMediaPoster(u *url.URL) (mediaserver.Session, bool) {
	if u == nil || u.Host == "" {
		return mediaserver.Session{}, false
	}
	mediaPosterMu.RLock()
	defer mediaPosterMu.RUnlock()
	if id := strings.TrimSpace(u.Query().Get(mediaPosterServerQuery)); id != "" {
		srv, ok := mediaPosterIdx.byID[id]
		if !ok || !srv.matches(u) || mediaPosterIdx.moreSpecific(u, srv) {
			return mediaserver.Session{}, false
		}
		return srv.session, true
	}
	var best posterServer
	bestLen := -1
	found := false
	ambiguous := false
	for _, srv := range mediaPosterIdx.ordered {
		if !srv.matches(u) {
			continue
		}
		n := len(srv.path)
		switch {
		case !found || n > bestLen:
			best, bestLen, found, ambiguous = srv, n, true, false
		case n == bestLen:
			ambiguous = true
		}
	}
	if !found || ambiguous {
		return mediaserver.Session{}, false
	}
	return best.session, true
}

func mediaPosterHeaders(u *url.URL) map[string]string {
	sess, ok := lookupMediaPoster(u)
	if !ok {
		return nil
	}
	return mediaserver.AuthHeaders(sess)
}

func stripMediaPosterQuery(u *url.URL) {
	if u == nil || u.RawQuery == "" || !strings.Contains(u.RawQuery, mediaPosterServerQuery) {
		return
	}
	q := u.Query()
	if _, ok := q[mediaPosterServerQuery]; !ok {
		return
	}
	q.Del(mediaPosterServerQuery)
	u.RawQuery = q.Encode()
}
