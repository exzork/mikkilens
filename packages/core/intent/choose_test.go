package intent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/intent"
)

// A sentence that matches two commands about equally used to be answered with
// "that matches more than one command" and nothing else. Now the model is
// asked which, and when it cannot tell either, she is.

// ambiguous is a sentence from a real stream, which the phrases tie between
// resuming the chat and unmuting it.
const ambiguous = "lanjutkan baca chat"

type choosingUnderstander struct {
	fakeUnderstander
	choice     string
	chooseErr  error
	candidates []string
}

func (c *choosingUnderstander) Disambiguate(
	_ context.Context, _ string, candidates []intent.Match, _ *intent.Set,
) (string, error) {
	for _, candidate := range candidates {
		c.candidates = append(c.candidates, candidate.Command)
	}
	return c.choice, c.chooseErr
}

func routerChoosing(t *testing.T, chooser *choosingUnderstander) (*intent.Router, *recordingBus, *[]call) {
	t.Helper()
	router, bus, calls := newRouter(t, time.Second)
	if chooser != nil {
		router.SetUnderstander(chooser)
	}
	return router, bus, calls
}

func TestTheSentenceReallyIsAmbiguous(t *testing.T) {
	router, _, _ := newRouter(t, time.Second)
	if _, rivals := router.Commands().Match(ambiguous); len(rivals) < 2 {
		t.Skipf("%q no longer ties; pick another sentence for these tests", ambiguous)
	}
}

func TestTheModelSettlesATie(t *testing.T) {
	chooser := &choosingUnderstander{choice: "chat_resume"}
	router, _, calls := routerChoosing(t, chooser)

	if got := router.HandleTranscript(ambiguous); got != "chat_resume" {
		t.Fatalf("ran %q, want chat_resume", got)
	}
	if len(chooser.candidates) < 2 {
		t.Errorf("the model was offered %v, want the tied commands", chooser.candidates)
	}
	if len(*calls) != 1 {
		t.Errorf("calls = %+v", *calls)
	}
}

// A model naming a command that was not in the tie is not trusted: these
// commands end broadcasts.
func TestAModelChoiceOutsideTheTieIsIgnored(t *testing.T) {
	chooser := &choosingUnderstander{choice: "stop_stream"}
	router, _, calls := routerChoosing(t, chooser)

	router.HandleTranscript(ambiguous)
	if len(*calls) != 0 {
		t.Fatalf("ran %+v, want nothing until she chooses", *calls)
	}
	if !router.AwaitingConfirmation() {
		t.Error("she must be asked which one")
	}
}

func TestWhenTheModelCannotTellSheIsAskedAndANumberPicks(t *testing.T) {
	for name, chooser := range map[string]*choosingUnderstander{
		"no model":      nil,
		"model unsure":  {choice: ""},
		"model failing": {chooseErr: errors.New("connection refused")},
	} {
		router, bus, calls := routerChoosing(t, chooser)

		router.HandleTranscript(ambiguous)
		if len(*calls) != 0 {
			t.Fatalf("%s: ran %+v before she chose", name, *calls)
		}
		if !router.AwaitingConfirmation() || !bus.saidContaining("satu") || !bus.saidContaining("dua") {
			t.Fatalf("%s: she must hear a numbered list, heard %+v", name, bus.said)
		}

		router.HandleTranscript("nomor dua")
		if len(*calls) != 1 {
			t.Fatalf("%s: calls = %+v, want the second one run", name, *calls)
		}
		if router.AwaitingConfirmation() {
			t.Errorf("%s: the question must be closed once answered", name)
		}
	}
}

func TestAChoiceCanBeCancelledOrAskedAgain(t *testing.T) {
	router, bus, calls := routerChoosing(t, nil)
	router.HandleTranscript(ambiguous)

	router.HandleTranscript("apa ya")
	if !router.AwaitingConfirmation() || !bus.saidContaining("sebut nomornya") {
		t.Fatal("an unclear answer must keep the question open and say what is wanted")
	}

	router.HandleTranscript("batal")
	if router.AwaitingConfirmation() || len(*calls) != 0 {
		t.Errorf("cancelling must close the question and run nothing, calls = %+v", *calls)
	}
}

// "Yang kedua" is an ordinal, and "pertama" is not the number word at all.
func TestOrdinalsPickToo(t *testing.T) {
	for spoken, want := range map[string]int{"yang kedua": 1, "pertama": 0, "2": 1} {
		router, _, calls := routerChoosing(t, nil)
		_, rivals := router.Commands().Match(ambiguous)
		router.HandleTranscript(ambiguous)
		router.HandleTranscript(spoken)
		if len(*calls) != 1 || (*calls)[0].command != rivals[want].Command {
			t.Fatalf("%q: calls = %+v, want %s", spoken, *calls, rivals[want].Command)
		}
	}
}
