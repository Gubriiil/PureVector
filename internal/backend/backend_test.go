package backend

import (
	"context"
	"testing"
	"unsafe"

	"github.com/Gubriiil/PureVector/internal/engine"
	"github.com/Gubriiil/PureVector/internal/profile"
)

func testEngine(t *testing.T, gains []float64) *engine.Engine {
	t.Helper()
	s, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.SetLogger(t.Logf)
	if gains != nil {
		if err := s.SaveProfile(profile.Profile{Name: "p", Gains: gains}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetDefault("p"); err != nil {
			t.Fatal(err)
		}
	}
	return engine.New(s)
}

// ---- Interception ------------------------------------------------------------

type fakePacket struct {
	dev int
	s   icMouseStroke
}

type fakeDriver struct {
	in     []fakePacket
	cur    fakePacket
	sent   []fakePacket
	hwids  map[int]string
	cancel context.CancelFunc
}

func (f *fakeDriver) Wait(uint32) int {
	if len(f.in) == 0 {
		f.cancel()
		return 0
	}
	f.cur, f.in = f.in[0], f.in[1:]
	return f.cur.dev
}

func (f *fakeDriver) Receive(dev int, s *icMouseStroke) int {
	*s = f.cur.s
	return 1
}

func (f *fakeDriver) Send(dev int, s *icMouseStroke) int {
	f.sent = append(f.sent, fakePacket{dev, *s})
	return 1
}

func (f *fakeDriver) HardwareID(dev int) string { return f.hwids[dev] }

func TestStrokeLayoutMatchesC(t *testing.T) {
	if got := unsafe.Sizeof(icMouseStroke{}); got != 20 {
		t.Fatalf("sizeof(InterceptionMouseStroke) = %d, want 20", got)
	}
	if got := unsafe.Offsetof(icMouseStroke{}.X); got != 8 {
		t.Fatalf("offsetof(x) = %d, want 8", got)
	}
}

func TestInterceptionLoop(t *testing.T) {
	eng := testEngine(t, []float64{0.5, 1, 1, 1, 1, 1, 1, 1}) // right ×0.5
	ctx, cancel := context.WithCancel(context.Background())
	mouse := icMaxKeyboard + 1
	drv := &fakeDriver{cancel: cancel, hwids: map[int]string{mouse: `HID\VID_1B1C&PID_1BAC&REV_0100&MI_00`}}
	for i := 0; i < 100; i++ {
		drv.in = append(drv.in, fakePacket{mouse, icMouseStroke{X: 10, Information: 7}})
	}
	drv.in = append(drv.in,
		fakePacket{mouse, icMouseStroke{State: 0x001, X: 0, Y: 0}},                       // button only
		fakePacket{mouse, icMouseStroke{Flags: icMouseMoveAbsolute, X: 30000, Y: 20000}}, // tablet
		fakePacket{1, icMouseStroke{X: 99}},                                              // keyboard slot
	)
	now := int64(0)
	l := &icLoop{drv: drv, eng: eng, now: func() int64 { now += 1_000_000; return now }, logf: t.Logf}
	l.run(ctx)

	if len(drv.sent) != 103 {
		t.Fatalf("sent %d strokes, want 103", len(drv.sent))
	}
	sum := 0
	for _, p := range drv.sent[:100] {
		if p.s.Y != 0 || p.s.Information != 7 {
			t.Fatalf("stroke fields changed: %+v", p.s)
		}
		sum += int(p.s.X)
	}
	if sum < 499 || sum > 501 {
		t.Errorf("rightward output %d, want ≈500", sum)
	}
	if s := drv.sent[100].s; s.State != 1 || s.X != 0 {
		t.Errorf("button stroke changed: %+v", s)
	}
	if s := drv.sent[101].s; s.X != 30000 || s.Y != 20000 {
		t.Errorf("absolute stroke changed: %+v", s)
	}
	if s := drv.sent[102]; s.dev != 1 || s.s.X != 99 {
		t.Errorf("keyboard stroke changed: %+v", s)
	}
	devs := eng.Devices()
	if len(devs) != 1 || devs[0].Key != "VID_1B1C&PID_1BAC" || devs[0].Name != "Corsair-Maus (1B1C:1BAC)" {
		t.Errorf("devices: %+v", devs)
	}
}

func TestInterceptionSlotReuse(t *testing.T) {
	eng := testEngine(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	mouse := icMaxKeyboard + 2
	drv := &fakeDriver{cancel: cancel, hwids: map[int]string{mouse: `HID\VID_046D&PID_C08B`}}
	drv.in = []fakePacket{{mouse, icMouseStroke{X: 1}}}
	now := int64(0)
	l := &icLoop{drv: drv, eng: eng, now: func() int64 { return now }}
	l.run(ctx)

	// Two seconds later another mouse sits in the same slot.
	now += 2_000_000_000
	drv.hwids[mouse] = `HID\VID_1532&PID_0099`
	ctx, cancel = context.WithCancel(context.Background())
	drv.cancel = cancel
	drv.in = []fakePacket{{mouse, icMouseStroke{X: 1}}}
	l.run(ctx)

	connected := map[string]bool{}
	for _, d := range eng.Devices() {
		connected[d.Key] = d.Connected
	}
	if connected["VID_046D&PID_C08B"] || !connected["VID_1532&PID_0099"] {
		t.Errorf("slot reuse not detected: %v", connected)
	}
}

func TestDescribeHardwareID(t *testing.T) {
	cases := []struct{ in, key, name string }{
		{`HID\VID_046D&PID_C547&REV_0402&MI_01&Col01`, "VID_046D&PID_C547", "Logitech-Maus (046D:C547)"},
		{`HID\{00001124-0000-1000-8000-00805f9b34fb}_VID&0002046d_PID&b023&Col01`, "VID_046D&PID_B023", "Logitech-Maus (046D:B023)"},
		{`HID\VID_ABCD&PID_0001`, "VID_ABCD&PID_0001", "Maus ABCD:0001"},
		{`ACPI\PNP0F13`, `ACPI\PNP0F13`, "PS/2-Maus"},
		{``, "SLOT_12", "Maus (Slot 12)"},
	}
	for _, c := range cases {
		key, name := describeHardwareID(c.in, 12)
		if key != c.key || name != c.name {
			t.Errorf("%q → (%q, %q), want (%q, %q)", c.in, key, name, c.key, c.name)
		}
	}
}

func TestUTF16MultiSz(t *testing.T) {
	in := []uint16{'H', 'I', 'D', '\\', 0xD83D, 0xDDB1, 0, 'X', 0, 0}
	if got := utf16MultiSzFirst(in); got != "HID\\🖱" {
		t.Errorf("got %q", got)
	}
}

// ---- evdev -------------------------------------------------------------------

type ev struct {
	typ, code uint16
	val       int32
}

func encode(evs ...ev) []byte {
	var b []byte
	for _, e := range evs {
		b = appendEvent(b, e.typ, e.code, e.val)
	}
	return b
}

func decodeAll(b []byte) []ev {
	var out []ev
	for off := 0; off+evEventSize <= len(b); off += evEventSize {
		e := decodeEvent(b[off:])
		out = append(out, ev{e.Type, e.Code, e.Value})
	}
	return out
}

func run(fp *frameProcessor, in []byte) []ev {
	var out []byte
	for off := 0; off+evEventSize <= len(in); off += evEventSize {
		out = fp.feed(decodeEvent(in[off:]), out)
	}
	return decodeAll(out)
}

func TestEventCodecRoundTrip(t *testing.T) {
	b := encode(ev{evRel, relX, -12345}, ev{evKey, btnLeft, 1})
	if len(b) != 2*evEventSize {
		t.Fatalf("encoded size %d", len(b))
	}
	got := decodeAll(b)
	if got[0] != (ev{evRel, relX, -12345}) || got[1] != (ev{evKey, btnLeft, 1}) {
		t.Errorf("round trip: %+v", got)
	}
}

func TestFrameProcessor(t *testing.T) {
	double := func(dx, dy int32, _ int64) (int32, int32) { return 2 * dx, 2 * dy }
	fp := newFrameProcessor(double, nil)
	got := run(fp, encode(
		ev{evRel, relX, 3}, ev{evRel, relX, 2}, ev{evRel, relY, -4}, ev{evKey, btnLeft, 1}, ev{evRel, 0x08, -1}, ev{evSyn, synReport, 0},
	))
	want := []ev{{evRel, relX, 10}, {evRel, relY, -8}, {evKey, btnLeft, 1}, {evRel, 0x08, -1}, {evSyn, synReport, 0}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d: got %+v want %+v", i, got[i], want[i])
		}
	}

	// Motion that rounds to zero and nothing else: nothing is written.
	zero := newFrameProcessor(func(int32, int32, int64) (int32, int32) { return 0, 0 }, nil)
	if got := run(zero, encode(ev{evRel, relX, 1}, ev{evSyn, synReport, 0})); len(got) != 0 {
		t.Errorf("empty frame written: %+v", got)
	}
}

func TestFrameProcessorResyncAfterDrop(t *testing.T) {
	ident := func(dx, dy int32, _ int64) (int32, int32) { return dx, dy }
	fp := newFrameProcessor(ident, func() map[uint16]bool { return map[uint16]bool{0x111: true} })
	run(fp, encode(ev{evKey, btnLeft, 1}, ev{evSyn, synReport, 0}))
	got := run(fp, encode(
		ev{evRel, relX, 50}, ev{evSyn, synDropped, 0}, ev{evRel, relX, 7}, ev{evKey, btnLeft, 0}, ev{evSyn, synReport, 0},
	))
	// Left button released, right button (0x111) pressed according to the
	// device state; the dropped motion is discarded.
	if len(got) != 3 || got[2] != (ev{evSyn, synReport, 0}) {
		t.Fatalf("resync output: %+v", got)
	}
	seen := map[ev]bool{got[0]: true, got[1]: true}
	if !seen[ev{evKey, btnLeft, 0}] || !seen[ev{evKey, 0x111, 1}] {
		t.Errorf("resync output: %+v", got)
	}
	if got := run(fp, encode(ev{evRel, relY, 5}, ev{evSyn, synReport, 0})); len(got) != 2 || got[0] != (ev{evRel, relY, 5}) {
		t.Errorf("after resync: %+v", got)
	}
}
