// Package mediaserver talks to a user-supplied Emby or Jellyfin server.
//
// The two servers share the MediaBrowser API shape, with a documented split
// this client follows instead of assuming the headers are interchangeable:
//
//   - Emby (https://dev.emby.media/doc/restapi/User-Authentication.html and
//     Video-Streaming.html): authorize with X-Emby-Authorization using the
//     Emby scheme, and send the access token in X-Emby-Token. Direct playback
//     is GET /Videos/{id}/stream.{container}?static=true with MediaSourceId
//     and PlaySessionId. static=true serves the original file.
//   - Jellyfin (https://github.com/jellyfin/jellyfin/blob/master/Jellyfin.Server.Implementations/Security/AuthorizationContext.cs
//     and the generated Videos API): the supported header is
//     Authorization: MediaBrowser Client=..., Device=..., DeviceId=..., Version=..., Token=...
//     X-Emby-Token is legacy and may be disabled. Direct playback is the same
//     static stream route; static=true streams the original file without encoding.
//
// Login is POST /Users/AuthenticateByName with {"Username","Pw"} on both.
// Library browsing is GET /Users/{id}/Views and GET /Users/{id}/Items.
// Responses may be PascalCase (Emby, or Jellyfin with the PascalCase profile)
// or camelCase; decoding accepts either.
//
// Nothing in this package contains a built-in server address. Callers pass
// the base URL the user typed.
package mediaserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/httpx"
)

const (
	clientName    = "Xiaomu"
	clientVersion = "1"
	deviceName    = "Xiaomu Desktop"
	pageLimit     = 40
	bodyLimit     = 4 << 20
)

// Product is the server implementation detected from /System/Info/Public.
type Product string

const (
	ProductEmby     Product = "emby"
	ProductJellyfin Product = "jellyfin"
)

// Sort values accepted by both servers' Items query.
const (
	SortName  = "SortName"
	SortIndex = "IndexNumber"
)

var (
	// ErrBadLogin is a rejected username or password.
	ErrBadLogin = errors.New("用户名或密码不正确")
	// ErrUnauthorized means a saved access token was rejected.
	ErrUnauthorized = errors.New("登录已失效，请重新登录这台服务器")
	// ErrDirectPlay means the server can only offer a transcode for this item.
	ErrDirectPlay = errors.New("该条目需要转码，当前版本只直接播放原文件")
	// ErrNotVideo means the item is a folder or another non-playable type.
	ErrNotVideo = errors.New("这个条目不能直接播放")
)

// Session is a logged-in user on one server. The access token is a secret
// and must stay on the machine that logged in.
type Session struct {
	BaseURL     string
	Product     Product
	ServerName  string
	UserID      string
	Username    string
	AccessToken string
	ServerID    string
	DeviceID    string
}

// PublicInfo is the unauthenticated identity of a server.
type PublicInfo struct {
	ServerName  string
	Version     string
	ProductName string
	ID          string
}

// Item is a library, folder, or video. ImageURL never carries the access token.
type Item struct {
	ID                string
	Name              string
	Type              string
	CollectionType    string
	Year              string
	IndexNumber       int
	ParentIndexNumber int
	SeriesName        string
	ImageURL          string
	Folder            bool
	Playable          bool
}

// Page is one page of children.
type Page struct {
	Items []Item
	Total int
}

// Playback is a direct-play URL. Headers carry the token; URL does not.
type Playback struct {
	URL           string
	Headers       map[string]string
	Container     string
	MediaSourceID string
}

// Client calls one server.
type Client struct {
	base string
	http *http.Client
}

// NormalizeBaseURL checks a user-typed server address. A missing scheme
// means http. Userinfo, queries, and fragments are rejected so credentials
// cannot hide in the address.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("请填写服务器地址")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("请填写 http 或 https 服务器地址")
	}
	if u.User != nil {
		return "", errors.New("请不要把用户名或密码写在地址里")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("请只填写服务器地址，不要附带参数")
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	if strings.Contains(p, "..") {
		return "", errors.New("请填写 http 或 https 服务器地址")
	}
	if p == "/" || p == "." {
		p = ""
	}
	return u.Scheme + "://" + u.Host + p, nil
}

// New returns a client for a normalized base URL.
func New(baseURL string) (*Client, error) {
	base, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	httpClient := httpx.NewClient(20 * time.Second)
	c := &Client{base: base, http: httpClient}
	httpClient.CheckRedirect = c.checkRedirect
	return c, nil
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("重定向次数过多")
	}
	if req.URL == nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return errors.New("重定向到了不支持的地址")
	}
	if len(via) > 0 && via[0].URL != nil && !sameOrigin(via[0].URL, req.URL) {
		// A 307/308 would resend the request body (the login password) to
		// the other address. Refuse instead of following it.
		if req.Body != nil && req.Body != http.NoBody {
			return errors.New("服务器把请求重定向到了其他地址")
		}
		req.Header.Del("Authorization")
		req.Header.Del("X-Emby-Authorization")
		req.Header.Del("X-Emby-Token")
	}
	return nil
}

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// ClassifyProduct reads ProductName from /System/Info/Public.
func ClassifyProduct(productName string) Product {
	n := strings.ToLower(productName)
	switch {
	case strings.Contains(n, "jellyfin"):
		return ProductJellyfin
	case strings.Contains(n, "emby"):
		return ProductEmby
	default:
		return ""
	}
}

// Connect reads the public server info, then logs in. deviceID should be
// stable for a saved server so the token stays bound to the same device;
// an empty id gets a new random one.
func Connect(ctx context.Context, baseURL, username, password, deviceID string) (Session, error) {
	implicit := !strings.Contains(strings.TrimSpace(baseURL), "://")
	c, err := New(baseURL)
	if err != nil {
		return Session{}, err
	}
	info, final, err := c.publicInfoFinal(ctx)
	if err != nil {
		return Session{}, err
	}
	// A bare host name defaults to http. If the probe was redirected to
	// https on the same host, confirm that address before the password
	// is sent; an explicitly typed http address (a LAN server) is kept.
	if implicit {
		if upgraded, ok := upgradedBase(c.base, final); ok {
			if nc, err := New(upgraded); err == nil {
				c = nc
			}
		}
	}
	if strings.TrimSpace(deviceID) == "" {
		deviceID, err = newDeviceID()
		if err != nil {
			return Session{}, err
		}
	}
	sess, err := c.Login(ctx, ClassifyProduct(info.ProductName), deviceID, username, password)
	if err != nil {
		return Session{}, err
	}
	if sess.ServerName == "" {
		sess.ServerName = strings.TrimSpace(info.ServerName)
	}
	if sess.ServerID == "" {
		sess.ServerID = strings.TrimSpace(info.ID)
	}
	if sess.ServerName == "" {
		if u, err := url.Parse(c.base); err == nil {
			sess.ServerName = u.Host
		}
	}
	return sess, nil
}

// PublicInfo calls GET /System/Info/Public.
func (c *Client) PublicInfo(ctx context.Context) (PublicInfo, error) {
	info, _, err := c.publicInfoFinal(ctx)
	return info, err
}

// publicInfoFinal also reports the URL the probe landed on after redirects,
// so Connect can confirm the effective address before sending a password.
func (c *Client) publicInfoFinal(ctx context.Context) (PublicInfo, string, error) {
	var raw struct {
		ServerName  string `json:"servername"`
		Version     string `json:"version"`
		ProductName string `json:"productname"`
		ID          string `json:"id"`
	}
	final, err := c.doFinal(ctx, Session{}, http.MethodGet, "/System/Info/Public", nil, nil, &raw)
	if err != nil {
		return PublicInfo{}, "", err
	}
	return PublicInfo{
		ServerName:  strings.TrimSpace(raw.ServerName),
		Version:     strings.TrimSpace(raw.Version),
		ProductName: strings.TrimSpace(raw.ProductName),
		ID:          strings.TrimSpace(raw.ID),
	}, final, nil
}

// upgradedBase returns the https base to use when the public probe for an
// http base landed on https on the same host. Anything else (a different
// host, a still-http landing, an unexpected final path) keeps the base.
func upgradedBase(base, final string) (string, bool) {
	b, err := url.Parse(base)
	if err != nil || b.Scheme != "http" {
		return "", false
	}
	f, err := url.Parse(final)
	if err != nil || f.Scheme != "https" || !strings.EqualFold(f.Hostname(), b.Hostname()) {
		return "", false
	}
	p := f.EscapedPath()
	if !strings.HasSuffix(p, "/System/Info/Public") {
		return "", false
	}
	p = strings.TrimRight(strings.TrimSuffix(p, "/System/Info/Public"), "/")
	return "https://" + f.Host + p, true
}

// Login calls POST /Users/AuthenticateByName.
func (c *Client) Login(ctx context.Context, product Product, deviceID, username, password string) (Session, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return Session{}, errors.New("请填写用户名")
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		var err error
		deviceID, err = newDeviceID()
		if err != nil {
			return Session{}, err
		}
	}
	probe := Session{BaseURL: c.base, Product: product, DeviceID: deviceID}
	var raw struct {
		AccessToken string `json:"accesstoken"`
		ServerID    string `json:"serverid"`
		User        struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
	}
	body := map[string]string{"Username": username, "Pw": password}
	if err := c.do(ctx, probe, http.MethodPost, "/Users/AuthenticateByName", nil, body, &raw); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return Session{}, ErrBadLogin
		}
		return Session{}, err
	}
	token := cleanHeaderValue(raw.AccessToken)
	if token == "" || strings.TrimSpace(raw.User.ID) == "" {
		return Session{}, errors.New("服务器没有返回登录信息")
	}
	name := strings.TrimSpace(raw.User.Name)
	if name == "" {
		name = username
	}
	return Session{
		BaseURL:     c.base,
		Product:     product,
		UserID:      strings.TrimSpace(raw.User.ID),
		Username:    name,
		AccessToken: token,
		ServerID:    strings.TrimSpace(raw.ServerID),
		DeviceID:    deviceID,
	}, nil
}

// Libraries lists video libraries for the signed-in user. Music, photos,
// books, and live TV are omitted.
func Libraries(ctx context.Context, s Session) ([]Item, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	c, err := New(s.BaseURL)
	if err != nil {
		return nil, err
	}
	items, _, err := c.items(ctx, s, "/Users/"+url.PathEscape(s.UserID)+"/Views", nil)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if !visibleLibrary(it.CollectionType) {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

// Children lists one page of videos and folders inside parentID.
func Children(ctx context.Context, s Session, parentID, sort string, start, limit int) (Page, error) {
	if err := s.validate(); err != nil {
		return Page{}, err
	}
	c, err := New(s.BaseURL)
	if err != nil {
		return Page{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = pageLimit
	}
	if start < 0 {
		start = 0
	}
	if sort != SortIndex {
		sort = SortName
	}
	q := url.Values{}
	q.Set("ParentId", parentID)
	q.Set("SortBy", sort)
	q.Set("SortOrder", "Ascending")
	q.Set("StartIndex", strconv.Itoa(start))
	q.Set("Limit", strconv.Itoa(limit))
	q.Set("Recursive", "false")
	q.Set("Fields", "ProductionYear,ChildCount")
	q.Set("IncludeItemTypes", "Movie,Series,Season,Episode,Video,Folder,BoxSet,CollectionFolder")
	q.Set("EnableImageTypes", "Primary")
	q.Set("ImageTypeLimit", "1")
	q.Set("EnableTotalRecordCount", "true")
	items, total, err := c.items(ctx, s, "/Users/"+url.PathEscape(s.UserID)+"/Items", q)
	if err != nil {
		return Page{}, err
	}
	if total < start+len(items) {
		total = start + len(items)
	}
	return Page{Items: items, Total: total}, nil
}

// DirectPlay builds a static original-file URL for itemID.
func DirectPlay(ctx context.Context, s Session, itemID string) (Playback, error) {
	if err := s.validate(); err != nil {
		return Playback{}, err
	}
	c, err := New(s.BaseURL)
	if err != nil {
		return Playback{}, err
	}
	return c.directPlay(ctx, s, itemID)
}

func (s Session) validate() error {
	if strings.TrimSpace(s.BaseURL) == "" || strings.TrimSpace(s.UserID) == "" || cleanHeaderValue(s.AccessToken) == "" {
		return ErrUnauthorized
	}
	return nil
}

// Logout revokes the access token. Failure is not fatal for local removal.
func Logout(ctx context.Context, s Session) error {
	if strings.TrimSpace(s.AccessToken) == "" {
		return nil
	}
	c, err := New(s.BaseURL)
	if err != nil {
		return err
	}
	return c.do(ctx, s, http.MethodPost, "/Sessions/Logout", nil, nil, nil)
}

// AuthHeaders returns the headers a later media or image request must send.
// The token is not placed in the URL.
func AuthHeaders(s Session) map[string]string {
	token := cleanHeaderValue(s.AccessToken)
	h := map[string]string{}
	switch s.Product {
	case ProductEmby:
		emby := embyAuthorization(s.UserID, s.DeviceID, token)
		h["Authorization"] = emby
		h["X-Emby-Authorization"] = emby
		if token != "" {
			h["X-Emby-Token"] = token
		}
	case ProductJellyfin:
		h["Authorization"] = jellyfinAuthorization(s.DeviceID, token)
	default:
		h["Authorization"] = jellyfinAuthorization(s.DeviceID, token)
		h["X-Emby-Authorization"] = embyAuthorization(s.UserID, s.DeviceID, token)
		if token != "" {
			h["X-Emby-Token"] = token
		}
	}
	return h
}

func embyAuthorization(userID, deviceID, token string) string {
	parts := []string{
		"Client=" + quoteEmby(clientName),
		"Device=" + quoteEmby(deviceName),
		"DeviceId=" + quoteEmby(deviceID),
		"Version=" + quoteEmby(clientVersion),
	}
	if strings.TrimSpace(userID) != "" {
		parts = append([]string{"UserId=" + quoteEmby(userID)}, parts...)
	}
	if token != "" && !strings.ContainsAny(token, "\",") {
		parts = append(parts, "Token="+quoteEmby(token))
	}
	return "Emby " + strings.Join(parts, ", ")
}

func jellyfinAuthorization(deviceID, token string) string {
	parts := []string{
		"Client=" + quoteJellyfin(clientName),
		"Device=" + quoteJellyfin(deviceName),
		"DeviceId=" + quoteJellyfin(deviceID),
		"Version=" + quoteJellyfin(clientVersion),
	}
	if token != "" {
		parts = append(parts, "Token="+quoteJellyfin(token))
	}
	return "MediaBrowser " + strings.Join(parts, ", ")
}

func quoteEmby(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '"' || r == ',' || r < 32 || r > 126 {
			return '_'
		}
		return r
	}, s)
	return `"` + s + `"`
}

func quoteJellyfin(s string) string {
	// Jellyfin percent-decodes each quoted value. PathEscape leaves
	// unreserved characters unchanged and encodes quotes, commas, and spaces.
	return `"` + url.PathEscape(s) + `"`
}

func (c *Client) directPlay(ctx context.Context, s Session, itemID string) (Playback, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return Playback{}, ErrNotVideo
	}
	q := url.Values{}
	q.Set("Fields", "MediaSources")
	var raw rawItem
	path := "/Users/" + url.PathEscape(s.UserID) + "/Items/" + url.PathEscape(itemID)
	if err := c.do(ctx, s, http.MethodGet, path, q, nil, &raw); err != nil {
		return Playback{}, err
	}
	item := c.mapItem(raw)
	if item.Folder || (item.Type != "" && !item.Playable) {
		return Playback{}, ErrNotVideo
	}
	source, ok := pickDirectSource(raw.MediaSources)
	if !ok {
		if len(raw.MediaSources) > 0 {
			return Playback{}, ErrDirectPlay
		}
		source = mediaSource{ID: itemID}
	}
	if source.ID == "" {
		source.ID = itemID
	}
	container := safeContainer(source.Container)
	if container == "" {
		container = safeContainer(extOf(source.Path))
	}
	target := source.RemoteURL
	if target == "" {
		sid, err := newDeviceID()
		if err != nil {
			return Playback{}, err
		}
		target = streamURL(c.base, itemID, container, source.ID, sid)
	}
	return Playback{
		URL:           target,
		Headers:       headersForTarget(target, s),
		Container:     container,
		MediaSourceID: source.ID,
	}, nil
}

func headersForTarget(target string, s Session) map[string]string {
	u, err := url.Parse(target)
	base, errBase := url.Parse(s.BaseURL)
	if err != nil || errBase != nil || !sameOrigin(base, u) {
		return nil
	}
	return AuthHeaders(s)
}

func streamURL(base, itemID, container, mediaSourceID, playSessionID string) string {
	p := "/Videos/" + url.PathEscape(itemID) + "/stream"
	if container != "" {
		p += "." + container
	}
	q := url.Values{}
	q.Set("static", "true")
	if mediaSourceID != "" {
		q.Set("MediaSourceId", mediaSourceID)
	}
	if playSessionID != "" {
		q.Set("PlaySessionId", playSessionID)
	}
	return strings.TrimRight(base, "/") + p + "?" + q.Encode()
}

type mediaSource struct {
	ID        string
	Container string
	Path      string
	RemoteURL string
	Rank      int
}

func pickDirectSource(sources []rawSource) (mediaSource, bool) {
	best := mediaSource{Rank: -1}
	found := false
	for _, src := range sources {
		rank, ok := src.directRank()
		if !ok || rank < best.Rank {
			continue
		}
		found = true
		best = mediaSource{
			ID:        strings.TrimSpace(src.ID),
			Container: src.Container,
			Path:      src.Path,
			Rank:      rank,
		}
		if remote, ok := httpURL(src.Path); ok && !strings.EqualFold(src.Protocol, "File") && src.Protocol != "" {
			best.RemoteURL = remote
		}
	}
	return best, found
}

type rawSource struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Container    string `json:"container"`
	Protocol     string `json:"protocol"`
	Path         string `json:"path"`
	DirectPlay   *bool  `json:"supportsdirectplay"`
	DirectStream *bool  `json:"supportsdirectstream"`
}

func (s rawSource) directRank() (int, bool) {
	play := s.DirectPlay != nil && *s.DirectPlay
	stream := s.DirectStream != nil && *s.DirectStream
	if s.DirectPlay != nil || s.DirectStream != nil {
		if !play && !stream {
			return 0, false
		}
	}
	protocol := strings.ToLower(strings.TrimSpace(s.Protocol))
	switch protocol {
	case "", "file":
	case "http":
		if _, ok := httpURL(s.Path); !ok {
			return 0, false
		}
	default:
		return 0, false
	}
	rank := 1
	if stream {
		rank = 2
	}
	if play {
		rank = 3
	}
	return rank, true
}

func httpURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	return u.String(), true
}

func safeContainer(raw string) string {
	raw = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "."))
	if i := strings.IndexAny(raw, ",; "); i >= 0 {
		raw = raw[:i]
	}
	switch raw {
	case "mkv", "mp4", "m4v", "avi", "ts", "m2ts", "webm", "mov", "wmv", "mpeg", "mpg", "flv", "ogv", "asf", "3gp", "wtv":
		return raw
	default:
		return ""
	}
}

func extOf(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.LastIndex(p, "."); i >= 0 && i > strings.LastIndex(p, "/") {
		return p[i+1:]
	}
	return ""
}

func visibleLibrary(collectionType string) bool {
	switch strings.ToLower(strings.TrimSpace(collectionType)) {
	case "music", "musicvideos", "photos", "books", "livetv", "playlists":
		return false
	default:
		return true
	}
}

type rawItem struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Type              string            `json:"type"`
	CollectionType    string            `json:"collectiontype"`
	ProductionYear    int               `json:"productionyear"`
	IndexNumber       int               `json:"indexnumber"`
	ParentIndexNumber int               `json:"parentindexnumber"`
	SeriesName        string            `json:"seriesname"`
	IsFolder          bool              `json:"isfolder"`
	ImageTags         map[string]string `json:"imagetags"`
	MediaSources      []rawSource       `json:"mediasources"`
}

func (c *Client) items(ctx context.Context, s Session, path string, query url.Values) ([]Item, int, error) {
	var raw json.RawMessage
	if err := c.do(ctx, s, http.MethodGet, path, query, nil, &raw); err != nil {
		return nil, 0, err
	}
	list, total, err := decodeItemList(raw)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Item, 0, len(list))
	for _, one := range list {
		it := c.mapItem(one)
		if it.ID == "" || (!it.Folder && !it.Playable) {
			continue
		}
		out = append(out, it)
	}
	if total == 0 {
		total = len(out)
	}
	return out, total, nil
}

func (c *Client) mapItem(raw rawItem) Item {
	it := Item{
		ID:                strings.TrimSpace(raw.ID),
		Name:              strings.TrimSpace(raw.Name),
		Type:              strings.TrimSpace(raw.Type),
		CollectionType:    strings.TrimSpace(raw.CollectionType),
		IndexNumber:       raw.IndexNumber,
		ParentIndexNumber: raw.ParentIndexNumber,
		SeriesName:        strings.TrimSpace(raw.SeriesName),
	}
	if raw.ProductionYear > 0 {
		it.Year = strconv.Itoa(raw.ProductionYear)
	}
	switch it.Type {
	case "Movie", "Episode", "Video", "MusicVideo":
		it.Playable = true
	case "CollectionFolder", "UserView", "Folder", "Series", "Season", "BoxSet", "AggregateFolder", "Playlist", "ManualPlaylistsFolder":
		it.Folder = true
	default:
		if raw.IsFolder && it.Type == "" {
			it.Folder = true
		}
	}
	if it.Playable {
		it.Folder = false
	}
	if it.ID != "" && strings.TrimSpace(raw.ImageTags["primary"]) != "" {
		it.ImageURL = c.base + "/Items/" + url.PathEscape(it.ID) + "/Images/Primary?maxHeight=480&quality=80"
	}
	return it
}

func decodeItemList(raw json.RawMessage) ([]rawItem, int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, 0, nil
	}
	if trimmed[0] == '[' {
		var list []rawItem
		if err := decodeFlex(trimmed, &list); err != nil {
			return nil, 0, err
		}
		return list, len(list), nil
	}
	var page struct {
		Items []rawItem `json:"items"`
		Total int       `json:"totalrecordcount"`
	}
	if err := decodeFlex(trimmed, &page); err != nil {
		return nil, 0, err
	}
	return page.Items, page.Total, nil
}

func (c *Client) do(ctx context.Context, s Session, method, path string, query url.Values, body any, out any) error {
	_, err := c.doFinal(ctx, s, method, path, query, body, out)
	return err
}

// doFinal is do plus the URL the request finally landed on after redirects.
func (c *Client) doFinal(ctx context.Context, s Session, method, path string, query url.Values, body any, out any) (string, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		rdr = bytes.NewReader(buf)
	}
	endpoint := c.base + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json; profile=\"PascalCase\"")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; profile=\"PascalCase\"")
	}
	for k, v := range AuthHeaders(s) {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("无法连接服务器")
	}
	defer resp.Body.Close()
	final := endpoint
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	data, exceeded, err := httpx.ReadLimit(resp.Body, resp.ContentLength, bodyLimit)
	if err != nil {
		return "", fmt.Errorf("无法连接服务器")
	}
	if exceeded {
		return "", errors.New("服务器响应过大")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("服务器返回 HTTP %d", resp.StatusCode)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return final, nil
	}
	if err := decodeFlex(data, out); err != nil {
		return "", errors.New("无法解析服务器响应")
	}
	return final, nil
}

// decodeFlex lowercases object keys so PascalCase and camelCase bind to the
// same struct tags.
func decodeFlex(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(data)))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	folded, err := json.Marshal(foldKeys(raw))
	if err != nil {
		return err
	}
	return json.Unmarshal(folded, out)
}

func foldKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[strings.ToLower(k)] = foldKeys(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = foldKeys(t[i])
		}
		return t
	default:
		return v
	}
}

func newDeviceID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func cleanHeaderValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}
