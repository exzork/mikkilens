//go:build windows

package tts

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The offline Windows voice sits behind the online one, because a dropped
// connection must never turn into silence.
//
// It renders to a WAV file rather than speaking directly, which keeps the
// routing guarantee intact: MikkiLens still chooses which device its voice
// comes out of, even on the fallback path. Speaking through SAPI's own output
// would send it wherever Windows felt like, which on a streaming machine can
// mean straight onto the broadcast.

const sapiScript = `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Speech
$synth = New-Object System.Speech.Synthesis.SpeechSynthesizer
$synth.Rate = %RATE%
$synth.SetOutputToWaveFile('%PATH%')
$synth.Speak([Console]::In.ReadToEnd())
$synth.Dispose()
`

// SynthesizeSAPI renders speech with the built-in Windows voice. It needs no
// network and no API key.
func SynthesizeSAPI(ctx context.Context, text string, ratePercent int) (Audio, error) {
	powershell, err := exec.LookPath("powershell")
	if err != nil {
		powershell, err = exec.LookPath("pwsh")
		if err != nil {
			return Audio{}, failure("the offline voice needs PowerShell, which was not found")
		}
	}

	directory, err := os.MkdirTemp("", "mikkilens-speech-")
	if err != nil {
		return Audio{}, failure("could not write the offline voice to disk: %v", err)
	}
	defer os.RemoveAll(directory)
	wav := filepath.Join(directory, "speech.wav")

	// SAPI's rate is -10 to 10, which is roughly a percentage divided by ten.
	rate := max(-10, min(10, (ratePercent+5)/10))
	script := strings.NewReplacer(
		"%RATE%", itoa(rate),
		"%PATH%", strings.ReplaceAll(wav, "'", "''"),
	).Replace(sapiScript)

	timed, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	command := exec.CommandContext(timed, powershell,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.Stdin = strings.NewReader(text)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	var stderr bytes.Buffer
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		return Audio{}, failure("the offline voice failed: %s", firstLine(stderr.String(), err.Error()))
	}
	data, err := os.ReadFile(wav)
	if err != nil {
		return Audio{}, failure("the offline voice produced no audio")
	}
	return Decode(data)
}

func firstLine(candidates ...string) string {
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if index := strings.IndexAny(trimmed, "\r\n"); index > 0 {
			trimmed = trimmed[:index]
		}
		if len(trimmed) > 200 {
			trimmed = trimmed[:200]
		}
		return trimmed
	}
	return "unknown error"
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
