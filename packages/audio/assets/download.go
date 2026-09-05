package assets

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The client these downloads use.
//
// Deliberately not http.DefaultClient, and deliberately without an overall
// Timeout: half a gigabyte on a slow connection legitimately takes many
// minutes, and a deadline on the whole request would abort the ones that were
// working. The limits below are the ones that only ever fire on something
// genuinely wrong -- a host that never answers, a handshake that never
// completes, a server that accepts the connection and then says nothing.
var downloadClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second}).DialContext,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	},
}

// progressMeter tracks how much has arrived and how fast, over a trailing
// window rather than since the start.
//
// Shared between the read loop and the watchdog, because the reports that
// matter most are the ones during a quiet spell -- and those are exactly the
// ones the read loop cannot send, since it is blocked waiting for bytes that
// are not coming. Without them the page keeps the last figure it was given,
// which is how a dead transfer shows as "43%, 1.0 MB/s" indefinitely.
type progressMeter struct {
	mu      sync.Mutex
	written int64
	recent  []meterSample
}

type meterSample struct {
	at    time.Time
	bytes int64
}

func newProgressMeter(from int64) *progressMeter {
	return &progressMeter{written: from, recent: []meterSample{{at: time.Now(), bytes: from}}}
}

func (m *progressMeter) add(n int64) {
	m.mu.Lock()
	m.written += n
	m.mu.Unlock()
}

// snapshot records the moment and returns what to show for it.
func (m *progressMeter) snapshot() (int64, float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.recent = append(m.recent, meterSample{at: now, bytes: m.written})
	for len(m.recent) > 1 && now.Sub(m.recent[0].at) > speedWindow {
		m.recent = m.recent[1:]
	}
	speed := 0.0
	if span := now.Sub(m.recent[0].at).Seconds(); span > 0 {
		speed = float64(m.written-m.recent[0].bytes) / span
	}
	return m.written, speed
}

// permanent marks a failure that another attempt cannot fix.
//
// The retry above exists for connections that drop and servers that go quiet.
// A 404 is neither: the asset is not there, and saying so at once is far more
// use than saying it a minute later.
type permanent struct{ error }

func permanentStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false // worth another go
	}
	return code >= 400 && code < 500
}

// stallTimeout is how long a transfer may deliver nothing before it is treated
// as dead.
//
// The failure this exists for is the one with no error attached: the socket
// stays open, the bytes stop, and Read blocks forever. Nothing times out,
// nothing fails, and the settings page sits at "43%, 1.0 MB/s" indefinitely --
// the speed being the average since the start, which decays far too slowly to
// look like the stall it is. Generous enough not to fire on a connection that
// is merely slow, because giving up on a working download is the worse mistake.
// A variable, not a constant, only so the tests can make it fire in a moment
// rather than in three quarters of a minute.
var stallTimeout = 45 * time.Second

// downloadAttempts is how many times a stalled or dropped transfer is picked
// up again. Resuming is cheap -- the .part file is already on disk and the
// range request asks only for the rest -- so the cost of another go is a few
// seconds, against her otherwise being stranded partway with no way forward.
const downloadAttempts = 5

// retryWait is the first pause between attempts, growing with each one. Also a
// variable for the tests, for the same reason.
var retryWait = time.Second

// speedWindow is how much recent history the reported speed is measured over.
//
// An average since the start is the wrong number to show: it barely moves when
// a transfer stalls, so the one moment the figure matters is the one moment it
// lies. Over a short window it falls towards zero within seconds, which is
// what a stall actually looks like.
// A variable only so the tests can use a short window; five seconds is the
// figure that matters in the settings page.
var speedWindow = 5 * time.Second

// download fetches one file, resuming a partial one rather than starting over.
//
// Half a gigabyte over a home connection is long enough that something will
// interrupt it -- a dropped link, a closed laptop, a stream that needed the
// bandwidth. Starting again from nothing each time would make it effectively
// impossible on a slow connection, and this is the download standing between
// her and an application that can hear at all.
func download(ctx context.Context, url, target string, expected int64, onProgress func(done, total int64, speed float64)) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	// Each attempt resumes where the last one stopped, so a connection that
	// drops at 43% costs seconds rather than the whole download. Cancelling is
	// not retried: that was a decision, not a fault.
	var err error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		err = downloadOnce(ctx, url, target, expected, onProgress)
		if err == nil || ctx.Err() != nil {
			return err
		}
		// A file that is not there will not be there next time either. Trying
		// four more times would only add ten seconds of silence before telling
		// her the same thing.
		var settled permanent
		if errors.As(err, &settled) {
			return settled.error
		}
		slog.Warn("a download stopped early, resuming",
			"attempt", attempt, "of", downloadAttempts, "url", url, "error", err)

		// A short pause, so a server that is refusing everything for a moment
		// is not hammered five times in five milliseconds.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * retryWait):
		}
	}
	return err
}

// downloadOnce is one attempt, picking up from whatever .part is already there.
func downloadOnce(ctx context.Context, url, target string, expected int64, onProgress func(done, total int64, speed float64)) error {
	partial := target + ".part"

	var existing int64
	if info, err := os.Stat(partial); err == nil {
		existing = info.Size()
	}

	// A context of its own, so the watchdog below can abort a transfer that has
	// gone quiet without disturbing anything else using the caller's.
	attemptCtx, abort := context.WithCancel(ctx)
	defer abort()

	request, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "mikkilens")
	if existing > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
	}

	response, err := downloadClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK:
		existing = 0 // the server ignored the range, so start over
	case http.StatusPartialContent:
		slog.Info("resuming a download", "have", existing, "url", url)
	default:
		failure := fmt.Errorf("the download failed: %s", response.Status)
		if permanentStatus(response.StatusCode) {
			return permanent{failure}
		}
		return failure
	}

	total := expected
	if response.ContentLength > 0 {
		total = existing + response.ContentLength
	}

	flags := os.O_CREATE | os.O_WRONLY
	if existing > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partial, flags, 0o644)
	if err != nil {
		return err
	}

	buffer := make([]byte, 1<<20)
	lastReport := time.Now()
	meter := newProgressMeter(existing)

	// The watchdog. Read on a stalled socket blocks with no error and no end,
	// so the only way to notice is to watch the clock from outside and cancel
	// the request, which makes the blocked Read return.
	var lastByte atomic.Int64
	lastByte.Store(time.Now().UnixNano())
	stalled := make(chan struct{})
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-attemptCtx.Done():
				return
			case <-ticker.C:
				quiet := time.Since(time.Unix(0, lastByte.Load()))
				if quiet > stallTimeout {
					close(stalled)
					abort()
					return
				}
				// Keep reporting while nothing arrives. The read loop cannot:
				// it is blocked on bytes that are not coming, which is the
				// whole problem.
				if onProgress != nil && quiet > time.Second {
					done, speed := meter.snapshot()
					onProgress(done, total, speed)
				}
			}
		}
	}()
	defer func() { abort(); <-watching }()

	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			if _, err := file.Write(buffer[:read]); err != nil {
				file.Close()
				return err
			}
			meter.add(int64(read))
			lastByte.Store(time.Now().UnixNano())

			// Reported about twice a second: often enough to sound alive,
			// rarely enough not to flood the settings page with updates.
			if onProgress != nil && time.Since(lastReport) > 500*time.Millisecond {
				lastReport = time.Now()
				done, speed := meter.snapshot()
				onProgress(done, total, speed)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			file.Close()
			select {
			case <-stalled:
				return fmt.Errorf("the download stopped sending for %s", stallTimeout)
			default:
			}
			return readErr
		}
		if ctx.Err() != nil {
			file.Close()
			return ctx.Err()
		}
	}

	if err := file.Close(); err != nil {
		return err
	}
	// Renamed only once it is whole, so an interrupted download is never
	// mistaken on the next start for a model that can be loaded. A truncated
	// ggml-small.bin is worse than no model at all: it is found, loaded, and
	// fails at the moment she speaks.
	return os.Rename(partial, target)
}

// unpack extracts the files matching want from an archive, flatly.
//
// Flatly because the whisper.cpp releases put their binaries in a Release/
// directory and the ONNX runtime puts its library in lib/, while everything
// that looks for them looks in one directory. Filtered because those archives
// also carry test executables, headers and import libraries that would
// otherwise sit in data/models forever, being nothing.
func unpack(archive, directory string, want func(name string) bool) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()

	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}

	extracted := 0
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(entry.Name)
		if name == "" || strings.HasPrefix(name, ".") || !want(name) {
			continue
		}
		if err := extract(entry, filepath.Join(directory, name)); err != nil {
			return err
		}
		extracted++
	}
	if extracted == 0 {
		return fmt.Errorf("%s held none of the files that were wanted", filepath.Base(archive))
	}
	return nil
}

func extract(entry *zip.File, target string) error {
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()

	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, source); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// wantedFromWhisper keeps the executables recognition drives and the libraries
// they load, and leaves the test binaries and the unrelated tools behind.
func wantedFromWhisper(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "test-") {
		return false
	}
	if strings.HasSuffix(lower, ".dll") {
		return true
	}
	switch lower {
	case "whisper-server.exe", "whisper-cli.exe", "main.exe":
		return true
	}
	return false
}

// wantedFromRuntime keeps the ONNX runtime library and nothing else: the
// archive is 65 megabytes of headers, import libraries and documentation
// around the one file the wake word actually loads.
func wantedFromRuntime(name string) bool {
	lower := strings.ToLower(name)
	return lower == "onnxruntime.dll" ||
		lower == "onnxruntime_providers_shared.dll" ||
		lower == "libonnxruntime.so"
}
