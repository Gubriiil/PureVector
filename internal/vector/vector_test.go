package vector

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

var testGains = []float64{0.90, 1.06, 1.00, 0.95, 1.10, 0.97, 1.03, 0.92}

func TestAngleConvention(t *testing.T) {
	cases := []struct {
		dx, dy, want float64
	}{
		{1, 0, 0},
		{0, -1, math.Pi / 2}, // mouse pushed away from the user = "up"
		{-1, 0, math.Pi},
		{0, 1, -math.Pi / 2},
	}
	for _, c := range cases {
		if got := AngleOf(c.dx, c.dy); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("AngleOf(%v,%v)=%v want %v", c.dx, c.dy, got, c.want)
		}
	}
}

func TestNeutralTable(t *testing.T) {
	tab := Neutral(8)
	if !tab.IsNeutral() {
		t.Fatal("neutral table not flagged neutral")
	}
	for i := 0; i < 1000; i++ {
		th := float64(i) * 0.0137
		if g := tab.At(th); g != 1 {
			t.Fatalf("At(%v)=%v want 1", th, g)
		}
	}
}

func TestAnchorsAreHit(t *testing.T) {
	tab := MustGainTable(testGains)
	for k, g := range testGains {
		th := tab.AnchorAngle(k)
		if got := tab.Exact(th); math.Abs(got-g) > 1e-12 {
			t.Errorf("Exact anchor %d = %v want %v", k, got, g)
		}
		if got := tab.At(th); math.Abs(got-g) > 1e-6 {
			t.Errorf("At anchor %d = %v want %v", k, got, g)
		}
		// Same anchor, expressed as a negative angle.
		if got := tab.At(th - 2*math.Pi); math.Abs(got-g) > 1e-6 {
			t.Errorf("At(anchor %d - 2π) = %v want %v", k, got, g)
		}
	}
}

func TestLUTMatchesExactAndIsSmooth(t *testing.T) {
	tab := MustGainTable(testGains)
	maxDiff, maxStep := 0.0, 0.0
	prev := tab.At(-math.Pi)
	for i := 1; i <= 20000; i++ {
		th := -math.Pi + float64(i)*2*math.Pi/20000
		lut, ex := tab.At(th), tab.Exact(th)
		maxDiff = math.Max(maxDiff, math.Abs(lut-ex))
		maxStep = math.Max(maxStep, math.Abs(lut-prev))
		prev = lut
	}
	if maxDiff > 5e-4 {
		t.Errorf("LUT deviates from exact interpolation by %v", maxDiff)
	}
	if maxStep > 1e-3 {
		t.Errorf("gain function not smooth: step %v between neighbouring angles", maxStep)
	}
}

func TestInvalidTables(t *testing.T) {
	if _, err := NewGainTable([]float64{1, 1, 1}); err == nil {
		t.Error("3 directions accepted")
	}
	if _, err := NewGainTable([]float64{1, 1, math.NaN(), 1}); err == nil {
		t.Error("NaN accepted")
	}
	if _, err := NewGainTable([]float64{1, 1, 9, 1}); err == nil {
		t.Error("gain 9 accepted")
	}
}

func TestComposeAndResample(t *testing.T) {
	a := MustGainTable(testGains)
	inv := make([]float64, len(testGains))
	for i, g := range testGains {
		inv[i] = 1 / g
	}
	c, err := Compose(a, MustGainTable(inv))
	if err != nil {
		t.Fatal(err)
	}
	for k, g := range c.Gains() {
		if math.Abs(g-1) > 1e-12 {
			t.Errorf("compose with inverse: anchor %d = %v", k, g)
		}
	}
	r, err := a.Resample(16)
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k < 8; k++ {
		if math.Abs(r[2*k]-testGains[k]) > 1e-12 {
			t.Errorf("resample lost anchor %d", k)
		}
	}
	// Composing tables with different anchor counts resamples b.
	c2, err := Compose(Neutral(8), MustGainTable(r))
	if err != nil {
		t.Fatal(err)
	}
	for k, g := range c2.Gains() {
		if math.Abs(g-testGains[k]) > 1e-12 {
			t.Errorf("compose 8x16: anchor %d = %v want %v", k, g, testGains[k])
		}
	}
}

func TestTrackerNeutralIsIdentity(t *testing.T) {
	tr := NewTracker(DefaultTrackerConfig())
	rng := rand.New(rand.NewSource(1))
	now := int64(0)
	for i := 0; i < 10000; i++ {
		dx, dy := int32(rng.Intn(41)-20), int32(rng.Intn(41)-20)
		now += int64(time.Millisecond)
		ox, oy := tr.Apply(Neutral(8), dx, dy, now)
		if ox != dx || oy != dy {
			t.Fatalf("neutral changed (%d,%d) to (%d,%d)", dx, dy, ox, oy)
		}
		ox, oy = tr.Apply(nil, dx, dy, now)
		if ox != dx || oy != dy {
			t.Fatalf("nil table changed (%d,%d) to (%d,%d)", dx, dy, ox, oy)
		}
	}
}

func TestTrackerPreservesAxisDirections(t *testing.T) {
	tab := MustGainTable(testGains)
	tr := NewTracker(DefaultTrackerConfig())
	now := int64(0)
	sumX := 0
	for i := 0; i < 5000; i++ {
		now += int64(125 * time.Microsecond)
		ox, oy := tr.Apply(tab, int32(1+i%3), 0, now)
		if oy != 0 {
			t.Fatalf("horizontal input produced vertical output %d", oy)
		}
		sumX += int(ox)
	}
	// Total input was 5000 reports averaging 2 counts → 10000 counts right.
	want := 0.90 * 9999
	if math.Abs(float64(sumX)-want) > 2 {
		t.Errorf("rightward gain: got %d counts want ≈%.0f", sumX, want)
	}
}

func TestTrackerRemainderBounded(t *testing.T) {
	tab := MustGainTable(testGains)
	tr := NewTracker(DefaultTrackerConfig())
	rng := rand.New(rand.NewSource(7))
	now := int64(0)
	for i := 0; i < 20000; i++ {
		now += int64(time.Millisecond)
		tr.Apply(tab, int32(rng.Intn(7)-3), int32(rng.Intn(7)-3), now)
		rx, ry := tr.Remainder()
		if math.Abs(rx) > 0.5 || math.Abs(ry) > 0.5 {
			t.Fatalf("remainder out of range: %v %v", rx, ry)
		}
	}
}

// minJerk is the classic minimum-jerk position profile on [0,1].
func minJerk(s float64) float64 {
	if s <= 0 {
		return 0
	}
	if s >= 1 {
		return 1
	}
	return s * s * s * (10 - 15*s + 6*s*s)
}

// simulateStroke moves a virtual sensor along a straight line and returns
// the integer reports a mouse polling at rateHz would send.
func simulateStroke(theta, dist, durS, rateHz float64) [][2]int32 {
	ux, uy := math.Cos(theta), -math.Sin(theta) // device coordinates, y down
	steps := int(durS * rateHz)
	var out [][2]int32
	var cx, cy int64
	for i := 1; i <= steps; i++ {
		s := minJerk(float64(i) / float64(steps))
		px, py := int64(math.Floor(dist*s*ux)), int64(math.Floor(dist*s*uy))
		dx, dy := px-cx, py-cy
		cx, cy = px, py
		if dx != 0 || dy != 0 {
			out = append(out, [2]int32{int32(dx), int32(dy)})
		}
	}
	return out
}

func TestTrackerIndependentOfPollingRate(t *testing.T) {
	tab := MustGainTable(testGains)
	for _, deg := range []float64{0, 10, 30, 45, 67.5, 100, 160, 200, 225, 290, 315, 350} {
		theta := deg * math.Pi / 180
		want := tab.Exact(theta)
		for _, rate := range []float64{125, 1000, 8000} {
			for _, dist := range []float64{60, 400, 2500} {
				tr := NewTracker(DefaultTrackerConfig())
				reports := simulateStroke(theta, dist, 0.35, rate)
				var inX, inY, outX, outY float64
				now := int64(0)
				for _, r := range reports {
					now += int64(float64(time.Second) / rate)
					ox, oy := tr.Apply(tab, r[0], r[1], now)
					inX += float64(r[0])
					inY += float64(r[1])
					outX += float64(ox)
					outY += float64(oy)
				}
				inLen := math.Hypot(inX, inY)
				outLen := math.Hypot(outX, outY)
				gain := outLen / inLen
				// Tolerance: rounding (≤1 count) plus the short warm-up of the
				// direction memory at the very start of the stroke.
				tol := 1.5/inLen + 0.004
				if math.Abs(gain-want) > tol {
					t.Errorf("θ=%v° rate=%v dist=%v: effective gain %.4f want %.4f (tol %.4f)", deg, rate, dist, gain, want, tol)
				}
				// The direction of the total movement must be preserved.
				dAng := math.Abs(math.Remainder(AngleOf(outX, outY)-AngleOf(inX, inY), 2*math.Pi))
				if dAng > 2.0/inLen+0.003 {
					t.Errorf("θ=%v° rate=%v dist=%v: direction changed by %.3f°", deg, rate, dist, dAng*180/math.Pi)
				}
			}
		}
	}
}

func TestTrackerIdleResetForgetsDirection(t *testing.T) {
	tab := MustGainTable(testGains)
	tr := NewTracker(DefaultTrackerConfig())
	now := int64(0)
	for i := 0; i < 50; i++ {
		now += int64(time.Millisecond)
		tr.Apply(tab, 10, 0, now) // moving right
	}
	now += int64(200 * time.Millisecond) // pause
	tr.Apply(tab, -10, 0, now)           // first report of a leftward move
	if math.Abs(tr.LastGain-1.10) > 1e-3 {
		t.Errorf("after idle reset the leftward gain should apply immediately, got %v", tr.LastGain)
	}
}

func BenchmarkTrackerApply(b *testing.B) {
	tab := MustGainTable(testGains)
	tr := NewTracker(DefaultTrackerConfig())
	now := int64(0)
	for i := 0; i < b.N; i++ {
		now += 125000
		tr.Apply(tab, int32(i%7-3), int32(i%5-2), now)
	}
}
