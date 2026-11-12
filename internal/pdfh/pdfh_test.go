package pdfh

import "testing"

const page = `
<div class="list">
  <a href="/a" data-src="img1">One</a>
  <a href="/b" class="item">Two <span>x</span></a>
</div>
<h1>标题</h1>
<div class="eps"><a href="/play/1">第1集</a></div>
`

func TestPDFH(t *testing.T) {
	cases := []struct{ expr, want string }{
		{"div.list&&a:eq(0)&&Text", "One"},
		{"div.list&&a:eq(1)&&Text", "Two x"},
		{"div.list&&a:eq(0)&&href", "/a"},
		{"div.list&&a:eq(0)&&data-src", "img1"},
		{"div.list&&a:eq(0)&&Html", "One"},
		{"div.list&&a:eq(-1)&&href", "/b"},
		{".missing&&Text||h1&&Text", "标题"},
		{"div.list&&a&&Text", "One"},
		{"h1&&Text", "标题"},
	}
	for _, tc := range cases {
		if got := PDFH(page, tc.expr); got != tc.want {
			t.Errorf("PDFH %q = %q, want %q", tc.expr, got, tc.want)
		}
	}
}

func TestPDFA(t *testing.T) {
	got := PDFA(page, "div.list&&a")
	if len(got) != 2 {
		t.Fatalf("pdfa len %d: %#v", len(got), got)
	}
	if PDFH(got[0], "a&&Text") != "One" || PDFH(got[1], "a&&href") != "/b" {
		t.Fatalf("items: %#v", got)
	}
	one := PDFA(page, "div.eps&&a:eq(0)")
	if len(one) != 1 || PDFH(one[0], "a&&Text") != "第1集" {
		t.Fatalf("eq item: %#v", one)
	}
}

func TestPD(t *testing.T) {
	got := PD(page, "div.list&&a:eq(0)&&href", "https://fixture.example/dir/page.html")
	if got != "https://fixture.example/a" {
		t.Fatalf("pd = %q", got)
	}
	abs := PD(page, "div.list&&a:eq(0)&&href", "")
	if abs != "/a" {
		t.Fatalf("no base = %q", abs)
	}
}
