package intent_test

import (
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/intent"
)

// Being asked a question and then having the microphone switched off is the
// worst shape this can take: she answers into nothing, hears nothing back, and
// cannot see why. These pin the pieces the engine relies on to keep listening
// through a confirmation.

// ask puts a real confirming command into its pending state.
func ask(t *testing.T, timeout time.Duration) (*intent.Router, *recordingBus, *[]call) {
	t.Helper()
	router, bus, calls := newRouter(t, timeout)

	router.HandleTranscript("hentikan siaran")
	if !router.AwaitingConfirmation() {
		t.Fatal("precondition: stop_stream should have asked for confirmation")
	}
	return router, bus, calls
}

func TestAConfirmationStaysOpenAfterTheQuestionIsAsked(t *testing.T) {
	router, _, calls := ask(t, 8*time.Second)

	if !router.AwaitingConfirmation() {
		t.Error("the question must stay open, or there is nothing to answer")
	}
	if len(*calls) != 0 {
		t.Error("the command must not run before she answers")
	}
}

// The clock is meant to measure how long she has to answer, not how long the
// question took to speak. Without renewing, a long prompt could lapse before
// she was ever given a chance to reply -- MikkiLens asking something and then
// refusing to listen.
func TestTheAnswerClockRestartsOnceTheQuestionHasBeenAsked(t *testing.T) {
	router, _, _ := ask(t, 60*time.Millisecond)

	time.Sleep(90 * time.Millisecond) // as if speaking the prompt took this long
	if router.AwaitingConfirmation() {
		t.Fatal("precondition: the question should have lapsed by now")
	}

	router.RenewPending()

	if !router.AwaitingConfirmation() {
		t.Error("renewing must give her the full time to answer, measured " +
			"from when the question finished being asked")
	}
}

func TestRenewingDoesNothingWhenNoQuestionIsOpen(t *testing.T) {
	router, _, _ := newRouter(t, time.Second)

	router.RenewPending() // must not invent a question out of nothing

	if router.AwaitingConfirmation() {
		t.Error("renewing with nothing pending must not open a question")
	}
}

// Giving up has to be audible. A question left in silence is one she may well
// read as "it happened".
func TestGivingUpOnAnUnansweredQuestionSaysSo(t *testing.T) {
	router, bus, calls := ask(t, 8*time.Second)
	bus.said = nil

	router.TimeOutPending()

	if router.AwaitingConfirmation() {
		t.Error("the question must be closed")
	}
	if len(*calls) != 0 {
		t.Error("an unanswered question must never run the command")
	}
	if len(bus.said) == 0 {
		t.Fatal("giving up in silence leaves her thinking it happened")
	}
}

func TestGivingUpTwiceOnlySpeaksOnce(t *testing.T) {
	router, bus, _ := ask(t, 8*time.Second)

	router.TimeOutPending()
	bus.said = nil
	router.TimeOutPending()

	if len(bus.said) != 0 {
		t.Errorf("spoke again with nothing pending: %v", bus.said)
	}
}

// An unclear answer keeps the question open on purpose: it must not be read as
// "no", and certainly not as "yes". That is what lets the engine ask again
// rather than guessing.
func TestAnUnclearAnswerKeepsTheQuestionOpenToAskAgain(t *testing.T) {
	router, _, calls := ask(t, 8*time.Second)

	router.HandleTranscript("mungkin nanti saja")

	if !router.AwaitingConfirmation() {
		t.Error("an unclear answer must leave the question open")
	}
	if len(*calls) != 0 {
		t.Error("an unclear answer must never be taken as yes")
	}
}

func TestAnsweringYesRunsTheCommandAndClosesTheQuestion(t *testing.T) {
	router, _, calls := ask(t, 8*time.Second)

	router.HandleTranscript("ya")

	if router.AwaitingConfirmation() {
		t.Error("an answered question must close")
	}
	if len(*calls) != 1 {
		t.Fatalf("ran %d commands, want the confirmed one", len(*calls))
	}
	if (*calls)[0].command != "stop_stream" {
		t.Errorf("ran %q", (*calls)[0].command)
	}
}

func TestAnsweringNoClosesTheQuestionWithoutRunningAnything(t *testing.T) {
	router, _, calls := ask(t, 8*time.Second)

	router.HandleTranscript("tidak")

	if router.AwaitingConfirmation() {
		t.Error("a refused question must close")
	}
	if len(*calls) != 0 {
		t.Error("no must not run the command")
	}
}
