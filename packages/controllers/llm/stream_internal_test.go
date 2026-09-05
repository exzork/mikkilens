package llm

import "testing"

// Where a streamed reply may be cut and handed to the speech bus.
//
// The bug worth keeping caught: the splitter runs over a buffer that is still
// filling, so "the end of what has arrived" is not "the end of the sentence".
// A reply saying "pukul 09.41" arrived in chunks, one of which happened to end
// just after "09.", and it was read aloud as "pukul 09." and then "41" -- two
// sentences, two wrong numbers, from a model that had answered correctly.

func TestASentenceIsOnlyCutWhereOneReallyEnds(t *testing.T) {
	cases := []struct {
		name     string
		buffered string
		sentence string
		rest     string
	}{
		// The separator is consumed with the sentence, so what is left starts
		// at the next word rather than at a space.
		{"a finished sentence", "Sekarang jam 14:35. Sisanya",
			"Sekarang jam 14:35.", "Sisanya"},
		{"nothing finished yet", "Sekarang jam 14", "", "Sekarang jam 14"},
		{"a chunk ending on a full stop waits",
			"Ya, masih sempat, karena sekarang pukul 09.", "",
			"Ya, masih sempat, karena sekarang pukul 09."},
		{"a decimal is not an ending",
			"Selesai sekitar pukul 10.21 dan itu cukup. Lalu",
			"Selesai sekitar pukul 10.21 dan itu cukup.", "Lalu"},
		{"a question mark ends one",
			"Apakah masih sempat? Iya", "Apakah masih sempat?", "Iya"},
		{"a quote after the stop comes too",
			`Dia bilang "sudah." Lalu pergi`, `Dia bilang "sudah."`, "Lalu pergi"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			sentence, rest := splitSentence(test.buffered)
			if sentence != test.sentence {
				t.Errorf("sentence = %q, want %q", sentence, test.sentence)
			}
			if rest != test.rest {
				t.Errorf("rest = %q, want %q", rest, test.rest)
			}
		})
	}
}

// Feeding a reply in one character at a time is the worst case the streaming
// path has to survive, and the one that found the bug above.
func TestAReplyArrivingOneCharacterAtATimeIsNotMangled(t *testing.T) {
	whole := "Ya, masih sempat, karena sekarang pukul 09.41 dan game 40 menit " +
		"selesai pukul 10.21. Jauh sebelum pukul 12.00."

	buffered := ""
	spoken := []string{}
	for _, char := range whole {
		buffered += string(char)
		for {
			sentence, rest := splitSentence(buffered)
			if sentence == "" {
				break
			}
			buffered = rest
			spoken = append(spoken, sentence)
		}
	}
	if tail := trimSpaceLocal(buffered); tail != "" {
		spoken = append(spoken, tail)
	}

	rejoined := ""
	for index, sentence := range spoken {
		if index > 0 {
			rejoined += " "
		}
		rejoined += sentence
	}
	if rejoined != whole {
		t.Errorf("reassembled to %q, want %q", rejoined, whole)
	}
	for _, sentence := range spoken {
		for _, broken := range []string{"09.", "10.", "12."} {
			if sentence == broken || endsWithLocal(sentence, " "+broken) {
				t.Errorf("a number was split: %q", sentence)
			}
		}
	}
}

func trimSpaceLocal(value string) string {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\n') {
		value = value[1:]
	}
	for len(value) > 0 && (value[len(value)-1] == ' ' || value[len(value)-1] == '\n') {
		value = value[:len(value)-1]
	}
	return value
}

func endsWithLocal(value, suffix string) bool {
	return len(value) >= len(suffix) && value[len(value)-len(suffix):] == suffix
}
