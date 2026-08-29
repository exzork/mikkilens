package intent_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// The behaviour that matters here is that nothing happens silently: every path
// through the router produces speech, and destructive commands wait for a
// spoken yes in her own language.

type spoken struct {
	priority string
	text     string
}

// recordingBus stands in for the speech bus, capturing what would have been said.
type recordingBus struct{ said []spoken }

func (b *recordingBus) Say(text string, priority intent.Priority) {
	b.said = append(b.said, spoken{priority.String(), text})
}

func (b *recordingBus) SayEarcon(text string, priority intent.Priority, _ string) {
	b.Say(text, priority)
}

func (b *recordingBus) saidContaining(needle string) bool {
	for _, entry := range b.said {
		if strings.Contains(strings.ToLower(entry.text), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

type call struct {
	command string
	slots   map[string]string
}

func newRouter(t *testing.T, timeout time.Duration) (*intent.Router, *recordingBus, *[]call) {
	t.Helper()
	set, err := intent.SetFromFile(paths.CommandsFile("id"))
	if err != nil {
		t.Fatalf("could not load commands: %v", err)
	}
	locale := i18n.Load("id")
	bus := &recordingBus{}
	router := intent.NewRouter(set, bus, locale, timeout)

	calls := &[]call{}
	for _, id := range set.Order {
		command := id
		router.Register(command, func(slots map[string]string) error {
			*calls = append(*calls, call{command, slots})
			return nil
		})
	}
	return router, bus, calls
}

// -- plain dispatch -----------------------------------------------------------

func TestASimpleCommandRunsImmediately(t *testing.T) {
	router, _, calls := newRouter(t, 300*time.Millisecond)
	if got := router.HandleTranscript("matikan mikrofon"); got != "mute_mic" {
		t.Fatalf("ran %q, want mute_mic", got)
	}
	if len(*calls) != 1 || (*calls)[0].command != "mute_mic" {
		t.Errorf("calls = %+v", *calls)
	}
}

func TestSlotsReachTheHandler(t *testing.T) {
	router, _, calls := newRouter(t, 300*time.Millisecond)
	router.HandleTranscript("ganti ke just chatting")
	if len(*calls) != 1 || (*calls)[0].slots["scene"] != "just chatting" {
		t.Errorf("calls = %+v", *calls)
	}
}

func TestAnUnknownCommandIsSpokenBackWithWhatWasHeard(t *testing.T) {
	router, bus, calls := newRouter(t, 300*time.Millisecond)
	if got := router.HandleTranscript("tolong buatkan saya kopi"); got != "" {
		t.Errorf("ran %q, want nothing", got)
	}
	if len(*calls) != 0 {
		t.Errorf("nothing should have run, got %+v", *calls)
	}
	if !bus.saidContaining("kopi") {
		t.Error("she must hear what it thought she said")
	}
}

func TestSilenceIsReportedRatherThanIgnored(t *testing.T) {
	router, bus, calls := newRouter(t, 300*time.Millisecond)
	if got := router.HandleTranscript("   "); got != "" {
		t.Errorf("ran %q, want nothing", got)
	}
	if len(*calls) != 0 {
		t.Errorf("nothing should have run, got %+v", *calls)
	}
	if len(bus.said) == 0 {
		t.Error("even empty input must produce feedback")
	}
}

func TestAHandlerThatFailsIsReportedAloud(t *testing.T) {
	set, err := intent.SetFromFile(paths.CommandsFile("id"))
	if err != nil {
		t.Fatal(err)
	}
	bus := &recordingBus{}
	router := intent.NewRouter(set, bus, i18n.Load("id"), 0)
	router.Register("mute_mic", func(map[string]string) error {
		return errors.New("OBS is on fire")
	})

	if got := router.HandleTranscript("matikan mikrofon"); got != "" {
		t.Errorf("ran %q, want nothing", got)
	}
	if !bus.saidContaining("OBS is on fire") {
		t.Error("the failure must be spoken")
	}
	if last := bus.said[len(bus.said)-1]; last.priority != "ERROR" {
		t.Errorf("last utterance was %s, want ERROR", last.priority)
	}
}

func TestAHandlerThatPanicsIsReportedRatherThanCrashing(t *testing.T) {
	set, err := intent.SetFromFile(paths.CommandsFile("id"))
	if err != nil {
		t.Fatal(err)
	}
	bus := &recordingBus{}
	router := intent.NewRouter(set, bus, i18n.Load("id"), 0)
	router.Register("mute_mic", func(map[string]string) error {
		panic("the microphone vanished")
	})

	if got := router.HandleTranscript("matikan mikrofon"); got != "" {
		t.Errorf("ran %q, want nothing", got)
	}
	if !bus.saidContaining("the microphone vanished") {
		t.Errorf("the panic must be spoken, said %+v", bus.said)
	}
}

func TestACommandWithNoHandlerIsReportedRatherThanSilent(t *testing.T) {
	set, err := intent.SetFromFile(paths.CommandsFile("id"))
	if err != nil {
		t.Fatal(err)
	}
	bus := &recordingBus{}
	router := intent.NewRouter(set, bus, i18n.Load("id"), 0) // nothing registered

	if got := router.HandleTranscript("matikan mikrofon"); got != "" {
		t.Errorf("ran %q, want nothing", got)
	}
	if len(bus.said) == 0 || bus.said[len(bus.said)-1].priority != "ERROR" {
		t.Errorf("said = %+v", bus.said)
	}
}

// -- confirmation -------------------------------------------------------------

func TestADestructiveCommandAsksBeforeActing(t *testing.T) {
	router, bus, calls := newRouter(t, 300*time.Millisecond)
	if got := router.HandleTranscript("hentikan siaran"); got != "" {
		t.Errorf("ran %q before she answered", got)
	}
	if len(*calls) != 0 {
		t.Errorf("must not act before she answers, got %+v", *calls)
	}
	if !router.AwaitingConfirmation() {
		t.Error("the question must stay open")
	}
	if !bus.saidContaining("Hentikan siaran?") {
		t.Errorf("said = %+v", bus.said)
	}
}

func TestYesInIndonesianConfirms(t *testing.T) {
	for _, answer := range []string{"ya", "iya", "oke", "betul"} {
		router, _, calls := newRouter(t, 300*time.Millisecond)
		router.HandleTranscript("hentikan siaran")
		if got := router.HandleTranscript(answer); got != "stop_stream" {
			t.Errorf("%q ran %q, want stop_stream", answer, got)
		}
		if len(*calls) != 1 || (*calls)[0].command != "stop_stream" {
			t.Errorf("%q: calls = %+v", answer, *calls)
		}
	}
}

func TestNoInIndonesianCancels(t *testing.T) {
	for _, answer := range []string{"tidak", "nggak", "batal", "jangan"} {
		router, bus, calls := newRouter(t, 300*time.Millisecond)
		router.HandleTranscript("hentikan siaran")
		if got := router.HandleTranscript(answer); got != "" {
			t.Errorf("%q ran %q, want nothing", answer, got)
		}
		if len(*calls) != 0 {
			t.Errorf("%q: calls = %+v", answer, *calls)
		}
		if !bus.saidContaining("Dibatalkan") {
			t.Errorf("%q: said = %+v", answer, bus.said)
		}
	}
}

func TestAnUnclearAnswerKeepsAskingInsteadOfGuessing(t *testing.T) {
	router, bus, calls := newRouter(t, 3*time.Second)
	router.HandleTranscript("hentikan siaran")
	router.HandleTranscript("mungkin")
	if len(*calls) != 0 {
		t.Errorf("calls = %+v", *calls)
	}
	if !router.AwaitingConfirmation() {
		t.Error("an unclear answer must not cancel the question")
	}
	if !bus.saidContaining("ya atau tidak") {
		t.Errorf("said = %+v", bus.said)
	}
}

func TestConfirmationExpiresAndIsAnnounced(t *testing.T) {
	router, bus, calls := newRouter(t, 300*time.Millisecond)
	router.HandleTranscript("hentikan siaran")
	time.Sleep(350 * time.Millisecond)

	if router.AwaitingConfirmation() {
		t.Error("the question should have expired")
	}
	router.HandleTranscript("matikan mikrofon")
	if !bus.saidContaining("Tidak ada jawaban") {
		t.Errorf("the expiry must be announced, said = %+v", bus.said)
	}
	if len(*calls) != 1 || (*calls)[0].command != "mute_mic" {
		t.Errorf("after expiry, normal commands must work again: %+v", *calls)
	}
}

func TestConfirmationCarriesItsSlotsThrough(t *testing.T) {
	router, _, calls := newRouter(t, 3*time.Second)
	router.HandleTranscript("ganti judul jadi main valorant malam ini")
	router.HandleTranscript("ya")
	if len(*calls) != 1 || (*calls)[0].slots["text"] != "main valorant malam ini" {
		t.Errorf("calls = %+v", *calls)
	}
}

func TestTheConfirmationPromptIncludesTheNewTitle(t *testing.T) {
	router, bus, _ := newRouter(t, 3*time.Second)
	router.HandleTranscript("ganti judul jadi main valorant")
	if !bus.saidContaining("main valorant") {
		t.Errorf("said = %+v", bus.said)
	}
}

func TestANonDestructiveCommandNeverAsks(t *testing.T) {
	router, _, calls := newRouter(t, 3*time.Second)
	router.HandleTranscript("berapa penontonnya")
	if len(*calls) != 1 || (*calls)[0].command != "viewer_count" {
		t.Errorf("calls = %+v", *calls)
	}
}

func TestCancelPendingIsAnnounced(t *testing.T) {
	router, bus, _ := newRouter(t, 3*time.Second)
	router.HandleTranscript("hentikan siaran")
	router.CancelPending()
	if router.AwaitingConfirmation() {
		t.Error("the question should be gone")
	}
	if !bus.saidContaining("Dibatalkan") {
		t.Errorf("said = %+v", bus.said)
	}
}

// -- coverage -----------------------------------------------------------------

// TestEveryShippedCommandHasSomewhereToGo catches a command added to the TOML
// with no handler wired up.
func TestEveryShippedCommandHasSomewhereToGo(t *testing.T) {
	set, err := intent.SetFromFile(paths.CommandsFile("id"))
	if err != nil {
		t.Fatal(err)
	}
	router := intent.NewRouter(set, &recordingBus{}, i18n.Load("id"), 0)
	if len(router.UnhandledCommands()) != set.Len() {
		t.Errorf("with nothing registered, every command should be unhandled")
	}
	router.Register("mute_mic", func(map[string]string) error { return nil })
	for _, id := range router.UnhandledCommands() {
		if id == "mute_mic" {
			t.Error("mute_mic should now be handled")
		}
	}
}
