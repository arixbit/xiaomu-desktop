package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	mpvplayer "github.com/arixbit/mygo-player"
	"github.com/egoist/mygo/ui"
)

type fakeTrackPlayer struct {
	*systemMediaPlayer
	videos      []mediaTrack
	audios      []mediaTrack
	subs        []mediaTrack
	subVisible  bool
	addedSubs   []string
	activeAudio int64
	activeSub   int64
	hwdec       string
	loop        bool
	subDelay    time.Duration
	subScale    float64
	subPos      int
	videoRotate int
	panscan     float64
	props       map[string]string
}

func newFakeTrackPlayer(t *testing.T) *fakeTrackPlayer {
	t.Helper()
	vp, err := ui.NewVideoPlayer(&timedVideo{})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeTrackPlayer{
		systemMediaPlayer: &systemMediaPlayer{p: vp},
		videos: []mediaTrack{
			{ID: 1, Codec: "hevc", Width: 3840, Height: 2160, FPS: 23.976, Selected: true},
		},
		audios: []mediaTrack{
			{ID: 1, Title: "国语", Lang: "zh", Codec: "dts", Channels: 6, SampleRate: 48000, Selected: true},
			{ID: 2, Title: "原声", Lang: "en", Codec: "eac3", Channels: 6, SampleRate: 48000, Selected: false},
		},
		subs: []mediaTrack{
			{ID: 1, Title: "简体中字", Lang: "zh", Codec: "ass", Selected: true},
			{ID: 2, Title: "English", Lang: "en", Codec: "subrip", Selected: false},
		},
		subVisible:  true,
		activeAudio: 1,
		activeSub:   1,
		hwdec:       "videotoolbox",
		props:       map[string]string{},
	}
}

func (f *fakeTrackPlayer) AudioTracks() []mediaTrack { return f.audios }
func (f *fakeTrackPlayer) SelectAudioTrack(id int64) error {
	f.activeAudio = id
	for i := range f.audios {
		f.audios[i].Selected = f.audios[i].ID == id
	}
	return nil
}
func (f *fakeTrackPlayer) SubtitleTracks() []mediaTrack { return f.subs }
func (f *fakeTrackPlayer) SubtitleVisible() bool        { return f.subVisible }
func (f *fakeTrackPlayer) SelectSubtitleTrack(id int64) error {
	f.activeSub = id
	f.subVisible = true
	for i := range f.subs {
		f.subs[i].Selected = f.subs[i].ID == id
	}
	return nil
}
func (f *fakeTrackPlayer) DisableSubtitleTrack() error {
	f.activeSub = 0
	f.subVisible = false
	for i := range f.subs {
		f.subs[i].Selected = false
	}
	return nil
}
func (f *fakeTrackPlayer) AddSubtitleFile(path string) error {
	f.addedSubs = append(f.addedSubs, path)
	id := int64(len(f.subs) + 1)
	for i := range f.subs {
		f.subs[i].Selected = false
	}
	f.subs = append(f.subs, mediaTrack{ID: id, Title: path, External: true, Selected: true})
	f.activeSub = id
	f.subVisible = true
	return nil
}
func (f *fakeTrackPlayer) CurrentHWDec() string { return f.hwdec }
func (f *fakeTrackPlayer) SetLoop(loop bool) error {
	f.loop = loop
	return nil
}
func (f *fakeTrackPlayer) SetSubtitleDelay(d time.Duration) error {
	f.subDelay = d
	return nil
}
func (f *fakeTrackPlayer) SetSubtitleScale(scale float64) error {
	f.subScale = scale
	return nil
}
func (f *fakeTrackPlayer) SetSubtitlePos(pos int) error {
	f.subPos = pos
	return nil
}
func (f *fakeTrackPlayer) SetVideoRotate(deg int) error {
	f.videoRotate = deg
	return nil
}
func (f *fakeTrackPlayer) SetPanscan(v float64) error {
	f.panscan = v
	return nil
}
func (f *fakeTrackPlayer) SetPropertyString(name, value string) error {
	if f.props == nil {
		f.props = map[string]string{}
	}
	f.props[name] = value
	return nil
}
func (f *fakeTrackPlayer) GetPropertyString(name string) (string, error) {
	if f.props != nil {
		if v, ok := f.props[name]; ok {
			return v, nil
		}
	}
	return "", nil
}
func (f *fakeTrackPlayer) VideoTracks() []mediaTrack { return f.videos }

func TestPlayerTrackAndSubtitleControls(t *testing.T) {
	fp := newFakeTrackPlayer(t)
	a := &app{
		player:      fp,
		playingFile: true,
		playingName: "示例4K.mkv",
		pickSubtitleFile: func() ([]string, error) {
			return []string{"/tmp/custom.ass"}, nil
		},
	}
	t.Cleanup(a.close)

	tt := ui.NewTester(a.view, 1120, 760)
	if err := tt.Click("切换音轨"); err != nil {
		t.Fatalf("audio track menu button missing: %v (texts=%v)", err, tt.Texts())
	}
	if err := tt.ChooseMenuItem("原声 · English · E-AC-3 5.1"); err != nil {
		t.Fatalf("choose audio track failed: %v (menu=%v)", err, tt.Menu())
	}
	if fp.activeAudio != 2 {
		t.Fatalf("activeAudio = %d, want 2", fp.activeAudio)
	}

	if err := tt.Click("切换字幕"); err != nil {
		t.Fatalf("subtitle popover button missing: %v", err)
	}
	if err := tt.Click("字幕开关"); err != nil {
		t.Fatalf("disable subtitle switch failed: %v (texts=%v)", err, tt.Texts())
	}
	if fp.subVisible || fp.activeSub != 0 {
		t.Fatalf("subtitle not disabled: visible=%v id=%d", fp.subVisible, fp.activeSub)
	}
	if err := tt.Click("加载本地外挂字幕…"); err != nil {
		t.Fatalf("load external subtitle failed: %v (texts=%v)", err, tt.Texts())
	}
	if len(fp.addedSubs) != 1 || fp.addedSubs[0] != "/tmp/custom.ass" || !fp.subVisible {
		t.Fatalf("external subtitle not added: subs=%v visible=%v status=%q", fp.addedSubs, fp.subVisible, a.status)
	}

	// Dropping a subtitle file onto an active player attaches it without restarting video.
	if !a.handleDroppedFiles([]string{"/tmp/dropped.srt"}) {
		t.Fatalf("dropping subtitle failed: status=%q", a.status)
	}
	if len(fp.addedSubs) != 2 || fp.addedSubs[1] != "/tmp/dropped.srt" || !strings.Contains(a.status, "dropped.srt") {
		t.Fatalf("dropped subtitle mismatch: subs=%v status=%q", fp.addedSubs, a.status)
	}
}

func TestDropMKVWithCompanionSubtitlePassesSubtitlesToMPV(t *testing.T) {
	if !mpvBackendSupported() {
		t.Skip("mygo-player MKV support requires macOS or Windows amd64")
	}
	var gotTarget string
	var gotOpts mpvplayer.OpenOptions
	a := &app{
		openMPV: func(target string, opts mpvplayer.OpenOptions) (mediaPlayer, error) {
			gotTarget, gotOpts = target, opts
			return newFakeTrackPlayer(t), nil
		},
	}
	t.Cleanup(a.close)

	if !a.handleDroppedFiles([]string{"/tmp/movie.ass", "/tmp/movie.mkv"}) {
		t.Fatalf("expected MKV + subtitle drop to open: status=%q", a.status)
	}
	// The video path is cleaned for the OS; subtitles pass through as dropped.
	if gotTarget != filepath.Clean("/tmp/movie.mkv") || len(gotOpts.Subtitles) != 1 || gotOpts.Subtitles[0] != "/tmp/movie.ass" {
		t.Fatalf("openMPV args = (%q, %+v)", gotTarget, gotOpts)
	}
}

func TestFormatTrackLabels(t *testing.T) {
	audio := formatAudioTrackLabel(mediaTrack{ID: 1, Title: "国语", Lang: "zh", Codec: "truehd", Channels: 8}, 0)
	if audio != "国语 · 中文 · TrueHD 7.1" {
		t.Fatalf("audio label = %q", audio)
	}
	sub := formatSubtitleTrackLabel(mediaTrack{ID: 2, Lang: "ja", Codec: "subrip", External: true}, 1)
	if sub != "日语 · SRT · 外挂" {
		t.Fatalf("sub label = %q", sub)
	}
	forced := formatSubtitleTrackLabel(mediaTrack{ID: 3, Lang: "en-US", Codec: "hdmv_pgs_subtitle", Forced: true}, 2)
	if forced != "English · 强制 · PGS" {
		t.Fatalf("forced sub label = %q", forced)
	}
	extNoisy := formatSubtitleTrackLabel(mediaTrack{
		ID:               4,
		Title:            "zh.srt",
		Lang:             "zh-CN",
		Codec:            "subrip",
		External:         true,
		ExternalFilename: "/tmp/movie.zh.srt",
	}, 3)
	if extNoisy != "movie.zh.srt · 中文 · SRT · 外挂" {
		t.Fatalf("external noisy title label = %q", extNoisy)
	}
	extURL := formatSubtitleTrackLabel(mediaTrack{
		ID:               5,
		Title:            "ep01.ass?token=secret",
		Codec:            "ass",
		External:         true,
		ExternalFilename: "https://example.com/subs/ep01.ass?token=secret",
	}, 4)
	if extURL != "ep01.ass · ASS · 外挂" {
		t.Fatalf("external URL title label = %q", extURL)
	}
	if got := displayTrackLang("ita"); got != "Italian" {
		t.Fatalf("displayTrackLang(ita) = %q, want Italian", got)
	}
	if got := displayTrackLang("pt-BR"); got != "Portuguese" {
		t.Fatalf("displayTrackLang(pt-BR) = %q, want Portuguese", got)
	}
	if got := displayTrackLang("mul"); got != "" {
		t.Fatalf("displayTrackLang(mul) = %q, want empty", got)
	}
}
