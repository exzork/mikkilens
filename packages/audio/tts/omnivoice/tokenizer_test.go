package omnivoice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The vectors in testdata were produced by the real tokenizer -- HuggingFace
// `tokenizers`, loading the same tokenizer.json this reads -- rather than by
// this code. That is the whole point of them: a tokenizer checked against
// itself will happily agree with its own mistakes, and a mistake here is not
// heard as an error but as MikkiLens fluently saying the wrong words.
//
// They cover what actually differs between a correct byte-level BPE and a
// nearly correct one: runs of spaces, tabs, line breaks, contractions in both
// cases, digits, punctuation runs, accented Latin, CJK, an emoji outside the
// basic plane, and the special tags the prompt is built from.
type tokenizerVector struct {
	Text string  `json:"text"`
	IDs  []int32 `json:"ids"`
}

func loadTestTokenizer(t *testing.T) *Tokenizer {
	t.Helper()
	path := tokenizerPath()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("OmniVoice is not installed; %s is missing", path)
	}
	tokenizer, err := LoadTokenizer(path)
	if err != nil {
		t.Fatalf("LoadTokenizer: %v", err)
	}
	return tokenizer
}

func TestTokenizerMatchesReferenceVectors(t *testing.T) {
	tokenizer := loadTestTokenizer(t)

	raw, err := os.ReadFile(filepath.Join("testdata", "tokenizer_vectors.json"))
	if err != nil {
		t.Fatalf("reading the vectors: %v", err)
	}
	var vectors []tokenizerVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("reading the vectors: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("there are no vectors to check against")
	}

	for _, vector := range vectors {
		got := tokenizer.Encode(vector.Text)
		if len(got) != len(vector.IDs) {
			t.Errorf("Encode(%q) produced %d ids, want %d\n got: %v\nwant: %v",
				vector.Text, len(got), len(vector.IDs), got, vector.IDs)
			continue
		}
		for at := range got {
			if got[at] != vector.IDs[at] {
				t.Errorf("Encode(%q) differs at %d: got %d, want %d\n got: %v\nwant: %v",
					vector.Text, at, got[at], vector.IDs[at], got, vector.IDs)
				break
			}
		}
	}
}

// The tags are how the prompt says what it is: which language, which
// instruction, where the text begins. A build of the model without one of them
// has to fail loudly, because every one of these is load-bearing.
func TestTokenizerHasThePromptTags(t *testing.T) {
	tokenizer := loadTestTokenizer(t)

	for _, tag := range []string{
		tagDenoise, tagLanguageStart, tagLanguageEnd,
		tagInstructStart, tagInstructEnd, tagTextStart, tagTextEnd,
	} {
		if _, err := tokenizer.Special(tag); err != nil {
			t.Errorf("Special(%q): %v", tag, err)
		}
	}
	if _, err := tokenizer.Special("<|not-a-real-tag|>"); err == nil {
		t.Error("Special accepted a tag that does not exist")
	}
}

// The split pattern is the part with no library behind it, so it is worth
// checking on its own: a failure here is easier to read as "spaces are wrong"
// than as a list of differing token ids.
func TestPreTokenizeSplitsTheWayThePatternDoes(t *testing.T) {
	for _, test := range []struct {
		text string
		want []string
	}{
		// A word takes the space in front of it, which is the rule the whole
		// pattern exists to produce.
		{"halo dunia", []string{"halo", " dunia"}},
		// All but the last space of a run belongs to the run; the last one
		// goes with the word. That is `\s+(?!\S)` doing its job.
		{"a   b", []string{"a", "  ", " b"}},
		// Trailing space has no word to join, so it is taken whole.
		{"a  ", []string{"a", "  "}},
		// Digits are one piece each, however many there are.
		{"2345", []string{"2", "3", "4", "5"}},
		// Punctuation runs together and takes a leading space.
		{"wow!!! ???", []string{"wow", "!!!", " ???"}},
		// Contractions are their own piece, in either case.
		{"don't DON'T", []string{"don", "'t", " DON", "'T"}},
		// A line break takes the whitespace ahead of it.
		{"a \n b", []string{"a", " \n", " b"}},
	} {
		got := preTokenize(test.text)
		if len(got) != len(test.want) {
			t.Errorf("preTokenize(%q) = %q, want %q", test.text, got, test.want)
			continue
		}
		for at := range got {
			if got[at] != test.want[at] {
				t.Errorf("preTokenize(%q) = %q, want %q", test.text, got, test.want)
				break
			}
		}
	}
}

// Every byte has to map to a distinct printable rune, or two different inputs
// become the same token and the mistake is invisible.
func TestByteMapIsComplete(t *testing.T) {
	seen := map[rune]int{}
	for value := 0; value < 256; value++ {
		letter := byteToRune[value]
		if letter == 0 {
			t.Fatalf("byte %d maps to nothing", value)
		}
		if earlier, clash := seen[letter]; clash {
			t.Fatalf("bytes %d and %d both map to %q", earlier, value, letter)
		}
		seen[letter] = value
	}
	// A space is the one worth naming: it is the character that appears in
	// half the vocabulary, and it has to come out as U+0120.
	if byteToRune[' '] != 'Ġ' {
		t.Errorf("a space maps to %q, want %q", byteToRune[' '], 'Ġ')
	}
}
