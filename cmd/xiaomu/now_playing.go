package main

// Now Playing: while a video plays, the system shows its title (and
// episode where known) and playback state, and the media keys / headset
// buttons drive it. All of it is MyGo's NowPlaying API; xiaomu only
// publishes its own state and maps the remote commands onto the player
// actions it already has.
//
// The system advances the position itself while the state is Playing at
// Rate, so xiaomu only publishes on changes: a new title/episode (Set),
// a play/pause toggle or a seek (SetPlayback), and clears when playback
// stops. Nothing here runs every frame.

import (
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"

	"github.com/egoist/mygo"
)

// nowPlayingSink is the part of MyGo's Now Playing API xiaomu uses. It
// is a field on app so headless tests can substitute a recording fake;
// in the real app it is mygo.NowPlaying.
type nowPlayingSink interface {
	Set(mygo.NowPlayingInfo)
	SetPlayback(mygo.PlaybackState, time.Duration)
	Clear()
}

// nowPlayingTitle is the title the system shows: the live channel name,
// the file name for a local file, the episode's title, or 直链播放.
func (a *app) nowPlayingTitle() string {
	if a.playingLive && a.live.playingChannel >= 0 && a.live.playingChannel < len(a.live.channels) {
		return a.live.channels[a.live.playingChannel].Name
	}
	if a.playingFile {
		return a.playingName
	}
	if a.title != nil && a.title.name != "" {
		return a.title.name
	}
	return "直链播放"
}

// syncNowPlaying publishes the full Now Playing info for what plays
// now, or clears it when nothing plays.
func (a *app) syncNowPlaying() {
	if a.nowPlaying == nil {
		return
	}
	if a.player == nil {
		a.nowPlaying.Clear()
		return
	}
	state := mygo.PlaybackPaused
	if a.player.Playing() {
		state = mygo.PlaybackPlaying
	}
	a.nowPlaying.Set(mygo.NowPlayingInfo{
		Title:    a.nowPlayingTitle(),
		Album:    a.playingEpisode,
		Duration: a.player.Duration(),
		Position: a.player.Position(),
		Rate:     a.player.Rate(),
		State:    state,
		Commands: mygo.MediaPlay | mygo.MediaPause | mygo.MediaNext | mygo.MediaPrevious,
	})
}

// syncNowPlayingState republishes only the play state and position,
// e.g. after a play/pause toggle or a seek.
func (a *app) syncNowPlayingState() {
	if a.nowPlaying == nil || a.player == nil {
		return
	}
	state := mygo.PlaybackPaused
	if a.player.Playing() {
		state = mygo.PlaybackPlaying
	}
	a.nowPlaying.SetPlayback(state, a.player.Position())
}

// handleMediaCommand handles one remote command from the system
// (media keys, headset, Control Center). Next/previous map to the
// next/previous episode or live channel; with no next/previous one the
// command does nothing rather than erroring.
func (a *app) handleMediaCommand(cmd mygo.MediaCommand, position time.Duration) {
	p := a.player
	if p == nil {
		return
	}
	switch cmd {
	case mygo.MediaPlay:
		if !p.Playing() {
			p.Play()
			a.syncNowPlayingState()
		}
	case mygo.MediaPause:
		if p.Playing() {
			p.Pause()
			a.syncNowPlayingState()
		}
	case mygo.MediaNext:
		a.playNextMedia()
	case mygo.MediaPrevious:
		a.playPreviousMedia()
	}
}

// nextEpisode returns the episode after the one playing, if there is one.
func (a *app) nextEpisode() (line string, episode catalog.Episode, ok bool) {
	source, index, found := a.currentEpisode()
	if !found {
		return "", catalog.Episode{}, false
	}
	episodes := a.title.detail.Sources[source].Episodes
	if index+1 >= len(episodes) {
		return "", catalog.Episode{}, false
	}
	return a.title.detail.Sources[source].Name, episodes[index+1], true
}

// prevEpisode returns the episode before the one playing, if there is one.
func (a *app) prevEpisode() (line string, episode catalog.Episode, ok bool) {
	source, index, found := a.currentEpisode()
	if !found {
		return "", catalog.Episode{}, false
	}
	if index-1 < 0 {
		return "", catalog.Episode{}, false
	}
	return a.title.detail.Sources[source].Name, a.title.detail.Sources[source].Episodes[index-1], true
}

func (a *app) playNextMedia() {
	if a.playingLive {
		if next := a.live.playingChannel + 1; next < len(a.live.channels) {
			a.playLiveChannel(next, 0)
		}
		return
	}
	if line, episode, ok := a.nextEpisode(); ok {
		a.playEpisode(line, episode)
	}
}

func (a *app) playPreviousMedia() {
	if a.playingLive {
		if prev := a.live.playingChannel - 1; prev >= 0 {
			a.playLiveChannel(prev, 0)
		}
		return
	}
	if line, episode, ok := a.prevEpisode(); ok {
		a.playEpisode(line, episode)
	}
}

// compile-time check that the real Now Playing object satisfies the sink.
var _ nowPlayingSink = mygo.NowPlaying
