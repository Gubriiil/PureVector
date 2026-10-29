package calib

import (
	"errors"
	"math"
	"testing"
)

func TestChi2Survival(t *testing.T) {
	cases := []struct {
		x  float64
		df int
		p  float64
	}{
		{3.841458820694124, 1, 0.05},
		{5.991464547107979, 2, 0.05},
		{14.067140449340169, 7, 0.05},
		{18.475306906582357, 7, 0.01},
		{2.833106917815, 7, 0.9},
	}
	for _, c := range cases {
		if got := chi2Survival(c.x, c.df); math.Abs(got-c.p) > 1e-6 {
			t.Errorf("chi2Survival(%v,%d)=%v want %v", c.x, c.df, got, c.p)
		}
	}
	if chi2Survival(0, 3) != 1 {
		t.Error("P(X>0) must be 1")
	}
}

func TestCholeskySolve(t *testing.T) {
	a := matrix{{4, 2, 0.4}, {2, 5, 1}, {0.4, 1, 3}}
	want := []float64{1, -2, 0.5}
	b := make([]float64, 3)
	for i := range a {
		for j := range a[i] {
			b[i] += a[i][j] * want[j]
		}
	}
	l, err := cholesky(a)
	if err != nil {
		t.Fatal(err)
	}
	got := cholSolve(l, b)
	inv := cholInverse(l)
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Errorf("x[%d]=%v want %v", i, got[i], want[i])
		}
		for j := range want {
			s := 0.0
			for k := range want {
				s += a[i][k] * inv[k][j]
			}
			if e := map[bool]float64{true: 1, false: 0}[i == j]; math.Abs(s-e) > 1e-12 {
				t.Errorf("A·A⁻¹[%d][%d]=%v", i, j, s)
			}
		}
	}
	if _, err := cholesky(matrix{{1, 2}, {2, 1}}); err == nil {
		t.Error("indefinite matrix accepted")
	}
}

// segment is one minimum-jerk movement of the synthetic crosshair.
type segment struct {
	startMs, durMs float64
	dx, dy         float64
}

// synthTrial renders movements into a trial sampled at 1 kHz with integer
// counts, the way the calibration page records it.
func synthTrial(tx, ty, r float64, endMs float64, segs ...segment) Trial {
	tr := Trial{Kind: KindOut, TX: tx, TY: ty, R: r, Hit: true, EndT: endMs}
	lastX, lastY := 0.0, 0.0
	for ms := 0.0; ms <= endMs; ms++ {
		x, y := 0.0, 0.0
		for _, s := range segs {
			u := (ms - s.startMs) / s.durMs
			if u <= 0 {
				continue
			}
			if u > 1 {
				u = 1
			}
			m := u * u * u * (10 - 15*u + 6*u*u)
			x += s.dx * m
			y += s.dy * m
		}
		x, y = math.Floor(x), math.Floor(y)
		if x != lastX || y != lastY {
			tr.Samples = append(tr.Samples, [3]float64{ms, x, y})
			lastX, lastY = x, y
		}
	}
	return tr
}

func TestAnalyzeTrialOvershoot(t *testing.T) {
	// Target 600 counts to the right; flick lands at 660 (+10 %), then a
	// corrective movement back onto the target.
	tr := synthTrial(600, 0, 40, 900,
		segment{220, 320, 660, 0},
		segment{640, 160, -60, 0})
	m := AnalyzeTrial(0, &tr)
	if !m.Valid {
		t.Fatalf("rejected: %s", m.Reject)
	}
	if math.Abs(m.Ratio-1.10) > 0.01 {
		t.Errorf("ratio %.4f want 1.10", m.Ratio)
	}
	if math.Abs(m.Theta) > 1e-9 {
		t.Errorf("theta %v want 0", m.Theta)
	}
	if m.Corrections != 1 {
		t.Errorf("corrections %d want 1", m.Corrections)
	}
	if math.Abs(m.ReactionMs-220) > 30 {
		t.Errorf("reaction %v want ≈220", m.ReactionMs)
	}
}

func TestAnalyzeTrialUndershootWithOverlappingCorrection(t *testing.T) {
	// Target 500 counts up (y negative). The primary covers 85 %, and the
	// correction starts before the primary has fully stopped.
	tr := synthTrial(0, -500, 35, 1000,
		segment{200, 300, 0, -425},
		segment{470, 220, 0, -75})
	m := AnalyzeTrial(0, &tr)
	if !m.Valid {
		t.Fatalf("rejected: %s", m.Reject)
	}
	if math.Abs(m.Ratio-0.85) > 0.04 {
		t.Errorf("ratio %.4f want ≈0.85", m.Ratio)
	}
	if math.Abs(m.Theta-math.Pi/2) > 1e-9 {
		t.Errorf("theta %v want π/2 (up)", m.Theta)
	}
}

func TestAnalyzeTrialRejections(t *testing.T) {
	short := synthTrial(15, 0, 4, 500, segment{200, 200, 15, 0})
	if m := AnalyzeTrial(0, &short); m.Reject != RejectTooShort {
		t.Errorf("short: %q", m.Reject)
	}
	still := Trial{TX: 500, R: 30, EndT: 3000}
	if m := AnalyzeTrial(0, &still); m.Reject != RejectNoMovement {
		t.Errorf("still: %q", m.Reject)
	}
	early := synthTrial(500, 0, 30, 800, segment{10, 300, 500, 0})
	if m := AnalyzeTrial(0, &early); m.Reject != RejectAnticipated {
		t.Errorf("anticipated: %q", m.Reject)
	}
	// Flick 40° away from the target direction.
	a := 40 * math.Pi / 180
	wrong := synthTrial(500, 0, 30, 900, segment{200, 300, 500 * math.Cos(a), -500 * math.Sin(a)})
	if m := AnalyzeTrial(0, &wrong); m.Reject != RejectDirection {
		t.Errorf("direction: %q", m.Reject)
	}
	far := synthTrial(400, 0, 30, 900, segment{200, 300, 900, 0})
	if m := AnalyzeTrial(0, &far); m.Reject != RejectRatio {
		t.Errorf("ratio: %q (ratio %.2f)", m.Reject, m.Ratio)
	}
}

func TestAnalyzeNeedsEnoughData(t *testing.T) {
	s := &Session{}
	for i := 0; i < 10; i++ {
		tr := synthTrial(600, 0, 40, 900, segment{220, 320, 600, 0})
		s.Trials = append(s.Trials, tr)
	}
	_, err := Analyze(s, Options{})
	if !errors.Is(err, ErrNotEnoughData) {
		t.Fatalf("err = %v", err)
	}
}

func TestSuggestModes(t *testing.T) {
	r := &Result{}
	for k, e := range []float64{0.05, 0.02, -0.03, -0.08, -0.04, 0.0, 0.01, 0.03} {
		r.PerDirection = append(r.PerDirection, Direction{Index: k, LogErr: e, Shrunk: math.Exp(e)})
	}
	mean := r.Suggest(RefMean, 1, 0.5, 2)
	prod := 1.0
	for _, g := range mean.Gains {
		prod *= g
	}
	if math.Abs(prod-1) > 1e-12 || math.Abs(mean.OverallChangePct) > 1e-9 {
		t.Errorf("mean mode changes overall sensitivity: product %v", prod)
	}
	abs := r.Suggest(RefAbsolute, 1, 0.5, 2)
	for k, p := range abs.PredictedRatio {
		if math.Abs(p-1) > 1e-12 {
			t.Errorf("absolute mode direction %d predicted ratio %v", k, p)
		}
	}
	st := r.Suggest(RefStrongest, 1, 0.5, 2)
	if st.ReferenceIndex != 5 || st.Gains[5] != 1 {
		t.Errorf("strongest should be direction 5 (log error 0): %+v", st)
	}
	half := r.Suggest(RefStrongest, 0.5, 0.5, 2)
	if want := math.Exp(-0.5 * 0.05); math.Abs(half.Gains[0]-want) > 1e-12 {
		t.Errorf("strength 0.5: gain %v want %v", half.Gains[0], want)
	}
	clamped := r.Suggest(RefStrongest, 1, 0.97, 1.03)
	if !clamped.Clamped[3] || clamped.Gains[3] != 1.03 {
		t.Errorf("clamping failed: %+v", clamped)
	}
}
