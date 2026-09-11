package omnivoice

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CleanName is the only thing standing between a name typed into a web form
// and a path on disk. It gets the attention that deserves.
func TestCleanNameRefusesToBecomeAPath(t *testing.T) {
	for _, hostile := range []string{
		"../../config",
		"..\\..\\config",
		"/etc/passwd",
		"C:\\Windows\\System32",
		"voice/../../../secrets",
		"....//....//x",
		"con.wav",
		"a\x00b",
	} {
		got, err := CleanName(hostile)
		if err != nil {
			continue // refused outright, which is also fine
		}
		for _, forbidden := range []string{"/", "\\", "..", ":", "\x00"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("CleanName(%q) = %q, which still contains %q",
					hostile, got, forbidden)
			}
		}
		// The real test is what it becomes as a path: still inside the folder.
		full := filepath.Clean(filepath.Join("voices", got+".wav"))
		if !strings.HasPrefix(full, "voices"+string(filepath.Separator)) {
			t.Errorf("CleanName(%q) = %q, which escapes to %q", hostile, got, full)
		}
	}
}

func TestCleanNameKeepsWhatWasMeant(t *testing.T) {
	for _, test := range []struct{ in, want string }{
		{"mikki", "mikki"},
		{"Mikki", "mikki"},
		{"  mikki  ", "mikki"},
		{"my voice", "my-voice"},
		{"my   voice", "my-voice"},
		{"voice_2", "voice_2"},
		{"suara-mikki", "suara-mikki"},
		{"Suara Mikki 2026", "suara-mikki-2026"},
		{"--mikki--", "mikki"},
		{"mikki!!!", "mikki"},
	} {
		got, err := CleanName(test.in)
		if err != nil {
			t.Errorf("CleanName(%q): %v", test.in, err)
			continue
		}
		if got != test.want {
			t.Errorf("CleanName(%q) = %q, want %q", test.in, got, test.want)
		}
	}

	// Nothing usable left is an error rather than a silent default: a voice
	// saved under a name she did not choose is one she cannot find again.
	for _, empty := range []string{"", "   ", "!!!", "---", "///"} {
		if got, err := CleanName(empty); err == nil {
			t.Errorf("CleanName(%q) = %q, want an error", empty, got)
		}
	}

	// Long names are cut rather than refused, and not left ending in a dash.
	long, err := CleanName(strings.Repeat("a", 200))
	if err != nil {
		t.Fatalf("CleanName(long): %v", err)
	}
	if len(long) > 48 {
		t.Errorf("CleanName kept %d characters, want at most 48", len(long))
	}
}

// Non-Latin names have to survive, because this application's first language
// is not English and a name is hers to choose.
func TestCleanNameKeepsNonLatinLetters(t *testing.T) {
	for _, name := range []string{"みっき", "мики", "米姬"} {
		got, err := CleanName(name)
		if err != nil {
			t.Errorf("CleanName(%q): %v", name, err)
			continue
		}
		if got == "" {
			t.Errorf("CleanName(%q) came back empty", name)
		}
		if strings.ContainsAny(got, `/\:`) {
			t.Errorf("CleanName(%q) = %q", name, got)
		}
	}
}

func TestSuggestAvoidsWhatIsTaken(t *testing.T) {
	// Whatever is actually installed, the suggestion must not collide with it.
	taken := map[string]bool{}
	for _, name := range Voices() {
		taken[name] = true
	}
	if got := Suggest("mikki"); taken[got] {
		t.Errorf("Suggest returned %q, which is already taken", got)
	}
	if got := Suggest(""); got == "" {
		t.Error("Suggest came back empty for an empty preference")
	}
}

// What is written has to be what is read back, or a voice sounds subtly wrong
// for a reason nobody would think to look for.
func TestWAVRoundTrip(t *testing.T) {
	original := make([]float32, SampleRate/2)
	for index := range original {
		original[index] = float32(math.Sin(float64(index) * 0.05))
	}

	path := filepath.Join(t.TempDir(), "voice.wav")
	if err := os.WriteFile(path, encodeWAV(original), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}

	read, err := readWAV(path)
	if err != nil {
		t.Fatalf("readWAV: %v", err)
	}
	if len(read) != len(original) {
		t.Fatalf("wrote %d samples and read %d back", len(original), len(read))
	}
	for index := range original {
		// Sixteen bit quantisation, so exact equality is the wrong test.
		if math.Abs(float64(original[index]-read[index])) > 1.0/32767*2 {
			t.Fatalf("sample %d went in as %v and came back as %v",
				index, original[index], read[index])
		}
	}
}

// A recording arrives from whatever somebody had to hand. Refusing a common
// shape here means a voice that cannot be added for a reason the message would
// not explain.
func TestReadWAVAcceptsTheShapesRecordingsComeIn(t *testing.T) {
	const frames = 1000
	for _, test := range []struct {
		name              string
		bits, format      int
		channels, rate    int
		wantResampledFrom int
	}{
		{name: "16-bit mono", bits: 16, format: 1, channels: 1, rate: SampleRate},
		{name: "16-bit stereo", bits: 16, format: 1, channels: 2, rate: SampleRate},
		{name: "8-bit mono", bits: 8, format: 1, channels: 1, rate: SampleRate},
		{name: "24-bit mono", bits: 24, format: 1, channels: 1, rate: SampleRate},
		{name: "32-bit float", bits: 32, format: 3, channels: 1, rate: SampleRate},
		{name: "44.1 kHz", bits: 16, format: 1, channels: 1, rate: 44100,
			wantResampledFrom: 44100},
		{name: "16 kHz, as the microphone records", bits: 16, format: 1, channels: 1,
			rate: 16000, wantResampledFrom: 16000},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "in.wav")
			raw := syntheticWAV(frames, test.bits, test.format, test.channels, test.rate)
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatalf("writing: %v", err)
			}

			samples, err := readWAV(path)
			if err != nil {
				t.Fatalf("readWAV: %v", err)
			}

			want := frames
			if test.wantResampledFrom != 0 {
				want = int(float64(frames) * float64(SampleRate) / float64(test.wantResampledFrom))
			}
			// Resampling rounds; within one frame is agreement.
			if difference := len(samples) - want; difference < -1 || difference > 1 {
				t.Errorf("got %d samples, want about %d", len(samples), want)
			}
			for _, value := range samples {
				if math.IsNaN(float64(value)) || value < -1.01 || value > 1.01 {
					t.Fatalf("a sample came back as %v, which is not audio", value)
				}
			}
		})
	}
}

func TestReadWAVRefusesWhatIsNotAWAV(t *testing.T) {
	directory := t.TempDir()
	for _, test := range []struct {
		name string
		body []byte
	}{
		{"empty", nil},
		{"too short", []byte("RIFF")},
		{"not a riff", []byte(strings.Repeat("x", 200))},
		{"riff but not wave", append([]byte("RIFF____AVI "), make([]byte, 100)...)},
	} {
		path := filepath.Join(directory, test.name+".wav")
		if err := os.WriteFile(path, test.body, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readWAV(path); err == nil {
			t.Errorf("readWAV accepted %s", test.name)
		}
	}
	if _, err := readWAV(filepath.Join(directory, "missing.wav")); err == nil {
		t.Error("readWAV accepted a file that is not there")
	}
}

// A stereo recording with the voice on one side only is a real thing that
// happens on a real interface, and taking the wrong channel gives silence.
func TestStereoIsMixedRatherThanHalfDiscarded(t *testing.T) {
	const frames = 500
	body := make([]byte, 0, frames*4)
	for index := 0; index < frames; index++ {
		body = binary.LittleEndian.AppendUint16(body, uint16(int16(8000))) // left
		body = binary.LittleEndian.AppendUint16(body, uint16(int16(0)))    // right: silent
	}
	path := filepath.Join(t.TempDir(), "one-sided.wav")
	if err := os.WriteFile(path, wrapWAV(body, 16, 1, 2, SampleRate), 0o644); err != nil {
		t.Fatal(err)
	}

	samples, err := readWAV(path)
	if err != nil {
		t.Fatalf("readWAV: %v", err)
	}
	if len(samples) == 0 || samples[0] == 0 {
		t.Fatalf("a recording with audio on the left came back silent: %v", samples[:1])
	}
}

func TestResampleKeepsTheSignal(t *testing.T) {
	original := make([]float32, 16000)
	for index := range original {
		original[index] = float32(math.Sin(float64(index) * 0.01))
	}

	up := resample(original, 16000, 24000)
	if len(up) != 24000 {
		t.Errorf("16 kHz to 24 kHz gave %d samples, want 24000", len(up))
	}
	for _, value := range up {
		if math.IsNaN(float64(value)) {
			t.Fatal("resampling produced a value that is not a number")
		}
	}
	// Nothing to do is not an error, and must not allocate a wrong-length
	// slice either.
	if same := resample(original, 24000, 24000); len(same) != len(original) {
		t.Errorf("resampling to the same rate changed the length to %d", len(same))
	}
	if none := resample(nil, 16000, 24000); len(none) != 0 {
		t.Errorf("resampling nothing gave %d samples", len(none))
	}
}

// -- helpers -------------------------------------------------------------------

func syntheticWAV(frames, bits, format, channels, rate int) []byte {
	width := bits / 8
	body := make([]byte, 0, frames*width*channels)
	for index := 0; index < frames; index++ {
		value := math.Sin(float64(index) * 0.02)
		for channel := 0; channel < channels; channel++ {
			switch {
			case format == 3 && bits == 32:
				body = binary.LittleEndian.AppendUint32(body,
					math.Float32bits(float32(value)))
			case bits == 8:
				body = append(body, byte(int(value*127)+128))
			case bits == 16:
				body = binary.LittleEndian.AppendUint16(body, uint16(int16(value*32767)))
			case bits == 24:
				scaled := int32(value * 8388607)
				body = append(body, byte(scaled), byte(scaled>>8), byte(scaled>>16))
			}
		}
	}
	return wrapWAV(body, bits, format, channels, rate)
}

func wrapWAV(body []byte, bits, format, channels, rate int) []byte {
	out := make([]byte, 0, len(body)+44)
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(36+len(body)))
	out = append(out, "WAVEfmt "...)
	out = binary.LittleEndian.AppendUint32(out, 16)
	out = binary.LittleEndian.AppendUint16(out, uint16(format))
	out = binary.LittleEndian.AppendUint16(out, uint16(channels))
	out = binary.LittleEndian.AppendUint32(out, uint32(rate))
	out = binary.LittleEndian.AppendUint32(out, uint32(rate*channels*bits/8))
	out = binary.LittleEndian.AppendUint16(out, uint16(channels*bits/8))
	out = binary.LittleEndian.AppendUint16(out, uint16(bits))
	out = append(out, "data"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body)))
	return append(out, body...)
}
