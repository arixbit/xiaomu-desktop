package catalog

import (
	"strings"
	"testing"
)

func TestReadDetailSources(t *testing.T) {
	detail, err := readDetail(`{"list":[{"vod_name":"示例","vod_play_from":"主线$$$备用","vod_play_url":"第1集$https://example.com/a.mp4?x=$1#第2集$two$$$https://example.com/b.m3u8"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Sources) != 2 || len(detail.Sources[0].Episodes) != 2 || detail.Sources[1].Name != "备用" {
		t.Fatalf("%+v", detail)
	}
	if got := detail.Sources[0].Episodes[0].ID; got != "https://example.com/a.mp4?x=$1" {
		t.Fatalf("episode id %q", got)
	}
	if got := detail.Sources[1].Episodes[0].Name; got != "第 1 集" {
		t.Fatalf("episode name %q", got)
	}
	for _, raw := range []string{`{"list":[]}`, `{"list":[{"vod_play_url":"空$#$$$"}]}`} {
		if _, err := readDetail(raw); err == nil {
			t.Fatalf("accepted empty detail %s", raw)
		}
	}
}

func TestReadPageIDsAndEmptyPlaceholder(t *testing.T) {
	page, err := readPage(`{"pagecount":"2","list":[{"vod_id":9007199254740993,"vod_name":"示例"},{"vod_id":"no_data"},{"vod_id":""}]}`, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Videos) != 1 || page.Videos[0].ID != "9007199254740993" || page.HasNext {
		t.Fatalf("%+v", page)
	}
}

func TestReadArtworkAndDetailMetadata(t *testing.T) {
	page, err := readPage(
		`{"list":[{"vod_id":"7","vod_name":"示例","vod_pic":"https://example.com/poster.jpg","vod_year":"2026","vod_class":"悬疑","vod_content":"海边的故事"}]}`,
		1, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Videos) != 1 || page.Videos[0].Pic != "https://example.com/poster.jpg" || page.Videos[0].Year != "2026" || page.Videos[0].Type != "悬疑" {
		t.Fatalf("page metadata: %+v", page)
	}
	detail, err := readDetail(`{"list":[{"vod_name":"示例","vod_pic":"https://example.com/poster-large.jpg","vod_year":"2026","vod_class":"悬疑","vod_content":"海边的故事","vod_play_from":"主线","vod_play_url":"第1集$https://example.com/clip.mp4"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Pic != "https://example.com/poster-large.jpg" || detail.Content != "海边的故事" || detail.Year != "2026" || detail.Type != "悬疑" || len(detail.Sources) != 1 {
		t.Fatalf("detail metadata: %+v", detail)
	}
}

func TestReadPlayback(t *testing.T) {
	for _, tt := range []struct{ name, raw, wantErr string }{
		{"direct", `{"parse":0,"url":"https://example.com/a.m3u8","header":{"Referer":"https://example.com/","X-Test":"1"}}`, ""},
		{"string flag", `{"parse":"0","url":"https://example.com/a.mp4"}`, ""},
		{"drpy direct mp4", `{"parse":1,"jx":0,"url":"https://example.com/a.mp4?token=example"}`, ""},
		{"drpy direct hls", `{"parse":"1","jx":"0","url":"https://example.com/a.M3U8"}`, ""},
		{"sniffing", `{"parse":1,"url":"https://example.com/watch"}`, ""},
		{"media in query", `{"parse":1,"url":"https://example.com/watch?url=https://example.com/a.mp4"}`, ""},
		{"media in fragment", `{"parse":1,"url":"https://example.com/watch#a.m3u8"}`, ""},
		{"direct needs external parser", `{"parse":1,"jx":1,"url":"https://example.com/a.mp4"}`, ""},
		{"other parse mode", `{"parse":2,"url":"https://example.com/a.mp4"}`, ""},
		{"external parser", `{"jx":"1","url":"https://example.com/watch"}`, ""},
		{"magnet", `{"url":"magnet:?xt=urn:btih:example"}`, "http/https"},
		{"empty", `{"url":""}`, "http/https"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			play, err := ReadPlayback(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (tt.name == "sniffing" || tt.name == "media in query" || tt.name == "other parse mode") && !play.Parse {
				t.Fatal("lost parse flag")
			}
			if tt.name == "external parser" && !play.JX {
				t.Fatal("lost jx flag")
			}
			if tt.name == "direct" && play.Headers["X-Test"] != "1" {
				t.Fatal("lost playback headers")
			}
		})
	}
}
