//go:build windows

// Package wasapi is a small, pure-Go binding to the Windows audio stack.
//
// It replaces a cgo binding to miniaudio. Dropping cgo removes the C
// toolchain from the build entirely, which matters more than it sounds: a
// MinGW old enough to emit debug sections at a virtual address outside the
// image produces an executable Windows refuses to start, with an error
// ("not a valid application for this OS platform") that points nowhere near
// the cause. A pure-Go build cannot fail that way, and it cross-compiles.
//
// Only the parts MikkiLens needs are bound: enumerate the endpoints, play a
// buffer, and capture a stream. COM is reached through vtable calls rather
// than a generated binding, because six interfaces are easier to read than a
// dependency.
package wasapi

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
	procPropVariantClear = ole32.NewProc("PropVariantClear")
)

// COM apartment and class context values.
const (
	coinitMultithreaded = 0x0
	clsctxAll           = 0x17

	// rpcEChangedMode means COM is already up on this thread in the other
	// apartment model. That is fine to work with, but we must not uninitialize.
	rpcEChangedMode = 0x80010106
	sFalse          = 0x00000001
)

// GUID is the COM identifier layout.
type GUID = windows.GUID

func guid(text string) GUID {
	parsed, err := windows.GUIDFromString(text)
	if err != nil {
		panic("wasapi: bad GUID " + text)
	}
	return parsed
}

var (
	clsidMMDeviceEnumerator = guid("{BCDE0395-E52F-467C-8E3D-C4579291692E}")
	iidIMMDeviceEnumerator  = guid("{A95664D2-9614-4F35-A746-DE8DB63617E6}")
	iidIAudioClient         = guid("{1CB9AD4C-DBFA-4C32-B178-C2F568A703B2}")
	iidIAudioRenderClient   = guid("{F294ACFC-3146-4483-A7BF-ADDCA7C260E2}")
	iidIAudioCaptureClient  = guid("{C8ADBD64-E71E-48A0-A4DE-185C395CD317}")
)

// propertyKey identifies one property on a device.
type propertyKey struct {
	FormatID GUID
	PropID   uint32
}

// keyDeviceFriendlyName is the name a person would recognise, such as
// "Speakers (G733 Gaming Headset)".
var keyDeviceFriendlyName = propertyKey{
	FormatID: guid("{A45C254E-DF1C-4EFD-8020-67D146A850E0}"),
	PropID:   14,
}

// propVariant is only ever read for a string here, so the tail is opaque.
type propVariant struct {
	vt        uint16
	reserved1 uint16
	reserved2 uint16
	reserved3 uint16
	value     unsafe.Pointer
	_         uintptr
}

const vtLPWSTR = 31

// Error is a COM failure, with the HRESULT kept for diagnosis.
type Error struct {
	Op      string
	HResult uint32
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s failed (HRESULT 0x%08X)", e.Op, e.HResult)
}

func check(op string, hr uintptr) error {
	if int32(hr) < 0 {
		return &Error{Op: op, HResult: uint32(hr)}
	}
	return nil
}

// comScope initializes COM for the calling thread.
//
// The caller must already have locked the goroutine to its OS thread: COM
// apartments belong to threads, and letting the Go scheduler move a goroutine
// mid-stream is how you get a silent, intermittent failure.
type comScope struct{ owned bool }

func enterCOM() (*comScope, error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
	switch uint32(hr) {
	case 0, sFalse:
		return &comScope{owned: true}, nil
	case rpcEChangedMode:
		// Someone else set the apartment model; we can still make calls.
		return &comScope{owned: false}, nil
	default:
		return nil, check("CoInitializeEx", hr)
	}
}

func (c *comScope) leave() {
	if c != nil && c.owned {
		procCoUninitialize.Call()
	}
}

// iface is a COM interface pointer.
//
// It is unsafe.Pointer rather than uintptr on purpose: COM hands back real
// pointers, and keeping them typed means the garbage collector understands
// them and `go vet` does not have to guess.
type iface = unsafe.Pointer

// vtable reaches a COM method by index. The first three slots are IUnknown.
func vtable(object iface) *[64]uintptr {
	return *(**[64]uintptr)(object)
}

// call invokes one COM method, passing the interface pointer as `this`.
func call(object iface, index int, args ...uintptr) uintptr {
	arguments := make([]uintptr, 0, len(args)+1)
	arguments = append(arguments, uintptr(object))
	arguments = append(arguments, args...)

	result, _, _ := syscall.SyscallN(vtable(object)[index], arguments...)
	return result
}

// release drops a reference. Every interface obtained here needs exactly one.
func release(object iface) {
	if object != nil {
		call(object, 2)
	}
}

func coTaskMemFree(pointer unsafe.Pointer) {
	if pointer != nil {
		procCoTaskMemFree.Call(uintptr(pointer))
	}
}

// createEnumerator builds the device enumerator, the entry point to everything
// else in this package.
func createEnumerator() (iface, error) {
	var enumerator iface
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)),
		0,
		clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enumerator)),
	)
	if err := check("CoCreateInstance(MMDeviceEnumerator)", hr); err != nil {
		return nil, err
	}
	return enumerator, nil
}
