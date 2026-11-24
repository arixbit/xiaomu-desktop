// Package iptv reads user-supplied IPTV playlists without executing source code.
package iptv

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Stream is one of a channel's alternative playback URLs.
type Stream struct {
	URL     string
	Headers map[string]string
}

// Channel is a named live stream in a group.
type Channel struct {
	Name          string
	Group         string
	Logo          string
	TVGID         string
	TVGName       string
	Catchup       string
	CatchupSource string
	CatchupDays   int
	Streams       []Stream
}

// Playlist is a parsed M3U or TXT playlist along with optional header-level EPG.
type Playlist struct {
	Channels []Channel
	EPG      string
}

// Source is an external playlist or channels embedded in a TVBox config.
type Source struct {
	Name     string
	URL      string
	EPG      string
	Logo     string
	Headers  map[string]string
	Channels []Channel
}

// Parse reads extended M3U or TVBox's name,URL text format. An HLS media or
// master playlist is a single channel; its segments/variants are not channels.
func Parse(base string, body []byte) ([]Channel, error) {
	pl, err := ParsePlaylist(base, body)
	if err != nil {
		return nil, err
	}
	return pl.Channels, nil
}

// ParsePlaylist reads extended M3U or TVBox's name,URL text format and returns
// both channels and any header-level EPG URL (x-tvg-url / url-tvg).
func ParsePlaylist(base string, body []byte) (Playlist, error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff"))
	if strings.HasPrefix(text, "<") {
		return Playlist{}, fmt.Errorf("返回的是网页，不是 IPTV 频道列表")
	}
	lines := strings.Split(text, "\n")
	m3u := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") || strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			return Playlist{Channels: []Channel{{Name: "直播频道", Group: "未分组", Streams: []Stream{{URL: base}}}}}, nil
		}
		m3u = m3u || strings.HasPrefix(line, "#EXTM3U") || strings.HasPrefix(line, "#EXTINF:")
	}
	b := channelList{index: map[string]int{}}
	var epgURL string
	if m3u {
		var err error
		epgURL, err = parseM3U(base, text, &b)
		if err != nil {
			return Playlist{}, err
		}
	} else {
		group := "未分组"
		for _, line := range lines {
			name, raw, ok := strings.Cut(strings.TrimSpace(line), ",")
			if !ok || strings.HasPrefix(name, "#") {
				continue
			}
			name, raw = strings.TrimSpace(name), strings.TrimSpace(raw)
			if strings.EqualFold(raw, "#genre#") {
				group = name
				continue
			}
			channel := Channel{Name: name, Group: group}
			for _, candidate := range strings.Split(raw, "#") {
				if strings.Contains(candidate, "://") || strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, "./") {
					channel.Streams = append(channel.Streams, parseStream(base, candidate, nil))
				}
			}
			b.add(channel)
		}
	}
	if len(b.channels) == 0 {
		return Playlist{}, fmt.Errorf("没有找到 IPTV 频道，请使用 M3U 或“频道名,播放地址”的 TXT 列表")
	}
	return Playlist{Channels: b.channels, EPG: epgURL}, nil
}

var attributes = regexp.MustCompile("([a-zA-Z0-9_-]+)\\s*=\\s*(\"[^\"]*\"|'[^']*'|[^\\s]+)")

func parseM3U(base, text string, b *channelList) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	group := "未分组"
	var epgURL string
	var defaultCatchup, defaultCatchupSource string
	var defaultCatchupDays int
	var channel Channel
	var headers, defaults map[string]string
	pending := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "#EXTM3U"):
			meta := strings.TrimPrefix(line, "#EXTM3U")
			attrs := parseAttributes(meta)
			if epgURL == "" {
				rawEPG := firstNonEmpty(attrs["x-tvg-url"], attrs["url-tvg"], attrs["tvg-url"])
				if rawEPG != "" {
					// x-tvg-url can be comma-separated; keep the first valid entry resolved against base.
					first, _, _ := strings.Cut(rawEPG, ",")
					epgURL = resolve(base, first)
				}
			}
			if v := attrs["catchup"]; v != "" {
				defaultCatchup = strings.ToLower(strings.TrimSpace(v))
			}
			if v := attrs["catchup-source"]; v != "" {
				defaultCatchupSource = strings.TrimSpace(v)
			}
			if v := attrs["catchup-days"]; v != "" {
				if d, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && d > 0 {
					defaultCatchupDays = d
				}
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			meta, name := splitInfo(strings.TrimPrefix(line, "#EXTINF:"))
			// Some exported lists put a comma before the tvg attributes.
			// Treat that comma as whitespace, keeping the last display name.
			if strings.HasPrefix(strings.TrimSpace(name), "tvg-") || strings.HasPrefix(strings.TrimSpace(name), "group-title=") {
				if extra, label := splitInfo(name); label != "" {
					meta += " " + extra
					name = label
				}
			}

			attrs := parseAttributes(meta)
			if name == "" {
				name = attrs["tvg-name"]
			}
			catchup := strings.ToLower(strings.TrimSpace(attrs["catchup"]))
			if catchup == "" {
				catchup = defaultCatchup
			}
			catchupSrc := strings.TrimSpace(attrs["catchup-source"])
			if catchupSrc == "" {
				catchupSrc = defaultCatchupSource
			}
			catchupDays := defaultCatchupDays
			if v := attrs["catchup-days"]; v != "" {
				if d, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && d > 0 {
					catchupDays = d
				}
			}
			channel = Channel{
				Name:          name,
				Group:         attrs["group-title"],
				Logo:          resolve(base, attrs["tvg-logo"]),
				TVGID:         strings.TrimSpace(attrs["tvg-id"]),
				TVGName:       strings.TrimSpace(attrs["tvg-name"]),
				Catchup:       catchup,
				CatchupSource: catchupSrc,
				CatchupDays:   catchupDays,
			}
			headers, pending = MergeHeaders(nil, defaults), true
		case strings.HasPrefix(line, "#EXTGRP:"):
			group = strings.TrimSpace(strings.TrimPrefix(line, "#EXTGRP:"))
			if pending {
				channel.Group = group
			}
		case strings.HasPrefix(line, "#EXTVLCOPT:"):
			key, value, _ := strings.Cut(strings.TrimPrefix(line, "#EXTVLCOPT:"), "=")
			var name string
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "http-user-agent":
				name = "User-Agent"
			case "http-referrer", "http-referer":
				name = "Referer"
			case "http-origin":
				name = "Origin"
			}
			if name != "" {
				headers = MergeHeaders(headers, map[string]string{name: strings.TrimSpace(value)})
				if !pending {
					defaults = MergeHeaders(defaults, headers)
				}
			}
		case strings.HasPrefix(line, "#EXTHTTP:"):
			var extra map[string]string
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "#EXTHTTP:")), &extra); err != nil {
				return "", fmt.Errorf("直播请求头格式错误：%w", err)
			}
			headers = MergeHeaders(headers, extra)
			if !pending {
				defaults = MergeHeaders(defaults, extra)
			}
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		default:
			if channel.Name == "" {
				channel.Name = fmt.Sprintf("频道 %d", len(b.channels)+1)
			}
			if channel.Group == "" {
				channel.Group = group
			}
			channel.Streams = []Stream{parseStream(base, line, MergeHeaders(defaults, headers))}
			b.add(channel)
			channel, headers, pending = Channel{}, nil, false
		}
	}
	return epgURL, scanner.Err()
}

func parseAttributes(meta string) map[string]string {
	attrs := map[string]string{}
	for _, match := range attributes.FindAllStringSubmatch(meta, -1) {
		attrs[strings.ToLower(match[1])] = strings.Trim(match[2], "\"'")
	}
	return attrs
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// The display name follows the first comma outside quoted attribute values.
func splitInfo(info string) (string, string) {
	var quote rune
	for i, ch := range info {
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
		} else if ch == '\'' || ch == '"' {
			quote = ch
		} else if ch == ',' {
			return info[:i], strings.TrimSpace(info[i+1:])
		}
	}
	return info, ""
}

func parseStream(base, raw string, headers map[string]string) Stream {
	raw, options, _ := strings.Cut(strings.TrimSpace(raw), "|")
	extra := map[string]string{}
	values, _ := url.ParseQuery(options)
	for name := range values {
		extra[name] = values.Get(name)
	}
	return Stream{URL: resolve(base, raw), Headers: MergeHeaders(headers, extra)}
}

// MergeHeaders copies and canonicalizes headers; explicit values take priority.
func MergeHeaders(base, extra map[string]string) map[string]string {
	out := map[string]string{}
	for _, headers := range []map[string]string{base, extra} {
		for key, value := range headers {
			key = http.CanonicalHeaderKey(strings.TrimSpace(key))
			if key == "Ua" {
				key = "User-Agent"
			}
			if key == "Referrer" {
				key = "Referer"
			}
			out[key] = value
		}
	}
	return out
}

func resolve(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	return b.ResolveReference(u).String()
}

type channelList struct {
	channels []Channel
	index    map[string]int
}

func (b *channelList) add(channel Channel) {
	if len(channel.Streams) == 0 {
		return
	}
	if channel.Name == "" {
		channel.Name = fmt.Sprintf("频道 %d", len(b.channels)+1)
	}
	if channel.Group == "" {
		channel.Group = "未分组"
	}
	key := channel.Group + "\x00" + channel.Name
	i, exists := b.index[key]
	if !exists {
		b.index[key] = len(b.channels)
		b.channels = append(b.channels, channel)
		return
	}
	if b.channels[i].Logo == "" && channel.Logo != "" {
		b.channels[i].Logo = channel.Logo
	}
	if b.channels[i].TVGID == "" && channel.TVGID != "" {
		b.channels[i].TVGID = channel.TVGID
	}
	if b.channels[i].TVGName == "" && channel.TVGName != "" {
		b.channels[i].TVGName = channel.TVGName
	}
	if b.channels[i].Catchup == "" && channel.Catchup != "" {
		b.channels[i].Catchup = channel.Catchup
	}
	if b.channels[i].CatchupSource == "" && channel.CatchupSource != "" {
		b.channels[i].CatchupSource = channel.CatchupSource
	}
	if b.channels[i].CatchupDays == 0 && channel.CatchupDays > 0 {
		b.channels[i].CatchupDays = channel.CatchupDays
	}
	for _, stream := range channel.Streams {
		duplicate := false
		for _, old := range b.channels[i].Streams {
			duplicate = duplicate || old.URL == stream.URL && maps.Equal(old.Headers, stream.Headers)
		}
		if !duplicate {
			b.channels[i].Streams = append(b.channels[i].Streams, stream)
		}
	}
}

type channelSpec struct {
	Name   string            `json:"name"`
	URL    string            `json:"url"`
	URLs   []string          `json:"urls"`
	Logo   string            `json:"logo"`
	UA     string            `json:"ua"`
	Header map[string]string `json:"header"`
}

type liveSpec struct {
	Name     string            `json:"name"`
	Group    string            `json:"group"`
	URL      string            `json:"url"`
	EPG      string            `json:"epg"`
	Logo     string            `json:"logo"`
	UA       string            `json:"ua"`
	Header   map[string]string `json:"header"`
	Channels []channelSpec     `json:"channels"`
	Groups   []liveSpec        `json:"groups"`
}

// ParseSources reads TVBox lives: a playlist URL, external providers or inline
// channel groups. Legacy proxy://do=live wrappers only unwrap a playlist URL.
func ParseSources(base string, raw json.RawMessage) ([]Source, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var external string
	if json.Unmarshal(raw, &external) == nil {
		if external == "" {
			return nil, nil
		}
		return []Source{{Name: "IPTV 直播", URL: playlistURL(base, external)}}, nil
	}
	var specs []liveSpec
	if raw[0] == '{' {
		var spec liveSpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			return nil, err
		}
		specs = []liveSpec{spec}
	} else if err := json.Unmarshal(raw, &specs); err != nil {
		return nil, fmt.Errorf("直播配置格式错误：%w", err)
	}
	var sources []Source
	inline := channelList{index: map[string]int{}}
	var inlineEPG, inlineLogo string
	var add func(liveSpec, map[string]string, string, string)
	add = func(spec liveSpec, inherited map[string]string, inheritedEPG, inheritedLogo string) {
		headers := MergeHeaders(inherited, spec.Header)
		if spec.UA != "" {
			headers["User-Agent"] = spec.UA
		}
		epg := inheritedEPG
		if strings.TrimSpace(spec.EPG) != "" {
			epg = resolveEPG(base, spec.EPG)
		}
		logoTpl := inheritedLogo
		if strings.TrimSpace(spec.Logo) != "" {
			logoTpl = resolveLogoTemplate(base, spec.Logo)
		}
		name := spec.Name
		if name == "" {
			name = "IPTV 直播"
		}
		if spec.URL != "" {
			sources = append(sources, Source{Name: name, URL: playlistURL(base, spec.URL), EPG: epg, Logo: logoTpl, Headers: headers})
		}
		group := spec.Group
		if group == "" {
			group = name
		}
		for _, c := range spec.Channels {
			logo := resolve(base, c.Logo)
			if logo == "" && logoTpl != "" {
				logo = ExpandLogoTemplate(logoTpl, c.Name)
			}
			channel := Channel{Name: c.Name, Group: group, Logo: logo}
			chHeaders := MergeHeaders(headers, c.Header)
			if c.UA != "" {
				chHeaders["User-Agent"] = c.UA
			}
			urls := c.URLs
			if c.URL != "" {
				urls = append([]string{c.URL}, urls...)
			}
			for _, raw := range urls {
				if strings.HasPrefix(raw, "proxy://do=live") {
					sources = append(sources, Source{Name: c.Name, URL: playlistURL(base, raw), EPG: epg, Logo: logoTpl, Headers: chHeaders})
				} else if strings.TrimSpace(raw) != "" {
					channel.Streams = append(channel.Streams, parseStream(base, raw, chHeaders))
				}
			}
			if len(channel.Streams) > 0 {
				if inlineEPG == "" {
					inlineEPG = epg
				}
				if inlineLogo == "" {
					inlineLogo = logoTpl
				}
			}
			inline.add(channel)
		}
		for _, child := range spec.Groups {
			add(child, headers, epg, logoTpl)
		}
	}
	for _, spec := range specs {
		add(spec, nil, "", "")
	}
	if len(inline.channels) > 0 {
		sources = append([]Source{{Name: "配置内直播", EPG: inlineEPG, Logo: inlineLogo, Channels: inline.channels}}, sources...)
	}
	return sources, nil
}

// ExpandLogoTemplate substitutes TVBox {name} placeholders inside a source logo URL.
func ExpandLogoTemplate(tpl, channelName string) string {
	tpl = strings.TrimSpace(tpl)
	channelName = strings.TrimSpace(channelName)
	if tpl == "" || channelName == "" || !strings.Contains(tpl, "{name}") {
		return ""
	}
	return strings.ReplaceAll(tpl, "{name}", url.PathEscape(channelName))
}

func resolveEPG(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "{") {
		// Preserve template placeholders like {name} / {date} without percent-encoding.
		if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
			return raw
		}
	}
	return resolve(base, raw)
}

func resolveLogoTemplate(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "{") && (strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")) {
		return raw
	}
	return resolve(base, raw)
}

func playlistURL(base, raw string) string {
	if strings.HasPrefix(raw, "proxy://do=live") {
		values, _ := url.ParseQuery(strings.TrimPrefix(raw, "proxy://"))
		ext := values.Get("ext")
		if strings.HasPrefix(ext, "http://") || strings.HasPrefix(ext, "https://") {
			raw = ext
		} else {
			for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
				if decoded, err := encoding.DecodeString(strings.ReplaceAll(ext, " ", "+")); err == nil && len(decoded) > 0 {
					raw = string(decoded)
					break
				}
			}
		}
	}
	return resolve(base, raw)
}
