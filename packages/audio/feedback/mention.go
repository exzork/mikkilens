package feedback

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// unmention drops the "@" from a mention so the voice reads the name instead
// of the symbol.
//
// Chat is written to people, so "@Mikki" is most of what anybody types, and
// every engine reads the character out: the local voice has an explicit
// "@" -> " at " rule because its encoder has no character for it, and Edge and
// SAPI arrive at the same reading on their own. Heard aloud it is "at Mikki"
// every single time somebody is addressed, which is a word she never asked
// for in front of every name.
//
// Only a mention loses the symbol. An "@" with a word character before it is
// an address -- "eko@gmail.com" -- where "at" is the reading anybody expects,
// so that one is left for the engine to say.
//
// This is applied to the text on its way to the synthesizer and nowhere else.
// The caption and the history keep the "@", so what is shown still matches
// what was typed.
func unmention(text string) string {
	if !strings.Contains(text, "@") {
		return text
	}

	var built strings.Builder
	built.Grow(len(text))

	// Start of the string counts as a word boundary: a message that opens with
	// a mention is the common case.
	atBoundary := true
	for index := 0; index < len(text); {
		character, width := utf8.DecodeRuneInString(text[index:])
		if character == '@' && atBoundary && opensName(text[index+width:]) {
			index += width
			continue
		}
		built.WriteRune(character)
		atBoundary = !isNameRune(character)
		index += width
	}
	return built.String()
}

// opensName reports whether what follows an "@" could be somebody's name.
//
// A bare "@" with nothing behind it is being used as a word rather than as a
// prefix, and is left alone.
func opensName(rest string) bool {
	character, width := utf8.DecodeRuneInString(rest)
	return width > 0 && isNameRune(character)
}

// isNameRune is what a handle is made of. Letters are checked by category
// rather than by range: chat is not written in ASCII.
func isNameRune(character rune) bool {
	return character == '_' ||
		unicode.IsLetter(character) ||
		unicode.IsDigit(character)
}
