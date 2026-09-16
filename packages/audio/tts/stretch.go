package tts

import "math"

// Speeding speech up after it has been said, rather than asking for it faster.
//
// OmniVoice has no speed control of its own. It fills the number of frames it
// is given, so asking for less time is asking it to fit the same sentence into
// fewer of them -- and it answers that by slurring and by stopping before the
// last word, at confident volume, sounding finished. Read back through
// recognition, a fifth faster already loses syllables, which is why the model
// itself is never rushed any more.
//
// What happens instead is here: the finished audio is cut into overlapping
// pieces and added back together at a shorter spacing. Each piece keeps its own
// waves exactly as they were, so the pitch does not move -- it is the same
// voice, saying the same words, with less time between them. Where the pieces
// meet is chosen by looking for the alignment that matches best, which is what
// keeps the joins from clicking.
const (
	// stretchWindow is the piece of sound each overlap works on. Forty
	// milliseconds is a few pitch periods of a speaking voice: long enough that
	// a join lands on matching waves, short enough that a syllable is never
	// swallowed whole.
	stretchWindow = 0.040

	// stretchSearch is how far a piece may slide to find that alignment. Ten
	// milliseconds covers a whole period even for a low voice.
	stretchSearch = 0.010

	// stretchMax is as fast as this is allowed to go. Past twice speed the
	// overlaps start landing on different sounds however well they are aligned,
	// and speech turns choppy rather than quick.
	stretchMax = 2.0
)

// stretch returns the samples sped up by speed, with the pitch left alone.
//
// Anything at or below ordinary speed is returned untouched: slowing down is
// not something anybody has asked for, and the model is better at it anyway,
// having been given more frames to fill.
func stretch(samples []float32, sampleRate int, speed float32) []float32 {
	if sampleRate <= 0 || speed <= 1.001 || len(samples) == 0 {
		return samples
	}
	if speed > stretchMax {
		speed = stretchMax
	}

	window := int(float64(sampleRate) * stretchWindow)
	overlap := window / 2
	advance := int(float64(overlap) * float64(speed))
	search := int(float64(sampleRate) * stretchSearch)
	if window < 4 || advance <= 0 || len(samples) < window+advance+search {
		// Too short to overlap anything: a word and a half at most, where the
		// time saved would not be heard and the joins would be.
		return samples
	}

	out := make([]float32, 0, int(float64(len(samples))/float64(speed))+window)
	out = append(out, samples[:window]...)

	for position := advance; position+window+search < len(samples); {
		// The tail of what has been written is what the next piece has to
		// continue from, so the piece chosen is the one that looks most like it.
		tail := out[len(out)-overlap:]
		best, bestScore := position, float32(-math.MaxFloat32)
		for offset := -search; offset <= search; offset++ {
			candidate := position + offset
			if candidate < 0 || candidate+overlap > len(samples) {
				continue
			}
			if score := similarity(tail, samples[candidate:candidate+overlap]); score > bestScore {
				best, bestScore = candidate, score
			}
		}

		frame := samples[best : best+window]
		// Crossfaded rather than butted together: even the best alignment is
		// only close, and a step between two waveforms is a click.
		for index := 0; index < overlap; index++ {
			fade := float32(index) / float32(overlap)
			at := len(out) - overlap + index
			out[at] = out[at]*(1-fade) + frame[index]*fade
		}
		out = append(out, frame[overlap:]...)
		position = best + advance
	}
	return out
}

// similarity is how alike two pieces of sound are, as a plain cross
// correlation. Only the ordering of the scores matters here, not their scale,
// so there is nothing to gain from normalising them.
func similarity(a, b []float32) float32 {
	total := float32(0)
	for index := range a {
		total += a[index] * b[index]
	}
	return total
}
