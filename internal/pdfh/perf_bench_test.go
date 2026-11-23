package pdfh

import (
	"strings"
	"testing"
)

func BenchmarkPDFHFallback(b *testing.B) {
	bigPage := "<html><body>" + strings.Repeat(`<div class="card"><a href="/v/1" title="片名"><img data-src="https://example.com/a.jpg"></a><span class="note">更新至10集</span></div>`, 50) + "<h1>最终标题</h1></body></html>"
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got := PDFH(bigPage, ".missing1&&Text||.missing2&&Text||.missing3&&Text||h1&&Text")
		if got != "最终标题" {
			b.Fatalf("got=%q", got)
		}
	}
}
