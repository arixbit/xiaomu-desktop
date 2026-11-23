package danmaku

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseFormats(t *testing.T) {
	for _, tc := range []struct {
		name, data string
	}{
		{"json", `{"comments":[{"p":"2.5,1,16711680,user","m":"第二条"},{"p":"1,5,16777215,user","m":"第一条 & <你好>"},{"p":"3,4,255,user","m":"底部"}]}`},
		{"json with bom", "\xef\xbb\xbf" + `{"comments":[{"p":"2.5,1,16711680,user","m":"第二条"},{"p":"1,5,16777215,user","m":"第一条 & <你好>"},{"p":"3,4,255,user","m":"底部"}]}`},
		{"xml", `<i><d p="2.5,1,25,16711680,0,0,0,0">第二条</d><d p="1,5,25,16777215,0,0,0,0">第一条 &amp; &lt;你好&gt;</d><d p="3,4,25,255,0,0,0,0">底部</d></i>`},
		{"xml with bom", "\xef\xbb\xbf" + `<i><d p="2.5,1,25,16711680,0,0,0,0">第二条</d><d p="1,5,25,16777215,0,0,0,0">第一条 &amp; &lt;你好&gt;</d><d p="3,4,25,255,0,0,0,0">底部</d></i>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.data))
			if err != nil || len(got) != 3 {
				t.Fatalf("parse = %+v, %v", got, err)
			}
			if got[0].Text != "第一条 & <你好>" || got[0].Time != time.Second || got[0].Mode != 5 || got[0].Color != 0xffffff {
				t.Fatalf("first = %+v", got[0])
			}
			if got[1].Time != 2500*time.Millisecond || got[1].Color != 0xff0000 || got[2].Mode != 4 || got[2].Color != 255 {
				t.Fatalf("comments = %+v", got)
			}
		})
	}
}

func TestParseRejectsErrorPages(t *testing.T) {
	for _, data := range []string{"", `<html><body>Access denied</body></html>`, `<i><d p="0,1,25,0">broken`, `{"comments":`, `{"message":"denied"}`, `{"errorCode":401,"comments":[]}`} {
		t.Run(data, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("accepted an invalid response")
			}
		})
	}
	for _, data := range []string{`{"comments":[]}`, `<i></i>`} {
		if comments, err := Parse([]byte(data)); err != nil || len(comments) != 0 {
			t.Fatalf("empty response = %+v, %v", comments, err)
		}
	}
}

func TestParseSkipsInvalidCommentsAndBoundsText(t *testing.T) {
	data := `{"comments":[`
	for _, params := range []string{"NaN,1,1", "Inf,1,1", "-1,1,1", "86401,1,1", "0,7,1", "0,1,16777216", "missing"} {
		data += fmt.Sprintf(`{"p":%q,"m":"invalid"},`, params)
	}
	data += fmt.Sprintf(`{"p":"0,1,0","m":%q}]}`, strings.Repeat("字", 300)+"\n")
	comments, err := Parse([]byte(data))
	if err != nil || len(comments) != 1 || len([]rune(comments[0].Text)) != 200 || comments[0].Color != 0 {
		t.Fatalf("comments = %+v, %v", comments, err)
	}
}
