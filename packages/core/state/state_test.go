package state_test

import (
	"reflect"
	"testing"

	"github.com/exzork/mikkilens/packages/core/state"
)

func TestUpdateReportsOnlyActualChanges(t *testing.T) {
	store := state.New()
	if got := store.Update(state.Changes{"streaming": true}); !reflect.DeepEqual(got, state.Changes{"streaming": true}) {
		t.Errorf("first update returned %v", got)
	}
	if got := store.Update(state.Changes{"streaming": true}); len(got) != 0 {
		t.Errorf("an unchanged value must not notify, got %v", got)
	}
}

func TestListenersReceiveOnlyTheDelta(t *testing.T) {
	store := state.New()
	seen := []state.Changes{}
	store.Subscribe(func(delta state.Changes) { seen = append(seen, delta) })

	store.Update(state.Changes{"streaming": true, "viewer_count": 12})
	store.Update(state.Changes{"viewer_count": 12})

	if len(seen) != 1 {
		t.Fatalf("expected one notification, got %d: %v", len(seen), seen)
	}
	if !reflect.DeepEqual(seen[0], state.Changes{"streaming": true, "viewer_count": 12}) {
		t.Errorf("delta = %v", seen[0])
	}
}

func TestUnsubscribeStopsNotifications(t *testing.T) {
	store := state.New()
	seen := []state.Changes{}
	unsubscribe := store.Subscribe(func(delta state.Changes) { seen = append(seen, delta) })

	store.Update(state.Changes{"streaming": true})
	unsubscribe()
	store.Update(state.Changes{"streaming": false})

	if len(seen) != 1 {
		t.Errorf("expected one notification, got %v", seen)
	}
}

func TestAFailingListenerDoesNotBreakStateOrOtherListeners(t *testing.T) {
	store := state.New()
	seen := []state.Changes{}
	store.Subscribe(func(state.Changes) { panic("listener blew up") })
	store.Subscribe(func(delta state.Changes) { seen = append(seen, delta) })

	store.Update(state.Changes{"streaming": true})

	if store.Get("streaming") != true {
		t.Error("the state must still have been written")
	}
	if len(seen) != 1 {
		t.Errorf("the healthy listener must still fire, got %v", seen)
	}
}

func TestUnknownFieldIsRejectedLoudly(t *testing.T) {
	store := state.New()
	applied, err := store.UpdateChecked(state.Changes{"not_a_field": 1})
	if err == nil {
		t.Error("an unknown field must be reported")
	}
	if len(applied) != 0 {
		t.Errorf("nothing should have been applied, got %v", applied)
	}
}

func TestKnownFieldsStillApplyAlongsideAnUnknownOne(t *testing.T) {
	store := state.New()
	applied, err := store.UpdateChecked(state.Changes{"streaming": true, "not_a_field": 1})
	if err == nil {
		t.Error("the unknown field must still be reported")
	}
	if !reflect.DeepEqual(applied, state.Changes{"streaming": true}) {
		t.Errorf("applied = %v", applied)
	}
}

func TestSnapshotIsJSONFriendly(t *testing.T) {
	initial := state.NewApp()
	initial.OBS = state.Connected
	initial.ChatReading = state.ChatPaused
	snapshot := state.New(initial).Snapshot()

	if snapshot["obs"] != state.Connected {
		t.Errorf("obs = %v", snapshot["obs"])
	}
	if snapshot["chat_reading"] != state.ChatPaused {
		t.Errorf("chat_reading = %v", snapshot["chat_reading"])
	}
	if _, ok := snapshot["scenes"].([]string); !ok {
		t.Errorf("scenes should be a list, got %T", snapshot["scenes"])
	}
}

// TestHealthDistinguishesUnknownFromDisconnected: "not checked yet" must never
// be announced as "broken".
func TestHealthDistinguishesUnknownFromDisconnected(t *testing.T) {
	if state.Unknown == state.Disconnected {
		t.Error("unknown and disconnected must be different states")
	}
	if got := state.New().Get("obs"); got != state.Unknown {
		t.Errorf("a fresh store reports obs as %v, want unknown", got)
	}
}

// TestJSONNumbersLandInIntegerFields covers the desktop app, whose JSON
// carries every number as a float.
func TestJSONNumbersLandInIntegerFields(t *testing.T) {
	store := state.New()
	store.Update(state.Changes{"viewer_count": float64(42)})
	if got := store.Get("viewer_count"); got != 42 {
		t.Errorf("viewer_count = %v (%T)", got, got)
	}
}
