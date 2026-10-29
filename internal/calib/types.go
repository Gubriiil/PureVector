// Package calib turns a recorded aim session into a direction-dependent
// sensitivity profile.
//
// The pipeline mirrors the four public steps of Corsair's PureVector
// ("measure, calculate, adjust, save"), implemented from first principles:
//
//  1. Every target acquisition is split into its primary submovement (the
//     fast ballistic flick) and the corrective submovements that follow.
//  2. For each flick the primary endpoint is expressed as a ratio of the
//     target distance (1.10 = 10 % overshoot, 0.95 = 5 % undershoot).
//  3. A robust regression estimates the log ratio as a smooth function of
//     the movement direction, controlling for target distance and for
//     outward vs. return flicks.
//  4. Random-effects shrinkage pulls noisy directions towards the common
//     mean, so the profile only changes where the data really show a
//     directional bias.
//  5. The correction gain per direction makes every direction land like
//     the reference direction (by default the strongest one).
package calib

// Session is the payload uploaded by the calibration page.
type Session struct {
	Version  int        `json:"version"`
	ID       string     `json:"sessionId,omitempty"`
	Mode     string     `json:"mode,omitempty"` // "fresh" or "refine"
	Settings Settings   `json:"settings"`
	Client   ClientInfo `json:"client"`
	Trials   []Trial    `json:"trials"`
}

// Settings describes the task the user played.
type Settings struct {
	DPI          float64 `json:"dpi"`
	Cm360        float64 `json:"cm360"`
	FOV          float64 `json:"fov"`
	Rounds       int     `json:"rounds"`
	RoundSeconds float64 `json:"roundSeconds"`
	Directions   int     `json:"directions"`
	CountsPerDeg float64 `json:"countsPerDeg"`
	MinDeg       float64 `json:"minDeg"`
	MaxDeg       float64 `json:"maxDeg"`
	RadiusDeg    float64 `json:"radiusDeg"`
}

// ClientInfo describes how the browser delivered mouse input.
type ClientInfo struct {
	UserAgent  string  `json:"userAgent,omitempty"`
	Unadjusted bool    `json:"unadjusted"` // raw input without OS acceleration
	EventType  string  `json:"eventType,omitempty"`
	RateHz     float64 `json:"rateHz,omitempty"` // estimated input event rate
}

// Trial is one target: from its appearance until it was hit or timed out.
// All positions are in raw mouse counts (x right, y down).
type Trial struct {
	Kind   string  `json:"kind"` // "out" (centre → target) or "return" (target → centre)
	Round  int     `json:"round"`
	SpawnT float64 `json:"spawnT"` // page clock (ms) when the target appeared
	SX     float64 `json:"sx"`     // crosshair position when the target appeared
	SY     float64 `json:"sy"`
	TX     float64 `json:"tx"` // target centre
	TY     float64 `json:"ty"`
	R      float64 `json:"r"` // target radius
	Hit    bool    `json:"hit"`
	EndT   float64 `json:"endT"` // ms after spawn when the trial ended
	// Samples are [ms since spawn, x - SX, y - SY], at most one per ms.
	Samples [][3]float64 `json:"samples"`
	Clicks  []float64    `json:"clicks,omitempty"` // ms since spawn
}

// Kinds of trials.
const (
	KindOut    = "out"
	KindReturn = "return"
)
