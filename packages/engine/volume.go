package engine

import (
	"log/slog"
	"strconv"
	"strings"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// Setting a volume by voice, which is the only way she can set one while the
// stream is running.
//
// Four separate commands rather than one with a "what" slot. Recognition has
// to tell "bicara" from "musik" either way; done as four, that decision is the
// phrase matcher's, made against fixed words with a margin it can report --
// and a mishearing turns the wrong thing down rather than being handed to a
// handler as a word to guess at.
//
// Every one of them answers by saying the new volume at the new volume. That
// is the whole confirmation: too quiet to hear is the same answer as "it is
// now too quiet", and it arrives without her having to ask.

func volumeHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"set_speech_volume": e.setSpeechVolume,
		"set_chat_volume":   e.setChatVolume,
		"set_earcon_volume": e.setEarconVolume,
		"set_music_volume":  e.setMusicVolume,
	}
}

func (e *Engine) setSpeechVolume(slots map[string]string) error {
	return e.setVolume(slots, "volume.speech", func(settings *config.Config, percent int) {
		settings.Speech.Volume = percent
	})
}

func (e *Engine) setChatVolume(slots map[string]string) error {
	return e.setVolume(slots, "volume.chat", func(settings *config.Config, percent int) {
		settings.Speech.ChatVolume = percent
	})
}

func (e *Engine) setEarconVolume(slots map[string]string) error {
	return e.setVolume(slots, "volume.earcon", func(settings *config.Config, percent int) {
		settings.Speech.EarconVolume = percent
	})
}

func (e *Engine) setMusicVolume(slots map[string]string) error {
	return e.setVolume(slots, "volume.music", func(settings *config.Config, percent int) {
		settings.Music.Volume = percent
	})
}

// setVolume reads the percentage she said, applies it, and says it back.
//
// Applied before it is saved, in that order and for the same reason the
// settings page does it: a value the engine will not take must never end up in
// the file, where it would be waiting the next time she starts.
func (e *Engine) setVolume(
	slots map[string]string, key string, apply func(*config.Config, int),
) error {
	spoken := trimmed(slots["amount"])
	percent, ok := e.spokenPercent(spoken)
	if !ok {
		e.bus.SayKey("volume.not_a_number", feedback.Error, i18n.Args{"amount": spoken})
		return nil
	}

	updated := e.Config()
	apply(&updated, percent)

	if err := e.ApplyConfig(updated); err != nil {
		e.bus.SayKey("volume.failed", feedback.Error, i18n.Args{"reason": err.Error()})
		return nil
	}
	if _, err := updated.Save(""); err != nil {
		// Said, not swallowed: the volume is right until she restarts, and
		// finding it back where it was tomorrow with no explanation is worse
		// than being told now.
		slog.Warn("could not save the new volume", "error", err)
		e.bus.SayKey("volume.not_saved", feedback.Error, i18n.Args{"reason": err.Error()})
		return nil
	}

	e.bus.SayKey(key, feedback.Result, i18n.Args{"percent": percent})
	return nil
}

// spokenPercent reads a percentage out of what she said.
//
// Recognition gives back "50", "lima puluh" or "fifty" depending on the model,
// the language and the day, so all three count. Words are added up rather than
// looked up, which is what makes ninety-five as easy to say as fifty.
//
// Anything above a hundred is refused rather than clamped. A volume over full
// does not exist, so hearing one means a word was misheard -- and being told
// the number was not understood is a better answer than being turned up to
// full because "dua ratus" was heard as "seratus".
func (e *Engine) spokenPercent(spoken string) (int, bool) {
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(
		strings.Trim(trimmed(spoken), "."), "%", " persen ")))
	if len(words) == 0 {
		return 0, false
	}

	scale, ones := numberWords(e.Locale().Language)

	total, pending := 0, 0
	found := false
	for _, word := range words {
		word = strings.Trim(word, ".,!?-")
		if number, err := strconv.Atoi(word); err == nil {
			total += number
			found = true
			continue
		}
		if word == "belas" {
			// Indonesian counts the teens the other way round: "delapan
			// belas" is eight-teen, ten plus what came before it.
			total += 10 + pending
			pending = 0
			found = true
			continue
		}
		if multiplier, ok := scale[word]; ok {
			if pending == 0 {
				pending = 1 // "sepuluh", "hundred": one of them
			}
			total += pending * multiplier
			pending = 0
			found = true
			continue
		}
		value, ok := ones[word]
		if !ok {
			continue // filler: "jadi", "ke", "persen", or a word that was missed
		}
		found = true
		if value >= 10 {
			// A whole ten ("tujuh puluh" written as one word, "seventy"),
			// which adds rather than waits for a multiplier.
			total += value
			continue
		}
		pending = value
	}
	total += pending

	if !found || total < 0 || total > 100 {
		return 0, false
	}
	return total, true
}

// numberWords is the arithmetic of one language: the words that multiply what
// came before them, and the words that are a number themselves.
//
// Kept here rather than in the locale file because it is grammar, not
// something worth asking someone to translate line by line -- and a language
// without an entry still works, on digits.
func numberWords(language string) (scale, ones map[string]int) {
	switch language {
	case "id":
		return map[string]int{
				"puluh": 10, "ratus": 100,
			}, map[string]int{
				"nol": 0, "kosong": 0,
				"satu": 1, "dua": 2, "tiga": 3, "empat": 4, "lima": 5,
				"enam": 6, "tujuh": 7, "delapan": 8, "sembilan": 9,
				"sepuluh": 10, "sebelas": 11, "seratus": 100,
				"setengah": 50, "separuh": 50, "penuh": 100, "maksimal": 100,
			}
	case "en":
		return map[string]int{"hundred": 100}, map[string]int{
			"zero": 0, "nought": 0,
			"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
			"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
			"eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14,
			"fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
			"nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40,
			"fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80,
			"ninety": 90, "half": 50, "full": 100, "max": 100,
		}
	}
	return map[string]int{}, map[string]int{}
}
