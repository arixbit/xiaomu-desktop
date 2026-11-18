package main

import (
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type recordingNowPlaying struct {
	sets   []mygo.NowPlayingInfo
	states []mygo.PlaybackState
	clears int
}

func (f *recordingNowPlaying) Set(info mygo.NowPlayingInfo) { f.sets = append(f.sets, info) }
func (f *recordingNowPlaying) SetPlayback(state mygo.PlaybackState, _ time.Duration) {
	f.states = append(f.states, state)
}
func (f *recordingNowPlaying) Clear() { f.clears++ }

func TestSyncNowPlayingPublishesTitleAndEpisode(t *testing.T) {
	a := newPlayerTestApp(t)
	fake := &recordingNowPlaying{}
	a.nowPlaying = fake
	a.syncNowPlaying()
	if len(fake.sets) != 1 {
		t.Fatalf("sync published %d infos, want 1", len(fake.sets))
	}
	info := fake.sets[0]
	if info.Title != "示例片" || info.Album != "第1集" || info.State != mygo.PlaybackPlaying {
		t.Fatalf("now playing info = %+v, want title 示例片, episode 第1集, playing", info)
	}
	if info.Commands&(mygo.MediaPlay|mygo.MediaPause|mygo.MediaNext|mygo.MediaPrevious) == 0 {
		t.Fatalf("now playing commands = %v, want play/pause/next/previous", info.Commands)
	}
}

func TestSyncNowPlayingStateFollowsPauseAndPlay(t *testing.T) {
	a := newPlayerTestApp(t)
	fake := &recordingNowPlaying{}
	a.nowPlaying = fake
	a.player.Pause()
	a.syncNowPlayingState()
	if len(fake.states) != 1 || fake.states[0] != mygo.PlaybackPaused {
		t.Fatalf("states after pause = %v, want [paused]", fake.states)
	}
	a.player.Play()
	a.syncNowPlayingState()
	if len(fake.states) != 2 || fake.states[1] != mygo.PlaybackPlaying {
		t.Fatalf("states after play = %v, want [paused playing]", fake.states)
	}
}

func TestLeavePlayerClearsNowPlaying(t *testing.T) {
	a := newPlayerTestApp(t)
	fake := &recordingNowPlaying{}
	a.nowPlaying = fake
	a.leavePlayer()
	if a.player != nil || fake.clears != 1 {
		t.Fatalf("leave did not clear now playing: player=%v clears=%d", a.player != nil, fake.clears)
	}
}

func TestNextAndPrevEpisodeSelection(t *testing.T) {
	a := newPlayerTestApp(t)
	if line, episode, ok := a.nextEpisode(); !ok || line != "线路" || episode.Name != "第2集" {
		t.Fatalf("next from 第1集 = %q %q %v, want 线路 第2集 true", line, episode.Name, ok)
	}
	if _, _, ok := a.prevEpisode(); ok {
		t.Fatal("prev from 第1集 exists, want none")
	}
	a.playingEpisode = "第2集"
	if _, _, ok := a.nextEpisode(); ok {
		t.Fatal("next from 第2集 exists, want none")
	}
	if line, episode, ok := a.prevEpisode(); !ok || line != "线路" || episode.Name != "第1集" {
		t.Fatalf("prev from 第2集 = %q %q %v, want 线路 第1集 true", line, episode.Name, ok)
	}
}

func TestMediaCommandTogglesPlayback(t *testing.T) {
	a := newPlayerTestApp(t)
	fake := &recordingNowPlaying{}
	a.nowPlaying = fake
	a.handleMediaCommand(mygo.MediaPause, 0)
	if a.player.Playing() || len(fake.states) != 1 || fake.states[0] != mygo.PlaybackPaused {
		t.Fatalf("media pause: playing=%v states=%v", a.player.Playing(), fake.states)
	}
	a.handleMediaCommand(mygo.MediaPlay, 0)
	if !a.player.Playing() || len(fake.states) != 2 || fake.states[1] != mygo.PlaybackPlaying {
		t.Fatalf("media play: playing=%v states=%v", a.player.Playing(), fake.states)
	}
}

func TestMediaCommandNextPrevLiveChannel(t *testing.T) {
	a := &app{open: func(string) (*ui.VideoPlayer, error) { return ui.NewVideoPlayer(&stillVideo{}) }}
	t.Cleanup(a.close)
	a.live.channels = []iptv.Channel{
		{Name: "频道一", Streams: []iptv.Stream{{URL: "https://example.com/one.m3u8"}}},
		{Name: "频道二", Streams: []iptv.Stream{{URL: "https://example.com/two.m3u8"}}},
	}
	a.playLiveChannel(0, 0)
	if !a.playingLive || a.live.playingChannel != 0 || a.player == nil {
		t.Fatalf("live did not start: live=%v channel=%d player=%v", a.playingLive, a.live.playingChannel, a.player != nil)
	}
	a.handleMediaCommand(mygo.MediaNext, 0)
	if a.live.playingChannel != 1 {
		t.Fatalf("media next stayed on channel %d, want 1", a.live.playingChannel)
	}
	a.handleMediaCommand(mygo.MediaNext, 0)
	if a.live.playingChannel != 1 {
		t.Fatalf("media next past the last channel moved to %d, want no-op", a.live.playingChannel)
	}
	a.handleMediaCommand(mygo.MediaPrevious, 0)
	if a.live.playingChannel != 0 {
		t.Fatalf("media previous moved to channel %d, want 0", a.live.playingChannel)
	}
	a.handleMediaCommand(mygo.MediaPrevious, 0)
	if a.live.playingChannel != 0 {
		t.Fatalf("media previous before the first channel moved to %d, want no-op", a.live.playingChannel)
	}
}

func TestMediaCommandWithoutPlayerIsNoop(t *testing.T) {
	a := &app{}
	t.Cleanup(a.close)
	fake := &recordingNowPlaying{}
	a.nowPlaying = fake
	a.handleMediaCommand(mygo.MediaPause, 0)
	a.handleMediaCommand(mygo.MediaNext, 0)
	if len(fake.sets) != 0 || len(fake.states) != 0 || fake.clears != 0 {
		t.Fatalf("media command without a player published: %+v", fake)
	}
}

func TestNowPlayingTitleForLiveAndDirect(t *testing.T) {
	a := newPlayerTestApp(t)
	a.live.channels = []iptv.Channel{{Name: "频道一"}}
	a.playingLive, a.live.playingChannel = true, 0
	if got := a.nowPlayingTitle(); got != "频道一" {
		t.Fatalf("live title = %q, want 频道一", got)
	}
	a.playingLive = false
	a.title = nil
	if got := a.nowPlayingTitle(); got != "直链播放" {
		t.Fatalf("direct title = %q, want 直链播放", got)
	}
	a.playingFile, a.playingName = true, "本地片.mp4"
	if got := a.nowPlayingTitle(); got != "本地片.mp4" {
		t.Fatalf("file title = %q, want 本地片.mp4", got)
	}
}
