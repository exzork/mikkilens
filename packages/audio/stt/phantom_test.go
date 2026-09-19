package stt

import "testing"

// The two lines from her log, heard while nobody said anything, and a command
// that merely contains the words.
func TestPhantomLinesAreRecognised(t *testing.T) {
	for _, text := range []string{
		"Terima kasih kerana menonton!", "Terima kasih.", " terima kasih kerana menonton. ",
		"Thanks for watching!",
	} {
		if !Phantom(text) {
			t.Errorf("%q should be recognised as invented", text)
		}
	}
	for _, text := range []string{
		"lanjutkan chat", "terima kasih, sekarang putar lagu", "",
	} {
		if Phantom(text) {
			t.Errorf("%q is not an invented line", text)
		}
	}
}
