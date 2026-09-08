package feedback_test

import (
	"testing"

	"github.com/exzork/mikkilens/packages/audio/feedback"
)

// The "@" in front of a name is punctuation, and every engine reads it as the
// word "at". What she hears should be the name.
func TestMentionsAreSpokenWithoutTheSymbol(t *testing.T) {
	for _, testCase := range []struct{ name, typed, spoken string }{
		{"opening a message", "@Mikki halo", "Mikki halo"},
		{"mid sentence", "halo @Mikki apa kabar", "halo Mikki apa kabar"},
		{"several at once", "@Mikki @Eko lihat ini", "Mikki Eko lihat ini"},
		{"inside brackets", "(@Mikki) lihat", "(Mikki) lihat"},
		{"underscores and digits", "@mikki_lens99 hai", "mikki_lens99 hai"},
		{"a name that is not ASCII", "@Мikki привет", "Мikki привет"},

		// An address is the one place "at" is the reading somebody wants, and
		// a bare "@" is being used as a word rather than as a prefix.
		{"an email address", "kirim ke eko@gmail.com", "kirim ke eko@gmail.com"},
		{"a bare symbol", "harga @ 5000", "harga @ 5000"},
		{"nothing to do", "halo semua", "halo semua"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			bus, player := newBus(t, 0.01)
			bus.Say(testCase.typed, feedback.Result)
			bus.Start()
			drain(t, bus)

			played := player.playedTexts()
			if len(played) != 1 {
				t.Fatalf("played %d utterances, want 1", len(played))
			}
			if played[0] != testCase.spoken {
				t.Errorf("spoke %q, want %q", played[0], testCase.spoken)
			}
		})
	}
}

// What is shown and what is said part ways here, deliberately: the symbol is
// dropped on the way to the voice and nowhere else, so the record of what was
// said still matches what was typed.
func TestTheRecordKeepsTheSymbolTheVoiceDropped(t *testing.T) {
	bus, player := newBus(t, 0.01)

	bus.Say("@Mikki halo", feedback.Result)
	bus.Start()
	drain(t, bus)

	if got := player.playedTexts(); len(got) != 1 || got[0] != "Mikki halo" {
		t.Errorf("the voice got %v, want [\"Mikki halo\"]", got)
	}

	history := bus.History()
	if len(history) != 1 {
		t.Fatalf("history has %d entries, want 1", len(history))
	}
	if history[0].Text != "@Mikki halo" {
		t.Errorf("history kept %q, want the text as typed", history[0].Text)
	}
}
