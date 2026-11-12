package urljoin

import "testing"

func TestJoin(t *testing.T) {
	cases := []struct {
		base, ref, want string
	}{
		{"https://fixture.example", "/list/1/1", "https://fixture.example/list/1/1"},
		{"https://fixture.example/v/7", "/play/7", "https://fixture.example/play/7"},
		{"https://fixture.example/v/7", "https://cdn.example/a.m3u8", "https://cdn.example/a.m3u8"},
		{"https://fixture.example/dir/page.html", "a.mp4", "https://fixture.example/dir/a.mp4"},
		{"https://fixture.example/x", "//cdn.example/a", "https://cdn.example/a"},
		{"https://fixture.example/a", "", "https://fixture.example/a"},
		{"", "rel", "rel"},
	}
	for _, tc := range cases {
		if got := Join(tc.base, tc.ref); got != tc.want {
			t.Errorf("Join(%q, %q) = %q, want %q", tc.base, tc.ref, got, tc.want)
		}
	}
}
