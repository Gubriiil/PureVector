// Package vector implements the direction-dependent sensitivity transform
// that sits at the heart of OmniVector.
//
// A profile is a set of N gains for N evenly spaced movement directions
// ("anchors"). Anchor k points at angle k·360°/N, measured counter-clockwise
// from "right" with the y axis pointing up (so 90° is "up" on screen).
// Between anchors the gain is interpolated smoothly (periodic Catmull-Rom in
// the log domain), so the sensitivity never jumps when the movement direction
// changes slightly.
package vector

import (
	"errors"
	"fmt"
	"math"
)

const (
	lutBits = 12
	lutSize = 1 << lutBits // 4096 entries, about 0.09° per entry
	lutMask = lutSize - 1

	// MinDirections and MaxDirections bound the number of anchors.
	MinDirections = 4
	MaxDirections = 72

	// MinGain and MaxGain are hard runtime safety limits for any gain.
	// Calibration itself clamps far tighter (see package calib).
	MinGain = 0.25
	MaxGain = 4.0
)

// GainTable is an immutable, precomputed direction→gain function.
// It is safe for concurrent use.
type GainTable struct {
	gains   []float64
	logs    []float64
	lut     [lutSize]float32
	neutral bool
}

// NewGainTable builds a table from N anchor gains (anchor 0 = right,
// counter-clockwise).
func NewGainTable(gains []float64) (*GainTable, error) {
	n := len(gains)
	if n < MinDirections || n > MaxDirections {
		return nil, fmt.Errorf("vector: %d directions not supported (allowed %d..%d)", n, MinDirections, MaxDirections)
	}
	t := &GainTable{
		gains:   make([]float64, n),
		logs:    make([]float64, n),
		neutral: true,
	}
	for k, g := range gains {
		if !(g >= MinGain && g <= MaxGain) { // also rejects NaN
			return nil, fmt.Errorf("vector: gain %d = %v outside [%v, %v]", k, g, MinGain, MaxGain)
		}
		t.gains[k] = g
		t.logs[k] = math.Log(g)
		if g != 1 {
			t.neutral = false
		}
	}
	for i := range t.lut {
		theta := float64(i) * (2 * math.Pi / lutSize)
		t.lut[i] = float32(t.Exact(theta))
	}
	return t, nil
}

// MustGainTable is NewGainTable for known-good input (tests, constants).
func MustGainTable(gains []float64) *GainTable {
	t, err := NewGainTable(gains)
	if err != nil {
		panic(err)
	}
	return t
}

// Neutral returns a table with gain 1 in every one of n directions.
func Neutral(n int) *GainTable {
	g := make([]float64, n)
	for i := range g {
		g[i] = 1
	}
	return MustGainTable(g)
}

// N is the number of anchor directions.
func (t *GainTable) N() int { return len(t.gains) }

// IsNeutral reports whether every anchor gain is exactly 1.
func (t *GainTable) IsNeutral() bool { return t.neutral }

// Gains returns a copy of the anchor gains.
func (t *GainTable) Gains() []float64 { return append([]float64(nil), t.gains...) }

// AnchorAngle is the angle (radians) of anchor k.
func (t *GainTable) AnchorAngle(k int) float64 { return AnchorAngle(k, len(t.gains)) }

// At returns the gain for movement direction theta (radians, any range)
// using the precomputed lookup table. This is the hot-path variant.
func (t *GainTable) At(theta float64) float64 {
	i := int(math.Floor(theta*(lutSize/(2*math.Pi))+0.5)) & lutMask
	return float64(t.lut[i])
}

// Exact evaluates the interpolated gain without the lookup table.
func (t *GainTable) Exact(theta float64) float64 {
	n := len(t.logs)
	s := wrapAngle(theta) / (2 * math.Pi / float64(n))
	k := int(math.Floor(s))
	f := s - float64(k)
	p0 := t.logs[mod(k-1, n)]
	p1 := t.logs[mod(k, n)]
	p2 := t.logs[mod(k+1, n)]
	p3 := t.logs[mod(k+2, n)]
	g := math.Exp(catmullRom(p0, p1, p2, p3, f))
	return math.Min(MaxGain, math.Max(MinGain, g))
}

// Resample evaluates the table at n evenly spaced anchors.
func (t *GainTable) Resample(n int) ([]float64, error) {
	if n < MinDirections || n > MaxDirections {
		return nil, fmt.Errorf("vector: %d directions not supported", n)
	}
	out := make([]float64, n)
	for k := range out {
		out[k] = t.Exact(AnchorAngle(k, n))
	}
	return out, nil
}

// Compose multiplies two profiles direction by direction. The result has
// the anchor count of a; b is resampled if necessary. It is used when a
// calibration is run with a profile already active ("refine").
func Compose(a, b *GainTable) (*GainTable, error) {
	if a == nil || b == nil {
		return nil, errors.New("vector: compose with nil table")
	}
	bg := b.gains
	if b.N() != a.N() {
		var err error
		if bg, err = b.Resample(a.N()); err != nil {
			return nil, err
		}
	}
	out := make([]float64, a.N())
	for k := range out {
		out[k] = math.Min(MaxGain, math.Max(MinGain, a.gains[k]*bg[k]))
	}
	return NewGainTable(out)
}

// AnchorAngle is the angle (radians) of anchor k out of n.
func AnchorAngle(k, n int) float64 { return float64(k) * 2 * math.Pi / float64(n) }

// AngleOf converts a raw device delta (x right, y down) into the profile's
// angle convention (counter-clockwise from right, y up).
func AngleOf(dx, dy float64) float64 {
	// 0-dy instead of -dy keeps +0 for dy == 0, so "left" is +π, not -π.
	return math.Atan2(0-dy, dx)
}

// DirectionName returns a human readable (German) name for anchor k of n.
func DirectionName(k, n int) string {
	if n == 8 {
		return [...]string{"rechts", "rechts oben", "oben", "links oben", "links", "links unten", "unten", "rechts unten"}[mod(k, 8)]
	}
	return fmt.Sprintf("%.1f°", AnchorAngle(k, n)*180/math.Pi)
}

func catmullRom(p0, p1, p2, p3, t float64) float64 {
	t2 := t * t
	t3 := t2 * t
	return 0.5 * (2*p1 + (p2-p0)*t + (2*p0-5*p1+4*p2-p3)*t2 + (3*p1-p0-3*p2+p3)*t3)
}

func wrapAngle(a float64) float64 {
	a = math.Mod(a, 2*math.Pi)
	if a < 0 {
		a += 2 * math.Pi
	}
	if a >= 2*math.Pi { // guard against rounding of tiny negative values
		a = 0
	}
	return a
}

func mod(a, n int) int {
	a %= n
	if a < 0 {
		a += n
	}
	return a
}
