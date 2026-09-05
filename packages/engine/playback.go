package engine

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/exzork/mikkilens/packages/audio/assets"
	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/controllers/music"
	"github.com/exzork/mikkilens/packages/controllers/player"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/state"
)

// Playing a song inside MikkiLens rather than handing it to a browser.
//
// The browser was the honest first answer -- she has an account and a player
// there already -- and it was wrong for the machine this runs on. A browser
// window is another thing on a screen she does not look at, another thing to
// find and close, and another thing OBS may or may not be capturing. Worse, it
// is not controllable: "stop the music" has nowhere to go, and the song plays
// over the chat being read because neither knows about the other.
//
// Played here, all of that is one problem instead of several. The song goes to
// a device she chooses, it stops when she says so, and it steps back on its own
// whenever MikkiLens speaks -- which is the part a browser could never do.

// duckRelease is how long the music stays quiet after the voice stops.
//
// Speech arrives as separate utterances with gaps between them, and lifting the
// duck in every gap would make the music surge between sentences -- which is
// more distracting than the ducking was ever worth. Held slightly past the end
// of each one, so a run of chat messages reads over one continuous dip.
const duckRelease = 600 * time.Millisecond

// playback is the song currently playing, and the controls over it.
//
// The three switches are atomics rather than mutex-guarded because they are
// read on the thread driving the sound card, once per audio block, while the
// command handler that writes them runs somewhere else entirely.
type playback struct {
	mu     sync.Mutex
	stream *player.Stream
	song   music.Song
	live   bool

	stopped atomic.Bool
	paused  atomic.Bool

	// heldForList is a pause that reading a list of songs took, rather than
	// one she asked for. It is what says the song is owed back when the
	// reading ends -- and what makes "jeda lagunya" during a reading mean
	// something, by turning that pause into hers.
	heldForList atomic.Bool

	// volume and ducked are the two halves of how loud the music is.
	volume atomic.Uint64 // float64 bits: what she set
	duck   atomic.Uint64 // float64 bits: what it drops to while MikkiLens talks
	ducked atomic.Bool
}

func (p *playback) Stopped() bool { return p.stopped.Load() }
func (p *playback) Paused() bool  { return p.paused.Load() }

// Gain is read once per block, so a duck takes effect in milliseconds rather
// than at the end of whatever was already buffered.
func (p *playback) Gain() float32 {
	bits := p.volume.Load()
	if p.ducked.Load() {
		bits = p.duck.Load()
	}
	return float32(math.Float64frombits(bits))
}

func musicPlaybackHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"stop_music":   func(map[string]string) error { e.StopMusic(); return nil },
		"pause_music":  func(map[string]string) error { e.PauseMusic(); return nil },
		"resume_music": func(map[string]string) error { e.ResumeMusic(); return nil },
		"now_playing":  func(map[string]string) error { e.SayNowPlaying(); return nil },
	}
}

// -- what is playing ----------------------------------------------------------

// NowPlaying is the song currently playing, and whether there is one.
func (e *Engine) NowPlaying() (music.Song, bool) {
	e.playing.mu.Lock()
	defer e.playing.mu.Unlock()
	return e.playing.song, e.playing.live
}

// SayNowPlaying answers "what is playing".
func (e *Engine) SayNowPlaying() {
	song, playing := e.NowPlaying()
	if !playing {
		e.bus.SayKey("music.nothing_playing", feedback.Result)
		return
	}
	if e.playing.paused.Load() {
		e.bus.SayKey("music.paused_is", feedback.Result,
			i18n.Args{"title": song.Title, "artist": song.Artist})
		return
	}
	e.bus.SayKey("music.playing_is", feedback.Result,
		i18n.Args{"title": song.Title, "artist": song.Artist})
}

// -- starting -----------------------------------------------------------------

// startSong plays one song, replacing whatever was playing.
//
// It returns as soon as the song is under way rather than when it finishes: the
// caller is a command handler, and holding one open for four minutes would hold
// the microphone with it.
func (e *Engine) startSong(song music.Song) {
	// Whatever was playing goes first, and silently: she asked for this song,
	// not for a sentence about the last one.
	e.stopSong(false)

	settings := e.Config()
	e.playing.stopped.Store(false)
	e.playing.paused.Store(false)
	e.playing.heldForList.Store(false)
	e.playing.ducked.Store(false)
	e.applyMusicVolume(settings)

	e.playing.mu.Lock()
	e.playing.song, e.playing.live = song, true
	e.playing.mu.Unlock()

	e.store.Update(state.Changes{"now_playing": song.Title + " — " + song.Artist})
	e.bus.SayKey("music.playing", feedback.Result,
		i18n.Args{"title": song.Title, "artist": song.Artist})

	go e.playSong(song)
}

func (e *Engine) playSong(song music.Song) {
	defer func() {
		if problem := recover(); problem != nil {
			slog.Error("playback panicked", "song", song.Title, "panic", problem)
		}
		e.finishSong()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tools, ok := e.playerTools(ctx)
	if !ok {
		return
	}

	url, err := player.Resolve(ctx, tools, song.URL())
	if err != nil {
		slog.Warn("could not resolve a song", "song", song.Title, "error", err)
		e.bus.SayKey("music.play_failed", feedback.Error, i18n.Args{"title": song.Title})
		return
	}
	// Stopped while it was being resolved, which is three seconds she may well
	// have changed her mind in.
	if e.playing.stopped.Load() {
		return
	}

	stream := player.Open(ctx, tools, url)
	e.playing.mu.Lock()
	e.playing.stream = stream
	e.playing.mu.Unlock()
	defer stream.Close()

	// The device is asked for its own rate, and ffmpeg is told to produce
	// exactly that -- so nothing here resamples, and the music arrives the
	// shape the sound card wanted.
	device := e.musicDevice()
	if err := devices.Stream(device, stream, 48000, 2, &e.playing); err != nil {
		if e.playing.stopped.Load() {
			return // stopping is a decision, not a fault
		}
		slog.Warn("playback failed", "song", song.Title, "error", err)
		e.bus.SayKey("music.play_failed", feedback.Error, i18n.Args{"title": song.Title})
	}
}

// finishSong puts the state back, however the song ended.
func (e *Engine) finishSong() {
	e.playing.mu.Lock()
	e.playing.stream, e.playing.live = nil, false
	e.playing.mu.Unlock()

	e.playing.paused.Store(false)
	e.playing.heldForList.Store(false)
	e.playing.ducked.Store(false)
	e.store.Update(state.Changes{"now_playing": ""})
}

// musicDevice is where the song comes out.
//
// Its own setting, defaulting to wherever the voice goes. On a streaming
// machine the two often want to be different: her voice in her headphones, the
// music into whatever OBS is capturing, so the audience hears the song and not
// the chat being read to her.
func (e *Engine) musicDevice() *devices.Device {
	settings := e.Config()

	wanted := settings.Music.OutputDevice
	if wanted == "" {
		wanted = settings.Speech.OutputDevice
	}
	device, err := devices.Resolve(wanted, devices.Output)
	if err != nil {
		slog.Warn("no music output device; using the default", "wanted", wanted, "error", err)
		return nil
	}
	return device
}

// -- stopping and pausing -----------------------------------------------------

// StopMusic ends the song, and says so unless it was replaced by another.
func (e *Engine) StopMusic() { e.stopSong(true) }

func (e *Engine) stopSong(announce bool) {
	e.playing.mu.Lock()
	stream, live := e.playing.stream, e.playing.live
	e.playing.mu.Unlock()

	if !live {
		if announce {
			e.bus.SayKey("music.nothing_playing", feedback.Result)
		}
		return
	}

	e.playing.stopped.Store(true)
	e.playing.paused.Store(false)
	e.playing.heldForList.Store(false)
	if stream != nil {
		// Closing is what unblocks the render: it ends the decoder, the source
		// reports the end, and the device stops within a block.
		_ = stream.Close()
	}
	if announce {
		e.bus.SayKey("music.stopped", feedback.Result)
	}
}

// PauseMusic holds the song where it is. The stream stays open, so resuming
// picks up on the same note rather than starting the song again.
func (e *Engine) PauseMusic() {
	if _, live := e.NowPlaying(); !live {
		e.bus.SayKey("music.nothing_playing", feedback.Result)
		return
	}
	if e.playing.paused.Load() {
		// A song held for a list being read is, to her, still playing: it
		// comes back on its own the moment the reading ends. So this is a
		// pause she is asking for, and taking it from the reading is what
		// makes it stay.
		if !e.playing.heldForList.Swap(false) {
			e.bus.SayKey("music.already_paused", feedback.Result)
			return
		}
		e.bus.SayKey("music.paused", feedback.Result)
		return
	}
	e.playing.paused.Store(true)
	e.bus.SayKey("music.paused", feedback.Result)
}

// pauseForList holds the song for as long as a list of results is being read.
//
// Ducking is the right answer for a sentence and the wrong one for a list. The
// reading is half a dozen utterances with a synthesis round trip between them,
// so the music comes back up in every gap and swells under the numbers she is
// trying to hold in her head -- and she is choosing a song, which means the one
// playing is the thing she is about to replace.
//
// Silent, and it leaves a pause she asked for alone: this is not a decision,
// it is the reading getting out of its own way.
func (e *Engine) pauseForList() {
	if _, live := e.NowPlaying(); !live {
		return
	}
	if e.playing.paused.Load() {
		return
	}
	e.playing.paused.Store(true)
	e.playing.heldForList.Store(true)
}

// resumeAfterList gives the song back, if reading a list is what took it.
func (e *Engine) resumeAfterList() {
	if !e.playing.heldForList.Swap(false) {
		return
	}
	if _, live := e.NowPlaying(); !live {
		return
	}
	e.playing.paused.Store(false)
}

// ResumeMusic starts it again from where it stopped.
func (e *Engine) ResumeMusic() {
	if _, live := e.NowPlaying(); !live {
		e.bus.SayKey("music.nothing_playing", feedback.Result)
		return
	}
	if !e.playing.paused.Load() {
		e.bus.SayKey("music.already_playing", feedback.Result)
		return
	}
	e.playing.heldForList.Store(false)
	e.playing.paused.Store(false)
	e.bus.SayKey("music.resumed", feedback.Result)
}

// -- getting out of the way ---------------------------------------------------

// duckMusic drops the music while MikkiLens is talking, and lifts it after.
//
// Called from the speech bus on every utterance. The lift is delayed because
// speech arrives as separate utterances with gaps between them, and coming
// back up in every gap would make the music surge between sentences.
func (e *Engine) duckMusic(speaking bool) {
	if speaking {
		e.duckTimer.Stop()
		e.playing.ducked.Store(true)
		return
	}
	e.duckTimer.Reset(duckRelease)
}

// -- the two programs ---------------------------------------------------------

// playerTools finds yt-dlp and ffmpeg, fetching them if this is the first song.
//
// Everything about the wait is said out loud, because the first song on a new
// machine is the one time this takes minutes rather than seconds, and silence
// with no music in it is indistinguishable from a command that did not work.
func (e *Engine) playerTools(ctx context.Context) (player.Tools, bool) {
	settings := e.Config()

	found := assets.FindPlayerTools(settings.Music.YtDlpPath, settings.Music.FFmpegPath)
	if found.Ready() {
		return player.Tools{YtDlp: found.YtDlp, FFmpeg: found.FFmpeg}, true
	}

	wanted := assets.MissingPlayer(settings.Music.YtDlpPath, settings.Music.FFmpegPath)
	if wanted.Empty() {
		// Both missing and nothing to fetch means a configured path that is
		// not there, which no amount of downloading will fix.
		e.bus.SayKey("music.no_tools", feedback.Error)
		return player.Tools{}, false
	}

	if e.installer.Running() {
		// The speech model is still coming down. Two large downloads at once
		// would make both slow and neither finish.
		e.bus.SayKey("music.tools_busy", feedback.Result)
		return player.Tools{}, false
	}

	e.bus.SayKey("music.installing", feedback.Result,
		i18n.Args{"size": megabytes(wanted.Bytes)})

	if !e.fetchPlayerTools(ctx, wanted) {
		return player.Tools{}, false
	}

	found = assets.FindPlayerTools(settings.Music.YtDlpPath, settings.Music.FFmpegPath)
	if !found.Ready() {
		e.bus.SayKey("music.install_failed", feedback.Error,
			i18n.Args{"reason": "the download finished but the files are not there"})
		return player.Tools{}, false
	}
	e.bus.SayKey("music.installed", feedback.Result)
	return player.Tools{YtDlp: found.YtDlp, FFmpeg: found.FFmpeg}, true
}

// fetchPlayerTools runs the download and waits for it, reporting failure aloud.
func (e *Engine) fetchPlayerTools(ctx context.Context, wanted assets.Wanted) bool {
	done := make(chan string, 1)
	finish := func(reason string) {
		select {
		case done <- reason:
		default:
		}
	}

	remaining := len(wanted.Stages)
	err := e.installer.Install(ctx, wanted, e.Config().STT.ModelSize,
		func(progress assets.Progress) {
			switch {
			case progress.Failed != "":
				finish(progress.Failed)
			case progress.Done:
				remaining--
				if remaining <= 0 {
					finish("")
				}
			}
		}, nil)
	if err != nil {
		e.bus.SayKey("music.install_failed", feedback.Error,
			i18n.Args{"reason": err.Error()})
		return false
	}

	select {
	case reason := <-done:
		if reason != "" {
			e.bus.SayKey("music.install_failed", feedback.Error, i18n.Args{"reason": reason})
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

// applyMusicVolume sets how loud the song is, and what it drops to under the
// voice.
//
// Called when a song starts and again whenever either is changed, which is
// what lets turning the music down land on the song already playing rather
// than on the next one.
func (e *Engine) applyMusicVolume(settings config.Config) {
	e.playing.volume.Store(math.Float64bits(gain(settings.Music.Volume)))
	e.playing.duck.Store(math.Float64bits(gain(settings.Music.DuckVolume)))
}

// gain turns a 0-to-100 volume into the multiplier every sample is scaled by.
func gain(percent int) float64 { return float64(config.ClampPercent(percent)) / 100 }
