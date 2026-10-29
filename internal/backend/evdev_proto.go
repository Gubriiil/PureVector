package backend

// Platform-independent part of the Linux evdev backend: the input_event
// wire format and per-frame processing. Kept free of syscalls so it can be
// tested anywhere.

import (
	"encoding/binary"
	"unsafe"
)

// Linux input constants (linux/input-event-codes.h).
const (
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03
	evMsc = 0x04

	synReport  = 0
	synDropped = 3

	relX = 0x00
	relY = 0x01

	btnLeft = 0x110
	keyMax  = 0x2ff
	relMax  = 0x0f
	absMax  = 0x3f
	mscMax  = 0x07
	propMax = 0x1f
)

// evEventSize is sizeof(struct input_event): two native longs for the
// timestamp plus type, code and value.
var evEventSize = int(2*unsafe.Sizeof(uintptr(0))) + 8

type inputEvent struct {
	Sec, Usec int64
	Type      uint16
	Code      uint16
	Value     int32
}

func (e inputEvent) timeNs() int64 { return e.Sec*1_000_000_000 + e.Usec*1_000 }

func decodeEvent(b []byte) inputEvent {
	ne := binary.NativeEndian
	var e inputEvent
	if evEventSize == 24 {
		e.Sec = int64(ne.Uint64(b[0:]))
		e.Usec = int64(ne.Uint64(b[8:]))
		b = b[16:]
	} else {
		e.Sec = int64(int32(ne.Uint32(b[0:])))
		e.Usec = int64(int32(ne.Uint32(b[4:])))
		b = b[8:]
	}
	e.Type = ne.Uint16(b[0:])
	e.Code = ne.Uint16(b[2:])
	e.Value = int32(ne.Uint32(b[4:]))
	return e
}

// appendEvent encodes an event; the timestamp is left zero because the
// kernel stamps uinput events itself.
func appendEvent(dst []byte, typ, code uint16, value int32) []byte {
	ne := binary.NativeEndian
	var b [24]byte
	off := evEventSize - 8
	ne.PutUint16(b[off:], typ)
	ne.PutUint16(b[off+2:], code)
	ne.PutUint32(b[off+4:], uint32(value))
	return append(dst, b[:evEventSize]...)
}

// frameProcessor collects the events of one SYN_REPORT frame, transforms
// the accumulated REL_X/REL_Y motion and re-emits the frame.
type frameProcessor struct {
	transform func(dx, dy int32, tNs int64) (int32, int32)
	// resync returns the keys that are currently pressed on the physical
	// device; it is used after the kernel dropped events (SYN_DROPPED).
	resync func() map[uint16]bool

	pending  []inputEvent
	dx, dy   int32
	moved    bool
	dropping bool
	pressed  map[uint16]bool
}

func newFrameProcessor(transform func(dx, dy int32, tNs int64) (int32, int32), resync func() map[uint16]bool) *frameProcessor {
	return &frameProcessor{transform: transform, resync: resync, pressed: map[uint16]bool{}}
}

// feed consumes one event and appends any output bytes to out.
func (f *frameProcessor) feed(e inputEvent, out []byte) []byte {
	switch {
	case e.Type == evSyn && e.Code == synDropped:
		f.dropping = true
		f.reset()
		return out
	case e.Type == evSyn && e.Code == synReport:
		if f.dropping {
			// Everything up to this report is incomplete; drop it and bring
			// the button state back in line with the device.
			f.dropping = false
			f.reset()
			return f.resyncKeys(out)
		}
		return f.flush(e, out)
	case f.dropping:
		return out
	case e.Type == evRel && e.Code == relX:
		f.dx += e.Value
		f.moved = true
	case e.Type == evRel && e.Code == relY:
		f.dy += e.Value
		f.moved = true
	default:
		f.pending = append(f.pending, e)
	}
	return out
}

func (f *frameProcessor) flush(syn inputEvent, out []byte) []byte {
	start := len(out)
	if f.moved {
		ox, oy := f.transform(f.dx, f.dy, syn.timeNs())
		if ox != 0 {
			out = appendEvent(out, evRel, relX, ox)
		}
		if oy != 0 {
			out = appendEvent(out, evRel, relY, oy)
		}
	}
	for _, p := range f.pending {
		if p.Type == evKey {
			switch p.Value {
			case 0:
				delete(f.pressed, p.Code)
			case 1:
				f.pressed[p.Code] = true
			}
		}
		out = appendEvent(out, p.Type, p.Code, p.Value)
	}
	if len(out) > start {
		out = appendEvent(out, evSyn, synReport, 0)
	}
	f.reset()
	return out
}

func (f *frameProcessor) resyncKeys(out []byte) []byte {
	if f.resync == nil {
		return out
	}
	now := f.resync()
	if now == nil {
		return out
	}
	start := len(out)
	for code := range f.pressed {
		if !now[code] {
			out = appendEvent(out, evKey, code, 0)
			delete(f.pressed, code)
		}
	}
	for code := range now {
		if !f.pressed[code] {
			out = appendEvent(out, evKey, code, 1)
			f.pressed[code] = true
		}
	}
	if len(out) > start {
		out = appendEvent(out, evSyn, synReport, 0)
	}
	return out
}

func (f *frameProcessor) reset() {
	f.pending = f.pending[:0]
	f.dx, f.dy, f.moved = 0, 0, false
}

// bitSet reports whether bit n is set in a kernel bitmask buffer.
func bitSet(b []byte, n int) bool {
	return n/8 < len(b) && b[n/8]&(1<<(uint(n)%8)) != 0
}
