// Package player turns a YouTube Music page into sound coming out of a
// speaker, without a browser and without a file on disk.
//
// Two programs do the parts that are genuinely hard. yt-dlp works out which of
// the stream URLs behind a video is the audio one and unpicks the signature
// protecting it -- a moving target that breaks every few months and is the
// reason this is not reimplemented here. ffmpeg reads that URL over HTTP and
// decodes it, because the sound card wants PCM and YouTube serves AAC.
//
// Nothing is downloaded. yt-dlp only resolves; ffmpeg streams the audio in and
// writes decoded samples out as they arrive, and [Stream] hands them to the
// device a block at a time. So the song starts a few seconds after she picks
// it rather than after a four minute file has been fetched, and the memory it
// costs is one buffer rather than the whole track -- which matters on a
// machine that is also encoding video.
package player

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Error is a playback failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Tools is where the two programs are. Both are absolute paths: this never
// searches the PATH itself, because what is found there is the caller's
// decision and it has better information about it.
type Tools struct {
	YtDlp  string
	FFmpeg string
}

// Ready reports whether both programs are known.
func (t Tools) Ready() bool { return t.YtDlp != "" && t.FFmpeg != "" }

// resolveTimeout bounds asking yt-dlp which URL to play.
//
// It is a network round trip and some JavaScript, about two or three seconds
// in practice. Past this something is wrong, and she is better served by being
// told than by more silence with no song in it.
const resolveTimeout = 45 * time.Second

// Resolve asks yt-dlp for a direct audio URL.
//
// m4a first and anything else second: m4a is one AAC stream that ffmpeg opens
// without demuxing a container full of video it will throw away, and it is
// what YouTube Music serves for almost everything. The fallback matters
// anyway, because "almost" is not "always" and a song that will not play is
// worse than one that plays from a larger stream.
func Resolve(ctx context.Context, tools Tools, pageURL string) (string, error) {
	if tools.YtDlp == "" {
		return "", &Error{Reason: "the music downloader is not installed yet"}
	}

	timed, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	command := exec.CommandContext(timed, tools.YtDlp,
		"--format", "bestaudio[ext=m4a]/bestaudio",
		"--get-url",
		"--no-playlist",
		"--no-warnings",
		// Nothing is written, so nothing needs cleaning up if this is killed
		// halfway -- which it will be, every time she stops a song early.
		"--no-cache-dir",
		pageURL)
	hideWindow(command)

	var out, errors strings.Builder
	command.Stdout = &out
	command.Stderr = &errors

	if err := command.Run(); err != nil {
		reason := strings.TrimSpace(errors.String())
		if reason == "" {
			reason = err.Error()
		}
		slog.Warn("could not resolve a song", "url", pageURL, "error", reason)
		return "", &Error{Reason: reason}
	}

	// One URL per line, and with a single format asked for there is one line.
	// Taking the first rather than the whole of it matters: a video that
	// slipped through as separate audio and video streams would otherwise be
	// handed to ffmpeg as one unusable string.
	for _, line := range strings.Split(out.String(), "\n") {
		if url := strings.TrimSpace(line); strings.HasPrefix(url, "http") {
			return url, nil
		}
	}
	return "", &Error{Reason: "no playable audio was found for that song"}
}

// Stream is a song being decoded, block by block, as it arrives.
//
// It satisfies the render's source: Format is called once with what the device
// actually accepted, and only then is ffmpeg started -- so ffmpeg resamples to
// the device's own rate, and nothing here has to stitch converted blocks
// together and leave a seam in the music every few milliseconds.
//
// Between ffmpeg and the device sits a pump goroutine and a buffer, and that
// is not an optimisation. [Read] is called on the thread driving the sound
// card, which must never block: if the network stalls mid-song, a read that
// waited for bytes that are not coming would hold that thread, the device
// would run dry, and the stall would never be noticed because nothing would
// ever return to notice it. So reads take what has arrived and no more, and an
// empty buffer is reported as nothing-yet rather than waited on.
type Stream struct {
	tools Tools
	url   string

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	cond     *sync.Cond
	command  *exec.Cmd
	problems *strings.Builder

	buffered []byte // decoded bytes waiting to be heard
	ended    bool   // ffmpeg has finished, for better or worse
	failed   error
	closed   bool
}

// Open prepares a stream. Nothing runs until the device has said what format
// it wants.
func Open(ctx context.Context, tools Tools, mediaURL string) *Stream {
	inner, cancel := context.WithCancel(ctx)
	stream := &Stream{
		tools: tools, url: mediaURL,
		ctx: inner, cancel: cancel,
		problems: &strings.Builder{},
	}
	stream.cond = sync.NewCond(&stream.mu)
	return stream
}

// bufferBytes is how much decoded audio is held between ffmpeg and the device.
//
// About a third of a second at 48 kHz stereo. Enough that a scheduling hiccup
// or a slow block off the network does not become a gap in the music, small
// enough that stopping is immediate rather than playing out a buffer she has
// already finished with -- and small enough that ffmpeg, which decodes far
// faster than the song plays, is kept waiting rather than racing ahead into
// memory.
const bufferBytes = 128 << 10

// prebuffer is how long Format waits for the first audio before letting the
// device start.
//
// ffmpeg takes a moment to open the connection and find the first frame.
// Starting the device into an empty buffer would put a third of a second of
// silence at the front of every song, which sounds like the command not having
// worked. Waiting is bounded: past this the song starts anyway and the buffer
// fills underneath it.
const prebuffer = 3 * time.Second

// Format starts ffmpeg, producing exactly what the device asked for.
func (s *Stream) Format(sampleRate, channels int) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return io.EOF
	}
	if s.command != nil {
		s.mu.Unlock()
		return nil
	}
	if s.tools.FFmpeg == "" {
		s.mu.Unlock()
		return &Error{Reason: "the audio decoder is not installed yet"}
	}

	command := exec.CommandContext(s.ctx, s.tools.FFmpeg,
		"-hide_banner",
		"-loglevel", "error",
		// Reconnect through the hiccups a four minute HTTP read meets.
		// Without these a dropped connection ends the song silently partway.
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-i", s.url,
		"-vn",
		"-f", "f32le",
		"-ar", fmt.Sprint(sampleRate),
		"-ac", fmt.Sprint(channels),
		"pipe:1")
	hideWindow(command)
	command.Stderr = s.problems

	pipe, err := command.StdoutPipe()
	if err != nil {
		s.mu.Unlock()
		return &Error{Reason: err.Error()}
	}
	if err := command.Start(); err != nil {
		s.mu.Unlock()
		return &Error{Reason: "could not start the audio decoder: " + err.Error()}
	}
	s.command = command
	s.mu.Unlock()

	go s.pump(pipe)
	s.waitForFirstAudio()
	return nil
}

// pump moves decoded audio out of ffmpeg and into the buffer, and stops when
// the buffer is full so that a decoder faster than real time does not pull the
// whole song into memory.
func (s *Stream) pump(pipe io.ReadCloser) {
	defer pipe.Close()

	chunk := make([]byte, 32<<10)
	for {
		read, err := pipe.Read(chunk)
		if read > 0 {
			s.mu.Lock()
			for len(s.buffered) >= bufferBytes && !s.closed {
				s.cond.Wait()
			}
			if s.closed {
				s.mu.Unlock()
				return
			}
			s.buffered = append(s.buffered, chunk[:read]...)
			s.cond.Broadcast()
			s.mu.Unlock()
		}
		if err != nil {
			s.mu.Lock()
			s.ended = true
			if !errors.Is(err, io.EOF) && s.ctx.Err() == nil {
				s.failed = &Error{Reason: s.reasonLocked(err)}
			}
			s.cond.Broadcast()
			s.mu.Unlock()
			return
		}
	}
}

// waitForFirstAudio holds the device back until there is something to play.
func (s *Stream) waitForFirstAudio() {
	deadline := time.Now().Add(prebuffer)
	s.mu.Lock()
	defer s.mu.Unlock()

	for len(s.buffered) == 0 && !s.ended && !s.closed && time.Now().Before(deadline) {
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		s.mu.Lock()
	}
}

// Read fills dst with whatever has arrived, and reports io.EOF when the song
// has finished.
//
// Whole samples only. A pipe read can end in the middle of a four byte float,
// and a sample assembled from the wrong halves is a click; the remainder stays
// in the buffer until the rest of it turns up.
func (s *Stream) Read(dst []float32) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, io.EOF
	}
	if len(dst) == 0 {
		return 0, nil
	}

	samples := min(len(dst), len(s.buffered)/4)
	if samples == 0 {
		switch {
		case s.failed != nil:
			return 0, s.failed
		case s.ended:
			return 0, io.EOF
		default:
			// Nothing yet. Reported rather than waited for: the caller is the
			// thread driving the sound card, and it fills the gap with silence
			// and comes back.
			return 0, nil
		}
	}

	for index := 0; index < samples; index++ {
		dst[index] = math.Float32frombits(binary.LittleEndian.Uint32(s.buffered[index*4:]))
	}
	s.buffered = s.buffered[samples*4:]
	// Compact rather than letting the slice header walk off the end of an
	// allocation that is never reclaimed while the song plays.
	if len(s.buffered) == 0 {
		s.buffered = s.buffered[:0:0]
	}
	s.cond.Broadcast()
	return samples, nil
}

// reasonLocked prefers what ffmpeg said over what the pipe said. "broken pipe"
// is true and useless; "Server returned 403 Forbidden" is the actual problem.
func (s *Stream) reasonLocked(err error) string {
	if said := strings.TrimSpace(s.problems.String()); said != "" {
		lines := strings.Split(said, "\n")
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return err.Error()
}

// Close stops the decoder. It is safe to call more than once, and from any
// goroutine -- which the stop command relies on.
func (s *Stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	command := s.command
	s.cond.Broadcast() // let the pump out of its wait
	s.mu.Unlock()

	s.cancel()
	if command != nil {
		// Already killed by the context; this only reaps it, so a song stopped
		// early does not leave an ffmpeg behind for the rest of the stream.
		_ = command.Wait()
	}
	return nil
}
