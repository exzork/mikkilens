package player

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"sync"
	"testing"
	"time"
)

// The buffer between ffmpeg and the sound card.
//
// Read is called on the thread driving the device, and the rule it has to obey
// is that it never blocks: if the network stalls mid-song, a read that waited
// for bytes that are not coming would hold that thread, the device would run
// dry, and nothing would ever return to notice.

// feed puts decoded bytes into a stream the way the pump would.
func feed(stream *Stream, samples ...float32) {
	raw := make([]byte, len(samples)*4)
	for index, sample := range samples {
		binary.LittleEndian.PutUint32(raw[index*4:], math.Float32bits(sample))
	}
	stream.mu.Lock()
	stream.buffered = append(stream.buffered, raw...)
	stream.mu.Unlock()
}

func newStream(t *testing.T) *Stream {
	t.Helper()
	stream := Open(t.Context(), Tools{YtDlp: "yt-dlp", FFmpeg: "ffmpeg"}, "https://example.invalid")
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func TestReadTakesWhatHasArrived(t *testing.T) {
	stream := newStream(t)
	feed(stream, 0.25, -0.5, 0.75)

	got := make([]float32, 8)
	read, err := stream.Read(got)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read != 3 {
		t.Fatalf("read %d samples, want 3", read)
	}
	for index, want := range []float32{0.25, -0.5, 0.75} {
		if got[index] != want {
			t.Errorf("sample %d = %v, want %v", index, got[index], want)
		}
	}
}

// An empty buffer is nothing-yet, not the end. The caller fills the gap with
// silence and comes back, which is what turns a network hiccup into a pause in
// the music rather than the end of the song.
func TestAnEmptyBufferIsNotTheEnd(t *testing.T) {
	stream := newStream(t)

	got := make([]float32, 8)
	read, err := stream.Read(got)
	if read != 0 || err != nil {
		t.Fatalf("read = (%d, %v), want (0, nil)", read, err)
	}
}

// And it must never block, whatever else is true.
func TestReadNeverBlocks(t *testing.T) {
	stream := newStream(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for attempt := 0; attempt < 50; attempt++ {
			stream.Read(make([]float32, 1024))
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reading from an empty stream blocked")
	}
}

// A pipe read can end in the middle of a four byte float. A sample assembled
// from the wrong halves is a click, so the remainder waits for the rest of it.
func TestAPartialSampleWaitsForTheRestOfItself(t *testing.T) {
	stream := newStream(t)

	// Two and a half samples' worth of bytes.
	raw := make([]byte, 10)
	binary.LittleEndian.PutUint32(raw[0:], math.Float32bits(0.5))
	binary.LittleEndian.PutUint32(raw[4:], math.Float32bits(-0.25))
	stream.mu.Lock()
	stream.buffered = append(stream.buffered, raw...)
	stream.mu.Unlock()

	got := make([]float32, 8)
	read, err := stream.Read(got)
	if err != nil || read != 2 {
		t.Fatalf("read = (%d, %v), want (2, nil)", read, err)
	}
	if got[0] != 0.5 || got[1] != -0.25 {
		t.Errorf("samples = %v, %v", got[0], got[1])
	}

	stream.mu.Lock()
	left := len(stream.buffered)
	stream.mu.Unlock()
	if left != 2 {
		t.Errorf("%d bytes carried, want the 2 that did not make a sample", left)
	}
}

func TestReadEndsWhenTheDecoderHas(t *testing.T) {
	stream := newStream(t)
	feed(stream, 0.5)

	stream.mu.Lock()
	stream.ended = true
	stream.mu.Unlock()

	got := make([]float32, 4)
	if read, err := stream.Read(got); read != 1 || err != nil {
		t.Fatalf("the last sample was lost: (%d, %v)", read, err)
	}
	if read, err := stream.Read(got); read != 0 || err != io.EOF {
		t.Fatalf("read = (%d, %v), want (0, EOF)", read, err)
	}
}

// A decoder that failed has to say so rather than look like a song that
// finished, or a broken stream is indistinguishable from a short one.
func TestAFailedDecoderIsReportedNotEnded(t *testing.T) {
	stream := newStream(t)

	stream.mu.Lock()
	stream.ended = true
	stream.failed = &Error{Reason: "Server returned 403 Forbidden"}
	stream.mu.Unlock()

	if _, err := stream.Read(make([]float32, 4)); err == nil || err == io.EOF {
		t.Fatalf("err = %v, want the decoder's reason", err)
	}
}

// Stopping happens from the command handler while the render thread is inside
// Read, so closing has to be safe from anywhere and safe to repeat.
func TestCloseIsSafeToRepeatAndConcurrent(t *testing.T) {
	stream := newStream(t)
	feed(stream, 0.1, 0.2)

	var group sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_ = stream.Close()
		}()
	}
	group.Wait()

	if read, err := stream.Read(make([]float32, 4)); read != 0 || err != io.EOF {
		t.Errorf("a closed stream read (%d, %v), want (0, EOF)", read, err)
	}
}

func TestToolsAreOnlyReadyWithBoth(t *testing.T) {
	for _, test := range []struct {
		tools Tools
		ready bool
	}{
		{Tools{YtDlp: "a", FFmpeg: "b"}, true},
		{Tools{YtDlp: "a"}, false},
		{Tools{FFmpeg: "b"}, false},
		{Tools{}, false},
	} {
		if got := test.tools.Ready(); got != test.ready {
			t.Errorf("%+v ready = %v, want %v", test.tools, got, test.ready)
		}
	}
}

func TestResolveWithoutTheDownloaderSaysSo(t *testing.T) {
	if _, err := Resolve(context.Background(), Tools{}, "https://example.invalid"); err == nil {
		t.Fatal("resolving with no yt-dlp was accepted")
	}
}

func TestFormatWithoutTheDecoderSaysSo(t *testing.T) {
	stream := Open(t.Context(), Tools{YtDlp: "yt-dlp"}, "https://example.invalid")
	defer stream.Close()

	if err := stream.Format(48000, 2); err == nil {
		t.Fatal("starting with no ffmpeg was accepted")
	}
}

// -- against the real thing ---------------------------------------------------

// The whole path, behind MIKKILENS_LIVE=1 like the other live tests: resolve a
// real song and decode the first seconds of it.
//
// Point it at the two programs with MIKKILENS_YTDLP and MIKKILENS_FFMPEG when
// they are not on the PATH.
func TestPlayingARealSong(t *testing.T) {
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to play a real song")
	}
	tools := Tools{
		YtDlp:  orPath(t, os.Getenv("MIKKILENS_YTDLP"), "yt-dlp"),
		FFmpeg: orPath(t, os.Getenv("MIKKILENS_FFMPEG"), "ffmpeg"),
	}

	start := time.Now()
	url, err := Resolve(t.Context(), tools, "https://music.youtube.com/watch?v=1RrF6Ee_io0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	t.Logf("resolved in %v", time.Since(start).Round(time.Millisecond))

	stream := Open(t.Context(), tools, url)
	defer stream.Close()

	start = time.Now()
	if err := stream.Format(48000, 2); err != nil {
		t.Fatalf("format: %v", err)
	}
	t.Logf("first audio in %v", time.Since(start).Round(time.Millisecond))

	// Two seconds of stereo, read the way the device reads it.
	const wanted = 48000 * 2 * 2
	got, loud := 0, false
	block := make([]float32, 4800)
	for deadline := time.Now().Add(20 * time.Second); got < wanted && time.Now().Before(deadline); {
		read, err := stream.Read(block)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		for _, sample := range block[:read] {
			if sample > 0.01 || sample < -0.01 {
				loud = true
			}
			if sample > 1.5 || sample < -1.5 {
				t.Fatalf("sample %v is out of range -- the byte order is wrong", sample)
			}
		}
		got += read
		if read == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	if got < wanted {
		t.Fatalf("decoded %d samples in 20 seconds, want %d", got, wanted)
	}
	if !loud {
		t.Error("two seconds of decoded audio is all silence")
	}
	t.Logf("decoded %d samples", got)
}

func orPath(t *testing.T, configured, fallback string) string {
	t.Helper()
	if configured != "" {
		return configured
	}
	return fallback
}
