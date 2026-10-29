// Package server provides the local web UI and its JSON API. It only ever
// listens on the loopback interface and protects state-changing requests
// against DNS rebinding and cross-site request forgery.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Gubriiil/PureVector/internal/calib"
	"github.com/Gubriiil/PureVector/internal/engine"
	"github.com/Gubriiil/PureVector/internal/profile"
	"github.com/Gubriiil/PureVector/internal/vector"
)

// Header that every state-changing request must carry. Browsers cannot add
// custom headers to cross-site requests without a CORS preflight, which
// this server never approves.
const csrfHeader = "X-OmniVector"

const (
	maxBody        = 1 << 20  // 1 MiB for ordinary requests
	maxSessionBody = 64 << 20 // calibration uploads
	pendingTTL     = 2 * time.Hour
)

// Options configure the server.
type Options struct {
	Version string
	Static  fs.FS // web assets (index.html, calibrate.html, app.css, js/…)
	Logf    func(format string, args ...any)
	// RawAccel is called for GET /api/profiles/{name}/rawaccel (optional).
	RawAccel func(p profile.Profile) (any, error)
}

// Server implements http.Handler.
type Server struct {
	eng   *engine.Engine
	store *profile.Store
	opt   Options
	mux   *http.ServeMux

	mu      sync.Mutex
	port    string
	pending map[string]*pendingCalibration
}

type pendingCalibration struct {
	ctx         engine.CalibrationContext
	session     *calib.Session
	result      *calib.Result
	sessionFile string
	created     time.Time
}

// New creates the server.
func New(eng *engine.Engine, opt Options) *Server {
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	s := &Server{eng: eng, store: eng.Store(), opt: opt, mux: http.NewServeMux(), pending: map[string]*pendingCalibration{}}
	s.routes()
	return s
}

// Listen binds the address. Only loopback addresses are accepted.
func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("aus Sicherheitsgründen nur Loopback-Adressen erlaubt (z. B. 127.0.0.1:47823), nicht %q", addr)
	}
	return net.Listen("tcp", addr)
}

// Serve runs the HTTP server on l until it is closed.
func (s *Server) Serve(l net.Listener) error {
	_, port, _ := net.SplitHostPort(l.Addr().String())
	s.mu.Lock()
	s.port = port
	s.mu.Unlock()
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	err := srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// ServeHTTP applies the security checks and dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")

	if !s.hostAllowed(r.Host) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if r.Header.Get(csrfHeader) != "1" {
			writeErr(w, http.StatusForbidden, "fehlender Sicherheits-Header")
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			writeErr(w, http.StatusForbidden, "fremde Herkunft")
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Cache-Control", "no-store")
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) hostAllowed(host string) bool {
	s.mu.Lock()
	port := s.port
	s.mu.Unlock()
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		return false
	}
	if port != "" && p != port {
		return false
	}
	switch strings.ToLower(h) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/ping", s.handlePing)
	m.HandleFunc("GET /api/state", s.handleState)
	m.HandleFunc("GET /api/live", s.handleLive)
	m.HandleFunc("POST /api/enabled", s.handleEnabled)
	m.HandleFunc("POST /api/toggle", s.handleToggle)
	m.HandleFunc("POST /api/default", s.handleDefault)
	m.HandleFunc("POST /api/assign", s.handleAssign)
	m.HandleFunc("GET /api/profiles", s.handleProfiles)
	m.HandleFunc("POST /api/profiles", s.handleSaveProfile)
	m.HandleFunc("GET /api/profiles/{name}", s.handleProfile)
	m.HandleFunc("DELETE /api/profiles/{name}", s.handleDeleteProfile)
	m.HandleFunc("GET /api/profiles/{name}/rawaccel", s.handleRawAccel)
	m.HandleFunc("POST /api/calibration/start", s.handleCalStart)
	m.HandleFunc("POST /api/calibration/cancel", s.handleCalCancel)
	m.HandleFunc("POST /api/calibration/heartbeat", s.handleCalHeartbeat)
	m.HandleFunc("POST /api/calibration/finish", s.handleCalFinish)
	m.HandleFunc("POST /api/calibration/preview", s.handleCalPreview)
	m.HandleFunc("POST /api/calibration/save", s.handleCalSave)
	if s.opt.Static != nil {
		files := http.FileServerFS(s.opt.Static)
		m.HandleFunc("GET /calibrate", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, s.opt.Static, "calibrate.html")
		})
		m.Handle("GET /", files)
	}
}

// ---- helpers -----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("JSON-Kodierung fehlgeschlagen: %v", err)
		status = http.StatusInternalServerError
		b = []byte(`{"error":"interner Kodierungsfehler"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "ungültige Anfrage: "+err.Error())
		return false
	}
	return true
}

// ---- state -------------------------------------------------------------------

type profileSummary struct {
	Name      string          `json:"name"`
	Gains     []float64       `json:"gains"`
	CreatedAt time.Time       `json:"createdAt"`
	Note      string          `json:"note,omitempty"`
	BuiltIn   bool            `json:"builtIn"`
	Source    *profile.Source `json:"source,omitempty"`
}

func summarize(ps []profile.Profile) []profileSummary {
	out := make([]profileSummary, len(ps))
	for i, p := range ps {
		out[i] = profileSummary{Name: p.Name, Gains: p.Gains, CreatedAt: p.CreatedAt, Note: p.Note, BuiltIn: p.BuiltIn, Source: p.Source}
	}
	return out
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"app": "omnivector", "version": s.opt.Version})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"app":            "omnivector",
		"version":        s.opt.Version,
		"platform":       runtime.GOOS,
		"enabled":        cfg.Enabled,
		"bypassed":       s.eng.Bypassed(),
		"defaultProfile": cfg.DefaultProfile,
		"hotkey":         cfg.Hotkey,
		"configDir":      s.store.Dir(),
		"backend":        s.eng.BackendStatus(),
		"devices":        s.eng.Devices(),
		"profiles":       summarize(s.store.Profiles()),
	})
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	snap := s.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  snap.Enabled,
		"bypassed": s.eng.Bypassed(),
		"devices":  s.eng.Devices(),
	})
}

func (s *Server) handleEnabled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, maxBody, &req) {
		return
	}
	if err := s.store.SetEnabled(req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
}

func (s *Server) handleToggle(w http.ResponseWriter, r *http.Request) {
	on, err := s.store.Toggle()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": on})
}

func (s *Server) handleDefault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string `json:"profile"`
	}
	if !readJSON(w, r, maxBody, &req) {
		return
	}
	if err := s.store.SetDefault(req.Profile); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"defaultProfile": req.Profile})
}

func (s *Server) handleAssign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device  string `json:"device"`
		Name    string `json:"name"`
		Profile string `json:"profile"`
	}
	if !readJSON(w, r, maxBody, &req) {
		return
	}
	if err := s.store.AssignDevice(req.Device, req.Name, req.Profile); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"device": req.Device, "profile": req.Profile})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, summarize(s.store.Profiles()))
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.store.Profile(r.PathValue("name"))
	if !ok {
		writeErr(w, http.StatusNotFound, "Profil nicht gefunden")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleSaveProfile(w http.ResponseWriter, r *http.Request) {
	var p profile.Profile
	if !readJSON(w, r, maxBody, &p) {
		return
	}
	p.CreatedAt = time.Time{}
	if p.Source == nil {
		p.Source = &profile.Source{Kind: "manual"}
	}
	if err := s.store.SaveProfile(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, _ := s.store.Profile(p.Name)
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteProfile(r.PathValue("name")); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleRawAccel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.store.Profile(r.PathValue("name"))
	if !ok {
		writeErr(w, http.StatusNotFound, "Profil nicht gefunden")
		return
	}
	if s.opt.RawAccel == nil {
		writeErr(w, http.StatusNotImplemented, "Raw-Accel-Export nicht verfügbar")
		return
	}
	res, err := s.opt.RawAccel(p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- calibration -------------------------------------------------------------

func (s *Server) handleCalStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if !readJSON(w, r, maxBody, &req) {
		return
	}
	info, err := s.eng.StartCalibration(req.Mode)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleCalCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, maxBody, &req) {
		return
	}
	s.eng.CancelCalibration(req.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}

func (s *Server) handleCalHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, maxBody, &req) {
		return
	}
	if err := s.eng.HeartbeatCalibration(req.ID); err != nil {
		writeErr(w, http.StatusNotFound, "Kalibrierung nicht (mehr) aktiv")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": true})
}

type finishRequest struct {
	ID      string         `json:"id"`
	Session *calib.Session `json:"session"`
}

type calContextJSON struct {
	DeviceKey   string    `json:"deviceKey"`
	DeviceName  string    `json:"deviceName"`
	Mode        string    `json:"mode"`
	BaseProfile string    `json:"baseProfile"`
	BaseGains   []float64 `json:"baseGains"`
}

func (s *Server) handleCalFinish(w http.ResponseWriter, r *http.Request) {
	var req finishRequest
	if !readJSON(w, r, maxSessionBody, &req) {
		return
	}
	if req.Session == nil || len(req.Session.Trials) == 0 {
		writeErr(w, http.StatusBadRequest, "keine Messdaten übertragen")
		return
	}
	ctx, err := s.eng.FinishCalibration(req.ID)
	if err != nil {
		// The engine may have been restarted; analyse anyway, as a fresh
		// measurement on an unknown device.
		ctx = engine.CalibrationContext{ID: req.ID, Mode: engine.ModeFresh}
	}
	if ctx.ID == "" {
		ctx.ID = req.ID
	}
	if ctx.ID == "" {
		ctx.ID = fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	sessFile := s.saveSession(ctx.ID, req.Session)

	res, err := calib.Analyze(req.Session, calib.Options{Directions: req.Session.Settings.Directions})
	if err != nil {
		status := http.StatusInternalServerError
		msg := err.Error()
		if errors.Is(err, calib.ErrNotEnoughData) {
			status = http.StatusUnprocessableEntity
			valid, total := 0, len(req.Session.Trials)
			if res != nil {
				valid = res.Counts.Valid
			}
			msg = fmt.Sprintf("Zu wenige auswertbare Schüsse (%d von %d). Bitte länger spielen (mindestens %d gültige).", valid, total, calib.MinValidTrials)
		}
		writeJSON(w, status, map[string]any{"error": msg, "counts": countsOf(res)})
		return
	}
	s.mu.Lock()
	s.gcPendingLocked()
	s.pending[ctx.ID] = &pendingCalibration{ctx: ctx, session: req.Session, result: res, sessionFile: sessFile, created: time.Now()}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"id":            ctx.ID,
		"result":        res,
		"points":        trialPoints(res),
		"context":       calContextJSON{ctx.DeviceKey, ctx.DeviceName, ctx.Mode, ctx.BaseProfile, ctx.BaseGains},
		"suggestedName": suggestName(ctx),
		"final":         composeGains(ctx.BaseGains, res.Suggested.Gains),
	})
}

func countsOf(res *calib.Result) any {
	if res == nil {
		return nil
	}
	return res.Counts
}

// trialPoints is a compact list for the scatter plot:
// [direction°, landing ratio, valid 0/1, return 0/1].
func trialPoints(res *calib.Result) [][4]float64 {
	out := make([][4]float64, 0, len(res.Trials))
	for _, m := range res.Trials {
		if m.Ratio == 0 || math.IsNaN(m.Ratio) {
			continue
		}
		v, k := 0.0, 0.0
		if m.Valid {
			v = 1
		}
		if m.Kind == calib.KindReturn {
			k = 1
		}
		out = append(out, [4]float64{math.Round(m.Theta*1800/math.Pi) / 10, math.Round(m.Ratio*1000) / 1000, v, k})
	}
	return out
}

func suggestName(ctx engine.CalibrationContext) string {
	ts := time.Now().Format("2006-01-02 15.04")
	if ctx.BaseProfile != "" {
		return "Verfeinert " + ts
	}
	return "Kalibrierung " + ts
}

// composeGains multiplies the profile that was active during a refine
// measurement with the new correction.
func composeGains(base, corr []float64) []float64 {
	if len(base) == 0 {
		return append([]float64(nil), corr...)
	}
	a, err := vector.NewGainTable(corr)
	if err != nil {
		return append([]float64(nil), corr...)
	}
	b, err := vector.NewGainTable(base)
	if err != nil {
		return append([]float64(nil), corr...)
	}
	c, err := vector.Compose(a, b)
	if err != nil {
		return append([]float64(nil), corr...)
	}
	return c.Gains()
}

func (s *Server) gcPendingLocked() {
	for id, p := range s.pending {
		if time.Since(p.created) > pendingTTL {
			delete(s.pending, id)
		}
	}
}

func (s *Server) saveSession(id string, sess *calib.Session) string {
	name := fmt.Sprintf("%s-%s.json", time.Now().Format("20060102-150405"), sanitizeID(id))
	path := filepath.Join(s.store.SessionsDir(), name)
	b, err := json.Marshal(sess)
	if err != nil {
		return ""
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		s.opt.Logf("Messdaten konnten nicht gespeichert werden: %v", err)
		return ""
	}
	return name
}

func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	if b.Len() > 32 {
		return b.String()[:32]
	}
	return b.String()
}

type previewRequest struct {
	ID        string  `json:"id"`
	Reference string  `json:"reference"`
	Strength  float64 `json:"strength"`
}

func (s *Server) lookupPending(id string) (*pendingCalibration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	return p, ok
}

func (s *Server) handleCalPreview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if !readJSON(w, r, maxBody, &req) {
		return
	}
	p, ok := s.lookupPending(req.ID)
	if !ok {
		writeErr(w, http.StatusNotFound, "Messung nicht (mehr) vorhanden – bitte neu kalibrieren")
		return
	}
	sug := p.result.Suggest(req.Reference, req.Strength, calib.DefaultMinGain, calib.DefaultMaxGain)
	writeJSON(w, http.StatusOK, map[string]any{"suggestion": sug, "final": composeGains(p.ctx.BaseGains, sug.Gains)})
}

func (s *Server) handleCalSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		previewRequest
		Name   string `json:"name"`
		Target string `json:"target"` // "default", "device" or "none"
	}
	if !readJSON(w, r, maxBody, &req) {
		return
	}
	p, ok := s.lookupPending(req.ID)
	if !ok {
		writeErr(w, http.StatusNotFound, "Messung nicht (mehr) vorhanden – bitte neu kalibrieren")
		return
	}
	if req.Target == "device" && p.ctx.DeviceKey == "" {
		writeErr(w, http.StatusBadRequest, "Es wurde keine Maus erkannt – bitte „Für alle Mäuse“ wählen")
		return
	}
	sug := p.result.Suggest(req.Reference, req.Strength, calib.DefaultMinGain, calib.DefaultMaxGain)
	measured := make([]float64, len(p.result.PerDirection))
	for k, d := range p.result.PerDirection {
		measured[k] = d.Shrunk
	}
	kind := "calibration"
	if p.ctx.BaseProfile != "" {
		kind = "refine"
	}
	prof := profile.Profile{
		Name:  strings.TrimSpace(req.Name),
		Gains: composeGains(p.ctx.BaseGains, sug.Gains),
		Source: &profile.Source{
			Kind: kind, SessionFile: p.sessionFile, Reference: sug.Reference, Strength: sug.Strength,
			Parent: p.ctx.BaseProfile, Device: p.ctx.DeviceName, ValidTrials: p.result.Counts.Valid,
			PValue: p.result.Model.PValue, SpreadPct: p.result.Model.SpreadPct, Measured: measured,
		},
	}
	if err := s.store.SaveProfile(prof); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var err error
	switch req.Target {
	case "device":
		err = s.store.AssignDevice(p.ctx.DeviceKey, p.ctx.DeviceName, prof.Name)
	case "none":
	default:
		err = s.store.SetDefault(prof.Name)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	saved, _ := s.store.Profile(prof.Name)
	writeJSON(w, http.StatusOK, saved)
}
