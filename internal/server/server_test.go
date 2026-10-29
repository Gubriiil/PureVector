package server

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gubriiil/PureVector/internal/calib"
	"github.com/Gubriiil/PureVector/internal/engine"
	"github.com/Gubriiil/PureVector/internal/profile"
	"github.com/Gubriiil/PureVector/internal/sim"
	"github.com/Gubriiil/PureVector/web"
)

const host = "127.0.0.1:47823"

func newServer(t *testing.T) (*Server, *engine.Engine) {
	t.Helper()
	st, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.SetLogger(t.Logf)
	eng := engine.New(st)
	return New(eng, Options{Version: "test", Static: web.Files, Logf: t.Logf}), eng
}

func do(t *testing.T, s *Server, method, path string, body any, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "http://"+host+path, rd)
	req.Host = host
	if method != http.MethodGet {
		req.Header.Set(csrfHeader, "1")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
}

func TestSecurityChecks(t *testing.T) {
	s, _ := newServer(t)
	if w := do(t, s, "GET", "/api/ping", nil, map[string]string{"Host": ""}); w.Code != 200 {
		t.Fatalf("ping: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "http://evil.example:47823/api/state", nil)
	req.Host = "evil.example:47823"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("DNS rebinding host accepted: %d", w.Code)
	}
	if w := do(t, s, "POST", "/api/toggle", nil, map[string]string{csrfHeader: ""}); w.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF header: %d", w.Code)
	}
	if w := do(t, s, "POST", "/api/toggle", nil, map[string]string{"Origin": "http://evil.example"}); w.Code != http.StatusForbidden {
		t.Errorf("foreign origin accepted: %d", w.Code)
	}
	if w := do(t, s, "POST", "/api/toggle", nil, map[string]string{"Origin": "http://" + host}); w.Code != 200 {
		t.Errorf("same-origin toggle: %d %s", w.Code, w.Body)
	}
	w = do(t, s, "GET", "/", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "OmniVector") {
		t.Errorf("index: %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP missing: %q", csp)
	}
}

func TestStaticFiles(t *testing.T) {
	s, _ := newServer(t)
	for path, want := range map[string]string{
		"/":                "<title>OmniVector</title>",
		"/calibrate":       "Kalibrierung",
		"/js/session.js":   "export class Session",
		"/app.css":         "--accent",
		"/js/radar.js":     "renderRadar",
		"/favicon.svg":     "<svg",
		"/js/calibrate.js": "pointerlockchange",
	} {
		w := do(t, s, "GET", path, nil, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d, missing %q", path, w.Code, want)
		}
	}
}

func TestCalibrationFlow(t *testing.T) {
	s, eng := newServer(t)
	mouse := eng.Device("VID_1B1C&PID_1BAC", "Testmaus", "test")

	w := do(t, s, "POST", "/api/calibration/start", map[string]string{"mode": "fresh"}, nil)
	var info engine.CalibrationInfo
	decode(t, w, &info)
	if w.Code != 200 || info.ID == "" || !eng.Bypassed() {
		t.Fatalf("start: %d %+v", w.Code, info)
	}
	// The "player" moves the mouse during the measurement.
	for i := 0; i < 50; i++ {
		eng.Process(mouse, 20, 3, int64(i)*1_000_000)
	}

	sess := sim.Run(sim.DefaultPlayer(sim.HarmonicBias(-0.03, 0.04, 0, 0.05, 45)), sim.Rig{PollHz: 1000}, sim.DefaultTask(), rand.New(rand.NewSource(5)))
	w = do(t, s, "POST", "/api/calibration/finish", map[string]any{"id": info.ID, "session": sess}, nil)
	if w.Code != 200 {
		t.Fatalf("finish: %d %s", w.Code, w.Body)
	}
	var fin struct {
		ID      string       `json:"id"`
		Result  calib.Result `json:"result"`
		Points  [][4]float64 `json:"points"`
		Context struct {
			DeviceKey  string `json:"deviceKey"`
			DeviceName string `json:"deviceName"`
		} `json:"context"`
		SuggestedName string    `json:"suggestedName"`
		Final         []float64 `json:"final"`
	}
	decode(t, w, &fin)
	if eng.Bypassed() {
		t.Error("bypass still active after finish")
	}
	if fin.Context.DeviceKey != mouse.Key || len(fin.Final) != 8 || len(fin.Points) < 300 || fin.Result.Counts.Valid < 300 {
		t.Fatalf("finish payload: ctx %+v final %v points %d valid %d", fin.Context, fin.Final, len(fin.Points), fin.Result.Counts.Valid)
	}

	w = do(t, s, "POST", "/api/calibration/preview", map[string]any{"id": fin.ID, "reference": "mean", "strength": 0.5}, nil)
	var prev struct {
		Suggestion calib.Suggestion `json:"suggestion"`
		Final      []float64        `json:"final"`
	}
	decode(t, w, &prev)
	if w.Code != 200 || prev.Suggestion.Reference != "mean" || prev.Suggestion.Strength != 0.5 {
		t.Fatalf("preview: %d %+v", w.Code, prev.Suggestion)
	}

	w = do(t, s, "POST", "/api/calibration/save", map[string]any{"id": fin.ID, "reference": "strongest", "strength": 1, "name": "Mein Profil", "target": "device"}, nil)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	snap := eng.Store().Snapshot()
	if tab, name := snap.TableFor(mouse.Key); tab == nil || name != "Mein Profil" {
		t.Fatalf("profile not assigned to the device: %v %q", tab, name)
	}
	p, ok := eng.Store().Profile("Mein Profil")
	if !ok || p.Source == nil || p.Source.Kind != "calibration" || p.Source.SessionFile == "" || p.Source.Device != "Testmaus" {
		t.Fatalf("saved profile: %+v %+v", p, p.Source)
	}

	// Refine: measure with the profile active; the result is composed.
	w = do(t, s, "POST", "/api/calibration/start", map[string]string{"mode": "refine"}, nil)
	decode(t, w, &info)
	eng.Process(mouse, 5, 0, 1)
	w = do(t, s, "POST", "/api/calibration/finish", map[string]any{"id": info.ID, "session": sess}, nil)
	var fin2 struct {
		Context struct {
			BaseProfile string `json:"baseProfile"`
		} `json:"context"`
		SuggestedName string `json:"suggestedName"`
	}
	decode(t, w, &fin2)
	if fin2.Context.BaseProfile != "Mein Profil" || !strings.HasPrefix(fin2.SuggestedName, "Verfeinert") {
		t.Errorf("refine context: %+v %q", fin2.Context, fin2.SuggestedName)
	}
}

func TestFinishWithTooLittleData(t *testing.T) {
	s, _ := newServer(t)
	sess := &calib.Session{Trials: []calib.Trial{{Kind: "out", TX: 300, R: 20, Samples: [][3]float64{{1, 1, 0}, {2, 2, 0}, {3, 3, 0}}}}}
	w := do(t, s, "POST", "/api/calibration/finish", map[string]any{"id": "x", "session": sess}, nil)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Zu wenige") {
		t.Errorf("got %d %s", w.Code, w.Body)
	}
}

func TestStateAndAssign(t *testing.T) {
	s, eng := newServer(t)
	eng.Device("K1", "Maus 1", "test")
	w := do(t, s, "POST", "/api/assign", map[string]string{"device": "K1", "name": "Maus 1", "profile": "neutral"}, nil)
	if w.Code != 200 {
		t.Fatalf("assign: %d %s", w.Code, w.Body)
	}
	w = do(t, s, "GET", "/api/state", nil, nil)
	var st struct {
		Devices  []engine.DeviceInfo `json:"devices"`
		Profiles []map[string]any    `json:"profiles"`
	}
	decode(t, w, &st)
	if len(st.Devices) != 1 || !st.Devices[0].Assigned || len(st.Profiles) != 1 {
		t.Errorf("state: %+v", st)
	}
	if w := do(t, s, "POST", "/api/assign", map[string]string{"device": "K1", "profile": "gibtsnicht"}, nil); w.Code != 400 {
		t.Errorf("unknown profile: %d", w.Code)
	}
}
