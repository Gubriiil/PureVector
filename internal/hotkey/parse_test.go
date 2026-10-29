package hotkey

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in       string
		mods, vk uint32
	}{
		{"ctrl+alt+f10", ModControl | ModAlt, 0x79},
		{"Strg + Umschalt + P", ModControl | ModShift, 0x50},
		{"alt+1", ModAlt, 0x31},
		{"win+pause", ModWin, 0x13},
		{"ctrl+f24", ModControl, 0x87},
	}
	for _, c := range cases {
		m, v, err := Parse(c.in)
		if err != nil || m != c.mods || v != c.vk {
			t.Errorf("Parse(%q) = %#x %#x %v, want %#x %#x", c.in, m, v, err, c.mods, c.vk)
		}
	}
	for _, bad := range []string{"f10", "ctrl+", "hyper+a", "ctrl+f25", "ctrl+ä"} {
		if _, _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}
