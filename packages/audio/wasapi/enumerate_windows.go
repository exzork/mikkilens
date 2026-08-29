//go:build windows

package wasapi

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Direction is which half of the sound card we mean.
type Direction int

const (
	Render  Direction = 0 // eRender: speakers and headphones
	Capture Direction = 1 // eCapture: microphones
)

const (
	deviceStateActive = 0x1
	roleConsole       = 0
	stgmRead          = 0
)

// Endpoint is one audio device as Windows presents it.
type Endpoint struct {
	// ID is the endpoint's stable identifier. It survives reboots and
	// renumbering, which is what makes it safe to store.
	ID string

	// Name is what a person would recognise, such as
	// "Speakers (G733 Gaming Headset)".
	Name string

	IsDefault bool
	Direction Direction
}

// Endpoints lists the active devices of one direction.
//
// It runs on its own locked thread because COM apartments belong to threads,
// and the caller should not have to know that.
func Endpoints(direction Direction) ([]Endpoint, error) {
	type answer struct {
		endpoints []Endpoint
		err       error
	}
	done := make(chan answer, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		scope, err := enterCOM()
		if err != nil {
			done <- answer{err: err}
			return
		}
		defer scope.leave()

		endpoints, err := listEndpoints(direction)
		done <- answer{endpoints: endpoints, err: err}
	}()

	result := <-done
	return result.endpoints, result.err
}

func listEndpoints(direction Direction) ([]Endpoint, error) {
	enumerator, err := createEnumerator()
	if err != nil {
		return nil, err
	}
	defer release(enumerator)

	defaultID := defaultEndpointID(enumerator, direction)

	var collection iface
	hr := call(enumerator, 3, // EnumAudioEndpoints
		uintptr(direction), deviceStateActive, uintptr(unsafe.Pointer(&collection)))
	if err := check("EnumAudioEndpoints", hr); err != nil {
		return nil, err
	}
	defer release(collection)

	var count uint32
	if hr := call(collection, 3, uintptr(unsafe.Pointer(&count))); check("GetCount", hr) != nil {
		return nil, check("GetCount", hr)
	}

	endpoints := make([]Endpoint, 0, count)
	for index := uint32(0); index < count; index++ {
		var device iface
		if hr := call(collection, 4, uintptr(index), uintptr(unsafe.Pointer(&device))); int32(hr) < 0 {
			continue
		}

		id := deviceID(device)
		name := deviceName(device)
		release(device)

		if id == "" {
			continue
		}
		if name == "" {
			name = id
		}
		endpoints = append(endpoints, Endpoint{
			ID: id, Name: name, IsDefault: id == defaultID, Direction: direction,
		})
	}
	return endpoints, nil
}

func defaultEndpointID(enumerator iface, direction Direction) string {
	var device iface
	hr := call(enumerator, 4, // GetDefaultAudioEndpoint
		uintptr(direction), roleConsole, uintptr(unsafe.Pointer(&device)))
	if int32(hr) < 0 {
		return "" // no default is normal on a machine with no sound card
	}
	defer release(device)
	return deviceID(device)
}

func deviceID(device iface) string {
	var raw *uint16
	if hr := call(device, 5, uintptr(unsafe.Pointer(&raw))); int32(hr) < 0 {
		return ""
	}
	defer coTaskMemFree(unsafe.Pointer(raw))
	return windows.UTF16PtrToString(raw)
}

func deviceName(device iface) string {
	var store iface
	hr := call(device, 4, stgmRead, uintptr(unsafe.Pointer(&store))) // OpenPropertyStore
	if int32(hr) < 0 {
		return ""
	}
	defer release(store)

	var value propVariant
	hr = call(store, 5, // GetValue
		uintptr(unsafe.Pointer(&keyDeviceFriendlyName)), uintptr(unsafe.Pointer(&value)))
	if int32(hr) < 0 {
		return ""
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(&value)))

	if value.vt != vtLPWSTR || value.value == nil {
		return ""
	}
	return windows.UTF16PtrToString((*uint16)(value.value))
}

// openDevice finds one endpoint by id, or the default when the id is empty.
func openDevice(enumerator iface, id string, direction Direction) (iface, error) {
	if id == "" {
		var device iface
		hr := call(enumerator, 4, uintptr(direction), roleConsole, uintptr(unsafe.Pointer(&device)))
		if err := check("GetDefaultAudioEndpoint", hr); err != nil {
			return nil, err
		}
		return device, nil
	}

	wide, err := windows.UTF16PtrFromString(id)
	if err != nil {
		return nil, err
	}
	var device iface
	hr := call(enumerator, 5, // GetDevice
		uintptr(unsafe.Pointer(wide)), uintptr(unsafe.Pointer(&device)))
	if err := check("GetDevice", hr); err != nil {
		return nil, err
	}
	return device, nil
}
