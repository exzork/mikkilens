package feedback_test

import (
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/feedback"
)

// Calling off a burst of speech that has been overtaken.
//
// Reading five search results is seven utterances, and the moment she picks
// one the other six are wrong: she is not waiting to hear options four and
// five over the song she just started. What has to happen is that the voice
// stops mid-word, and that nothing else waiting to be said goes with it.

func queueGroup(bus *feedback.Bus, group string, texts ...string) {
	for _, text := range texts {
		bus.Enqueue(feedback.Utterance{
			Text: text, Priority: feedback.Result, Group: group,
		})
	}
}

func TestClearGroupDropsOnlyThatGroup(t *testing.T) {
	bus, player := newBus(t, 0.02)

	queueGroup(bus, "results", "one", "two", "three")
	bus.Say("something else", feedback.Result)

	if dropped := bus.ClearGroup("results"); dropped != 3 {
		t.Fatalf("dropped %d, want 3", dropped)
	}

	bus.Start()
	drain(t, bus)

	played := player.playedTexts()
	if !contains(played, "something else") {
		t.Errorf("an unrelated answer was dropped with the group: %v", played)
	}
	for _, gone := range []string{"one", "two", "three"} {
		if contains(played, gone) {
			t.Errorf("%q was read after its group was called off: %v", gone, played)
		}
	}
}

// The interruption is the point rather than a side effect: she presses 2 while
// result three is being read.
func TestClearGroupCutsOffTheMemberBeingSpoken(t *testing.T) {
	bus, player := newBus(t, 0.5)
	bus.Start()

	queueGroup(bus, "results", "result three", "result four", "result five")
	player.waitForStart(t)

	bus.ClearGroup("results")

	eventually(t, 2*time.Second, "the reading to be cut off", func() bool {
		return contains(player.interruptedTexts(), "result three")
	})

	// And it stays gone. Every other interruption in this application
	// postpones rather than drops; a result she has already chosen past is the
	// one thing that must not come back.
	time.Sleep(300 * time.Millisecond)
	for _, gone := range []string{"result three", "result four", "result five"} {
		if contains(player.playedTexts(), gone) {
			t.Errorf("%q was read after being called off", gone)
		}
	}
}

// Cancelling a reading that has already finished has to be free, because the
// caller does it unconditionally rather than trying to work out whether the
// voice is still going.
func TestClearGroupOnNothingIsHarmless(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.Say("an answer", feedback.Result)
	drain(t, bus)

	if dropped := bus.ClearGroup("results"); dropped != 0 {
		t.Errorf("dropped %d from an empty queue", dropped)
	}
	if dropped := bus.ClearGroup(""); dropped != 0 {
		t.Errorf("an empty group name dropped %d", dropped)
	}
	if !contains(player.playedTexts(), "an answer") {
		t.Error("the answer was lost")
	}
}

// An error still has to reach her while a group is being read, and must not be
// swept up by the cancelling that follows.
func TestAnErrorSurvivesTheGroupBeingCalledOff(t *testing.T) {
	bus, player := newBus(t, 0.02)

	queueGroup(bus, "results", "result one", "result two")
	bus.Error("obs.not_responding")
	bus.ClearGroup("results")

	bus.Start()
	drain(t, bus)

	if !contains(player.playedTexts(), bus.Locale().T("obs.not_responding")) {
		t.Errorf("the error went with the group: %v", player.playedTexts())
	}
}

func TestSpeakingGroupReportsWhatIsBeingRead(t *testing.T) {
	bus, player := newBus(t, 0.5)
	bus.Start()

	if group := bus.SpeakingGroup(); group != "" {
		t.Fatalf("a silent bus reported group %q", group)
	}

	queueGroup(bus, "results", "result one")
	player.waitForStart(t)

	if group := bus.SpeakingGroup(); group != "results" {
		t.Errorf("speaking group = %q, want results", group)
	}
}
