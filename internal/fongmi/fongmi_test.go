package fongmi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func TestClassSpiderFlow(t *testing.T) {
	const script = `import { Spider, net } from 'catvod';
export default class Demo extends Spider {
  async init(ext) { this.base = ext.base; }
  async homeContent() { return {class: [{type_id: "1", type_name: "电影"}]}; }
  async categoryContent() { return await net.json(this.base + "/videos"); }
  async detailContent(ids) {
    return {list: [{vod_name: "示例", vod_play_from: "直链", vod_play_url: "第1集$" + this.base + "/clip.mp4"}]};
  }
  async playerContent(flag, id) { return {parse: 0, url: id}; }
}`
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/spider.js":
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, script)
		case "/videos":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "{\"list\":[{\"vod_id\":\"7\",\"vod_name\":\"示例\"}],\"pagecount\":1}")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, categories, err := Open(context.Background(), config.Site{Kind: config.KindFongMi, API: srv.URL + "/spider.js", Ext: fmt.Sprintf("{\"base\":%q}", srv.URL)}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(categories) != 1 || categories[0].ID != "1" {
		t.Fatalf("categories: %+v", categories)
	}
	page, err := s.Category(context.Background(), "1", 1)
	if err != nil || len(page.Videos) != 1 || page.Videos[0].ID != "7" {
		t.Fatalf("page: %+v, %v", page, err)
	}
	detail, err := s.Detail(context.Background(), "7")
	if err != nil || len(detail.Sources) != 1 || len(detail.Sources[0].Episodes) != 1 {
		t.Fatalf("detail: %+v, %v", detail, err)
	}
	play, err := s.Play(context.Background(), detail.Sources[0].Name, detail.Sources[0].Episodes[0])
	if err != nil || play.URL != srv.URL+"/clip.mp4" {
		t.Fatalf("play: %+v, %v", play, err)
	}
}
