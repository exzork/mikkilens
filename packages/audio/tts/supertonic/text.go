package supertonic

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Languages are the tags the model was trained with. "na" means "not
// applicable": it reads the characters without committing to a language, and
// is what an unrecognised tag falls back to rather than failing.
//
// The tag is not a hint. It is wrapped around the text as literal characters
// and read by the same encoder that reads the words, so it has to be one of
// these exactly.
var Languages = []string{
	"en", "ko", "ja", "ar", "bg", "cs", "da", "de", "el", "es", "et", "fi",
	"fr", "hi", "hr", "hu", "id", "it", "lt", "lv", "nl", "pl", "pt", "ro",
	"ru", "sk", "sl", "sv", "tr", "uk", "vi", "na",
}

// Speaks reports whether the model has a tag for a language.
func Speaks(language string) bool {
	for _, known := range Languages {
		if known == strings.ToLower(language) {
			return true
		}
	}
	return false
}

// languageTag settles on a tag the encoder will recognise.
//
// An unknown language becomes "na" rather than an error. Somebody who has set
// her output language to something the model has never seen should hear the
// words read plainly, not hear nothing.
func languageTag(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	// Locales arrive as "id-ID" from the rest of the application, and the
	// model only knows the language half of that.
	if base, _, found := strings.Cut(language, "-"); found {
		language = base
	}
	if Speaks(language) {
		return language
	}
	return "na"
}

var (
	// The ranges the model's own preprocessing drops. Emoji reach the voice
	// constantly -- chat is made of them -- and the encoder has no character
	// for them, so left in they are read as the unknown slot, which is a
	// glitch in the middle of a sentence.
	emoji = regexp.MustCompile(`[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}\x{1F1E6}-\x{1F1FF}\x{FE0F}\x{200D}]+`)

	spaceBeforePunctuation = regexp.MustCompile(` +([,.!?;:'])`)
	runsOfSpace            = regexp.MustCompile(`\s+`)
	endsSentence           = regexp.MustCompile(`[.!?;:,'"“”‘’)\]}…。」』】〉》›»]$`)
	paragraphBreak         = regexp.MustCompile(`\n\s*\n`)
	sentenceBreak          = regexp.MustCompile(`([.!?])\s+`)
)

// substitutions are applied in order, so the result does not depend on map
// iteration. Each turns something the encoder has no character for into
// something it does.
var substitutions = []struct{ from, to string }{
	{"–", "-"}, // en dash
	{"‑", "-"}, // non-breaking hyphen
	{"—", "-"}, // em dash
	{"_", " "},
	{"“", `"`}, {"”", `"`},
	{"‘", "'"}, {"’", "'"},
	{"´", "'"}, {"`", "'"},
	{"[", " "}, {"]", " "}, {"|", " "}, {"/", " "}, {"#", " "},
	{"→", " "}, {"←", " "},
	{"♥", ""}, {"☆", ""}, {"♡", ""}, {"©", ""}, {`\`, ""},
	{"@", " at "},
	{"e.g.,", "for example, "},
	{"i.e.,", "that is, "},
}

// prepare cleans one piece of text and wraps it in its language tag.
//
// The tag is part of the text the encoder reads, which is why this is the last
// step and why nothing downstream may touch the string again.
func prepare(text, language string) string {
	text = norm.NFKD.String(text)
	text = emoji.ReplaceAllString(text, "")

	for _, swap := range substitutions {
		text = strings.ReplaceAll(text, swap.from, swap.to)
	}

	text = spaceBeforePunctuation.ReplaceAllString(text, "$1")
	for _, doubled := range []string{`""`, "''", "``"} {
		single := doubled[:1]
		for strings.Contains(text, doubled) {
			text = strings.ReplaceAll(text, doubled, single)
		}
	}
	text = strings.TrimSpace(runsOfSpace.ReplaceAllString(text, " "))

	// A phrase with no final punctuation is read as though it were cut off:
	// the pitch stays up and the last word runs into the silence. Almost every
	// confirmation MikkiLens says is a bare phrase, so this matters more here
	// than it would in a paragraph of prose.
	if text != "" && !endsSentence.MatchString(text) {
		text += "."
	}

	tag := languageTag(language)
	return "<" + tag + ">" + text + "</" + tag + ">"
}

// chunkLimit is how much text goes through the model at once, in characters.
//
// The models take text and duration together, so one very long phrase is one
// very long tensor and one very long wait before any of it is heard. Splitting
// is what keeps a read-aloud donation starting promptly.
func chunkLimit(language string) int {
	switch languageTag(language) {
	case "ko", "ja":
		// These write far more sound per character, so the same limit would be
		// several times the audio.
		return 120
	default:
		return 300
	}
}

// abbreviations end in a period without ending a sentence.
var abbreviations = []string{
	"Dr.", "Mr.", "Mrs.", "Ms.", "Prof.", "Sr.", "Jr.", "St.", "Ave.", "Rd.",
	"Blvd.", "Dept.", "Inc.", "Ltd.", "Co.", "Corp.", "etc.", "vs.", "i.e.",
	"e.g.", "Ph.D.", "dr.", "hj.", "tgl.", "dll.", "dsb.", "yth.",
}

// chunk splits text into pieces the model can read in one go, preferring
// paragraph breaks, then sentences, then commas, then plain word boundaries.
//
// Lengths are counted in runes rather than bytes. A byte count would make the
// limit mean different things in different languages, which is the opposite of
// what a limit expressed in characters is for.
func chunk(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 {
		limit = 300
	}

	var chunks []string
	for _, paragraph := range paragraphBreak.Split(text, -1) {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if count(paragraph) <= limit {
			chunks = append(chunks, paragraph)
			continue
		}
		chunks = append(chunks, splitLong(paragraph, limit)...)
	}
	return chunks
}

func splitLong(paragraph string, limit int) []string {
	var (
		chunks  []string
		current strings.Builder
	)
	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}
	add := func(piece, separator string) {
		if current.Len() > 0 && count(current.String())+count(piece)+count(separator) > limit {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString(separator)
		}
		current.WriteString(piece)
	}

	for _, sentence := range sentences(paragraph) {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		if count(sentence) <= limit {
			add(sentence, " ")
			continue
		}
		// One sentence longer than the whole limit. Commas next, and failing
		// those, words -- at which point it fits by construction.
		flush()
		for _, clause := range strings.Split(sentence, ",") {
			clause = strings.TrimSpace(clause)
			if clause == "" {
				continue
			}
			if count(clause) <= limit {
				add(clause, ", ")
				continue
			}
			flush()
			for _, word := range strings.Fields(clause) {
				add(word, " ")
			}
			flush()
		}
		flush()
	}
	flush()
	return chunks
}

// sentences splits on terminal punctuation, keeping abbreviations whole.
//
// Go's regexp has no lookbehind, so the split is done first and the pieces
// that turned out to end in "Dr." or "dll." are joined back on.
func sentences(text string) []string {
	breaks := sentenceBreak.FindAllStringIndex(text, -1)
	if len(breaks) == 0 {
		return []string{text}
	}

	var (
		found []string
		start int
	)
	for _, where := range breaks {
		if isAbbreviation(strings.TrimSpace(text[start:where[1]])) {
			continue
		}
		found = append(found, text[start:where[1]])
		start = where[1]
	}
	if start < len(text) {
		found = append(found, text[start:])
	}
	if len(found) == 0 {
		return []string{text}
	}
	return found
}

func isAbbreviation(candidate string) bool {
	for _, abbreviation := range abbreviations {
		if strings.HasSuffix(candidate, abbreviation) {
			return true
		}
	}
	return false
}

func count(text string) int { return len([]rune(text)) }
