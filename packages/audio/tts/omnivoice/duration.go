package omnivoice

import (
	"math"
	"sort"
	"unicode"
)

// How long a sentence will take to say has to be decided before the model runs
// rather than discovered while it runs.
//
// OmniVoice does not stop when it has finished speaking: it fills exactly the
// number of frames it is given. Ask for too few and the end of the sentence is
// cut off mid-word; ask for too many and what follows the words is the model
// inventing breathing, room tone, or another sentence entirely. The length is
// the single setting that most decides whether an utterance is usable.
//
// So it is estimated the way the model's own authors estimate it: every
// character is worth some amount of speaking time depending on which script it
// belongs to, and the total is scaled by how fast the reference voice actually
// spoke. A Han character is three times a Latin letter; a combining accent is
// free; a digit is three and a half, because "7" is said as a word.
//
// This is a port of RuleDurationEstimator from omnivoice/utils/duration.py.
// The range table below is generated from that file rather than typed out --
// see the note above durationRanges.

// The weights, as multiples of one Latin letter (roughly 40 to 50 ms).
const (
	weightCjk          = 3.0 // Chinese, Japanese kanji: one character, one syllable
	weightHangul       = 2.5
	weightKana         = 2.2
	weightEthiopic     = 3.0
	weightYi           = 3.0
	weightIndic        = 1.8 // consonant-vowel complexes
	weightThaiLao      = 1.5
	weightKhmerMyanmar = 1.8
	weightArabic       = 1.5 // consonant-heavy, vowels often unwritten
	weightHebrew       = 1.5
	weightLatin        = 1.0 // the baseline
	weightCyrillic     = 1.0
	weightGreek        = 1.0
	weightArmenian     = 1.0
	weightGeorgian     = 1.0
	weightPunctuation  = 0.5 // room for a pause, not a sound
	weightSpace        = 0.2
	weightDigit        = 3.5 // "7" is said as a word, not a letter
	weightMark         = 0.0 // diacritics change a sound rather than adding one
	weightDefault      = 1.0
)

type scriptRange struct {
	end    rune
	weight float64
}

// generated from omnivoice/utils/duration.py -- do not hand-edit
var durationRanges = []scriptRange{
	{end: 0x02AF, weight: weightLatin},        // Latin (Basic, Supplement, Ext, IPA)
	{end: 0x03FF, weight: weightGreek},        // Greek & Coptic
	{end: 0x052F, weight: weightCyrillic},     // Cyrillic
	{end: 0x058F, weight: weightArmenian},     // Armenian
	{end: 0x05FF, weight: weightHebrew},       // Hebrew
	{end: 0x077F, weight: weightArabic},       // Arabic, Syriac, Arabic Supplement
	{end: 0x089F, weight: weightArabic},       // Arabic Extended-B (+ Syriac Supp)
	{end: 0x08FF, weight: weightArabic},       // Arabic Extended-A
	{end: 0x097F, weight: weightIndic},        // Devanagari
	{end: 0x09FF, weight: weightIndic},        // Bengali
	{end: 0x0A7F, weight: weightIndic},        // Gurmukhi
	{end: 0x0AFF, weight: weightIndic},        // Gujarati
	{end: 0x0B7F, weight: weightIndic},        // Oriya
	{end: 0x0BFF, weight: weightIndic},        // Tamil
	{end: 0x0C7F, weight: weightIndic},        // Telugu
	{end: 0x0CFF, weight: weightIndic},        // Kannada
	{end: 0x0D7F, weight: weightIndic},        // Malayalam
	{end: 0x0DFF, weight: weightIndic},        // Sinhala
	{end: 0x0EFF, weight: weightThaiLao},      // Thai & Lao
	{end: 0x0FFF, weight: weightIndic},        // Tibetan (Abugida)
	{end: 0x109F, weight: weightKhmerMyanmar}, // Myanmar
	{end: 0x10FF, weight: weightGeorgian},     // Georgian
	{end: 0x11FF, weight: weightHangul},       // Hangul Jamo
	{end: 0x137F, weight: weightEthiopic},     // Ethiopic
	{end: 0x139F, weight: weightEthiopic},     // Ethiopic Supplement
	{end: 0x13FF, weight: weightDefault},      // Cherokee
	{end: 0x167F, weight: weightDefault},      // Canadian Aboriginal Syllabics
	{end: 0x169F, weight: weightDefault},      // Ogham
	{end: 0x16FF, weight: weightDefault},      // Runic
	{end: 0x171F, weight: weightDefault},      // Tagalog (Baybayin)
	{end: 0x173F, weight: weightDefault},      // Hanunoo
	{end: 0x175F, weight: weightDefault},      // Buhid
	{end: 0x177F, weight: weightDefault},      // Tagbanwa
	{end: 0x17FF, weight: weightKhmerMyanmar}, // Khmer
	{end: 0x18AF, weight: weightDefault},      // Mongolian
	{end: 0x18FF, weight: weightDefault},      // Canadian Aboriginal Syllabics Ext
	{end: 0x194F, weight: weightIndic},        // Limbu
	{end: 0x19DF, weight: weightIndic},        // Tai Le & New Tai Lue
	{end: 0x19FF, weight: weightKhmerMyanmar}, // Khmer Symbols
	{end: 0x1A1F, weight: weightIndic},        // Buginese
	{end: 0x1AAF, weight: weightIndic},        // Tai Tham
	{end: 0x1B7F, weight: weightIndic},        // Balinese
	{end: 0x1BBF, weight: weightIndic},        // Sundanese
	{end: 0x1BFF, weight: weightIndic},        // Batak
	{end: 0x1C4F, weight: weightIndic},        // Lepcha
	{end: 0x1C7F, weight: weightIndic},        // Ol Chiki (Santali)
	{end: 0x1C8F, weight: weightCyrillic},     // Cyrillic Extended-C
	{end: 0x1CBF, weight: weightGeorgian},     // Georgian Extended
	{end: 0x1CCF, weight: weightIndic},        // Sundanese Supplement
	{end: 0x1CFF, weight: weightIndic},        // Vedic Extensions
	{end: 0x1D7F, weight: weightLatin},        // Phonetic Extensions
	{end: 0x1DBF, weight: weightLatin},        // Phonetic Extensions Supplement
	{end: 0x1DFF, weight: weightDefault},      // Combining Diacritical Marks Supplement
	{end: 0x1EFF, weight: weightLatin},        // Latin Extended Additional (Vietnamese)
	{end: 0x309F, weight: weightKana},         // Hiragana
	{end: 0x30FF, weight: weightKana},         // Katakana
	{end: 0x312F, weight: weightCjk},          // Bopomofo (Pinyin)
	{end: 0x318F, weight: weightHangul},       // Hangul Compatibility Jamo
	{end: 0x9FFF, weight: weightCjk},          // CJK Unified Ideographs (Main)
	{end: 0xA4CF, weight: weightYi},           // Yi Syllables
	{end: 0xA4FF, weight: weightDefault},      // Lisu
	{end: 0xA63F, weight: weightDefault},      // Vai
	{end: 0xA69F, weight: weightCyrillic},     // Cyrillic Extended-B
	{end: 0xA6FF, weight: weightDefault},      // Bamum
	{end: 0xA7FF, weight: weightLatin},        // Latin Extended-D
	{end: 0xA82F, weight: weightIndic},        // Syloti Nagri
	{end: 0xA87F, weight: weightDefault},      // Phags-pa
	{end: 0xA8DF, weight: weightIndic},        // Saurashtra
	{end: 0xA8FF, weight: weightIndic},        // Devanagari Extended
	{end: 0xA92F, weight: weightIndic},        // Kayah Li
	{end: 0xA95F, weight: weightIndic},        // Rejang
	{end: 0xA97F, weight: weightHangul},       // Hangul Jamo Extended-A
	{end: 0xA9DF, weight: weightIndic},        // Javanese
	{end: 0xA9FF, weight: weightKhmerMyanmar}, // Myanmar Extended-B
	{end: 0xAA5F, weight: weightIndic},        // Cham
	{end: 0xAA7F, weight: weightKhmerMyanmar}, // Myanmar Extended-A
	{end: 0xAADF, weight: weightIndic},        // Tai Viet
	{end: 0xAAFF, weight: weightIndic},        // Meetei Mayek Extensions
	{end: 0xAB2F, weight: weightEthiopic},     // Ethiopic Extended-A
	{end: 0xAB6F, weight: weightLatin},        // Latin Extended-E
	{end: 0xABBF, weight: weightDefault},      // Cherokee Supplement
	{end: 0xABFF, weight: weightIndic},        // Meetei Mayek
	{end: 0xD7AF, weight: weightHangul},       // Hangul Syllables
	{end: 0xFAFF, weight: weightCjk},          // CJK Compatibility
	{end: 0xFDFF, weight: weightArabic},       // Arabic Presentation Forms-A
	{end: 0xFE6F, weight: weightDefault},      // Variation Selectors
	{end: 0xFEFF, weight: weightArabic},       // Arabic Presentation Forms-B
	{end: 0xFFEF, weight: weightLatin},        // Fullwidth Latin
}

// charWeight is how much speaking time one character is worth.
//
// The order of the tests is the order in the original and matters: a character
// is classified by what it does before it is classified by where it lives. A
// full stop inside the Devanagari block is still a full stop, and reaching the
// block table with it would price it as a syllable.
func charWeight(letter rune) float64 {
	if (letter >= 'A' && letter <= 'Z') || (letter >= 'a' && letter <= 'z') {
		return weightLatin
	}
	if letter == ' ' {
		return weightSpace
	}
	// Arabic tatweel is a typographic stretch, not a sound.
	if letter == 0x0640 {
		return weightMark
	}

	switch {
	case unicode.Is(unicode.M, letter):
		return weightMark
	case unicode.Is(unicode.P, letter), unicode.Is(unicode.S, letter):
		return weightPunctuation
	case unicode.Is(unicode.Z, letter):
		return weightSpace
	case unicode.Is(unicode.N, letter):
		return weightDigit
	}

	// The first range whose end is at or above this character, which is what
	// bisect_left does on the list of range ends.
	at := sort.Search(len(durationRanges), func(index int) bool {
		return durationRanges[index].end >= letter
	})
	if at < len(durationRanges) {
		return durationRanges[at].weight
	}
	// Above the table: the upper planes are mostly historic scripts and the
	// CJK extensions, and the extensions are what anybody actually writes in.
	if letter > 0x20000 {
		return weightCjk
	}
	return weightDefault
}

func textWeight(text string) float64 {
	total := 0.0
	for _, letter := range text {
		total += charWeight(letter)
	}
	return total
}

// estimateFrames is how many audio frames the text will need.
//
// Everything here is in frames rather than seconds, including the threshold:
// the reference is measured in frames, so working in seconds would mean
// converting twice to get back where we started.
//
// Below lowThreshold the estimate stops being trusted linearly and is pulled
// back up towards it by a cube root. Short phrases are where a proportional
// estimate goes most wrong -- "Ya." is three characters and is not spoken three
// characters quickly -- and cutting off a two-word confirmation is exactly the
// failure this whole function exists to avoid.
func estimateFrames(text, referenceText string, referenceFrames int, speed float32) int {
	const (
		lowThreshold  = 50.0 // frames, which is two seconds at 25 per second
		boostStrength = 3.0
	)

	// No usable reference: borrow the one the model's own code borrows, which
	// is a short English sentence and the frames it takes to say.
	if referenceFrames <= 0 || referenceText == "" {
		referenceText, referenceFrames = "Nice to meet you.", 25
	}

	referenceWeight := textWeight(referenceText)
	if referenceWeight == 0 {
		referenceText, referenceFrames = "Nice to meet you.", 25
		referenceWeight = textWeight(referenceText)
	}

	framesPerWeight := float64(referenceFrames) / referenceWeight
	frames := textWeight(text) * framesPerWeight

	if frames < lowThreshold {
		frames = lowThreshold * math.Pow(frames/lowThreshold, 1.0/boostStrength)
	}
	if speed > 0 && speed != 1 {
		frames /= float64(speed)
	}
	if frames < 1 {
		return 1
	}
	return int(frames)
}
