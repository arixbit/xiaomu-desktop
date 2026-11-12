package config

import (
	"context"
	"fmt"
	"net/url"

	"github.com/arixbit/xiaomu-desktop/internal/iptv"
)

// LiveChannels loads one live provider, including nested TVBox lists. Headers
// apply both to loading its playlist and to media unless a channel overrides it.
func LiveChannels(ctx context.Context, source iptv.Source, fetch Fetcher) ([]iptv.Channel, error) {
	remaining := 32
	return liveChannels(ctx, source, fetch, 0, &remaining)
}

func liveChannels(ctx context.Context, source iptv.Source, fetch Fetcher, depth int, remaining *int) ([]iptv.Channel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if depth > 4 || *remaining <= 0 {
		return nil, fmt.Errorf("直播列表嵌套过多或重复引用，请检查地址")
	}
	var out []iptv.Channel
	for _, channel := range source.Channels {
		copy := channel
		copy.Streams = make([]iptv.Stream, len(channel.Streams))
		for i, stream := range channel.Streams {
			copy.Streams[i] = iptv.Stream{URL: stream.URL, Headers: iptv.MergeHeaders(source.Headers, stream.Headers)}
		}
		out = append(out, copy)
	}
	if source.URL != "" {
		u, err := url.Parse(source.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "file") {
			return nil, fmt.Errorf("直播列表地址需使用 http、https 或 file 协议")
		}
		*remaining--
		loader := fetch
		if loader == nil {
			loader = httpFetcher(nil, source.Headers)
		}
		doc, err := Read(ctx, source.URL, loader)
		if err != nil {
			return nil, err
		}
		for _, child := range doc.Lives {
			child.Headers = iptv.MergeHeaders(source.Headers, child.Headers)
			channels, err := liveChannels(ctx, child, fetch, depth+1, remaining)
			if err != nil {
				return nil, err
			}
			out = append(out, channels...)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("此配置没有 IPTV 频道列表")
	}
	return out, nil
}
