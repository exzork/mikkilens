// Package speakable turns what is written into what can be read aloud.
//
// A voice is handed text that was written for eyes. "Rp50.000", "20:15" and
// "12.500" are instantly clear on a screen and are anybody's guess to a speech
// model: OmniVoice reads digits one at a time or skips them, Supertonic says
// "fifty point zero zero zero", and the one reading that is certainly wrong is
// a donation amount said as the wrong number. Writing the numbers out as words
// before any engine sees them makes all four voices say the same, right thing.
package speakable

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Numbers writes every number in text out as words in the given language.
//
// Only Indonesian and English are known, because those are the two languages
// MikkiLens speaks. Anything else is returned untouched: digits read by a voice
// in its own way beat digits spelled out in a language it is not speaking.
func Numbers(text, language string) string {
	spoken, ok := languages[base(language)]
	if !ok || strings.IndexFunc(text, isDigit) < 0 {
		return text
	}
	return spoken.rewrite(text)
}

func base(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if head, _, found := strings.Cut(language, "-"); found {
		return head
	}
	return language
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// language is everything that differs between the two: the words, and which
// of "." and "," groups thousands.
type language struct {
	code     string
	grouping byte // the thousands separator; the other one marks a fraction

	cardinal func(uint64) string
	ordinal  func(match, *language) (string, bool)

	point, dot, minus, to, times, percent string
	months                                [12]string

	// date and clock read a day and a time the way this language says them.
	date  func(l *language, day, month, year uint64) string
	clock func(l *language, hour, minute, second uint64) string
}

var languages = map[string]*language{
	"id": {
		code:     "id",
		grouping: '.',
		cardinal: indonesianCardinal,
		ordinal:  indonesianOrdinal,
		point:    "koma", dot: "titik", minus: "minus", to: "sampai", times: "kali", percent: "persen",
		months: [12]string{"Januari", "Februari", "Maret", "April", "Mei", "Juni",
			"Juli", "Agustus", "September", "Oktober", "November", "Desember"},
		date: func(l *language, day, month, year uint64) string {
			return l.cardinal(day) + " " + l.months[month-1] + " " + l.cardinal(year)
		},
		clock: func(l *language, hour, minute, second uint64) string {
			words := l.cardinal(hour)
			if minute > 0 {
				words += " lewat " + l.cardinal(minute) + " menit"
			}
			if second > 0 {
				words += " " + l.cardinal(second) + " detik"
			}
			return words
		},
	},
	"en": {
		code:     "en",
		grouping: ',',
		cardinal: englishCardinal,
		ordinal:  englishOrdinalSuffix,
		point:    "point", dot: "point", minus: "minus", to: "to", times: "times", percent: "percent",
		months: [12]string{"January", "February", "March", "April", "May", "June",
			"July", "August", "September", "October", "November", "December"},
		date: func(l *language, day, month, year uint64) string {
			return "the " + englishOrdinal(day) + " of " + l.months[month-1] + " " + l.cardinal(year)
		},
		clock: func(l *language, hour, minute, second uint64) string {
			var words string
			switch {
			case minute == 0 && second == 0:
				return l.cardinal(hour) + " o'clock"
			case minute < 10:
				words = l.cardinal(hour) + " oh " + l.cardinal(minute)
			default:
				words = l.cardinal(hour) + " " + l.cardinal(minute)
			}
			if second > 0 {
				words += " and " + l.cardinal(second) + " seconds"
			}
			return words
		},
	},
}

// The shapes a number is written in, tried from the most specific to the
// least, because each one turns its digits into words and the next only sees
// what is left. A date has to go before a range, or "12-09-2026" is two
// ranges; money before plain numbers, or "Rp50.000" loses its currency.
var (
	written = `\d[\d.,]*\d|\d`

	isoDate  = regexp.MustCompile(`\b(\d{4})-(\d{1,2})-(\d{1,2})\b`)
	date     = regexp.MustCompile(`\b(\d{1,2})[/-](\d{1,2})[/-](\d{4}|\d{2})\b`)
	idOrder  = regexp.MustCompile(`(?i)\bke-(\d{1,6})\b`)
	enOrder  = regexp.MustCompile(`(?i)\b(\d{1,9})(st|nd|rd|th)\b`)
	clock    = regexp.MustCompile(`\b([01]?\d|2[0-3]):([0-5]\d)(?::([0-5]\d))?\b`)
	named    = regexp.MustCompile(`(?i)\b(jam|pukul|at)\s+([01]?\d|2[0-3])\.([0-5]\d)\b`)
	prefixed = regexp.MustCompile(`(?i)(?:\b(rp|idr|usd|eur|gbp|jpy)\.?|(us\$|\$|€|£|¥))\s?(-?)(` +
		written + `)(?:\s?(k|rb|ribu|jt|juta)\b)?`)
	suffixed = regexp.MustCompile(`(?i)(` + written + `)\s?(idr|usd|eur|gbp|jpy)\b`)
	percent  = regexp.MustCompile(`(-?)(` + written + `)\s?%`)
	scaled   = regexp.MustCompile(`(?i)(` + written + `)\s?(k|rb|jt)\b`)
	times    = regexp.MustCompile(`(?i)\b(\d{1,6})\s?x\b`)
	span     = regexp.MustCompile(`\b(\d{1,6})\s?[-–]\s?(\d{1,6})\b`)
	plain    = regexp.MustCompile(`(-?)(` + written + `)`)
)

func (l *language) rewrite(text string) string {
	text = each(isoDate, text, func(m match) (string, bool) {
		return l.sayDate(m.number(3), m.number(2), m.group(1))
	})
	text = each(date, text, func(m match) (string, bool) {
		return l.sayDate(m.number(1), m.number(2), m.group(3))
	})
	text = each(l.orderPattern(), text, func(m match) (string, bool) { return l.ordinal(m, l) })
	text = each(clock, text, func(m match) (string, bool) {
		return l.clock(l, m.number(1), m.number(2), m.number(3)), true
	})
	text = each(named, text, func(m match) (string, bool) {
		return m.group(1) + " " + l.clock(l, m.number(2), m.number(3), 0), true
	})
	text = each(prefixed, text, func(m match) (string, bool) {
		code := strings.ToLower(m.group(1))
		if code == "" {
			code = symbols[strings.ToLower(m.group(2))]
		}
		if code == "rp" {
			code = "idr"
		}
		return l.signed(m, 3, l.money(code, m.group(4), m.group(5))), true
	})
	text = each(suffixed, text, func(m match) (string, bool) {
		return l.money(strings.ToLower(m.group(2)), m.group(1), ""), true
	})
	text = each(percent, text, func(m match) (string, bool) {
		return l.signed(m, 1, l.say(m.group(2))+" "+l.percent), true
	})
	text = each(scaled, text, func(m match) (string, bool) {
		value, ok := l.scale(m.group(1), m.group(2))
		if !ok {
			return "", false
		}
		return l.cardinal(value), true
	})
	text = each(times, text, func(m match) (string, bool) {
		return l.integer(m.group(1)) + " " + l.times, true
	})
	text = each(span, text, func(m match) (string, bool) {
		from, until := m.group(1), m.group(2)
		// Part of a longer number -- "1.000-2.000" -- or a phone number, both
		// of which the plain pass reads better than a range would.
		if strings.ContainsAny(m.before(), ".,") || strings.ContainsAny(m.after(), ".,") ||
			leadingZero(from) || leadingZero(until) {
			return "", false
		}
		return l.integer(from) + " " + l.to + " " + l.integer(until), true
	})
	return each(plain, text, func(m match) (string, bool) {
		return l.signed(m, 1, l.say(m.group(2))), true
	})
}

func (l *language) orderPattern() *regexp.Regexp {
	if l.code == "id" {
		return idOrder
	}
	return enOrder
}

// signed puts a minus in front of words when the "-" in the given group is one.
//
// A hyphen straight after a word is not a sign: "COVID-19" is a name with a
// number in it, not a negative nineteen.
func (l *language) signed(m match, group int, words string) string {
	if m.group(group) == "" {
		return words
	}
	previous, _ := utf8.DecodeLastRuneInString(m.text[:m.loc[2*group]])
	if m.loc[2*group] == 0 || unicode.IsSpace(previous) || strings.ContainsRune("([{", previous) {
		return l.minus + " " + words
	}
	return "-" + words
}

func (l *language) sayDate(day, month uint64, year string) (string, bool) {
	if day < 1 || day > 31 || month < 1 || month > 12 {
		return "", false
	}
	value, _ := strconv.ParseUint(year, 10, 64)
	if len(year) == 2 {
		value += 2000
	}
	return l.date(l, day, month, value), true
}

// -- reading one written number ------------------------------------------------

// number is a written number with its separators resolved: the whole part and
// the fraction, both still as digits.
type number struct{ whole, fraction string }

// parse works out which separator groups thousands and which starts a
// fraction.
//
// Indonesian writes 12.500 and 3,5; English writes 12,500 and 3.5. A number
// with both settles it by whichever comes last. A number with one separator
// followed by three digits is a thousands group in its own language's
// convention and a fraction in the other's -- except rupiah, which has no
// fractions in practice and arrives as "Rp50.000" whichever language she
// speaks.
func (l *language) parse(written string, rupiah bool) (number, bool) {
	dots, commas := strings.Count(written, "."), strings.Count(written, ",")
	switch {
	case dots == 0 && commas == 0:
		return number{whole: written}, true

	case dots > 0 && commas > 0:
		fraction := byte('.')
		if strings.LastIndexByte(written, ',') > strings.LastIndexByte(written, '.') {
			fraction = ','
		}
		if strings.Count(written, string(fraction)) != 1 {
			return number{}, false
		}
		at := strings.LastIndexByte(written, fraction)
		whole, ok := ungroup(written[:at], other(fraction))
		return number{whole: whole, fraction: written[at+1:]}, ok
	}

	separator, count := byte('.'), dots
	if commas > 0 {
		separator, count = ',', commas
	}
	if count == 1 {
		parts := strings.SplitN(written, string(separator), 2)
		if len(parts[1]) != 3 || (separator != l.grouping && !rupiah) {
			return number{whole: parts[0], fraction: parts[1]}, true
		}
	}
	whole, ok := ungroup(written, separator)
	return number{whole: whole}, ok
}

func other(separator byte) byte {
	if separator == '.' {
		return ','
	}
	return '.'
}

// ungroup takes the separators out of "12.500.000", refusing anything that is
// not grouped in threes -- "1.2.3" is a version, not a number.
func ungroup(written string, separator byte) (string, bool) {
	parts := strings.Split(written, string(separator))
	if len(parts[0]) == 0 || len(parts[0]) > 3 {
		return "", false
	}
	for _, part := range parts[1:] {
		if len(part) != 3 {
			return "", false
		}
	}
	return strings.Join(parts, ""), true
}

// say reads a written number, or the pieces of one that is not a number at
// all: "1.2.3" is one point two point three.
func (l *language) say(written string) string {
	parsed, ok := l.parse(written, false)
	if !ok {
		return l.pieces(written)
	}
	words := l.integer(parsed.whole)
	if parsed.fraction != "" {
		words += " " + l.point + " " + l.digits(parsed.fraction)
	}
	return words
}

// integer reads a run of digits as one number -- unless it starts with a zero
// or is too long to be a quantity, which makes it a code, a phone number or an
// id, and those are read a digit at a time the way a person would.
func (l *language) integer(digits string) string {
	if (len(digits) > 1 && digits[0] == '0') || len(digits) > 15 {
		return l.digits(digits)
	}
	value, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return l.digits(digits)
	}
	return l.cardinal(value)
}

func (l *language) digits(digits string) string {
	words := make([]string, 0, len(digits))
	for _, digit := range digits {
		words = append(words, l.cardinal(uint64(digit-'0')))
	}
	return strings.Join(words, " ")
}

func (l *language) pieces(written string) string {
	var built strings.Builder
	start := 0
	for index := 0; index <= len(written); index++ {
		if index < len(written) && written[index] != '.' && written[index] != ',' {
			continue
		}
		if index > start {
			built.WriteString(l.integer(written[start:index]))
		}
		if index < len(written) {
			if written[index] == '.' {
				built.WriteString(" " + l.dot + " ")
			} else {
				built.WriteString(", ")
			}
		}
		start = index + 1
	}
	return built.String()
}

func leadingZero(digits string) bool { return len(digits) > 1 && digits[0] == '0' }

// scale turns "10rb", "1,5jt" and "10k" into the number they stand for.
func (l *language) scale(written, suffix string) (uint64, bool) {
	multiplier := uint64(1_000)
	switch strings.ToLower(suffix) {
	case "jt", "juta":
		multiplier = 1_000_000
	}
	parsed, ok := l.parse(written, false)
	if !ok || len(parsed.whole) > 9 || len(parsed.fraction) > 6 {
		return 0, false
	}
	whole, err := strconv.ParseUint(parsed.whole, 10, 64)
	if err != nil {
		return 0, false
	}
	value := whole * multiplier
	if parsed.fraction != "" {
		divisor := uint64(1)
		for range parsed.fraction {
			divisor *= 10
		}
		if multiplier%divisor != 0 {
			return 0, false
		}
		fraction, _ := strconv.ParseUint(parsed.fraction, 10, 64)
		value += fraction * (multiplier / divisor)
	}
	return value, true
}

// -- money ---------------------------------------------------------------------

type currency struct {
	indonesian, singular, plural string
	// Cents are only read as cents where a currency has them; a fraction of a
	// rupiah is read as a plain fraction, and is almost always ",00".
	centIndonesian, centSingular, centPlural string
}

var currencies = map[string]currency{
	"idr": {indonesian: "rupiah", singular: "rupiah", plural: "rupiah"},
	"usd": {indonesian: "dolar", singular: "dollar", plural: "dollars",
		centIndonesian: "sen", centSingular: "cent", centPlural: "cents"},
	"eur": {indonesian: "euro", singular: "euro", plural: "euros",
		centIndonesian: "sen", centSingular: "cent", centPlural: "cents"},
	"gbp": {indonesian: "pound", singular: "pound", plural: "pounds",
		centIndonesian: "pence", centSingular: "penny", centPlural: "pence"},
	"jpy": {indonesian: "yen", singular: "yen", plural: "yen"},
}

var symbols = map[string]string{"us$": "usd", "$": "usd", "€": "eur", "£": "gbp", "¥": "jpy"}

func (c currency) name(l *language, many bool) string {
	switch {
	case l.code == "id":
		return c.indonesian
	case many:
		return c.plural
	default:
		return c.singular
	}
}

func (c currency) cent(l *language, many bool) string {
	switch {
	case l.code == "id":
		return c.centIndonesian
	case many:
		return c.centPlural
	default:
		return c.centSingular
	}
}

// money reads an amount with its currency after it, the way it is said:
// "Rp50.000" is fifty thousand rupiah.
func (l *language) money(code, written, suffix string) string {
	kind, known := currencies[code]
	if !known {
		return l.say(written)
	}
	if suffix != "" {
		if value, ok := l.scale(written, suffix); ok {
			return l.cardinal(value) + " " + kind.name(l, value != 1)
		}
	}

	parsed, ok := l.parse(written, code == "idr")
	if !ok {
		return l.pieces(written) + " " + kind.name(l, true)
	}
	if strings.Trim(parsed.fraction, "0") == "" {
		parsed.fraction = ""
	}
	whole := l.integer(parsed.whole)
	many := parsed.whole != "1"

	if kind.centSingular != "" && len(parsed.fraction) == 2 {
		joiner := " "
		if l.code == "en" {
			joiner = " and "
		}
		cents := l.integer(strings.TrimLeft(parsed.fraction, "0"))
		return whole + " " + kind.name(l, many) + joiner +
			cents + " " + kind.cent(l, parsed.fraction != "01")
	}
	if parsed.fraction != "" {
		return whole + " " + l.point + " " + l.digits(parsed.fraction) + " " + kind.name(l, true)
	}
	return whole + " " + kind.name(l, many)
}

// -- the words -----------------------------------------------------------------

var indonesianUnits = [...]string{
	"nol", "satu", "dua", "tiga", "empat", "lima", "enam", "tujuh", "delapan", "sembilan",
}

type scaleWord struct {
	value uint64
	name  string
}

var indonesianScales = []scaleWord{
	{1_000_000_000_000, "triliun"}, {1_000_000_000, "miliar"}, {1_000_000, "juta"}, {1_000, "ribu"},
}

func indonesianCardinal(n uint64) string {
	switch {
	case n < 10:
		return indonesianUnits[n]
	case n == 10:
		return "sepuluh"
	case n == 11:
		return "sebelas"
	case n < 20:
		return indonesianUnits[n-10] + " belas"
	case n < 100:
		return joined(indonesianUnits[n/10]+" puluh", n%10, indonesianCardinal)
	case n < 200:
		return joined("seratus", n%100, indonesianCardinal)
	case n < 1_000:
		return joined(indonesianUnits[n/100]+" ratus", n%100, indonesianCardinal)
	case n < 2_000:
		return joined("seribu", n%1_000, indonesianCardinal)
	}
	for _, scale := range indonesianScales {
		if n >= scale.value {
			return joined(indonesianCardinal(n/scale.value)+" "+scale.name, n%scale.value, indonesianCardinal)
		}
	}
	return strconv.FormatUint(n, 10)
}

// indonesianOrdinal reads "ke-3" as ketiga, and "ke-1" as pertama, which is
// what anybody says rather than the kesatu it is spelled as.
func indonesianOrdinal(m match, l *language) (string, bool) {
	value := m.number(1)
	if value == 1 {
		return "pertama", true
	}
	return "ke" + l.cardinal(value), true
}

var englishSmall = [...]string{
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
	"eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen",
}

var englishTens = [...]string{
	"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety",
}

var englishScales = []scaleWord{
	{1_000_000_000_000, "trillion"}, {1_000_000_000, "billion"}, {1_000_000, "million"}, {1_000, "thousand"},
}

func englishCardinal(n uint64) string {
	switch {
	case n < 20:
		return englishSmall[n]
	case n < 100:
		if n%10 == 0 {
			return englishTens[n/10]
		}
		return englishTens[n/10] + "-" + englishSmall[n%10]
	case n < 1_000:
		return joined(englishSmall[n/100]+" hundred", n%100, englishCardinal)
	}
	for _, scale := range englishScales {
		if n >= scale.value {
			return joined(englishCardinal(n/scale.value)+" "+scale.name, n%scale.value, englishCardinal)
		}
	}
	return strconv.FormatUint(n, 10)
}

var englishIrregular = map[string]string{
	"one": "first", "two": "second", "three": "third", "five": "fifth",
	"eight": "eighth", "nine": "ninth", "twelve": "twelfth",
}

func englishOrdinal(n uint64) string {
	words := englishCardinal(n)
	cut := strings.LastIndexAny(words, " -") + 1
	head, last := words[:cut], words[cut:]
	switch {
	case englishIrregular[last] != "":
		return head + englishIrregular[last]
	case strings.HasSuffix(last, "y"):
		return head + strings.TrimSuffix(last, "y") + "ieth"
	default:
		return head + last + "th"
	}
}

func englishOrdinalSuffix(m match, _ *language) (string, bool) {
	return englishOrdinal(m.number(1)), true
}

func joined(head string, rest uint64, say func(uint64) string) string {
	if rest == 0 {
		return head
	}
	return head + " " + say(rest)
}

// -- replacing -----------------------------------------------------------------

type match struct {
	text string
	loc  []int
}

func (m match) group(index int) string {
	if m.loc[2*index] < 0 {
		return ""
	}
	return m.text[m.loc[2*index]:m.loc[2*index+1]]
}

func (m match) number(index int) uint64 {
	value, _ := strconv.ParseUint(m.group(index), 10, 64)
	return value
}

// before and after are the single characters either side of the match.
func (m match) before() string {
	_, size := utf8.DecodeLastRuneInString(m.text[:m.loc[0]])
	return m.text[m.loc[0]-size : m.loc[0]]
}

func (m match) after() string {
	_, size := utf8.DecodeRuneInString(m.text[m.loc[1]:])
	return m.text[m.loc[1] : m.loc[1]+size]
}

// each replaces every match that say accepts, and leaves the rest as written.
//
// Words put straight against letters are spaced off them, so "mp3" is read as
// mp three rather than as one long word the voice has never seen.
func each(pattern *regexp.Regexp, text string, say func(match) (string, bool)) string {
	found := pattern.FindAllStringSubmatchIndex(text, -1)
	if found == nil {
		return text
	}
	var built strings.Builder
	last := 0
	for _, loc := range found {
		words, ok := say(match{text: text, loc: loc})
		if !ok {
			continue
		}
		built.WriteString(text[last:loc[0]])
		if touchesLetter(text[:loc[0]], true) && !strings.HasPrefix(words, "-") {
			built.WriteByte(' ')
		}
		built.WriteString(words)
		if touchesLetter(text[loc[1]:], false) {
			built.WriteByte(' ')
		}
		last = loc[1]
	}
	built.WriteString(text[last:])
	return built.String()
}

func touchesLetter(side string, before bool) bool {
	var r rune
	if before {
		r, _ = utf8.DecodeLastRuneInString(side)
	} else {
		r, _ = utf8.DecodeRuneInString(side)
	}
	return r != utf8.RuneError && unicode.IsLetter(r)
}
