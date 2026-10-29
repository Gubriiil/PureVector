package vector

import (
	"math"
	"time"
)

// TrackerConfig tunes how the movement direction is estimated.
type TrackerConfig struct {
	// MemoryCounts is the travel distance (in sensor counts) over which the
	// direction memory decays to 1/e. A few counts are enough to resolve the
	// direction even when the sensor reports tiny integer steps such as
	// (1,0),(0,1),(1,0)… for a slow 45° movement.
	MemoryCounts float64 `json:"memoryCounts"`
	// IdleReset forgets the previous direction after this much inactivity,
	// so a new flick starts with a fresh estimate.
	IdleReset time.Duration `json:"idleReset"`
}

// DefaultTrackerConfig returns the defaults used by the engine.
func DefaultTrackerConfig() TrackerConfig {
	return TrackerConfig{MemoryCounts: 16, IdleReset: 50 * time.Millisecond}
}

func (c TrackerConfig) sanitized() TrackerConfig {
	d := DefaultTrackerConfig()
	if !(c.MemoryCounts >= 1 && c.MemoryCounts <= 10000) {
		c.MemoryCounts = d.MemoryCounts
	}
	if c.IdleReset <= 0 || c.IdleReset > 10*time.Second {
		c.IdleReset = d.IdleReset
	}
	return c
}

// Tracker holds the per-device state of the transform. It is not safe for
// concurrent use; every input device gets its own Tracker.
//
// For every report the tracker
//  1. updates a distance-weighted memory of recent motion and takes its
//     angle as the movement direction,
//  2. scales the report by the gain for that direction (the scaling is a
//     scalar, so the direction of every single report is preserved:
//     purely horizontal motion stays purely horizontal), and
//  3. carries the fractional remainder to the next report, so no motion is
//     lost or invented by rounding.
//
// Because the gain comes from the smoothed direction instead of each tiny
// report, the result does not depend on the polling rate or on how the
// sensor happens to split a movement into reports.
type Tracker struct {
	cfg        TrackerConfig
	idleNs     int64
	wx, wy     float64
	lastNs     int64
	remX, remY float64

	// LastTheta and LastGain describe the most recent transformed report
	// (used for the live view).
	LastTheta float64
	LastGain  float64
}

// NewTracker creates a tracker; invalid config values fall back to defaults.
func NewTracker(cfg TrackerConfig) *Tracker {
	cfg = cfg.sanitized()
	return &Tracker{cfg: cfg, idleNs: int64(cfg.IdleReset), LastGain: 1}
}

// Reset forgets the direction memory and any carried remainder.
func (t *Tracker) Reset() {
	t.wx, t.wy, t.remX, t.remY = 0, 0, 0, 0
	t.LastGain = 1
}

// Remainder returns the carried sub-count motion (always within ±0.5).
func (t *Tracker) Remainder() (float64, float64) { return t.remX, t.remY }

// Apply transforms one relative motion report (x right, y down, in counts)
// observed at monotonic time nowNs. A nil table means "pass through".
func (t *Tracker) Apply(tab *GainTable, dx, dy int32, nowNs int64) (int32, int32) {
	if dx == 0 && dy == 0 {
		return 0, 0
	}
	if nowNs-t.lastNs > t.idleNs || nowNs < t.lastNs {
		t.wx, t.wy = 0, 0
	}
	t.lastNs = nowNs

	fx, fy := float64(dx), float64(dy)
	decay := math.Exp(-math.Hypot(fx, fy) / t.cfg.MemoryCounts)
	t.wx = t.wx*decay + fx
	t.wy = t.wy*decay + fy

	if tab == nil || tab.neutral {
		t.LastTheta = AngleOf(t.wx, t.wy)
		t.LastGain = 1
		return dx, dy
	}

	theta := AngleOf(t.wx, t.wy)
	g := tab.At(theta)
	t.LastTheta, t.LastGain = theta, g

	ox := fx*g + t.remX
	oy := fy*g + t.remY
	// Round half to even: with |remainder| <= 0.5 an axis that did not move
	// in this report (input 0) always yields exactly 0 output.
	rx := math.RoundToEven(ox)
	ry := math.RoundToEven(oy)
	t.remX = ox - rx
	t.remY = oy - ry
	return clampInt32(rx), clampInt32(ry)
}

func clampInt32(v float64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}
