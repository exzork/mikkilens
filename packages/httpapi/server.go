// Package httpapi is the local API the desktop app talks to.
//
// It is HTTP on localhost rather than Electron IPC on purpose. The voice
// engine keeps running whether or not a window is open, someone can help from
// their own laptop when [ui] lan_access is turned on, and the whole surface
// can be exercised with curl when something is wrong.
//
// Everything here operates on the live engine, so a change takes effect
// immediately rather than at the next restart -- which matters, because
// restarting means losing the OBS connection and the chat backlog mid-stream.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/exzork/mikkilens/packages/audio/assets"
	"github.com/exzork/mikkilens/packages/audio/capture"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/audio/hotkey"
	"github.com/exzork/mikkilens/packages/audio/stt"
	"github.com/exzork/mikkilens/packages/audio/wake"
	"github.com/exzork/mikkilens/packages/chat"
	"github.com/exzork/mikkilens/packages/controllers/obs"
	"github.com/exzork/mikkilens/packages/controllers/youtube"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/state"
)

// Engine is what the API needs from the running app.
//
// It is an interface rather than the concrete engine so the routes can be
// tested against a stub with no audio hardware and no network, which is the
// only way this many endpoints stay covered.
type Engine interface {
	Config() config.Config
	ApplyConfig(config.Config) error
	Locale() *i18n.Locale
	State() *state.Store
	Bus() *feedback.Bus
	Commands() *intent.Set
	AdoptCommands(*intent.Set)
	ReloadCommands()
	Router() *intent.Router
	Transcriber() *stt.Transcriber
	Installing() assets.Progress
	Wake() *wake.Detector
	WakeError() string
	Hotkey() hotkey.Watcher
	HotkeyError() string
	Microphone() *capture.Stream
	OBS() *obs.Controller
	YouTube() *youtube.Controller
	ChatIngest() *chat.Ingest
	ChatReader() *chat.Reader
	BeginListening()
	RunCommand(id string, confirm bool)
	RefreshYouTube(context.Context)
	ConnectYouTube(context.Context) error
	ConnectChannel(context.Context) error
	DisconnectYouTube() error
	SwitchChannel(context.Context, string) error
}

// Server runs the API alongside the voice engine.
type Server struct {
	engine   Engine
	settings config.UI

	mu       sync.Mutex
	http     *http.Server
	listener net.Listener
	done     chan struct{}
}

// NewServer prepares the API. Nothing listens until Start is called.
func NewServer(engine Engine) *Server {
	return &Server{engine: engine, settings: engine.Config().UI}
}

// Host is the address to bind. Localhost unless LAN access is turned on
// explicitly, because the API can mute her microphone and end her stream.
func (s *Server) Host() string {
	if s.settings.LanAccess {
		return "0.0.0.0"
	}
	if s.settings.Host == "" {
		return "127.0.0.1"
	}
	return s.settings.Host
}

// Port is the port actually bound, which may differ from the configured one
// when that port was taken.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		if address, ok := s.listener.Addr().(*net.TCPAddr); ok {
			return address.Port
		}
	}
	return s.settings.Port
}

// URL is where the desktop app should connect.
func (s *Server) URL() string {
	host := s.Host()
	if host == "0.0.0.0" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%d", host, s.Port())
}

// Start begins listening.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.http != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	address := net.JoinHostPort(s.Host(), strconv.Itoa(s.settings.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("could not open the settings API on %s: %w", address, err)
	}

	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.mu.Lock()
	s.http, s.listener, s.done = server, listener, make(chan struct{})
	done := s.done
	s.mu.Unlock()

	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("the settings API stopped", "error", err)
		}
	}()

	slog.Info("settings API listening", "url", s.URL())
	return nil
}

// Stop shuts the API down.
func (s *Server) Stop() {
	s.mu.Lock()
	server, done := s.http, s.done
	s.http, s.listener = nil, nil
	s.mu.Unlock()

	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)

	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
}

// Running reports whether the API is listening.
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.http != nil
}

// Handler builds the routes. It is exported so tests can drive them without
// binding a port.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)

	// The desktop app is served from a file:// origin, so the browser sends
	// Origin: null. Binding to localhost is what keeps this safe; CORS here
	// only exists so the renderer can talk to its own engine.
	return withCORS(mux)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		writer.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, OPTIONS")
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// -- websocket ----------------------------------------------------------------

var upgrader = websocket.Upgrader{
	// The engine listens on localhost, and any origin check here would be
	// checking a value the client controls. The bind address is the boundary.
	CheckOrigin: func(*http.Request) bool { return true },
}

type envelope struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// handleWebSocket pushes state changes, so the desktop app never has to poll.
func (s *Server) handleWebSocket(writer http.ResponseWriter, request *http.Request) {
	connection, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer connection.Close()

	// Buffered, and dropped when full: a stalled window must never block the
	// state store, which the voice path writes to on every command.
	updates := make(chan state.Changes, 64)
	unsubscribe := s.engine.State().Subscribe(func(delta state.Changes) {
		select {
		case updates <- delta:
		default:
		}
	})
	defer unsubscribe()

	if err := writeJSON(connection, envelope{Type: "snapshot", Data: s.fullSnapshot()}); err != nil {
		return
	}

	// A read loop is what notices the window closing; without it a dead socket
	// only shows up on the next write.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-closed:
			return
		case delta := <-updates:
			if err := writeJSON(connection, envelope{Type: "delta", Data: delta}); err != nil {
				return
			}
		case <-ping.C:
			_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func writeJSON(connection *websocket.Conn, payload any) error {
	_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return connection.WriteJSON(payload)
}

// -- helpers ------------------------------------------------------------------

func respond(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		slog.Debug("could not write a response", "error", err)
	}
}

// fail answers with a message worth showing, in the shape the desktop app
// always reads: {"detail": "..."}.
func fail(writer http.ResponseWriter, status int, detail string) {
	respond(writer, status, map[string]string{"detail": detail})
}

func decode(writer http.ResponseWriter, request *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 4<<20))
	if err := decoder.Decode(into); err != nil {
		fail(writer, http.StatusBadRequest, "could not read the request: "+err.Error())
		return false
	}
	return true
}

// only restricts a route to one method, so a typo in the desktop app produces
// a clear 405 rather than a confusing empty success.
func only(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != method {
			fail(writer, http.StatusMethodNotAllowed, "use "+method+" for this")
			return
		}
		handler(writer, request)
	}
}
