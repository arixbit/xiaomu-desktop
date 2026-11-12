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
	"strings"
)

// Stream is one of a channel's alternative playback URLs.
type Stream struct {
	URL     string
	Headers map[string]string
}

// Channel is a named live stream in a group.
type Channel struct {
	Name    string
	Group   string
	Logo    string
	Streams []Stream
}

// Source is an external playlist or channels embedded in a TVBox config.
type Source struct {
	Name     string
	URL      string
	Headers  map[string]string
	Channels []Channel
}

// Parse reads extended M3U or TVBox's name,URL text format. An HLS media or
// master playlist is a single channel; its segments/variants are not channels.
func Parse(base string, body []byte) ([]Channel, error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff"))
	if strings.HasPrefix(text, "<") {
		return nil, fmt.Errorf("返回的是网页，不是 IPTV 频道列表")
	}
	lines := strings.Split(text, "\n")
	m3u := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") || strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			return []Channel{{Name: "直播频道", Group: "未分组", Streams: []Stream{{URL: base}}}}, nil
		}
		m3u = m3u || strings.HasPrefix(line, "#EXTM3U") || strings.HasPrefix(line, "#EXTINF:")
	}
	b := channelList{index: map[string]int{}}
	if m3u {
		if err := parseM3U(base, text, &b); err != nil {
			return nil, err
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
		return nil, fmt.Errorf("没有找到 IPTV 频道，请使用 M3U 或“频道名,播放地址”的 TXT 列表")
	}
	return b.channels, nil
}

var attributes = regexp.MustCompile("([a-zA-Z0-9_-]+)\\s*=\\s*(\"[^\"]*\"|'[^']*'|[^\\s]+)")

func parseM3U(base, text string, b *channelList) error {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	group := "未分组"
	var channel Channel
	var headers, defaults map[string]string
	pending := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
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

			attrs := map[string]string{}
			for _, match := range attributes.FindAllStringSubmatch(meta, -1) {
				attrs[strings.ToLower(match[1])] = strings.Trim(match[2], "\"'")
			}
			if name == "" {
				name = attrs["tvg-name"]
			}
			channel = Channel{Name: name, Group: attrs["group-title"], Logo: resolve(base, attrs["tvg-logo"])}
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
				return fmt.Errorf("直播请求头格式错误：%w", err)
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
	return scanner.Err()
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
	var add func(liveSpec, map[string]string)
	add = func(spec liveSpec, inherited map[string]string) {
		headers := MergeHeaders(inherited, spec.Header)
		if spec.UA != "" {
			headers["User-Agent"] = spec.UA
		}
		name := spec.Name
		if name == "" {
			name = "IPTV 直播"
		}
		if spec.URL != "" {
			sources = append(sources, Source{Name: name, URL: playlistURL(base, spec.URL), Headers: headers})
		}
		group := spec.Group
		if group == "" {
			group = name
		}
		for _, c := range spec.Channels {
			channel := Channel{Name: c.Name, Group: group, Logo: resolve(base, c.Logo)}
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
					sources = append(sources, Source{Name: c.Name, URL: playlistURL(base, raw), Headers: chHeaders})
				} else if strings.TrimSpace(raw) != "" {
					channel.Streams = append(channel.Streams, parseStream(base, raw, chHeaders))
				}
			}
			inline.add(channel)
		}
		for _, child := range spec.Groups {
			add(child, headers)
		}
	}
	for _, spec := range specs {
		add(spec, nil)
	}
	if len(inline.channels) > 0 {
		sources = append([]Source{{Name: "配置内直播", Channels: inline.channels}}, sources...)
	}
	return sources, nil
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
