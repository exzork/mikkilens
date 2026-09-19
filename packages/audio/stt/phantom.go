package stt

import (
	"strings"
	"unicode"
)

// phantoms are what Whisper writes when it is handed audio with no speech in
// it. It learned them from the closing lines and credits of YouTube videos, so
// they come out whole, punctuated and confident -- "Terima kasih kerana
// menonton!", in Malay spelling, from a recogniser told the language is
// Indonesian. Measured on two seconds of silence, small said that; turbo said
// "Terima kasih.".
//
// Matched against the whole transcript only. A command that happens to contain
// one of these words is still a command; a transcript that is nothing but one
// of these lines is almost never something she said to MikkiLens, and none of
// them is a command, so the cost of being wrong is one unanswered pleasantry.
var phantoms = map[string]bool{
	"terima kasih":                         true,
	"terima kasih banyak":                  true,
	"terima kasih kerana menonton":         true,
	"terima kasih telah menonton":          true,
	"terima kasih sudah menonton":          true,
	"terima kasih karena menonton":         true,
	"terima kasih telah menyaksikan":       true,
	"sampai jumpa":                         true,
	"sampai jumpa lagi":                    true,
	"sampai jumpa di video berikutnya":     true,
	"jangan lupa subscribe":                true,
	"thank you":                            true,
	"thank you for watching":               true,
	"thanks for watching":                  true,
	"see you next time":                    true,
	"subtitles by the amara org community": true,
}

// Phantom reports whether a transcript is one of the lines Whisper invents for
// audio that had no speech in it.
func Phantom(text string) bool {
	return phantoms[normalizePhantom(text)]
}

func normalizePhantom(text string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, text)
	return strings.Join(strings.Fields(cleaned), " ")
}
