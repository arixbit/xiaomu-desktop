package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/nas"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNASPendingOpenCannotInstallAfterConnectionChanges(t *testing.T) {
	for _, action := range []struct {
		name string
		run  func(*app, nas.Server)
	}{
		{"disconnect", func(a *app, _ nas.Server) { a.disconnectNAS() }},
		{"remove", func(a *app, _ nas.Server) { a.removeNAS() }},
		{"close", func(a *app, _ nas.Server) { a.close() }},
		{"reconnect", func(a *app, srv nas.Server) {
			a.disconnectNAS()
			srv.Password = "new"
			a.startNASConnection(srv)
		}},
	} {
		t.Run(action.name, func(t *testing.T) {
			mygo.App.SetPath(mygo.PathUserData, t.TempDir())
			t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

			a := &app{}
			defer func() {
				if !a.closed {
					a.close()
				}
			}()
			srv := nas.Server{Address: "192.168.1.10", Share: "Movies", Password: "old"}
			// The server is still selected after the old session fails. The
			// next player request is responsible for redialing.
			a.nas.server, a.nas.hasServer, a.nas.connected = srv, true, true
			started, release := make(chan struct{}), make(chan struct{})
			a.nas.dial = func(ctx context.Context, server nas.Server) (*nas.Client, error) {
				if server == srv {
					close(started)
					<-release
				}
				return fakeNASDial()(ctx, server)
			}
			done := make(chan error, 1)
			go func() {
				f, err := a.openNASFile(context.Background(), srv.Address, srv.Share, "示例.mp4")
				if f != nil {
					_ = f.Close()
				}
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				close(release)
				t.Fatal("the pending request never started dialing")
			}
			action.run(a, srv)
			a.nas.mu.Lock()
			expectedClient := a.nas.client
			a.nas.mu.Unlock()
			close(release)
			if action.name == "reconnect" && expectedClient == nil {
				t.Error("reconnect did not install a new SMB session")
			}
			select {
			case err := <-done:
				if !errors.Is(err, errNASDisconnected) {
					t.Errorf("pending open returned %v after %s", err, action.name)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("the pending request never finished")
			}
			a.nas.mu.Lock()
			client := a.nas.client
			a.nas.mu.Unlock()
			if client != expectedClient {
				t.Error("the pending request installed a session from the previous connection")
			}
		})
	}
}

func TestNASResumeCannotStartAfterReconnect(t *testing.T) {
	mygo.App.SetPath(mygo.PathUserData, t.TempDir())
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })

	a := &app{}
	defer a.close()
	a.nas.dial = fakeNASDial()
	srv := nas.Server{Address: "192.168.1.10", Share: "Movies"}
	a.startNASConnection(srv)

	updates := make(chan func(), 1)
	a.dispatch = func(update func()) { updates <- update }
	a.resumeNAS(historyEntry{
		Kind:      historyKindNAS,
		ConfigURL: srv.URL(),
		VideoID:   "示例.mp4",
		Name:      "示例.mp4",
	})
	var apply func()
	select {
	case apply = <-updates:
	case <-time.After(3 * time.Second):
		t.Fatal("the pending resume did not reach the UI")
	}
	a.disconnectNAS()
	a.dispatch = nil
	a.startNASConnection(srv)
	if !a.nas.connected {
		t.Fatalf("reconnect failed: %s", a.status)
	}
	opened := false
	a.open = func(string) (*ui.VideoPlayer, error) {
		opened = true
		return nil, errors.New("unexpected playback")
	}
	apply()
	if opened {
		t.Fatal("resume from the previous connection started playback after reconnect")
	}
}
