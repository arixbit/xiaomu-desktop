package xbpq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/resolve"
)

func TestRuleBrowseSearchDetailAndResolve(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rule.json":
			json.NewEncoder(w).Encode(map[string]any{
				"主页url": srv.URL, "分类": "电影$1#剧集$2#", "分类url": "/list/{cateId}/{catePg}?area={area}", "搜索url": "/search?wd={wd}&page={pg}",
				"数组": "<article>&&</article>", "标题": "title=\"&&\"", "链接": "href=\"&&\"", "图片": "data-original=\"&&\"",
				"线路数组": "<nav>&&</nav>", "线路标题": "<span>&&</span>", "播放数组": "<section>&&</section>", "播放列表": "<a&&</a>", "播放链接": "href=\"&&\"", "播放标题": "<span>&&</span>",
			})
		case "/list/1/2", "/search":
			if r.URL.Path == "/search" && r.URL.Query().Get("wd") != "中文 片名" {
				t.Error(r.URL)
			}
			fmt.Fprint(w, "<article><a href='/detail/1' title='测试影片'><img data-original='/poster.jpg'></a></article>")
		case "/detail/1":
			fmt.Fprint(w, "<h1>测试影片</h1><nav><span>第一线</span></nav><nav><span>第二线</span></nav><section><a href='/play/1'><span>第一集</span></a><a href='/play/2'><span>第二集</span></a></section><section><a href='/play/3'><span>高清</span></a></section>")
		case "/play/2":
			fmt.Fprint(w, "<script>const videoConfig={url:'/media.m3u8'};</script>")
		case "/media.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\nsegment.ts\n#EXT-X-ENDLIST\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	s, cs, e := Open(ctx, config.Site{Ext: srv.URL + "/rule.json"})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if len(cs) != 2 || cs[1].Name != "剧集" {
		t.Fatal(cs)
	}
	p, e := s.Category(ctx, "1", 2)
	if e != nil || len(p.Videos) != 1 || p.Videos[0].Name != "测试影片" || p.Videos[0].Pic != srv.URL+"/poster.jpg" {
		t.Fatalf("%+v %v", p, e)
	}
	p, e = s.Search(ctx, "中文 片名", 1)
	if e != nil || len(p.Videos) != 1 {
		t.Fatalf("%+v %v", p, e)
	}
	d, e := s.Detail(ctx, string(p.Videos[0].ID))
	if e != nil || len(d.Sources) != 2 || len(d.Sources[0].Episodes) != 2 || d.Sources[0].Name != "第一线" {
		t.Fatalf("%+v %v", d, e)
	}
	play, e := s.Play(ctx, d.Sources[0].Name, d.Sources[0].Episodes[1])
	if e != nil {
		t.Fatal(e)
	}
	play, e = (resolve.Resolver{}).Resolve(ctx, play)
	if e != nil || play.URL != srv.URL+"/media.m3u8" || play.Headers["Referer"] != srv.URL+"/play/2" {
		t.Fatalf("%+v %v", play, e)
	}
}

func TestEmptyRulesUseMacCMSTemplates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/list" {
			fmt.Fprint(w, "<div><a class='stui-vodlist__thumb' href='/detail' title='默认模板' data-original='/poster.jpg'></a></div>")
			return
		}
		fmt.Fprint(w, "<h1>默认模板</h1><ul class='stui-content__playlist'><li><a href='/play'>第一集</a></li></ul>")
	}))
	defer srv.Close()
	raw, _ := json.Marshal(map[string]string{"主页url": srv.URL, "分类": "电影$1", "分类url": "/list", "数组": "", "播放数组": ""})
	s, _, e := Open(context.Background(), config.Site{Ext: string(raw)})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p, e := s.Category(context.Background(), "1", 1)
	if e != nil || len(p.Videos) != 1 {
		t.Fatalf("%+v %v", p, e)
	}
	d, e := s.Detail(context.Background(), string(p.Videos[0].ID))
	if e != nil || len(d.Sources) != 1 || d.Sources[0].Episodes[0].Name != "第一集" {
		t.Fatalf("%+v %v", d, e)
	}
}

func TestParallelCategoryNamesAndIDs(t *testing.T) {
	rule, _ := json.Marshal(map[string]string{"主页url": "https://example.com/", "分类": "电影&剧集", "分类值": "movie&series"})
	s, cs, e := Open(context.Background(), config.Site{Ext: string(rule)})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if len(cs) != 2 || cs[1].Name != "剧集" || cs[1].ID != "series" {
		t.Fatal(cs)
	}
}
