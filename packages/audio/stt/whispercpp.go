package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// whisperCPP drives a whisper.cpp build as a child process.
//
// Running it out of process rather than linking it in is a deliberate trade.
// It costs a few tens of milliseconds per command to start, against a build
// that needs no CGO toolchain and no CUDA SDK on the streaming machine: she
// drops in whichever prebuilt whisper.cpp suits her GPU -- CUDA, Vulkan, or
// plain CPU -- and MikkiLens uses it without being rebuilt.
type whisperCPP struct {
	binary    string
	modelPath string
	beamSize  int
	threads   int
	label     string
}

// candidateBinaries are the names whisper.cpp has shipped its CLI under.
var candidateBinaries = []string{
	"whisper-cli.exe", "whisper-cli",
	"main.exe", "main",
	"whisper.exe", "whisper",
}

func newWhisperCPP(settings config.STT) (Backend, error) {
	binary, err := findBinary(settings.Binary)
	if err != nil {
		return nil, err
	}
	model, err := findModel(settings.ModelPath, settings.ModelSize)
	if err != nil {
		return nil, err
	}

	beam := settings.BeamSize
	if beam < 1 {
		beam = 1
	}

	return &whisperCPP{
		binary:    binary,
		modelPath: model,
		beamSize:  beam,
		threads:   recognitionThreads(),
		label: fmt.Sprintf("whisper.cpp %s (%s)",
			strings.TrimSuffix(filepath.Base(model), filepath.Ext(model)),
			filepath.Base(binary)),
	}, nil
}

// binarySearchPath is where a person would actually put a whisper.cpp build.
func binarySearchPath() []string {
	return []string{
		paths.ModelsDir(),
		filepath.Join(paths.ModelsDir(), "whisper"),
		filepath.Join(paths.Root(), "vendor", "whisper"),
	}
}

// recognitionThreads leaves the machine some room.
//
// Recognition runs while she is streaming, and taking every core would stutter
// the encode she is actually broadcasting. Half the machine, capped, is plenty
// for a few seconds of speech.
func recognitionThreads() int {
	threads := runtime.NumCPU() / 2
	if threads < 1 {
		threads = 1
	}
	if threads > 8 {
		threads = 8
	}
	return threads
}

func findBinary(configured string) (string, error) {
	return findNamedBinary(configured, candidateBinaries)
}

// findModel looks for a GGML model matching the configured size.
func findModel(configured, size string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
		return "", &Error{Reason: "the configured speech model was not found: " + configured}
	}
	if size == "" {
		size = "small"
	}

	searchDirs := []string{
		paths.ModelsDir(),
		filepath.Join(paths.ModelsDir(), "whisper"),
	}
	wanted := []string{
		"ggml-" + size + ".bin",
		"ggml-" + size + ".en.bin",
		"ggml-" + size + "-q5_1.bin",
		"ggml-" + size + "-q8_0.bin",
	}
	for _, directory := range searchDirs {
		for _, name := range wanted {
			candidate := filepath.Join(directory, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		// Failing an exact match, take any GGML model rather than refusing to
		// listen at all: the wrong size still recognizes speech.
		if entries, err := os.ReadDir(directory); err == nil {
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasPrefix(name, "ggml-") && strings.HasSuffix(name, ".bin") {
					return filepath.Join(directory, name), nil
				}
			}
		}
	}
	return "", &Error{Reason: "no speech model was found in " + paths.ModelsDir()}
}

func (w *whisperCPP) Name() string { return w.label }

// Load checks the model is readable now, rather than discovering it is not on
// the first command she speaks.
func (w *whisperCPP) Load(context.Context) error {
	if _, err := os.Stat(w.modelPath); err != nil {
		return &Error{Reason: "the speech model could not be read: " + err.Error()}
	}
	return nil
}

func (w *whisperCPP) Close() error { return nil }

func (w *whisperCPP) Transcribe(ctx context.Context, audio []float32, language string) (Transcript, error) {
	directory, err := os.MkdirTemp("", "mikkilens-listen-")
	if err != nil {
		return Transcript{}, &Error{Reason: "could not write the recording to disk: " + err.Error()}
	}
	defer os.RemoveAll(directory)

	wavPath := filepath.Join(directory, "utterance.wav")
	if err := os.WriteFile(wavPath, encodeWAV(audio, SampleRate), 0o644); err != nil {
		return Transcript{}, &Error{Reason: "could not write the recording to disk: " + err.Error()}
	}

	arguments := []string{
		"--model", w.modelPath,
		"--file", wavPath,
		"--output-json",
		"--output-file", filepath.Join(directory, "result"),
		"--beam-size", strconv.Itoa(w.beamSize),
		"--threads", strconv.Itoa(w.threads),
		"--no-timestamps",
		"--no-prints",
	}
	if language != "" && language != "auto" {
		arguments = append(arguments, "--language", language)
	}

	command := exec.CommandContext(ctx, w.binary, arguments...)
	hideConsole(command)

	var stderr bytes.Buffer
	command.Stderr = &stderr
	var stdout bytes.Buffer
	command.Stdout = &stdout

	if err := command.Run(); err != nil {
		return Transcript{}, &Error{
			Reason: "speech recognition failed: " + summarize(stderr.String(), err.Error()),
		}
	}

	if parsed, ok := readWhisperJSON(filepath.Join(directory, "result.json")); ok {
		return parsed, nil
	}
	// Older builds ignore --output-json and simply print the text.
	return Transcript{Text: cleanTranscript(stdout.String()), Language: language}, nil
}

// whisperJSON is the subset of whisper.cpp's output we care about.
type whisperJSON struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []struct {
		Text string `json:"text"`
	} `json:"transcription"`
}

func readWhisperJSON(path string) (Transcript, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Transcript{}, false
	}
	var parsed whisperJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Transcript{}, false
	}
	parts := make([]string, 0, len(parsed.Transcription))
	for _, segment := range parsed.Transcription {
		if trimmed := strings.TrimSpace(segment.Text); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return Transcript{
		Text:     cleanTranscript(strings.Join(parts, " ")),
		Language: parsed.Result.Language,
	}, true
}

// encodeWAV wraps mono float32 audio as 16-bit PCM, which is what every
// whisper build reads without argument.
func encodeWAV(audio []float32, sampleRate int) []byte {
	const bitsPerSample = 16
	const channels = 1

	body := make([]byte, len(audio)*2)
	for index, sample := range audio {
		clamped := math.Max(-1, math.Min(1, float64(sample)))
		binary.LittleEndian.PutUint16(body[index*2:], uint16(int16(clamped*32767)))
	}

	header := &bytes.Buffer{}
	header.WriteString("RIFF")
	_ = binary.Write(header, binary.LittleEndian, uint32(36+len(body)))
	header.WriteString("WAVE")
	header.WriteString("fmt ")
	_ = binary.Write(header, binary.LittleEndian, uint32(16))
	_ = binary.Write(header, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(header, binary.LittleEndian, uint16(channels))
	_ = binary.Write(header, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(header, binary.LittleEndian, uint32(sampleRate*channels*bitsPerSample/8))
	_ = binary.Write(header, binary.LittleEndian, uint16(channels*bitsPerSample/8))
	_ = binary.Write(header, binary.LittleEndian, uint16(bitsPerSample))
	header.WriteString("data")
	_ = binary.Write(header, binary.LittleEndian, uint32(len(body)))

	return append(header.Bytes(), body...)
}

func summarize(candidates ...string) string {
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		lines := strings.Split(trimmed, "\n")
		last := strings.TrimSpace(lines[len(lines)-1])
		if last == "" && len(lines) > 1 {
			last = strings.TrimSpace(lines[len(lines)-2])
		}
		if len(last) > 200 {
			last = last[:200]
		}
		return last
	}
	return "unknown error"
}
