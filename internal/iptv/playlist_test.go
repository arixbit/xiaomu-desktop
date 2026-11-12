package iptv

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestM3UGroupsAlternativesAndHeaders(t *testing.T) {
	body := "\ufeff#EXTM3U\r\n" + `
#EXTINF:-1 tvg-name="备用名称" tvg-logo="./logo.png" group-title="新闻,综合",演示一
#EXTVLCOPT:http-user-agent=PlayerFixture/1.0
#EXTVLCOPT:http-referrer=https://example.com/watch
#EXTHTTP:{"Cookie":"session=fixture"}
./one.m3u8|Origin=https%3A%2F%2Fexample.com
#EXTINF:-1 group-title="新闻,综合",演示一
./backup.m3u8
#EXTINF:-1 group-title="新闻,综合",演示一
./backup.m3u8
#EXTINF:-1 tvg-name="演示二",
#EXTGRP:体育
https://example.com/two.m3u8
`
	channels, err := Parse("https://example.com/list/channels.m3u", []byte(body))
	if err != nil || len(channels) != 2 {
		t.Fatalf("channels=%+v err=%v", channels, err)
	}
	one := channels[0]
	if one.Name != "演示一" || one.Group != "新闻,综合" || one.Logo != "https://example.com/list/logo.png" || len(one.Streams) != 2 {
		t.Fatalf("channel=%+v", one)
	}
	stream := one.Streams[0]
	if stream.URL != "https://example.com/list/one.m3u8" || stream.Headers["User-Agent"] != "PlayerFixture/1.0" || stream.Headers["Referer"] != "https://example.com/watch" || stream.Headers["Cookie"] != "session=fixture" || stream.Headers["Origin"] != "https://example.com" {
		t.Fatalf("stream=%+v", stream)
	}
	if len(one.Streams[1].Headers) != 0 || len(channels[1].Streams[0].Headers) != 0 {
		t.Fatal("channel headers leaked into other channels")
	}
	if channels[1].Name != "演示二" || channels[1].Group != "体育" {
		t.Fatalf("channel=%+v", channels[1])
	}
}

func TestTXTGroupsAndMultipleLines(t *testing.T) {
	channels, err := Parse("https://example.com/channels.txt", []byte(`
新闻,#genre#
频道甲,https://example.com/a.m3u8#https://example.com/b.m3u8
频道甲,https://example.com/b.m3u8
体育,#genre#
频道乙,http://example.com:8080/live/2?token=fixture,with-comma
组播频道,udp://239.0.0.1:1234
`))
	if err != nil || len(channels) != 3 || len(channels[0].Streams) != 2 {
		t.Fatalf("channels=%+v err=%v", channels, err)
	}
	if channels[1].Group != "体育" || !strings.HasSuffix(channels[1].Streams[0].URL, "fixture,with-comma") || channels[2].Streams[0].URL != "udp://239.0.0.1:1234" {
		t.Fatalf("channels=%+v", channels)
	}
}

func TestHLSIsOneChannelNotSegments(t *testing.T) {
	for _, body := range []string{
		"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nsegment.ts\n",
		"#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nvariant.m3u8\n",
	} {
		channels, err := Parse("https://example.com/live.m3u8", []byte(body))
		if err != nil || len(channels) != 1 || channels[0].Streams[0].URL != "https://example.com/live.m3u8" {
			t.Fatalf("channels=%+v err=%v", channels, err)
		}
	}
}

func TestPlaylistRejectsInvalidBodies(t *testing.T) {
	for _, body := range []string{"", "<html>unavailable</html>", "not a playlist", "#EXTM3U\n#EXTINF:-1,Missing URL", "#EXTM3U\n#EXTHTTP:{broken}\nhttps://example.com/a"} {
		if _, err := Parse("https://example.com/list", []byte(body)); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
}

func TestTVBoxLiveSources(t *testing.T) {
	raw := `[
  {"name":"外部列表","url":"./channels.txt","ua":"ListUA"},
  {"name":"新闻","channels":[{"name":"示例甲","urls":["./a.m3u8","./b.m3u8"],"header":{"referer":"https://example.com/"}}]},
  {"name":"嵌套","ua":"GroupUA","groups":[{"group":"体育","channels":[{"name":"示例乙","url":"./c.m3u8"}]}]}
]`
	sources, err := ParseSources("https://example.com/box/config.json", json.RawMessage(raw))
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	channels := sources[0].Channels
	if len(channels) != 2 || channels[0].Group != "新闻" || channels[1].Group != "体育" || len(channels[0].Streams) != 2 || channels[1].Streams[0].Headers["User-Agent"] != "GroupUA" {
		t.Fatalf("channels=%+v", channels)
	}
	if sources[1].URL != "https://example.com/box/channels.txt" || sources[1].Headers["User-Agent"] != "ListUA" {
		t.Fatalf("external=%+v", sources[1])
	}
}

func TestLegacyLiveWrapper(t *testing.T) {
	u := "https://example.com/channels.txt"
	wrapper := "proxy://do=live&type=txt&ext=" + base64.StdEncoding.EncodeToString([]byte(u))
	raw, _ := json.Marshal([]map[string]any{{"name": "直播", "channels": []map[string]any{{"name": "频道列表", "urls": []string{wrapper}}}}})
	sources, err := ParseSources("https://example.com/config.json", raw)
	if err != nil || len(sources) != 1 || sources[0].URL != u || len(sources[0].Channels) != 0 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
}

func TestM3UWithExporterCommaAndNonHLSExtension(t *testing.T) {
	body := "#EXTM3U\n#EXT-X-SESSION-DATA:DATA-ID=\"fixture\"\n#EXTINF:-1,tvg-id=\"demo\" group-title=\"其他\",演示频道\nhttps://example.com/live.m3u8\n#EXTINF:-1,第二台\nhttps://example.com/second.m3u8\n"
	cs, err := Parse("https://example.com/channels.m3u", []byte(body))
	if err != nil || len(cs) != 2 || cs[0].Name != "演示频道" || cs[0].Group != "其他" {
		t.Fatalf("%+v %v", cs, err)
	}
}
