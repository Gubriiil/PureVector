# OmniVector – das PureVector-Prinzip für jede Maus

**OmniVector** passt die Mausempfindlichkeit **je Bewegungsrichtung** an deine Hand an – so wie Corsairs
*PureVector* (SABRE v3 PRO PureVector, Oktober 2026). Es funktioniert aber mit **jeder** Maus, die am
PC hängt: kabelgebunden oder kabellos, jeder Hersteller, mehrere Mäuse gleichzeitig.

> Unabhängiges Projekt. Nicht mit Corsair oder Aimlabs verbunden. „PureVector“ und „SABRE“ sind
> Marken von Corsair. Corsair beschreibt PureVector als patentiert. OmniVector ist eine eigenständige
> Neuentwicklung auf Basis öffentlicher Informationen und motorikwissenschaftlicher Literatur. Der Code
> von Corsair, Aimlabs oder aus einer Firmware wurde weder verwendet noch eingesehen. Ob Patente die
> Nutzung in deinem Land betreffen, musst du selbst prüfen (keine Rechtsberatung).

## Was PureVector macht – und was OmniVector daraus macht

Viele Spieler zielen nicht in alle Richtungen gleich genau. Nach rechts oben überschießt die schnelle
Zielbewegung („Flick“) zum Beispiel um 6 %, nach links oben bleibt sie 9 % zu kurz. Ursache ist vor allem
die Mechanik von Arm und Handgelenk. Das Muster ist individuell, für eine Person aber stabil. PureVector
misst das in einer kurzen Aimlabs-Übung und speichert je Richtung einen Sensitivitätsfaktor in der
Maus-Firmware.

| Schritt | Corsair PureVector | OmniVector |
|---|---|---|
| Messen | Aimlabs „Spidershot“, 5 × 60 s | gleiche Übung im Browser (Rohdaten der Maus, eigene DPI, cm/360° und FOV) |
| Berechnen | 8 Richtungen, Algorithmus nicht öffentlich | Primärbewegungen erkennen, robuste Regression, Schrumpfung, Signifikanztest (8 oder 16 Richtungen) |
| Anpassen | „schwächere Richtungen verhalten sich wie die stärkste“ | gleich (Standard); wahlweise „Mittelwert“ oder „Absolut“, Stärke stufenlos |
| Speichern | Firmware der SABRE v3 PRO PureVector | Treiberebene des PCs: **jede Maus**, eigenes Profil je Maus möglich |
| Verfeinern | „Refine calibration“ | Messung mit aktivem Profil, Ergebnis wird verrechnet |

Wie PureVector auseinandergenommen wurde und warum das Verfahren so aufgebaut ist:
[docs/PUREVECTOR_ANALYSE.md](docs/PUREVECTOR_ANALYSE.md). Formeln, Parameter und Validierung:
[docs/ALGORITHMUS.md](docs/ALGORITHMUS.md).

## Funktionen

- **Omnikompatibel:** wirkt unterhalb aller Programme, also auf Desktop, Raw Input und Spiele.
  - Windows: über den Interception-Filtertreiber.
  - Linux: über evdev/uinput.
- **Kalibrierung im Browser** nach dem Spidershot-Muster. Die Messung nutzt rohe Mausdaten
  (`unadjustedMovement`), bei Chrome/Edge in voller Abtastrate (`pointerrawupdate`).
- **Statistisch abgesichert:**
  - Die Analyse zeigt Konfidenzintervalle und einen Signifikanztest.
  - Robuste Schätzung: Ausreißer verzerren nichts.
  - Ohne messbare Richtungsunterschiede bleibt das Profil neutral.
- **Profil je Maus:** zum Beispiel Hauptmaus kalibriert, Touchpad „neutral“.
- **Live-Ansicht:** zeigt, welche Richtung du gerade bewegst und welcher Faktor wirkt.
- **Ein/Aus-Tastenkürzel** (Windows, Standard `Strg+Alt+F10`) mit Ton als Bestätigung.
- **Raw-Accel-Export** für Spiele, deren Anti-Cheat Interception sperrt. Raw Accel nutzt einen
  signierten Treiber und kann Rechts/Links/Oben/Unten abbilden.
- **Sicher:**
  - Fällt OmniVector aus oder wird beendet, verhält sich die Maus sofort wieder original.
  - Die Oberfläche ist nur lokal erreichbar (127.0.0.1) und gegen fremde Webseiten abgesichert.
- **Schnell:** rund 60 ns Rechenzeit pro Mausbericht, unabhängig von der Abtastrate (125–8000 Hz).

## Installation

### Windows

1. **Interception installieren** (einmalig):
   [github.com/oblitum/Interception/releases](https://github.com/oblitum/Interception/releases) →
   `Interception.zip` entpacken. Dann eine Eingabeaufforderung **als Administrator** öffnen und ausführen:
   ```
   "command line installer\install-interception.exe" /install
   ```
   Danach Windows **neu starten**.
2. **OmniVector besorgen:** `omnivector.exe` aus den
   [GitHub-Actions-Artefakten](https://github.com/Gubriiil/PureVector/actions) laden oder selbst bauen
   (siehe unten).
3. `library\x64\interception.dll` aus dem Interception-Download **neben `omnivector.exe`** legen.
4. `omnivector.exe` starten. Der Browser öffnet die Oberfläche unter http://127.0.0.1:47823.

> **Anti-Cheat-Hinweis:** Nach Berichten lassen sich Spiele mit Vanguard (Valorant) oder FACEIT nicht
> starten, solange der Interception-Treiber installiert ist. Andere Anti-Cheats können ihn ebenfalls
> melden. Für solche Spiele:
> `omnivector.exe --backend none` (nur Kalibrierung), Profil per **Raw-Accel-Export** übertragen und
> Interception deinstallieren (`install-interception.exe /uninstall`, Neustart). Prüfe die Regeln deines
> Spiels bzw. Turniers.

Autostart: Verknüpfung zu `omnivector.exe --no-browser` in den Ordner `shell:startup` legen.

### Linux

```bash
go build -o omnivector ./cmd/omnivector       # Go ≥ 1.24
sudo modprobe uinput
sudo ./omnivector --no-browser                # dann http://127.0.0.1:47823 öffnen
```

Ohne root: Benutzer zur Gruppe `input` hinzufügen und die udev-Regel installieren. Achtung: Die Gruppe
`input` darf alle Eingabegeräte lesen.

```bash
sudo cp packaging/linux/99-omnivector-uinput.rules /etc/udev/rules.d/
echo uinput | sudo tee /etc/modules-load.d/uinput.conf
sudo usermod -aG input "$USER"                 # danach neu anmelden
```

OmniVector übernimmt jede Maus exklusiv und gibt ihre Ereignisse über ein virtuelles Gerät mit gleichem
Namen und gleichen IDs weiter. Desktop-Einstellungen und hwdb-Korrekturen gelten deshalb weiter.

## Kalibrieren

1. Im Dashboard **„Kalibrierung starten“** klicken (Chrome oder Edge empfohlen).
2. Mit DPI, cm/360° und FOV aus deinem Spiel stimmen die Handwege wie im Spiel. Wer die Werte nicht
   kennt, lässt 800 DPI und 40 cm/360°.
3. **5 Runden à 60 s:** Ziele abwechselnd außen (zufällige Richtung) und in der Mitte abschießen.
   Spiele schnell und natürlich wie im Spiel, nicht übervorsichtig.
4. Ergebnis ansehen:
   - Wo landen deine Flicks je Richtung? Jeder Punkt ist ein Schuss.
   - Welcher Faktor gleicht das aus?
   - Wie sicher ist das Ergebnis?
5. Bezug und Stärke wählen und **„Speichern & aktivieren“** klicken, für alle Mäuse oder nur für die
   gemessene Maus.
6. Nach ein paar Tagen Spielen **„Verfeinern / prüfen“** wählen. Die Messung läuft dann mit dem aktiven
   Profil und korrigiert nur die verbleibende Abweichung.

Bezugsrichtung:

- **Stärkste Richtung** (Standard, wie PureVector): Alle Richtungen landen künftig wie deine genaueste.
- **Mittelwert:** gleicht nur die Unterschiede aus, die Gesamtsensitivität bleibt exakt gleich.
- **Absolut:** korrigiert jede Richtung exakt aufs Ziel (experimentell, siehe Analyse-Dokument).

## Befehle

```
omnivector                     Dienst + Oberfläche starten (Doppelklick unter Windows)
  --backend auto|interception|evdev|none   --listen 127.0.0.1:47823   --no-browser   --config VERZ
omnivector calibrate           Kalibrierung im Browser öffnen
omnivector status              Zustand, Backend und Mäuse anzeigen
omnivector on | off | toggle   ein-/ausschalten (z. B. auf eine Taste legen)
omnivector devices             erkannte Mäuse mit Kennung (VID/PID)
omnivector profiles            Profile mit Faktoren je Richtung
omnivector use PROFIL          Standardprofil setzen
omnivector rawaccel --profile PROFIL --settings C:\…\rawaccel\settings.json [--dry-run]
omnivector analyze sessions\DATEI.json [--reference mean] [--strength 0.8] [--json]
omnivector simulate [--seed 1] [--bias "-0.03,0.04,0,0.05,45"] [--save sitzung.json]
```

Konfiguration, Profile und alle Messungen liegen unter `%AppData%\OmniVector` (Windows) bzw.
`~/.config/OmniVector` (Linux): `config.json`, `profiles/*.json`, `sessions/*.json`.

## Raw Accel statt Interception

Mit [Raw Accel](https://github.com/RawAccelOfficial/rawaccel) hast du vier Richtungsfaktoren:
*Sensitivity/Output DPI*, *Y/X*, *L/R* (links) und *U/D* (oben). Die Diagonalen ergeben sich daraus.
OmniVector passt diese vier Faktoren bestmöglich an dein Profil an und zeigt, welcher Anteil der
Richtungsunterschiede damit abgebildet wird.

```
omnivector rawaccel --profile "Kalibrierung 2026-10-29 15.30" --settings "C:\Tools\rawaccel\settings.json"
```

Was der Befehl macht:

- Er multipliziert nur diese vier Werte. Alles andere bleibt unverändert, auch deine
  Beschleunigungskurve.
- Vorher legt er eine Sicherungskopie an.
- Ein früherer OmniVector-Export wird ersetzt, nicht aufmultipliziert.

Danach Raw Accel öffnen und „Apply“ klicken.

## Selbst bauen und testen

```bash
go test ./...                                   # Go-Tests (Mathematik, Analyse, Simulation, Backends, Server)
node --test web/test/*.test.mjs                 # Tests der Kalibrier-Logik im Browser-Code
GOOS=windows GOARCH=amd64 go build -o omnivector.exe ./cmd/omnivector
go build -o omnivector ./cmd/omnivector
./omnivector simulate                           # virtuellen Spieler kalibrieren und Wirkung prüfen
```

Keine externen Abhängigkeiten außer der Go-Standardbibliothek. Die Oberfläche ist eingebettet und
lädt nichts aus dem Internet.

## Ehrliche Einordnung

- Corsair nennt bis zu 33 % (81 ms) schnelleres Zielen. Das sind Herstellerangaben. Erste Reviews
  (ProSettings, KitGuru) fanden im eigenen Test keinen eindeutigen Vorteil bzw. sehen den Nutzen stark
  vom Spieler abhängig. Ob dir eine Richtungskorrektur hilft, hängt davon ab, wie ausgeprägt deine
  Richtungsunterschiede sind. Die Analyse sagt dir das mit Signifikanzangabe.
- Die mitgelieferte Simulation belegt, dass Messung, Analyse und Treiberkorrektur zusammen eine
  vorhandene Richtungsabweichung zuverlässig erkennen und zu rund 80 % beseitigen. Sie belegt nicht,
  dass Menschen dadurch besser treffen. Das zeigt nur deine eigene Prüfmessung im Spiel.
- Direktionale Sensitivität verändert nichts am Spiel und liest keine Spieldaten. Für Corsair ist sie
  deshalb vergleichbar mit Mausbeschleunigung. Ob Eingabe-Software in deinem Spiel oder Turnier erlaubt
  ist, entscheiden dessen Regeln.
