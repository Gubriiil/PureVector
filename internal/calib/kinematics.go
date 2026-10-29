package calib

import (
	"math"
	"sort"

	"github.com/Gubriiil/PureVector/internal/vector"
)

// Rejection reasons for trials that cannot be used.
const (
	RejectNoMovement  = "no_movement"
	RejectTooShort    = "too_short"
	RejectAnticipated = "anticipated"
	RejectSlowStart   = "slow_start"
	RejectDuration    = "primary_duration"
	RejectRatio       = "ratio_out_of_range"
	RejectDirection   = "direction_error"
	RejectIncomplete  = "incomplete"
)

// TrialMetrics are the kinematic features of one trial.
type TrialMetrics struct {
	Index       int     `json:"index"`
	Kind        string  `json:"kind"`
	Valid       bool    `json:"valid"`
	Reject      string  `json:"reject,omitempty"`
	Theta       float64 `json:"theta"`    // movement direction (rad, CCW from right, y up)
	Distance    float64 `json:"distance"` // counts from movement onset to target centre
	Ratio       float64 `json:"ratio"`    // primary endpoint (along the target axis) / distance
	AngErr      float64 `json:"angErr"`   // direction error of the primary endpoint (rad, + = CCW)
	ReactionMs  float64 `json:"reactionMs"`
	PrimaryMs   float64 `json:"primaryMs"`
	PeakSpeed   float64 `json:"peakSpeed"`   // counts per ms
	TimeToHitMs float64 `json:"timeToHitMs"` // 0 if the target was not hit
	Corrections int     `json:"corrections"`
	Hit         bool    `json:"hit"`
}

// Kinematic parsing thresholds. They follow the usual submovement parsing
// rules (Meyer et al., 1988): the primary submovement ends when the velocity
// towards the target falls below a small fraction of its peak, reverses, or
// reaches a clear local minimum that is followed by a new acceleration.
const (
	onsetFrac         = 0.05 // movement onset: velocity above 5 % of peak
	endFrac           = 0.05 // primary end: velocity below 5 % of peak
	minimumFrac       = 0.60 // a local minimum below 60 % of peak may end the primary…
	minimumRiseFrac   = 0.10 // …if velocity then rises again by 10 % of peak
	minimumLookMs     = 80
	correctionOnFrac  = 0.10
	correctionOffFrac = 0.05
	maxTrialMs        = 10000

	minReactionMs = 60
	maxReactionMs = 1500
	minPrimaryMs  = 50
	maxPrimaryMs  = 1500
	minRatio      = 0.40
	maxRatio      = 1.80
	maxAngErr     = 25 * math.Pi / 180
)

// AnalyzeTrial extracts the kinematic features of one trial.
func AnalyzeTrial(index int, tr *Trial) TrialMetrics {
	m := TrialMetrics{Index: index, Kind: tr.Kind, Hit: tr.Hit}
	if tr.Hit {
		m.TimeToHitMs = tr.EndT
	}
	reject := func(reason string) TrialMetrics {
		m.Valid = false
		m.Reject = reason
		return m
	}

	tx, ty := tr.TX-tr.SX, tr.TY-tr.SY
	dist0 := math.Hypot(tx, ty)
	m.Theta = vector.AngleOf(tx, ty)
	m.Distance = dist0
	if dist0 < math.Max(3*tr.R, 20) {
		return reject(RejectTooShort)
	}
	if len(tr.Samples) < 3 {
		return reject(RejectNoMovement)
	}

	samples := append([][3]float64(nil), tr.Samples...)
	sort.SliceStable(samples, func(i, j int) bool { return samples[i][0] < samples[j][0] })
	endMs := math.Max(tr.EndT, samples[len(samples)-1][0]) + 30
	if endMs > maxTrialMs {
		endMs = maxTrialMs
	}
	xs, ys := resampleStepHold(samples, int(math.Ceil(endMs)))
	sigma := math.Max(5, medianInterval(samples))
	sx := gaussSmooth(xs, sigma)
	sy := gaussSmooth(ys, sigma)

	// The search window for the primary movement ends at the hit (or the
	// end of the recording for timed-out targets).
	jLimit := len(sx) - 1
	if tr.EndT > 0 && int(tr.EndT) < jLimit {
		jLimit = int(tr.EndT)
	}

	// Pass 1: axis from the spawn position to the target.
	v0 := alongVelocity(sx, sy, 0, 0, tx/dist0, ty/dist0)
	jp := argmax(v0, 0, jLimit)
	if jp < 0 || v0[jp] < 0.02 {
		return reject(RejectNoMovement)
	}
	jo := jp
	for jo > 0 && v0[jo] >= onsetFrac*v0[jp] {
		jo--
	}

	// Pass 2: axis from the actual onset position to the target.
	ox, oy := sx[jo], sy[jo]
	dx, dy := tx-ox, ty-oy
	dist := math.Hypot(dx, dy)
	if dist < math.Max(3*tr.R, 20) {
		return reject(RejectTooShort)
	}
	ux, uy := dx/dist, dy/dist
	v := alongVelocity(sx, sy, ox, oy, ux, uy)
	jp = argmax(v, jo, jLimit)
	if jp < 0 || v[jp] < 0.02 {
		return reject(RejectNoMovement)
	}
	vpk := v[jp]

	je := primaryEnd(v, jp, vpk)
	if je < 0 {
		return reject(RejectIncomplete)
	}

	ex, ey := sx[je]-ox, sy[je]-oy
	m.Theta = vector.AngleOf(dx, dy)
	m.Distance = dist
	m.Ratio = (ex*ux + ey*uy) / dist
	m.AngErr = wrapPi(vector.AngleOf(ex, ey) - m.Theta)
	m.ReactionMs = float64(jo)
	m.PrimaryMs = float64(je - jo)
	m.PeakSpeed = vpk
	m.Corrections = countCorrections(sx, sy, je, jLimit, vpk)

	switch {
	case m.ReactionMs < minReactionMs:
		return reject(RejectAnticipated)
	case m.ReactionMs > maxReactionMs:
		return reject(RejectSlowStart)
	case m.PrimaryMs < minPrimaryMs || m.PrimaryMs > maxPrimaryMs:
		return reject(RejectDuration)
	case !(m.Ratio >= minRatio && m.Ratio <= maxRatio):
		return reject(RejectRatio)
	case math.Abs(m.AngErr) > maxAngErr:
		return reject(RejectDirection)
	}
	m.Valid = true
	return m
}

// primaryEnd finds the end of the primary submovement after the peak jp.
func primaryEnd(v []float64, jp int, vpk float64) int {
	for j := jp + 1; j < len(v); j++ {
		if v[j] <= endFrac*vpk { // slowed down, stopped or reversed
			return j
		}
		if j+1 < len(v) && v[j] < minimumFrac*vpk && v[j] <= v[j-1] && v[j] < v[j+1] {
			// Local minimum: only a boundary if a new submovement follows.
			hi := v[j]
			for k := j + 1; k < len(v) && k <= j+minimumLookMs; k++ {
				hi = math.Max(hi, v[k])
			}
			if hi-v[j] >= minimumRiseFrac*vpk {
				return j
			}
		}
	}
	return -1
}

// countCorrections counts speed bumps after the primary submovement.
func countCorrections(sx, sy []float64, from, to int, vpk float64) int {
	n := 0
	active := false
	for j := from + 1; j < to && j+1 < len(sx); j++ {
		sp := 0.5 * math.Hypot(sx[j+1]-sx[j-1], sy[j+1]-sy[j-1])
		if !active && sp > correctionOnFrac*vpk {
			active = true
			n++
		} else if active && sp < correctionOffFrac*vpk {
			active = false
		}
	}
	return n
}

// resampleStepHold converts irregular position samples into a 1 ms grid.
// A sample at time t means the crosshair moved to (x, y) at t and stayed
// there until the next sample.
func resampleStepHold(samples [][3]float64, n int) ([]float64, []float64) {
	if n < 2 {
		n = 2
	}
	xs := make([]float64, n)
	ys := make([]float64, n)
	k := 0
	cx, cy := 0.0, 0.0
	for j := 0; j < n; j++ {
		for k < len(samples) && samples[k][0] <= float64(j) {
			cx, cy = samples[k][1], samples[k][2]
			k++
		}
		xs[j], ys[j] = cx, cy
	}
	return xs, ys
}

// medianInterval is the typical time between input events in ms.
func medianInterval(samples [][3]float64) float64 {
	if len(samples) < 2 {
		return 1
	}
	d := make([]float64, 0, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		if dt := samples[i][0] - samples[i-1][0]; dt > 0 {
			d = append(d, dt)
		}
	}
	if len(d) == 0 {
		return 1
	}
	return median(d)
}

// gaussSmooth applies a Gaussian filter (sigma in samples) with edge
// replication.
func gaussSmooth(v []float64, sigma float64) []float64 {
	r := int(math.Ceil(3 * sigma))
	k := make([]float64, 2*r+1)
	s := 0.0
	for i := -r; i <= r; i++ {
		w := math.Exp(-0.5 * float64(i*i) / (sigma * sigma))
		k[i+r] = w
		s += w
	}
	for i := range k {
		k[i] /= s
	}
	out := make([]float64, len(v))
	last := len(v) - 1
	for j := range v {
		acc := 0.0
		for i := -r; i <= r; i++ {
			idx := j + i
			if idx < 0 {
				idx = 0
			} else if idx > last {
				idx = last
			}
			acc += k[i+r] * v[idx]
		}
		out[j] = acc
	}
	return out
}

// alongVelocity is the velocity (counts/ms) projected on the axis u,
// starting from origin o.
func alongVelocity(sx, sy []float64, ox, oy, ux, uy float64) []float64 {
	n := len(sx)
	a := make([]float64, n)
	for j := range a {
		a[j] = (sx[j]-ox)*ux + (sy[j]-oy)*uy
	}
	v := make([]float64, n)
	for j := range v {
		switch {
		case n < 2:
		case j == 0:
			v[j] = a[1] - a[0]
		case j == n-1:
			v[j] = a[n-1] - a[n-2]
		default:
			v[j] = 0.5 * (a[j+1] - a[j-1])
		}
	}
	return v
}

func argmax(v []float64, from, to int) int {
	if from < 0 {
		from = 0
	}
	if to >= len(v) {
		to = len(v) - 1
	}
	best := -1
	for j := from; j <= to; j++ {
		if best < 0 || v[j] > v[best] {
			best = j
		}
	}
	return best
}
