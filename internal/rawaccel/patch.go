package rawaccel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Patching edits an existing Raw Accel settings.json in place. Only the
// four directional values are multiplied; everything else – including key
// order and unknown fields – is preserved. Keys are matched by prefix so
// both the older ("Sensitivity multiplier", "Y/X sensitivity ratio …") and
// the newer ("Output DPI", "Y/X output DPI ratio …") formats work.

// Change records one modified value.
type Change struct {
	Profile string  `json:"profile"`
	Key     string  `json:"key"`
	Old     float64 `json:"old"`
	New     float64 `json:"new"`
}

// PatchResult describes what Patch did.
type PatchResult struct {
	Changes []Change `json:"changes"`
	Backup  string   `json:"backup,omitempty"`
	Missing []string `json:"missing,omitempty"` // fields this Raw Accel version lacks
}

// ErrNoProfiles means the file does not look like Raw Accel settings.
var ErrNoProfiles = errors.New("keine Raw-Accel-Profile in der Datei gefunden (\"profiles\" fehlt)")

type field struct {
	prefixes []string
	factor   func(Factors) float64
	label    string
}

var fields = []field{
	{[]string{"output dpi", "sensitivity multiplier", "sens multiplier"}, func(f Factors) float64 { return f.Sens }, "Sensitivity/Output DPI"},
	{[]string{"y/x"}, func(f Factors) float64 { return f.YX }, "Y/X ratio"},
	{[]string{"l/r"}, func(f Factors) float64 { return f.LR }, "L/R ratio"},
	{[]string{"u/d"}, func(f Factors) float64 { return f.UD }, "U/D ratio"},
}

// Divide returns f / prev component-wise (used to undo a previous export).
func (f Factors) Divide(prev Factors) Factors {
	d := func(a, b float64) float64 {
		if b == 0 {
			return a
		}
		return a / b
	}
	return Factors{Sens: d(f.Sens, prev.Sens), YX: d(f.YX, prev.YX), LR: d(f.LR, prev.LR), UD: d(f.UD, prev.UD)}
}

// Patch multiplies the directional values of the Raw Accel profiles in data
// by f. If profileName is not empty only that profile is changed.
func Patch(data []byte, f Factors, profileName string) ([]byte, PatchResult, error) {
	var res PatchResult
	root, err := parseOrdered(data)
	if err != nil {
		return nil, res, fmt.Errorf("settings.json ist kein gültiges JSON: %w", err)
	}
	profiles := root.get("profiles")
	if root.kind != 'o' || profiles == nil || profiles.kind != 'a' {
		return nil, res, ErrNoProfiles
	}
	matched := 0
	missing := map[string]bool{}
	for _, p := range profiles.vals {
		if p.kind != 'o' {
			continue
		}
		name := ""
		if n := p.get("name"); n != nil && n.kind == 's' {
			name = n.str
		}
		if profileName != "" && name != profileName {
			continue
		}
		matched++
		for _, fd := range fields {
			found := false
			for i, k := range p.keys {
				lk := strings.ToLower(k)
				hit := false
				for _, pre := range fd.prefixes {
					if strings.HasPrefix(lk, pre) {
						hit = true
						break
					}
				}
				if !hit || p.vals[i].kind != 'n' {
					continue
				}
				old, err := strconv.ParseFloat(p.vals[i].str, 64)
				if err != nil {
					continue
				}
				nv := old * fd.factor(f)
				p.vals[i].str = strconv.FormatFloat(roundSig(nv, 10), 'f', -1, 64)
				res.Changes = append(res.Changes, Change{Profile: name, Key: k, Old: old, New: nv})
				found = true
				break
			}
			if !found {
				missing[fd.label] = true
			}
		}
	}
	if matched == 0 {
		if profileName != "" {
			return nil, res, fmt.Errorf("Raw-Accel-Profil %q nicht gefunden", profileName)
		}
		return nil, res, ErrNoProfiles
	}
	for m := range missing {
		res.Missing = append(res.Missing, m)
	}
	var buf bytes.Buffer
	root.encode(&buf, 0)
	buf.WriteByte('\n')
	return buf.Bytes(), res, nil
}

// History remembers which factors were applied to which file so that a new
// export replaces the previous one instead of compounding it.
type History map[string]map[string]Factors // abs path → Raw Accel profile ("" = all) → factors

// LoadHistory reads the history file (missing file = empty history).
func LoadHistory(path string) History {
	h := History{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &h)
	}
	return h
}

// Save writes the history file.
func (h History) Save(path string) error {
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// PatchFile patches a settings.json on disk, writing a timestamped backup
// first. prev (may be nil) are the factors of an earlier export to undo.
func PatchFile(path string, f Factors, profileName string, prev *Factors, dryRun bool) (PatchResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PatchResult{}, err
	}
	eff := f
	if prev != nil {
		eff = f.Divide(*prev)
	}
	out, res, err := Patch(data, eff, profileName)
	if err != nil || dryRun {
		return res, err
	}
	backup := fmt.Sprintf("%s.omnivector-backup-%s", path, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backup, data, 0o644); err != nil {
		return res, fmt.Errorf("Sicherung fehlgeschlagen: %w", err)
	}
	res.Backup = backup
	tmp := filepath.Join(filepath.Dir(path), ".omnivector-tmp-settings.json")
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return res, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return res, err
	}
	return res, nil
}

func roundSig(v float64, digits int) float64 {
	if v == 0 || isInfOrNaN(v) {
		return v
	}
	s := strconv.FormatFloat(v, 'g', digits, 64)
	r, _ := strconv.ParseFloat(s, 64)
	return r
}

func isInfOrNaN(v float64) bool { return v != v || v > 1e308 || v < -1e308 }

// ---- order-preserving JSON ----------------------------------------------------

type node struct {
	kind byte // 'o' object, 'a' array, 's' string, 'n' number, 'b' bool, 'z' null
	keys []string
	vals []*node
	str  string // string value, number text or "true"/"false"
}

func (n *node) get(key string) *node {
	if n == nil || n.kind != 'o' {
		return nil
	}
	for i, k := range n.keys {
		if k == key {
			return n.vals[i]
		}
	}
	return nil
}

func parseOrdered(data []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("zusätzliche Daten nach dem JSON-Objekt")
	}
	return n, nil
}

func parseValue(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &node{kind: 'o'}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("Objektschlüssel erwartet")
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				n.keys = append(n.keys, k)
				n.vals = append(n.vals, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return n, nil
		case '[':
			n := &node{kind: 'a'}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				n.vals = append(n.vals, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return n, nil
		}
		return nil, fmt.Errorf("unerwartetes Zeichen %v", t)
	case string:
		return &node{kind: 's', str: t}, nil
	case json.Number:
		return &node{kind: 'n', str: t.String()}, nil
	case bool:
		return &node{kind: 'b', str: strconv.FormatBool(t)}, nil
	case nil:
		return &node{kind: 'z'}, nil
	}
	return nil, fmt.Errorf("unbekannter JSON-Wert %v", tok)
}

func writeString(buf *bytes.Buffer, s string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	buf.Write(bytes.TrimRight(b.Bytes(), "\n"))
}

func (n *node) encode(buf *bytes.Buffer, level int) {
	indent := func(l int) {
		buf.WriteByte('\n')
		for i := 0; i < l; i++ {
			buf.WriteString("  ")
		}
	}
	switch n.kind {
	case 'o':
		if len(n.keys) == 0 {
			buf.WriteString("{}")
			return
		}
		buf.WriteByte('{')
		for i, k := range n.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			indent(level + 1)
			writeString(buf, k)
			buf.WriteString(": ")
			n.vals[i].encode(buf, level+1)
		}
		indent(level)
		buf.WriteByte('}')
	case 'a':
		if len(n.vals) == 0 {
			buf.WriteString("[]")
			return
		}
		buf.WriteByte('[')
		for i, v := range n.vals {
			if i > 0 {
				buf.WriteByte(',')
			}
			indent(level + 1)
			v.encode(buf, level+1)
		}
		indent(level)
		buf.WriteByte(']')
	case 's':
		writeString(buf, n.str)
	case 'n', 'b':
		buf.WriteString(n.str)
	default:
		buf.WriteString("null")
	}
}
