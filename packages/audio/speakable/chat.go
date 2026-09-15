package speakable

import (
	"regexp"
	"strings"
	"unicode"
)

// Chat tidies a chat message for reading aloud.
//
// Chat is typed fast, for eyes, by people who all know the shorthand: "jga",
// "yg", "bgt", laughter written as a keyboard smash. Read as written, a voice
// spells the shorthand out letter by letter and turns "oawkoawkoawk" into three
// words nobody said -- which is most of what made a real stream's chat hard to
// follow by ear. This writes the shorthand out, keeps laughter to one short
// "wkwk", and squeezes letters held down for emphasis back to two.
//
// Numbers are left alone here; Numbers handles them for every line, chat or not.
func Chat(text, language string) string {
	language = base(language)
	words := strings.Fields(text)
	spoken := make([]string, 0, len(words))
	laughed := false

	for _, word := range words {
		lead, core, trail := splitPunctuation(word)
		if core == "" {
			spoken = append(spoken, word)
			continue
		}

		if laugh := laughter(core, language); laugh != "" {
			// One laugh for a run of them: "wkwk wkwkwk wkwk" is one laugh
			// typed three times, and read three times it is a stutter.
			if laughed {
				if trail != "" && len(spoken) > 0 {
					spoken[len(spoken)-1] += trail
				}
				continue
			}
			laughed = true
			spoken = append(spoken, lead+laugh+trail)
			continue
		}
		laughed = false

		core = squeeze(core)
		if expanded, ok := shorthand[language][strings.ToLower(core)]; ok {
			core = matchCase(expanded, core)
		}
		spoken = append(spoken, lead+core+trail)
	}
	return strings.Join(spoken, " ")
}

// Name turns a chat handle into something that can be said.
//
// Handles are made to be unique, not spoken: "EnkiKanataJellyus99",
// "Reinzer-13", "Kiril_06". Read whole they come out as one long invented word
// run into a number. Split where the capitals, separators and digits already
// split it, the same name is one a voice can say.
//
// All of it is kept -- every letter, and the number too, which Numbers then
// reads as a number. It is somebody's name, and hearing it said back in full is
// the point of reading it at all.
func Name(handle string) string {
	name := strings.TrimPrefix(strings.TrimSpace(handle), "@")
	if name == "" {
		return handle
	}

	var built strings.Builder
	runes := []rune(name)
	for index, letter := range runes {
		if letter == '_' || letter == '-' || letter == '.' {
			built.WriteRune(' ')
			continue
		}
		if index > 0 && boundary(runes[index-1], letter, runes[index+1:]) {
			built.WriteRune(' ')
		}
		built.WriteRune(letter)
	}

	fields := strings.Fields(built.String())
	if len(fields) == 0 {
		return name
	}
	return strings.Join(fields, " ")
}

// boundary reports whether a handle changes word between previous and letter:
// lower to upper ("enkiKanata"), the last capital of a run before a lower one
// ("NReina"), and letters meeting digits either way.
func boundary(previous, letter rune, rest []rune) bool {
	switch {
	case unicode.IsLower(previous) && unicode.IsUpper(letter):
		return true
	case unicode.IsUpper(previous) && unicode.IsUpper(letter) &&
		len(rest) > 0 && unicode.IsLower(rest[0]):
		return true
	case unicode.IsLetter(previous) && unicode.IsDigit(letter),
		unicode.IsDigit(previous) && unicode.IsLetter(letter):
		return true
	}
	return false
}

// splitPunctuation separates a word from what surrounds it, so "jga," is
// looked up as "jga" and keeps its comma.
func splitPunctuation(word string) (lead, core, trail string) {
	start := strings.IndexFunc(word, isWordRune)
	if start < 0 {
		return "", "", word
	}
	end := strings.LastIndexFunc(word, isWordRune)
	_, size := decodeAt(word, end)
	return word[:start], word[start : end+size], word[end+size:]
}

func isWordRune(letter rune) bool { return unicode.IsLetter(letter) || unicode.IsDigit(letter) }

func decodeAt(text string, index int) (rune, int) {
	for size := 1; size <= 4 && index+size <= len(text); size++ {
		if r := []rune(text[index : index+size]); len(r) == 1 && r[0] != unicode.ReplacementChar {
			return r[0], size
		}
	}
	return rune(text[index]), 1
}

// squeeze holds a letter typed three or more times in a row back to two:
// "kakkkk" is kakk, "jesssnuuu" is jessnuu. Two is left alone, because plenty
// of real words have a double letter and none have a triple.
func squeeze(word string) string {
	var built strings.Builder
	var previous rune
	run := 0
	for _, letter := range word {
		if unicode.ToLower(letter) == unicode.ToLower(previous) && unicode.IsLetter(letter) {
			run++
		} else {
			run = 1
		}
		previous = letter
		if run <= 2 {
			built.WriteRune(letter)
		}
	}
	return built.String()
}

var (
	// Indonesian laughter is a smash of w, k, a and o; it has to use both w and
	// k, and k at least twice, which no ordinary word made of those letters does.
	keyboardLaugh = regexp.MustCompile(`^[wkao]+$`)
	// "haha", "hehehe", "xixixi": a syllable said at least twice.
	syllableLaugh = regexp.MustCompile(`^(?:ha|he|hi|hu|xi){2,}h?$`)
)

// laughter is the short form of a laugh, or "" when word is not one.
func laughter(word, language string) string {
	lower := strings.ToLower(word)
	if len(lower) >= 4 && keyboardLaugh.MatchString(lower) &&
		strings.Contains(lower, "w") && strings.Count(lower, "k") >= 2 {
		if language == "en" {
			return "haha"
		}
		return "wkwk"
	}
	if syllableLaugh.MatchString(lower) {
		return lower[:min(len(lower), 6)]
	}
	return ""
}

func matchCase(expanded, typed string) string {
	if typed != "" && unicode.IsUpper([]rune(typed)[0]) {
		runes := []rune(expanded)
		runes[0] = unicode.ToUpper(runes[0])
		return string(runes)
	}
	return expanded
}

// shorthand is what chat abbreviates, per language, keyed in lower case.
//
// Only abbreviations that mean one thing. "ga" is nggak and nothing else in
// chat; "sm" could be several words and is still sama nine times in ten.
var shorthand = map[string]map[string]string{
	"id": {
		"yg": "yang", "dgn": "dengan", "utk": "untuk", "krn": "karena", "karna": "karena",
		"tdk": "tidak", "gk": "nggak", "gak": "nggak", "ga": "nggak", "ngga": "nggak", "gaa": "nggak",
		"udh": "udah", "sdh": "sudah", "blm": "belum", "bgt": "banget", "bngt": "banget",
		"jg": "juga", "jga": "juga", "sm": "sama", "tp": "tapi", "tpi": "tapi",
		"klo": "kalau", "kalo": "kalau", "lg": "lagi", "dr": "dari", "sy": "saya",
		"aq": "aku", "ak": "aku", "km": "kamu", "kmu": "kamu", "org": "orang",
		"bnyk": "banyak", "byk": "banyak", "emg": "emang", "gmn": "gimana", "knp": "kenapa",
		"kpn": "kapan", "dmn": "di mana", "bs": "bisa", "bsa": "bisa", "trs": "terus",
		"skrg": "sekarang", "bkn": "bukan", "cm": "cuma", "gpp": "nggak apa-apa",
		"mksh": "makasih", "makasi": "makasih", "tq": "thank you", "ty": "thank you",
		"thx": "thanks", "pls": "please", "plis": "please", "btw": "by the way",
		"otw": "on the way", "gws": "get well soon", "kak": "kak", "bang": "bang",
		"dah": "udah", "dh": "udah", "nnti": "nanti", "ntar": "nanti", "sbntr": "sebentar",
	},
	"en": {
		"u": "you", "ur": "your", "r": "are", "pls": "please", "plz": "please",
		"thx": "thanks", "ty": "thank you", "btw": "by the way", "idk": "I don't know",
		"imo": "in my opinion", "omg": "oh my god", "gg": "good game", "gn": "good night",
		"gm": "good morning", "tbh": "to be honest", "rn": "right now",
	},
}
