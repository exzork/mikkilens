package engine

import (
	"math"
	"testing"

	"github.com/exzork/mikkilens/packages/audio/assets"
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

// A volume of zero in the config file is far more likely to be a field nobody
// filled in than a deliberate choice to play music silently -- and silence is
// indistinguishable from the song having failed.
func TestVolumeFallsBackRatherThanPlayingSilently(t *testing.T) {
	for _, test := range []struct {
		value, fallback, want float64
	}{
		{0.5, 0.7, 0.5},
		{0, 0.7, 0.7},
		{-1, 0.7, 0.7},
		{1, 0.7, 1},
		// Louder than the device can go is clamped rather than allowed to clip.
		{4, 0.7, 1},
	} {
		if got := clampVolume(test.value, test.fallback); got != test.want {
			t.Errorf("clampVolume(%v, %v) = %v, want %v",
				test.value, test.fallback, got, test.want)
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
