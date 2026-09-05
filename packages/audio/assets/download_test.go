package assets

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Resuming and giving up: what happens when half a gigabyte does not arrive in
// one piece.

// A server that accepts the connection, sends some of the file and then goes
// quiet forever is the failure with no error attached: Read blocks, nothing
// times out, and the settings page sits at a percentage that never moves. The
// watchdog has to notice and the retry has to pick up where it stopped.
func TestAStalledDownloadIsNoticedAndResumed(t *testing.T) {
	previousStall, previousWait := stallTimeout, retryWait
	stallTimeout, retryWait = 300*time.Millisecond, 10*time.Millisecond
	defer func() { stallTimeout, retryWait = previousStall, previousWait }()

	whole := bytes.Repeat([]byte("m"), 8000)
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			attempts++
			from := 0
			if _, err := fmt.Sscanf(request.Header.Get("Range"), "bytes=%d-", &from); err == nil && from > 0 {
				writer.Header().Set("Content-Range",
					fmt.Sprintf("bytes %d-%d/%d", from, len(whole)-1, len(whole)))
				writer.WriteHeader(http.StatusPartialContent)
			}

			// The first attempt sends half and then stops for good.
			if attempts == 1 {
				_, _ = writer.Write(whole[:len(whole)/2])
				writer.(http.Flusher).Flush()
				<-request.Context().Done()
				return
			}
			_, _ = writer.Write(whole[from:])
		}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "ggml-small.bin")
	if err := download(context.Background(), server.URL, target, int64(len(whole)), nil); err != nil {
		t.Fatalf("download: %v", err)
	}

	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the finished file: %v", err)
	}
	if !bytes.Equal(written, whole) {
		t.Errorf("got %d bytes, want %d", len(written), len(whole))
	}
	if attempts < 2 {
		t.Errorf("made %d attempts, want the stall to have forced another", attempts)
	}
}

// The speed shown has to fall when the bytes stop. An average since the start
// barely moves, which is why "1.0 MB/s" stayed on screen through a dead
// transfer.
func TestReportedSpeedFallsWhenTheTransferGoesQuiet(t *testing.T) {
	previousStall, previousWait, previousWindow := stallTimeout, retryWait, speedWindow
	stallTimeout, retryWait, speedWindow = 6*time.Second, 10*time.Millisecond, 400*time.Millisecond
	defer func() {
		stallTimeout, retryWait, speedWindow = previousStall, previousWait, previousWindow
	}()

	// The quiet spell has to be comfortably longer than the watchdog's own
	// interval, not just longer than the second of silence it reports after.
	// At 1.2s it was neither: the watchdog wakes once a second and reports once
	// the transfer has been quiet for a second, so there was a single 200ms
	// window for a tick to land in, and whether it did came down to scheduling
	// -- which failed about one run in four, more on a loaded machine. Nothing
	// was wrong with the download; the test was timing a coin flip.
	whole := bytes.Repeat([]byte("m"), 4000)
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write(whole[:2000])
			writer.(http.Flusher).Flush()
			time.Sleep(2500 * time.Millisecond) // quiet, but not long enough to be dead
			_, _ = writer.Write(whole[2000:])
		}))
	defer server.Close()

	speeds := []float64{}
	target := filepath.Join(t.TempDir(), "model.bin")
	err := download(context.Background(), server.URL, target, int64(len(whole)),
		func(done, total int64, speed float64) { speeds = append(speeds, speed) })
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	if len(speeds) < 2 {
		t.Fatalf("only %d reports; the quiet spell must still be reported on", len(speeds))
	}

	// The last report is after the bytes resume, so it is high again by then.
	// What matters is that the figure fell while nothing was arriving, rather
	// than holding at whatever it read before the transfer went quiet.
	peak, lowest := speeds[0], speeds[0]
	for _, speed := range speeds {
		if speed > peak {
			peak = speed
		}
		if speed < lowest {
			lowest = speed
		}
	}
	// Silence longer than the window must take it all the way to nothing, not
	// merely dent an average that keeps the old figure alive.
	if lowest != 0 {
		t.Errorf("speed fell from %.0f only to %.0f B/s; silence longer than "+
			"the window must read as zero", peak, lowest)
	}
}
