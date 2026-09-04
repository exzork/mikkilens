package assets

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	partial := target + ".part"

	var existing int64
	if info, err := os.Stat(partial); err == nil {
		existing = info.Size()
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "mikkilens")
	if existing > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
	}

	response, err := http.DefaultClient.Do(request)
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
		return fmt.Errorf("the download failed: %s", response.Status)
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

	written := existing
	started := time.Now()
	buffer := make([]byte, 1<<20)
	lastReport := time.Now()

	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			if _, err := file.Write(buffer[:read]); err != nil {
				file.Close()
				return err
			}
			written += int64(read)

			// Reported about twice a second: often enough to sound alive,
			// rarely enough not to flood the settings page with updates.
			if onProgress != nil && time.Since(lastReport) > 500*time.Millisecond {
				lastReport = time.Now()
				elapsed := time.Since(started).Seconds()
				speed := 0.0
				if elapsed > 0 {
					speed = float64(written-existing) / elapsed
				}
				onProgress(written, total, speed)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			file.Close()
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
