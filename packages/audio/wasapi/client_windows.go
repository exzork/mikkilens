//go:build windows

package wasapi

import (
	"unsafe"
)

// Shared-mode streaming, with WASAPI doing the format conversion.
//
// AUTOCONVERTPCM lets us ask for exactly the format the rest of MikkiLens
// works in -- float32 at 16 kHz mono for recognition, whatever the voice
// produced for playback -- and have Windows resample to whatever the device
// actually runs at. That removes a resampler, and with it a whole class of
// subtle quality bugs, from our side of the line.
const (
	shareModeShared = 0

	streamFlagsAutoConvertPCM    = 0x80000000
	streamFlagsSRCDefaultQuality = 0x08000000
	streamFlagsAutoConvert       = streamFlagsAutoConvertPCM | streamFlagsSRCDefaultQuality

	waveFormatIEEEFloat = 3

	// referenceTimesPerSecond is the unit WASAPI states buffer durations in.
	referenceTimesPerSecond = 10_000_000

	// bufferDuration is how much audio the device buffers. Long enough to be
	// robust when OBS is encoding hard, short enough that an interrupt is
	// heard promptly.
	bufferDuration = 2 * referenceTimesPerSecond / 10 // 200 ms

	audioClientBufferEmpty = 0x08890001
)

// waveFormatEx is the classic format descriptor.
type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

func floatFormat(sampleRate, channels int) waveFormatEx {
	const bits = 32
	blockAlign := channels * bits / 8
	return waveFormatEx{
		FormatTag:      waveFormatIEEEFloat,
		Channels:       uint16(channels),
		SamplesPerSec:  uint32(sampleRate),
		AvgBytesPerSec: uint32(sampleRate * blockAlign),
		BlockAlign:     uint16(blockAlign),
		BitsPerSample:  bits,
		Size:           0,
	}
}

// audioClient is one initialized stream on one device.
type audioClient struct {
	device iface
	client iface
	format waveFormatEx
	frames uint32 // the device buffer size, in frames
}

// activate opens an IAudioClient on a device and initializes it.
//
// It asks for the wanted format first. If the driver refuses even with
// conversion enabled, it falls back to the device's own mix format and reports
// it, so the caller can convert rather than go silent.
func activate(device iface, direction Direction, sampleRate, channels int) (*audioClient, error) {
	var client iface
	hr := call(device, 3, // Activate
		uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0,
		uintptr(unsafe.Pointer(&client)))
	if err := check("IMMDevice::Activate", hr); err != nil {
		return nil, err
	}

	wanted := floatFormat(sampleRate, channels)
	hr = call(client, 3, // Initialize
		shareModeShared, streamFlagsAutoConvert,
		uintptr(bufferDuration), 0,
		uintptr(unsafe.Pointer(&wanted)), 0)

	if int32(hr) < 0 {
		// Some drivers refuse the conversion flags. The mix format is always
		// accepted, so take it and convert on our side instead of failing.
		mix, mixErr := mixFormat(client)
		if mixErr != nil {
			release(client)
			return nil, check("IAudioClient::Initialize", hr)
		}
		hr = call(client, 3,
			shareModeShared, 0, uintptr(bufferDuration), 0,
			uintptr(unsafe.Pointer(mix)), 0)
		if err := check("IAudioClient::Initialize", hr); err != nil {
			release(client)
			return nil, err
		}
		wanted = *mix
	}

	var frames uint32
	if hr := call(client, 4, uintptr(unsafe.Pointer(&frames))); int32(hr) < 0 { // GetBufferSize
		release(client)
		return nil, check("IAudioClient::GetBufferSize", hr)
	}

	return &audioClient{device: device, client: client, format: wanted, frames: frames}, nil
}

// mixFormat is the format the device is actually mixing at.
func mixFormat(client iface) (*waveFormatEx, error) {
	var pointer *waveFormatEx
	hr := call(client, 8, uintptr(unsafe.Pointer(&pointer))) // GetMixFormat
	if err := check("IAudioClient::GetMixFormat", hr); err != nil {
		return nil, err
	}
	defer coTaskMemFree(unsafe.Pointer(pointer))

	// Copy it out before freeing: the caller keeps it past this scope.
	copied := *pointer
	return &copied, nil
}

func (a *audioClient) service(iid *GUID) (iface, error) {
	var out iface
	hr := call(a.client, 14, // GetService
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if err := check("IAudioClient::GetService", hr); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *audioClient) start() error { return check("IAudioClient::Start", call(a.client, 10)) }
func (a *audioClient) stop() error  { return check("IAudioClient::Stop", call(a.client, 11)) }

func (a *audioClient) padding() (uint32, error) {
	var frames uint32
	hr := call(a.client, 6, uintptr(unsafe.Pointer(&frames))) // GetCurrentPadding
	if err := check("IAudioClient::GetCurrentPadding", hr); err != nil {
		return 0, err
	}
	return frames, nil
}

func (a *audioClient) close() {
	release(a.client)
	release(a.device)
}

// SampleRate is the rate the stream is actually running at, which may differ
// from the one asked for when the driver refused the conversion.
func (a *audioClient) sampleRate() int { return int(a.format.SamplesPerSec) }

// Channels is the channel count actually in use.
func (a *audioClient) channels() int { return int(a.format.Channels) }

// isFloat32 reports whether the stream carries the format we prefer. When it
// does not, the caller has to convert.
func (a *audioClient) isFloat32() bool {
	return a.format.BitsPerSample == 32 &&
		(a.format.FormatTag == waveFormatIEEEFloat || a.format.FormatTag == 0xFFFE)
}
