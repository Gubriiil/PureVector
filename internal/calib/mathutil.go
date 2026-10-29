package calib

import (
	"errors"
	"math"
	"sort"
)

// ---- descriptive statistics -------------------------------------------------

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return 0.5 * (s[m-1] + s[m])
}

// mad is the median absolute deviation scaled to estimate a normal SD.
func mad(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	m := median(v)
	d := make([]float64, len(v))
	for i, x := range v {
		d[i] = math.Abs(x - m)
	}
	return 1.4826 * median(d)
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func stddev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	m := mean(v)
	s := 0.0
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(v)-1))
}

// wrapPi maps an angle to (-π, π].
func wrapPi(a float64) float64 {
	a = math.Mod(a+math.Pi, 2*math.Pi)
	if a <= 0 {
		a += 2 * math.Pi
	}
	return a - math.Pi
}

// wrap2Pi maps an angle to [0, 2π).
func wrap2Pi(a float64) float64 {
	a = math.Mod(a, 2*math.Pi)
	if a < 0 {
		a += 2 * math.Pi
	}
	if a >= 2*math.Pi {
		a = 0
	}
	return a
}

// ---- small dense linear algebra ---------------------------------------------

type matrix [][]float64

func newMatrix(r, c int) matrix {
	m := make(matrix, r)
	buf := make([]float64, r*c)
	for i := range m {
		m[i] = buf[i*c : (i+1)*c]
	}
	return m
}

// cholesky returns the lower-triangular L with A = L·Lᵀ.
func cholesky(a matrix) (matrix, error) {
	n := len(a)
	l := newMatrix(n, n)
	for i := 0; i < n; i++ {
		for j := 0; j <= i; j++ {
			s := a[i][j]
			for k := 0; k < j; k++ {
				s -= l[i][k] * l[j][k]
			}
			if i == j {
				if !(s > 0) {
					return nil, errors.New("calib: matrix not positive definite")
				}
				l[i][i] = math.Sqrt(s)
			} else {
				l[i][j] = s / l[j][j]
			}
		}
	}
	return l, nil
}

// cholSolve solves (L·Lᵀ)·x = b.
func cholSolve(l matrix, b []float64) []float64 {
	n := len(l)
	y := make([]float64, n)
	for i := 0; i < n; i++ {
		s := b[i]
		for k := 0; k < i; k++ {
			s -= l[i][k] * y[k]
		}
		y[i] = s / l[i][i]
	}
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		s := y[i]
		for k := i + 1; k < n; k++ {
			s -= l[k][i] * x[k]
		}
		x[i] = s / l[i][i]
	}
	return x
}

// cholInverse returns A⁻¹ from its Cholesky factor.
func cholInverse(l matrix) matrix {
	n := len(l)
	inv := newMatrix(n, n)
	e := make([]float64, n)
	for j := 0; j < n; j++ {
		for i := range e {
			e[i] = 0
		}
		e[j] = 1
		col := cholSolve(l, e)
		for i := 0; i < n; i++ {
			inv[i][j] = col[i]
		}
	}
	return inv
}

// ---- distributions ----------------------------------------------------------

// chi2Survival returns P(X > x) for X ~ χ²(df).
func chi2Survival(x float64, df int) float64 {
	if df <= 0 || math.IsNaN(x) {
		return math.NaN()
	}
	if x <= 0 {
		return 1
	}
	return gammaQ(float64(df)/2, x/2)
}

// gammaQ is the regularized upper incomplete gamma function Q(a, x).
func gammaQ(a, x float64) float64 {
	if x < a+1 {
		return 1 - gammaPSeries(a, x)
	}
	return gammaQContinuedFraction(a, x)
}

func gammaPSeries(a, x float64) float64 {
	lg, _ := math.Lgamma(a)
	sum := 1 / a
	del := sum
	ap := a
	for n := 0; n < 1000; n++ {
		ap++
		del *= x / ap
		sum += del
		if math.Abs(del) < math.Abs(sum)*1e-15 {
			break
		}
	}
	return sum * math.Exp(-x+a*math.Log(x)-lg)
}

func gammaQContinuedFraction(a, x float64) float64 {
	const tiny = 1e-300
	lg, _ := math.Lgamma(a)
	b := x + 1 - a
	c := 1 / tiny
	d := 1 / b
	h := d
	for i := 1; i < 1000; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = b + an/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < 1e-15 {
			break
		}
	}
	return math.Exp(-x+a*math.Log(x)-lg) * h
}
