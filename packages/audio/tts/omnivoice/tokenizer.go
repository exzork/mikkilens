package omnivoice

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The text side of OmniVoice is Qwen3's tokenizer, which is byte-level BPE:
// the text is cut into pieces by a fixed pattern, each piece's UTF-8 bytes are
// rewritten as printable runes, and those are merged back up by a ranked
// table. It is reimplemented here rather than shelled out to, because the
// alternative is a Python process per utterance.
//
// Getting it wrong is quiet. A tokenizer that is almost right produces ids
// that are all inside the vocabulary and all wrong, and what comes out is
// fluent speech saying something else. That is why tokenizer_test.go checks
// this against vectors taken from the real tokenizer rather than against
// itself.

// Tokenizer is the loaded vocabulary, merge table and special tokens.
type Tokenizer struct {
	vocab  map[string]int32
	ranks  map[string]int32 // "left\x00right" -> merge rank, lower merges first
	added  []addedToken     // longest first, so the scan can take the first hit
	frozen map[string]int32 // added tokens by content, for the ids we need by name
}

type addedToken struct {
	content string
	id      int32
}

// LoadTokenizer reads a HuggingFace tokenizer.json.
//
// Only the parts that decide the output are read: the vocabulary, the merges
// and the added tokens. The normalizer and pre-tokenizer are not read but
// reimplemented, because they are a fixed pattern rather than data -- see
// preTokenize.
func LoadTokenizer(path string) (*Tokenizer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, failure("could not read the text tokenizer: %v", err)
	}

	var parsed struct {
		AddedTokens []struct {
			ID      int32  `json:"id"`
			Content string `json:"content"`
		} `json:"added_tokens"`
		Model struct {
			Vocab  map[string]int32  `json:"vocab"`
			Merges []json.RawMessage `json:"merges"`
		} `json:"model"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, failure("could not read the text tokenizer: %v", err)
	}
	if len(parsed.Model.Vocab) == 0 || len(parsed.Model.Merges) == 0 {
		return nil, &Error{Reason: "the text tokenizer has no vocabulary"}
	}

	tokenizer := &Tokenizer{
		vocab:  parsed.Model.Vocab,
		ranks:  make(map[string]int32, len(parsed.Model.Merges)),
		frozen: map[string]int32{},
	}

	// Merges are a pair per line, and the file format has carried them both as
	// ["a", "b"] and as "a b" depending on when it was written. Both are read,
	// because which one this file uses is not something to find out from a
	// model that loads and then mispronounces everything.
	for index, entry := range parsed.Model.Merges {
		var pair []string
		if err := json.Unmarshal(entry, &pair); err != nil {
			var joined string
			if err := json.Unmarshal(entry, &joined); err != nil {
				return nil, failure("could not read merge %d of the text tokenizer", index)
			}
			// Space-separated, and exactly once: a merge of " " with something
			// is written as "Ġ x", so splitting on every space would lose it.
			pair = strings.SplitN(joined, " ", 2)
		}
		if len(pair) != 2 {
			return nil, failure("merge %d of the text tokenizer is not a pair", index)
		}
		tokenizer.ranks[pair[0]+"\x00"+pair[1]] = int32(index)
	}

	for _, token := range parsed.AddedTokens {
		tokenizer.added = append(tokenizer.added, addedToken{content: token.Content, id: token.ID})
		tokenizer.frozen[token.Content] = token.ID
	}
	// Longest first. "<|text_start|>" and a hypothetical "<|text|>" would both
	// match at the same place, and the shorter one winning would leave the
	// rest of the tag to be tokenized as ordinary text.
	sort.SliceStable(tokenizer.added, func(a, b int) bool {
		return len(tokenizer.added[a].content) > len(tokenizer.added[b].content)
	})
	return tokenizer, nil
}

// Special is the id of one added token, by its literal content.
//
// The prompt is built from tags like "<|text_start|>", and a build of the
// model that did not have one is a build this code cannot drive -- so the
// caller gets told which tag is missing rather than getting a plausible id.
func (t *Tokenizer) Special(content string) (int32, error) {
	if id, ok := t.frozen[content]; ok {
		return id, nil
	}
	return 0, failure("the text tokenizer has no %s token", content)
}

// Encode turns text into token ids.
//
// Added tokens are matched first and taken whole, so "<|text_start|>" is one
// id rather than the eight pieces its characters would otherwise become.
// Everything between them goes through NFC, the split pattern, the byte-level
// rewrite and then BPE, in that order.
func (t *Tokenizer) Encode(text string) []int32 {
	var ids []int32
	for _, span := range t.splitAdded(text) {
		if span.id >= 0 {
			ids = append(ids, span.id)
			continue
		}
		for _, piece := range preTokenize(norm.NFC.String(span.text)) {
			ids = append(ids, t.bpe(byteLevel(piece))...)
		}
	}
	return ids
}

// span is a stretch of the input: either one added token, or ordinary text.
type span struct {
	text string
	id   int32 // -1 for ordinary text
}

func (t *Tokenizer) splitAdded(text string) []span {
	var spans []span
	plain := 0
	for at := 0; at < len(text); {
		matched := false
		for _, token := range t.added {
			if strings.HasPrefix(text[at:], token.content) {
				if at > plain {
					spans = append(spans, span{text: text[plain:at], id: -1})
				}
				spans = append(spans, span{id: token.id})
				at += len(token.content)
				plain = at
				matched = true
				break
			}
		}
		if !matched {
			at++
		}
	}
	if plain < len(text) {
		spans = append(spans, span{text: text[plain:], id: -1})
	}
	return spans
}

// -- the split pattern ---------------------------------------------------------

// preTokenize cuts text the way Qwen3's pre-tokenizer does.
//
// The pattern it comes from is this, and the alternatives are tried in order
// at each position, first match wins:
//
//	(?i:'s|'t|'re|'ve|'m|'ll|'d)   contractions, either case
//	[^\r\n\p{L}\p{N}]?\p{L}+       a word, with one leading symbol if there is one
//	\p{N}                          one digit, alone
//	 ?[^\s\p{L}\p{N}]+[\r\n]*      punctuation, with one leading space
//	\s*[\r\n]+                     a line break and the space in front of it
//	\s+(?!\S)                      trailing space, all but the last of it
//	\s+                            anything else made of space
//
// It is hand-written rather than compiled because Go's regexp has no
// lookahead, and `\s+(?!\S)` is the alternative that keeps a word's leading
// space attached to the word rather than to the run before it. Dropping that
// one alternative would retokenize every space in every sentence.
func preTokenize(text string) []string {
	runes := []rune(text)
	var pieces []string
	for at := 0; at < len(runes); {
		width := matchContraction(runes, at)
		if width == 0 {
			width = matchWord(runes, at)
		}
		if width == 0 && isNumber(runes[at]) {
			width = 1
		}
		if width == 0 {
			width = matchPunctuation(runes, at)
		}
		if width == 0 {
			width = matchNewline(runes, at)
		}
		if width == 0 {
			width = matchTrailingSpace(runes, at)
		}
		if width == 0 {
			width = matchSpace(runes, at)
		}
		if width == 0 {
			// Nothing in the pattern matched, which the pattern makes
			// impossible -- every rune is a letter, a number, a space or
			// something else, and the last three alternatives cover the rest.
			// Taking one rune anyway means a future Unicode category cannot
			// turn this into an endless loop.
			width = 1
		}
		pieces = append(pieces, string(runes[at:at+width]))
		at += width
	}
	return pieces
}

var contractions = []string{"'s", "'t", "'re", "'ve", "'m", "'ll", "'d"}

func matchContraction(runes []rune, at int) int {
	if runes[at] != '\'' {
		return 0
	}
	for _, suffix := range contractions {
		want := []rune(suffix)
		if at+len(want) > len(runes) {
			continue
		}
		same := true
		for offset, letter := range want {
			if unicode.ToLower(runes[at+offset]) != letter {
				same = false
				break
			}
		}
		if same {
			return len(want)
		}
	}
	return 0
}

// matchWord is `[^\r\n\p{L}\p{N}]?\p{L}+`: letters, with at most one leading
// character that is not a letter, a digit or a line break. That leading slot
// is what keeps the space in " word" attached to the word.
func matchWord(runes []rune, at int) int {
	start := at
	if letter := runes[at]; letter != '\r' && letter != '\n' &&
		!unicode.IsLetter(letter) && !isNumber(letter) {
		at++
	}
	letters := 0
	for at+letters < len(runes) && unicode.IsLetter(runes[at+letters]) {
		letters++
	}
	if letters == 0 {
		// The optional character was taken and there was no word behind it, so
		// give it back: this alternative did not match, and the character will
		// be picked up by the punctuation one instead.
		return 0
	}
	return at - start + letters
}

// matchPunctuation is ` ?[^\s\p{L}\p{N}]+[\r\n]*`.
func matchPunctuation(runes []rune, at int) int {
	start := at
	if runes[at] == ' ' {
		at++
	}
	symbols := 0
	for at+symbols < len(runes) {
		letter := runes[at+symbols]
		if unicode.IsSpace(letter) || unicode.IsLetter(letter) || isNumber(letter) {
			break
		}
		symbols++
	}
	if symbols == 0 {
		return 0
	}
	at += symbols
	for at < len(runes) && (runes[at] == '\r' || runes[at] == '\n') {
		at++
	}
	return at - start
}

// matchNewline is `\s*[\r\n]+`, which comes to this: a run of whitespace that
// contains a line break is taken up to and including the last break in it.
func matchNewline(runes []rune, at int) int {
	end := whitespaceRun(runes, at)
	last := -1
	for index := at; index < end; index++ {
		if runes[index] == '\r' || runes[index] == '\n' {
			last = index
		}
	}
	if last < 0 {
		return 0
	}
	return last + 1 - at
}

// matchTrailingSpace is `\s+(?!\S)`: whitespace not followed by anything
// printable. Inside a line that means all but the last space of a run, which
// is the last one's job to carry into the word after it; at the end of the
// text it means the whole run.
func matchTrailingSpace(runes []rune, at int) int {
	end := whitespaceRun(runes, at)
	if end == at {
		return 0
	}
	if end == len(runes) {
		return end - at
	}
	if end-at >= 2 {
		return end - at - 1
	}
	return 0
}

func matchSpace(runes []rune, at int) int { return whitespaceRun(runes, at) - at }

func whitespaceRun(runes []rune, at int) int {
	end := at
	for end < len(runes) && unicode.IsSpace(runes[end]) {
		end++
	}
	return end
}

// isNumber is `\p{N}`, which is every numeric character and not only ASCII
// digits -- Arabic-Indic, Devanagari and the rest. The pattern takes them one
// at a time, so "2345" is four pieces.
func isNumber(letter rune) bool { return unicode.IsNumber(letter) }

// -- the byte-level rewrite ----------------------------------------------------

// byteToRune maps each of the 256 byte values to a printable rune.
//
// BPE works on characters, and bytes 0 to 32 are not characters anybody can
// put in a vocabulary file. The 188 bytes that are already printable stand for
// themselves and the other 68 are moved up into U+0100 and above, which is
// what makes a space appear as "Ġ" in the vocabulary.
var byteToRune = buildByteMap()

func buildByteMap() [256]rune {
	var table [256]rune
	taken := [256]bool{}
	for _, span := range [][2]int{{'!', '~'}, {0xA1, 0xAC}, {0xAE, 0xFF}} {
		for value := span[0]; value <= span[1]; value++ {
			table[value] = rune(value)
			taken[value] = true
		}
	}
	next := 0
	for value := 0; value < 256; value++ {
		if !taken[value] {
			table[value] = rune(256 + next)
			next++
		}
	}
	return table
}

func byteLevel(piece string) string {
	var out strings.Builder
	out.Grow(len(piece) * 2)
	for index := 0; index < len(piece); index++ {
		out.WriteRune(byteToRune[piece[index]])
	}
	return out.String()
}

// -- BPE -----------------------------------------------------------------------

// bpe merges one pre-token up into vocabulary entries.
//
// It starts as one symbol per character and repeatedly joins whichever
// adjacent pair the merge table ranks highest, which is the ordinary greedy
// BPE loop. The pieces are short -- a word, a run of punctuation -- so the
// quadratic scan for the best pair costs nothing worth avoiding.
func (t *Tokenizer) bpe(piece string) []int32 {
	symbols := make([]string, 0, len(piece))
	for _, letter := range piece {
		symbols = append(symbols, string(letter))
	}
	if len(symbols) == 0 {
		return nil
	}

	for len(symbols) > 1 {
		bestRank := int32(-1)
		bestAt := -1
		for at := 0; at+1 < len(symbols); at++ {
			rank, ok := t.ranks[symbols[at]+"\x00"+symbols[at+1]]
			if !ok {
				continue
			}
			if bestAt < 0 || rank < bestRank {
				bestRank, bestAt = rank, at
			}
		}
		if bestAt < 0 {
			break
		}
		symbols[bestAt] += symbols[bestAt+1]
		symbols = append(symbols[:bestAt+1], symbols[bestAt+2:]...)
	}

	ids := make([]int32, 0, len(symbols))
	for _, symbol := range symbols {
		if id, ok := t.vocab[symbol]; ok {
			ids = append(ids, id)
			continue
		}
		// A symbol with no id means the merge table built something the
		// vocabulary does not have, which cannot happen with a matched pair of
		// files. Dropping it rather than failing keeps one strange character
		// from silencing the whole sentence.
	}
	return ids
}
