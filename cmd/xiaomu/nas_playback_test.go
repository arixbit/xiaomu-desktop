package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/nas"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNASPlaybackTokenAfterFailedReplacement(t *testing.T) {
	srv := nas.Server{Address: "192.168.1.10", Share: "Movies", User: "guest"}
	tests := []struct {
		name    string
		replace func(*app) bool
		kept    bool
	}{
		{"NAS file", func(a *app) bool {
			return a.playNASFile(srv.Address, srv.Share, "纪录片.mov", "纪录片.mov")
		}, false},
		{"direct URL", func(a *app) bool {
			return a.playDirectURL("https://example.com/video.mp4", nil)
		}, false},
		{"local file", func(a *app) bool {
			return a.openLocalFile("video.mp4")
		}, false},
		{"staged URL", func(a *app) bool {
			return a.openPlaybackURL("https://example.com/video.mp4", nil, true)
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mygo.App.SetPath(mygo.PathUserData, t.TempDir())
			t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })
			a := &app{}
			defer a.close()
			a.nas.dial = fakeNASDial()
			a.startNASConnection(srv)

			var playbackURL string
			a.open = func(raw string) (*ui.VideoPlayer, error) {
				playbackURL = raw
				return ui.NewVideoPlayer(&stillVideo{})
			}
			if !a.playNASFile(srv.Address, srv.Share, "示例.mp4", "示例.mp4") {
				t.Fatalf("initial playback failed: %s", a.status)
			}
			failedOpen := func(string) (*ui.VideoPlayer, error) {
				return nil, errors.New("fixture: cannot open replacement")
			}
			a.open, a.openLocal = failedOpen, failedOpen
			if tt.replace(a) {
				t.Fatal("replacement succeeded")
			}

			wantStatus := http.StatusForbidden
			if tt.kept {
				wantStatus = http.StatusOK
			}
			response := httptest.NewRecorder()
			a.proxy.ServeHTTP(response, httptest.NewRequest(http.MethodHead, playbackURL, nil))
			if response.Code != wantStatus || (a.player != nil) != tt.kept || (a.playingNAS != nil) != tt.kept {
				t.Fatalf("old playback: HTTP status=%d player=%v NAS=%v, want status=%d kept=%v",
					response.Code, a.player != nil, a.playingNAS != nil, wantStatus, tt.kept)
			}
		})
	}
}
