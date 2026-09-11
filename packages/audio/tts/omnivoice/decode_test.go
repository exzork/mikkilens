package omnivoice

import (
	"math"
	"strings"
	"testing"
)

// The schedule decides how many of the eight-by-T slots stop being undecided
// on each step. Two things about it are not negotiable: it must settle every
// slot, and it must never try to settle more than are left. The first is why
// decoding terminates at all; the second would be an index past the end of the
// score list.
func TestScheduleSettlesEverythingExactlyOnce(t *testing.T) {
	for _, steps := range []int{1, 2, 4, 8, 16, 32, 64} {
		for _, total := range []int{1, 8, 25, 200, 1000, 8000} {
			schedule := unmaskSchedule(steps, total)
			if len(schedule) != steps {
				t.Fatalf("unmaskSchedule(%d, %d) gave %d steps", steps, total, len(schedule))
			}

			sum := 0
			for index, count := range schedule {
				if count < 0 {
					t.Fatalf("unmaskSchedule(%d, %d) step %d settles %d",
						steps, total, index, count)
				}
				sum += count
				if sum > total {
					t.Fatalf("unmaskSchedule(%d, %d) has settled %d of %d by step %d",
						steps, total, sum, total, index)
				}
			}
			if sum != total {
				t.Errorf("unmaskSchedule(%d, %d) settles %d, want %d",
					steps, total, sum, total)
			}
		}
	}
}

// The shift is the reason the schedule is worth having at all: the early steps
// commit to less than an even split would, because every later decision is
// conditioned on them.
func TestScheduleCommitsSlowlyAtFirst(t *testing.T) {
	schedule := unmaskSchedule(16, 1600)
	even := 1600 / 16
	if schedule[0] >= even {
		t.Errorf("the first step settles %d of 1600, which is not fewer than an "+
			"even split of %d -- the time shift is not being applied", schedule[0], even)
	}
	if schedule[len(schedule)-1] <= schedule[0] {
		t.Errorf("the last step settles %d and the first %d; the schedule should "+
			"accelerate", schedule[len(schedule)-1], schedule[0])
	}
}

// Length is the setting that most decides whether an utterance is usable, so
// the estimator gets its own tests rather than being covered only through a
// two-gigabyte model.
func TestLengthGrowsWithTheText(t *testing.T) {
	short := estimateFrames("Ya.", "", 0, 1)
	medium := estimateFrames("Halo semuanya, selamat datang.", "", 0, 1)
	long := estimateFrames(strings.Repeat("Halo semuanya, selamat datang. ", 6), "", 0, 1)

	if !(short < medium && medium < long) {
		t.Errorf("length does not grow with the text: %d, %d, %d", short, medium, long)
	}
	if short < 1 {
		t.Errorf("even the shortest phrase needs a frame, got %d", short)
	}
}

// Scripts are not worth the same. A Han character is a whole syllable and a
// combining accent is silent, and an estimator that treated them as equal
// would cut Chinese off halfway and leave Vietnamese padded with invention.
func TestLengthWeighsScriptsDifferently(t *testing.T) {
	han := estimateFrames("你好世界你好世界", "", 0, 1)
	latin := estimateFrames("halohalo", "", 0, 1)
	if han <= latin {
		t.Errorf("eight Han characters (%d frames) should need longer than eight "+
			"Latin letters (%d frames)", han, latin)
	}

	// "é" as e + combining acute is the same sound as "é" on its own, so the
	// mark must be free. NFC would fold these together before the model sees
	// them; the weight table has to agree anyway, because it is also asked
	// about scripts where the mark does not compose.
	if plain, marked := charWeight('e'), charWeight('́'); marked != 0 {
		t.Errorf("a combining accent weighs %v, want 0 (a plain letter is %v)", marked, plain)
	}
	if charWeight('7') <= charWeight('a') {
		t.Error("a digit is said as a word and should outweigh a letter")
	}
	if charWeight('.') >= charWeight('a') {
		t.Error("a full stop is a pause, not a syllable")
	}
}

// Speed is the only lever there is: the model fills the time it is given, so
// asking for less time is the whole of asking it to hurry.
func TestSpeedShortensTheRead(t *testing.T) {
	const text = "Halo semuanya, selamat datang di siaran hari ini."
	normal := estimateFrames(text, "", 0, 1)
	faster := estimateFrames(text, "", 0, 1.5)
	slower := estimateFrames(text, "", 0, 0.75)

	if faster >= normal {
		t.Errorf("1.5x speed wants %d frames, which is not fewer than %d", faster, normal)
	}
	if slower <= normal {
		t.Errorf("0.75x speed wants %d frames, which is not more than %d", slower, normal)
	}
}

// A reference recording is what calibrates the estimate: the same words take
// longer in the voice of somebody who was speaking slowly.
func TestReferenceSetsThePace(t *testing.T) {
	const text = "Halo semuanya, selamat datang di siaran hari ini."
	const reference = "Nice to meet you."

	quick := estimateFrames(text, reference, 25, 1)
	slow := estimateFrames(text, reference, 100, 1)
	if slow <= quick {
		t.Errorf("a reference that took four times as long wants %d frames "+
			"against %d; it should want more", slow, quick)
	}

	// A reference with no transcript cannot calibrate anything, and must fall
	// back rather than divide by zero.
	if frames := estimateFrames(text, "", 125, 1); frames < 1 {
		t.Errorf("a reference with no transcript gave %d frames", frames)
	}
}

func TestChunkSplitsAtSentenceEnds(t *testing.T) {
	long := strings.Repeat("Ini kalimat yang cukup panjang. ", 12)
	pieces := chunk(long)
	if len(pieces) < 2 {
		t.Fatalf("a %d character read came back as %d piece(s)", len(long), len(pieces))
	}
	for _, piece := range pieces {
		if len(piece) > chunkLimit {
			t.Errorf("a piece is %d characters, over the %d limit: %q",
				len(piece), chunkLimit, piece)
		}
		if strings.TrimSpace(piece) != piece {
			t.Errorf("a piece has loose whitespace on it: %q", piece)
		}
	}
	if joined := strings.Join(pieces, " "); len(joined) < len(strings.TrimSpace(long))-len(pieces) {
		t.Error("splitting lost text")
	}

	if got := chunk("  "); got != nil {
		t.Errorf("chunk(%q) = %q, want nothing to say", "  ", got)
	}
}

func TestCombineTextTidiesTheWayTheModelExpects(t *testing.T) {
	for _, test := range []struct {
		text, reference, want string
	}{
		{"Halo dunia.", "", "Halo dunia."},
		// The reference transcript leads, because to the model this is one
		// continuous read.
		{"Halo dunia.", "Nice to meet you.", "Nice to meet you. Halo dunia."},
		{"Halo\ndunia.", "", "Halodunia."},
		{"Halo   dunia.", "", "Halo dunia."},
		{"Halo\t\tdunia.", "", "Halo dunia."},
		{"Halo （dunia）.", "", "Halo (dunia)."},
		// A space between Han characters is a typing artefact, and the model
		// hears it as a pause.
		{"你好 世界", "", "你好世界"},
		{"halo 世界", "", "halo世界"},
	} {
		if got := combineText(test.text, test.reference); got != test.want {
			t.Errorf("combineText(%q, %q) = %q, want %q",
				test.text, test.reference, got, test.want)
		}
	}
}

// The logits arrive as half precision, and the subnormal case is the one that
// is easy to get wrong and never notice: it only affects values near zero,
// which in a log-probability is exactly where the interesting ones are.
func TestHalfPrecisionWidensCorrectly(t *testing.T) {
	for _, test := range []struct {
		half uint16
		want float32
	}{
		{0x0000, 0},
		{0x8000, float32(math.Copysign(0, -1))},
		{0x3C00, 1},
		{0xBC00, -1},
		{0x4000, 2},
		{0x3555, 0.333251953125},
		{0x7BFF, 65504}, // the largest half there is
		{0x0400, 6.103515625e-05},
		{0x0001, 5.960464477539063e-08}, // the smallest subnormal
		{0x03FF, 6.0975551605224609e-05},
	} {
		got := math.Float32frombits(halfToBits(test.half))
		if got != test.want {
			t.Errorf("half 0x%04X widened to %v, want %v", test.half, got, test.want)
		}
	}

	if !math.IsInf(float64(math.Float32frombits(halfToBits(0x7C00))), 1) {
		t.Error("half 0x7C00 should widen to +Inf")
	}
	if !math.IsNaN(float64(math.Float32frombits(halfToBits(0x7E00)))) {
		t.Error("half 0x7E00 should widen to NaN")
	}
}

// normalizeLog has to survive the values the guidance step actually hands it,
// which include a negative infinity for the ruled-out mask token.
func TestNormalizeLogProducesProbabilities(t *testing.T) {
	values := []float64{1, 2, 3, math.Inf(-1), -50}
	normalizeLog(values)

	total := 0.0
	for _, value := range values {
		total += math.Exp(value)
	}
	if math.Abs(total-1) > 1e-9 {
		t.Errorf("the probabilities sum to %v, want 1", total)
	}
	if !math.IsInf(values[3], -1) {
		t.Errorf("a ruled-out entry became %v; it should stay impossible", values[3])
	}
}

// highest picks which slots settle. It must never return one that was ruled
// out, however few candidates are left.
func TestHighestIgnoresWhatIsAlreadyDecided(t *testing.T) {
	scores := []float64{5, math.Inf(-1), 3, math.Inf(-1), 9}

	got := highest(scores, 2)
	if len(got) != 2 || got[0] != 4 || got[1] != 0 {
		t.Errorf("highest(_, 2) = %v, want the 9 then the 5 (indices 4, 0)", got)
	}

	// Asking for more than exist returns what exists rather than overrunning.
	if all := highest(scores, 99); len(all) != 3 {
		t.Errorf("highest(_, 99) returned %d of 3 available: %v", len(all), all)
	}
	if none := highest([]float64{math.Inf(-1), math.Inf(-1)}, 1); len(none) != 0 {
		t.Errorf("highest of nothing available returned %v", none)
	}
}

// The fade exists so an utterance spliced into a stream of other utterances
// does not click at the seam.
func TestPostProcessFadesTheEnds(t *testing.T) {
	samples := make([]float32, SampleRate) // one second
	for index := range samples {
		samples[index] = 0.5
	}
	out := postProcess(samples, nil)

	if out[0] != 0 {
		t.Errorf("the first sample is %v, want silence", out[0])
	}
	if last := out[len(out)-1]; last != 0 {
		t.Errorf("the last sample is %v, want silence", last)
	}
	if middle := out[len(out)/2]; middle == 0 {
		t.Error("the middle was faded out too; the ramp is too long")
	}

	// A clip shorter than two fades must not fold back on itself.
	tiny := []float32{1, 1, 1}
	if got := postProcess(tiny, nil); len(got) != 3 {
		t.Errorf("postProcess turned 3 samples into %d", len(got))
	}
	if got := postProcess(nil, nil); len(got) != 0 {
		t.Errorf("postProcess invented %d samples from nothing", len(got))
	}
}

// A voice file that is the wrong shape has to be refused rather than reach the
// model, where the failure would be a crash inside a C library.
func TestVoiceShapeIsChecked(t *testing.T) {
	var missing *Voice
	if missing.Frames() != 0 {
		t.Error("a voice that is not there should have no frames")
	}
	voice := &Voice{Codes: make([][]int32, Codebooks)}
	for row := range voice.Codes {
		voice.Codes[row] = make([]int32, 40)
	}
	if voice.Frames() != 40 {
		t.Errorf("Frames() = %d, want 40", voice.Frames())
	}
}

func TestOptionDefaults(t *testing.T) {
	empty := Options{}
	if empty.steps() != DefaultSteps {
		t.Errorf("steps() = %d, want %d", empty.steps(), DefaultSteps)
	}
	if empty.guidance() != DefaultGuidance {
		t.Errorf("guidance() = %v, want %v", empty.guidance(), DefaultGuidance)
	}
	if empty.speed() != 1 {
		t.Errorf("speed() = %v, want 1", empty.speed())
	}
	// "None" rather than an empty string: it is what the model was trained to
	// see when nobody said, and an empty tag is a different thing entirely.
	if empty.language() != "None" || empty.instruct() != "None" {
		t.Errorf("an unset language and instruction came out as %q and %q, want None",
			empty.language(), empty.instruct())
	}
	if negative := (Options{Speed: -1, Steps: -5}); negative.speed() != 1 ||
		negative.steps() != DefaultSteps {
		t.Error("nonsense values should fall back to the defaults rather than through")
	}
}
