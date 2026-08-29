package wake_test

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/wake"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// These need the ONNX runtime and the openWakeWord models in data/models, so
// they skip when those are not installed. When they are, they check the thing
// that actually matters: that the pipeline computes, rather than quietly
// returning the same number forever. A wake word stuck at a constant would
// never fire, and would look exactly like one that simply never heard her.
//
// Loading the three models costs seconds, so one detector is shared. Resume
// clears its buffers, which is the same thing that happens between commands in
// the running app, so sharing does not weaken what is being tested.

var (
	once     sync.Once
	detector *wake.Detector
	loadErr  error

	firedMu sync.Mutex
	fired   int
)

func shared(t *testing.T) *wake.Detector {
	t.Helper()

	once.Do(func() {
		root := repoRoot(t)
		paths.SetRoot(root)

		for _, name := range []string{
			"onnxruntime.dll", "melspectrogram.onnx", "embedding_model.onnx",
		} {
			if _, err := os.Stat(filepath.Join(root, "data", "models", name)); err != nil {
				return // loadErr stays nil; the skip below reports it
			}
		}

		built := wake.New(wake.Options{
			Model: "hey_jarvis", Threshold: 0.6,
			OnDetected: func(string, float64) {
				firedMu.Lock()
				fired++
				firedMu.Unlock()
			},
		})
		if err := built.Load(); err != nil {
			loadErr = err
			return
		}
		detector = built
	})

	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if detector == nil {
		t.Skip("the wake word models are not installed in data/models")
	}

	// A clean slate, exactly as the app does after a command.
	detector.Resume()
	firedMu.Lock()
	fired = 0
	firedMu.Unlock()
	return detector
}

func timesFired() int {
	firedMu.Lock()
	defer firedMu.Unlock()
	return fired
}

func repoRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find the repository root")
		}
		directory = parent
	}
}

// feed pushes audio through the detector one chunk at a time and returns the
// last score it produced.
//
// Scoring runs on its own goroutine, so a chunk is handed over rather than
// computed here. Feeding in real time, a frame at a time, is also what the
// microphone actually does -- pushing everything at once would just overflow
// the queue and drop most of it.
func feed(d *wake.Detector, samples []float32) float64 {
	for start := 0; start+wake.ChunkSamples <= len(samples); start += wake.ChunkSamples {
		d.Feed(samples[start : start+wake.ChunkSamples])
		if !d.WaitIdle(5 * time.Second) {
			panic("wake word scoring never caught up")
		}
	}
	return d.LastScore()
}

func silence(seconds float64) []float32 {
	return make([]float32, int(16000*seconds))
}

func noise(seconds float64, seed int64) []float32 {
	source := rand.New(rand.NewSource(seed))
	samples := make([]float32, int(16000*seconds))
	for index := range samples {
		samples[index] = float32(source.NormFloat64() * 0.25)
	}
	return samples
}

// tone is closer to speech than noise is: something with harmonic structure
// for the spectrogram to find.
func tone(seconds float64, frequency float64) []float32 {
	samples := make([]float32, int(16000*seconds))
	for index := range samples {
		at := float64(index) / 16000
		samples[index] = float32(
			0.3*math.Sin(2*math.Pi*frequency*at) +
				0.15*math.Sin(2*math.Pi*frequency*2.1*at) +
				0.08*math.Sin(2*math.Pi*frequency*3.3*at))
	}
	return samples
}

func TestPipelineLoads(t *testing.T) {
	d := shared(t)
	if !d.Loaded() {
		t.Fatal("the detector reports itself unloaded after a successful Load")
	}
	t.Logf("loaded wake word model %q", d.ModelName())
}

// TestPipelineRespondsToDifferentAudio is the test that matters: three inputs
// that sound nothing alike must not produce the same score.
func TestPipelineRespondsToDifferentAudio(t *testing.T) {
	scores := map[string]float64{}
	for _, sample := range []struct {
		name  string
		audio []float32
	}{
		{"silence", silence(2.5)},
		{"noise", noise(2.5, 1)},
		{"tone", tone(2.5, 220)},
	} {
		d := shared(t)
		scores[sample.name] = feed(d, sample.audio)
		t.Logf("  %-8s -> %.6g", sample.name, scores[sample.name])
	}

	if scores["silence"] == scores["noise"] && scores["noise"] == scores["tone"] {
		t.Error("every input scored identically; the pipeline is not computing")
	}
	for name, score := range scores {
		if math.IsNaN(score) || math.IsInf(score, 0) {
			t.Errorf("%s produced a non-finite score: %v", name, score)
		}
		if score < 0 || score > 1 {
			t.Errorf("%s scored %v, outside the 0 to 1 a classifier should give", name, score)
		}
	}
}

// TestSilenceDoesNotTrigger is the false-positive check. She talks
// continuously while streaming, and a trigger she did not ask for is worse
// than one she has to repeat.
func TestSilenceDoesNotTrigger(t *testing.T) {
	d := shared(t)
	feed(d, silence(4))
	feed(d, noise(4, 2))

	if count := timesFired(); count != 0 {
		t.Errorf("the wake word fired %d times on silence and noise", count)
	}
}

// TestScoringKeepsUpWithRealTime: this runs on the microphone thread, so it
// has to cost far less than the 80 ms of audio each chunk represents.
func TestScoringKeepsUpWithRealTime(t *testing.T) {
	d := shared(t)
	audio := tone(4, 300)
	chunks := len(audio) / wake.ChunkSamples

	started := time.Now()
	feed(d, audio)
	elapsed := time.Since(started)

	perChunk := elapsed / time.Duration(chunks)
	const budget = 80 * time.Millisecond
	t.Logf("%d chunks in %v (%v each, budget %v)", chunks, elapsed, perChunk, budget)

	if perChunk > budget {
		t.Errorf("scoring costs %v per chunk, more than the %v of audio it covers",
			perChunk, budget)
	}
}

func TestPauseStopsScoring(t *testing.T) {
	d := shared(t)
	feed(d, tone(2, 300))

	d.Pause()
	if d.Enabled() {
		t.Error("the detector should be paused")
	}

	before := d.LastScore()
	feed(d, noise(2, 3))
	if d.LastScore() != before {
		t.Error("a paused detector must not keep scoring; that is how a command " +
			"she is speaking triggers the wake word")
	}

	d.Resume()
	if !d.Enabled() {
		t.Error("the detector should be listening again")
	}
}
