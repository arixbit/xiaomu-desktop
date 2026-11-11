package main

import (
	"image"
	"io"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/egoist/mygo/ui"
)

type stillVideo struct{ sent bool }

func (d *stillVideo) Next() (ui.VideoFrame, error) {
	if d.sent {
		return ui.VideoFrame{}, io.EOF
	}
	d.sent = true
	return ui.VideoFrame{Image: image.NewRGBA(image.Rect(0, 0, 2, 2)), Duration: time.Minute}, nil
}

func (d *stillVideo) Rewind() error { d.sent = false; return nil }

func TestPlayerScreenReturnsToLibrary(t *testing.T) {
	player, err := ui.NewVideoPlayer(&stillVideo{})
	if err != nil {
		t.Fatal(err)
	}
	a := &app{player: player, autoNext: true, playingSource: "线路", playingEpisode: "第1集"}
	a.browser.detail = &catalog.Detail{
		Name: "示例片",
		Sources: []catalog.Source{{
			Name: "线路",
			Episodes: []catalog.Episode{
				{Name: "第1集", ID: "one"},
				{Name: "第2集", ID: "two"},
			},
		}},
	}
	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("线路与选集"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("线路与选集") || !tt.HasText("第2集") || !tt.HasText("播完自动下一集") {
		t.Fatalf("player controls missing: %v", tt.Texts())
	}
	if err := tt.Click("返回片库"); err != nil {
		t.Fatal(err)
	}
	if a.player != nil || !tt.HasText("片库") {
		t.Fatalf("did not return to library: %v", tt.Texts())
	}
}
