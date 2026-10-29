// Command omnivector applies a calibrated, direction-dependent sensitivity
// profile (the idea behind Corsair's PureVector) to every mouse connected to
// the computer.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Gubriiil/PureVector/internal/backend"
	"github.com/Gubriiil/PureVector/internal/calib"
	"github.com/Gubriiil/PureVector/internal/engine"
	"github.com/Gubriiil/PureVector/internal/hotkey"
	"github.com/Gubriiil/PureVector/internal/profile"
	"github.com/Gubriiil/PureVector/internal/rawaccel"
	"github.com/Gubriiil/PureVector/internal/server"
	"github.com/Gubriiil/PureVector/internal/sim"
	"github.com/Gubriiil/PureVector/internal/vector"
	"github.com/Gubriiil/PureVector/web"
)

// version is overridden at build time with -ldflags "-X main.version=…".
var version = "1.0.0-dev"

const usage = `OmniVector – richtungsabhängige Sensitivität für jede Maus (inspiriert von Corsair PureVector)

Aufruf:
  omnivector [run]            Dienst + Web-Oberfläche starten (Standard)
  omnivector calibrate        Kalibrierung im Browser öffnen
  omnivector status           Zustand anzeigen
  omnivector on | off | toggle
  omnivector devices          erkannte Mäuse auflisten
  omnivector profiles         Profile auflisten
  omnivector use PROFIL       Standardprofil für alle Mäuse setzen
  omnivector rawaccel --profile PROFIL [--settings PFAD\settings.json]
                              Profil in Raw Accel übertragen (Anti-Cheat-kompatibel)
  omnivector analyze DATEI    gespeicherte Messung erneut auswerten
  omnivector simulate         virtuellen Spieler kalibrieren (Demo/Selbsttest)
  omnivector version

Gemeinsame Optionen:
  --config VERZ    Konfigurationsverzeichnis (Standard: Benutzer-Konfigurationsordner/OmniVector)

"omnivector BEFEHL -h" zeigt die Optionen eines Befehls.
`

func main() {
	log.SetFlags(log.Ltime)
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run", "start":
		err = cmdRun(args)
	case "calibrate", "kalibrieren":
		err = cmdCalibrate(args)
	case "status":
		err = cmdStatus(args)
	case "on", "off", "toggle":
		err = cmdSwitch(cmd, args)
	case "devices":
		err = cmdDevices(args)
	case "profiles":
		err = cmdProfiles(args)
	case "use":
		err = cmdUse(args)
	case "rawaccel":
		err = cmdRawAccel(args)
	case "analyze":
		err = cmdAnalyze(args)
	case "simulate":
		err = cmdSimulate(args)
	case "version", "--version":
		fmt.Printf("omnivector %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "Unbekannter Befehl %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

// ---- shared helpers ------------------------------------------------------------

type common struct {
	fs     *flag.FlagSet
	config string
	listen string
}

func newFlags(name string) *common {
	c := &common{fs: flag.NewFlagSet(name, flag.ExitOnError)}
	c.fs.StringVar(&c.config, "config", "", "Konfigurationsverzeichnis")
	c.fs.StringVar(&c.listen, "listen", "", "Adresse der Web-Oberfläche (Standard aus config.json: "+profile.DefaultListen+")")
	return c
}

func (c *common) openStore() (*profile.Store, error) {
	dir := c.config
	if dir == "" {
		d, err := profile.DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	return profile.Open(dir)
}

// addr returns the listen address from flag or config.
func (c *common) addr() string {
	if c.listen != "" {
		return c.listen
	}
	if st, err := c.openStore(); err == nil {
		if a := st.Config().Listen; a != "" {
			return a
		}
	}
	return profile.DefaultListen
}

var httpClient = &http.Client{Timeout: 3 * time.Second}

// remote talks to a running instance; ok is false if none is running.
func remote(addr, method, path string, body any, out any) (ok bool, err error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, "http://"+addr+path, rd)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-OmniVector", "1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return true, errors.New(e.Error)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return true, err
		}
	}
	return true, nil
}

func running(addr string) bool {
	var p struct {
		App string `json:"app"`
	}
	ok, err := remote(addr, "GET", "/api/ping", nil, &p)
	return ok && err == nil && p.App == "omnivector"
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if os.Geteuid() == 0 {
			return // never start a browser as root
		}
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// ---- run -------------------------------------------------------------------------

func cmdRun(args []string) error {
	c := newFlags("run")
	backendName := c.fs.String("backend", "auto", "Eingabe-Backend: auto, interception (Windows), evdev (Linux), none")
	noBrowser := c.fs.Bool("no-browser", false, "Browser nicht automatisch öffnen")
	_ = c.fs.Parse(args)

	store, err := c.openStore()
	if err != nil {
		return err
	}
	addr := c.listen
	if addr == "" {
		addr = store.Config().Listen
	}
	url := "http://" + addr + "/"
	if running(addr) {
		fmt.Println("OmniVector läuft bereits:", url)
		if !*noBrowser {
			openBrowser(url)
		}
		return nil
	}

	eng := engine.New(store)
	be, err := backend.New(*backendName, eng)
	if err != nil {
		return err
	}
	l, err := server.Listen(addr)
	if err != nil {
		return fmt.Errorf("Web-Oberfläche kann nicht auf %s starten: %w", addr, err)
	}
	srv := server.New(eng, server.Options{Version: version, Static: web.Files, RawAccel: rawAccelInfo})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		if err := srv.Serve(l); err != nil {
			log.Printf("Web-Server: %v", err)
		}
	}()
	beDone := make(chan error, 1)
	go func() { beDone <- be.Run(ctx) }()

	cfg := store.Config()
	hk := cfg.Hotkey
	if hk != "" {
		err := hotkey.Run(ctx, hk, func() {
			on, err := store.Toggle()
			if err != nil {
				log.Printf("Umschalten fehlgeschlagen: %v", err)
				return
			}
			hotkey.Feedback(on)
			log.Printf("OmniVector %s (Tastenkürzel)", map[bool]string{true: "eingeschaltet", false: "ausgeschaltet"}[on])
		})
		switch {
		case err == nil:
			log.Printf("Tastenkürzel zum Umschalten: %s", strings.ToUpper(hk))
		case errors.Is(err, hotkey.ErrUnsupported):
		default:
			log.Printf("Tastenkürzel %s nicht verfügbar: %v", hk, err)
		}
	}

	fmt.Printf("\n  OmniVector %s läuft – Oberfläche: %s\n  Konfiguration: %s\n  Beenden mit Strg+C (Mausbewegung ist danach sofort wieder original).\n\n", version, url, store.Dir())
	if !*noBrowser {
		openBrowser(url)
	}

	select {
	case <-ctx.Done():
	case err := <-beDone:
		if err != nil {
			log.Printf("Eingabe-Backend: %v", err)
			log.Printf("Die Oberfläche läuft weiter (Kalibrierung, Profile, Raw-Accel-Export).")
		}
		<-ctx.Done()
	}
	log.Printf("Beende …")
	cancel()
	l.Close()
	select {
	case <-beDone:
	case <-time.After(3 * time.Second):
	}
	return nil
}

func rawAccelInfo(p profile.Profile) (any, error) {
	fit, err := rawaccel.FitProfile(p.Gains)
	if err != nil {
		return nil, err
	}
	exe := "omnivector"
	path := "/pfad/zu/rawaccel/settings.json"
	if runtime.GOOS == "windows" {
		exe = "omnivector.exe"
		path = `C:\Pfad\zu\rawaccel\settings.json`
	}
	return map[string]any{
		"profile":     p.Name,
		"axis":        fit.Axis,
		"factors":     fit.Factors,
		"model":       fit.Model,
		"coveragePct": fit.CoveragePct,
		"maxErrorPct": fit.MaxErrorPct,
		"command":     fmt.Sprintf(`%s rawaccel --profile "%s" --settings "%s"`, exe, p.Name, path),
	}, nil
}

// ---- simple commands ---------------------------------------------------------

func cmdCalibrate(args []string) error {
	c := newFlags("calibrate")
	_ = c.fs.Parse(args)
	addr := c.addr()
	if !running(addr) {
		fmt.Println("OmniVector läuft noch nicht – bitte zuerst „omnivector“ starten (der Dienst wendet die Profile an).")
		return nil
	}
	url := "http://" + addr + "/calibrate"
	fmt.Println("Kalibrierung:", url)
	openBrowser(url)
	return nil
}

type stateJSON struct {
	Version        string               `json:"version"`
	Enabled        bool                 `json:"enabled"`
	Bypassed       bool                 `json:"bypassed"`
	DefaultProfile string               `json:"defaultProfile"`
	Backend        engine.BackendStatus `json:"backend"`
	Devices        []engine.DeviceInfo  `json:"devices"`
}

func cmdStatus(args []string) error {
	c := newFlags("status")
	_ = c.fs.Parse(args)
	addr := c.addr()
	var st stateJSON
	ok, err := remote(addr, "GET", "/api/state", nil, &st)
	if !ok {
		store, err := c.openStore()
		if err != nil {
			return err
		}
		cfg := store.Config()
		fmt.Printf("Dienst: läuft nicht (starten mit „omnivector“)\nEingeschaltet: %s\nStandardprofil: %s\n", yesNo(cfg.Enabled), cfg.DefaultProfile)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("Dienst: läuft (v%s) – http://%s/\n", st.Version, addr)
	state := yesNo(st.Enabled)
	if st.Bypassed {
		state += " (pausiert für Kalibrierung)"
	}
	fmt.Printf("Eingeschaltet: %s\nStandardprofil: %s\n", state, st.DefaultProfile)
	be := st.Backend.Name
	if st.Backend.Error != "" {
		be += " – FEHLER: " + st.Backend.Error
	} else if st.Backend.Running {
		be += " – aktiv"
	}
	fmt.Println("Backend:", be)
	printDevices(st.Devices)
	return nil
}

func yesNo(b bool) string {
	if b {
		return "ja"
	}
	return "nein"
}

func printDevices(ds []engine.DeviceInfo) {
	if len(ds) == 0 {
		fmt.Println("Mäuse: noch keine erkannt (Maus bewegen)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "GERÄT\tKENNUNG\tVERBUNDEN\tPROFIL")
	for _, d := range ds {
		prof := d.Profile
		if !d.Assigned {
			prof += " (Standard)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Name, d.Key, yesNo(d.Connected), prof)
	}
	w.Flush()
}

func cmdSwitch(which string, args []string) error {
	c := newFlags(which)
	_ = c.fs.Parse(args)
	addr := c.addr()
	var res struct {
		Enabled bool `json:"enabled"`
	}
	var ok bool
	var err error
	if which == "toggle" {
		ok, err = remote(addr, "POST", "/api/toggle", map[string]any{}, &res)
	} else {
		ok, err = remote(addr, "POST", "/api/enabled", map[string]bool{"enabled": which == "on"}, &res)
	}
	if !ok {
		store, e := c.openStore()
		if e != nil {
			return e
		}
		if which == "toggle" {
			res.Enabled, err = store.Toggle()
		} else {
			res.Enabled = which == "on"
			err = store.SetEnabled(res.Enabled)
		}
	}
	if err != nil {
		return err
	}
	fmt.Println("OmniVector", map[bool]string{true: "eingeschaltet", false: "ausgeschaltet"}[res.Enabled])
	return nil
}

func cmdDevices(args []string) error {
	c := newFlags("devices")
	backendName := c.fs.String("backend", "auto", "Backend für die direkte Abfrage")
	_ = c.fs.Parse(args)
	var st stateJSON
	if ok, err := remote(c.addr(), "GET", "/api/state", nil, &st); ok {
		if err != nil {
			return err
		}
		printDevices(st.Devices)
		return nil
	}
	store, err := c.openStore()
	if err != nil {
		return err
	}
	be, err := backend.New(*backendName, engine.New(store))
	if err != nil {
		return err
	}
	list, err := be.ListDevices()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("Keine Mäuse gefunden.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "GERÄT\tKENNUNG\tDETAIL")
	for _, d := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\n", d.Name, d.Key, d.Detail)
	}
	return w.Flush()
}

func cmdProfiles(args []string) error {
	c := newFlags("profiles")
	_ = c.fs.Parse(args)
	store, err := c.openStore()
	if err != nil {
		return err
	}
	def := store.Config().DefaultProfile
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROFIL\tFAKTOREN (→ ↗ ↑ ↖ ← ↙ ↓ ↘ …)\tERSTELLT")
	for _, p := range store.Profiles() {
		name := p.Name
		if name == def {
			name += " *"
		}
		g := make([]string, len(p.Gains))
		for i, v := range p.Gains {
			g[i] = strconv.FormatFloat(v, 'f', 3, 64)
		}
		created := ""
		if !p.CreatedAt.IsZero() {
			created = p.CreatedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, strings.Join(g, " "), created)
	}
	w.Flush()
	fmt.Println("* = Standardprofil")
	return nil
}

func cmdUse(args []string) error {
	c := newFlags("use")
	_ = c.fs.Parse(args)
	if c.fs.NArg() != 1 {
		return errors.New("Aufruf: omnivector use PROFIL")
	}
	name := c.fs.Arg(0)
	if ok, err := remote(c.addr(), "POST", "/api/default", map[string]string{"profile": name}, nil); ok {
		if err != nil {
			return err
		}
	} else {
		store, err := c.openStore()
		if err != nil {
			return err
		}
		if err := store.SetDefault(name); err != nil {
			return err
		}
	}
	fmt.Printf("Standardprofil: %s\n", name)
	return nil
}

// ---- rawaccel -------------------------------------------------------------------

func cmdRawAccel(args []string) error {
	c := newFlags("rawaccel")
	name := c.fs.String("profile", "", "OmniVector-Profil (Standard: aktuelles Standardprofil)")
	settings := c.fs.String("settings", "", "Pfad zur settings.json von Raw Accel (ohne: nur Werte anzeigen)")
	raProfile := c.fs.String("rawaccel-profile", "", "nur dieses Raw-Accel-Profil ändern (Standard: alle)")
	dry := c.fs.Bool("dry-run", false, "nur anzeigen, nichts schreiben")
	fresh := c.fs.Bool("fresh", false, "frühere OmniVector-Exporte in dieser Datei nicht herausrechnen")
	_ = c.fs.Parse(args)

	store, err := c.openStore()
	if err != nil {
		return err
	}
	if *name == "" {
		*name = store.Config().DefaultProfile
	}
	p, ok := store.Profile(*name)
	if !ok {
		return fmt.Errorf("Profil %q nicht gefunden", *name)
	}
	fit, err := rawaccel.FitProfile(p.Gains)
	if err != nil {
		return err
	}
	fmt.Printf("Profil %q → Raw Accel (deckt %.0f %% der Richtungsunterschiede ab, max. Abweichung %.1f %%)\n\n", p.Name, fit.CoveragePct, fit.MaxErrorPct)
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RAW-ACCEL-FELD\tMULTIPLIZIEREN MIT")
	fmt.Fprintf(w, "Sensitivity multiplier / Output DPI\t%.4f\n", fit.Factors.Sens)
	fmt.Fprintf(w, "Y/X ratio\t%.4f\n", fit.Factors.YX)
	fmt.Fprintf(w, "L/R ratio\t%.4f\n", fit.Factors.LR)
	fmt.Fprintf(w, "U/D ratio\t%.4f\n", fit.Factors.UD)
	w.Flush()
	if *settings == "" {
		fmt.Println("\nMit --settings PFAD\\settings.json werden die Werte automatisch eingetragen (mit Sicherung).")
		return nil
	}
	abs, err := filepath.Abs(*settings)
	if err != nil {
		return err
	}
	histPath := filepath.Join(store.Dir(), "rawaccel-history.json")
	hist := rawaccel.LoadHistory(histPath)
	var prev *rawaccel.Factors
	if !*fresh {
		if f, ok := hist[abs][*raProfile]; ok {
			prev = &f
			fmt.Println("\nFrüherer OmniVector-Export in dieser Datei wird ersetzt (statt aufmultipliziert). Mit --fresh abschalten.")
		}
	}
	res, err := rawaccel.PatchFile(abs, fit.Factors, *raProfile, prev, *dry)
	if err != nil {
		return err
	}
	fmt.Println()
	for _, ch := range res.Changes {
		fmt.Printf("  [%s] %s: %g → %g\n", ch.Profile, ch.Key, ch.Old, roundTo(ch.New, 6))
	}
	if len(res.Missing) > 0 {
		fmt.Printf("\nDiese Raw-Accel-Version hat keine Felder für: %s – bitte Raw Accel aktualisieren (≥ 1.6).\n", strings.Join(res.Missing, ", "))
	}
	if *dry {
		fmt.Println("\n(Probelauf – nichts geschrieben)")
		return nil
	}
	if hist[abs] == nil {
		hist[abs] = map[string]rawaccel.Factors{}
	}
	hist[abs][*raProfile] = fit.Factors
	if err := hist.Save(histPath); err != nil {
		log.Printf("Verlauf konnte nicht gespeichert werden: %v", err)
	}
	fmt.Printf("\nGespeichert. Sicherung: %s\nJetzt Raw Accel öffnen und „Apply“ klicken (oder writer.exe settings.json ausführen).\n", res.Backup)
	return nil
}

func roundTo(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

// ---- analyze & simulate -------------------------------------------------------

func cmdAnalyze(args []string) error {
	c := newFlags("analyze")
	ref := c.fs.String("reference", calib.RefStrongest, "Bezug: strongest, mean, absolute")
	strength := c.fs.Float64("strength", 1, "Stärke der Korrektur 0..1")
	dirs := c.fs.Int("directions", 0, "Anzahl Richtungen (Standard: wie gemessen)")
	asJSON := c.fs.Bool("json", false, "Ergebnis als JSON ausgeben")
	_ = c.fs.Parse(args)
	if c.fs.NArg() != 1 {
		return errors.New("Aufruf: omnivector analyze DATEI.json (Messungen liegen im Ordner sessions/ des Konfigurationsverzeichnisses)")
	}
	b, err := os.ReadFile(c.fs.Arg(0))
	if err != nil {
		return err
	}
	var s calib.Session
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("keine gültige Messung: %w", err)
	}
	res, err := calib.Analyze(&s, calib.Options{Directions: *dirs, Reference: *ref, Strength: *strength})
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	printResult(res)
	return nil
}

func printResult(res *calib.Result) {
	fmt.Printf("Ziele: %d, auswertbar: %d (nach außen %d, zurück %d)\n", res.Counts.Total, res.Counts.Valid, res.Counts.Out, res.Counts.Return)
	if len(res.Counts.Rejected) > 0 {
		fmt.Printf("Verworfen: %v\n", res.Counts.Rejected)
	}
	p := "nicht berechenbar"
	if res.Model.PValue >= 0 {
		p = fmt.Sprintf("p = %.2g", res.Model.PValue)
	}
	fmt.Printf("Richtungsstreuung: %.1f %%  •  Richtungsunterschiede: %s  •  Datenqualität: %s\n\n", res.Model.SpreadPct, p, res.Quality)
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "RICHTUNG\tZIELE\tLANDUNG\t95%-BEREICH\tFAKTOR\tDANACH\t")
	s := res.Suggested
	for k, d := range res.PerDirection {
		mark := ""
		if k == s.ReferenceIndex {
			mark = " (Bezug)"
		}
		fmt.Fprintf(w, "%s%s\t%d\t%+.1f %%\t%+.1f … %+.1f %%\t%.3f×\t%+.1f %%\t\n", d.Name, mark, d.N,
			100*(d.Shrunk-1), 100*(d.RatioLow-1), 100*(d.RatioHigh-1), s.Gains[k], 100*(s.PredictedRatio[k]-1))
	}
	w.Flush()
	fmt.Printf("\nBezug: %s, Stärke %.0f %%, Gesamtsensitivität %+.1f %%\n", s.Reference, 100*s.Strength, s.OverallChangePct)
	for _, msg := range res.Warnings {
		fmt.Println("Hinweis:", msg)
	}
}

// residualRMS is the RMS directional extent error (log units, over all
// angles, mean removed) of a simulated player with an optional profile.
func residualRMS(bias func(float64) float64, tab *vector.GainTable) float64 {
	const n = 720
	v := make([]float64, n)
	m := 0.0
	for i := range v {
		th := float64(i) * 2 * math.Pi / n
		v[i] = bias(th)
		if tab != nil {
			v[i] += math.Log(tab.Exact(th))
		}
		m += v[i]
	}
	m /= n
	ss := 0.0
	for _, x := range v {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / n)
}

func cmdSimulate(args []string) error {
	c := newFlags("simulate")
	seed := c.fs.Int64("seed", 1, "Zufallsstartwert")
	targets := c.fs.Int("targets", 180, "Ziele nach außen (jeweils plus Rückweg)")
	hz := c.fs.Float64("hz", 1000, "Abtastrate der virtuellen Maus")
	biasSpec := c.fs.String("bias", "-0.03,0.04,0,0.05,45", "Fehlermodell: Mittel,A1,φ1°,A2,φ2° (log. Weitenfehler)")
	save := c.fs.String("save", "", "simulierte Messung zusätzlich als JSON speichern (für analyze/Tests)")
	_ = c.fs.Parse(args)
	parts := strings.Split(*biasSpec, ",")
	if len(parts) != 5 {
		return errors.New("--bias erwartet 5 Werte: Mittel,A1,φ1,A2,φ2")
	}
	var v [5]float64
	for i, s := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return fmt.Errorf("--bias: %w", err)
		}
		v[i] = f
	}
	bias := sim.HarmonicBias(v[0], v[1], v[2], v[3], v[4])
	task := sim.DefaultTask()
	task.Targets = *targets
	rng := rand.New(rand.NewSource(*seed))

	fmt.Println("1) Messung ohne Profil")
	sess := sim.Run(sim.DefaultPlayer(bias), sim.Rig{PollHz: *hz}, task, rng)
	sess.Settings.DPI, sess.Settings.Cm360, sess.Settings.FOV = 800, 40, 103
	if *save != "" {
		b, err := json.Marshal(sess)
		if err != nil {
			return err
		}
		if err := os.WriteFile(*save, b, 0o644); err != nil {
			return err
		}
		fmt.Println("Messung gespeichert:", *save)
	}
	res, err := calib.Analyze(sess, calib.Options{})
	if err != nil {
		return err
	}
	printResult(res)
	tab, err := vector.NewGainTable(res.Suggested.Gains)
	if err != nil {
		return err
	}
	fmt.Println("\n2) Kontrollmessung mit dem neuen Profil im (simulierten) Treiber")
	sess2 := sim.Run(sim.DefaultPlayer(bias), sim.Rig{PollHz: *hz, Profile: tab}, task, rng)
	res2, err := calib.Analyze(sess2, calib.Options{})
	if err != nil {
		return err
	}
	sig := "keine signifikanten Richtungsunterschiede mehr"
	if res2.Model.PValue >= 0 && res2.Model.PValue < 0.05 {
		sig = "noch signifikante Unterschiede"
	}
	fmt.Printf("Gemessene Richtungsstreuung: %.1f %% → %.1f %% (%s, p = %.2g)\n", res.Model.SpreadPct, res2.Model.SpreadPct, sig, res2.Model.PValue)
	before, after := residualRMS(bias, nil), residualRMS(bias, tab)
	fmt.Printf("Wahre Richtungsabweichung des virtuellen Spielers (RMS): %.1f %% → %.1f %% (%.0f %% beseitigt)\n",
		100*before, 100*after, 100*(1-after/before))
	return nil
}
