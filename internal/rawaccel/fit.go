// Package rawaccel translates OmniVector profiles into settings for Raw
// Accel (https://github.com/RawAccelOfficial/rawaccel), a signed Windows
// mouse filter driver that many anti-cheat systems accept.
//
// Raw Accel cannot express an arbitrary direction profile, but it has four
// directional sensitivity controls: an overall multiplier, a vertical (Y/X)
// ratio, a left (L/R) ratio and an up (U/D) ratio. Together they scale the
// x component of each movement by R (right) or L (left) and the y component
// by U (up) or D (down). The gain in direction θ is therefore
//
//	g(θ) = sqrt((a·cos θ)² + (b·sin θ)²),  a ∈ {R, L},  b ∈ {U, D}
//
// Fit finds the R, L, U, D that reproduce a profile best (least squares on
// the log gains) and reports how much of the profile that model covers.
package rawaccel

import (
	"fmt"
	"math"

	"github.com/Gubriiil/PureVector/internal/vector"
)

// Axis gains of the four-direction model.
type Axis struct {
	Right float64 `json:"right"`
	Left  float64 `json:"left"`
	Up    float64 `json:"up"`
	Down  float64 `json:"down"`
}

// Factors are multipliers for the Raw Accel settings.
type Factors struct {
	Sens float64 `json:"sens"` // "Sensitivity multiplier" / "Output DPI"
	YX   float64 `json:"yx"`   // "Y/X ratio" (vertical multiplier)
	LR   float64 `json:"lr"`   // "L/R ratio" (left multiplier)
	UD   float64 `json:"ud"`   // "U/D ratio" (up multiplier)
}

// Fit is the result of fitting a profile.
type Fit struct {
	Axis        Axis      `json:"axis"`
	Factors     Factors   `json:"factors"`
	Model       []float64 `json:"model"` // model gain at each profile anchor
	CoveragePct float64   `json:"coveragePct"`
	MaxErrorPct float64   `json:"maxErrorPct"`
}

// ModelGain evaluates the four-direction model at angle theta (radians,
// counter-clockwise from right, y up).
func ModelGain(a Axis, theta float64) float64 {
	c, s := math.Cos(theta), math.Sin(theta)
	x := a.Right
	if c < 0 {
		x = a.Left
	}
	y := a.Up
	if s < 0 {
		y = a.Down
	}
	return math.Hypot(x*c, y*s)
}

// FactorsFor converts axis gains into Raw Accel multipliers:
// right = sens, left = sens·lr, down = sens·yx, up = sens·yx·ud.
func FactorsFor(a Axis) Factors {
	return Factors{Sens: a.Right, YX: a.Down / a.Right, LR: a.Left / a.Right, UD: a.Up / a.Down}
}

// FitProfile fits the four-direction model to profile gains.
func FitProfile(gains []float64) (Fit, error) {
	tab, err := vector.NewGainTable(gains)
	if err != nil {
		return Fit{}, err
	}
	n := len(gains)
	theta := make([]float64, n)
	target := make([]float64, n)
	for k := range gains {
		theta[k] = vector.AnchorAngle(k, n)
		target[k] = math.Log(gains[k])
	}
	// Start at the profile's own axis gains.
	p := [4]float64{
		math.Log(tab.Exact(0)), math.Log(tab.Exact(math.Pi)),
		math.Log(tab.Exact(math.Pi / 2)), math.Log(tab.Exact(-math.Pi / 2)),
	}
	axis := func(p [4]float64) Axis {
		return Axis{Right: math.Exp(p[0]), Left: math.Exp(p[1]), Up: math.Exp(p[2]), Down: math.Exp(p[3])}
	}
	resid := func(p [4]float64) []float64 {
		a := axis(p)
		r := make([]float64, n)
		for k := range r {
			r[k] = math.Log(ModelGain(a, theta[k])) - target[k]
		}
		return r
	}
	sse := func(r []float64) float64 {
		s := 0.0
		for _, v := range r {
			s += v * v
		}
		return s
	}

	// Levenberg–Marquardt on the log parameters.
	lambda := 1e-3
	r := resid(p)
	cur := sse(r)
	for iter := 0; iter < 200 && cur > 1e-18; iter++ {
		var jac [][4]float64
		for k := 0; k < n; k++ {
			jac = append(jac, [4]float64{})
		}
		const h = 1e-6
		for j := 0; j < 4; j++ {
			q := p
			q[j] += h
			rq := resid(q)
			for k := 0; k < n; k++ {
				jac[k][j] = (rq[k] - r[k]) / h
			}
		}
		var jtj [4][4]float64
		var jtr [4]float64
		for k := 0; k < n; k++ {
			for a := 0; a < 4; a++ {
				jtr[a] += jac[k][a] * r[k]
				for b := 0; b < 4; b++ {
					jtj[a][b] += jac[k][a] * jac[k][b]
				}
			}
		}
		improved := false
		for try := 0; try < 10; try++ {
			var m [4][4]float64
			for a := 0; a < 4; a++ {
				for b := 0; b < 4; b++ {
					m[a][b] = jtj[a][b]
				}
				m[a][a] += lambda * (1 + jtj[a][a])
			}
			step, ok := solve4(m, jtr)
			if !ok {
				lambda *= 10
				continue
			}
			var q [4]float64
			for a := 0; a < 4; a++ {
				q[a] = p[a] - step[a]
			}
			rq := resid(q)
			if s := sse(rq); s < cur {
				p, r, cur = q, rq, s
				lambda = math.Max(lambda/10, 1e-12)
				improved = true
				break
			}
			lambda *= 10
		}
		if !improved {
			break
		}
	}

	a := axis(p)
	fit := Fit{Axis: a, Factors: FactorsFor(a), Model: make([]float64, n)}
	mean := 0.0
	for _, t := range target {
		mean += t
	}
	mean /= float64(n)
	ssTot, ssRes := 0.0, 0.0
	for k := range gains {
		fit.Model[k] = ModelGain(a, theta[k])
		d := math.Log(fit.Model[k]) - target[k]
		ssRes += d * d
		ssTot += (target[k] - mean) * (target[k] - mean)
		fit.MaxErrorPct = math.Max(fit.MaxErrorPct, 100*math.Abs(fit.Model[k]/gains[k]-1))
	}
	fit.CoveragePct = 100
	if ssTot > 1e-12 {
		fit.CoveragePct = math.Max(0, 100*(1-ssRes/ssTot))
	}
	return fit, nil
}

func solve4(m [4][4]float64, b [4]float64) ([4]float64, bool) {
	// Gaussian elimination with partial pivoting.
	var a [4][5]float64
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			a[i][j] = m[i][j]
		}
		a[i][4] = b[i]
	}
	for c := 0; c < 4; c++ {
		piv := c
		for r := c + 1; r < 4; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[piv][c]) {
				piv = r
			}
		}
		if math.Abs(a[piv][c]) < 1e-15 {
			return [4]float64{}, false
		}
		a[c], a[piv] = a[piv], a[c]
		for r := 0; r < 4; r++ {
			if r == c {
				continue
			}
			f := a[r][c] / a[c][c]
			for j := c; j < 5; j++ {
				a[r][j] -= f * a[c][j]
			}
		}
	}
	var x [4]float64
	for i := 0; i < 4; i++ {
		x[i] = a[i][4] / a[i][i]
	}
	return x, true
}

// Describe returns a short human-readable summary (German).
func (f Fit) Describe() string {
	return fmt.Sprintf("Raw Accel: Sensitivity ×%.4f, Y/X ×%.4f, L/R ×%.4f, U/D ×%.4f (deckt %.0f %% des Profils ab)",
		f.Factors.Sens, f.Factors.YX, f.Factors.LR, f.Factors.UD, f.CoveragePct)
}
