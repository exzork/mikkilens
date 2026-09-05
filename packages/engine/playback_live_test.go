package engine

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/assets"
	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/controllers/player"
)

// The whole playback path, on real hardware, with real sound.
//
// Everything else about playback is tested without a speaker: the buffer, the
// sample decoding, the gain arithmetic. None of that can tell whether audio
// actually reaches a device -- the streaming render talks to WASAPI, and WASAPI
// is only honest when it is running.
//
// So this one is listened to rather than asserted. It narrates what it is
// doing, and each stage is something audible: the song starts, it drops while
// "MikkiLens is speaking", it comes back, it pauses on a note and resumes on
// the same one, and it stops immediately rather than playing out a buffer.
//
//	go test ./packages/engine -run TestPlaybackOutLoud -v
//
// with MIKKILENS_LIVE=1 set.
func TestPlaybackOutLoud(t *testing.T) {
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to actually play a song")
	}
	if os.Getenv("MIKKILENS_SILENT") == "1" {
		t.Skip("MIKKILENS_SILENT is set, so nothing would come out")
	}

	tools := livePlayerTools(t)

	// A song rather than a tone, because a tone would prove the device opened
	// and nothing about whether the decoding is right. Anything wrong with the
	// sample format is instantly obvious as noise.
	const song = "https://music.youtube.com/watch?v=1RrF6Ee_io0"

	t.Log("resolving...")
	started := time.Now()
	url, err := player.Resolve(t.Context(), tools, song)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	t.Logf("resolved in %v", time.Since(started).Round(time.Millisecond))

	stream := player.Open(t.Context(), tools, url)
	defer stream.Close()

	sound := newPlayback(0.7, 0.25)

	// The script, run alongside the render. Each step is something to listen
	// for rather than something to assert.
	go func() {
		steps := []struct {
			wait time.Duration
			say  string
			do   func()
		}{
			{4 * time.Second, "ducking, as if MikkiLens started speaking", func() {
				sound.ducked.Store(true)
			}},
			{3 * time.Second, "lifting the duck", func() { sound.ducked.Store(false) }},
			{3 * time.Second, "pausing", func() { sound.paused.Store(true) }},
			{3 * time.Second, "resuming on the same note", func() { sound.paused.Store(false) }},
			{4 * time.Second, "stopping", func() {
				sound.stopped.Store(true)
				_ = stream.Close()
			}},
		}
		for _, step := range steps {
			time.Sleep(step.wait)
			t.Log(step.say)
			step.do()
		}
	}()

	t.Log("playing on the default output device -- listen")
	started = time.Now()
	if err := devices.Stream(nil, stream, 48000, 2, sound); err != nil {
		if !sound.Stopped() {
			t.Fatalf("stream: %v", err)
		}
	}
	played := time.Since(started)
	t.Logf("the device ran for %v", played.Round(time.Millisecond))

	// The script above adds up to seventeen seconds. Finishing far short of
	// that means the device gave up rather than played, which is the failure
	// this test exists to catch -- and it is not something a listener can tell
	// apart from a song that was simply short.
	if played < 15*time.Second {
		t.Errorf("playback ended after %v, well before the script finished", played)
	}
}

// livePlayerTools finds the two programs, fetching what is missing exactly as
// the first song on a new machine would.
func livePlayerTools(t *testing.T) player.Tools {
	t.Helper()

	found := assets.FindPlayerTools(os.Getenv("MIKKILENS_YTDLP"), os.Getenv("MIKKILENS_FFMPEG"))
	if found.Ready() {
		t.Logf("using yt-dlp %s and ffmpeg %s", found.YtDlp, found.FFmpeg)
		return player.Tools{YtDlp: found.YtDlp, FFmpeg: found.FFmpeg}
	}

	wanted := assets.MissingPlayer(os.Getenv("MIKKILENS_YTDLP"), os.Getenv("MIKKILENS_FFMPEG"))
	if wanted.Empty() {
		t.Fatalf("neither program is installed and there is nothing to fetch: %+v", found)
	}
	t.Logf("fetching %v, about %d MB", wanted.Stages, wanted.Bytes/(1<<20))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	done := make(chan string, 1)
	remaining := len(wanted.Stages)
	installer := assets.NewInstaller()
	err := installer.Install(ctx, wanted, "small",
		func(progress assets.Progress) {
			switch {
			case progress.Failed != "":
				select {
				case done <- progress.Failed:
				default:
				}
			case progress.Done:
				t.Logf("fetched %s", progress.Stage)
				remaining--
				if remaining <= 0 {
					select {
					case done <- "":
					default:
					}
				}
			}
		}, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	select {
	case reason := <-done:
		if reason != "" {
			t.Fatalf("install: %s", reason)
		}
	case <-ctx.Done():
		t.Fatal("the download did not finish")
	}

	found = assets.FindPlayerTools(os.Getenv("MIKKILENS_YTDLP"), os.Getenv("MIKKILENS_FFMPEG"))
	if !found.Ready() {
		t.Fatalf("the download finished but the programs are not there: %+v", found)
	}
	t.Logf("using yt-dlp %s and ffmpeg %s", found.YtDlp, found.FFmpeg)
	return player.Tools{YtDlp: found.YtDlp, FFmpeg: found.FFmpeg}
}
