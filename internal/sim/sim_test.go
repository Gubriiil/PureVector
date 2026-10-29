package sim

import (
	"math"
	"math/rand"
	"testing"

	"github.com/Gubriiil/PureVector/internal/calib"
	"github.com/Gubriiil/PureVector/internal/vector"
)

// The "harmonic" test player overshoots towards the right (first harmonic)
// and along the up-right/down-left diagonal (second harmonic).
var harmonic = HarmonicBias(-0.03, 0.04, 0, 0.05, 45)

func trueRelative(bias func(float64) float64, n int) []float64 {
	v := make([]float64, n)
	m := 0.0
	for k := range v {
		v[k] = bias(vector.AnchorAngle(k, n))
		m += v[k]
	}
	for k := range v {
		v[k] -= m / float64(n)
	}
	return v
}

func estRelative(res *calib.Result) []float64 {
	v := make([]float64, len(res.PerDirection))
	m := 0.0
	for k, d := range res.PerDirection {
		v[k] = d.LogErr
		m += d.LogErr
	}
	for k := range v {
		v[k] -= m / float64(len(v))
	}
	return v
}

func rmse(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += (a[i] - b[i]) * (a[i] - b[i])
	}
	return math.Sqrt(s / float64(len(a)))
}

// residualRMS is the RMS directional bias (log units, over all angles) that
// remains when the player uses the given profile (nil = no profile).
func residualRMS(bias func(float64) float64, tab *vector.GainTable) float64 {
	const m = 720
	v := make([]float64, m)
	mean := 0.0
	for i := range v {
		th := float64(i) * 2 * math.Pi / m
		v[i] = bias(th)
		if tab != nil {
			v[i] += math.Log(tab.Exact(th))
		}
		mean += v[i]
	}
	mean /= m
	s := 0.0
	for _, x := range v {
		s += (x - mean) * (x - mean)
	}
	return math.Sqrt(s / m)
}

func session(bias func(float64) float64, rig Rig, seed int64) *calib.Session {
	return Run(DefaultPlayer(bias), rig, DefaultTask(), rand.New(rand.NewSource(seed)))
}

func mustAnalyze(t *testing.T, s *calib.Session, o calib.Options) *calib.Result {
	t.Helper()
	res, err := calib.Analyze(s, o)
	if err != nil {
		t.Fatalf("analyze: %v (counts %+v)", err, res.Counts)
	}
	return res
}

func TestSimulatedSessionIsUsable(t *testing.T) {
	sess := session(harmonic, Rig{PollHz: 1000}, 42)
	if len(sess.Trials) != 360 {
		t.Fatalf("trials = %d", len(sess.Trials))
	}
	res := mustAnalyze(t, sess, calib.Options{})
	if res.Counts.Valid < 340 {
		t.Errorf("only %d of %d trials valid: %v", res.Counts.Valid, res.Counts.Total, res.Counts.Rejected)
	}
	if res.Model.PValue > 1e-6 {
		t.Errorf("strong directional bias not detected, p = %g", res.Model.PValue)
	}
	if res.Quality != "good" {
		t.Errorf("quality %q", res.Quality)
	}
}

func TestNeutralPlayerKeepsNeutralProfile(t *testing.T) {
	flat := HarmonicBias(-0.03, 0, 0, 0, 0)
	for seed := int64(1); seed <= 5; seed++ {
		res := mustAnalyze(t, session(flat, Rig{PollHz: 1000}, seed), calib.Options{})
		for k, g := range res.Suggested.Gains {
			if math.Abs(g-1) > 0.02 {
				t.Errorf("seed %d: player without bias got gain %.3f for %s", seed, g, res.PerDirection[k].Name)
			}
		}
	}
}

func TestRecoversDirectionalBias(t *testing.T) {
	res := mustAnalyze(t, session(harmonic, Rig{PollHz: 1000}, 42), calib.Options{Reference: calib.RefMean})
	if e := rmse(trueRelative(harmonic, 8), estRelative(res)); e > 0.025 {
		t.Errorf("recovered bias RMSE %.4f (log units) too large", e)
	}
}

func TestProfileRemovesMostOfTheBias(t *testing.T) {
	before := residualRMS(harmonic, nil)
	sum := 0.0
	for seed := int64(1); seed <= 4; seed++ {
		res := mustAnalyze(t, session(harmonic, Rig{PollHz: 1000}, seed), calib.Options{Reference: calib.RefMean})
		sum += residualRMS(harmonic, vector.MustGainTable(res.Suggested.Gains))
	}
	after := sum / 4
	t.Logf("directional bias RMS before %.4f, after %.4f (%.0f %% removed)", before, after, 100*(1-after/before))
	if after > 0.4*before {
		t.Errorf("profile removed too little bias: before %.4f after %.4f", before, after)
	}
}

// TestClosedLoopRefine runs a second calibration with the first profile
// active in the (simulated) driver and checks that the measured directional
// spread collapses and that refining does not make the profile worse.
func TestClosedLoopRefine(t *testing.T) {
	res1 := mustAnalyze(t, session(harmonic, Rig{PollHz: 1000}, 7), calib.Options{})
	p1 := vector.MustGainTable(res1.Suggested.Gains)

	sess2 := session(harmonic, Rig{PollHz: 1000, Profile: p1}, 8)
	sess2.Mode = "refine"
	res2 := mustAnalyze(t, sess2, calib.Options{})
	if res2.Model.SpreadPct > 0.5*res1.Model.SpreadPct {
		t.Errorf("spread with profile %.2f %% vs %.2f %% without", res2.Model.SpreadPct, res1.Model.SpreadPct)
	}
	p2, err := vector.Compose(p1, vector.MustGainTable(res2.Suggested.Gains))
	if err != nil {
		t.Fatal(err)
	}
	r1, r2 := residualRMS(harmonic, p1), residualRMS(harmonic, p2)
	t.Logf("spread %.2f%% → %.2f%%, residual %.4f → %.4f after refine", res1.Model.SpreadPct, res2.Model.SpreadPct, r1, r2)
	if r2 > r1+0.006 {
		t.Errorf("refining made the profile worse: %.4f → %.4f", r1, r2)
	}
}

func TestPollingRatesAgree(t *testing.T) {
	tr := trueRelative(harmonic, 8)
	for _, hz := range []float64{125, 500, 8000} {
		res := mustAnalyze(t, session(harmonic, Rig{PollHz: hz}, 42), calib.Options{Reference: calib.RefMean})
		if e := rmse(tr, estRelative(res)); e > 0.025 {
			t.Errorf("%v Hz: RMSE %.4f", hz, e)
		}
	}
}

func TestStrongestReference(t *testing.T) {
	res := mustAnalyze(t, session(harmonic, Rig{PollHz: 1000}, 3), calib.Options{})
	s := res.Suggested
	if s.Reference != calib.RefStrongest || s.ReferenceIndex < 0 {
		t.Fatalf("unexpected reference %+v", s)
	}
	best := res.PerDirection[s.ReferenceIndex]
	for _, d := range res.PerDirection {
		if math.Abs(d.LogErr) < math.Abs(best.LogErr) {
			t.Errorf("%s lands closer to target than the chosen reference %s", d.Name, best.Name)
		}
	}
	if math.Abs(s.Gains[s.ReferenceIndex]-1) > 1e-12 {
		t.Errorf("reference direction gain %.4f, want 1", s.Gains[s.ReferenceIndex])
	}
	for k, p := range s.PredictedRatio {
		if !s.Clamped[k] && math.Abs(p-best.Shrunk) > 1e-9 {
			t.Errorf("predicted ratio %s = %.4f, want %.4f", res.PerDirection[k].Name, p, best.Shrunk)
		}
	}
}
