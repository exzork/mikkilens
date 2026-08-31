package llm

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// MikkiLens runs its own model rather than asking her to install one.
//
// Telling someone to install Ollama, pull a model and leave it running is
// three more things to get right before anything works, and every one of them
// is a place to get stuck with no way to see why. The same reasoning that put
// whisper.cpp behind a spawned server applies here: the runtime ships with the
// application, the model is a file in data/models, and MikkiLens starts and
// stops the server itself.
//
// Out of process rather than linked in, again deliberately. It needs no CGO
// toolchain to build MikkiLens, and whichever llama.cpp build suits the
// machine -- plain CPU, CUDA, Vulkan -- can be dropped in without rebuilding
// anything.

// serverNames are what llama.cpp ships its server as.
var serverNames = []string{"llama-server.exe", "llama-server"}

// contextTokens is the context window asked for.
//
// The prompt is the command list and one short utterance, which is well under
// a thousand tokens. Asking for more would reserve memory on a machine that is
// also encoding video.
const contextTokens = 2048

// LocalServer runs llama.cpp with a model loaded, and answers on localhost.
type LocalServer struct {
	mu      sync.Mutex
	command *exec.Cmd
	baseURL string
	model   string
	loading bool
	vision  bool
}

// Vision reports whether the loaded model can describe an image.
func (s *LocalServer) Vision() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vision && s.baseURL != ""
}

// NewLocalServer prepares a server. Nothing starts until Start.
func NewLocalServer() *LocalServer { return &LocalServer{} }

// bundled is the one server this process runs.
//
// A package-level instance because a language model is several gigabytes of
// resident memory: two of them on a machine that is also encoding video would
// be a serious problem, and nothing here has any use for a second.
var (
	bundledOnce sync.Once
	bundled     *LocalServer
)

// Bundled is the model server MikkiLens runs for itself.
func Bundled() *LocalServer {
	bundledOnce.Do(func() { bundled = NewLocalServer() })
	return bundled
}

// BaseURL is where the server answers, or empty if it is not running.
func (s *LocalServer) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}

// Loading reports whether the model is still being read into memory.
func (s *LocalServer) Loading() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loading
}

// ModelName is the model file in use, for the settings page.
func (s *LocalServer) ModelName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.model == "" {
		return ""
	}
	return filepath.Base(s.model)
}

// Start launches the server in the background.
//
// Having no runtime or no model is not an error worth shouting about: it means
// the fallback matcher is simply not available yet, which is the state
// MikkiLens ships in until she chooses to download one.
func (s *LocalServer) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.command != nil || s.loading {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	binary, err := FindServerBinary()
	if err != nil {
		return err
	}
	model, err := FindModelFile()
	if err != nil {
		return err
	}

	port, err := freePort()
	if err != nil {
		return &Error{Reason: "could not find a free port for the model: " + err.Error()}
	}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)

	arguments := []string{
		"--model", model,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--ctx-size", strconv.Itoa(contextTokens),
		"--threads", strconv.Itoa(matcherThreads()),
	}
	// With a projector the same server can look at a screenshot, which is what
	// lets one loaded model answer both "what did she say" and "what is on the
	// screen" without a second download or a cloud account.
	projector := ProjectorFor(model)
	if projector != "" {
		arguments = append(arguments, "--mmproj", projector)
	}

	command := exec.CommandContext(ctx, binary, arguments...)
	hideConsole(command)
	command.Stdout, command.Stderr = nil, nil

	if err := command.Start(); err != nil {
		return &Error{Reason: "could not start the language model: " + err.Error()}
	}

	// Tied to this process, so a force quit cannot leave gigabytes of model
	// resident with nothing to stop it.
	adopt(command.Process)

	s.mu.Lock()
	s.command, s.model, s.loading = command, model, true
	s.vision = projector != ""
	s.mu.Unlock()

	go func() {
		err := waitForModel(ctx, baseURL, command)

		s.mu.Lock()
		s.loading = false
		if err != nil {
			s.command = nil
		} else {
			s.baseURL = baseURL + "/v1"
		}
		s.mu.Unlock()

		if err != nil {
			slog.Warn("the language model did not start", "error", err)
			return
		}
		slog.Info("the language model is ready",
			"model", filepath.Base(model), "threads", matcherThreads())
	}()
	return nil
}

// Stop ends the server.
func (s *LocalServer) Stop() {
	s.mu.Lock()
	command := s.command
	s.command, s.baseURL, s.loading, s.vision = nil, "", false, false
	s.mu.Unlock()

	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
}

// matcherThreads leaves the machine room to do its actual job.
//
// This runs while she is streaming, and a language model will take every core
// it is given. Taking them would stutter the encode her viewers are watching,
// which is a far worse outcome than a command being understood a little more
// slowly. A quarter of the machine, capped, is plenty for one short answer.
func matcherThreads() int {
	threads := runtime.NumCPU() / 4
	if threads < 1 {
		threads = 1
	}
	if threads > 4 {
		threads = 4
	}
	return threads
}

// waitForModel waits for the server to finish loading.
//
// Loading several gigabytes from disk takes a while, and the port opens before
// the model is ready, so a plain connection is not proof of anything: the
// health endpoint is asked until it says the model is actually loaded.
func waitForModel(ctx context.Context, baseURL string, command *exec.Cmd) error {
	deadline := time.Now().Add(5 * time.Minute)
	address := baseURL[len("http://"):]

	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()

	client := &http.Client{Timeout: 3 * time.Second}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return &Error{Reason: "gave up waiting for the language model to load"}
		case err := <-exited:
			return &Error{Reason: fmt.Sprintf("the language model stopped while loading: %v", err)}
		default:
		}

		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		connection.Close()

		response, err := client.Get(baseURL + "/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return &Error{Reason: "the language model took too long to load"}
}

// FindServerBinary looks where the runtime is kept.
func FindServerBinary() (string, error) {
	for _, directory := range runtimeSearchPath() {
		for _, name := range serverNames {
			candidate := filepath.Join(directory, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	for _, name := range serverNames {
		if resolved, err := exec.LookPath(name); err == nil {
			return resolved, nil
		}
	}
	return "", &Error{Reason: "the language model runtime is not installed yet"}
}

// FindModelFile looks for a GGUF to load, preferring one that looks like a
// model MikkiLens knows how to download.
func FindModelFile() (string, error) {
	for _, directory := range runtimeSearchPath() {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		fallback := ""
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || filepath.Ext(name) != ".gguf" {
				continue
			}
			for _, known := range Models {
				if name == known.File {
					return filepath.Join(directory, name), nil
				}
			}
			if fallback == "" {
				fallback = filepath.Join(directory, name)
			}
		}
		if fallback != "" {
			return fallback, nil
		}
	}
	return "", &Error{Reason: "no language model has been downloaded yet"}
}

// runtimeSearchPath is where the runtime and its models live.
func runtimeSearchPath() []string {
	return []string{
		filepath.Join(paths.ModelsDir(), "llama"),
		paths.ModelsDir(),
	}
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
