package backend

// Platform-independent part of the Windows Interception backend. The DLL
// binding lives in interception_windows.go; keeping the loop here lets it be
// tested on any OS with a fake driver.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Gubriiil/PureVector/internal/engine"
)

// Constants from interception.h.
const (
	icMaxKeyboard = 10
	icMaxMouse    = 10
	icMaxDevice   = icMaxKeyboard + icMaxMouse

	// icFilterMouseAll intercepts every mouse packet: movement that arrives
	// together with a button change is transformed as well, and the order
	// of all packets is preserved.
	icFilterMouseAll = 0xFFFF

	icMouseMoveAbsolute = 0x001
)

// icMouseStroke mirrors InterceptionMouseStroke (20 bytes, same layout).
type icMouseStroke struct {
	State       uint16
	Flags       uint16
	Rolling     int16
	_           int16
	X           int32
	Y           int32
	Information uint32
}

func icIsMouse(dev int) bool { return dev > icMaxKeyboard && dev <= icMaxDevice }

// icDriver is the subset of the Interception API the loop needs.
type icDriver interface {
	// Wait blocks up to timeoutMs and returns a device with pending input
	// (1..20) or 0 on timeout.
	Wait(timeoutMs uint32) int
	Receive(dev int, s *icMouseStroke) int
	Send(dev int, s *icMouseStroke) int
	// HardwareID returns the first hardware ID string of a device slot or
	// "" if the slot is empty.
	HardwareID(dev int) string
}

type icDevice struct {
	hwid   string
	dev    *engine.Device
	lastNs int64
}

// icRefreshNs: after this much idle time a device slot's hardware ID is
// queried again (the slot may now belong to a different, replugged mouse).
const icRefreshNs = int64(1_000_000_000)

type icLoop struct {
	drv    icDriver
	eng    *engine.Engine
	now    func() int64
	names  func(key string) string
	logf   func(format string, args ...any)
	devs   [icMaxDevice + 1]*icDevice
	panics int
}

// run processes strokes until ctx is cancelled. Every received stroke is
// sent on, so input keeps flowing even if a transform fails.
func (l *icLoop) run(ctx context.Context) {
	s := new(icMouseStroke) // reused: no allocation per packet
	for ctx.Err() == nil {
		dev := l.drv.Wait(100)
		if dev <= 0 || dev > icMaxDevice {
			continue
		}
		*s = icMouseStroke{}
		if l.drv.Receive(dev, s) <= 0 {
			continue
		}
		if icIsMouse(dev) && s.Flags&icMouseMoveAbsolute == 0 && (s.X != 0 || s.Y != 0) {
			l.transform(dev, s)
		}
		l.drv.Send(dev, s)
	}
}

func (l *icLoop) transform(dev int, s *icMouseStroke) {
	defer func() {
		// Leave the stroke unchanged on a bug; input must never stop.
		if r := recover(); r != nil {
			l.panics++
			if l.logf != nil && l.panics <= 3 {
				l.logf("Interner Fehler bei der Verarbeitung (Bewegung unverändert weitergegeben): %v", r)
			}
		}
	}()
	t := l.now()
	st := l.devs[dev]
	if st == nil || t-st.lastNs > icRefreshNs {
		st = l.resolve(dev, st)
	}
	st.lastNs = t
	s.X, s.Y = l.eng.Process(st.dev, s.X, s.Y, t)
}

func (l *icLoop) resolve(dev int, old *icDevice) *icDevice {
	hwid := l.drv.HardwareID(dev)
	if old != nil && old.hwid == hwid {
		return old
	}
	if old != nil {
		l.eng.Disconnected(old.dev)
	}
	key, name := describeHardwareID(hwid, dev)
	if l.names != nil {
		if n := l.names(key); n != "" {
			name = n
		}
	}
	st := &icDevice{hwid: hwid, dev: l.eng.Device(key, name, "interception")}
	l.devs[dev] = st
	return st
}

// describeHardwareID derives a stable device key and a readable name from a
// Windows hardware ID such as "HID\VID_1B1C&PID_1BAC&REV_0102&MI_00&Col01".
func describeHardwareID(hwid string, slot int) (key, name string) {
	vid, pid, ok := parseVidPid(hwid)
	if ok {
		key = fmt.Sprintf("VID_%04X&PID_%04X", vid, pid)
		if v := vendorName(vid); v != "" {
			return key, fmt.Sprintf("%s-Maus (%04X:%04X)", v, vid, pid)
		}
		return key, fmt.Sprintf("Maus %04X:%04X", vid, pid)
	}
	h := strings.ToUpper(strings.TrimSpace(hwid))
	switch {
	case h == "":
		return fmt.Sprintf("SLOT_%d", slot), fmt.Sprintf("Maus (Slot %d)", slot)
	case strings.Contains(h, "PNP0F"):
		return h, "PS/2-Maus"
	default:
		return h, "Maus " + h
	}
}

// parseVidPid understands USB style IDs ("VID_046D&PID_C08B") and the
// Bluetooth form ("VID&0002046D_PID&B023", vendor ID source + vendor ID).
func parseVidPid(hwid string) (vid, pid uint16, ok bool) {
	h := strings.ToUpper(hwid)
	hex4 := func(at int) (uint16, bool) {
		if at < 0 || at+4 > len(h) {
			return 0, false
		}
		v, err := strconv.ParseUint(h[at:at+4], 16, 16)
		return uint16(v), err == nil
	}
	if vi, pi := strings.Index(h, "VID_"), strings.Index(h, "PID_"); vi >= 0 && pi >= 0 {
		v, ok1 := hex4(vi + 4)
		p, ok2 := hex4(pi + 4)
		if ok1 && ok2 {
			return v, p, true
		}
	}
	if vi, pi := strings.Index(h, "VID&"), strings.Index(h, "PID&"); vi >= 0 && pi >= 0 {
		v, ok1 := hex4(vi + 8) // skip the 4 digit vendor ID source
		p, ok2 := hex4(pi + 4)
		if ok1 && ok2 {
			return v, p, true
		}
	}
	return 0, 0, false
}

// vendorName maps well-known USB vendor IDs of mouse makers.
func vendorName(vid uint16) string {
	switch vid {
	case 0x046D:
		return "Logitech"
	case 0x1532:
		return "Razer"
	case 0x1B1C:
		return "Corsair"
	case 0x1038:
		return "SteelSeries"
	case 0x045E:
		return "Microsoft"
	case 0x0951, 0x03F0:
		return "HyperX"
	case 0x1E7D:
		return "ROCCAT"
	case 0x0B05:
		return "ASUS"
	case 0x2516:
		return "Cooler Master"
	case 0x258A:
		return "Sinowealth"
	case 0x093A:
		return "PixArt"
	case 0x17EF:
		return "Lenovo"
	case 0x413C:
		return "Dell"
	case 0x04F2:
		return "Chicony"
	case 0x0458:
		return "Genius"
	}
	return ""
}

// utf16MultiSzFirst extracts the first string of a REG_MULTI_SZ buffer.
func utf16MultiSzFirst(b []uint16) string {
	end := 0
	for end < len(b) && b[end] != 0 {
		end++
	}
	r := make([]rune, 0, end)
	for i := 0; i < end; i++ {
		c := b[i]
		if c >= 0xD800 && c < 0xDC00 && i+1 < end {
			lo := b[i+1]
			if lo >= 0xDC00 && lo < 0xE000 {
				r = append(r, (rune(c)-0xD800)<<10+(rune(lo)-0xDC00)+0x10000)
				i++
				continue
			}
		}
		r = append(r, rune(c))
	}
	return string(r)
}
