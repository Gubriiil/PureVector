# PureVector – Analyse und Nachbau

Dieses Dokument beschreibt, wie Corsairs PureVector funktioniert (soweit öffentlich bekannt bzw.
ableitbar) und wie OmniVector jedes Element nachbildet, und zwar so, dass es mit jeder Maus funktioniert.

## 1. Methode

PureVector ist an Corsairs Maus-Firmware und an die Aimlabs-Software gebunden; beides ist proprietär.
Ein Firmware-Dump oder das Dekompilieren fremder Software war weder möglich noch nötig. Die Analyse
ist ein **Black-Box-Nachbau**:

1. das öffentlich dokumentierte Verhalten sammeln (Ablauf, Ein- und Ausgaben, Angaben des Herstellers,
   unabhängige Reviews),
2. daraus das zugrunde liegende Modell rekonstruieren und mit der motorikwissenschaftlichen Literatur
   abgleichen,
3. jedes Element neu und herstellerunabhängig implementieren,
4. die Kette mit einem simulierten Spieler, dessen Fehler bekannt ist, auf Korrektheit prüfen.

Es wurde kein Code von Corsair oder Aimlabs verwendet. Corsair gibt an, der Algorithmus sei patentiert.
Eine konkrete Patentschrift ließ sich in den öffentlichen Quellen nicht finden.

## 2. Öffentlich bekannte Fakten (Stand: Ende Oktober 2026)

| Thema | Angabe | Quelle |
|---|---|---|
| Vorstellung | 20.10.2026, Mausserie SABRE v3 PRO PureVector (6 Modelle), entwickelt mit Aimlabs | [1], [3] |
| Ablauf | „Measure – Calculate – Adjust – Save“ | [2] |
| Messung | Kalibrierung in Aimlabs, ca. 5 Minuten: 5 Runden *Spidershot* à 1 Minute | [2], [4] |
| Richtungen | individuelle Sensitivität in **8 Richtungen** | [5] |
| Kenngrößen | je Richtung u. a. zurückgelegte Strecke und Genauigkeit; Über- bzw. Unterschießen; Darstellung als Netzdiagramm gegen eine Referenz „perfekt getroffen“ | [2], [4] |
| Korrektur | Die Maus ändert die Umsetzung Hand → Cursor, damit „schwächere Richtungen sich wie die stärkste verhalten“ | [2] |
| Speicherung | „Directional DPI map“ im Onboard-Speicher; keine Hintergrundsoftware; nichts passt sich in Echtzeit an | [2], [6] |
| DPI | Es wird eine DPI-Stufe kalibriert; die anderen Stufen werden danach deaktiviert | [2] |
| Nachjustieren | „Refine calibration“ und Zurücksetzen; „After“-Diagramm nach mindestens 5 weiteren Aimlabs-Runden | [2] |
| Herstellerangaben | bis zu 81 ms bzw. 33 % schnellere Ziel- und Korrekturphase; Richtungsstreuung im Mittel −18 %; „patentierter Algorithmus“, „über 4 Milliarden Datensätze“ | [1], [3] |
| Fairness | liest keine Spieldaten, vergleichbar mit Mausbeschleunigung | [4] |
| Reviews | kein eindeutig messbarer Vorteil im eigenen Test bzw. Nutzen stark vom Spieler abhängig; wenig Transparenz nach der Kalibrierung | [5], [7] |

## 3. Rekonstruiertes Modell

### 3.1 Was gemessen wird

Spidershot lässt Ziele abwechselnd in der Mitte und an zufälligen Positionen erscheinen. Jede
Zielerfassung besteht aus

- einer schnellen, weitgehend vorprogrammierten **Primärbewegung** („Flick“) und
- einer oder mehreren **Korrekturbewegungen**, bis das Ziel getroffen ist
  (Zwei-Komponenten-Modell nach Woodworth; Submovement-Analyse nach Meyer et al. [L1], Elliott et al. [L3]).

Corsair zielt ausdrücklich auf die „aiming and correction phase“. Wie lang die Korrekturphase dauert,
hängt direkt davon ab, wie weit die Primärbewegung vom Ziel entfernt endet. Die entscheidende Messgröße
ist deshalb das **Landeverhältnis der Primärbewegung je Richtung**:

```
r(θ) = (Weg der Primärbewegung entlang der Zielrichtung) / (Zielentfernung)
r = 1,06  → 6 % überschossen      r = 0,91 → 9 % zu kurz
```

### 3.2 Warum die Richtung eine Rolle spielt

Bei schnellen Armbewegungen in der Ebene treten **systematische, richtungsabhängige Weitenfehler** auf.
Ihre Hauptursache ist die **Trägheitsanisotropie** von Arm und Hand: In Richtungen mit geringer
effektiver Trägheit fällt dieselbe Bewegungsplanung weiter aus (Gordon, Ghilardi, Cooper & Ghez [L2]).

- Dazu kommen Unterschiede zwischen Beugen und Strecken des Handgelenks, die Fingerarbeit und die
  Mausgriff-Haltung.
- Das Muster ist individuell, aber stabil. Genau das berichtet Aimlabs.
- Es hat typischerweise zwei Anteile:
  - eine „Ellipse“ (2. Harmonische, z. B. diagonal zu weit),
  - eine einseitige Asymmetrie (1. Harmonische, z. B. nach rechts zu weit, nach links zu kurz).

### 3.3 Die Korrektur

Wenn eine Richtung systematisch um den Faktor r(θ) daneben landet, gleicht ein Sensitivitätsfaktor

```
g(θ) = r_Bezug / r(θ)
```

diese Abweichung aus. Die Hand macht dieselbe Bewegung wie bisher, der Cursor landet aber wie in der
Bezugsrichtung. Das entspricht Corsairs Beschreibung („die Hand ändert sich nicht, nur die Umsetzung“).

PureVector legt g für 8 Richtungen ab und muss für Zwischenrichtungen interpolieren. Weil die
Korrektur in der Firmware wirkt, also bevor das Betriebssystem die Daten sieht, gilt sie in jedem Spiel.

### 3.4 Warum „wie die stärkste Richtung“ und nicht „exakt aufs Ziel“

Menschen **unterschießen bei Primärbewegungen absichtlich leicht**. Eine Korrektur in derselben
Richtung ist billiger als eine Umkehr nach dem Überschießen („optimal undershoot“, Elliott et al. [L3]).
Würde man jede Richtung auf r = 1 trimmen, stiegen Gesamtsensitivität und Überschießquote.
PureVector gleicht deshalb relativ aus, nämlich auf die beste Richtung. OmniVector übernimmt das als
Standard. Zusätzlich gibt es „Mittelwert“ (Gesamtsensitivität bleibt exakt gleich) und „Absolut“
(experimentell).

### 3.5 Firmware vs. PC

Eine Firmware-Lösung funktioniert nur mit der eigenen Maus. Den gleichen Effekt für **jede** Maus
erreicht man, wenn die Korrektur an der Stelle im Eingabepfad des PCs sitzt, an der noch alle
Programme dieselben Daten sehen: **unterhalb** von Raw Input.

| Plattform | Einhängepunkt | Wirkung |
|---|---|---|
| Windows | Interception-Filtertreiber an der Mausklasse (`mouclass`) | Desktop, Raw Input, DirectInput und Spiele sehen die korrigierten Daten, als kämen sie von der Maus |
| Linux | evdev-Gerät exklusiv übernehmen (`EVIOCGRAB`), über uinput-Klon weitergeben | X11, Wayland, libinput, SDL und Wine sehen nur den Klon |
| Anti-Cheat-Spiele | Raw Accel (signierter Treiber) | nur das Vier-Richtungs-Modell (rechts/links/oben/unten) |

Ein Low-Level-Hook (`WH_MOUSE_LL`) plus `SendInput` reicht dafür nicht: Spiele lesen Raw Input und
würden die Originalbewegung zusätzlich sehen. Viele Anti-Cheats ignorieren außerdem injizierte
Eingaben.

## 4. Abbildung PureVector → OmniVector

| PureVector | OmniVector | Code |
|---|---|---|
| Aimlabs Spidershot, 5 × 60 s | Browser-Übung im gleichen Muster, geschichtete Zufallsrichtungen (16 Sektoren), Zielweiten 8–28° log-gleichverteilt, Rohdaten über Pointer Lock `unadjustedMovement`, 1-ms-Aufzeichnung | `web/js/session.js`, `web/js/calibrate.js` |
| „Calculate“ | Primärbewegung erkennen → robuste Regression je Richtung (Distanz und Rückweg als Störgrößen) → Random-Effects-Schrumpfung → Wald-Test | `internal/calib` |
| „Adjust“ (8 Richtungen) | g(θ) für 8 oder 16 Stützrichtungen, periodische Catmull-Rom-Interpolation im Log-Raum | `internal/vector/gain.go` |
| Anwendung in der Firmware | Treiberebene (Interception / evdev), Richtungsschätzung mit Abstandsgedächtnis, Restwert-Übertrag | `internal/vector/tracker.go`, `internal/backend` |
| Onboard-Speicher | Profil je Maus (VID/PID) oder als Standard für alle | `internal/profile` |
| Refine / After-Diagramm | „Verfeinern / prüfen“: Messung mit aktivem Profil, Ergebnis wird mit dem Basisprofil multipliziert | `internal/engine`, `internal/server` |
| Netzdiagramm | Netzdiagramme für Messung (mit allen Einzelschüssen) und Korrektur, Live-Ansicht | `web/js/radar.js` |

## 5. Was anders ist (und warum)

- **Jede Maus, mehrere Profile.** Das Profil hängt an der Geräte-ID statt an der Firmware. Eine
  zweite Maus oder ein Touchpad kann neutral bleiben.
- **Transparenz.** Für jede Richtung siehst du Schusszahl, Konfidenzintervall und die
  Signifikanz der Unterschiede. Reviews bemängeln genau diese fehlende Transparenz bei PureVector.
- **Keine Überkorrektur durch Zufall.** Corsair stützt sich nach eigener Aussage auf Milliarden
  Datensätze, vermutlich als Vorwissen über typische Muster. OmniVector schätzt dieses Vorwissen aus
  deinen eigenen Daten (empirischer Bayes / DerSimonian-Laird, [L5]). Gibt es keine echten
  Unterschiede, bleibt das Profil neutral. In der Simulation weicht kein Faktor bei einem Spieler ohne
  Richtungsfehler um mehr als 2 % ab.
- **Abtastrate egal.** Die Richtung wird aus einem gleitenden, wegbasierten Gedächtnis geschätzt.
  Deshalb wirkt der Faktor bei 125 Hz und 8000 Hz gleich, auch wenn ein langsamer 45°-Zug als (1,0),
  (0,1), (1,0) … ankommt.
- **DPI.** Die Faktoren sind Verhältnisse und unabhängig von der DPI. Nach einer Änderung von DPI oder
  Spielsensitivität ist eine neue Messung trotzdem sinnvoll, weil sich die Handwege ändern.

## 6. Offene Punkte

- Wie PureVector die „stärkste“ Richtung genau bestimmt, ist nicht veröffentlicht. OmniVector nimmt
  die Richtung, deren geschrumpftes Landeverhältnis am nächsten bei 1 liegt.
- Die genaue Interpolation und die Richtungsschätzung in der Firmware sind unbekannt. Beide Lösungen
  in OmniVector sind glatt und unabhängig von der Abtastrate.
- Wofür die „4 Milliarden Datensätze“ genau dienen, ist offen (Vorwissen, Parameterwahl, Validierung).
- Ob eine Richtungskorrektur Menschen messbar schneller treffen lässt, ist unabhängig noch nicht belegt
  (siehe Reviews). OmniVector misst deshalb nach und zeigt, ob deine Unterschiede signifikant sind.

## 7. Validierung des Nachbaus

Ein simulierter Spieler (`internal/sim`) zielt wie ein Mensch:

- Reaktionszeit,
- Primärbewegung mit minimalem Ruck (Flash & Hogan [L4]), Dauer nach Fitts,
- **bekannter** richtungsabhängiger Weitenfehler plus Streuung (8 % Weite, 3° Richtung),
- visuell geführte Korrekturbewegungen.

Die Bewegung wird mit 125–8000 Hz abgetastet, auf ganze Counts gerundet, optional durch den echten
OmniVector-Transformator geschickt und wie im Browser aufgezeichnet.

| Prüfung (feste Startwerte, 360 Ziele ≈ 4–5 min) | Ergebnis |
|---|---|
| Spieler ohne Richtungsfehler | alle Faktoren innerhalb ±2 % von 1,00 |
| Wiedergefundener Fehlerverlauf (RMSE, log) | ≈ 0,013–0,017 bei 8 % Einzelschuss-Streuung |
| Wahre Richtungsabweichung nach Korrektur | 4,5 % → 0,6–1,1 % RMS (≈ 80 % beseitigt) |
| Prüfmessung mit aktivem Profil im Treiber | gemessene Streuung 4,7 % → 1,1 %; Verfeinern verschlechtert nichts |
| Abtastrate 125 / 500 / 1000 / 8000 Hz | gleiche Ergebnisse |
| Transformator | Gesamtfaktor je Richtung auf ±0,4 % genau, waagerechte Bewegung bleibt exakt waagerecht |

Diese Tests laufen bei jedem `go test ./...` mit. Sie belegen, dass die Kette korrekt misst und
korrigiert, nicht aber, dass ein Mensch dadurch besser trifft.

## Quellen

1. Corsair, Pressemitteilung „CORSAIR Changes the Game with PureVector …“ (20.10.2026) –
   https://www.corsair.com/newsroom/press-release/corsair-changes-the-game-with-purevector-a-science-based-personalized-aiming-technology-launching-exclusively-in-the-sabre-v3-pro-purevector-mouse-series
2. Corsair, „How to Use PureVector“ – https://www.corsair.com/us/en/explorer/gamer/mice/how-to-use-purevector/
   sowie „PureVector: The Future of Aiming“ – https://www.corsair.com/us/en/explorer/gamer/mice/purevector-the-future-of-aiming/
3. Yahoo Finance / Business Wire, „CORSAIR Unleashes PureVector, with Testing Showing up to a 33% Reduction in Aiming and Correction Time“ –
   https://finance.yahoo.com/technology/articles/corsair-unleashes-purevector-testing-showing-130000648.html
4. „What is PureVector? Corsair and Aimlabs know where you miss …“ – https://kaytomas.com/news/purevector-explained
5. ProSettings, „Corsair Sabre V3 Pro Review“ – https://prosettings.net/reviews/corsair-sabre-v3-pro/
6. ByteTech Lab, „Corsair Sabre V3 Pro PureVector Review: Aimlabs Directional DPI Tech …“ –
   https://bytetechlab.com/bt-lab-test/blog/2026/gaming-hardware/corsair-sabre-v3-pro-purevector-review-aimlabs-directional-dpi-tech-37g-ultralight-esports-mouse/
7. KitGuru, „Corsair Sabre V3 Pro PureVector CF Mouse Review“ –
   https://www.kitguru.net/tech-news/featured-tech-reviews/carlosribeiro/corsair-sabre-v3-pro-purevector-cf-mouse-review/
8. TweakTown, „Corsair's Sabre V3 PRO mouse lineup features brand-new PureVector tech …“ –
   https://www.tweaktown.com/news/114222/corsairs-sabre-v3-pro-mouse-lineup-features-brand-new-purevector-tech-that-improves-aiming-and-accuracy/index.html

Literatur:

- [L1] Meyer, D. E., Abrams, R. A., Kornblum, S., Wright, C. E., & Smith, J. E. K. (1988). Optimality in human motor performance: Ideal control of rapid aimed movements. *Psychological Review, 95*(3), 340–370.
- [L2] Gordon, J., Ghilardi, M. F., Cooper, S. E., & Ghez, C. (1994). Accuracy of planar reaching movements. II. Systematic extent errors resulting from inertial anisotropy. *Experimental Brain Research, 99*(1), 112–130.
- [L3] Elliott, D., Helsen, W. F., & Chua, R. (2001). A century later: Woodworth's (1899) two-component model of goal-directed aiming. *Psychological Bulletin, 127*(3), 342–357; sowie Elliott, D., Hansen, S., Mendoza, J., & Tremblay, L. (2004). Learning to optimize speed, accuracy, and energy expenditure. *Journal of Motor Behavior, 36*(3), 339–351.
- [L4] Flash, T., & Hogan, N. (1985). The coordination of arm movements: An experimentally confirmed mathematical model. *Journal of Neuroscience, 5*(7), 1688–1703.
- [L5] DerSimonian, R., & Laird, N. (1986). Meta-analysis in clinical trials. *Controlled Clinical Trials, 7*(3), 177–188.
- [L6] Huber, P. J. (1964). Robust estimation of a location parameter. *Annals of Mathematical Statistics, 35*(1), 73–101.
