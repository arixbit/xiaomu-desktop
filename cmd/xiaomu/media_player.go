package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	mpvplayer "github.com/arixbit/mygo-player"
	"github.com/egoist/mygo/ui"
)

// mediaPlayer is the common playback contract between Xiaomu's UI and the
// active video engine: mygo-player (libmpv zero-copy Metal on macOS;
// double-buffered ui.Bitmap + D3D11VA on Windows amd64), with automatic
// fallback to MyGo's built-in system ui.VideoPlayer on other platforms and in
// headless unit tests.
type mediaPlayer interface {
	Play()
	Pause()
	Playing() bool
	Ended() bool
	Ready() bool
	Buffering() bool
	Size() (w, h int)
	Duration() time.Duration
	Position() time.Duration
	Seek(time.Duration)
	Rate() float64
	SetRate(float64)
	Volume() float64
	SetVolume(float64)
	SetMuted(bool)
	Err() error
	Close()
	View(c *ui.Context) ui.Element
}

// mediaTrack describes one video, audio, or subtitle stream exposed by
// track-capable playback engines such as mygo-player.
type mediaTrack struct {
	ID               int64
	Title            string
	Lang             string
	Codec            string
	Width            int
	Height           int
	FPS              float64
	Channels         int
	SampleRate       int
	Selected         bool
	Default          bool
	Forced           bool
	External         bool
	ExternalFilename string
}

// trackCapablePlayer is optionally implemented by mediaPlayer backends that
// support live audio track switching, embedded/external subtitles, and hwdec
// introspection.
type trackCapablePlayer interface {
	AudioTracks() []mediaTrack
	SelectAudioTrack(id int64) error
	SubtitleTracks() []mediaTrack
	SubtitleVisible() bool
	SelectSubtitleTrack(id int64) error
	DisableSubtitleTrack() error
	AddSubtitleFile(path string) error
	CurrentHWDec() string
}

// advancedMediaPlayer is optionally implemented by mediaPlayer backends (such
// as mygo-player / libmpv) that expose loop control, typed subtitle/video
// fine-tuning, arbitrary property getters/setters, and video track metadata.
type advancedMediaPlayer interface {
	SetLoop(loop bool) error
	SetSubtitleDelay(d time.Duration) error
	SetSubtitleScale(scale float64) error
	SetSubtitlePos(pos int) error
	SetVideoRotate(deg int) error
	SetPanscan(v float64) error
	SetPropertyString(name, value string) error
	GetPropertyString(name string) (string, error)
	VideoTracks() []mediaTrack
}

func mpvBackendSupported() bool {
	return runtime.GOOS == "darwin" || (runtime.GOOS == "windows" && runtime.GOARCH == "amd64")
}

// systemMediaPlayer adapts MyGo's built-in *ui.VideoPlayer to mediaPlayer.
type systemMediaPlayer struct {
	p *ui.VideoPlayer
}

func wrapSystemPlayer(p *ui.VideoPlayer) mediaPlayer {
	if p == nil {
		return nil
	}
	return &systemMediaPlayer{p: p}
}

func (s *systemMediaPlayer) Play()                      { s.p.Play() }
func (s *systemMediaPlayer) Pause()                     { s.p.Pause() }
func (s *systemMediaPlayer) Playing() bool              { return s.p.Playing() }
func (s *systemMediaPlayer) Ended() bool                { return s.p.Ended() }
func (s *systemMediaPlayer) Ready() bool                { return s.p.Ready() }
func (s *systemMediaPlayer) Buffering() bool            { return s.p.Buffering() }
func (s *systemMediaPlayer) Size() (int, int)           { return s.p.Size() }
func (s *systemMediaPlayer) Duration() time.Duration    { return s.p.Duration() }
func (s *systemMediaPlayer) Position() time.Duration    { return s.p.Position() }
func (s *systemMediaPlayer) Seek(d time.Duration)       { s.p.Seek(d) }
func (s *systemMediaPlayer) Rate() float64              { return s.p.Rate() }
func (s *systemMediaPlayer) SetRate(r float64)          { s.p.SetRate(r) }
func (s *systemMediaPlayer) Volume() float64            { return s.p.Volume() }
func (s *systemMediaPlayer) SetVolume(v float64)        { s.p.SetVolume(v) }
func (s *systemMediaPlayer) SetMuted(m bool)            { s.p.SetMuted(m) }
func (s *systemMediaPlayer) Err() error                 { return s.p.Err() }
func (s *systemMediaPlayer) Close()                     { s.p.Close() }
func (s *systemMediaPlayer) View(c *ui.Context) ui.Element {
	return ui.Video(c, s.p)
}

// mpvMediaPlayer adapts github.com/arixbit/mygo-player (*mpvplayer.Player) to
// mediaPlayer and trackCapablePlayer.
type mpvMediaPlayer struct {
	p *mpvplayer.Player

	mu     sync.Mutex
	paused bool
	volume float64 // normalized [0, 1]
	rate   float64
	closed bool
}

func openMPVPlayer(target string, opts mpvplayer.OpenOptions) (*mpvMediaPlayer, error) {
	// Leave HWDec empty so mygo-player chooses its platform-optimal default
	// (HWDecAutoSafe / VideoToolbox on macOS, HWDecAutoCopy / D3D11VA on Windows).
	p, err := mpvplayer.New(mpvplayer.Options{
		Volume: 100,
	})
	if err != nil {
		return nil, err
	}
	if err := p.Open(target, opts); err != nil {
		_ = p.Close()
		return nil, err
	}
	return &mpvMediaPlayer{
		p:      p,
		paused: opts.Paused,
		volume: 1,
		rate:   1,
	}, nil
}

func (m *mpvMediaPlayer) Play() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.paused = false
	m.mu.Unlock()
	_ = m.p.Play()
}

func (m *mpvMediaPlayer) Pause() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.paused = true
	m.mu.Unlock()
	_ = m.p.Pause()
}

func (m *mpvMediaPlayer) Playing() bool {
	m.mu.Lock()
	closed, paused := m.closed, m.paused
	m.mu.Unlock()
	if closed || paused {
		return false
	}
	snap := m.p.Snapshot()
	return !snap.EOF && snap.Err == nil && snap.State != mpvplayer.StateError
}

func (m *mpvMediaPlayer) Ended() bool {
	snap := m.p.Snapshot()
	return snap.EOF || snap.State == mpvplayer.StateEnded
}

func (m *mpvMediaPlayer) Ready() bool {
	snap := m.p.Snapshot()
	switch snap.State {
	case mpvplayer.StatePlaying, mpvplayer.StatePaused, mpvplayer.StateEnded:
		return true
	case mpvplayer.StateBuffering:
		return snap.VideoWidth > 0 || m.p.FramesRendered() > 0
	default:
		return false
	}
}

func (m *mpvMediaPlayer) Buffering() bool {
	return m.p.Snapshot().State == mpvplayer.StateBuffering
}

func (m *mpvMediaPlayer) Size() (int, int) {
	snap := m.p.Snapshot()
	return snap.VideoWidth, snap.VideoHeight
}

func (m *mpvMediaPlayer) Duration() time.Duration {
	return m.p.Snapshot().Duration
}

func (m *mpvMediaPlayer) Position() time.Duration {
	return m.p.Snapshot().Position
}

func (m *mpvMediaPlayer) Seek(d time.Duration) {
	if d < 0 {
		d = 0
	}
	_ = m.p.Seek(d)
}

func (m *mpvMediaPlayer) Rate() float64 {
	m.mu.Lock()
	r := m.rate
	m.mu.Unlock()
	if r <= 0 {
		return 1
	}
	return r
}

func (m *mpvMediaPlayer) SetRate(r float64) {
	if r <= 0 {
		r = 1
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.rate = r
	m.mu.Unlock()
	_ = m.p.SetSpeed(r)
}

func (m *mpvMediaPlayer) Volume() float64 {
	m.mu.Lock()
	v := m.volume
	m.mu.Unlock()
	return v
}

func (m *mpvMediaPlayer) SetVolume(v float64) {
	v = min(max(v, 0), 1)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.volume = v
	m.mu.Unlock()
	_ = m.p.SetVolume(v * 100)
}

func (m *mpvMediaPlayer) SetMuted(muted bool) {
	_ = m.p.SetMuted(muted)
}

func (m *mpvMediaPlayer) Err() error {
	return m.p.Snapshot().Err
}

func (m *mpvMediaPlayer) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	_ = m.p.Close()
}

func (m *mpvMediaPlayer) View(c *ui.Context) ui.Element {
	return mpvplayer.View(c, m.p)
}

func (m *mpvMediaPlayer) AudioTracks() []mediaTrack {
	return convertMPVTracks(m.p.AudioTracks())
}

func (m *mpvMediaPlayer) SelectAudioTrack(id int64) error {
	return m.p.SelectAudioTrack(id)
}

func (m *mpvMediaPlayer) SubtitleTracks() []mediaTrack {
	return convertMPVTracks(m.p.SubtitleTracks())
}

func (m *mpvMediaPlayer) SubtitleVisible() bool {
	return m.p.Snapshot().SubVisible
}

func (m *mpvMediaPlayer) SelectSubtitleTrack(id int64) error {
	return m.p.SelectSubtitleTrack(id)
}

func (m *mpvMediaPlayer) DisableSubtitleTrack() error {
	return m.p.DisableSubtitleTrack()
}

func (m *mpvMediaPlayer) AddSubtitleFile(path string) error {
	return m.p.AddSubtitleFile(path, mpvplayer.SubtitleSelect)
}

func (m *mpvMediaPlayer) CurrentHWDec() string {
	return m.p.CurrentHWDec()
}

func (m *mpvMediaPlayer) SetLoop(loop bool) error {
	return m.p.SetLoop(loop)
}

func (m *mpvMediaPlayer) SetSubtitleDelay(d time.Duration) error {
	return m.p.SetSubtitleDelay(d)
}

func (m *mpvMediaPlayer) SetSubtitleScale(scale float64) error {
	return m.p.SetSubtitleScale(scale)
}

func (m *mpvMediaPlayer) SetSubtitlePos(pos int) error {
	return m.p.SetSubtitlePos(pos)
}

func (m *mpvMediaPlayer) SetVideoRotate(deg int) error {
	return m.p.SetVideoRotate(deg)
}

func (m *mpvMediaPlayer) SetPanscan(v float64) error {
	return m.p.SetPanscan(v)
}

func (m *mpvMediaPlayer) SetPropertyString(name, value string) error {
	return m.p.SetPropertyString(name, value)
}

func (m *mpvMediaPlayer) GetPropertyString(name string) (string, error) {
	return m.p.GetPropertyString(name)
}

func (m *mpvMediaPlayer) VideoTracks() []mediaTrack {
	return convertMPVTracks(m.p.VideoTracks())
}

var (
	_ mediaPlayer         = (*mpvMediaPlayer)(nil)
	_ trackCapablePlayer  = (*mpvMediaPlayer)(nil)
	_ advancedMediaPlayer = (*mpvMediaPlayer)(nil)
)

func convertMPVTracks(in []mpvplayer.Track) []mediaTrack {
	if len(in) == 0 {
		return nil
	}
	out := make([]mediaTrack, len(in))
	for i, tr := range in {
		out[i] = mediaTrack{
			ID:               tr.ID,
			Title:            tr.Title,
			Lang:             tr.Lang,
			Codec:            tr.Codec,
			Width:            tr.Width,
			Height:           tr.Height,
			FPS:              tr.FPS,
			Channels:         tr.Channels,
			SampleRate:       tr.SampleRate,
			Selected:         tr.Selected,
			Default:          tr.Default,
			Forced:           tr.Forced,
			External:         tr.External,
			ExternalFilename: tr.ExternalFilename,
		}
	}
	return out
}

// openMediaWithFallback tries mygo-player first on supported platforms, then
// gracefully falls back to the system player when mygo-player is unsupported
// (such as Windows before v0.2).
func openMediaWithFallback(target string, opts mpvplayer.OpenOptions, fallback func(string) (*ui.VideoPlayer, error)) (mediaPlayer, error) {
	mpv, err := openMPVPlayer(target, opts)
	if err == nil {
		return mpv, nil
	}
	if errors.Is(err, errors.ErrUnsupported) {
		vp, ferr := fallback(target)
		if ferr != nil {
			return nil, ferr
		}
		return wrapSystemPlayer(vp), nil
	}
	// If libmpv failed to load in an unpackaged environment for a container the
	// system decoder handles natively, fall back cleanly instead of blocking playback.
	if isSystemContainer(target) {
		if vp, ferr := fallback(target); ferr == nil {
			return wrapSystemPlayer(vp), nil
		}
	}
	return nil, err
}

func isSystemContainer(target string) bool {
	lower := strings.ToLower(strings.TrimSpace(target))
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	for _, ext := range []string{".mp4", ".mov", ".m4v", ".m3u8"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func trackDisplayTitle(t mediaTrack) string {
	if t.External && t.ExternalFilename != "" {
		// Reuse mygo-player's external track title cleanup so libmpv's noisy
		// suffix titles ("srt", "en.srt") and URL query credentials collapse to
		// the clean basename while preserving explicit titles.
		return (mpvplayer.Track{
			ID:               t.ID,
			Title:            t.Title,
			External:         true,
			ExternalFilename: t.ExternalFilename,
		}).DisplayName()
	}
	return strings.TrimSpace(t.Title)
}

func formatAudioTrackLabel(t mediaTrack, idx int) string {
	var parts []string
	lang := displayTrackLang(t.Lang)
	if title := trackDisplayTitle(t); title != "" && !(lang != "" && strings.EqualFold(title, t.Lang)) {
		parts = append(parts, title)
	}
	if lang != "" && (len(parts) == 0 || !strings.Contains(strings.ToLower(parts[0]), strings.ToLower(lang))) {
		parts = append(parts, lang)
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("音轨 %d", idx+1))
	}
	codec := formatTrackCodec(t.Codec)
	ch := formatTrackChannels(t.Channels)
	if codec != "" && ch != "" {
		parts = append(parts, codec+" "+ch)
	} else if codec != "" {
		parts = append(parts, codec)
	} else if ch != "" {
		parts = append(parts, ch)
	}
	return strings.Join(parts, " · ")
}

func formatSubtitleTrackLabel(t mediaTrack, idx int) string {
	var parts []string
	lang := displayTrackLang(t.Lang)
	if title := trackDisplayTitle(t); title != "" && !(lang != "" && strings.EqualFold(title, t.Lang)) {
		parts = append(parts, title)
	}
	if lang != "" && (len(parts) == 0 || !strings.Contains(strings.ToLower(parts[0]), strings.ToLower(lang))) {
		parts = append(parts, lang)
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("字幕 %d", idx+1))
	}
	if t.Forced {
		parts = append(parts, "强制")
	}
	if codec := formatTrackCodec(t.Codec); codec != "" {
		parts = append(parts, codec)
	}
	if t.External {
		parts = append(parts, "外挂")
	}
	return strings.Join(parts, " · ")
}

func displayTrackLang(code string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	switch c {
	case "", "und", "unk", "mis", "zxx", "mul":
		return ""
	case "zh", "zho", "chi", "zh-cn", "zh-hans", "chs", "sc", "cmn":
		return "中文"
	case "zh-tw", "zh-hk", "zh-hant", "cht", "tc":
		return "繁体中文"
	case "yue", "cantonese":
		return "粤语"
	}
	base := c
	if i := strings.IndexAny(base, "-_"); i > 0 {
		base = base[:i]
	}
	switch base {
	case "en", "eng":
		return "English"
	case "ja", "jpn":
		return "日语"
	case "ko", "kor":
		return "韩语"
	case "fr", "fra", "fre":
		return "法语"
	case "de", "deu", "ger":
		return "德语"
	case "es", "spa":
		return "西班牙语"
	case "ru", "rus":
		return "俄语"
	case "yue":
		return "粤语"
	case "zh", "zho", "chi", "cmn":
		return "中文"
	default:
		return mpvplayer.LanguageName(code)
	}
}

func formatTrackCodec(codec string) string {
	c := strings.ToLower(strings.TrimSpace(codec))
	switch c {
	case "":
		return ""
	case "eac3":
		return "E-AC-3"
	case "ac3":
		return "AC-3"
	case "truehd":
		return "TrueHD"
	case "dts":
		return "DTS"
	case "aac":
		return "AAC"
	case "flac":
		return "FLAC"
	case "opus":
		return "Opus"
	case "subrip":
		return "SRT"
	case "hdmv_pgs_subtitle":
		return "PGS"
	case "webvtt":
		return "VTT"
	default:
		return strings.ToUpper(c)
	}
}

func formatTrackChannels(ch int) string {
	switch ch {
	case 1:
		return "1.0"
	case 2:
		return "2.0"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		if ch > 0 {
			return fmt.Sprintf("%dch", ch)
		}
		return ""
	}
}
