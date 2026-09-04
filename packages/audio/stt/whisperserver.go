package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
)

// whisperServer keeps whisper.cpp running with the model already loaded.
//
// The obvious way to drive whisper.cpp out of process is to run its one-shot
// CLI per command, and that is what this did first. It works, but it reloads
// half a gigabyte of model from disk every single time: a two-second command
// took three seconds to recognise, nearly all of it loading. Three seconds of
// silence after speaking is long enough that she starts again, and then the
// command arrives twice.
//
// So the server is started once and kept. The model is loaded when MikkiLens
// starts, and each command afterwards is only the recognition itself.
type whisperServer struct {
	binary    string
	modelPath string
	threads   int
	beamSize  int
	gpu       bool
	note      string
	language  string

	mu      sync.Mutex
	command *exec.Cmd
	baseURL string
	client  *http.Client
	label   string
}

// serverBinaries are the names whisper.cpp has shipped its server under.
var serverBinaries = []string{"whisper-server.exe", "whisper-server"}

func newWhisperServer(settings config.STT) (Backend, error) {
	build, err := chooseBuild(settings.Binary, settings.Device, serverBinaries)
	if err != nil {
		return nil, err
	}
	model, err := findModel(settings.ModelPath, settings.ModelSize)
	if err != nil {
		return nil, err
	}
	return &whisperServer{
		binary:    build.binary,
		modelPath: model,
		threads:   recognitionThreads(),
		beamSize:  beamFor(settings.BeamSize, build.gpu),
		gpu:       build.gpu,
		note:      build.note,
		label:     fmt.Sprintf("whisper.cpp %s on %s", modelLabel(model), build.where()),
	}, nil
}

// beamFor decides how hard to search for the words.
//
// A wider beam is measurably more accurate and costs decode time roughly
// linearly, which is a trade that depends entirely on where it runs: on the
// graphics card five beams are still faster than she can notice, and on the
// processor they are seconds she spends waiting in silence. Zero in the
// config file means "decide", and is the default.
func beamFor(configured int, gpu bool) int {
	if configured > 0 {
		return configured
	}
	if gpu {
		return 5
	}
	return 1
}

// modelLabel is the model name without the ggml- prefix or the extension, so
// the status page reads "whisper.cpp small on the graphics card".
func modelLabel(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return strings.TrimPrefix(name, "ggml-")
}

func (w *whisperServer) Name() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.label
}

// Load starts the server and waits for the model to be in memory.
//
// This is slow the first time -- seconds, for a large model -- which is why
// the engine does it in the background while everything else comes up.
func (w *whisperServer) Load(ctx context.Context) error {
	w.mu.Lock()
	if w.command != nil {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	port, err := freePort()
	if err != nil {
		return &Error{Reason: "could not find a port for speech recognition: " + err.Error()}
	}

	arguments := []string{
		"--model", w.modelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--threads", strconv.Itoa(w.threads),
		"--beam-size", strconv.Itoa(w.beamSize),
		// Recognition runs while she is streaming, so it must not take the
		// machine with it. Whisper's own VAD is off: the recorder has already
		// trimmed the silence.
		"--no-timestamps",
		// Whisper writes "(clears throat)" and the like as ordinary text,
		// which then has to be matched as a command. Suppressing those tokens
		// is cheaper than cleaning them out afterwards.
		"--suppress-nst",
	}
	if w.gpu {
		// Flash attention is a straight win on the card and unsupported off
		// it, so it travels with the same decision.
		arguments = append(arguments, "--flash-attn")
	} else {
		arguments = append(arguments, "--no-gpu")
	}

	command := exec.Command(w.binary, arguments...)
	hideConsole(command)
	command.Stdout = io.Discard
	command.Stderr = io.Discard

	if err := command.Start(); err != nil {
		return &Error{Reason: "could not start speech recognition: " + err.Error()}
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForServer(ctx, baseURL, command); err != nil {
		_ = command.Process.Kill()
		return err
	}

	w.mu.Lock()
	w.command, w.baseURL = command, baseURL
	w.client = &http.Client{Timeout: 60 * time.Second}
	w.mu.Unlock()

	// One throwaway inference before anything asks for a real one.
	//
	// A prebuilt whisper.cpp carries kernels for the cards that existed when
	// it was published, and on a newer one the driver compiles them from PTX
	// the first time they run: twelve seconds, once, cached afterwards. That
	// is a fine price to pay while the rest of MikkiLens is still starting,
	// and a terrible one to pay on the first thing she says.
	if w.gpu {
		w.warmUp(ctx)
	}

	slog.Info("speech recognition loaded",
		"model", filepath.Base(w.modelPath), "port", port,
		"accelerator", w.where(), "beam_size", w.beamSize)
	if w.note != "" {
		// Recognition that quietly runs ten times slower than it could is the
		// hardest kind of problem to notice from the outside.
		slog.Warn("speech recognition is not using the graphics card", "reason", w.note)
	}
	return nil
}

func (w *whisperServer) warmUp(ctx context.Context) {
	started := time.Now()
	if _, err := w.Transcribe(ctx, make([]float32, SampleRate), ""); err != nil {
		// Not fatal: the next command simply pays for the compile instead.
		slog.Warn("could not warm speech recognition up", "error", err)
		return
	}
	slog.Info("speech recognition warmed up", "took", time.Since(started).Round(time.Millisecond))
}

func (w *whisperServer) where() string {
	if w.gpu {
		return "the graphics card"
	}
	return "the processor"
}

// waitForServer polls until the port answers, or the process gives up.
func waitForServer(ctx context.Context, baseURL string, command *exec.Cmd) error {
	deadline := time.Now().Add(3 * time.Minute)
	address := strings.TrimPrefix(baseURL, "http://")

	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return &Error{Reason: "gave up waiting for speech recognition to load"}
		case err := <-exited:
			return &Error{Reason: fmt.Sprintf("speech recognition stopped while loading: %v", err)}
		default:
		}

		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			connection.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return &Error{Reason: "speech recognition did not finish loading in time"}
}

func (w *whisperServer) Close() error {
	w.mu.Lock()
	command := w.command
	w.command = nil
	w.mu.Unlock()

	if command != nil && command.Process != nil {
		return command.Process.Kill()
	}
	return nil
}

func (w *whisperServer) Transcribe(ctx context.Context, audio []float32, language string) (Transcript, error) {
	w.mu.Lock()
	baseURL, client := w.baseURL, w.client
	w.mu.Unlock()

	if baseURL == "" || client == nil {
		return Transcript{}, &Error{Reason: "speech recognition is not loaded"}
	}

	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)

	part, err := form.CreateFormFile("file", "utterance.wav")
	if err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}
	if _, err := part.Write(encodeWAV(audio, SampleRate)); err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}
	_ = form.WriteField("response_format", "json")
	_ = form.WriteField("temperature", "0")
	if language != "" && language != "auto" {
		_ = form.WriteField("language", language)
	}
	if err := form.Close(); err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/inference", body)
	if err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}
	request.Header.Set("Content-Type", form.FormDataContentType())

	response, err := client.Do(request)
	if err != nil {
		return Transcript{}, &Error{Reason: "speech recognition did not answer: " + err.Error()}
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}
	if response.StatusCode != http.StatusOK {
		return Transcript{}, &Error{
			Reason: fmt.Sprintf("speech recognition answered %s", response.Status)}
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		// Some builds answer with bare text rather than JSON.
		return Transcript{Text: cleanTranscript(string(payload)), Language: language}, nil
	}
	return Transcript{Text: cleanTranscript(parsed.Text), Language: language}, nil
}

// freePort asks the operating system for a port nobody is using, so two
// MikkiLens installations or a stray server cannot collide.
func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// namedBinaries looks where a person would actually put a whisper.cpp build:
// the configured path, then data/models, then anywhere on PATH.
//
// Every match is returned rather than the first, in the order they were found,
// because which one to run is not a question of order alone: a GPU build
// further down the list beats a processor-only build at the top of it.
func namedBinaries(configured string, names []string) ([]string, error) {
	if configured != "" {
		if resolved, err := exec.LookPath(configured); err == nil {
			return []string{resolved}, nil
		}
		if _, err := os.Stat(configured); err == nil {
			return []string{configured}, nil
		}
		return nil, &Error{Reason: "the configured whisper.cpp binary was not found: " + configured}
	}

	found := []string{}
	for _, directory := range binaryDirectories() {
		for _, name := range names {
			candidate := filepath.Join(directory, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				found = append(found, candidate)
			}
		}
	}
	for _, name := range names {
		if resolved, err := exec.LookPath(name); err == nil {
			found = append(found, resolved)
		}
	}
	if len(found) == 0 {
		return nil, &Error{Reason: "no whisper.cpp build was found"}
	}
	return found, nil
}
