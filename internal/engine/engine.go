// Package engine connects input backends with the active profiles. Backends
// call Process for every relative motion report of every mouse; the engine
// looks up the device's profile in the current snapshot and applies the
// direction-dependent gain with a per-device tracker.
package engine

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Gubriiil/PureVector/internal/profile"
	"github.com/Gubriiil/PureVector/internal/vector"
)

// Device is one physical input device known to the engine.
//
// The unexported hot-path fields are owned by the single backend goroutine
// that processes the device; the atomics may be read by anyone.
type Device struct {
	Key       string
	Name      string
	Backend   string
	FirstSeen time.Time

	tracker *vector.Tracker
	snapVer uint64
	table   *vector.GainTable
	trkCfg  vector.TrackerConfig

	events    atomic.Uint64
	distance  atomic.Uint64 // Σ(|dx|+|dy|) of raw input
	lastNs    atomic.Int64  // wall clock of the last report
	liveTheta atomic.Uint64 // float64 bits
	liveGain  atomic.Uint64 // float64 bits
	connected atomic.Bool
}

// DeviceInfo is a read-only view for the API.
type DeviceInfo struct {
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Backend   string    `json:"backend"`
	Connected bool      `json:"connected"`
	Profile   string    `json:"profile"`
	Assigned  bool      `json:"assigned"` // explicit assignment (not the default)
	Events    uint64    `json:"events"`
	Distance  uint64    `json:"distance"`
	LastSeen  time.Time `json:"lastSeen"`
	LiveAngle float64   `json:"liveAngle"` // degrees, CCW from right
	LiveGain  float64   `json:"liveGain"`
	Active    bool      `json:"active"` // moved within the last second
}

// BackendStatus describes the input backend for the UI.
type BackendStatus struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Engine is safe for concurrent use.
type Engine struct {
	store *profile.Store

	bypassUntil atomic.Int64 // wall clock ns; transform paused before this

	mu      sync.Mutex
	devices map[string]*Device
	backend BackendStatus
	cal     *calibration
}

// New creates an engine on top of a profile store.
func New(store *profile.Store) *Engine {
	return &Engine{store: store, devices: map[string]*Device{}}
}

// Store returns the profile store.
func (e *Engine) Store() *profile.Store { return e.store }

// SetBackendStatus is called by backends to report their state.
func (e *Engine) SetBackendStatus(st BackendStatus) {
	e.mu.Lock()
	e.backend = st
	e.mu.Unlock()
}

// BackendStatus returns the reported backend state.
func (e *Engine) BackendStatus() BackendStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.backend
}

// Device registers a device (or returns the existing one) and marks it as
// connected. Backends call it when a device appears.
func (e *Engine) Device(key, name, backend string) *Device {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.devices[key]
	if !ok {
		d = &Device{Key: key, Name: name, Backend: backend, FirstSeen: time.Now()}
		d.liveGain.Store(math.Float64bits(1))
		e.devices[key] = d
	}
	if name != "" {
		d.Name = name
	}
	d.connected.Store(true)
	return d
}

// Disconnected marks a device as gone (its statistics are kept).
func (e *Engine) Disconnected(d *Device) { d.connected.Store(false) }

// Process transforms one relative motion report of device d. nowNs is a
// monotonic timestamp from the backend.
func (e *Engine) Process(d *Device, dx, dy int32, nowNs int64) (int32, int32) {
	if dx == 0 && dy == 0 {
		return 0, 0
	}
	wall := time.Now().UnixNano()
	d.events.Add(1)
	d.distance.Add(uint64(abs32(dx)) + uint64(abs32(dy)))
	d.lastNs.Store(wall)

	snap := e.store.Snapshot()
	if snap.Version != d.snapVer || d.tracker == nil {
		d.table, _ = snap.TableFor(d.Key)
		if d.tracker == nil || d.trkCfg != snap.Tracker {
			d.tracker = vector.NewTracker(snap.Tracker)
			d.trkCfg = snap.Tracker
		} else {
			d.tracker.Reset()
		}
		d.snapVer = snap.Version
	}
	if !snap.Enabled || d.table == nil || wall < e.bypassUntil.Load() {
		d.liveTheta.Store(math.Float64bits(vector.AngleOf(float64(dx), float64(dy))))
		d.liveGain.Store(math.Float64bits(1))
		return dx, dy
	}
	ox, oy := d.tracker.Apply(d.table, dx, dy, nowNs)
	d.liveTheta.Store(math.Float64bits(d.tracker.LastTheta))
	d.liveGain.Store(math.Float64bits(d.tracker.LastGain))
	return ox, oy
}

// Bypassed reports whether the transform is temporarily paused.
func (e *Engine) Bypassed() bool { return time.Now().UnixNano() < e.bypassUntil.Load() }

// Devices lists known devices: connected ones and assigned ones.
func (e *Engine) Devices() []DeviceInfo {
	snap := e.store.Snapshot()
	cfg := e.store.Config()
	now := time.Now()
	e.mu.Lock()
	out := make([]DeviceInfo, 0, len(e.devices))
	seen := map[string]bool{}
	for _, d := range e.devices {
		seen[d.Key] = true
		_, assigned := cfg.Devices[d.Key]
		info := DeviceInfo{
			Key:       d.Key,
			Name:      d.Name,
			Backend:   d.Backend,
			Connected: d.connected.Load(),
			Profile:   snap.ProfileFor(d.Key),
			Assigned:  assigned,
			Events:    d.events.Load(),
			Distance:  d.distance.Load(),
			LiveAngle: math.Float64frombits(d.liveTheta.Load()) * 180 / math.Pi,
			LiveGain:  math.Float64frombits(d.liveGain.Load()),
		}
		if ns := d.lastNs.Load(); ns > 0 {
			info.LastSeen = time.Unix(0, ns)
			info.Active = now.Sub(info.LastSeen) < time.Second
		}
		out = append(out, info)
	}
	e.mu.Unlock()
	for key, prof := range cfg.Devices {
		if seen[key] {
			continue
		}
		name := cfg.DeviceNames[key]
		if name == "" {
			name = key
		}
		out = append(out, DeviceInfo{Key: key, Name: name, Profile: prof, Assigned: true, LiveGain: 1})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Connected != out[j].Connected {
			return out[i].Connected
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ---- calibration bookkeeping ------------------------------------------------

// Calibration modes.
const (
	ModeFresh  = "fresh"  // measure without the active profile
	ModeRefine = "refine" // measure with the active profile and compose
)

// calHeartbeat is how long a fresh calibration keeps the transform paused
// without hearing from the calibration page. If the page is closed or the
// browser crashes, the profile is active again shortly afterwards.
var calHeartbeat = 20 * time.Second

type calibration struct {
	id        string
	mode      string
	started   time.Time
	baseline  map[string]uint64
	prevUntil int64
}

// CalibrationInfo is returned when a session starts.
type CalibrationInfo struct {
	ID      string `json:"id"`
	Mode    string `json:"mode"`
	Enabled bool   `json:"enabled"`
	// Profile is the profile currently active on the most recently used
	// device (what "refine" would build upon).
	Profile string `json:"profile"`
	Device  string `json:"device"`
}

// CalibrationContext tells the caller which device was calibrated and which
// profile was active while measuring.
type CalibrationContext struct {
	ID          string
	Mode        string
	DeviceKey   string // "" if the backend saw no movement (e.g. no backend)
	DeviceName  string
	BaseProfile string    // profile active during measurement ("" = none)
	BaseGains   []float64 // nil = neutral
}

// ErrUnknownSession is returned for stale or unknown calibration IDs.
var ErrUnknownSession = errors.New("engine: unknown calibration session")

// StartCalibration begins a measurement. In fresh mode the transform is
// paused while the calibration page keeps sending heartbeats, until
// FinishCalibration or CancelCalibration.
func (e *Engine) StartCalibration(mode string) (CalibrationInfo, error) {
	if mode != ModeRefine {
		mode = ModeFresh
	}
	id := newID()
	e.mu.Lock()
	if e.cal != nil {
		e.bypassUntil.Store(e.cal.prevUntil)
	}
	c := &calibration{id: id, mode: mode, started: time.Now(), baseline: map[string]uint64{},
		prevUntil: e.bypassUntil.Load()}
	var recent *Device
	for k, d := range e.devices {
		c.baseline[k] = d.distance.Load()
		if recent == nil || d.lastNs.Load() > recent.lastNs.Load() {
			recent = d
		}
	}
	e.cal = c
	e.mu.Unlock()
	if mode == ModeFresh {
		e.bypassUntil.Store(time.Now().Add(calHeartbeat).UnixNano())
	}
	snap := e.store.Snapshot()
	info := CalibrationInfo{ID: id, Mode: mode, Enabled: snap.Enabled, Profile: snap.DefaultName}
	if recent != nil {
		info.Device = recent.Name
		info.Profile = snap.ProfileFor(recent.Key)
	}
	return info, nil
}

// HeartbeatCalibration keeps a running calibration alive. For a fresh
// measurement it (re-)extends the pause of the transform.
func (e *Engine) HeartbeatCalibration(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cal == nil || e.cal.id != id {
		return ErrUnknownSession
	}
	if e.cal.mode == ModeFresh {
		e.bypassUntil.Store(time.Now().Add(calHeartbeat).UnixNano())
	}
	return nil
}

// FinishCalibration ends the measurement and reports which device moved the
// most during it, together with the profile that was active on it.
func (e *Engine) FinishCalibration(id string) (CalibrationContext, error) {
	e.mu.Lock()
	c := e.cal
	if c == nil || c.id != id {
		e.mu.Unlock()
		return CalibrationContext{}, ErrUnknownSession
	}
	e.cal = nil
	e.bypassUntil.Store(c.prevUntil)
	var best *Device
	var bestDist uint64
	for k, d := range e.devices {
		moved := d.distance.Load() - c.baseline[k]
		if moved > bestDist {
			best, bestDist = d, moved
		}
	}
	e.mu.Unlock()

	ctx := CalibrationContext{ID: id, Mode: c.mode}
	snap := e.store.Snapshot()
	if best != nil {
		ctx.DeviceKey, ctx.DeviceName = best.Key, best.Name
	}
	if c.mode == ModeRefine && snap.Enabled {
		var tab *vector.GainTable
		if best != nil {
			tab, ctx.BaseProfile = snap.TableFor(best.Key)
		} else {
			tab, ctx.BaseProfile = snap.TableFor("")
		}
		if tab != nil {
			ctx.BaseGains = tab.Gains()
		} else {
			ctx.BaseProfile = ""
		}
	}
	return ctx, nil
}

// CancelCalibration aborts a measurement and restores the transform.
func (e *Engine) CancelCalibration(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cal != nil && (id == "" || e.cal.id == id) {
		e.bypassUntil.Store(e.cal.prevUntil)
		e.cal = nil
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func abs32(v int32) int64 {
	if v < 0 {
		return -int64(v)
	}
	return int64(v)
}
