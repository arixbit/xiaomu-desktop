package danmaku

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkParseJSON10k(b *testing.B) {
	var buf bytes.Buffer
	buf.WriteString(`{"errorCode":0,"comments":[`)
	for i := range 10000 {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"p":"%.2f,1,16777215,u","m":"测试弹幕内容第%d条"}`, float64(i)*0.25, i)
	}
	buf.WriteString(`]}`)
	payload := buf.Bytes()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		comments, err := Parse(payload)
		if err != nil || len(comments) != 10000 {
			b.Fatalf("len=%d err=%v", len(comments), err)
		}
	}
}

func BenchmarkCleanCommentText(b *testing.B) {
	samples := []string{
		"前方高能预警",
		"哈哈哈笑死我了",
		"  需要去空格的   多空白\n弹幕  ",
		"English and 中文 mixed comment 123",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, s := range samples {
			_ = cleanCommentText(s)
		}
	}
}

func TestCleanCommentTextEquivalence(t *testing.T) {
	cases := []string{
		"",
		"   \t\n\r ",
		"前方高能",
		"  前方高能  ",
		"a  b\tc\nd",
		"中文\u3000全角空格  测试",
		strings.Repeat("弹", 250),
		"  " + strings.Repeat("a b ", 120),
	}
	for _, tc := range cases {
		want := strings.Join(strings.Fields(tc), " ")
		if runes := []rune(want); len(runes) > 200 {
			want = string(runes[:200])
		}
		if got := cleanCommentText(tc); got != want {
			t.Errorf("cleanCommentText(%q) = %q, want %q", tc, got, want)
		}
	}
}

func TestNewClientSharesTransport(t *testing.T) {
	c1, err := NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := NewClient("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if c1.HTTP.Transport != c2.HTTP.Transport {
		t.Fatal("NewClient created separate http.Transports instead of sharing connection pool")
	}
}
