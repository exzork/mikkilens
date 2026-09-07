package supertonic

import (
	"strings"
	"testing"
)

func TestTextIsWrappedInItsLanguageTag(t *testing.T) {
	got := prepare("Halo", "id")
	if want := "<id>Halo.</id>"; got != want {
		t.Errorf("prepare(...) = %q, want %q", got, want)
	}
}

// A language the model has never seen must still be read, because the
// alternative is that changing the output language stops her talking.
func TestAnUnknownLanguageIsReadAnyway(t *testing.T) {
	for _, language := range []string{"xx", "", "klingon"} {
		got := prepare("Halo", language)
		if !strings.HasPrefix(got, "<na>") {
			t.Errorf("prepare(%q) = %q, want the <na> tag", language, got)
		}
	}
}

// The rest of the application says "id-ID"; the model only knows "id".
func TestALocaleIsNarrowedToItsLanguage(t *testing.T) {
	if got := languageTag("id-ID"); got != "id" {
		t.Errorf("languageTag(\"id-ID\") = %q, want \"id\"", got)
	}
	if got := languageTag("EN-us"); got != "en" {
		t.Errorf("languageTag(\"EN-us\") = %q, want \"en\"", got)
	}
}

// Almost every phrase MikkiLens says is a bare one. Read without a final stop
// they trail off as though she had been cut off mid-word.
func TestAPhraseGetsAFinalStop(t *testing.T) {
	if got := prepare("Mikrofon dimatikan", "id"); !strings.Contains(got, "dimatikan.") {
		t.Errorf("prepare(...) = %q, want a period added", got)
	}
	// One that already ends properly must not collect a second.
	if got := prepare("Sudah live!", "id"); strings.Contains(got, "!.") {
		t.Errorf("prepare(...) = %q, want no extra period", got)
	}
	if got := prepare("Selesai?", "id"); strings.Contains(got, "?.") {
		t.Errorf("prepare(...) = %q, want no extra period", got)
	}
}

// Chat is made of emoji and the encoder has no character for any of them.
func TestEmojiAreDropped(t *testing.T) {
	got := prepare("Halo 👋 semua 🎉", "id")
	for _, unwanted := range []string{"👋", "🎉"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("prepare(...) = %q, still contains %q", got, unwanted)
		}
	}
	if !strings.Contains(got, "Halo") || !strings.Contains(got, "semua") {
		t.Errorf("prepare(...) = %q, want the words kept", got)
	}
}

func TestTypographyBecomesSomethingReadable(t *testing.T) {
	got := prepare("dia bilang “halo” — lalu pergi", "id")
	for _, unwanted := range []string{"“", "”", "—"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("prepare(...) = %q, still contains %q", got, unwanted)
		}
	}
}

func TestShortTextIsNotSplit(t *testing.T) {
	got := chunk("Halo, apa kabar?", chunkLimit("id"))
	if len(got) != 1 {
		t.Fatalf("chunk(...) = %d pieces, want 1: %q", len(got), got)
	}
}

func TestEmptyTextIsNoChunksAtAll(t *testing.T) {
	for _, text := range []string{"", "   ", "\n\n"} {
		if got := chunk(text, 300); len(got) != 0 {
			t.Errorf("chunk(%q) = %q, want nothing", text, got)
		}
	}
}

// A long donation has to come apart somewhere, and every piece has to fit --
// otherwise the split was decoration and the model still gets the long tensor.
func TestLongTextIsSplitAndEveryPieceFits(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat(
		"Terima kasih banyak atas dukungannya hari ini. ", 20))

	pieces := chunk(long, 120)
	if len(pieces) < 2 {
		t.Fatalf("chunk(...) = %d piece(s), want it split", len(pieces))
	}
	for _, piece := range pieces {
		if count(piece) > 120 {
			t.Errorf("a piece is %d characters, over the limit of 120: %q", count(piece), piece)
		}
		if strings.TrimSpace(piece) == "" {
			t.Error("a piece is empty")
		}
	}

	// Nothing may be lost. Joining the pieces back up has to give the words
	// back, whatever happened to the spacing between them.
	rejoined := strings.Join(strings.Fields(strings.Join(pieces, " ")), " ")
	if want := strings.Join(strings.Fields(long), " "); rejoined != want {
		t.Errorf("words were lost in the split:\n got %q\nwant %q", rejoined, want)
	}
}

// One sentence with no punctuation to break on still has to fit, or a wall of
// text with no full stops would go through whole.
func TestOneUnbrokenSentenceIsSplitOnWords(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("kata ", 200))
	for _, piece := range chunk(long, 100) {
		if count(piece) > 100 {
			t.Errorf("a piece is %d characters, over the limit of 100", count(piece))
		}
	}
}

func TestAbbreviationsDoNotEndASentence(t *testing.T) {
	got := sentences("Terima kasih dr. Budi atas bantuannya. Sampai jumpa.")
	if len(got) != 2 {
		t.Errorf("sentences(...) = %d pieces, want 2: %q", len(got), got)
	}
}

// Korean and Japanese write far more sound per character, so the same limit
// would be several times the audio.
func TestDenseLanguagesGetASmallerLimit(t *testing.T) {
	if chunkLimit("ja") >= chunkLimit("id") {
		t.Error("Japanese should chunk sooner than Indonesian")
	}
	if chunkLimit("ko") != chunkLimit("ja") {
		t.Error("Korean and Japanese should chunk the same way")
	}
}

func TestGenderIsReadFromThePresetNames(t *testing.T) {
	if got := genderOf("F1"); got != "Female" {
		t.Errorf("genderOf(\"F1\") = %q", got)
	}
	if got := genderOf("M5"); got != "Male" {
		t.Errorf("genderOf(\"M5\") = %q", got)
	}
	// A voice somebody built themselves is called whatever they called it.
	if got := genderOf("mikki-custom"); got != "" {
		t.Errorf("genderOf(\"mikki-custom\") = %q, want no claim", got)
	}
}

func TestIndonesianIsOneOfTheLanguages(t *testing.T) {
	if !Speaks("id") {
		t.Error("the model is supposed to speak Indonesian")
	}
	if Speaks("tlh") {
		t.Error("Speaks said yes to a language the model does not have")
	}
}
