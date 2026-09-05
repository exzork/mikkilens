// Package state is the single source of truth for "what is true right now".
//
// The spoken status command and the desktop app read the same store, so they
// can never disagree with each other. Writes notify subscribers with just the
// delta, which is how the desktop app stays current over its WebSocket without
// polling for it.
package state

import (
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"
)

// ChatReading is what the chat reader is doing.
type ChatReading string

const (
	ChatPlaying ChatReading = "playing"
	ChatPaused  ChatReading = "paused"
	ChatStopped ChatReading = "stopped"
)

// Health is deliberately three-valued rather than a boolean.
//
// Unknown is not the same as Disconnected: "I have not checked yet" must never
// be announced as "it is broken".
type Health string

const (
	Unknown      Health = "unknown"
	Connected    Health = "connected"
	Disconnected Health = "disconnected"
	Errored      Health = "error"
)

// App is everything the rest of MikkiLens needs to agree on. The json tags are
// the wire names the desktop app sees, and the names Update accepts.
type App struct {
	OBS     Health `json:"obs"`
	YouTube Health `json:"youtube"`
	Chat    Health `json:"chat"`
	Vision  Health `json:"vision"`

	Streaming    bool     `json:"streaming"`
	CurrentScene string   `json:"current_scene"`
	Scenes       []string `json:"scenes"`
	MicMuted     bool     `json:"mic_muted"`

	BroadcastTitle string `json:"broadcast_title"`
	ViewerCount    int    `json:"viewer_count"`

	ChatReading ChatReading `json:"chat_reading"`
	ChatBacklog int         `json:"chat_backlog"`

	// ChatMuted is the mute key, not the reader. Muted chat is still being
	// read -- collected, counted, queued -- and simply not spoken, so this and
	// ChatReading are separately true and both worth showing.
	ChatMuted bool `json:"chat_muted"`

	// NowPlaying is the song coming out of the speakers, empty for none.
	NowPlaying string `json:"now_playing"`

	Listening      bool    `json:"listening"`
	LastTranscript string  `json:"last_transcript"`
	LastCommand    string  `json:"last_command"`
	LastError      string  `json:"last_error"`
	StartedAt      float64 `json:"started_at"`
}

// NewApp is the state of a MikkiLens that has just started up.
func NewApp() App {
	return App{
		OBS: Unknown, YouTube: Unknown, Chat: Unknown, Vision: Unknown,
		Scenes:      []string{},
		ChatReading: ChatStopped,
		StartedAt:   float64(time.Now().UnixNano()) / 1e9,
	}
}

// Changes is a set of field updates, keyed by the json names in App.
type Changes map[string]any

// Listener is called with only what actually changed.
type Listener func(Changes)

// Store is a concurrency-safe App with change notification.
type Store struct {
	mu        sync.RWMutex
	app       App
	listeners map[int]Listener
	nextID    int
}

// New builds a store around a starting state.
func New(initial ...App) *Store {
	app := NewApp()
	if len(initial) == 1 {
		app = initial[0]
	}
	return &Store{app: app, listeners: map[int]Listener{}}
}

// Subscribe registers a listener and returns the function that removes it.
func (s *Store) Subscribe(listener Listener) func() {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.listeners[id] = listener
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

// Snapshot is the whole state, ready to be sent as JSON.
func (s *Store) Snapshot() Changes {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return toChanges(s.app)
}

// App returns a copy of the current state.
func (s *Store) App() App {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.app
}

// Get reads one field by its json name.
func (s *Store) Get(name string) any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, _ := fieldByJSONName(reflect.ValueOf(&s.app).Elem(), name)
	if !value.IsValid() {
		return nil
	}
	return value.Interface()
}

// Update applies changes and notifies listeners, returning what actually
// changed. An unknown field is logged rather than silently accepted; use
// UpdateChecked when the caller wants to handle that itself.
func (s *Store) Update(changes Changes) Changes {
	applied, err := s.UpdateChecked(changes)
	if err != nil {
		slog.Error("state update rejected", "error", err)
	}
	return applied
}

// UpdateChecked is Update, but it reports an unknown field as an error.
func (s *Store) UpdateChecked(changes Changes) (Changes, error) {
	applied := Changes{}
	var failure error

	s.mu.Lock()
	target := reflect.ValueOf(&s.app).Elem()
	for name, incoming := range changes {
		field, ok := fieldByJSONName(target, name)
		if !ok {
			failure = fmt.Errorf("unknown state field %q", name)
			continue
		}
		converted, ok := convert(incoming, field.Type())
		if !ok {
			failure = fmt.Errorf("cannot store %T in state field %q", incoming, name)
			continue
		}
		if reflect.DeepEqual(field.Interface(), converted.Interface()) {
			continue
		}
		field.Set(converted)
		applied[name] = converted.Interface()
	}
	listeners := make([]Listener, 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()

	if len(applied) == 0 {
		return applied, failure
	}
	for _, listener := range listeners {
		notify(listener, applied)
	}
	return applied, failure
}

// notify keeps one broken listener from taking the state store down with it.
func notify(listener Listener, applied Changes) {
	defer func() {
		if problem := recover(); problem != nil {
			slog.Error("state listener panicked", "panic", problem)
		}
	}()
	listener(applied)
}

func toChanges(app App) Changes {
	value := reflect.ValueOf(app)
	outer := value.Type()
	changes := make(Changes, outer.NumField())
	for i := 0; i < outer.NumField(); i++ {
		name := outer.Field(i).Tag.Get("json")
		if name == "" {
			continue
		}
		changes[name] = value.Field(i).Interface()
	}
	return changes
}

func fieldByJSONName(target reflect.Value, name string) (reflect.Value, bool) {
	outer := target.Type()
	for i := 0; i < outer.NumField(); i++ {
		if outer.Field(i).Tag.Get("json") == name {
			return target.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// convert coerces a value into a field's type, which matters because JSON
// arriving from the desktop app carries every number as a float64.
func convert(value any, want reflect.Type) (reflect.Value, bool) {
	if value == nil {
		return reflect.Zero(want), true
	}
	incoming := reflect.ValueOf(value)
	if incoming.Type() == want {
		return incoming, true
	}
	if incoming.Type().ConvertibleTo(want) &&
		incoming.Kind() != reflect.Map && incoming.Kind() != reflect.Slice {
		return incoming.Convert(want), true
	}
	if want.Kind() == reflect.Slice && incoming.Kind() == reflect.Slice {
		converted := reflect.MakeSlice(want, incoming.Len(), incoming.Len())
		for i := 0; i < incoming.Len(); i++ {
			element, ok := convert(incoming.Index(i).Interface(), want.Elem())
			if !ok {
				return reflect.Value{}, false
			}
			converted.Index(i).Set(element)
		}
		return converted, true
	}
	return reflect.Value{}, false
}
