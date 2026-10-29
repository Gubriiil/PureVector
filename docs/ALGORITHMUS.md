# OmniVector – Algorithmus und Technik

Technische Referenz zu allen Verfahren, Parametern und Formaten. Hintergrund und Herleitung:
[PUREVECTOR_ANALYSE.md](PUREVECTOR_ANALYSE.md).

## 1. Konventionen

- Mausdaten: ganze **Counts**, x nach rechts, y nach **unten** (wie USB-HID, Windows und evdev).
- Richtungswinkel: θ = atan2(−dy, dx), gegen den Uhrzeigersinn ab „rechts“, 90° = „oben“ (Maus vom
  Körper weg).
- Profil: N Faktoren g₀ … g_{N−1} für die Stützrichtungen φ_k = k · 360°/N (N = 8 oder 16,
  erlaubt 4–72). g = 1 heißt unverändert, g = 0,95 heißt 5 % weniger Cursorweg in dieser Richtung.

## 2. Laufzeit-Transformation (`internal/vector`)

Für jedes Gerät gibt es einen eigenen Zustand. Pro Mausbericht d = (dx, dy):

1. **Richtungsgedächtnis** (wegbasiert, nicht zeitbasiert):

   ```
   w ← w · exp(−|d| / L) + d          L = 16 Counts
   w ← 0,  falls seit dem letzten Bericht > 50 ms vergangen sind
   θ = atan2(−w_y, w_x)
   ```

   Ein langsamer 45°-Zug kommt als (1,0), (0,1), (1,0) … an. Das Gedächtnis mittelt über die letzten
   ~L Counts Weg und liefert deshalb trotzdem 45° statt abwechselnd 0° und 90°.

2. **Faktor** g(θ): periodische Catmull-Rom-Interpolation der Werte ln g_k (stetig differenzierbar,
   trifft die Stützwerte exakt). Vorberechnet als Tabelle mit 4096 Einträgen (0,09° Auflösung).

3. **Ausgabe mit Restwert-Übertrag:**

   ```
   o = g(θ) · d + rest
   ausgabe = round_half_even(o)          rest = o − ausgabe   (|rest| ≤ 0,5)
   ```

Eigenschaften (alle durch Tests abgesichert):

| Eigenschaft | Begründung |
|---|---|
| Richtung jedes Berichts bleibt erhalten | g ist ein Skalar; waagerechte Bewegung bleibt exakt waagerecht |
| Keine Bewegung geht verloren oder kommt hinzu | Restwert-Übertrag; eine Achse ohne Eingabe gibt exakt 0 aus |
| Unabhängig von der Abtastrate (125–8000 Hz) | θ stammt aus dem Gedächtnis, nicht aus einzelnen Mini-Berichten |
| Gesamtfaktor je Strich auf ±0,4 % genau | Test über 12 Winkel × 3 Raten × 3 Weiten |
| ≈ 55–60 ns pro Bericht | ein `exp`, ein `atan2`, ein Tabellenzugriff |

Bei neutralem Profil, ausgeschaltetem Dienst oder pausierter Kalibrierung wird jeder Bericht
unverändert durchgereicht.

## 3. Kalibrier-Übung (`web/js/session.js`, `web/js/calibrate.js`)

| Parameter | Wert |
|---|---|
| Counts pro Grad | cpd = DPI / 2,54 · (cm/360°) / 360 |
| Ablauf | Ziel außen → Ziel in der Mitte → außen … (wie Spidershot); Fadenkreuz am Rundenstart zentriert |
| Richtungen | 16 Sektoren in gemischter Reihenfolge („Beutel“), Zufallswinkel im Sektor, also exakt ausgeglichen |
| Zielweite | 8–28° log-gleichverteilt (bei schmalem Bildschirm auf 80 % des vertikalen halben FOV begrenzt) |
| Zielradius | 1,4° |
| Zeitlimit je Ziel | 3 s |
| Darstellung | perspektivisch wie im Spiel: x = f·tan(Δ), f = (Breite/2) / tan(FOV/2) |
| Eingabe | Pointer Lock mit `unadjustedMovement` (Rohdaten ohne OS-Beschleunigung), `pointerrawupdate` (volle Abtastrate) bzw. `getCoalescedEvents()` |
| Aufzeichnung | Position relativ zum Erscheinen des Ziels, höchstens ein Wert pro Millisekunde |

Während einer **Neukalibrierung** ist das aktive Profil auf dem Server pausiert, damit die
Rohbewegung gemessen wird. Die Seite meldet sich alle 5 s. Bleibt die Meldung 20 s aus (Tab
geschlossen, Absturz), ist das Profil automatisch wieder aktiv. Beim Verlassen der Seite wird die
Pause sofort aufgehoben.

## 4. Analyse (`internal/calib`)

### 4.1 Zerlegung jeder Zielerfassung

1. Positionen auf ein 1-ms-Raster bringen (Treppenfunktion), dann Gauß-Filter mit
   σ = max(5 ms, typischer Ereignisabstand).
2. Geschwindigkeit entlang der Zielachse v(t) per zentraler Differenz.
3. **Spitze** v_max, **Beginn** = letzter Zeitpunkt vor der Spitze mit v < 5 % v_max. Danach wird die
   Zielachse vom tatsächlichen Startpunkt aus neu bestimmt.
4. **Ende der Primärbewegung** = erster Zeitpunkt nach der Spitze mit
   - v ≤ 5 % v_max (Stopp oder Umkehr), **oder**
   - lokalem Minimum < 60 % v_max, auf das innerhalb von 80 ms ein Wiederanstieg um ≥ 10 % v_max
     folgt (überlappende Korrekturbewegung).
5. Merkmale:
   - Landeverhältnis r = Weg entlang der Achse / Zielentfernung,
   - Winkelfehler,
   - Reaktionszeit, Dauer, Anzahl der Korrekturen, Zeit bis zum Treffer.

Verworfen wird ein Versuch bei:

- Entfernung < max(3 · Radius, 20 Counts),
- Reaktion < 60 ms (vorweggenommen) oder > 1,5 s,
- Primärdauer außerhalb 50–1500 ms,
- r außerhalb 0,4–1,8,
- Winkelfehler > 25°.

### 4.2 Richtungsmodell

Für jeden gültigen Versuch i:

```
y_i = ln r_i = Σ_k B_k(θ_i) · a_k  +  b · z_i  +  c · q_i  +  ε_i
```

- B_k: „Hut“-Basis, also lineare Aufteilung auf die zwei benachbarten Stützrichtungen,
- z_i = ln(Entfernung) − Mittelwert: gleicht Entfernungs-Effekte aus,
- q_i = Rückweg-Indikator − Mittelwert: Flicks zur Mitte dürfen ein anderes Niveau haben,
- a_k = logarithmisches Landeverhältnis in Richtung k.

Schätzung:

- Huber-gewichtete iterativ neugewichtete kleinste Quadrate (k = 1,345, Skala = 1,4826 · MAD).
- Schwacher Glättungsterm λ Σ (a_k − a_{k+1})² mit λ = 0,5, damit das System auch mit einer leeren
  Richtung lösbar bleibt.
- Standardfehler: s_k aus σ² (XᵀWX + P)⁻¹.

Die Hut-Basis mit schwacher Glättung hat in der Simulation über vier Fehlermuster am besten
abgeschnitten (Vergleich gegen Sektor-Zuordnung und λ = 5/15/40).

### 4.3 Test und Schrumpfung

- **Wald-Test** H₀: a₀ = … = a_{N−1} mit den N−1 Nachbardifferenzen, χ²(N−1) → p-Wert
  („Signifikanz der Unterschiede“).
- **DerSimonian-Laird-Schrumpfung:**

  ```
  w_k = 1 / s_k²     ā = Σ w_k a_k / Σ w_k     Q = Σ w_k (a_k − ā)²
  τ² = max(0, (Q − (N−1)) / (Σw − Σw²/Σw))
  a*_k = ā + τ² / (τ² + s_k²) · (a_k − ā)
  ```

  Ohne echte Unterschiede gilt τ² = 0, alle Richtungen fallen auf den Mittelwert und das Profil bleibt
  neutral. Verrauschte Richtungen werden stärker zum Mittel gezogen als gut gemessene.

### 4.4 Korrekturfaktoren

```
g_k = exp(−α · (a*_k − e_ref))       auf [0,80; 1,25] begrenzt
```

| Bezug | e_ref | Wirkung |
|---|---|---|
| `strongest` (Standard) | a*_j der Richtung j mit kleinstem \|a*_j\| | alle Richtungen landen wie die genaueste |
| `mean` | Mittelwert der a*_k | nur Unterschiede ausgleichen, Gesamtsensitivität exakt gleich |
| `absolute` | 0 | jede Richtung exakt aufs Ziel (experimentell) |

α ist die „Stärke“ (0–100 %). Die Datenqualität „gut“ verlangt ≥ 150 gültige Schüsse und
s_k < 0,02. Unter 40 gültigen Schüssen wird kein Profil vorgeschlagen.

**Verfeinern:** Die Messung läuft mit aktivem Profil g_alt. Die gemessene Restabweichung ergibt
g_korr, das neue Profil ist g_neu = g_alt · g_korr (bei unterschiedlicher Richtungszahl wird
umgerechnet).

## 5. Eingabe-Backends (`internal/backend`)

### Windows – Interception

- Lädt `interception.dll` (neben der .exe) per `syscall`, ohne cgo.
- Filter `INTERCEPTION_FILTER_MOUSE_ALL` auf allen Maus-Slots (11–20).
- Jedes Paket wird gelesen und wieder gesendet: relative Bewegung transformiert, alles andere
  (Tasten, Rad, absolute Bewegung von Tablets/RDP) unverändert.
- Geräteschlüssel aus der Hardware-ID (`VID_xxxx&PID_yyyy`, auch Bluetooth-Form). Lesbarer Name über
  Raw Input + `HidD_GetProductString`.
- Wird ein Slot nach > 1 s Pause wieder aktiv, wird die Hardware-ID neu geprüft (andere Maus
  eingesteckt?).
- Zeitbasis `QueryPerformanceCounter`, Verarbeitungs-Thread mit hoher Priorität.
- Ein interner Fehler gibt das Paket unverändert weiter.
- Beim Beenden wird der Interception-Kontext geschlossen. Stürzt der Prozess ab, schließt Windows
  ihn ebenfalls. In beiden Fällen ist die Maus sofort wieder original.

### Linux – evdev/uinput

- Alle 1 s nach neuen `/dev/input/event*` mit `EV_REL`/`REL_X`/`REL_Y`/`BTN_LEFT` suchen.
- uinput-Klon mit gleichem Namen, gleichen IDs und Fähigkeiten (Tasten, Achsen, MSC, Properties,
  ABS) anlegen, dann das Original per `EVIOCGRAB` exklusiv übernehmen. Klone tragen
  `phys = omnivector-virtual/…` und werden nie selbst übernommen.
- Je `SYN_REPORT`-Frame: REL_X/REL_Y summieren, transformieren und mit allen anderen Ereignissen des
  Frames weitergeben.
- Nach `SYN_DROPPED`: Frame verwerfen, Tastenzustand per `EVIOCGKEY` abgleichen (keine hängenden
  Tasten).
- Zeitstempel über `EVIOCSCLOCKID` = `CLOCK_MONOTONIC`.

## 6. Lokale API (`internal/server`)

Nur auf Loopback-Adressen.

Schutzmaßnahmen:

- `Host`-Prüfung gegen DNS-Rebinding,
- Pflicht-Header `X-OmniVector: 1` sowie `Origin`-Prüfung für alle ändernden Anfragen (gegen CSRF),
- strenge Content-Security-Policy, keine externen Ressourcen.

| Methode | Pfad | Zweck |
|---|---|---|
| GET | `/api/state`, `/api/live`, `/api/ping` | Zustand, Geräte, Profile; Live-Richtung/-Faktor |
| POST | `/api/enabled`, `/api/toggle` | ein/aus |
| POST | `/api/default`, `/api/assign` | Standardprofil; Profil je Gerät (`""` = Standard) |
| GET/POST/DELETE | `/api/profiles[/{name}]` | Profile lesen, anlegen, löschen |
| GET | `/api/profiles/{name}/rawaccel` | Vier-Richtungs-Anpassung für Raw Accel |
| POST | `/api/calibration/start\|heartbeat\|cancel` | Messung steuern (Pause des Profils) |
| POST | `/api/calibration/finish` | Messdaten auswerten (wird unter `sessions/` gespeichert) |
| POST | `/api/calibration/preview\|save` | Bezug/Stärke ausprobieren; Profil speichern und zuweisen |

## 7. Dateiformate

`profiles/NAME.json`:

```json
{
  "name": "Kalibrierung 2026-10-29 15.30",
  "gains": [0.990, 0.950, 1.034, 1.129, 1.106, 1.000, 1.047, 1.056],
  "createdAt": "2026-10-29T14:30:00Z",
  "source": { "kind": "calibration", "reference": "strongest", "strength": 1,
              "validTrials": 360, "pValue": 2.8e-23, "spreadPct": 5.5,
              "device": "Corsair SABRE v3 PRO", "sessionFile": "20261029-153000-….json",
              "measured": [1.013, 1.056, 0.970, 0.889, 0.907, 1.003, 0.958, 0.950] }
}
```

`config.json`:

```json
{
  "enabled": true,
  "defaultProfile": "Kalibrierung 2026-10-29 15.30",
  "devices": { "VID_056A&PID_0374": "neutral" },
  "listen": "127.0.0.1:47823",
  "hotkey": "ctrl+alt+f10",
  "tracker": { "memoryCounts": 16, "idleResetMs": 50 }
}
```

`sessions/*.json` enthält die Rohdaten jeder Messung im Upload-Format
(`settings`, `client`, `trials[]` mit `kind`, `sx/sy`, `tx/ty`, `r`, `hit`, `endT`,
`samples[[ms, x, y]]`, `clicks[]`). `omnivector analyze` wertet sie erneut aus.

## 8. Raw-Accel-Modell (`internal/rawaccel`)

Raw Accel skaliert die x-Komponente mit R (rechts) bzw. L (links) und die y-Komponente mit
U (oben) bzw. D (unten). Daraus folgt der Faktor in Richtung θ:

```
g(θ) = sqrt((a·cos θ)² + (b·sin θ)²),   a ∈ {R, L},  b ∈ {U, D}
```

R, L, U und D werden per Levenberg-Marquardt an ln g_k angepasst. Die **Abdeckung**
= 1 − SS_Rest/SS_gesamt sagt, wie viel der Richtungsunterschiede Raw Accel abbilden kann.
Rein diagonale Muster sind damit nur teilweise darstellbar.

Umrechnung in die Raw-Accel-Felder:

```
Sensitivity/Output DPI ×R    Y/X ×(D/R)    L/R ×(L/R)    U/D ×(U/D)
```

Die vorhandene `settings.json` wird reihenfolgetreu eingelesen. Felder werden per Präfix erkannt
(`Output DPI`/`Sensitivity multiplier`, `Y/X`, `L/R`, `U/D`), also für alte und neue Versionen. Nur
diese Werte werden multipliziert; vorher legt OmniVector eine Sicherungskopie an. Der Verlauf in
`rawaccel-history.json` verhindert, dass wiederholte Exporte sich aufmultiplizieren.
