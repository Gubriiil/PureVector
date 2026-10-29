package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var g8 = []float64{0.9, 1, 1.05, 1, 1.1, 1, 0.95, 1}

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetLogger(t.Logf)
	return s
}

func TestDefaultsAndPersistence(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	snap := s.Snapshot()
	if !snap.Enabled || snap.DefaultName != NeutralName {
		t.Fatalf("unexpected defaults: %+v", snap)
	}
	if tab, name := snap.TableFor("VID_1234&PID_5678"); tab != nil || name != NeutralName {
		t.Fatalf("default should be neutral pass-through")
	}
	if err := s.SaveProfile(Profile{Name: "Mein Profil", Gains: g8, Source: &Source{Kind: "calibration", Measured: []float64{1, 2}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault("Mein Profil"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignDevice("VID_AAAA&PID_BBBB", "Touchpad", NeutralName); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}

	s2 := open(t, dir)
	snap = s2.Snapshot()
	if snap.Enabled {
		t.Error("enabled flag not persisted")
	}
	tab, name := snap.TableFor("VID_1111&PID_2222")
	if tab == nil || name != "Mein Profil" || tab.Gains()[0] != 0.9 {
		t.Errorf("default profile not persisted: %v %q", tab, name)
	}
	if tab, name := snap.TableFor("VID_AAAA&PID_BBBB"); tab != nil || name != NeutralName {
		t.Errorf("device assignment not persisted: %v %q", tab, name)
	}
	if got := s2.Config().DeviceNames["VID_AAAA&PID_BBBB"]; got != "Touchpad" {
		t.Errorf("device name %q", got)
	}
	ps := s2.Profiles()
	if len(ps) != 2 || ps[0].Name != NeutralName || ps[1].Source == nil || len(ps[1].Source.Measured) != 2 {
		t.Errorf("profiles: %+v", ps)
	}
}

func TestDeleteClearsAssignments(t *testing.T) {
	s := open(t, t.TempDir())
	if err := s.SaveProfile(Profile{Name: "a", Gains: g8}); err != nil {
		t.Fatal(err)
	}
	_ = s.SetDefault("a")
	_ = s.AssignDevice("dev", "", "a")
	if err := s.DeleteProfile("a"); err != nil {
		t.Fatal(err)
	}
	cfg := s.Config()
	if cfg.DefaultProfile != NeutralName || len(cfg.Devices) != 0 {
		t.Errorf("assignments survived delete: %+v", cfg)
	}
	if err := s.DeleteProfile(NeutralName); err == nil {
		t.Error("neutral profile deleted")
	}
	if err := s.AssignDevice("dev", "", "missing"); err == nil {
		t.Error("assigned unknown profile")
	}
}

func TestToggleAndSnapshotVersions(t *testing.T) {
	s := open(t, t.TempDir())
	v := s.Snapshot().Version
	on, err := s.Toggle()
	if err != nil || on {
		t.Fatalf("toggle: %v %v", on, err)
	}
	if s.Snapshot().Version <= v || s.Snapshot().Enabled {
		t.Error("snapshot not republished")
	}
}

func TestInvalidInputs(t *testing.T) {
	s := open(t, t.TempDir())
	for _, bad := range []string{"", "CON", "con.json", "a/b", `a\b`, "a:b", " x", "x.", strings.Repeat("x", 65), NeutralName} {
		if err := s.SaveProfile(Profile{Name: bad, Gains: g8}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	for _, good := range []string{"Profil 1", "Kalibrierung (2026-10-29 15.30)", "Größe_ä+ö"} {
		if err := ValidateName(good); err != nil {
			t.Errorf("name %q rejected: %v", good, err)
		}
	}
	if err := s.SaveProfile(Profile{Name: "x", Gains: []float64{1, 2}}); err == nil {
		t.Error("2 gains accepted")
	}
}

func TestCorruptFilesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if err := s.SaveProfile(Profile{Name: "ok", Gains: g8}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "profiles", "broken.json"), []byte("{nope"), 0o644)
	os.WriteFile(filepath.Join(dir, "profiles", "bad.json"), []byte(`{"name":"bad","gains":[9,9,9,9]}`), 0o644)
	s2 := open(t, dir)
	if got := len(s2.Profiles()); got != 2 {
		t.Errorf("expected neutral + ok, got %d profiles", got)
	}
}
