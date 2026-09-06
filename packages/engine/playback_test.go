package engine

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/exzork/mikkilens/packages/audio/assets"
	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/controllers/music"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// Getting out of the way of the voice.
//
// The ducking is the reason playing a song here beats handing it to a browser:
// a browser cannot know that the chat is being read over it, so the two talk at
// once and neither can be followed. Gain is read on the thread driving the
// sound card, once per block, which is what makes a duck land in milliseconds
// rather than at the end of whatever was already buffered.

func newPlayback(volume, duck float64) *playback {
	sound := &playback{}
	sound.volume.Store(math.Float64bits(volume))
	sound.duck.Store(math.Float64bits(duck))
	return sound
}

func TestDuckingDropsTheMusicAndLiftsItAgain(t *testing.T) {
	sound := newPlayback(0.7, 0.25)

	if gain := sound.Gain(); gain != 0.7 {
		t.Fatalf("gain = %v, want 0.7", gain)
	}

	sound.ducked.Store(true)
	if gain := sound.Gain(); gain != 0.25 {
		t.Errorf("ducked gain = %v, want 0.25", gain)
	}

	sound.ducked.Store(false)
	if gain := sound.Gain(); gain != 0.7 {
		t.Errorf("gain after the duck = %v, want 0.7", gain)
	}
}

// Ducked means quieter, never silent. A song that vanishes entirely whenever a
// chat message is read is its own kind of confusing on a stream, and it sounds
// exactly like the music having stopped.
func TestDuckingNeverSilencesTheMusic(t *testing.T) {
	sound := newPlayback(0.7, 0.25)
	sound.ducked.Store(true)

	if gain := sound.Gain(); gain <= 0 {
		t.Errorf("ducked gain = %v, which is silence", gain)
	}
}

func TestStoppedAndPausedAreReadBack(t *testing.T) {
	sound := newPlayback(0.7, 0.25)

	if sound.Stopped() || sound.Paused() {
		t.Fatal("a fresh playback reports itself stopped or paused")
	}
	sound.paused.Store(true)
	if !sound.Paused() || sound.Stopped() {
		t.Error("pausing was not read back, or stopped it as well")
	}
	sound.stopped.Store(true)
	if !sound.Stopped() {
		t.Error("stopping was not read back")
	}
}

// The music volume is a percentage of the song's own level, like every other
// volume in the config. Nothing outside nought to a hundred can be played:
// above full would clip, and below nought is not a sound.
func TestTheMusicGainIsThePercentageOfFull(t *testing.T) {
	for _, test := range []struct {
		percent int
		want    float64
	}{
		{70, 0.7},
		{25, 0.25},
		{100, 1},
		{0, 0},
		{-10, 0},
		{400, 1},
	} {
		if got := gain(test.percent); got != test.want {
			t.Errorf("gain(%d) = %v, want %v", test.percent, got, test.want)
		}
	}
}

// -- getting out of the way of a list -----------------------------------------

// Reading a list holds the song rather than ducking under it.
//
// Ducking is right for a sentence and wrong for a list: the reading is half a
// dozen utterances with a synthesis round trip between them, so the music comes
// back up in every gap and swells under the numbers she is trying to hold in
// her head.
func TestReadingAListHoldsTheSongAndGivesItBack(t *testing.T) {
	engine := playingSomething()

	engine.pauseForList()
	if !engine.playing.Paused() {
		t.Fatal("the song kept playing under the list being read")
	}

	engine.resumeAfterList()
	if engine.playing.Paused() {
		t.Error("the song was not given back when the reading ended")
	}
}

// A pause she asked for is hers. The reading must not treat it as its own and
// hand the song back at the end of a list she was not listening to anyway.
func TestAPauseSheAskedForSurvivesTheReading(t *testing.T) {
	engine := playingSomething()
	engine.playing.paused.Store(true)

	engine.pauseForList()
	engine.resumeAfterList()

	if !engine.playing.Paused() {
		t.Error("the reading gave back a song she had paused herself")
	}
}

// The other way round: she says "jeda lagunya" while the list is being read.
// The song is already paused, so the only thing that answer can mean is that
// the pause should outlast the reading.
func TestPausingDuringAReadingKeepsTheSongPaused(t *testing.T) {
	t.Setenv("MIKKILENS_SILENT", "1")

	engine := playingSomething()
	engine.locale = i18n.Load("id")
	engine.bus = feedback.NewWith(config.Default(), engine.locale, silentSpeaker{}, silentVoice)
	t.Cleanup(engine.bus.Stop)

	engine.pauseForList()
	engine.PauseMusic()
	engine.resumeAfterList()

	if !engine.playing.Paused() {
		t.Error("the song came back after she asked for it to stay paused")
	}
}

// Nothing playing, nothing to hold: the reading must not leave a pause behind
// on a song that starts afterwards.
func TestHoldingWithNothingPlayingIsHarmless(t *testing.T) {
	engine := &Engine{}

	engine.pauseForList()
	engine.resumeAfterList()

	if engine.playing.Paused() {
		t.Error("a reading with no song playing left a pause behind")
	}
}

func playingSomething() *Engine {
	engine := &Engine{}
	engine.playing.song = music.Song{Title: "Monokrom", VideoID: "1RrF6Ee_io0"}
	engine.playing.live = true
	return engine
}

type silentSpeaker struct{}

func (silentSpeaker) Play(tts.Audio) (bool, error) { return true, nil }
func (silentSpeaker) Stop()                        {}
func (silentSpeaker) SetDevice(*devices.Device)    {}

func silentVoice(_ context.Context, text string, _ tts.Options) (tts.Audio, error) {
	return tts.Audio{Samples: make([]float32, 8), SampleRate: 48000, Channels: 1, Text: text}, nil
}

// She starts talking while the list is being read: pressing the key, or saying
// MikkiLens's name. Whatever she is about to say, the list is over -- and it
// must not be left holding the song that was playing before it.
func TestStartingToTalkStopsTheListAndGivesTheSongBack(t *testing.T) {
	t.Setenv("MIKKILENS_SILENT", "1")

	engine := playingSomething()
	engine.locale = i18n.Load("id")
	engine.bus = feedback.NewWith(config.Default(), engine.locale, silentSpeaker{}, silentVoice)
	t.Cleanup(engine.bus.Stop)

	engine.pauseForList()
	for _, line := range []string{"Ada 2 lagu.", "1. Monokrom.", "2. Sewindu."} {
		engine.say(line)
	}

	engine.stopReadingTheList()

	if engine.bus.PendingGroup(resultsGroup) {
		t.Error("the rest of the list is still queued")
	}
	if engine.playing.Paused() {
		t.Error("the song was left held by a reading that is over")
	}
}

// Nothing being read, nothing to stop: starting a turn at any other moment
// must not go near the song.
func TestStartingToTalkWithNoListReadingLeavesTheSongAlone(t *testing.T) {
	t.Setenv("MIKKILENS_SILENT", "1")

	engine := playingSomething()
	engine.locale = i18n.Load("id")
	engine.bus = feedback.NewWith(config.Default(), engine.locale, silentSpeaker{}, silentVoice)
	t.Cleanup(engine.bus.Stop)

	engine.playing.paused.Store(true) // she paused it herself, a while ago

	engine.stopReadingTheList()

	if !engine.playing.Paused() {
		t.Error("a turn with no list being read gave back a pause she asked for")
	}
}

// -- restarting ---------------------------------------------------------------

// Restarting has its own sentence: saying "playing X by Y" again would be
// answering a question she did not ask, and would sound exactly like the
// command having been misheard as "play it".
func TestBothLanguagesSayWhatRestartingIs(t *testing.T) {
	for _, language := range []string{"id", "en"} {
		locale := i18n.Load(language)
		line := locale.T("music.restarted", i18n.Args{"title": "Monokrom", "artist": "Tulus"})
		if strings.Contains(line, "Missing text") || !strings.Contains(line, "Monokrom") {
			t.Errorf("[%s] music.restarted = %q", language, line)
		}
	}
}

// A command that exists in code and in no command file is a command she cannot
// say. Both shipped files have to carry all of these.
func TestBothCommandFilesCarryTheMusicControls(t *testing.T) {
	handlers := musicPlaybackHandlers(&Engine{})
	for _, path := range []string{"../../commands.id.toml", "../../commands.en.toml"} {
		set, err := intent.SetFromFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for id := range handlers {
			if !set.Has(id) {
				t.Errorf("%s has no %s command", path, id)
			}
		}
	}
}

// -- the two programs ---------------------------------------------------------

// A configured path that is not there has to come back empty rather than
// falling through to a different copy: someone who wrote a path there had a
// reason, and quietly running a different program than the one asked for is
// the kind of thing that is impossible to work out by ear.
func TestAConfiguredToolPathThatIsMissingIsNotSubstituted(t *testing.T) {
	found := assets.FindPlayerTools(
		"D:/nowhere/yt-dlp.exe", "D:/nowhere/ffmpeg.exe")

	if found.YtDlp != "" || found.FFmpeg != "" {
		t.Errorf("a missing configured path resolved to %+v", found)
	}
	if found.Ready() {
		t.Error("a pair of missing programs reported itself ready")
	}
}

func TestPlayerToolsAreOnlyReadyWithBoth(t *testing.T) {
	if (assets.PlayerTools{YtDlp: "a"}).Ready() {
		t.Error("yt-dlp alone reported ready")
	}
	if (assets.PlayerTools{FFmpeg: "b"}).Ready() {
		t.Error("ffmpeg alone reported ready")
	}
	if !(assets.PlayerTools{YtDlp: "a", FFmpeg: "b"}).Ready() {
		t.Error("both together did not report ready")
	}
}

// Whatever is already on the machine wins, so a streaming box that already
// runs ffmpeg never downloads a second copy of it.
func TestNothingIsFetchedForToolsThatAreAlreadyThere(t *testing.T) {
	wanted := assets.MissingPlayer("", "")

	for _, stage := range wanted.Stages {
		if stage != assets.StagePlayer && stage != assets.StageFFmpeg {
			t.Errorf("unexpected stage %q", stage)
		}
	}
	// Whatever this machine has, the two of them are the only things that can
	// ever be wanted, and the size has to add up to something sayable.
	if wanted.Bytes < 0 {
		t.Errorf("a negative download size: %d", wanted.Bytes)
	}
}

// -- the commands -------------------------------------------------------------

// Controlling the song is the part a browser could never do, so it has to be
// sayable in both languages or it does not exist.
func TestBothCommandFilesCanControlTheMusic(t *testing.T) {
	for _, path := range []string{"../../commands.id.toml", "../../commands.en.toml"} {
		set, err := intent.SetFromFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, id := range []string{"stop_music", "pause_music", "resume_music", "now_playing"} {
			if !set.Has(id) {
				t.Errorf("%s has no %s command", path, id)
			}
		}
	}
}

// Stopping the music and stopping the stream are different enough that being
// misheard between them cannot happen. One is a song; the other is her
// livelihood going off the air.
func TestStoppingMusicIsNotMistakenForStoppingTheStream(t *testing.T) {
	for path, said := range map[string]map[string]string{
		"../../commands.id.toml": {
			"hentikan lagunya": "stop_music",
			"hentikan siaran":  "stop_stream",
			"jeda lagunya":     "pause_music",
			"jeda chat":        "chat_pause",
		},
		"../../commands.en.toml": {
			"stop the music":  "stop_music",
			"stop the stream": "stop_stream",
			"pause the music": "pause_music",
			"pause the chat":  "chat_pause",
		},
	} {
		set, err := intent.SetFromFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for phrase, want := range said {
			match, rivals := set.Match(phrase)
			if match == nil {
				t.Errorf("%s: %q matched nothing (%d rivals)", path, phrase, len(rivals))
				continue
			}
			if match.Command != want {
				t.Errorf("%s: %q -> %s, want %s", path, phrase, match.Command, want)
			}
		}
	}
}
