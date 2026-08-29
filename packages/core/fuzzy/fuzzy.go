// Package fuzzy is a small, dependency-free port of the RapidFuzz scorers the
// Python version relied on.
//
// The thresholds spread through command matching (72 to accept a phrase, 92 to
// call two phrases ambiguous, 65 to accept an OBS scene name) were tuned
// against RapidFuzz's numbers, so the scoring has to agree with it rather than
// merely be "some similarity measure". Ratio is therefore the exact indel
// formula RapidFuzz uses: 100 * (1 - indel_distance / (len(a) + len(b))),
// where indel distance counts insertions and deletions only.
package fuzzy

import (
	"sort"
	"strings"
)

// Ratio is RapidFuzz's fuzz.ratio: normalized indel similarity, 0-100.
func Ratio(a, b string) float64 {
	return ratioRunes([]rune(a), []rune(b))
}

func ratioRunes(a, b []rune) float64 {
	total := len(a) + len(b)
	if total == 0 {
		return 100.0
	}
	distance := total - 2*longestCommonSubsequence(a, b)
	return 100.0 * (1.0 - float64(distance)/float64(total))
}

// longestCommonSubsequence uses two rolling rows: the strings here are single
// spoken phrases, so the quadratic cost is a few thousand operations at worst.
func longestCommonSubsequence(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				current[j] = previous[j-1] + 1
			} else if previous[j] >= current[j-1] {
				current[j] = previous[j]
			} else {
				current[j] = current[j-1]
			}
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

// PartialRatio scores the best alignment of the shorter string inside the
// longer one, which is what makes "Mic/Aux" match "Mic/Aux (2)".
func PartialRatio(a, b string) float64 {
	shorter, longer := []rune(a), []rune(b)
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if len(shorter) == 0 {
		if len(longer) == 0 {
			return 100.0
		}
		return 0.0
	}
	best := 0.0
	// Windows of the shorter string's length, plus a little slack either side
	// so a near-match that spans a word boundary is still found.
	window := len(shorter)
	for start := 0; start+1 <= len(longer); start++ {
		end := start + window
		if end > len(longer) {
			end = len(longer)
		}
		if score := ratioRunes(shorter, longer[start:end]); score > best {
			best = score
		}
		if end == len(longer) {
			break
		}
	}
	return best
}

func tokens(value string) []string {
	return strings.Fields(strings.ToLower(value))
}

func sortedJoin(values []string) string {
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	return strings.Join(ordered, " ")
}

// TokenSortRatio compares the two strings with their words in sorted order, so
// "just chatting scene" and "scene just chatting" score the same.
func TokenSortRatio(a, b string) float64 {
	return Ratio(sortedJoin(tokens(a)), sortedJoin(tokens(b)))
}

// TokenSetRatio compares the shared words against each string's extras, which
// forgives one side carrying words the other does not have at all.
func TokenSetRatio(a, b string) float64 {
	left, right := tokens(a), tokens(b)
	shared, leftOnly, rightOnly := partition(left, right)

	joinedShared := sortedJoin(shared)
	first := strings.TrimSpace(joinedShared + " " + sortedJoin(leftOnly))
	second := strings.TrimSpace(joinedShared + " " + sortedJoin(rightOnly))

	best := Ratio(first, second)
	if len(shared) > 0 {
		best = max3(best, Ratio(joinedShared, first), Ratio(joinedShared, second))
	}
	return best
}

func partition(left, right []string) (shared, leftOnly, rightOnly []string) {
	inRight := map[string]bool{}
	for _, word := range right {
		inRight[word] = true
	}
	inLeft := map[string]bool{}
	for _, word := range left {
		inLeft[word] = true
	}
	seen := map[string]bool{}
	for _, word := range left {
		if seen[word] {
			continue
		}
		seen[word] = true
		if inRight[word] {
			shared = append(shared, word)
		} else {
			leftOnly = append(leftOnly, word)
		}
	}
	seen = map[string]bool{}
	for _, word := range right {
		if seen[word] || inLeft[word] {
			continue
		}
		seen[word] = true
		rightOnly = append(rightOnly, word)
	}
	return shared, leftOnly, rightOnly
}

// WRatio is RapidFuzz's weighted ratio: it tries the plain ratio, then the
// token scorers, and falls back to partial matching only when the two strings
// differ enough in length for that to be the honest comparison.
func WRatio(a, b string) float64 {
	const unbaseScale = 0.95

	lengthA, lengthB := len([]rune(a)), len([]rune(b))
	if lengthA == 0 || lengthB == 0 {
		if lengthA == lengthB {
			return 100.0
		}
		return 0.0
	}

	base := Ratio(a, b)
	longest, shortest := lengthA, lengthB
	if shortest > longest {
		longest, shortest = shortest, longest
	}
	lengthRatio := float64(longest) / float64(shortest)

	tokenScore := max2(TokenSortRatio(a, b), TokenSetRatio(a, b)) * unbaseScale
	if lengthRatio < 1.5 {
		return max2(base, tokenScore)
	}

	partialScale := 0.9
	if lengthRatio >= 8.0 {
		partialScale = 0.6
	}
	partial := PartialRatio(a, b) * partialScale
	partialToken := max2(
		PartialRatio(sortedJoin(tokens(a)), sortedJoin(tokens(b))),
		partialTokenSet(a, b),
	) * partialScale * unbaseScale

	return max3(base, partial, partialToken)
}

func partialTokenSet(a, b string) float64 {
	shared, leftOnly, rightOnly := partition(tokens(a), tokens(b))
	joinedShared := sortedJoin(shared)
	first := strings.TrimSpace(joinedShared + " " + sortedJoin(leftOnly))
	second := strings.TrimSpace(joinedShared + " " + sortedJoin(rightOnly))
	return PartialRatio(first, second)
}

// Scorer is any of the functions above, so callers can pick one by name.
type Scorer func(a, b string) float64

// ExtractOne returns the index of the best-scoring choice and its score. It
// returns -1 when there is nothing to choose from.
func ExtractOne(query string, choices []string, scorer Scorer) (int, float64) {
	if scorer == nil {
		scorer = Ratio
	}
	bestIndex, bestScore := -1, -1.0
	for index, choice := range choices {
		if score := scorer(query, choice); score > bestScore {
			bestIndex, bestScore = index, score
		}
	}
	if bestIndex < 0 {
		return -1, 0
	}
	return bestIndex, bestScore
}

func max2(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func max3(a, b, c float64) float64 { return max2(max2(a, b), c) }
