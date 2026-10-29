package calib

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/Gubriiil/PureVector/internal/vector"
)

// Reference modes decide which landing behaviour all directions are
// equalised to.
const (
	// RefStrongest equalises every direction to the direction whose primary
	// flicks land closest to the target ("weaker directions behave like the
	// strongest one" – the PureVector behaviour).
	RefStrongest = "strongest"
	// RefMean only removes the directional differences and keeps the
	// overall sensitivity (geometric mean of all gains = 1).
	RefMean = "mean"
	// RefAbsolute corrects every direction to land exactly on target. This
	// also changes the overall sensitivity and usually over-corrects, since
	// humans deliberately undershoot slightly; for experiments only.
	RefAbsolute = "absolute"
)

// Options controls the analysis.
type Options struct {
	Directions    int     `json:"directions"` // anchors, default: session setting or 8
	Reference     string  `json:"reference"`  // default RefStrongest
	Strength      float64 `json:"strength"`   // 0..1, default 1
	MinGain       float64 `json:"minGain"`    // default 0.80
	MaxGain       float64 `json:"maxGain"`    // default 1.25
	Basis         string  `json:"basis"`      // default BasisHat
	ExcludeReturn bool    `json:"excludeReturn"`
}

// Defaults for Options.
const (
	DefaultDirections = 8
	DefaultMinGain    = 0.80
	DefaultMaxGain    = 1.25
	// MinValidTrials is required before any profile is proposed.
	MinValidTrials = 40
	// GoodValidTrials is the amount of data for a reliable profile.
	GoodValidTrials = 150
)

func (o Options) withDefaults(s *Session) Options {
	if o.Directions == 0 {
		o.Directions = s.Settings.Directions
	}
	if o.Directions < vector.MinDirections || o.Directions > vector.MaxDirections {
		o.Directions = DefaultDirections
	}
	if o.Reference == "" {
		o.Reference = RefStrongest
	}
	if !(o.Strength > 0 && o.Strength <= 1) {
		o.Strength = 1
	}
	if !(o.MinGain >= vector.MinGain && o.MinGain <= 1) {
		o.MinGain = DefaultMinGain
	}
	if !(o.MaxGain >= 1 && o.MaxGain <= vector.MaxGain) {
		o.MaxGain = DefaultMaxGain
	}
	if o.Basis != BasisSector {
		o.Basis = BasisHat
	}
	return o
}

// Direction summarises one anchor direction.
type Direction struct {
	Index    int     `json:"index"`
	AngleDeg float64 `json:"angleDeg"`
	Name     string  `json:"name"`
	// N is the number of valid trials nearest to this direction.
	N int `json:"n"`
	// Ratio is the model estimate of the primary landing ratio (1 = on
	// target, 1.05 = 5 % overshoot) with its 95 % confidence interval.
	Ratio     float64 `json:"ratio"`
	RatioLow  float64 `json:"ratioLow"`
	RatioHigh float64 `json:"ratioHigh"`
	// LogErr is the shrunken log ratio used for the correction.
	LogErr float64 `json:"logErr"`
	// Shrunk = exp(LogErr).
	Shrunk float64 `json:"shrunk"`
	SE     float64 `json:"se"`
	// Raw per-sector descriptives.
	MedianRatio  float64 `json:"medianRatio"`
	AngErrDeg    float64 `json:"angErrDeg"`
	MedianTimeMs float64 `json:"medianTimeMs"`
	Corrections  float64 `json:"corrections"`
	HitRate      float64 `json:"hitRate"`
}

// Model holds global model parameters and test results.
type Model struct {
	Sigma         float64 `json:"sigma"`         // robust residual SD (log ratio)
	DistanceSlope float64 `json:"distanceSlope"` // d log(ratio) / d log(distance)
	ReturnOffset  float64 `json:"returnOffset"`  // log ratio difference return vs. out
	Tau           float64 `json:"tau"`           // SD of true directional effects
	Wald          float64 `json:"wald"`
	DF            int     `json:"df"`
	PValue        float64 `json:"pValue"` // H0: no directional differences; -1 = unknown
	SpreadPct     float64 `json:"spreadPct"`
	RangePct      float64 `json:"rangePct"`
	MeanRatio     float64 `json:"meanRatio"`
	MeanAngErrDeg float64 `json:"meanAngErrDeg"`
	Basis         string  `json:"basis"`
}

// Counts summarises trial usage.
type Counts struct {
	Total    int            `json:"total"`
	Valid    int            `json:"valid"`
	Out      int            `json:"out"`
	Return   int            `json:"return"`
	Rejected map[string]int `json:"rejected"`
}

// Suggestion is a concrete correction profile.
type Suggestion struct {
	Reference        string    `json:"reference"`
	ReferenceIndex   int       `json:"referenceIndex"` // -1 unless RefStrongest
	ReferenceLogErr  float64   `json:"referenceLogErr"`
	Strength         float64   `json:"strength"`
	Gains            []float64 `json:"gains"`
	Clamped          []bool    `json:"clamped"`
	PredictedRatio   []float64 `json:"predictedRatio"` // landing ratio after correction
	OverallChangePct float64   `json:"overallChangePct"`
}

// Result is the complete analysis of a session.
type Result struct {
	Directions   int            `json:"directions"`
	Counts       Counts         `json:"counts"`
	PerDirection []Direction    `json:"perDirection"`
	Model        Model          `json:"model"`
	Suggested    Suggestion     `json:"suggested"`
	Warnings     []string       `json:"warnings"`
	Quality      string         `json:"quality"` // "good", "fair", "poor"
	Options      Options        `json:"options"`
	Trials       []TrialMetrics `json:"-"`
}

// ErrNotEnoughData is returned when too few usable trials were recorded.
var ErrNotEnoughData = errors.New("calib: not enough valid trials")

// Analyze runs the full calibration analysis.
func Analyze(s *Session, opt Options) (*Result, error) {
	if s == nil {
		return nil, errors.New("calib: nil session")
	}
	opt = opt.withDefaults(s)
	n := opt.Directions
	res := &Result{
		Directions: n,
		Options:    opt,
		Counts:     Counts{Total: len(s.Trials), Rejected: map[string]int{}},
		Warnings:   []string{},
	}

	metrics := make([]TrialMetrics, len(s.Trials))
	var valid []TrialMetrics
	for i := range s.Trials {
		m := AnalyzeTrial(i, &s.Trials[i])
		metrics[i] = m
		if m.Valid && opt.ExcludeReturn && m.Kind == KindReturn {
			m.Valid, m.Reject = false, "excluded_return"
			metrics[i] = m
		}
		if !m.Valid {
			res.Counts.Rejected[m.Reject]++
			continue
		}
		valid = append(valid, m)
		if m.Kind == KindReturn {
			res.Counts.Return++
		} else {
			res.Counts.Out++
		}
	}
	res.Trials = metrics
	res.Counts.Valid = len(valid)
	if len(valid) < MinValidTrials || len(valid) < n+8 {
		return res, fmt.Errorf("%w: %d valid of %d (need at least %d)", ErrNotEnoughData, len(valid), len(s.Trials), MinValidTrials)
	}

	// Build observations with centred covariates.
	meanZ, meanQ := 0.0, 0.0
	for _, m := range valid {
		meanZ += math.Log(m.Distance)
		if m.Kind == KindReturn {
			meanQ++
		}
	}
	meanZ /= float64(len(valid))
	meanQ /= float64(len(valid))
	obs := make([]observation, len(valid))
	for i, m := range valid {
		q := 0.0
		if m.Kind == KindReturn {
			q = 1
		}
		obs[i] = observation{theta: m.Theta, y: math.Log(m.Ratio), z: math.Log(m.Distance) - meanZ, q: q - meanQ}
	}

	fit, err := fitDirectional(obs, n, opt.Basis)
	if err != nil {
		return res, err
	}

	res.Model = Model{
		Sigma:         fit.sigma,
		DistanceSlope: fit.b,
		ReturnOffset:  fit.c,
		Tau:           math.Sqrt(fit.tau2),
		Wald:          fit.wald,
		DF:            fit.df,
		PValue:        fit.pValue,
		Basis:         opt.Basis,
	}
	if math.IsNaN(fit.wald) || math.IsNaN(fit.pValue) {
		res.Model.Wald, res.Model.PValue = 0, -1 // test not computable
	}

	// Per-direction summaries.
	sectors := make([][]TrialMetrics, n)
	angErrs := make([]float64, 0, len(valid))
	for _, m := range valid {
		k := nearestAnchor(m.Theta, n)
		sectors[k] = append(sectors[k], m)
		angErrs = append(angErrs, m.AngErr)
	}
	res.Model.MeanAngErrDeg = mean(angErrs) * 180 / math.Pi
	shrunkRatios := make([]float64, n)
	logSum := 0.0
	for k := 0; k < n; k++ {
		d := Direction{
			Index:     k,
			AngleDeg:  vector.AnchorAngle(k, n) * 180 / math.Pi,
			Name:      vector.DirectionName(k, n),
			N:         len(sectors[k]),
			Ratio:     math.Exp(fit.a[k]),
			RatioLow:  math.Exp(fit.a[k] - 1.96*fit.se[k]),
			RatioHigh: math.Exp(fit.a[k] + 1.96*fit.se[k]),
			LogErr:    fit.shrunk[k],
			Shrunk:    math.Exp(fit.shrunk[k]),
			SE:        fit.se[k],
		}
		d.MedianRatio, d.AngErrDeg, d.MedianTimeMs, d.Corrections, d.HitRate = sectorStats(sectors[k])
		res.PerDirection = append(res.PerDirection, d)
		shrunkRatios[k] = d.Shrunk
		logSum += fit.shrunk[k]
	}
	res.Model.SpreadPct = 100 * stddev(shrunkRatios)
	lo, hi := minMax(shrunkRatios)
	res.Model.RangePct = 100 * (hi - lo)
	res.Model.MeanRatio = math.Exp(logSum / float64(n))

	res.Suggested = res.Suggest(opt.Reference, opt.Strength, opt.MinGain, opt.MaxGain)
	res.addWarnings(s)
	return res, nil
}

// Suggest computes the correction gains for a reference mode and strength.
// It is cheap and can be called repeatedly (e.g. for a UI preview).
func (r *Result) Suggest(reference string, strength, minGain, maxGain float64) Suggestion {
	n := len(r.PerDirection)
	if !(strength > 0 && strength <= 1) {
		strength = 1
	}
	if !(minGain >= vector.MinGain && minGain <= 1) {
		minGain = DefaultMinGain
	}
	if !(maxGain >= 1 && maxGain <= vector.MaxGain) {
		maxGain = DefaultMaxGain
	}
	s := Suggestion{Reference: reference, ReferenceIndex: -1, Strength: strength,
		Gains: make([]float64, n), Clamped: make([]bool, n), PredictedRatio: make([]float64, n)}
	switch reference {
	case RefMean:
		sum := 0.0
		for _, d := range r.PerDirection {
			sum += d.LogErr
		}
		s.ReferenceLogErr = sum / float64(n)
	case RefAbsolute:
		s.ReferenceLogErr = 0
	default:
		s.Reference = RefStrongest
		best := 0
		for k, d := range r.PerDirection {
			if math.Abs(d.LogErr) < math.Abs(r.PerDirection[best].LogErr) {
				best = k
			}
		}
		s.ReferenceIndex = best
		s.ReferenceLogErr = r.PerDirection[best].LogErr
	}
	logSum := 0.0
	for k, d := range r.PerDirection {
		g := math.Exp(-strength * (d.LogErr - s.ReferenceLogErr))
		if g < minGain {
			g, s.Clamped[k] = minGain, true
		} else if g > maxGain {
			g, s.Clamped[k] = maxGain, true
		}
		s.Gains[k] = g
		s.PredictedRatio[k] = math.Exp(d.LogErr) * g
		logSum += math.Log(g)
	}
	s.OverallChangePct = 100 * (math.Exp(logSum/float64(n)) - 1)
	return s
}

func (r *Result) addWarnings(s *Session) {
	if r.Counts.Valid < GoodValidTrials {
		r.Warnings = append(r.Warnings, fmt.Sprintf("Nur %d gültige Schüsse – für ein zuverlässiges Profil sind %d+ empfohlen (mehr Runden spielen).", r.Counts.Valid, GoodValidTrials))
	}
	if !s.Client.Unadjusted {
		r.Warnings = append(r.Warnings, "Der Browser hat keine Rohdaten geliefert (unadjustedMovement). Windows-/Desktop-Mausbeschleunigung kann die Messung verfälschen – bitte deaktivieren oder Chrome/Edge verwenden.")
	}
	if s.Client.RateHz > 0 && s.Client.RateHz < 200 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("Niedrige Eingaberate (%.0f Hz). Die Analyse funktioniert, ist aber genauer mit Chrome/Edge (pointerrawupdate).", s.Client.RateHz))
	}
	maxSE := 0.0
	for _, d := range r.PerDirection {
		maxSE = math.Max(maxSE, d.SE)
		if d.N < 8 {
			r.Warnings = append(r.Warnings, fmt.Sprintf("Richtung %s hat nur %d gültige Schüsse.", d.Name, d.N))
		}
	}
	for k, c := range r.Suggested.Clamped {
		if c {
			r.Warnings = append(r.Warnings, fmt.Sprintf("Korrektur für %s wurde auf die Sicherheitsgrenze begrenzt – ungewöhnlich große Abweichung (Mausbeschleunigung aktiv?).", r.PerDirection[k].Name))
		}
	}
	if r.Model.PValue > 0.2 {
		r.Warnings = append(r.Warnings, "Keine statistisch belastbaren Richtungsunterschiede gefunden – das Profil bleibt nahezu neutral.")
	}
	switch {
	case r.Counts.Valid >= GoodValidTrials && maxSE < 0.02:
		r.Quality = "good"
	case r.Counts.Valid >= MinValidTrials*2 && maxSE < 0.035:
		r.Quality = "fair"
	default:
		r.Quality = "poor"
	}
}

// sectorStats returns descriptive values for the trials nearest to one
// direction. Values that cannot be computed are 0 (check N and HitRate).
func sectorStats(ms []TrialMetrics) (medRatio, angErrDeg, medTime, corrections, hitRate float64) {
	if len(ms) == 0 {
		return 0, 0, 0, 0, 0
	}
	ratios := make([]float64, 0, len(ms))
	angs := make([]float64, 0, len(ms))
	times := make([]float64, 0, len(ms))
	corr, hits := 0, 0
	for _, m := range ms {
		ratios = append(ratios, m.Ratio)
		angs = append(angs, m.AngErr)
		if m.Hit {
			times = append(times, m.TimeToHitMs)
			hits++
		}
		corr += m.Corrections
	}
	if len(times) > 0 {
		medTime = median(times)
	}
	return median(ratios), mean(angs) * 180 / math.Pi, medTime, float64(corr) / float64(len(ms)), float64(hits) / float64(len(ms))
}

func nearestAnchor(theta float64, n int) int {
	return int(math.Floor(wrap2Pi(theta)/(2*math.Pi/float64(n))+0.5)) % n
}

func minMax(v []float64) (float64, float64) {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[0], s[len(s)-1]
}
