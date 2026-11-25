// Package alist natively adapts TVBox csp_Alist / OpenList sites over standard
// HTTP JSON APIs (/api/fs/list, /api/fs/get, /api/fs/search) without Android jars.
package alist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/httpx"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
)

const (
	pageSize          = 40
	maxSearchDrives   = 8
	driveOpTimeout    = 7 * time.Second
	maxDirVideoExpand = 200
)

// Drive describes one AList / OpenList mount inside a TVBox csp_Alist config.
type Drive struct {
	Name      string
	Server    string
	StartPath string
	Password  string
	Search    bool
}

// Session implements source.Session for a TVBox csp_Alist / alist.min.js site.
type Session struct {
	drives []Drive
	vodPic string
	client *http.Client
}

// Open parses the site's AList ext (shorthand map, standard drives array, or
// remote JSON URL) and returns a native session whose categories are the drives.
func Open(ctx context.Context, site config.Site) (*Session, []catalog.Category, error) {
	raw := strings.TrimSpace(site.Ext)
	if raw == "" {
		return nil, nil, fmt.Errorf("AList 站点缺少配置内容")
	}
	if !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[") {
		res, err := config.HTTPFetcher(nil)(ctx, raw)
		if err != nil {
			return nil, nil, fmt.Errorf("读取 AList 配置失败：%w", err)
		}
		if res.Status < 200 || res.Status >= 300 {
			return nil, nil, fmt.Errorf("读取 AList 配置失败：HTTP %d", res.Status)
		}
		body, err := config.Decode(res.Body)
		if err != nil {
			body = res.Body
		}
		raw = string(body)
	}
	drives, vodPic, err := parseDrives(raw)
	if err != nil {
		return nil, nil, err
	}
	if len(drives) == 0 {
		return nil, nil, fmt.Errorf("AList 配置中没有可用的站点地址")
	}
	s := &Session{
		drives: drives,
		vodPic: vodPic,
		client: httpx.NewClient(12 * time.Second),
	}
	cats := make([]catalog.Category, 0, len(drives))
	for i, d := range drives {
		cats = append(cats, catalog.Category{
			ID:   catalog.ID(strconv.Itoa(i)),
			Name: d.Name,
		})
	}
	return s, cats, nil
}

func parseDrives(raw string) ([]Drive, string, error) {
	clean := config.Strip([]byte(raw))
	var standard struct {
		VodPic string `json:"vodPic"`
		Drives []struct {
			Name      string `json:"name"`
			Server    string `json:"server"`
			StartPath string `json:"startPath"`
			Password  string `json:"password"`
			Search    *bool  `json:"search"`
			Hidden    bool   `json:"hidden"`
		} `json:"drives"`
	}
	if json.Unmarshal(clean, &standard) == nil && len(standard.Drives) > 0 {
		out := make([]Drive, 0, len(standard.Drives))
		for _, d := range standard.Drives {
			if d.Hidden {
				continue
			}
			drv, ok := normalizeDrive(d.Name, d.Server, d.StartPath, d.Password, d.Search == nil || *d.Search)
			if ok {
				out = append(out, drv)
			}
		}
		return out, strings.TrimSpace(standard.VodPic), nil
	}

	// Array of drive objects directly.
	var arr []struct {
		Name      string `json:"name"`
		Server    string `json:"server"`
		StartPath string `json:"startPath"`
		Password  string `json:"password"`
		Search    *bool  `json:"search"`
	}
	if json.Unmarshal(clean, &arr) == nil && len(arr) > 0 {
		out := make([]Drive, 0, len(arr))
		for _, d := range arr {
			drv, ok := normalizeDrive(d.Name, d.Server, d.StartPath, d.Password, d.Search == nil || *d.Search)
			if ok {
				out = append(out, drv)
			}
		}
		return out, "", nil
	}

	// Shorthand map: {"Drive Name": "https://host/optional/subpath"}
	var short map[string]string
	if err := json.Unmarshal(clean, &short); err != nil {
		return nil, "", fmt.Errorf("解析 AList 配置失败：%w", err)
	}
	names := make([]string, 0, len(short))
	for name := range short {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Drive, 0, len(names))
	for _, name := range names {
		drv, ok := normalizeDrive(name, short[name], "", "", true)
		if ok {
			out = append(out, drv)
		}
	}
	return out, "", nil
}

func normalizeDrive(name, rawServer, startPath, password string, search bool) (Drive, bool) {
	u, err := url.Parse(strings.TrimSpace(rawServer))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Drive{}, false
	}
	base := u.Scheme + "://" + u.Host
	p := strings.TrimSpace(startPath)
	if p == "" {
		p = u.Path
	}
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = path.Clean(p)
	name = strings.TrimSpace(name)
	if name == "" {
		name = u.Host
	}
	return Drive{
		Name:      name,
		Server:    base,
		StartPath: p,
		Password:  password,
		Search:    search,
	}, true
}

type fsEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	IsDir  bool   `json:"is_dir"`
	Thumb  string `json:"thumb"`
	Sign   string `json:"sign"`
	Parent string `json:"parent"`
	RawURL string `json:"raw_url"`
}

func (s *Session) apiPost(ctx context.Context, server, endpoint string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "xiaomu/1.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer httpx.DrainAndClose(resp.Body)
	raw, _, err := httpx.ReadLimit(resp.Body, resp.ContentLength, 4<<20)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("AList 返回 HTTP %d", resp.StatusCode)
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if env.Code != 200 && env.Code != 0 {
		if env.Message != "" {
			return fmt.Errorf("AList 返回错误：%s", env.Message)
		}
		return fmt.Errorf("AList 返回错误码 %d", env.Code)
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func (s *Session) listDir(ctx context.Context, d Drive, dirPath string, page, perPage int) ([]fsEntry, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage <= 0 {
		perPage = pageSize
	}
	var data struct {
		Content []fsEntry `json:"content"`
		Total   int       `json:"total"`
	}
	err := s.apiPost(ctx, d.Server, "/api/fs/list", map[string]any{
		"path":     dirPath,
		"password": d.Password,
		"page":     page,
		"per_page": perPage,
		"refresh":  false,
	}, &data)
	if err != nil {
		return nil, 0, err
	}
	return data.Content, data.Total, nil
}

func encodeItemID(driveIdx int, isDir bool, fullPath string) string {
	kind := "f"
	if isDir {
		kind = "d"
	}
	return fmt.Sprintf("alist:%d:%s:%s", driveIdx, kind, fullPath)
}

func decodeItemID(raw string) (driveIdx int, isDir bool, fullPath string, ok bool) {
	if !strings.HasPrefix(raw, "alist:") {
		return 0, false, "", false
	}
	rest := strings.TrimPrefix(raw, "alist:")
	idxStr, afterIdx, ok1 := strings.Cut(rest, ":")
	kind, p, ok2 := strings.Cut(afterIdx, ":")
	if !ok1 || !ok2 {
		return 0, false, "", false
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 || p == "" {
		return 0, false, "", false
	}
	return idx, kind == "d", p, true
}

func (s *Session) Home(ctx context.Context) (catalog.Page, error) {
	// Return items from the first responsive drive without stalling home feed.
	limit := min(3, len(s.drives))
	var lastErr error
	for i := range limit {
		dctx, cancel := context.WithTimeout(ctx, driveOpTimeout)
		p, err := s.Category(dctx, strconv.Itoa(i), 1)
		cancel()
		if err == nil && len(p.Videos) > 0 {
			return p, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	return catalog.Page{}, lastErr
}

func (s *Session) Category(ctx context.Context, id string, page int) (catalog.Page, error) {
	idx, err := strconv.Atoi(id)
	if err != nil || idx < 0 || idx >= len(s.drives) {
		return catalog.Page{}, fmt.Errorf("无效的 AList 站点编号")
	}
	d := s.drives[idx]
	entries, total, err := s.listDir(ctx, d, d.StartPath, page, pageSize)
	if err != nil {
		return catalog.Page{}, err
	}
	videos := make([]catalog.Video, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir && !pan.IsVideoFilename(e.Name) {
			continue
		}
		full := path.Join(d.StartPath, e.Name)
		remarks := d.Name
		if e.IsDir {
			remarks = "目录 · " + d.Name
		} else {
			remarks = pan.ContainerLabel(e.Name) + " · " + d.Name
		}
		pic := e.Thumb
		if pic == "" {
			pic = s.vodPic
		}
		videos = append(videos, catalog.Video{
			ID:      catalog.ID(encodeItemID(idx, e.IsDir, full)),
			Name:    pan.DisplayEpisodeName(e.Name),
			Pic:     pic,
			Remarks: remarks,
			Type:    d.Name,
		})
	}
	return catalog.Page{Videos: videos, HasNext: page*pageSize < total}, nil
}

func (s *Session) Search(ctx context.Context, query string, page int) (catalog.Page, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return catalog.Page{}, nil
	}
	if page < 1 {
		page = 1
	}
	var searchable []int
	for i, d := range s.drives {
		if d.Search {
			searchable = append(searchable, i)
		}
	}
	if len(searchable) > maxSearchDrives {
		searchable = searchable[:maxSearchDrives]
	}
	type hit struct {
		videos []catalog.Video
		more   bool
	}
	results := make([]hit, len(searchable))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for pos, driveIdx := range searchable {
		wg.Add(1)
		go func(slot, idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			d := s.drives[idx]
			dctx, cancel := context.WithTimeout(ctx, driveOpTimeout)
			defer cancel()
			var data struct {
				Content []fsEntry `json:"content"`
				Total   int       `json:"total"`
			}
			err := s.apiPost(dctx, d.Server, "/api/fs/search", map[string]any{
				"parent":   d.StartPath,
				"keywords": query,
				"scope":    0,
				"page":     page,
				"per_page": 20,
				"password": d.Password,
			}, &data)
			if err != nil {
				return
			}
			var videos []catalog.Video
			for _, e := range data.Content {
				if !e.IsDir && !pan.IsVideoFilename(e.Name) {
					continue
				}
				parent := e.Parent
				if parent == "" {
					parent = d.StartPath
				}
				full := path.Join(parent, e.Name)
				remarks := d.Name
				if e.IsDir {
					remarks = "目录 · " + d.Name
				} else {
					remarks = pan.ContainerLabel(e.Name) + " · " + d.Name
				}
				pic := e.Thumb
				if pic == "" {
					pic = s.vodPic
				}
				videos = append(videos, catalog.Video{
					ID:      catalog.ID(encodeItemID(idx, e.IsDir, full)),
					Name:    pan.DisplayEpisodeName(e.Name),
					Pic:     pic,
					Remarks: remarks,
					Type:    d.Name,
				})
			}
			results[slot] = hit{videos: videos, more: page*20 < data.Total}
		}(pos, driveIdx)
	}
	wg.Wait()

	var all []catalog.Video
	hasNext := false
	for _, r := range results {
		all = append(all, r.videos...)
		hasNext = hasNext || r.more
	}
	return catalog.Page{Videos: all, HasNext: hasNext}, nil
}

func (s *Session) Detail(ctx context.Context, id string) (catalog.Detail, error) {
	idx, isDir, fullPath, ok := decodeItemID(id)
	if !ok || idx >= len(s.drives) {
		return catalog.Detail{}, fmt.Errorf("无效的 AList 资源地址")
	}
	d := s.drives[idx]
	title := pan.DisplayEpisodeName(path.Base(fullPath))
	if !isDir {
		return catalog.Detail{
			Name: title,
			Pic:  s.vodPic,
			Type: d.Name,
			Sources: []catalog.Source{{
				Name: d.Name,
				Episodes: []catalog.Episode{{
					Name: title,
					ID:   encodeItemID(idx, false, fullPath),
				}},
			}},
		}, nil
	}

	// Expand directory (up to depth 2) to collect all episodes inside.
	var eps []catalog.Episode
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		entries, _, err := s.listDir(ctx, d, dir, 1, maxDirVideoExpand)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if len(eps) >= maxDirVideoExpand {
				break
			}
			child := path.Join(dir, e.Name)
			if e.IsDir {
				if depth < 2 {
					_ = walk(child, depth+1)
				}
				continue
			}
			if !pan.IsVideoFilename(e.Name) {
				continue
			}
			eps = append(eps, catalog.Episode{
				Name: pan.DisplayEpisodeName(e.Name),
				ID:   encodeItemID(idx, false, child),
			})
		}
		return nil
	}
	if err := walk(fullPath, 0); err != nil {
		return catalog.Detail{}, err
	}
	if len(eps) == 0 {
		return catalog.Detail{}, fmt.Errorf("目录 %q 下没有找到可播放的视频文件", title)
	}
	sort.SliceStable(eps, func(i, j int) bool {
		return pan.NaturalLess(eps[i].Name, eps[j].Name)
	})
	return catalog.Detail{
		Name: title,
		Pic:  s.vodPic,
		Type: d.Name,
		Sources: []catalog.Source{{
			Name:     d.Name,
			Episodes: eps,
		}},
	}, nil
}

func (s *Session) Play(ctx context.Context, _ string, ep catalog.Episode) (catalog.Playback, error) {
	idx, _, fullPath, ok := decodeItemID(ep.ID)
	if !ok || idx >= len(s.drives) {
		return catalog.Playback{}, fmt.Errorf("无效的 AList 文件路径")
	}
	d := s.drives[idx]
	var info fsEntry
	if err := s.apiPost(ctx, d.Server, "/api/fs/get", map[string]any{
		"path":     fullPath,
		"password": d.Password,
	}, &info); err != nil {
		return catalog.Playback{}, fmt.Errorf("获取 AList 播放直链失败：%w", err)
	}
	rawURL := strings.TrimSpace(info.RawURL)
	if rawURL == "" {
		// Fallback to AList's standard /d/ download redirect path.
		rawURL = d.Server + "/d" + escapePath(fullPath)
		if info.Sign != "" {
			rawURL += "?sign=" + url.QueryEscape(info.Sign)
		}
	}
	filename := info.Name
	if filename == "" {
		filename = path.Base(fullPath)
	}
	native := pan.IsNativePlayableFilename(filename)
	label := "原画 " + pan.ContainerLabel(filename)
	stream := catalog.Stream{
		Label:          label,
		Quality:        "original",
		URL:            rawURL,
		Filename:       filename,
		NativePlayable: native,
		IsHLS:          strings.EqualFold(path.Ext(filename), ".m3u8"),
		IsOriginal:     true,
		Size:           info.Size,
	}
	return catalog.Playback{
		URL:      rawURL,
		Filename: filename,
		Streams:  []catalog.Stream{stream},
	}, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (s *Session) Close() {}
