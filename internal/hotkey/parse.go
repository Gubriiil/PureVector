// Package hotkey provides a global toggle hotkey (Windows) so OmniVector
// can be switched on and off while a game has focus.
package hotkey

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Windows modifier flags and virtual key codes.
const (
	ModAlt      = 0x0001
	ModControl  = 0x0002
	ModShift    = 0x0004
	ModWin      = 0x0008
	ModNoRepeat = 0x4000
)

// ErrUnsupported is returned on platforms without global hotkeys.
var ErrUnsupported = errors.New("globale Tastenkürzel werden auf diesem System nicht unterstützt")

var namedKeys = map[string]uint32{
	"pause": 0x13, "scrolllock": 0x91, "insert": 0x2D, "delete": 0x2E, "home": 0x24, "end": 0x23,
	"pageup": 0x21, "pagedown": 0x22, "space": 0x20, "numlock": 0x90,
}

// Parse converts "ctrl+alt+f10" into Windows modifiers and a virtual key.
func Parse(spec string) (mods, vk uint32, err error) {
	parts := strings.Split(strings.ToLower(strings.ReplaceAll(spec, " ", "")), "+")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("Tastenkürzel %q: mindestens eine Zusatztaste und eine Taste angeben, z. B. ctrl+alt+f10", spec)
	}
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl", "control", "strg":
			mods |= ModControl
		case "alt":
			mods |= ModAlt
		case "shift", "umschalt":
			mods |= ModShift
		case "win", "super":
			mods |= ModWin
		default:
			return 0, 0, fmt.Errorf("Tastenkürzel %q: unbekannte Zusatztaste %q", spec, p)
		}
	}
	key := parts[len(parts)-1]
	switch {
	case len(key) == 1 && key[0] >= 'a' && key[0] <= 'z':
		vk = uint32(key[0]-'a') + 0x41
	case len(key) == 1 && key[0] >= '0' && key[0] <= '9':
		vk = uint32(key[0]-'0') + 0x30
	case len(key) >= 2 && key[0] == 'f':
		n, err := strconv.Atoi(key[1:])
		if err != nil || n < 1 || n > 24 {
			return 0, 0, fmt.Errorf("Tastenkürzel %q: unbekannte Taste %q", spec, key)
		}
		vk = 0x70 + uint32(n-1)
	default:
		v, ok := namedKeys[key]
		if !ok {
			return 0, 0, fmt.Errorf("Tastenkürzel %q: unbekannte Taste %q", spec, key)
		}
		vk = v
	}
	return mods, vk, nil
}
