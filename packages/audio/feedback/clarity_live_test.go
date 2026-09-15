package feedback

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/speakable"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/audio/tts/omnivoice"
)

// TestClarityLive renders a fixed set of sentences in an OmniVoice voice and
// writes each one to a wav, for a recognizer to read back and score.
//
// Clarity is not something a unit test can assert, and "it sounds fine" is not
// a measurement that survives changing one setting at a time. What this gives
// instead is the same sentences, through the same text handling the bus
// applies, rendered under whatever options the environment names -- so two
// runs differ only in the thing being tried.
//
//	MIKKILENS_LIVE=1 MIKKILENS_HOME=<a home with the models> \
//	MIKKILENS_CORPUS=corpus.json MIKKILENS_OUT=out/baseline \
//	    go test ./packages/audio/feedback/ -run TestClarityLive -v -timeout 60m
//
// MIKKILENS_VOICE, MIKKILENS_SPEED, MIKKILENS_STEPS and MIKKILENS_GUIDANCE
// override the voice and the decoding options. The corpus is a JSON list of
// {"id", "set", "text"}; "set" is "chat" for lines read at the chat rate.
// Nothing is played and nothing is cached.
func TestClarityLive(t *testing.T) {
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to render the clarity corpus")
	}
	corpusPath, out := os.Getenv("MIKKILENS_CORPUS"), os.Getenv("MIKKILENS_OUT")
	if corpusPath == "" || out == "" {
		t.Skip("set MIKKILENS_CORPUS and MIKKILENS_OUT")
	}

	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatal(err)
	}
	var corpus []struct{ ID, Set, Text string }
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	voice := envOr("MIKKILENS_VOICE", omnivoice.DefaultVoice)
	options := omnivoice.Options{
		Voice:    voice,
		Language: "id",
		Steps:    envInt("MIKKILENS_STEPS"),
		Guidance: envFloat("MIKKILENS_GUIDANCE"),
	}
	// The rates she actually has: +10% for everything MikkiLens says, +15%
	// for chat. Overridable as one number for both.
	speech, chatSpeed := float32(1.10), float32(1.15)
	if speed := envFloat("MIKKILENS_SPEED"); speed > 0 {
		speech, chatSpeed = speed, speed
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	engine, err := omnivoice.Shared(ctx)
	if err != nil {
		t.Fatalf("could not open OmniVoice: %v", err)
	}
	t.Logf("voice=%q steps=%d guidance=%v speed=%v chat speed=%v accelerated=%v",
		voice, options.Steps, options.Guidance, speech, chatSpeed, engine.Accelerated())

	type rendered struct {
		ID, Set, Text, Spoken string
		Seconds, Took         float64
	}
	var results []rendered
	// MIKKILENS_CHATCLEAN=1 puts chat lines through the chat tidying first, the
	// way the reader renders them: the handle as a name, the message written out.
	chatClean := os.Getenv("MIKKILENS_CHATCLEAN") == "1"

	for _, line := range corpus {
		text := line.Text
		if chatClean && line.Set == "chat" {
			if author, message, found := strings.Cut(text, ", "); found {
				text = speakable.Name(author) + ", " + speakable.Chat(message, "id")
			}
		}
		spoken := speakable.Numbers(unmention(text), "id")
		options.Speed = speech
		if line.Set == "chat" {
			options.Speed = chatSpeed
		}

		started := time.Now()
		samples, err := engine.Speak(ctx, spoken, options)
		if err != nil {
			t.Fatalf("%s: %v", line.ID, err)
		}
		took := time.Since(started).Seconds()

		wav := tts.EncodeWAV(tts.Audio{Samples: samples, SampleRate: engine.SampleRate(), Channels: 1})
		if err := os.WriteFile(filepath.Join(out, line.ID+".wav"), wav, 0o644); err != nil {
			t.Fatal(err)
		}
		seconds := float64(len(samples)) / float64(engine.SampleRate())
		results = append(results, rendered{line.ID, line.Set, line.Text, spoken, seconds, took})
		t.Logf("%-7s %5.2fs in %5.2fs  %s", line.ID, seconds, took, spoken)
	}

	summary, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "rendered.json"), summary, 0o644); err != nil {
		t.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string) int {
	value, _ := strconv.Atoi(os.Getenv(key))
	return value
}

func envFloat(key string) float32 {
	value, _ := strconv.ParseFloat(os.Getenv(key), 32)
	return float32(value)
}
