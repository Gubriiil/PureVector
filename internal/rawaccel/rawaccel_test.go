package rawaccel

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gubriiil/PureVector/internal/vector"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestFitRecoversRepresentableProfile(t *testing.T) {
	want := Axis{Right: 0.95, Left: 1.05, Up: 1.02, Down: 0.98}
	gains := make([]float64, 8)
	for k := range gains {
		gains[k] = ModelGain(want, vector.AnchorAngle(k, 8))
	}
	f, err := FitProfile(gains)
	if err != nil {
		t.Fatal(err)
	}
	if !near(f.Axis.Right, want.Right, 1e-6) || !near(f.Axis.Left, want.Left, 1e-6) ||
		!near(f.Axis.Up, want.Up, 1e-6) || !near(f.Axis.Down, want.Down, 1e-6) {
		t.Errorf("axis %+v want %+v", f.Axis, want)
	}
	if f.CoveragePct < 99.99 {
		t.Errorf("coverage %.3f", f.CoveragePct)
	}
	// The Raw Accel multipliers reproduce the four axis gains.
	x := f.Factors
	if !near(x.Sens, 0.95, 1e-6) || !near(x.Sens*x.LR, 1.05, 1e-6) || !near(x.Sens*x.YX, 0.98, 1e-6) || !near(x.Sens*x.YX*x.UD, 1.02, 1e-6) {
		t.Errorf("factors %+v", x)
	}
}

func TestFitDiagonalProfileIsApproximated(t *testing.T) {
	gains := []float64{1, 0.9, 1, 1, 1, 1, 1, 1} // only up-right reduced
	f, err := FitProfile(gains)
	if err != nil {
		t.Fatal(err)
	}
	if f.CoveragePct <= 0 || f.CoveragePct >= 99 {
		t.Errorf("coverage %.1f should be partial", f.CoveragePct)
	}
	if f.Model[1] >= 1 {
		t.Errorf("model should reduce the up-right gain, got %.3f", f.Model[1])
	}
	neutral, _ := FitProfile([]float64{1, 1, 1, 1, 1, 1, 1, 1})
	if neutral.Factors != (Factors{1, 1, 1, 1}) || neutral.CoveragePct != 100 {
		t.Errorf("neutral: %+v", neutral)
	}
}

const sample17 = `{
  "### Accel Modes ###": "classic | jump | natural | synchronous | power | lut | noaccel",
  "version": "1.7.0",
  "profiles": [
    {
      "name": "default",
      "Whole/combined accel (set false for 'by component' mode)": true,
      "lpNorm": 2.0,
      "Stretches domain for horizontal vs vertical inputs": {"x": 1.0, "y": 1.0},
      "Output DPI": 1600.0,
      "Y/X output DPI ratio (vertical sens multiplier)": 1.0,
      "L/R output DPI ratio (left sens multiplier)": 1.0,
      "U/D output DPI ratio (up sens multiplier)": 1.0,
      "Degrees of rotation": 0.0
    },
    {
      "name": "second",
      "Output DPI": 800,
      "Y/X output DPI ratio (vertical sens multiplier)": 1.1,
      "L/R output DPI ratio (left sens multiplier)": 1,
      "U/D output DPI ratio (up sens multiplier)": 1
    }
  ],
  "devices": []
}`

func TestPatchKeepsEverythingElse(t *testing.T) {
	f := Factors{Sens: 0.9, YX: 1.1, LR: 1.05, UD: 0.95}
	out, res, err := Patch([]byte(sample17), f, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 4 || len(res.Missing) != 0 {
		t.Fatalf("changes %+v missing %v", res.Changes, res.Missing)
	}
	var doc struct {
		Version  string           `json:"version"`
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	p := doc.Profiles[0]
	checks := map[string]float64{
		"Output DPI": 1440,
		"Y/X output DPI ratio (vertical sens multiplier)": 1.1,
		"L/R output DPI ratio (left sens multiplier)":     1.05,
		"U/D output DPI ratio (up sens multiplier)":       0.95,
		"Degrees of rotation":                             0,
		"lpNorm":                                          2,
	}
	for k, want := range checks {
		if got, _ := p[k].(float64); !near(got, want, 1e-9) {
			t.Errorf("%s = %v want %v", k, p[k], want)
		}
	}
	if doc.Profiles[1]["Output DPI"].(float64) != 800 {
		t.Error("other profile was modified")
	}
	// Key order preserved.
	s := string(out)
	if strings.Index(s, `"### Accel Modes ###"`) > strings.Index(s, `"version"`) ||
		strings.Index(s, `"Output DPI"`) > strings.Index(s, `"Y/X output`) {
		t.Errorf("key order changed:\n%s", s)
	}
}

func TestPatchOldFormatAndMissingFields(t *testing.T) {
	old := `{"profiles":[{"name":"default","Sensitivity multiplier":2.0,"Y/X sensitivity ratio (vertical sens multiplier)":1.0}]}`
	_, res, err := Patch([]byte(old), Factors{Sens: 1.5, YX: 2, LR: 1, UD: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 2 || res.Changes[0].New != 3 || len(res.Missing) != 2 {
		t.Errorf("result %+v", res)
	}
	if _, _, err := Patch([]byte(`{"x":1}`), Factors{1, 1, 1, 1}, ""); err != ErrNoProfiles {
		t.Errorf("err %v", err)
	}
	if _, _, err := Patch([]byte(sample17), Factors{1, 1, 1, 1}, "nope"); err == nil {
		t.Error("unknown profile accepted")
	}
}

func TestPatchFileWithHistoryDoesNotCompound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(sample17), 0o644); err != nil {
		t.Fatal(err)
	}
	f := Factors{Sens: 0.9, YX: 1, LR: 1, UD: 1}
	res, err := PatchFile(path, f, "default", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Backup); err != nil {
		t.Fatalf("no backup: %v", err)
	}
	// Exporting the same profile again with history must not compound.
	if _, err := PatchFile(path, f, "default", &f, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var doc struct {
		Profiles []map[string]any `json:"profiles"`
	}
	json.Unmarshal(b, &doc)
	if got := doc.Profiles[0]["Output DPI"].(float64); !near(got, 1440, 1e-9) {
		t.Errorf("Output DPI %v want 1440", got)
	}
	// Dry run leaves the file untouched.
	before, _ := os.ReadFile(path)
	if _, err := PatchFile(path, Factors{Sens: 2, YX: 1, LR: 1, UD: 1}, "", nil, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("dry run modified the file")
	}

	h := History{path: {"default": f}}
	hp := filepath.Join(dir, "hist.json")
	if err := h.Save(hp); err != nil {
		t.Fatal(err)
	}
	if got := LoadHistory(hp)[path]["default"]; got != f {
		t.Errorf("history round trip: %+v", got)
	}
}
