//go:build windows

package hotkey

// virtualKey maps the names the config file uses to Windows virtual key codes.
//
// The names match what the Python version accepted, so an existing
// config.toml keeps working: <ctrl>+<alt>+<space> means the same thing here.
func virtualKey(name string) (uint32, bool) {
	if code, ok := namedKeys[name]; ok {
		return code, true
	}
	// A single letter or digit is its own code on Windows.
	if len(name) == 1 {
		character := name[0]
		if character >= 'a' && character <= 'z' {
			return uint32(character - 'a' + 'A'), true
		}
		if character >= '0' && character <= '9' {
			return uint32(character), true
		}
	}
	return 0, false
}

var namedKeys = map[string]uint32{
	"ctrl": 0x11, "control": 0x11,
	"ctrl_l": 0xA2, "ctrl_r": 0xA3,
	"alt": 0x12, "alt_l": 0xA4, "alt_r": 0xA5, "alt_gr": 0xA5,
	"shift": 0x10, "shift_l": 0xA0, "shift_r": 0xA1,
	"cmd": 0x5B, "win": 0x5B, "super": 0x5B, "cmd_l": 0x5B, "cmd_r": 0x5C,

	"space": 0x20, "enter": 0x0D, "return": 0x0D, "tab": 0x09,
	"esc": 0x1B, "escape": 0x1B, "backspace": 0x08, "delete": 0x2E,
	"insert": 0x2D, "home": 0x24, "end": 0x23,
	"page_up": 0x21, "page_down": 0x22,
	"up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
	"caps_lock": 0x14, "num_lock": 0x90, "scroll_lock": 0x91,
	"print_screen": 0x2C, "pause": 0x13, "menu": 0x5D,

	"f1": 0x70, "f2": 0x71, "f3": 0x72, "f4": 0x73,
	"f5": 0x74, "f6": 0x75, "f7": 0x76, "f8": 0x77,
	"f9": 0x78, "f10": 0x79, "f11": 0x7A, "f12": 0x7B,
	"f13": 0x7C, "f14": 0x7D, "f15": 0x7E, "f16": 0x7F,
	"f17": 0x80, "f18": 0x81, "f19": 0x82, "f20": 0x83,
	"f21": 0x84, "f22": 0x85, "f23": 0x86, "f24": 0x87,

	// Numeric keypad, which is where a lot of foot pedals and macro keys land.
	"num_0": 0x60, "num_1": 0x61, "num_2": 0x62, "num_3": 0x63, "num_4": 0x64,
	"num_5": 0x65, "num_6": 0x66, "num_7": 0x67, "num_8": 0x68, "num_9": 0x69,
	"num_multiply": 0x6A, "num_add": 0x6B, "num_subtract": 0x6D,
	"num_decimal": 0x6E, "num_divide": 0x6F,
}
