package catalog

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func BenchmarkCMSJSONPageDecode(b *testing.B) {
	var buf bytes.Buffer
	buf.WriteString(`{"pagecount":10,"list":[`)
	for i := range 200 {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"vod_id":"%d","vod_name":"合成剧集 %d","vod_pic":"https://example.com/p/%d.jpg","vod_remarks":"更新至第%d集","vod_year":"2026","vod_class":"剧情","vod_content":"%s"}`,
			i, i, i, i%40+1, "这是一段较长的影片简介内容用于模拟真实的CMS接口返回的响应体大小。")
	}
	buf.WriteString(`]}`)
	payload := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	s, err := OpenCMS(config.Site{Key: "cms", API: srv.URL, Kind: config.KindJSON})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	s.cms.client = srv.Client()

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		page, err := s.Search(ctx, "合成", 1)
		if err != nil || len(page.Videos) != 200 {
			b.Fatalf("len=%d err=%v", len(page.Videos), err)
		}
	}
}

func TestHasHTMLPrefix(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"<!DOCTYPE html><html>", true},
		{"<!doctype html>", true},
		{"<HTML>", true},
		{"<html lang=\"zh\">", true},
		{`{"list":[]}`, false},
		{"<?xml version=\"1.0\"?>", false},
		{"<htm", false},
		{"", false},
	} {
		if got := hasHTMLPrefix([]byte(tc.in)); got != tc.want {
			t.Errorf("hasHTMLPrefix(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
