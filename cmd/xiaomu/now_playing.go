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
// stops. Frames observe changes but only publish when something changes.

import (
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// nowPlayingSink is the part of MyGo's Now Playing API xiaomu uses. It
// is a field on app so headless tests can substitute a recording fake;
// in the real app it is mygo.NowPlaying.
type nowPlayingSink interface {
	Set(mygo.NowPlayingInfo)
	SetPlayback(mygo.PlaybackState, time.Duration)
	Clear()
}

type nowPlayingSnapshot struct {
	player *ui.VideoPlayer
	info   mygo.NowPlayingInfo
	at     time.Time
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
	if a.playingNAS != nil {
		return a.playingNAS.Name
	}
	if !a.playingDirect && a.title != nil && a.title.name != "" {
		return a.title.name
	}
	return "直链播放"
}

// syncNowPlaying publishes the full Now Playing info for what plays
// now, or clears it when nothing plays.
func (a *app) syncNowPlaying() {
	defer a.syncPlaybackPower(time.Now())
	if a.nowPlaying == nil {
		return
	}
	if a.player == nil {
		a.nowPlaying.Clear()
		a.nowPlayingLast = nowPlayingSnapshot{}
		return
	}
	info := a.currentNowPlaying()
	a.nowPlaying.Set(info)
	a.nowPlayingLast = nowPlayingSnapshot{a.player, info, time.Now()}
}

func (a *app) currentNowPlaying() mygo.NowPlayingInfo {
	state := mygo.PlaybackPaused
	if a.player.Ended() || a.player.Err() != nil {
		state = mygo.PlaybackStopped
	} else if a.player.Playing() {
		state = mygo.PlaybackPlaying
	}
	commands := mygo.MediaPlay | mygo.MediaPause
	if _, _, ok := a.nextEpisode(); ok || a.playingLive && a.live.playingChannel+1 < len(a.live.channels) {
		commands |= mygo.MediaNext
	}
	if _, _, ok := a.prevEpisode(); ok || a.playingLive && a.live.playingChannel > 0 {
		commands |= mygo.MediaPrevious
	}
	return mygo.NowPlayingInfo{
		Title:    a.nowPlayingTitle(),
		Album:    a.playingEpisode,
		Duration: a.player.Duration(),
		Position: a.player.Position(),
		Rate:     a.player.Rate(),
		State:    state,
		Commands: commands,
	}
}

func sameNowPlayingMedia(a, b mygo.NowPlayingInfo) bool {
	return a.Title == b.Title && a.Album == b.Album && a.Duration == b.Duration && a.Rate == b.Rate && a.Commands == b.Commands
}

// observeNowPlaying catches readiness, natural ends, failures, rate changes
// and completed asynchronous seeks. Normal progress is advanced by the OS.
func (a *app) observeNowPlaying() {
	if a.nowPlaying == nil {
		return
	}
	last := a.nowPlayingLast
	if a.player == nil {
		if last.player != nil {
			a.syncNowPlaying()
		}
		return
	}
	info := a.currentNowPlaying()
	if last.player != a.player || !sameNowPlayingMedia(last.info, info) {
		a.syncNowPlaying()
		return
	}
	position := last.info.Position
	if last.info.State == mygo.PlaybackPlaying {
		position += time.Duration(float64(time.Since(last.at)) * last.info.Rate)
	}
	if info.State != last.info.State || info.Position-position > time.Second || position-info.Position > time.Second {
		a.syncNowPlayingState()
	}
}

// syncNowPlayingState republishes only the play state and position,
// e.g. after a play/pause toggle or a seek.
func (a *app) syncNowPlayingState() {
	defer a.syncPlaybackPower(time.Now())
	if a.nowPlaying == nil || a.player == nil {
		return
	}
	info := a.currentNowPlaying()
	if a.nowPlayingLast.player != a.player || !sameNowPlayingMedia(a.nowPlayingLast.info, info) {
		a.syncNowPlaying()
		return
	}
	a.nowPlaying.SetPlayback(info.State, info.Position)
	a.nowPlayingLast = nowPlayingSnapshot{a.player, info, time.Now()}
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
	if !a.hasEpisodes() {
		return "", catalog.Episode{}, false
	}
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
	if !a.hasEpisodes() {
		return "", catalog.Episode{}, false
	}
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
