// Package devices enumerates and selects audio hardware.
//
// Windows exposes every physical device once per backend -- WASAPI, DirectSound,
// WinMM -- which on a normal machine turns seven real devices into thirty-one
// entries, with WinMM truncating the names to 31 characters. Reading that list
// aloud would be unusable, so the context is opened on one backend, preferring
// WASAPI: full device names, the lowest latency, and one entry per device.
package devices

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/gen2brain/malgo"

	"github.com/exzork/mikkilens/packages/core/fuzzy"
)

// Kind is which half of the sound card we mean.
type Kind string

const (
	Input  Kind = "input"
	Output Kind = "output"
)

// ErrNoDevice means no usable device of the requested kind exists.
var ErrNoDevice = errors.New("no audio device found")

// Backends in descending order of preference.
var preferredBackends = []struct {
	backend malgo.Backend
	name    string
}{
	{malgo.BackendWasapi, "Windows WASAPI"},
	{malgo.BackendDsound, "Windows DirectSound"},
	{malgo.BackendWinmm, "MME"},
}

// Device is one piece of audio hardware, as MikkiLens presents it.
type Device struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	HostAPI   string `json:"host_api"`
	IsDefault bool   `json:"is_default"`
	Kind      Kind   `json:"kind"`

	id malgo.DeviceID
}

// ID is the handle miniaudio needs to open this device.
func (d Device) ID() malgo.DeviceID { return d.id }

// Label is how the device is announced aloud and shown in the settings page.
func (d Device) Label() string {
	if d.IsDefault {
		return d.Name + " (default)"
	}
	return d.Name
}

// MarshalJSON adds the label, because every caller that sends a device
// somewhere wants to show it.
func (d Device) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(
		`{"index":%d,"name":%q,"label":%q,"host_api":%q,"is_default":%t,"kind":%q}`,
		d.Index, d.Name, d.Label(), d.HostAPI, d.IsDefault, d.Kind,
	)), nil
}

var (
	contextOnce sync.Once
	context     *malgo.AllocatedContext
	contextName string
	contextErr  error
)

// Context is the shared miniaudio context. Opening more than one is a good way
// to have Windows hand out an exclusive handle and leave something deaf, so
// every caller shares this one.
func Context() (*malgo.AllocatedContext, error) {
	contextOnce.Do(func() {
		for _, candidate := range preferredBackends {
			allocated, err := malgo.InitContext(
				[]malgo.Backend{candidate.backend}, malgo.ContextConfig{}, nil)
			if err != nil {
				continue
			}
			playback, _ := allocated.Devices(malgo.Playback)
			capture, _ := allocated.Devices(malgo.Capture)
			if len(playback) == 0 && len(capture) == 0 {
				_ = allocated.Uninit()
				allocated.Free()
				continue
			}
			context, contextName = allocated, candidate.name
			return
		}
		contextErr = fmt.Errorf("%w: no audio backend could be opened", ErrNoDevice)
	})
	return context, contextErr
}

// HostAPI names the backend actually in use.
func HostAPI() string {
	if _, err := Context(); err != nil {
		return ""
	}
	return contextName
}

// List returns the devices of one kind, one entry per physical device.
func List(kind Kind) ([]Device, error) {
	allocated, err := Context()
	if err != nil {
		return nil, err
	}
	deviceType := malgo.Playback
	if kind == Input {
		deviceType = malgo.Capture
	}
	found, err := allocated.Devices(deviceType)
	if err != nil {
		return nil, err
	}

	devices := make([]Device, 0, len(found))
	for index, info := range found {
		devices = append(devices, Device{
			Index:     index,
			Name:      strings.TrimSpace(info.Name()),
			HostAPI:   contextName,
			IsDefault: info.IsDefault != 0,
			Kind:      kind,
			id:        info.ID,
		})
	}
	return devices, nil
}

// Resolve turns a config value into a device.
//
// It accepts an empty string (meaning the system default), a device index, or
// any part of the device name -- so config can say "Headphones" and keep
// working after Windows renumbers its devices, which it does on every
// reconnect.
func Resolve(spec string, kind Kind) (*Device, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil // the caller falls back to the system default
	}

	devices, err := List(kind)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("%w: no %s devices", ErrNoDevice, kind)
	}

	if index, err := strconv.Atoi(spec); err == nil {
		for _, device := range devices {
			if device.Index == index {
				found := device
				return &found, nil
			}
		}
		return nil, nil // fall back to the default rather than failing
	}

	lowered := strings.ToLower(spec)
	for _, device := range devices {
		if strings.ToLower(device.Name) == lowered {
			found := device
			return &found, nil
		}
	}
	for _, device := range devices {
		if strings.Contains(strings.ToLower(device.Name), lowered) {
			found := device
			return &found, nil
		}
	}

	// Last resort: the name in config may itself be a mishearing or an old
	// name, so accept a close match rather than going silent.
	names := make([]string, len(devices))
	for index, device := range devices {
		names[index] = strings.ToLower(device.Name)
	}
	if index, score := fuzzy.ExtractOne(lowered, names, fuzzy.PartialRatio); score >= 80 {
		found := devices[index]
		return &found, nil
	}
	return nil, nil
}

// Default is the device Windows would pick.
func Default(kind Kind) (*Device, error) {
	devices, err := List(kind)
	if err != nil {
		return nil, err
	}
	for _, device := range devices {
		if device.IsDefault {
			found := device
			return &found, nil
		}
	}
	if len(devices) > 0 {
		return &devices[0], nil
	}
	return nil, fmt.Errorf("%w: no %s devices", ErrNoDevice, kind)
}

// Describe is the human-readable list the setup wizard reads aloud.
func Describe(kind Kind) []string {
	devices, err := List(kind)
	if err != nil {
		return nil
	}
	lines := make([]string, 0, len(devices))
	for index, device := range devices {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, device.Label()))
	}
	return lines
}

// Tone renders a short sine tone with fades on both ends, so it reads as a
// chime rather than as a click.
func Tone(frequency, duration float64, sampleRate int, volume float64) []float32 {
	samples := int(float64(sampleRate) * duration)
	if samples <= 0 {
		return nil
	}
	wave := make([]float32, samples)
	step := 2.0 * math.Pi * frequency / float64(sampleRate)
	for index := range wave {
		wave[index] = float32(math.Sin(step*float64(index)) * volume)
	}

	fade := int(float64(sampleRate) * 0.012)
	if fade < 1 {
		fade = 1
	}
	if samples > 2*fade {
		for index := 0; index < fade; index++ {
			ramp := float32(index) / float32(fade)
			wave[index] *= ramp
			wave[samples-1-index] *= ramp
		}
	}
	return wave
}
