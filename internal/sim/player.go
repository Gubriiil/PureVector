// Package sim contains a virtual player for validating the calibration
// pipeline end to end.
//
// The player aims like a human: after a reaction time it makes a fast
// minimum-jerk primary movement whose extent carries a direction-dependent
// bias (the effect PureVector corrects), followed by visually guided
// corrective submovements until the target is hit. The hand movement is
// sampled by a virtual sensor at a chosen polling rate, quantised to integer
// counts, optionally run through the OmniVector transform, and recorded the
// way the calibration page records it.
package sim

import (
	"math"
	"math/rand"

	"github.com/Gubriiil/PureVector/internal/calib"
	"github.com/Gubriiil/PureVector/internal/vector"
)

// Player models the motor behaviour of the virtual human.
type Player struct {
	// Bias is the log extent error of primary movements as a function of
	// the movement direction (rad, counter-clockwise from right, y up).
	Bias          func(theta float64) float64
	AmpNoise      float64    // SD of log amplitude noise of primary movements
	DirNoiseDeg   float64    // SD of the direction noise of primary movements
	ReactionMs    [2]float64 // uniform range
	FittsA        float64    // primary duration (ms) = FittsA + FittsB·log2(1 + D/W)
	FittsB        float64
	CorrLatencyMs [2]float64 // pause before a corrective movement
	CorrNoise     float64    // SD of corrective endpoints, in target radii
	ClickDelayMs  [2]float64
}

// DefaultPlayer returns a plausible player with the given bias function.
func DefaultPlayer(bias func(float64) float64) Player {
	if bias == nil {
		bias = func(float64) float64 { return -0.03 }
	}
	return Player{
		Bias:          bias,
		AmpNoise:      0.08,
		DirNoiseDeg:   3,
		ReactionMs:    [2]float64{170, 290},
		FittsA:        110,
		FittsB:        55,
		CorrLatencyMs: [2]float64{60, 130},
		CorrNoise:     0.45,
		ClickDelayMs:  [2]float64{30, 90},
	}
}

// HarmonicBias builds a bias made of a constant, a first harmonic (one
// direction overshoots, the opposite undershoots – e.g. stronger wrist
// extension) and a second harmonic (an elliptic pattern like the inertia
// anisotropy of the arm). Angles are in degrees.
func HarmonicBias(mean, a1, phi1Deg, a2, phi2Deg float64) func(float64) float64 {
	p1 := phi1Deg * math.Pi / 180
	p2 := phi2Deg * math.Pi / 180
	return func(th float64) float64 {
		return mean + a1*math.Cos(th-p1) + a2*math.Cos(2*(th-p2))
	}
}

// Rig is the hardware/software chain between hand and screen.
type Rig struct {
	PollHz  float64
	Profile *vector.GainTable // OmniVector profile, nil = off
	Tracker vector.TrackerConfig
}

// Task mirrors the calibration page's target layout.
type Task struct {
	CountsPerDeg float64
	MinDeg       float64
	MaxDeg       float64
	RadiusDeg    float64
	Targets      int // outward targets; each is followed by a return target
	Bins         int // direction strata
	TimeoutMs    float64
	Directions   int
}

// DefaultTask: 800 DPI, 40 cm/360°, the same layout as the web page.
func DefaultTask() Task {
	return Task{
		CountsPerDeg: 800 / 2.54 * 40 / 360,
		MinDeg:       8,
		MaxDeg:       28,
		RadiusDeg:    1.4,
		Targets:      180,
		Bins:         16,
		TimeoutMs:    3000,
		Directions:   8,
	}
}

type simulator struct {
	p    Player
	rig  Rig
	task Task
	rng  *rand.Rand

	nowNs  int64
	pollNs int64
	handX  float64 // hand position in counts (continuous)
	handY  float64
	cumX   int64 // integer counts already reported by the sensor
	cumY   int64
	curX   float64 // crosshair position (world, counts)
	curY   float64
	track  *vector.Tracker

	trial   *calib.Trial
	spawnNs int64
}

// Run simulates a complete calibration session.
func Run(p Player, rig Rig, task Task, rng *rand.Rand) *calib.Session {
	if rig.PollHz <= 0 {
		rig.PollHz = 1000
	}
	s := &simulator{p: p, rig: rig, task: task, rng: rng,
		pollNs: int64(1e9 / rig.PollHz), track: vector.NewTracker(rig.Tracker)}
	sess := &calib.Session{
		Version: 1,
		Mode:    "fresh",
		Settings: calib.Settings{
			CountsPerDeg: task.CountsPerDeg, MinDeg: task.MinDeg, MaxDeg: task.MaxDeg,
			RadiusDeg: task.RadiusDeg, Directions: task.Directions,
		},
		Client: calib.ClientInfo{Unadjusted: true, EventType: "simulation", RateHz: math.Min(rig.PollHz, 1000)},
	}
	r := task.RadiusDeg * task.CountsPerDeg
	var bag []int
	for i := 0; i < task.Targets; i++ {
		if len(bag) == 0 {
			bag = rng.Perm(task.Bins)
		}
		bin := bag[0]
		bag = bag[1:]
		theta := (float64(bin) + rng.Float64()) * 2 * math.Pi / float64(task.Bins)
		logD := math.Log(task.MinDeg) + rng.Float64()*(math.Log(task.MaxDeg)-math.Log(task.MinDeg))
		dist := math.Exp(logD) * task.CountsPerDeg
		// Outward target relative to the centre (world origin); y down.
		tx, ty := dist*math.Cos(theta), -dist*math.Sin(theta)
		sess.Trials = append(sess.Trials, s.runTrial(calib.KindOut, tx, ty, r))
		sess.Trials = append(sess.Trials, s.runTrial(calib.KindReturn, 0, 0, r))
	}
	return sess
}

func (s *simulator) uniform(r [2]float64) float64 { return r[0] + s.rng.Float64()*(r[1]-r[0]) }

func (s *simulator) runTrial(kind string, tx, ty, r float64) calib.Trial {
	s.trial = &calib.Trial{Kind: kind, SX: s.curX, SY: s.curY, TX: tx, TY: ty, R: r,
		SpawnT: float64(s.nowNs) / 1e6}
	s.spawnNs = s.nowNs
	deadline := s.nowNs + int64(s.task.TimeoutMs*1e6)

	s.wait(s.uniform(s.p.ReactionMs))

	// Primary submovement with direction-dependent extent bias.
	ex, ey := tx-s.curX, ty-s.curY
	d := math.Hypot(ex, ey)
	theta := vector.AngleOf(ex, ey)
	amp := d * math.Exp(s.p.Bias(theta)+s.rng.NormFloat64()*s.p.AmpNoise)
	dir := theta + s.rng.NormFloat64()*s.p.DirNoiseDeg*math.Pi/180
	dur := s.p.FittsA + s.p.FittsB*math.Log2(1+d/(2*r))
	s.move(amp*math.Cos(dir), -amp*math.Sin(dir), dur)

	hit := false
	for attempt := 0; attempt < 8 && s.nowNs < deadline; attempt++ {
		ex, ey = tx-s.curX, ty-s.curY
		if math.Hypot(ex, ey) <= 0.92*r {
			s.wait(s.uniform(s.p.ClickDelayMs))
			s.click()
			hit = true
			break
		}
		s.wait(s.uniform(s.p.CorrLatencyMs))
		th := vector.AngleOf(ex, ey)
		k := math.Exp(s.p.Bias(th))
		hx := ex*k + s.rng.NormFloat64()*s.p.CorrNoise*r
		hy := ey*k + s.rng.NormFloat64()*s.p.CorrNoise*r
		cd := math.Hypot(ex, ey)
		s.move(hx, hy, 80+0.5*s.p.FittsB*math.Log2(1+cd/(2*r)))
	}
	s.trial.Hit = hit
	s.trial.EndT = float64(s.nowNs-s.spawnNs) / 1e6
	if !hit && s.nowNs < deadline {
		s.wait(float64(deadline-s.nowNs) / 1e6)
		s.trial.EndT = float64(s.nowNs-s.spawnNs) / 1e6
	}
	t := *s.trial
	s.trial = nil
	return t
}

func (s *simulator) click() {
	s.trial.Clicks = append(s.trial.Clicks, float64(s.nowNs-s.spawnNs)/1e6)
}

// wait advances time without hand movement.
func (s *simulator) wait(ms float64) { s.nowNs += int64(ms * 1e6) }

// move performs a minimum-jerk hand movement by (dx, dy) counts.
func (s *simulator) move(dx, dy, durMs float64) {
	x0, y0 := s.handX, s.handY
	t0 := s.nowNs
	t1 := t0 + int64(durMs*1e6)
	if t1 <= t0 {
		t1 = t0 + 1
	}
	tick := (t0/s.pollNs + 1) * s.pollNs
	for ; tick <= t1+s.pollNs; tick += s.pollNs {
		u := float64(tick-t0) / float64(t1-t0)
		if u > 1 {
			u = 1
		}
		m := u * u * u * (10 - 15*u + 6*u*u)
		s.handX = x0 + dx*m
		s.handY = y0 + dy*m
		s.poll(tick)
	}
	s.nowNs = tick - s.pollNs
}

// poll emulates one sensor report and its way to the calibration page.
func (s *simulator) poll(tick int64) {
	cx, cy := int64(math.Floor(s.handX)), int64(math.Floor(s.handY))
	dx, dy := cx-s.cumX, cy-s.cumY
	s.cumX, s.cumY = cx, cy
	if dx == 0 && dy == 0 {
		return
	}
	ox, oy := s.track.Apply(s.rig.Profile, int32(dx), int32(dy), tick)
	s.curX += float64(ox)
	s.curY += float64(oy)
	if s.trial == nil {
		return
	}
	ms := math.Floor(float64(tick-s.spawnNs) / 1e6)
	smp := [3]float64{ms, s.curX - s.trial.SX, s.curY - s.trial.SY}
	if n := len(s.trial.Samples); n > 0 && s.trial.Samples[n-1][0] == ms {
		s.trial.Samples[n-1] = smp
	} else {
		s.trial.Samples = append(s.trial.Samples, smp)
	}
}
