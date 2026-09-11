package omnivoice

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Adding a voice is the one thing OmniVoice needs that no other engine does,
// and for a long time the way to do it was to put a wav file in a folder by
// hand. That is a fine thing for somebody building this to do and a terrible
// thing to ask of somebody using it -- particularly here, where the whole
// premise is that she never has to look at the screen.
//
// So this is the other half: everything needed to add, list and remove a voice
// without opening a file manager. What it does not do is decide where the
// audio came from. A recording made through the microphone she already chose
// and a file she exported from Audacity arrive here the same way, as samples,
// and the difference between them stops mattering at this line.

// VoiceInfo is one voice as the settings page shows it.
type VoiceInfo struct {
	Name string `json:"name"`

	// Prepared is whether the recording has been turned into codes yet. An
	// unprepared voice still works -- the first sentence prepares it -- but it
	// costs several seconds the first time, and saying so beforehand is better
	// than letting that look like a hang.
	Prepared bool `json:"prepared"`

	// Seconds is how long the reference is. It is worth showing because it is
	// the number most likely to be wrong: the model wants three to ten, and a
	// two-second clip clones noticeably worse for a reason nobody would guess.
	Seconds float64 `json:"seconds"`

	// Text is the transcript, if there is one. Its absence is worth showing
	// too -- it is the single easiest quality improvement available, and it is
	// invisible otherwise.
	Text string `json:"text"`
}

// Library lists every voice on the machine, prepared or not.
func Library() []VoiceInfo {
	var found []VoiceInfo
	for _, name := range Voices() {
		found = append(found, describe(name))
	}
	sort.Slice(found, func(a, b int) bool { return found[a].Name < found[b].Name })
	return found
}

func describe(name string) VoiceInfo {
	info := VoiceInfo{Name: name, Text: readTranscript(name)}

	if raw, err := os.ReadFile(preparedPath(name)); err == nil {
		var prepared Voice
		if json.Unmarshal(raw, &prepared) == nil && prepared.Frames() > 0 {
			info.Prepared = true
			info.Seconds = float64(prepared.Frames()) / FrameRate
			if info.Text == "" {
				// The prepared file carries the transcript it was made with,
				// which is what the model actually heard. A .txt deleted since
				// then does not change that.
				info.Text = prepared.Text
			}
			return info
		}
	}

	// Not prepared: the length has to come from the recording instead.
	if samples, err := readWAV(recordingPath(name)); err == nil {
		info.Seconds = float64(len(samples)) / SampleRate
	}
	return info
}

// Save writes a recording as a voice and prepares it.
//
// The samples are whatever was captured, at whatever rate they were captured
// at -- the microphone runs at 16 kHz because that is what recognition wants,
// and a file she exported might be anything. Both are resampled on the way in,
// so nothing above this line has to know what the model wants.
//
// Preparing happens here rather than at the first sentence, because this is
// the moment she is waiting and watching. The same work later is a pause in
// the middle of a stream with nothing on screen explaining it.
func Save(name, text string, samples []float32, sampleRate int) error {
	clean, err := CleanName(name)
	if err != nil {
		return err
	}
	if len(samples) == 0 {
		return &Error{Reason: "there was no audio in that recording"}
	}
	if sampleRate <= 0 {
		sampleRate = SampleRate
	}
	if sampleRate != SampleRate {
		samples = resample(samples, sampleRate, SampleRate)
	}
	if len(samples) < SampleRate/2 {
		return &Error{Reason: "that recording is shorter than half a second"}
	}

	if err := os.MkdirAll(voiceDir(), 0o755); err != nil {
		return failure("could not save the voice %q: %v", clean, err)
	}
	if err := os.WriteFile(recordingPath(clean), encodeWAV(samples), 0o644); err != nil {
		return failure("could not save the voice %q: %v", clean, err)
	}

	// The transcript is written even when empty, so that clearing it is a
	// thing that can be done rather than a thing that silently keeps the old
	// one. An empty file and no file mean the same to readTranscript.
	if err := os.WriteFile(transcriptPath(clean),
		[]byte(strings.TrimSpace(text)), 0o644); err != nil {
		return failure("could not save the transcript for %q: %v", clean, err)
	}

	// A voice being replaced has stale codes beside it, and Prepare returns
	// early when a prepared file exists -- so the old one has to go first, or
	// the new recording is saved and never heard.
	_ = os.Remove(preparedPath(clean))
	forget(clean)

	return Prepare(clean)
}

// SaveWAV is Save for audio that is already a wav file, which is what arrives
// when she picks one rather than recording.
func SaveWAV(name, text string, wav []byte) error {
	clean, err := CleanName(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(voiceDir(), 0o755); err != nil {
		return failure("could not save the voice %q: %v", clean, err)
	}

	// Written and then read back rather than parsed from memory, because
	// readWAV is the one place that knows every shape a wav can arrive in --
	// and because what is on disk afterwards should be the file she chose.
	temporary := filepath.Join(voiceDir(), "."+clean+".incoming.wav")
	if err := os.WriteFile(temporary, wav, 0o644); err != nil {
		return failure("could not save the voice %q: %v", clean, err)
	}
	defer os.Remove(temporary)

	samples, err := readWAV(temporary)
	if err != nil {
		return err
	}
	return Save(clean, text, samples, SampleRate)
}

// Remove deletes a voice and everything belonging to it.
func Remove(name string) error {
	clean, err := CleanName(name)
	if err != nil {
		return err
	}
	removed := false
	for _, path := range []string{
		preparedPath(clean), recordingPath(clean), transcriptPath(clean),
	} {
		if err := os.Remove(path); err == nil {
			removed = true
		} else if !os.IsNotExist(err) {
			return failure("could not remove the voice %q: %v", clean, err)
		}
	}
	if !removed {
		return failure("there is no voice called %q", clean)
	}
	forget(clean)
	return nil
}

// forget drops a voice from the loaded engine's cache.
//
// Without it, replacing a recording leaves the old codes in memory and the
// next sentence is read in the voice that was just overwritten -- which reads
// as the save having silently failed.
func forget(name string) {
	sharedMu.Lock()
	engine := shared
	sharedMu.Unlock()

	if engine == nil {
		return
	}
	engine.mu.Lock()
	delete(engine.voices, name)
	engine.mu.Unlock()
}

// CleanName turns what she typed into something that is safe as a filename and
// recognisable as what she typed.
//
// It is deliberately strict rather than clever. This string becomes a path, it
// arrives over an HTTP API, and "../../config" is a name somebody could type
// by accident as easily as on purpose -- so anything that is not a letter, a
// digit, a dash or an underscore becomes a dash, and the result has to still
// have something in it.
func CleanName(name string) (string, error) {
	var out strings.Builder
	for _, letter := range strings.TrimSpace(name) {
		switch {
		case unicode.IsLetter(letter) || unicode.IsDigit(letter):
			out.WriteRune(unicode.ToLower(letter))
		case letter == '_':
			// Kept as itself. An underscore is safe in a filename and is what
			// people actually type; turning it into a dash is a name coming
			// back different from the one she chose, for no reason.
			out.WriteRune('_')
		case letter == '-' || unicode.IsSpace(letter):
			out.WriteRune('-')
		}
	}
	clean := strings.Trim(out.String(), "-")
	for strings.Contains(clean, "--") {
		clean = strings.ReplaceAll(clean, "--", "-")
	}
	if clean == "" {
		return "", &Error{Reason: "a voice needs a name with letters or numbers in it"}
	}
	if len(clean) > 48 {
		clean = strings.Trim(clean[:48], "-")
	}
	return clean, nil
}

// Suggest is a name that is not taken yet, for the first voice somebody adds.
func Suggest(preferred string) string {
	clean, err := CleanName(preferred)
	if err != nil {
		clean = "voice"
	}
	taken := map[string]bool{}
	for _, name := range Voices() {
		taken[name] = true
	}
	if !taken[clean] {
		return clean
	}
	for number := 2; number < 100; number++ {
		candidate := clean + "-" + itoa(number)
		if !taken[candidate] {
			return candidate
		}
	}
	return clean
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// encodeWAV writes mono 16-bit PCM at the model's own rate.
//
// Sixteen bit rather than float, because what this produces is a file somebody
// may well want to open, trim and put back -- and every audio editor in the
// world opens this.
func encodeWAV(samples []float32) []byte {
	body := &bytes.Buffer{}
	body.Grow(len(samples) * 2)
	for _, value := range samples {
		clamped := math.Max(-1, math.Min(1, float64(value)))
		_ = binary.Write(body, binary.LittleEndian, int16(clamped*32767))
	}

	out := &bytes.Buffer{}
	out.Grow(body.Len() + 44)
	out.WriteString("RIFF")
	_ = binary.Write(out, binary.LittleEndian, uint32(36+body.Len()))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(out, binary.LittleEndian, uint32(16))
	_ = binary.Write(out, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(out, binary.LittleEndian, uint16(1)) // mono
	_ = binary.Write(out, binary.LittleEndian, uint32(SampleRate))
	_ = binary.Write(out, binary.LittleEndian, uint32(SampleRate*2))
	_ = binary.Write(out, binary.LittleEndian, uint16(2))
	_ = binary.Write(out, binary.LittleEndian, uint16(16))
	out.WriteString("data")
	_ = binary.Write(out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}
