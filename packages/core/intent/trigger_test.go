package intent_test

import (
	"testing"
	"time"
)

// A key on a Stream Deck, a mouse macro and the command line all reach a
// command through Trigger. What matters is that they are indistinguishable
// from speech from there on: the same handler runs, the gate still holds, and
// nothing happens in silence.

func TestTriggerRunsACommandNobodySaid(t *testing.T) {
	router, _, calls := newRouter(t, time.Second)

	if got := router.Trigger("go_live", true); got != "go_live" {
		t.Fatalf("Trigger returned %q, want go_live", got)
	}
	if len(*calls) != 1 || (*calls)[0].command != "go_live" {
		t.Fatalf("calls = %v, want one go_live", *calls)
	}
}

func TestTriggerStillAsksBeforeStoppingTheStream(t *testing.T) {
	router, _, calls := newRouter(t, time.Second)

	router.Trigger("stop_stream", true)
	if len(*calls) != 0 {
		t.Fatalf("stop_stream ran without being confirmed: %v", *calls)
	}
	if !router.AwaitingConfirmation() {
		t.Fatal("a key that stops the stream must ask first")
	}

	// She answers the question out loud, exactly as she would have if she had
	// asked for it out loud.
	if got := router.HandleTranscript("ya"); got != "stop_stream" {
		t.Fatalf("answering ran %q, want stop_stream", got)
	}
	if len(*calls) != 1 || (*calls)[0].command != "stop_stream" {
		t.Fatalf("calls = %v, want one stop_stream", *calls)
	}
}

func TestABindingCanWaiveTheQuestion(t *testing.T) {
	router, _, calls := newRouter(t, time.Second)

	// A dedicated key is a deliberate act in a way a misheard sentence is not,
	// so a binding is allowed to turn the gate off.
	if got := router.Trigger("stop_stream", false); got != "stop_stream" {
		t.Fatalf("Trigger returned %q, want stop_stream", got)
	}
	if router.AwaitingConfirmation() {
		t.Error("nothing should be left waiting for an answer")
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %v, want one stop_stream", *calls)
	}
}

func TestTriggeringAnUnknownCommandIsSaidAloud(t *testing.T) {
	router, bus, calls := newRouter(t, time.Second)

	router.Trigger("make_coffee", true)
	if len(*calls) != 0 {
		t.Fatalf("calls = %v, want nothing", *calls)
	}
	// A key that does nothing and says nothing is indistinguishable from a
	// broken key, and the config file it came from cannot be glanced at.
	if len(bus.said) == 0 {
		t.Fatal("an unknown command must be reported out loud")
	}
}
