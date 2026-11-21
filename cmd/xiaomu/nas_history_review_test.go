package main

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/nas"
	"github.com/egoist/mygo/ui"
)

func TestNASHistorySpecialShareNames(t *testing.T) {
	for _, share := range []string{"Movies#HD", "Movies 100%", "Movies%23HD"} {
		t.Run(share, func(t *testing.T) {
			const server = "example.com"
			const path = "Example.mp4"
			saved := nas.Server{Address: server, Share: share}
			a := &app{playingNAS: &nasPlaying{Server: server, Share: share, Path: path, Name: path}}
			a.nas.server, a.nas.hasServer = saved, true
			player, err := ui.NewVideoPlayer(&stillVideo{})
			if err != nil {
				t.Fatal(err)
			}
			defer player.Close()
			a.recordHistory(player, time.Now())
			if len(a.history.entries) != 1 {
				t.Fatal("NAS play was not recorded")
			}
			data, err := json.Marshal(a.history.entries[0])
			if err != nil {
				t.Fatal(err)
			}
			var restored historyEntry
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			shareURL, err := url.Parse(restored.ConfigURL)
			if err != nil {
				t.Fatal(err)
			}
			if shareURL.Host != server || shareURL.Path != "/"+share || shareURL.Fragment != "" {
				t.Fatalf("invalid share URL %q", restored.ConfigURL)
			}
			if !a.historyAvailable(restored) {
				t.Fatalf("new record did not match share %q", share)
			}
			legacy := historyEntry{
				ConfigURL: "smb://" + server + "/" + share, Line: server,
				Name: path, VideoID: path, HomeHidden: true,
			}
			if !a.historyAvailable(legacy) || restored.dedupKey() != legacy.dedupKey() {
				t.Fatalf("legacy record did not identify the same file on %q", share)
			}
			var history historyStore
			history.note(legacy)
			history.note(restored)
			if len(history.entries) != 1 || !history.entries[0].HomeHidden {
				t.Fatalf("re-recording left duplicate history or lost hidden state: %+v", history.entries)
			}
		})
	}
}

func TestNASContinueWatchingUsesFileIdentity(t *testing.T) {
	const name = "01.mp4"
	const server = "example.com"
	const share = "Movies"
	online := historyEntry{Name: name, ConfigURL: "https://example.com/config", SiteKey: "nas", HomeHidden: true}
	first := historyEntry{Kind: historyKindNAS, ConfigURL: "smb://example.com/Movies", VideoID: `A\01.mp4`, Name: name}
	second := historyEntry{Kind: historyKindNAS, ConfigURL: "smb://example.com/Movies", VideoID: `B\01.mp4`, Name: name}
	a := &app{open: func(string) (*ui.VideoPlayer, error) {
		return ui.NewVideoPlayer(&stillVideo{})
	}}
	defer a.close()
	a.nas.server = nas.Server{Address: server, Share: share}
	a.nas.hasServer, a.nas.connected = true, true
	a.history.note(online)
	a.history.note(first)
	a.history.note(second)

	a.setContinueWatchingHidden(first.dedupKey(), true)
	a.revealContinueWatching(name)
	if !a.history.entries[1].HomeHidden || a.history.entries[0].HomeHidden || a.history.entries[2].HomeHidden {
		t.Fatalf("wrong records hidden after hiding A and replaying the site title: %+v", a.history.entries)
	}

	a.setContinueWatchingHidden(second.dedupKey(), true)
	if !a.playNASFile(server, share, first.VideoID, first.Name) {
		t.Fatalf("replay NAS file: %s", a.status)
	}
	if a.history.entries[1].HomeHidden || !a.history.entries[0].HomeHidden {
		t.Fatalf("replaying A did not restore just its card: %+v", a.history.entries)
	}
}

func TestFollowingHistorySkipsSameNamedNASFile(t *testing.T) {
	const name = "Example.mp4"
	online := historyEntry{Name: name, ConfigURL: "https://example.com/config", SiteKey: "nas", VideoID: "7"}
	nasFile := historyEntry{Name: name, ConfigURL: "smb://example.com/Movies", Kind: historyKindNAS, VideoID: name}
	a := &app{}
	a.history.note(nasFile)
	if _, ok := a.followingHistory(name); ok {
		t.Fatal("NAS-only history counted as online viewing")
	}
	a.history.note(online)
	a.history.note(nasFile)
	got, ok := a.followingHistory(name)
	if !ok || got != online {
		t.Fatalf("following history = %+v, %v, want the online record", got, ok)
	}
}
