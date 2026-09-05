package intent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/intent"
)

// The fallback runs only where MikkiLens used to give up, and everything it
// says is checked before it is acted on. A model asked to pick from a list
// will happily invent an item that was not on it, and these commands end
// broadcasts.

type fakeUnderstander struct {
	command  string
	slots    map[string]string
	answered bool
	err      error
	asked    []string
}

func (f *fakeUnderstander) Understand(
	_ context.Context, transcript string, _ *intent.Set,
) (intent.Resolution, error) {
	f.asked = append(f.asked, transcript)
	return intent.Resolution{
		Command: f.command, Slots: f.slots, Answered: f.answered,
	}, f.err
}

func withUnderstander(t *testing.T, fake *fakeUnderstander) (*intent.Router, *recordingBus, *[]call) {
	t.Helper()
	router, bus, calls := newRouter(t, time.Second)
	router.SetUnderstander(fake)
	return router, bus, calls
}

// The whole point: something the phrases do not match now gets a second look.
func TestSomethingThePhrasesMissIsPassedToTheFallback(t *testing.T) {
	fake := &fakeUnderstander{command: "mute_mic"}
	router, _, calls := withUnderstander(t, fake)

	router.HandleTranscript("tolong bisukan suaranya dulu ya")

	if len(fake.asked) != 1 {
		t.Fatalf("the fallback was asked %d times, want once", len(fake.asked))
	}
	if len(*calls) != 1 || (*calls)[0].command != "mute_mic" {
		t.Fatalf("ran %v, want mute_mic", *calls)
	}
}

// And the corollary: a command the phrases already handle must never pay for
// the fallback, which is the entire reason it sits behind them.
func TestAPhraseThatAlreadyMatchesNeverReachesTheFallback(t *testing.T) {
	fake := &fakeUnderstander{command: "stop_stream"}
	router, _, calls := withUnderstander(t, fake)

	router.HandleTranscript("matikan mikrofon")

	if len(fake.asked) != 0 {
		t.Errorf("the fallback was consulted for a phrase that already matched: %v",
			fake.asked)
	}
	if len(*calls) != 1 || (*calls)[0].command != "mute_mic" {
		t.Errorf("ran %v, want mute_mic from the phrases", *calls)
	}
}

// A model told to choose will choose. An id that does not exist has to come to
// nothing rather than to a command nobody wrote.
func TestAnInventedCommandIsRefused(t *testing.T) {
	fake := &fakeUnderstander{command: "delete_everything"}
	router, bus, calls := withUnderstander(t, fake)

	router.HandleTranscript("sesuatu yang aneh sekali")

	if len(*calls) != 0 {
		t.Fatalf("ran %v; an invented command must never run", *calls)
	}
	if !bus.saidContaining("tidak") && !bus.saidContaining("belum") {
		t.Errorf("she must still be told it was not understood, said %v", bus.said)
	}
}

// Refusing is a valid answer, and must land on the ordinary "I did not
// understand" rather than on silence.
func TestTheFallbackIsAllowedToRecogniseNothing(t *testing.T) {
	fake := &fakeUnderstander{command: ""}
	router, bus, calls := withUnderstander(t, fake)

	router.HandleTranscript("sesuatu yang aneh sekali")

	if len(*calls) != 0 {
		t.Errorf("ran %v after the fallback refused", *calls)
	}
	if len(bus.said) == 0 {
		t.Error("a refusal must still be spoken, not left as silence")
	}
}

// A local model that is down, wedged or unreachable must degrade to exactly
// the behaviour MikkiLens had before it existed.
func TestAFailingFallbackDegradesToTheOldAnswer(t *testing.T) {
	fake := &fakeUnderstander{err: errors.New("connection refused")}
	router, bus, calls := withUnderstander(t, fake)

	router.HandleTranscript("sesuatu yang aneh sekali")

	if len(*calls) != 0 {
		t.Errorf("ran %v despite the matcher failing", *calls)
	}
	if len(bus.said) == 0 {
		t.Error("she must still hear that it was not understood")
	}
	// The model's own failure is not worth explaining mid-stream.
	if bus.saidContaining("connection refused") {
		t.Error("a provider error must not be read out as if it meant something")
	}
}

// Slots the handlers do not understand must be dropped rather than passed on.
func TestOnlyKnownSlotsSurvive(t *testing.T) {
	fake := &fakeUnderstander{
		command: "set_title",
		slots: map[string]string{
			"text":     "main minecraft",
			"nonsense": "dibuang",
			"scene":    "  ", // blank once trimmed
		},
	}
	router, _, calls := withUnderstander(t, fake)

	router.HandleTranscript("judulnya ganti dong jadi main minecraft")
	// set_title asks first, so the slots have to survive the confirmation too.
	router.HandleTranscript("ya")

	if len(*calls) == 0 {
		t.Fatal("set_title should have run once confirmed")
	}
	slots := (*calls)[0].slots
	if slots["text"] != "main minecraft" {
		t.Errorf("text slot is %q", slots["text"])
	}
	if _, present := slots["nonsense"]; present {
		t.Error("a slot no handler understands must be dropped")
	}
	if _, present := slots["scene"]; present {
		t.Error("a blank slot must be dropped rather than passed on empty")
	}
}

// Without a model configured, nothing about the old behaviour changes.
func TestWithNoFallbackTheOldBehaviourIsUnchanged(t *testing.T) {
	router, bus, calls := newRouter(t, time.Second)
	router.SetUnderstander(nil)

	router.HandleTranscript("sesuatu yang aneh sekali")

	if len(*calls) != 0 {
		t.Errorf("ran %v", *calls)
	}
	if len(bus.said) == 0 {
		t.Error("she must be told it was not understood")
	}
}

// A confirmed command reached through the fallback must still ask first. This
// is the path with the least certainty behind it, so it is the last place to
// skip the question.
func TestAConfirmedCommandStillAsksWhenItCameFromTheFallback(t *testing.T) {
	fake := &fakeUnderstander{command: "stop_stream"}
	router, _, calls := withUnderstander(t, fake)

	router.HandleTranscript("sudahi saja dulu ya siarannya")

	if len(*calls) != 0 {
		t.Fatal("stop_stream must not run before she confirms")
	}
	if !router.AwaitingConfirmation() {
		t.Fatal("it must ask, exactly as it would from a written phrase")
	}

	router.HandleTranscript("ya")
	if len(*calls) != 1 || (*calls)[0].command != "stop_stream" {
		t.Errorf("ran %v after confirming", *calls)
	}
}

// A fallback that has already answered must not then have the command run on
// top of it. For a question like "berapa menit lagi sampai jam 12" the command
// has been run once already, to get the time; dispatching would run it again
// and read the bare time out over an answer she is still listening to.
func TestAnAnsweredResolutionRunsNothingAndSaysNothingMore(t *testing.T) {
	fake := &fakeUnderstander{answered: true}
	router, bus, calls := withUnderstander(t, fake)

	router.HandleTranscript("berapa menit lagi sampai jam 12")

	if len(*calls) != 0 {
		t.Errorf("ran %v; an answered resolution must dispatch nothing", *calls)
	}
	if len(bus.said) != 0 {
		t.Errorf("said %v on top of an answer already given", bus.said)
	}
}

// Answered wins over any command it also carries, so a fallback that answers
// cannot accidentally act as well.
func TestAnsweredWinsOverACommand(t *testing.T) {
	fake := &fakeUnderstander{command: "mute_mic", answered: true}
	router, _, calls := withUnderstander(t, fake)

	router.HandleTranscript("berapa menit lagi sampai jam 12")

	if len(*calls) != 0 {
		t.Errorf("ran %v despite having answered", *calls)
	}
}
