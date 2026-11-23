package danmaku

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDirectEpisodeIDParsing(t *testing.T) {
	for _, tc := range []struct {
		site, raw, want string
		ok              bool
	}{
		{"qq", "https://example.com/x/cover/cid000001/v00123456ab.html?traceid=1", "qq:v00123456ab", true},
		{"qq", "https://example.com/x/page/a00112233bc.html", "qq:a00112233bc", true},
		{"qq", "https://example.com/play?vid=k00445566zz", "qq:k00445566zz", true},
		{"qq", "https://example.com/x/cover/cid000001.html", "", false},
		{"qiyi", "https://example.com/v_19rrja7wj1.html?vfm=1", "qiyi:100000001", true},
		{"imgo", "https://example.com/b/123456/654321.html?cxid=1", "imgo:123456:654321", true},
		{"bilibili1", "https://example.com/bangumi/play/ep123456?bsource=1", "bili:ep:123456", true},
		{"bilibili1", "https://example.com/video/BV1xx411c7mD", "bili:bv:BV1xx411c7mD", true},
		{"youku", "https://example.com/v_show/id_X123.html", "", false},
	} {
		got, ok := directEpisodeID(tc.site, tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Errorf("directEpisodeID(%q, %q) = (%q, %v), want (%q, %v)", tc.site, tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDirectProviderSearchAndCommentsOffline(t *testing.T) {
	var iqiyiCompressed bytes.Buffer
	zw := zlib.NewWriter(&iqiyiCompressed)
	io.WriteString(zw, `<?xml version="1.0" encoding="UTF-8"?><danmu><data><entry><list><bulletInfo><content>爱奇艺弹幕</content><showTime>12.5</showTime><color>00FF00</color><position>1</position></bulletInfo></list></entry></data></danmu>`)
	zw.Close()

	var biliCompressed bytes.Buffer
	fw, err := flate.NewWriter(&biliCompressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(fw, `<?xml version="1.0" encoding="UTF-8"?><i><d p="3.5,1,25,16711680,0,0,0,0">B站弹幕</d></i>`)
	fw.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index":
			switch r.URL.Query().Get("kw") {
			case "空结果":
				io.WriteString(w, `{"code":0,"data":{"longData":[]}}`)
			default:
				io.WriteString(w, `{
					"code":0,
					"data":{"longData":{"rows":[
						{
							"en_id":"drama1",
							"titleTxt":"示例剧",
							"year":"2024",
							"cat_name":"电视剧",
							"seriesSite":"qiyi",
							"seriesPlaylinks":[{"url":"https://example.com/v_19rrja7wj1.html"}],
							"playlinks":{"qiyi":"https://example.com/v_19rrja7wj1.html","qq":"https://example.com/x/cover/cid000001/v00123456ab.html"}
						},
						{
							"en_id":"movie1",
							"titleTxt":"示例电影",
							"year":2023,
							"cat_name":"电影",
							"playlinks":{"imgo":"https://example.com/b/123456/654321.html","bilibili1":"https://example.com/bangumi/play/ep123456"}
						},
						{
							"en_id":"anime1",
							"titleTxt":"示例番",
							"year":"2025",
							"cat_name":"动漫",
							"playlinks":{"bilibili1":"https://example.com/bangumi/play/ep123456"}
						},
						{
							"en_id":"show1",
							"titleTxt":"示例综艺",
							"year":"2025",
							"cat_name":"综艺",
							"playlinks":{"imgo":"https://example.com/b/123456/654321.html"}
						}
					]}}
				}`)
			}
		case "/episodesv2":
			io.WriteString(w, `{"code":0,"data":[{"cat_id":2,"ent_id":"drama1","site":"qq","seriesHTML":{"seriesPlaylinks":[{"url":"https://example.com/x/cover/cid000001/v00123456ab.html"}]}}]}`)
		case "/v1/detail":
			io.WriteString(w, `{"data":{"allepidetail":{"bilibili1":[{"playlink_num":1,"url":"https://example.com/bangumi/play/ep123456"}]}}}`)
		case "/episodeszongyi":
			io.WriteString(w, `{"data":{"list":[{"name":"第1期","period":"20250101","url":"https://example.com/b/123456/654321.html"}]}}`)
		case "/barrage/base/v00123456ab":
			io.WriteString(w, `{"segment_index":{"30000":{"segment_start":"30000","segment_name":"seg2"},"0":{"segment_start":"0","segment_name":"seg1"}}}`)
		case "/barrage/segment/v00123456ab/seg1":
			io.WriteString(w, `{"barrage_list":[{"time_offset":"5000","content":"腾讯首段","content_style":"{\"gradient_colors\":[\"#FF0000\"],\"position\":2}"}]}`)
		case "/barrage/segment/v00123456ab/seg2":
			io.WriteString(w, `{"barrage_list":[{"time_offset":35000,"content":"腾讯二段","content_style":"{\"color\":\"00ff00\",\"position\":3}"}]}`)
		case "/video/video/baseinfo/100000001":
			io.WriteString(w, `{"data":{"durationSec":290,"displayBarrage":true}}`)
		case "/bullet/00/01/100000001_300_1.z":
			w.Write(iqiyiCompressed.Bytes())
		case "/video/info":
			io.WriteString(w, `{"data":{"info":{"time":"01:00"}}}`)
		case "/getctlbarrage":
			io.WriteString(w, `{"data":{"cdn_list":"example.invalid","cdn_version":"bullet/v1"}}`)
		case "/bullet/v1/0.json":
			io.WriteString(w, `{"data":{"items":[{"time":2000,"content":"芒果弹幕","v2_position":2,"v2_color":{"color_left":{"r":10,"g":20,"b":30}}}]}}`)
		case "/pgc/view/web/season":
			io.WriteString(w, `{"code":0,"result":{"episodes":[{"id":123456,"cid":998877}]}}`)
		case "/998877.xml":
			w.Write(biliCompressed.Bytes())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := &directProvider{
		http: server.Client(),
		endpoints: directEndpoints{
			so360:       server.URL,
			web360:      server.URL,
			qqBarrage:   server.URL,
			iqiyiInfo:   server.URL,
			iqiyiBullet: server.URL,
			mgtvInfo:    server.URL,
			mgtvBarrage: server.URL,
			mgtvCDNBase: server.URL,
			biliAPI:     server.URL,
			biliComment: server.URL,
		},
	}
	client := &Client{direct: provider, HTTP: server.Client()}

	// Empty longData array must return nil without error.
	if eps, err := client.Search(context.Background(), "空结果", ""); err != nil || len(eps) != 0 {
		t.Fatalf("empty search = %+v, err=%v", eps, err)
	}

	eps, err := client.Search(context.Background(), "示例剧", "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Episode{}
	for _, ep := range eps {
		byID[ep.ID+"|"+ep.AnimeID] = ep
	}
	if _, ok := byID["qq:v00123456ab|drama1:qq"]; !ok {
		t.Fatalf("missing Tencent drama episode via episodesv2: %+v", eps)
	}
	if _, ok := byID["qiyi:100000001|drama1:qiyi"]; !ok {
		t.Fatalf("missing iQIYI inline drama episode: %+v", eps)
	}
	if ep, ok := byID["imgo:123456:654321|movie1:imgo"]; !ok || !strings.Contains(ep.Name, "正片") {
		t.Fatalf("missing Mango movie episode: %+v", eps)
	}
	if _, ok := byID["bili:ep:123456|anime1:bilibili1"]; !ok {
		t.Fatalf("missing Bilibili anime episode via v1/detail fallback: %+v", eps)
	}
	if _, ok := byID["imgo:123456:654321|show1:imgo"]; !ok {
		t.Fatalf("missing Mango variety episode: %+v", eps)
	}

	// Tencent comments (ordered, colored, top/bottom modes).
	qqComments, err := client.Comments(context.Background(), "qq:v00123456ab")
	if err != nil || len(qqComments) != 2 {
		t.Fatalf("qq comments = %+v, err=%v", qqComments, err)
	}
	if qqComments[0].Text != "腾讯首段" || qqComments[0].Mode != 5 || qqComments[0].Color != 0xff0000 || qqComments[0].Time != 5*time.Second {
		t.Fatalf("qq comment[0] = %+v", qqComments[0])
	}
	if qqComments[1].Text != "腾讯二段" || qqComments[1].Mode != 4 || qqComments[1].Color != 0x00ff00 || qqComments[1].Time != 35*time.Second {
		t.Fatalf("qq comment[1] = %+v", qqComments[1])
	}

	// iQIYI comments (zlib XML).
	qiyiComments, err := client.Comments(context.Background(), "qiyi:100000001")
	if err != nil || len(qiyiComments) != 1 || qiyiComments[0].Text != "爱奇艺弹幕" || qiyiComments[0].Mode != 5 || qiyiComments[0].Color != 0x00ff00 {
		t.Fatalf("qiyi comments = %+v, err=%v", qiyiComments, err)
	}

	// MangoTV comments (CDN JSON).
	mangoComments, err := client.Comments(context.Background(), "imgo:123456:654321")
	if err != nil || len(mangoComments) != 1 || mangoComments[0].Text != "芒果弹幕" || mangoComments[0].Mode != 4 || mangoComments[0].Color != (10<<16|20<<8|30) {
		t.Fatalf("mango comments = %+v, err=%v", mangoComments, err)
	}

	// Bilibili comments (raw-deflate XML).
	biliComments, err := client.Comments(context.Background(), "bili:ep:123456")
	if err != nil || len(biliComments) != 1 || biliComments[0].Text != "B站弹幕" || biliComments[0].Color != 0xff0000 {
		t.Fatalf("bili comments = %+v, err=%v", biliComments, err)
	}
}

func TestSampleCommentsCoversFullTimeline(t *testing.T) {
	all := make([]Comment, 100)
	for i := range all {
		all[i] = Comment{Time: time.Duration(i) * time.Second, Mode: 1, Text: fmt.Sprintf("c%d", i)}
	}
	got := sampleComments(all, 10)
	if len(got) != 10 {
		t.Fatalf("len = %d, want 10", len(got))
	}
	if got[0].Time != 0 || got[9].Time < 85*time.Second {
		t.Fatalf("sampling truncated ending: first=%s last=%s", got[0].Time, got[9].Time)
	}
}
