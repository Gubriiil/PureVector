// Package profile persists direction profiles and the device assignment and
// publishes an immutable runtime snapshot for the input hot path.
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Gubriiil/PureVector/internal/vector"
)

// NeutralName is the built-in profile with gain 1 in every direction.
const NeutralName = "neutral"

// Profile is a named direction profile.
type Profile struct {
	Name      string    `json:"name"`
	Gains     []float64 `json:"gains"`
	CreatedAt time.Time `json:"createdAt"`
	Note      string    `json:"note,omitempty"`
	Source    *Source   `json:"source,omitempty"`
	BuiltIn   bool      `json:"builtIn,omitempty"`
}

// Source records where a profile came from.
type Source struct {
	Kind        string    `json:"kind"` // "calibration", "refine", "manual", "import"
	SessionFile string    `json:"sessionFile,omitempty"`
	Reference   string    `json:"reference,omitempty"`
	Strength    float64   `json:"strength,omitempty"`
	Parent      string    `json:"parent,omitempty"`
	Device      string    `json:"device,omitempty"`
	ValidTrials int       `json:"validTrials,omitempty"`
	PValue      float64   `json:"pValue,omitempty"`
	SpreadPct   float64   `json:"spreadPct,omitempty"`
	Measured    []float64 `json:"measured,omitempty"` // landing ratio per direction
}

// TrackerSettings mirror vector.TrackerConfig in JSON friendly units.
type TrackerSettings struct {
	MemoryCounts float64 `json:"memoryCounts"`
	IdleResetMs  float64 `json:"idleResetMs"`
}

// Config is the persistent application configuration.
type Config struct {
	Enabled        bool              `json:"enabled"`
	DefaultProfile string            `json:"defaultProfile"`
	Devices        map[string]string `json:"devices"`
	DeviceNames    map[string]string `json:"deviceNames,omitempty"`
	Listen         string            `json:"listen"`
	Hotkey         string            `json:"hotkey"`
	Tracker        TrackerSettings   `json:"tracker"`
}

// Defaults.
const (
	DefaultListen = "127.0.0.1:47823"
	DefaultHotkey = "ctrl+alt+f10"
)

func defaultConfig() Config {
	d := vector.DefaultTrackerConfig()
	return Config{
		Enabled:        true,
		DefaultProfile: NeutralName,
		Devices:        map[string]string{},
		DeviceNames:    map[string]string{},
		Listen:         DefaultListen,
		Hotkey:         DefaultHotkey,
		Tracker: TrackerSettings{
			MemoryCounts: d.MemoryCounts,
			IdleResetMs:  float64(d.IdleReset) / float64(time.Millisecond),
		},
	}
}

// TrackerConfig converts the settings for the vector package.
func (c Config) TrackerConfig() vector.TrackerConfig {
	return vector.TrackerConfig{
		MemoryCounts: c.Tracker.MemoryCounts,
		IdleReset:    time.Duration(c.Tracker.IdleResetMs * float64(time.Millisecond)),
	}
}

// Snapshot is the immutable view used by the input hot path.
type Snapshot struct {
	Version     uint64
	Enabled     bool
	DefaultName string
	Tracker     vector.TrackerConfig
	def         *vector.GainTable
	devices     map[string]string
	tables      map[string]*vector.GainTable
}

// TableFor returns the table and profile name for a device. A nil table
// means the device is passed through untouched.
func (s *Snapshot) TableFor(deviceKey string) (*vector.GainTable, string) {
	if name, ok := s.devices[deviceKey]; ok {
		return s.tables[name], name
	}
	return s.def, s.DefaultName
}

// ProfileFor returns only the profile name that applies to a device.
func (s *Snapshot) ProfileFor(deviceKey string) string {
	_, n := s.TableFor(deviceKey)
	return n
}

// Store manages config.json and profiles/*.json in one directory.
type Store struct {
	dir      string
	mu       sync.Mutex
	cfg      Config
	profiles map[string]*Profile
	tables   map[string]*vector.GainTable
	version  uint64
	snap     atomic.Pointer[Snapshot]
	logf     func(format string, args ...any)
}

// DefaultDir is the per-user configuration directory.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "OmniVector"), nil
}

// Open loads (or initialises) the store in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, cfg: defaultConfig(), profiles: map[string]*Profile{}, tables: map[string]*vector.GainTable{},
		logf: log.Printf}
	if b, err := os.ReadFile(s.configPath()); err == nil {
		cfg := defaultConfig()
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("profile: %s: %w", s.configPath(), err)
		}
		if cfg.Devices == nil {
			cfg.Devices = map[string]string{}
		}
		if cfg.DeviceNames == nil {
			cfg.DeviceNames = map[string]string{}
		}
		if cfg.Listen == "" {
			cfg.Listen = DefaultListen
		}
		s.cfg = cfg
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	files, _ := filepath.Glob(filepath.Join(dir, "profiles", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			s.logf("Profil %s übersprungen: %v", f, err)
			continue
		}
		var p Profile
		if err := json.Unmarshal(b, &p); err != nil {
			s.logf("Profil %s übersprungen: %v", f, err)
			continue
		}
		if err := ValidateName(p.Name); err != nil || p.Name == NeutralName {
			s.logf("Profil %s übersprungen: ungültiger Name %q", f, p.Name)
			continue
		}
		t, err := vector.NewGainTable(p.Gains)
		if err != nil {
			s.logf("Profil %s übersprungen: %v", f, err)
			continue
		}
		pp := p
		s.profiles[p.Name] = &pp
		s.tables[p.Name] = t
	}
	// Drop assignments that point to missing profiles.
	if _, ok := s.lookup(s.cfg.DefaultProfile); !ok {
		s.cfg.DefaultProfile = NeutralName
	}
	for dev, name := range s.cfg.Devices {
		if _, ok := s.lookup(name); !ok {
			delete(s.cfg.Devices, dev)
		}
	}
	s.publish()
	return s, nil
}

// SetLogger replaces the logger (used in tests).
func (s *Store) SetLogger(f func(string, ...any)) { s.logf = f }

// Dir is the store directory.
func (s *Store) Dir() string { return s.dir }

// SessionsDir holds raw calibration recordings.
func (s *Store) SessionsDir() string { return filepath.Join(s.dir, "sessions") }

func (s *Store) configPath() string { return filepath.Join(s.dir, "config.json") }

func (s *Store) profilePath(name string) string {
	return filepath.Join(s.dir, "profiles", name+".json")
}

// Snapshot returns the current runtime snapshot (never nil).
func (s *Store) Snapshot() *Snapshot { return s.snap.Load() }

// Config returns a copy of the configuration.
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyConfig()
}

func (s *Store) copyConfig() Config {
	c := s.cfg
	c.Devices = make(map[string]string, len(s.cfg.Devices))
	for k, v := range s.cfg.Devices {
		c.Devices[k] = v
	}
	c.DeviceNames = make(map[string]string, len(s.cfg.DeviceNames))
	for k, v := range s.cfg.DeviceNames {
		c.DeviceNames[k] = v
	}
	return c
}

// Profiles lists all profiles, the built-in neutral profile first, then
// newest first.
func (s *Store) Profiles() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Profile, 0, len(s.profiles)+1)
	for _, p := range s.profiles {
		out = append(out, clone(*p))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].Name < out[j].Name
	})
	return append([]Profile{neutralProfile()}, out...)
}

// Profile returns a profile by name (including the built-in neutral one).
func (s *Store) Profile(name string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.lookup(name)
	if !ok {
		return Profile{}, false
	}
	return clone(*p), true
}

// Table returns the gain table of a profile (nil for neutral).
func (s *Store) Table(name string) (*vector.GainTable, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == NeutralName {
		return nil, true
	}
	t, ok := s.tables[name]
	return t, ok
}

func (s *Store) lookup(name string) (*Profile, bool) {
	if name == NeutralName {
		p := neutralProfile()
		return &p, true
	}
	p, ok := s.profiles[name]
	return p, ok
}

// SaveProfile creates or replaces a profile.
func (s *Store) SaveProfile(p Profile) error {
	if err := ValidateName(p.Name); err != nil {
		return err
	}
	if p.Name == NeutralName {
		return errors.New("der Name „neutral“ ist reserviert")
	}
	t, err := vector.NewGainTable(p.Gains)
	if err != nil {
		return err
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	p.BuiltIn = false
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFileAtomic(s.profilePath(p.Name), b); err != nil {
		return err
	}
	pp := clone(p)
	s.profiles[p.Name] = &pp
	s.tables[p.Name] = t
	s.publish()
	return nil
}

// DeleteProfile removes a profile and every assignment that used it.
func (s *Store) DeleteProfile(name string) error {
	if name == NeutralName {
		return errors.New("das neutrale Profil kann nicht gelöscht werden")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[name]; !ok {
		return fmt.Errorf("Profil %q nicht gefunden", name)
	}
	if err := os.Remove(s.profilePath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	delete(s.profiles, name)
	delete(s.tables, name)
	if s.cfg.DefaultProfile == name {
		s.cfg.DefaultProfile = NeutralName
	}
	for dev, p := range s.cfg.Devices {
		if p == name {
			delete(s.cfg.Devices, dev)
		}
	}
	if err := s.saveConfigLocked(); err != nil {
		return err
	}
	s.publish()
	return nil
}

// SetEnabled switches the transform on or off globally.
func (s *Store) SetEnabled(on bool) error {
	return s.update(func(c *Config) error { c.Enabled = on; return nil })
}

// Toggle flips the global switch and returns the new state.
func (s *Store) Toggle() (bool, error) {
	var now bool
	err := s.update(func(c *Config) error { c.Enabled = !c.Enabled; now = c.Enabled; return nil })
	return now, err
}

// SetDefault selects the profile for all devices without an assignment.
func (s *Store) SetDefault(name string) error {
	return s.update(func(c *Config) error {
		if _, ok := s.lookup(name); !ok {
			return fmt.Errorf("Profil %q nicht gefunden", name)
		}
		c.DefaultProfile = name
		return nil
	})
}

// AssignDevice pins a profile to a device. An empty name removes the
// assignment so the default profile applies again.
func (s *Store) AssignDevice(deviceKey, displayName, name string) error {
	if strings.TrimSpace(deviceKey) == "" {
		return errors.New("leere Geräte-ID")
	}
	return s.update(func(c *Config) error {
		if name == "" {
			delete(c.Devices, deviceKey)
			return nil
		}
		if _, ok := s.lookup(name); !ok {
			return fmt.Errorf("Profil %q nicht gefunden", name)
		}
		c.Devices[deviceKey] = name
		if displayName != "" {
			c.DeviceNames[deviceKey] = displayName
		}
		return nil
	})
}

// SetTracker changes the direction tracker settings.
func (s *Store) SetTracker(t TrackerSettings) error {
	return s.update(func(c *Config) error { c.Tracker = t; return nil })
}

func (s *Store) update(f func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.copyConfig()
	if err := f(&next); err != nil {
		return err
	}
	prev := s.cfg
	s.cfg = next
	if err := s.saveConfigLocked(); err != nil {
		s.cfg = prev
		return err
	}
	s.publish()
	return nil
}

func (s *Store) saveConfigLocked() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.configPath(), b)
}

// publish builds a new snapshot; s.mu must be held (or not yet shared).
func (s *Store) publish() {
	s.version++
	snap := &Snapshot{
		Version:     s.version,
		Enabled:     s.cfg.Enabled,
		DefaultName: s.cfg.DefaultProfile,
		Tracker:     s.cfg.TrackerConfig(),
		devices:     make(map[string]string, len(s.cfg.Devices)),
		tables:      make(map[string]*vector.GainTable, len(s.tables)),
	}
	for k, v := range s.cfg.Devices {
		snap.devices[k] = v
	}
	for k, v := range s.tables {
		snap.tables[k] = v
	}
	snap.def = s.tables[s.cfg.DefaultProfile] // nil for neutral
	s.snap.Store(snap)
}

func neutralProfile() Profile {
	g := make([]float64, 8)
	for i := range g {
		g[i] = 1
	}
	return Profile{Name: NeutralName, Gains: g, BuiltIn: true, Note: "Keine Veränderung (Originalverhalten der Maus)"}
}

func clone(p Profile) Profile {
	p.Gains = append([]float64(nil), p.Gains...)
	if p.Source != nil {
		src := *p.Source
		src.Measured = append([]float64(nil), src.Measured...)
		p.Source = &src
	}
	return p
}

var reservedWindowsNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateName checks that a profile name is safe to use as a file name on
// every platform.
func ValidateName(name string) error {
	if name == "" || len([]rune(name)) > 64 {
		return errors.New("Profilname muss 1–64 Zeichen lang sein")
	}
	if strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") || strings.HasPrefix(name, ".") {
		return errors.New("Profilname darf nicht mit Leerzeichen oder Punkt beginnen/enden")
	}
	for _, r := range name {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" -_.()+", r)
		if !ok {
			return fmt.Errorf("Zeichen %q ist im Profilnamen nicht erlaubt", r)
		}
	}
	base := strings.ToLower(strings.SplitN(name, ".", 2)[0])
	if reservedWindowsNames[base] {
		return fmt.Errorf("%q ist unter Windows ein reservierter Name", name)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
