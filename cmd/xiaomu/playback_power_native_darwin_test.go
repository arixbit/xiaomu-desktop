package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The existing opt-in native test mode starts MyGo's macOS event loop.
// A local decoder is enough here: the assertion itself uses real macOS APIs.
func TestNativePlaybackPowerAssertions(t *testing.T) {
	if os.Getenv("XIAOMU_NATIVE_MEDIA_DIR") == "" {
		t.Skip("set XIAOMU_NATIVE_MEDIA_DIR to enable the macOS event loop")
	}
	a := newKeyboardTestApp(t)
	a.playbackPower.keepAwake = mygo.Power.KeepAwake
	tt := ui.NewTester(a.view, 1120, 760)
	assertPower := func(want bool) {
		t.Helper()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").Output()
			if err != nil {
				t.Fatal(err)
			}
			display, system := false, false
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, fmt.Sprintf("pid %d(", os.Getpid())) && strings.Contains(line, "Xiaomu video playback") {
					display = display || strings.Contains(line, "PreventUserIdleDisplaySleep")
					system = system || strings.Contains(line, "PreventUserIdleSystemSleep")
				}
			}
			if display == want && system == want {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("native assertions: display=%v system=%v, want %v", display, system, want)
			case <-tick.C:
			}
		}
	}
	assertPower(true)
	a.handleMediaCommand(mygo.MediaPause, 0)
	assertPower(false)
	a.handleMediaCommand(mygo.MediaPlay, 0)
	assertPower(true)
	a.player.Seek(a.player.Duration())
	tt.Frame()
	assertPower(false)
	a.handleMediaCommand(mygo.MediaPlay, 0)
	tt.Frame()
	assertPower(true)
	a.close()
	assertPower(false)
	t.Log("macOS reports display/system idle-sleep assertions only during playback; pause, end and close release them")
}
