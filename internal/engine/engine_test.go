package engine

import (
	"testing"
	"time"

	"github.com/Gubriiil/PureVector/internal/profile"
)

func newEngine(t *testing.T) *Engine {
	t.Helper()
	s, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.SetLogger(t.Logf)
	if err := s.SaveProfile(profile.Profile{Name: "p", Gains: []float64{0.8, 1, 1, 1, 1.2, 1, 1, 1}}); err != nil {
		t.Fatal(err)
	}
	return New(s)
}

// moveRight sends n reports of +step counts to the right and returns the
// total output.
func moveRight(e *Engine, d *Device, n int, step int32) (sum int64) {
	now := time.Now().UnixNano()
	for i := 0; i < n; i++ {
		now += int64(time.Millisecond)
		ox, oy := e.Process(d, step, 0, now)
		if oy != 0 {
			panic("vertical output from horizontal input")
		}
		sum += int64(ox)
	}
	return sum
}

func TestProcessUsesAssignedProfiles(t *testing.T) {
	e := newEngine(t)
	d := e.Device("VID_1&PID_1", "Maus", "test")
	if got := moveRight(e, d, 100, 10); got != 1000 {
		t.Fatalf("neutral default changed movement: %d", got)
	}
	if err := e.Store().SetDefault("p"); err != nil {
		t.Fatal(err)
	}
	if got := moveRight(e, d, 100, 10); got < 798 || got > 802 {
		t.Fatalf("profile p (right gain 0.8) gave %d, want ≈800", got)
	}
	if err := e.Store().AssignDevice(d.Key, d.Name, profile.NeutralName); err != nil {
		t.Fatal(err)
	}
	if got := moveRight(e, d, 100, 10); got != 1000 {
		t.Fatalf("device pinned to neutral still transformed: %d", got)
	}
	_ = e.Store().AssignDevice(d.Key, "", "")
	_ = e.Store().SetEnabled(false)
	if got := moveRight(e, d, 100, 10); got != 1000 {
		t.Fatalf("disabled engine transformed: %d", got)
	}
}

func TestCalibrationLifecycle(t *testing.T) {
	e := newEngine(t)
	_ = e.Store().SetDefault("p")
	a := e.Device("A", "Maus A", "test")
	b := e.Device("B", "Maus B", "test")

	info, err := e.StartCalibration(ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Bypassed() {
		t.Fatal("fresh calibration must pause the transform")
	}
	if got := moveRight(e, b, 50, 10); got != 500 {
		t.Fatalf("transform active during fresh calibration: %d", got)
	}
	moveRight(e, a, 5, 10)
	ctx, err := e.FinishCalibration(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.DeviceKey != "B" || ctx.BaseGains != nil {
		t.Errorf("fresh: %+v", ctx)
	}
	if e.Bypassed() {
		t.Error("bypass not lifted after finish")
	}
	if _, err := e.FinishCalibration(info.ID); err != ErrUnknownSession {
		t.Errorf("second finish: %v", err)
	}

	info, _ = e.StartCalibration(ModeRefine)
	if e.Bypassed() {
		t.Error("refine must keep the profile active")
	}
	moveRight(e, a, 50, 10)
	ctx, _ = e.FinishCalibration(info.ID)
	if ctx.DeviceKey != "A" || ctx.BaseProfile != "p" || len(ctx.BaseGains) != 8 || ctx.BaseGains[0] != 0.8 {
		t.Errorf("refine: %+v", ctx)
	}

	info, _ = e.StartCalibration(ModeFresh)
	e.CancelCalibration(info.ID)
	if e.Bypassed() {
		t.Error("cancel did not lift bypass")
	}
}

func TestDevicesListIncludesOfflineAssignments(t *testing.T) {
	e := newEngine(t)
	d := e.Device("A", "Maus A", "test")
	moveRight(e, d, 3, 5)
	_ = e.Store().AssignDevice("OLD", "Alte Maus", "p")
	list := e.Devices()
	if len(list) != 2 || list[0].Key != "A" || !list[0].Connected || !list[0].Active {
		t.Fatalf("devices: %+v", list)
	}
	if list[1].Key != "OLD" || list[1].Name != "Alte Maus" || list[1].Profile != "p" || list[1].Connected {
		t.Errorf("offline assignment: %+v", list[1])
	}
	e.Disconnected(d)
	if e.Devices()[0].Connected {
		t.Error("disconnect not reflected")
	}
}

func TestCalibrationPauseNeedsHeartbeats(t *testing.T) {
	old := calHeartbeat
	calHeartbeat = 40 * time.Millisecond
	defer func() { calHeartbeat = old }()
	e := newEngine(t)
	info, _ := e.StartCalibration(ModeFresh)
	for i := 0; i < 4; i++ {
		time.Sleep(20 * time.Millisecond)
		if err := e.HeartbeatCalibration(info.ID); err != nil {
			t.Fatal(err)
		}
		if !e.Bypassed() {
			t.Fatal("heartbeat did not keep the pause")
		}
	}
	time.Sleep(80 * time.Millisecond) // page gone: no more heartbeats
	if e.Bypassed() {
		t.Error("pause did not expire without heartbeats")
	}
	if err := e.HeartbeatCalibration(info.ID); err != nil || !e.Bypassed() {
		t.Errorf("late heartbeat should resume the pause: %v", err)
	}
	if err := e.HeartbeatCalibration("unknown"); err != ErrUnknownSession {
		t.Errorf("unknown id: %v", err)
	}
}
