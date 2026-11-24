package iptv

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParsePlaylistEPGCatchupAndTVBoxFields(t *testing.T) {
	body := `#EXTM3U x-tvg-url="./epg.xml.gz" catchup="append" catchup-days="7"
#EXTINF:-1 tvg-id="cctv1" tvg-name="CCTV1" tvg-logo="./cctv1.png" catchup-source="?playseek=${(b)yyyyMMddHHmmss}-${(e)yyyyMMddHHmmss}" group-title="央视",CCTV-1 综合
https://example.com/live/cctv1.m3u8
`
	pl, err := ParsePlaylist("https://example.com/iptv/list.m3u", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if pl.EPG != "https://example.com/iptv/epg.xml.gz" {
		t.Fatalf("EPG = %q", pl.EPG)
	}
	if len(pl.Channels) != 1 {
		t.Fatalf("channels = %+v", pl.Channels)
	}
	ch := pl.Channels[0]
	if ch.TVGID != "cctv1" || ch.TVGName != "CCTV1" || ch.Catchup != "append" || ch.CatchupDays != 7 ||
		ch.CatchupSource != "?playseek=${(b)yyyyMMddHHmmss}-${(e)yyyyMMddHHmmss}" {
		t.Fatalf("channel metadata mismatch: %+v", ch)
	}

	// TVBox JSON lives preserve epg and logo templates.
	raw := `[{"name":"综合直播","url":"./live.txt","epg":"https://epg.example.com/?ch={name}&date={date}","logo":"https://logo.example.com/{name}.png"}]`
	sources, err := ParseSources("https://example.com/box/config.json", json.RawMessage(raw))
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources = %+v err=%v", sources, err)
	}
	if sources[0].EPG != "https://epg.example.com/?ch={name}&date={date}" || sources[0].Logo != "https://logo.example.com/{name}.png" {
		t.Fatalf("TVBox source metadata = %+v", sources[0])
	}
	if got := ExpandLogoTemplate(sources[0].Logo, "CCTV-1"); got != "https://logo.example.com/CCTV-1.png" {
		t.Fatalf("ExpandLogoTemplate = %q", got)
	}
}

func TestXMLTVParseGzipLookupAndCatchupURL(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="cctv1">
    <display-name>CCTV-1</display-name>
    <display-name>央视一套</display-name>
  </channel>
  <programme start="20261124180000 +0800" stop="20261124190000 +0800" channel="cctv1">
    <title>晚间新闻</title>
  </programme>
  <programme start="20261124190000 +0800" stop="20261124193000 +0800" channel="cctv1">
    <title>新闻联播</title>
    <desc>每日要闻</desc>
  </programme>
  <programme start="20261124193000 +0800" stop="20261124200000 +0800" channel="cctv1">
    <title>焦点访谈</title>
  </programme>
</tv>`

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(xmlData))
	_ = zw.Close()

	epg, err := ParseXMLTV(gz.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 24, 19, 15, 0, 0, loc)

	// Normalized lookup matches "CCTV-1 高清" even without TVGID.
	ch := Channel{
		Name:          "CCTV-1 高清",
		Catchup:       "append",
		CatchupSource: "?playseek=${(b)yyyyMMddHHmmss}-${(e)yyyyMMddHHmmss}",
		CatchupDays:   7,
		Streams:       []Stream{{URL: "https://example.com/live/cctv1.m3u8?token=abc"}},
	}
	cur, next := epg.CurrentAndNext(ch, now)
	if cur == nil || cur.Title != "新闻联播" || next == nil || next.Title != "焦点访谈" {
		t.Fatalf("CurrentAndNext = (%+v, %+v)", cur, next)
	}
	progs := epg.ProgramsForDay(ch, now)
	if len(progs) != 3 {
		t.Fatalf("ProgramsForDay = %d, want 3", len(progs))
	}

	// Build catchup URL for the first (past) program.
	replayURL, ok := CatchupURL(ch, 0, progs[0], now)
	if !ok {
		t.Fatal("expected catchup URL")
	}
	if !strings.HasPrefix(replayURL, "https://example.com/live/cctv1.m3u8?token=abc&playseek=") {
		t.Fatalf("unexpected catchup URL: %q", replayURL)
	}
}

func TestFetchDIYPChannelEPG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ch") != "CCTV-1" || r.URL.Query().Get("date") != "2026-11-24" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"channel_name":"CCTV-1","date":"2026-11-24","epg_data":[{"start":"19:00","end":"19:30","title":"新闻联播"},{"start":"23:30","end":"00:30","title":"午夜剧场"}]}`)
	}))
	defer srv.Close()

	tpl := srv.URL + "/?ch={name}&date={date}"
	if !IsTemplateEPG(tpl) {
		t.Fatal("expected template EPG")
	}
	day := time.Date(2026, 11, 24, 19, 10, 0, 0, time.Local)
	progs, err := FetchDIYPChannel(context.Background(), srv.Client(), tpl, Channel{Name: "CCTV-1"}, day, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(progs) != 2 || progs[0].Title != "新闻联播" || !progs[1].End.After(progs[1].Start) {
		t.Fatalf("DIYP programs = %+v", progs)
	}
}
