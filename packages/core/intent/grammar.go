// Package intent turns what she said into a command.
//
// Matching is rule-based rather than model-based on purpose. Working by ear,
// there is no way to check what the app thought she said, so being predictable
// matters more than being clever: the same words always produce the same
// command, it works offline, and it costs nothing per utterance.
//
// Speech recognition still mangles words, so matching is fuzzy rather than
// exact, and the intended fix for a persistent mishearing is to add the
// misheard text as another phrase in commands.toml -- no code change.
package intent

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/exzork/mikkilens/packages/core/fuzzy"
)

var (
	slotPattern = regexp.MustCompile(`\{(\w+)\}`)
	punctuation = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	whitespace  = regexp.MustCompile(`\s+`)
)

const (
	// DefaultThreshold is the similarity a phrase needs before it counts as a
	// match. 72 tolerates a wrong word ending or a dropped short word without
	// letting unrelated sentences through.
	DefaultThreshold = 72.0

	// AmbiguityMargin: if the two best candidates are different commands
	// within this many points, the utterance is called ambiguous rather than
	// guessed at.
	AmbiguityMargin = 4.0

	// SlotPenalty: slotted phrases match greedily, so they lose ties against a
	// literal phrase. It must exceed AmbiguityMargin, or the two readings look
	// ambiguous instead of one simply winning.
	SlotPenalty = 5.0

	// collisionThreshold is how alike two phrases in different commands have
	// to be before we warn that neither will work reliably.
	collisionThreshold = 92.0
)

// KnownSlots are the placeholders the handlers understand.
var KnownSlots = map[string]bool{
	"scene": true, "source": true, "text": true, "question": true, "value": true,
	"channel": true,
	// query is a song name, and number is which of the five read back to play.
	// Both belong to the music search, which is the one command whose input is
	// typed rather than spoken.
	"query": true, "number": true,
	// amount is a volume: "lima puluh persen", "fifty percent", or "50".
	"amount": true,
}

// Normalize lowercases, strips punctuation and collapses whitespace.
func Normalize(text string) string {
	text = punctuation.ReplaceAllString(strings.ToLower(text), " ")
	return strings.TrimSpace(whitespace.ReplaceAllString(text, " "))
}

// Phrase is one trigger phrase, pre-split around its slots.
type Phrase struct {
	Raw          string
	LiteralParts []string // the text between slots, in order
	SlotNames    []string
}

// ParsePhrase splits a phrase into its literal parts and its slot names.
func ParsePhrase(raw string) Phrase {
	names := []string{}
	for _, found := range slotPattern.FindAllStringSubmatch(raw, -1) {
		names = append(names, found[1])
	}

	literals := []string{}
	for _, part := range slotPattern.Split(raw, -1) {
		literals = append(literals, Normalize(part))
	}

	return Phrase{Raw: strings.TrimSpace(raw), LiteralParts: literals, SlotNames: names}
}

// HasSlots reports whether this phrase captures anything.
func (p Phrase) HasSlots() bool { return len(p.SlotNames) > 0 }

// LiteralText is every fixed word in the phrase, which is what collision
// detection compares.
func (p Phrase) LiteralText() string {
	parts := make([]string, 0, len(p.LiteralParts))
	for _, part := range p.LiteralParts {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

// Command is one entry from commands.toml.
type Command struct {
	ID            string
	Phrases       []Phrase
	Confirm       bool
	ConfirmPrompt string

	// Answers marks a command that reports something rather than doing
	// something: the time, the viewer count, which scene is up.
	//
	// It changes what happens when the model was the one who worked out what
	// she meant. "Berapa menit lagi sampai jam 12" is not a request to be told
	// the time, it is a question the time is needed to answer -- so for these,
	// the result goes back to the model and it answers in its own words.
	//
	// Only for commands that report. Handing "the microphone is off" back to a
	// model to comment on would add a round trip to a command that was already
	// finished, and every one of those seconds is one she spends waiting
	// mid-stream.
	Answers bool
}

// Match is a transcript resolved to a command, with anything it captured.
type Match struct {
	Command    string
	Slots      map[string]string
	Phrase     string
	Score      float64
	Transcript string
}

// FileError means commands.toml could not be understood. It is reported aloud,
// never swallowed: a broken command file leaves her with no working commands.
type FileError struct{ Reason string }

func (e *FileError) Error() string { return e.Reason }

// Set is a whole command file: the commands plus anything wrong with it.
type Set struct {
	Commands map[string]Command
	Order    []string // file order, so the spoken help list is stable
	Warnings []string
	Source   string
}

// SetFromMap builds a command set from a decoded commands.toml.
func SetFromMap(document map[string]any, source string) (*Set, error) {
	section, ok := document["commands"].(map[string]any)
	if !ok {
		return nil, &FileError{Reason: "no [commands] section found"}
	}

	// Warnings starts empty rather than nil: "no problems" is an empty list,
	// and a nil slice would reach the settings app as JSON null, where every
	// caller that counts it would fail.
	set := &Set{Commands: map[string]Command{}, Warnings: []string{}, Source: source}

	ids := make([]string, 0, len(section))
	for id := range section {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		body, ok := section[id].(map[string]any)
		if !ok {
			set.Warnings = append(set.Warnings,
				fmt.Sprintf("[commands.%s] is not a table; skipped", id))
			continue
		}
		rawPhrases, ok := body["phrases"].([]any)
		if !ok || len(rawPhrases) == 0 {
			set.Warnings = append(set.Warnings,
				fmt.Sprintf("[commands.%s] has no phrases; skipped", id))
			continue
		}

		parsed := []Phrase{}
		for _, entry := range rawPhrases {
			text, ok := entry.(string)
			if !ok || strings.TrimSpace(text) == "" {
				set.Warnings = append(set.Warnings,
					fmt.Sprintf("[commands.%s] has an empty phrase; skipped", id))
				continue
			}
			phrase := ParsePhrase(text)
			if phrase.LiteralText() == "" {
				set.Warnings = append(set.Warnings, fmt.Sprintf(
					"[commands.%s] phrase %q is only a slot, so it would match everything; skipped",
					id, text))
				continue
			}
			if unknown := unknownSlots(phrase); len(unknown) > 0 {
				set.Warnings = append(set.Warnings, fmt.Sprintf(
					"[commands.%s] phrase %q uses unknown slot(s) %v", id, text, unknown))
			}
			parsed = append(parsed, phrase)
		}
		if len(parsed) == 0 {
			continue
		}

		confirm, _ := body["confirm"].(bool)
		prompt, _ := body["confirm_prompt"].(string)
		answers, _ := body["answers"].(bool)
		set.Commands[id] = Command{
			ID: id, Phrases: parsed, Confirm: confirm, ConfirmPrompt: prompt,
			Answers: answers,
		}
		set.Order = append(set.Order, id)
	}

	if len(set.Commands) == 0 {
		return nil, &FileError{Reason: "no usable commands were defined"}
	}
	set.Warnings = append(set.Warnings, set.findCollisions()...)
	return set, nil
}

// SetFromFile reads and validates one commands.toml.
func SetFromFile(path string) (*Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &FileError{Reason: baseName(path) + " does not exist"}
		}
		return nil, &FileError{Reason: "could not read " + baseName(path) + ": " + err.Error()}
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return nil, &FileError{Reason: baseName(path) + " is not valid TOML: " + err.Error()}
	}
	set, err := SetFromMap(document, path)
	if err != nil {
		return nil, err
	}
	// Decoding into a map loses the file's ordering, and the order matters:
	// it is the order "what can I say" reads her commands out in.
	set.reorder(headerOrder(string(data)))
	return set, nil
}

var headerPattern = regexp.MustCompile(`(?m)^\s*\[commands\.([^\]]+)\]`)

// headerOrder lists the command ids in the order they appear in the file.
func headerOrder(source string) []string {
	order := []string{}
	for _, found := range headerPattern.FindAllStringSubmatch(source, -1) {
		order = append(order, strings.Trim(strings.TrimSpace(found[1]), `"'`))
	}
	return order
}

// reorder puts the commands back into file order, keeping anything the file
// order did not mention at the end.
func (s *Set) reorder(wanted []string) {
	seen := map[string]bool{}
	ordered := make([]string, 0, len(s.Order))
	for _, id := range wanted {
		if s.Has(id) && !seen[id] {
			seen[id] = true
			ordered = append(ordered, id)
		}
	}
	for _, id := range s.Order {
		if !seen[id] {
			seen[id] = true
			ordered = append(ordered, id)
		}
	}
	s.Order = ordered
}

func baseName(path string) string {
	if index := strings.LastIndexAny(path, `/\`); index >= 0 {
		return path[index+1:]
	}
	return path
}

func unknownSlots(phrase Phrase) []string {
	unknown := []string{}
	for _, name := range phrase.SlotNames {
		if !KnownSlots[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// findCollisions reports phrases that two commands would both answer to.
//
// This is announced aloud at startup, because an ambiguous phrase is a command
// she will simply find does not work, with nothing on screen to explain why.
func (s *Set) findCollisions() []string {
	type entry struct {
		id     string
		phrase Phrase
	}
	entries := []entry{}
	for _, id := range s.Order {
		for _, phrase := range s.Commands[id].Phrases {
			entries = append(entries, entry{id, phrase})
		}
	}

	problems := []string{}
	for i, left := range entries {
		for _, right := range entries[i+1:] {
			if left.id == right.id || left.phrase.HasSlots() != right.phrase.HasSlots() {
				continue
			}
			if fuzzy.Ratio(left.phrase.LiteralText(), right.phrase.LiteralText()) >= collisionThreshold {
				problems = append(problems, fmt.Sprintf("%q and %q both answer to %q / %q",
					left.id, right.id, left.phrase.Raw, right.phrase.Raw))
			}
		}
	}
	return problems
}

// Match finds the best command for a transcript.
//
// It returns the winner, or every close rival when the utterance is genuinely
// ambiguous. Reporting the ambiguity rather than picking one keeps the failure
// honest: the caller says "that matches two commands" instead of quietly doing
// the wrong thing.
func (s *Set) Match(transcript string) (best *Match, rivals []Match) {
	return s.MatchThreshold(transcript, DefaultThreshold)
}

// MatchThreshold is Match with the acceptance threshold spelled out.
func (s *Set) MatchThreshold(transcript string, threshold float64) (*Match, []Match) {
	cleaned := Normalize(transcript)
	if cleaned == "" {
		return nil, nil
	}

	candidates := []Match{}
	for _, id := range s.Order {
		command := s.Commands[id]
		var winner *Match
		for _, phrase := range command.Phrases {
			score, slots, ok := scorePhrase(phrase, cleaned)
			if !ok {
				continue
			}
			if winner == nil || score > winner.Score {
				winner = &Match{
					Command: command.ID, Slots: slots, Phrase: phrase.Raw,
					Score: score, Transcript: transcript,
				}
			}
		}
		if winner != nil && winner.Score >= threshold {
			candidates = append(candidates, *winner)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	top := candidates[0]

	tied := []Match{}
	for _, candidate := range candidates[1:] {
		if top.Score-candidate.Score <= AmbiguityMargin {
			tied = append(tied, candidate)
		}
	}
	if len(tied) > 0 {
		return nil, append([]Match{top}, tied...)
	}
	return &top, nil
}

// PhrasesFor lists the trigger phrases for one command.
func (s *Set) PhrasesFor(id string) []string {
	command, ok := s.Commands[id]
	if !ok {
		return nil
	}
	phrases := make([]string, 0, len(command.Phrases))
	for _, phrase := range command.Phrases {
		phrases = append(phrases, phrase.Raw)
	}
	return phrases
}

// SpokenPhrases is one representative phrase per command, for "what can I say".
func (s *Set) SpokenPhrases() []string {
	phrases := make([]string, 0, len(s.Order))
	for _, id := range s.Order {
		if command := s.Commands[id]; len(command.Phrases) > 0 {
			phrases = append(phrases, command.Phrases[0].Raw)
		}
	}
	return phrases
}

// Len is how many commands are defined.
func (s *Set) Len() int { return len(s.Commands) }

// Has reports whether a command id exists.
func (s *Set) Has(id string) bool { _, ok := s.Commands[id]; return ok }

// scorePhrase scores one phrase against a normalized transcript and pulls out
// whatever its slots captured.
func scorePhrase(phrase Phrase, cleaned string) (float64, map[string]string, bool) {
	if !phrase.HasSlots() {
		score := fuzzy.Ratio(phrase.LiteralText(), cleaned)
		if score == 0 {
			return 0, nil, false
		}
		return score, map[string]string{}, true
	}

	// A slotted phrase is the greedier reading: "{question} di layar" matches
	// anything ending in "di layar", including the literal phrase "apa yang
	// ada di layar". Penalising slots lets the more specific literal phrase
	// win that tie, while keeping every score inside 0-100.
	var (
		score float64
		slots map[string]string
		ok    bool
	)
	if len(phrase.SlotNames) == 1 {
		score, slots, ok = scoreSingleSlot(phrase, cleaned)
	} else {
		score, slots, ok = scoreMultiSlot(phrase, cleaned)
	}
	if !ok {
		return 0, nil, false
	}
	return max(0, score-SlotPenalty), slots, true
}

// scoreSingleSlot handles the common shapes: "ganti ke {scene}" and
// "{question} di layar".
func scoreSingleSlot(phrase Phrase, cleaned string) (float64, map[string]string, bool) {
	name := phrase.SlotNames[0]
	before := phrase.LiteralParts[0]
	after := ""
	if len(phrase.LiteralParts) > 1 {
		after = phrase.LiteralParts[1]
	}

	words := strings.Fields(cleaned)
	if len(words) == 0 {
		return 0, nil, false
	}

	var (
		bestScore = -1.0
		bestValue string
		found     bool
	)
	consider := func(score float64, value string) {
		if !found || score > bestScore {
			bestScore, bestValue, found = score, value, true
		}
	}

	switch {
	case before != "" && after == "":
		// A literal prefix, and the slot takes the rest.
		for split := 1; split < len(words); split++ {
			consider(
				fuzzy.Ratio(before, strings.Join(words[:split], " ")),
				strings.Join(words[split:], " "),
			)
		}
	case after != "" && before == "":
		// The slot comes first, then a literal suffix.
		for split := 1; split < len(words); split++ {
			consider(
				fuzzy.Ratio(after, strings.Join(words[split:], " ")),
				strings.Join(words[:split], " "),
			)
		}
	case before != "" && after != "":
		for start := 1; start < len(words); start++ {
			for end := start + 1; end <= len(words); end++ {
				score := (fuzzy.Ratio(before, strings.Join(words[:start], " ")) +
					fuzzy.Ratio(after, strings.Join(words[end:], " "))) / 2.0
				consider(score, strings.Join(words[start:end], " "))
			}
		}
	default:
		return 0, nil, false
	}

	if !found || strings.TrimSpace(bestValue) == "" {
		return 0, nil, false
	}
	return bestScore, map[string]string{name: bestValue}, true
}

// scoreMultiSlot handles two or more slots by requiring the literal parts to
// appear in order and exactly. Fuzzy matching several anchors at once produces
// captures that are impossible to predict, and predictability is the point.
func scoreMultiSlot(phrase Phrase, cleaned string) (float64, map[string]string, bool) {
	parts := []string{}
	for index, literal := range phrase.LiteralParts {
		if literal != "" {
			parts = append(parts, regexp.QuoteMeta(literal))
		}
		if index < len(phrase.SlotNames) {
			parts = append(parts, `(.+?)`)
		}
	}

	pattern, err := regexp.Compile(`^\s*` + strings.Join(parts, `\s*`) + `\s*$`)
	if err != nil {
		return 0, nil, false
	}
	found := pattern.FindStringSubmatch(cleaned)
	if found == nil {
		return 0, nil, false
	}

	slots := map[string]string{}
	for index, name := range phrase.SlotNames {
		value := strings.TrimSpace(found[index+1])
		if value == "" {
			return 0, nil, false
		}
		slots[name] = value
	}
	return 100.0, slots, true
}
