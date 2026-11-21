package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestWindowBoundsInWorkArea(t *testing.T) {
	work := mygo.Rectangle{Width: 1024, Height: 720}
	tests := []struct {
		name   string
		bounds mygo.Rectangle
		work   mygo.Rectangle
		want   mygo.Rectangle
	}{
		{
			name:   "oversized window on a small display",
			bounds: mygo.Rectangle{X: -48, Y: -20, Width: 1120, Height: 760},
			work:   work,
			want:   work,
		},
		{
			name:   "saved position clipped on a secondary display",
			bounds: mygo.Rectangle{X: -2300, Y: 470, Width: 800, Height: 600},
			work:   mygo.Rectangle{X: -2240, Y: 32, Width: 1280, Height: 900},
			want:   mygo.Rectangle{X: -2240, Y: 332, Width: 800, Height: 600},
		},
		{
			name:   "preserve visible position and size",
			bounds: mygo.Rectangle{X: 80, Y: 45, Width: 800, Height: 600},
			work:   work,
			want:   mygo.Rectangle{X: 80, Y: 45, Width: 800, Height: 600},
		},
		{
			name:   "no display reported",
			bounds: mygo.Rectangle{X: 80, Y: 45, Width: 800, Height: 600},
			want:   mygo.Rectangle{X: 80, Y: 45, Width: 800, Height: 600},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := windowBoundsInWorkArea(tt.bounds, tt.work); got != tt.want {
				t.Errorf("fitted bounds = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPlayRejectsEmpty(t *testing.T) {
	a := &app{open: func(string) (*ui.VideoPlayer, error) {
		t.Fatal("should not open")
		return nil, nil
	}}
	a.play()
	if !strings.Contains(a.status, "http") {
		t.Fatalf("status %q", a.status)
	}
}

func TestPlayWrapsThroughProxy(t *testing.T) {
	a := &app{
		rawURL:  "https://example.com/a.mp4",
		referer: "https://example.com/",
		ua:      "TestUA",
	}
	var opened string
	a.open = func(u string) (*ui.VideoPlayer, error) {
		opened = u
		if a.proxy.Header("User-Agent") != "TestUA" || a.proxy.Header("Referer") != "https://example.com/" {
			t.Fatalf("headers ua=%q ref=%q", a.proxy.Header("User-Agent"), a.proxy.Header("Referer"))
		}
		return nil, nil
	}
	a.play()
	if a.player != nil {
		t.Fatal("stub returned no player")
	}
	if a.status != "正在播放" {
		t.Fatalf("status %q", a.status)
	}
	if !strings.HasPrefix(opened, "http://127.0.0.1:") || !strings.Contains(opened, "example.com") {
		t.Fatalf("opened %q", opened)
	}
	a.proxy.Close()
}

func TestPlaybackHeaders(t *testing.T) {
	h := playbackHeaders(" https://example.com/ ", "")
	if h["Referer"] != "https://example.com/" || len(h) != 1 {
		t.Fatalf("%v", h)
	}
}

func TestViewPlay(t *testing.T) {
	a := &app{}
	var opened string
	a.open = func(u string) (*ui.VideoPlayer, error) {
		opened = u
		return nil, nil
	}
	tt := ui.NewTester(a.view, 960, 640)
	if err := tt.Click("打开链接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("播放"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("请输入 http 或 https 播放地址") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("地址"); err != nil {
		t.Fatal(err)
	}
	tt.Type("https://example.com/clip.m3u8")
	if err := tt.Click("Referer"); err != nil {
		t.Fatal(err)
	}
	tt.Type("https://example.com/")
	if err := tt.Click("播放"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("正在播放") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if !strings.Contains(opened, "clip.m3u8") || a.proxy.Header("Referer") != "https://example.com/" {
		t.Fatalf("opened %q referer %q", opened, a.proxy.Header("Referer"))
	}
	a.proxy.Close()
}
