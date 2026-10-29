package calib

import (
	"errors"
	"math"
)

// Basis selects how a trial's direction is mapped onto the anchors.
const (
	// BasisHat interpolates linearly between the two neighbouring anchors
	// (the model function is piecewise linear in the angle).
	BasisHat = "hat"
	// BasisSector assigns each trial to its nearest anchor.
	BasisSector = "sector"
)

type observation struct {
	theta float64 // rad
	y     float64 // log ratio
	z     float64 // centred log distance
	q     float64 // centred "return trial" indicator
}

type directionalFit struct {
	n      int
	a      []float64 // per-anchor log ratio (at mean distance / trial mix)
	se     []float64 // standard errors of a
	b, c   float64   // distance slope, return-trial offset
	sigma  float64   // robust residual scale
	wald   float64   // test statistic for "no directional differences"
	df     int
	pValue float64

	// Random-effects shrinkage.
	tau2   float64
	center float64   // precision-weighted mean of a
	shrunk []float64 // a after shrinkage (used for the correction)
}

const (
	huberK      = 1.345
	irlsMaxIter = 50
)

// smoothLambda couples neighbouring anchors (a weak smoothness prior that
// also keeps the system solvable when a direction has no data).
var smoothLambda = 0.5

func basisRow(theta float64, n int, basis string, row []float64) {
	for k := 0; k < n; k++ {
		row[k] = 0
	}
	s := wrap2Pi(theta) / (2 * math.Pi / float64(n))
	k0 := int(math.Floor(s)) % n
	f := s - math.Floor(s)
	if basis == BasisSector {
		if f >= 0.5 {
			row[(k0+1)%n] = 1
		} else {
			row[k0] = 1
		}
		return
	}
	row[k0] += 1 - f
	row[(k0+1)%n] += f
}

// fitDirectional fits y = Σ_k B_k(θ)·a_k + b·z + c·q with Huber-weighted
// iteratively reweighted least squares.
func fitDirectional(obs []observation, n int, basis string) (*directionalFit, error) {
	p := n + 2
	if len(obs) < p {
		return nil, errors.New("calib: not enough observations")
	}
	x := newMatrix(len(obs), p)
	y := make([]float64, len(obs))
	varZ, varQ := 0.0, 0.0
	for i, o := range obs {
		basisRow(o.theta, n, basis, x[i][:n])
		x[i][n] = o.z
		x[i][n+1] = o.q
		y[i] = o.y
		varZ += o.z * o.z
		varQ += o.q * o.q
	}

	// Penalty: smoothLambda·Σ(a_k - a_{k+1})² plus a tiny ridge on b and c
	// (keeps them identifiable when distances or trial kinds do not vary).
	penalty := newMatrix(p, p)
	for k := 0; k < n; k++ {
		k1 := (k + 1) % n
		penalty[k][k] += smoothLambda
		penalty[k1][k1] += smoothLambda
		penalty[k][k1] -= smoothLambda
		penalty[k1][k] -= smoothLambda
	}
	penalty[n][n] += 1e-6 + 1e-9*varZ
	penalty[n+1][n+1] += 1e-6 + 1e-9*varQ

	w := make([]float64, len(obs))
	for i := range w {
		w[i] = 1
	}
	var beta []float64
	var chol matrix
	resid := make([]float64, len(obs))
	sigma := 0.0
	for iter := 0; iter < irlsMaxIter; iter++ {
		a := newMatrix(p, p)
		rhs := make([]float64, p)
		for i := range obs {
			xi := x[i]
			wi := w[i]
			for r := 0; r < p; r++ {
				if xi[r] == 0 {
					continue
				}
				wr := wi * xi[r]
				rhs[r] += wr * y[i]
				for c := 0; c < p; c++ {
					a[r][c] += wr * xi[c]
				}
			}
		}
		for r := 0; r < p; r++ {
			for c := 0; c < p; c++ {
				a[r][c] += penalty[r][c]
			}
		}
		l, err := cholesky(a)
		if err != nil {
			return nil, err
		}
		next := cholSolve(l, rhs)
		chol = l
		maxDelta := math.Inf(1)
		if beta != nil {
			maxDelta = 0
			for i := range next {
				maxDelta = math.Max(maxDelta, math.Abs(next[i]-beta[i]))
			}
		}
		beta = next

		for i := range obs {
			s := 0.0
			for c := 0; c < p; c++ {
				s += x[i][c] * beta[c]
			}
			resid[i] = y[i] - s
		}
		sigma = mad(resid)
		if !(sigma > 1e-6) {
			sigma = 1e-6
		}
		for i := range w {
			u := math.Abs(resid[i]) / sigma
			if u <= huberK {
				w[i] = 1
			} else {
				w[i] = huberK / u
			}
		}
		if maxDelta < 1e-10 {
			break
		}
	}

	cov := cholInverse(chol)
	f := &directionalFit{
		n:     n,
		a:     beta[:n],
		se:    make([]float64, n),
		b:     beta[n],
		c:     beta[n+1],
		sigma: sigma,
		df:    n - 1,
	}
	for k := 0; k < n; k++ {
		f.se[k] = sigma * math.Sqrt(cov[k][k])
	}

	// Wald test of H0: a_0 = a_1 = … = a_{n-1} using neighbour differences.
	m := n - 1
	d := make([]float64, m)
	sc := newMatrix(m, m)
	for i := 0; i < m; i++ {
		d[i] = f.a[i] - f.a[i+1]
		for j := 0; j < m; j++ {
			// Cov(a_i - a_{i+1}, a_j - a_{j+1})
			sc[i][j] = sigma * sigma * (cov[i][j] - cov[i][j+1] - cov[i+1][j] + cov[i+1][j+1])
		}
	}
	if l, err := cholesky(sc); err == nil {
		sol := cholSolve(l, d)
		for i := range d {
			f.wald += d[i] * sol[i]
		}
		f.pValue = chi2Survival(f.wald, f.df)
	} else {
		f.wald, f.pValue = math.NaN(), math.NaN()
	}

	f.shrink()
	return f, nil
}

// shrink applies DerSimonian–Laird random-effects shrinkage: each anchor
// estimate is pulled towards the precision-weighted mean in proportion to
// its own noise. Without real directional differences (τ² = 0) every
// direction collapses onto the mean and the profile stays neutral.
func (f *directionalFit) shrink() {
	sw, sw2, swa := 0.0, 0.0, 0.0
	for k := 0; k < f.n; k++ {
		wk := 1 / (f.se[k] * f.se[k])
		sw += wk
		sw2 += wk * wk
		swa += wk * f.a[k]
	}
	f.center = swa / sw
	q := 0.0
	for k := 0; k < f.n; k++ {
		wk := 1 / (f.se[k] * f.se[k])
		q += wk * (f.a[k] - f.center) * (f.a[k] - f.center)
	}
	f.tau2 = math.Max(0, (q-float64(f.n-1))/(sw-sw2/sw))
	f.shrunk = make([]float64, f.n)
	for k := 0; k < f.n; k++ {
		bk := 0.0
		if f.tau2 > 0 {
			bk = f.tau2 / (f.tau2 + f.se[k]*f.se[k])
		}
		f.shrunk[k] = f.center + bk*(f.a[k]-f.center)
	}
}
