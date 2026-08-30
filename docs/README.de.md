# Baton

> Ein erweiterbarer, agentenfreundlicher Terminal-Multiplexer.

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md) · [한국어](README.ko.md) ·
[Français](README.fr.md) · **Deutsch** · [Español](README.es.md)

Mehrere AI-Coding-Agents gleichzeitig am Laufen? Das wird schnell unübersichtlich — Fenster, die du jonglieren musst,
über Tabs verstreute Sessions, und keine einzige Stelle, an der du siehst, wer arbeitet, wer feststeckt und wer auf dich wartet.

Baton ist für AI-Agents das, was tmux für Shells ist. Es gibt dir **ein einziges, tastaturgesteuertes Cockpit**: ein
Live-Dashboard aller Agents, gruppiert nach den Aufgaben, zu denen sie gehören — jeder nur einen Tastendruck entfernt.

Du hältst den Taktstock. Die Agents spielen. Du dirigierst. 🎼

![Baton-Cockpit-Demo — die Tastenliste, gestartete Panels, der geöffnete Conductor, zwei zu einem Work Item gruppiert und dasselbe ? noch einmal im Split und im Zoom](assets/baton-demo.png)

_Eine Taste macht die ganze Runde: `?` zeigt die Tasten der Ansicht, in der du gerade stehst. Panels starten, `n C`
ruft den Conductor, `g g` und dann `g c` gruppieren zwei zu einem Work Item — und `?` im Split, `C-t ?` im Zoom sind
drei verschiedene Tabellen._

_Der Clip wurde aus [`baton-demo.tape`](assets/baton-demo.tape) erzeugt; das Agent-CLI des Conductors ist ein
Platzhalter ([`demo-agent.sh`](assets/demo-agent.sh)), damit der Clip auf jeder Maschine gleich aufgezeichnet wird, und
die Flotte, die er über den Socket steuert, ist echt._

## Erste Schritte

Baton ist eine einzelne, statisch gelinkte Binärdatei. Unter macOS holst du sie dir mit [Homebrew](https://brew.sh):

```sh
brew install cmj0121/tap/baton
```

Unter Linux genügt eine Zeile:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

… oder, auf jeder Plattform, holst du sie dir mit [Go](https://go.dev) 1.26+:

```sh
go install github.com/cmj0121/baton/cmd/baton@latest
```

… oder du baust sie aus einem Clone mit `make install`. Dann einfach ausführen:

```sh
baton
```

Baton startet seinen Hintergrund-Server und setzt dich auf dem **Dashboard** ab — deiner Heimatbasis. Deine erste Minute:

1. Drücke **`A`**, um einen Agent zu starten (du wählst dabei ein Arbeitsverzeichnis für ihn aus).
2. Drücke **`enter`**, um hineinzuzoomen und ihm bei der Arbeit zuzusehen; **`C-t d`** bringt dich zurück zum Dashboard.
3. Drücke **`q`**, um dich abzukoppeln und wegzugehen — alles läuft weiter. Komm jederzeit mit `baton` zurück.

Verirrt? **`?`** zeigt dir immer die Tasten für die Stelle, an der du gerade bist.

## Warum nicht einfach tmux?

Weil tmux nicht weiß, was im Pane steckt. Es gibt dir Fenster; welches welches ist, musst du dir selbst merken, und dass
ein Agent auf dich gewartet hat, merkst du erst, wenn du sie der Reihe nach durchgehst. Baton setzt voraus, dass im Pane
ein Agent sitzt, und alles Weitere folgt daraus:

| Was du gerade tust               | tmux, von Hand                  | Baton                                                                                                |
| -------------------------------- | ------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Finden, wer dich braucht         | die Panes durchgehen und lesen  | ein lebendiger Zustand je Panel und ein `C-t a`-Posteingang mit denen, die auf einen Menschen warten |
| Zusammenhängendes zusammenhalten | Fenster benennen, Schema merken | Work Items — eine benannte Gruppe von Panels, mit zwei Tasten                                        |
| Arbeit verteilen                 | in jedes Pane selbst tippen     | eine Aufgabe an eines oder eine ganze Gruppe schicken, oder ein Conductor-Agent steuert die Flotte   |
| Einen entlaufenen Build stoppen  | nichts                          | CPU-, Speicher- und Prozessgrenzen über den gesamten Prozessbaum des Panels                          |
| Wissen, was die Flotte kostet    | nichts                          | Tokens und Kosten des Abrechnungsfensters und deine Quota-Balken, einem Panel zuordenbar             |

Baton ist kein Ersatz für tmux und will deine Shells nicht — lass es in tmux laufen, wenn du dort zu Hause bist.

## Konzept

- **Agents, keine Shells.** Die Arbeitseinheit ist ein laufender Agent, kein Fenster, das du beaufsichtigen musst.
- **Dashboard, keine Fenster.** Eine Live-Übersicht über alles auf einmal, statt eines Stapels von Tabs.
- **Headless-Kern, austauschbare Frontends.** Das Gehirn ist ein Hintergrund-Daemon; das Gesicht, das es darstellt, ist austauschbar.

| Konzept          | Was es ist                                                                                                                                      |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| **Panel**        | Ein Live-Terminal — ein _agent_-Panel (ein Agent-CLI) oder ein _shell_-Panel.                                                                   |
| **Work item**    | Eine benannte Gruppe von Panels, die zu einer Aufgabe gehören.                                                                                  |
| **Task**         | Ein Auftrag, den du an einen Agent schickst — über seinen Lebenszyklus verfolgt, eingereiht und eingeplant, wenn er warten muss.                |
| **Conductor**    | Ein Agent, der die Flotte für dich steuert — er startet, gruppiert und promptet die anderen Panels über den Socket.                             |
| **Global shell** | Eine einzelne, schlichte Host-Shell, die der Server in `$HOME` hält, immer einen Tastendruck entfernt — eine Heimatbasis, kein Flottensteuerer. |

## Ansichten

Du steuerst Baton über drei Ansichten und wechselst mit einem Tastendruck zwischen ihnen:

- **Dashboard** — die Einsatzzentrale. Eine kleine Flotte ist ein Raster aus **Karten**, je eine pro Panel und pro Work Item;
  ab sechs Dingen auf oberster Ebene wird daraus ein Live-**Baum** aller Panels: ein Work Item pro Zeile, seine Untergruppen
  darunter eingerückt, seine Panels darunter. `space` zeigt oder verbirgt in jeder Tiefe, was unter einer Zeile verschachtelt
  ist, `→` öffnet ein Work Item und steigt hinein, `←` schließt es und geht eine Ebene zurück — und aus der obersten Ebene
  heraus zurück zu den Karten. Die Zeile trägt Status, Arbeitsverzeichnis, Ausgabe-Sparkline und die zugewiesene Aufgabe,
  sobald das Terminal breit genug ist; `v p` blendet daneben eine Detailspalte ein. Hier navigierst du, startest und schließt
  Panels und gruppierst sie zu Work Items.
- **Gruppe (Group)** — die Live-Aufteilung eines Work Items: seine Panels nebeneinander gekachelt, alle gleichzeitig
  streamend. Die ersten paar streamen als Live-Kacheln; der Rest klappt in eine einzelne **Zusammenfassungs-Kachel**
  zusammen, in die du hineinzoomen kannst. Pinne ein paar an, damit sie immer laufen, steuere die fokussierte an Ort und
  Stelle mit **`i`**, oder springe mit **`enter`** hinein.
- **Zoom** — ein Panel als dein einziges Terminal. Tastendrücke gehen direkt an das Programm; über den Leader **`C-t`**
  handelst du oder steigst wieder aus.

## Tasten

Die Tasten sind **modal**: auf dem Dashboard und in einer Gruppe ist jede Aktion eine einzelne Taste; im Zoom oder beim
Interagieren steuern deine Tastendrücke das Programm, eine Baton-Aktion ist also der Leader **`C-t`** und dann die
Taste. Drücke **`?`** für die vollständige, neu belegbare Liste der aktuellen Ansicht und **`C-t k`**, um die
Tastenbelegung zu bearbeiten.
Vier Tasten sind _Landings_: Sie tun allein nichts und öffnen eine Familie — `n` startet, `v` zeichnet, `g` gruppiert,
`x` ist der Doppeltipp, der sich selbst bestätigt — und die Statuszeile nennt, was jede als Nächstes annimmt.

| Where       | Taste                 | Wirkung                                                             |
| ----------- | --------------------- | ------------------------------------------------------------------- |
| After `C-t` | `d` / `b`             | zum Dashboard springen / eine Ebene zurück                          |
|             | `a`                   | Attention-Posteingang — erledigen, was einen Menschen braucht       |
|             | `[`                   | in den Scroll-Modus wechseln                                        |
|             | `l` / `L`             | Panel in eine Datei protokollieren / dieses Protokoll lesen         |
|             | `R` / `S`             | Konfiguration neu laden / Server-Neustart erzwingen                 |
|             | `q`                   | abkoppeln (der Server läuft weiter)                                 |
| Dashboard   | `jk` / `↑↓`           | den Cursor bewegen                                                  |
|             | `hl` / `←→`           | eine Karte weiter · im Baum: Work Item ein-/ausklappen              |
|             | `space`               | zeigen / verbergen, was unter der Zeile verschachtelt ist           |
|             | `v p` / `v g`         | Detailspalte / Gruppierung: Work Item, Verzeichnis, Profil, Status  |
|             | `v l`                 | das Dashboard-Layout: Karten oder Baum                              |
|             | `m`                   | eine Zeile aufnehmen — Pfeile tragen sie, `enter` legt sie ab       |
|             | `enter`               | die Auswahl öffnen / hineinzoomen                                   |
|             | `p` / `A` / `n c`     | neues shell- / agent- / Befehlsauswahl-Panel                        |
|             | `n .`                 | neues Shell-Panel im Verzeichnis des fokussierten Panels            |
|             | `n C`                 | den Conductor öffnen (ein Agent, der die Flotte steuert)            |
|             | `n h`                 | die Global Shell öffnen (eine Host-Shell in `$HOME`)                |
|             | `w` / `x x`           | die Auswahl schließen / Beendete entfernen                          |
|             | `r`                   | die beendeten Panels unter dem Fokus erneut ausführen               |
|             | `g g` / `g c` / `g u` | markieren / markierte Panels gruppieren / Gruppierung aufheben      |
|             | `s` / `f` / `D`       | der Auswahl ein Signal senden / sie finden / diffen                 |
|             | `/`                   | die Ausgabe jedes Panels durchsuchen (die Flotte greppen)           |
|             | `T` / `Q`             | eine Task vergeben / die Task-Warteschlange verwalten               |
|             | `v u`                 | Nutzungs-Fußzeile durchschalten: aus / Fenster / Panel / Kontingent |
|             | `v U`                 | Kontonutzung — Kontingentbalken und wer sie verbraucht              |
|             | `v k`                 | die Tastenanzeige in der Fußzeile umschalten                        |
| Group       | `tab`                 | das nächste Panel fokussieren                                       |
|             | `+` / `-`             | mehr / weniger Live-Kacheln zeigen                                  |
|             | `L`                   | das Kachel-Layout durchschalten                                     |
|             | `p` / `i`             | das fokussierte Panel anpinnen / damit interagieren                 |
|             | `enter`               | das fokussierte Panel zoomen                                        |
| Zoom        | tippen                | das Programm direkt steuern                                         |
|             | `C-t f` / `C-t G`     | den Scrollback durchsuchen / Git-Menü (agent)                       |

Die vollständige Tastenreferenz steht in **[docs/KEYS.md](KEYS.md)**, die Gestaltung hinter jeder Ansicht in
**[docs/SPEC.md](SPEC.md)**.

## Funktionen

Fünf Dinge, die ein Terminal-Multiplexer nicht tut:

- **Aufmerksamkeit statt Abklappern** — einer Flotte geht es meistens gut; du schaust auf den Bildschirm wegen der
  wenigen, denen es nicht gut geht. Eine einzige stille Uhr ordnet sie alle — `running`, `idle` nach zehn Sekunden,
  `done` für einen Agent, der seine Runde beendet hat, `stuck`, wenn es zu lange dauert — und ein Agent kann sich selbst
  melden, über die ganze Leiter hinweg. `C-t a` öffnet aus jeder Ansicht den Posteingang, und dort wird die Schlange
  abgearbeitet; `settings.notify` schickt eine OSC-9-Desktopbenachrichtigung, wenn niemand hinsieht, gebündelt und
  niemals für `done`. Siehe **[docs/ATTENTION.md](ATTENTION.md)**.
- **Ein Conductor** — `n C` öffnet einen Agent, der die Flotte für dich steuert: Er startet, gruppiert, signalisiert und
  promptet die anderen Panels über den Socket, per `baton ctl` oder den `baton mcp`-Tools, eingezäunt, damit er seinen
  eigenen Host nicht ruinieren kann. Sein Ziel steht in `$HOME/.baton/CONDUCTOR.md`. Siehe
  **[docs/CONTROL.md](CONTROL.md)**.
- **Aufgaben und ein Rückstau** — `T` gibt einen Auftrag an einen Agent oder verteilt ihn über ein ganzes Work Item; er
  steht auf der Karte und wird zugestellt, sobald der Agent bereit ist. `Q` verwaltet einen dauerhaften Rückstau, den ein
  serverseitiger Scheduler auf freie Agents abfließen lässt. Ein Lua-Hook `task.pre` kann einen Auftrag umschreiben oder
  ablehnen; `task.change` beobachtet ihn.
- **Grenzen über den gesamten Prozessbaum** — begrenze CPU, Speicher und Prozesse eines Panels und halte seinen gesamten
  Prozessbaum daran, damit ein entlaufener Build nicht die Maschine mitnimmt. Ein flottenweiter Boden mit Overrides je
  Agent, mit `C-t R` auf die laufende Flotte angewandt, unter Linux per cgroup v2 durchgesetzt — und das Panel sagt
  deutlich, wenn ein Host das nicht durchsetzen kann. Siehe **[docs/LIMITS.md](LIMITS.md)**.
- **Verbrauch bis aufs Panel** — `v u` blättert durch eine Fußzeile: Tokens und Kosten des Abrechnungsfensters mit
  Countdown (`⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31`), der Anteil des fokussierten Panels oder die Rate-Limit-Balken
  deines Kontos (`⊙ 5h ▓▓▓▓▓░░░ 2:14:31 · 7d ▓▓▓░░░░░ 3d4h`). `v U` öffnet alles — jedes Quota-Fenster, das
  Zusatzguthaben und die Panels, die es verbrauchen. Siehe **[docs/USAGE.md](USAGE.md)**.

Vier weitere, die die meisten auch nicht haben:

- **Container-Isolation** — optional je Agent-Profil: Mit `isolate: docker` laufen die Panels dieses Profils in einem
  Container mit eingehängtem Worktree. Das Image benennst du selbst (Baton liefert keines); `mount`, `network`,
  `env-allow` und `user` entscheiden, was sonst hinübergeht, und aus deiner Umgebung geht nichts hinüber, was du nicht
  benennst. Standardmäßig aus, und keine Grenze gegen einen feindlichen Agent. Siehe
  **[docs/ISOLATION.md](ISOLATION.md)**.
- **Die ganze Flotte greppen** — `/` durchsucht die Ausgabe aller Panels auf einmal und listet die Treffer nach Panel
  gruppiert; `enter` zoomt den gewählten, gelandet auf dem Treffer. `C-t f` durchsucht einen einzelnen Scrollback per
  regulärem Ausdruck, und der Scrollmodus (`C-t [`) markiert und kopiert über OSC 52, funktioniert also über SSH ohne
  Hilfsbinary.
- **Agent-Backends** — Baton kennt einen Katalog von Agent-CLIs (`claude`, `codex`, `gemini`, `aider`, `opencode`,
  `grok`) und erkennt, welche auf der Maschine der Flotte tatsächlich vorhanden sind. `A` startet den gewählten, `C-t P`
  setzt den Flottenstandard und nennt zu den fehlenden, wo es sie gibt, `C-t R` erkennt nach einer Installation neu.
  Eigene kommen unter `panel.agents`.
- **Fernzugriff** — `baton --remote` hängt dasselbe Cockpit an eine Flotte auf einer anderen Maschine, über das ssh, das
  du ohnehin benutzt: kein lauschender Port, kein TLS, kein eigener Schlüsseltausch von Baton. Standardmäßig aus; `C-t @`
  schaltet es ein, prägt einen Passkey, der nie auf die Platte geschrieben wird, und listet jede lebende Verbindung zum
  Rauswerfen, Erneuern oder Abschalten. Siehe **[docs/REMOTE.md](REMOTE.md)**.

Und das Cockpit, das man von einem Multiplexer erwartet, jeweils eine Taste entfernt:

| Funktion              | Taste           | Was sie tut                                                                                               |
| --------------------- | --------------- | --------------------------------------------------------------------------------------------------------- |
| Diff                  | `D`             | der Work-Tree-Diff des Agent-Panels — gestaged und ungestaged zugleich, unversionierte inklusive          |
| Git                   | `C-t G`         | Diff, Log, Status, Stage, Commit, Push, Branch und Worktrees — **[docs/GIT.md](GIT.md)**                  |
| Signale               | `s`             | ein beliebiges Signal an die Auswahl, die fokussierte Kachel oder die ganze Gruppe                        |
| Suchen                | `f`             | die Flotte nach Titel oder Gruppe filtern                                                                 |
| Gruppen-Layouts       | `+` `-` `L`     | wie viele Mitglieder live laufen, und die Form des Splits                                                 |
| Globale Shell         | `n h`           | eine schlichte Host-Shell, die der Server in `$HOME` hält, immer eine Taste entfernt                      |
| Gemerktes Verzeichnis | `n .`           | Panels verfolgen ihr Verzeichnis über OSC 7 — **[docs/RESTART.md](RESTART.md)**                           |
| Panel-Logging         | `C-t l` `C-t L` | die Ausgabe eines Panels in eine Datei leiten und zurücklesen — **[docs/LOGGING.md](LOGGING.md)**         |
| Persistenz            | `r`             | die Flotte übersteht einen Neustart als Slots, die du aus ihrer Spec neu startest                         |
| Restart-Policy        | —               | `panel.restart: on-failure` holt ein Panel mit Backoff und Limit zurück                                   |
| Hot Reload            | `C-t R`         | Konfiguration ohne Neustart der Flotte — oder ein `SIGHUP` an den Daemon                                  |
| Erscheinungsbild      | —               | Theme und eigene Split-Raster in `$HOME/.baton/TUI.yaml` — **[docs/TUI.md](TUI.md)**                      |
| Bildschirmschoner     | —               | ein bildschirmfüllender Datenregen, wenn das Cockpit ruht — **[docs/TUI.md](TUI.md)**                     |
| Maus                  | —               | standardmäßig aus, damit die eigene Auswahl des Terminals bleibt                                          |
| Sprache               | —               | die Tastenliste liest sich auf Englisch oder Traditionell-Chinesisch — **[docs/TUI.md](TUI.md#language)** |

## Architektur

Ein headless **baton server** (ein Hintergrund-Daemon) besitzt den gesamten Zustand und jedes Terminal. Austauschbare
Frontends verbinden sich über einen einzigen Unix-Domain-Socket — Befehle hinauf, Events hinab — sodass du dich
abkoppeln und wieder verbinden kannst, ohne etwas zu verlieren.

Das vollständige Diagramm und das Interaktionsmodell stehen in **[docs/SPEC.md](SPEC.md)**.

## Plugins

Eine einzige Lua-Datei (`$HOME/.baton/plug-in.lua`) formt Baton auf deinen Workflow um: auf Lebenszyklus-Events
reagieren (dich anpingen, wenn ein Agent dich braucht, den nächsten Schritt anhängen, wenn einer fertig ist), die Flotte
steuern, eigene Befehle hinzufügen und Konfiguration setzen — alles über ein einziges `baton`-Objekt.
Siehe **[docs/PLUGIN.md](PLUGIN.md)**.

## Dokumentation

- **[docs/SPEC.md](SPEC.md)** — die vollständige Spezifikation: Ansichten, der Panel-Lebenszyklus, Work Items, Signale,
  Diff, Persistenz, die Tastenreferenz pro Ansicht und das Architekturdiagramm.
- **[docs/ATTENTION.md](ATTENTION.md)** — Aufmerksamkeit im großen Maßstab: die Stille-Leiter (`done`, `stuck`,
  failed), der `C-t a`-Posteingang, die beiden Faltungen des Dashboards, Desktop-Benachrichtigungen und alle
  Stellschrauben dazu.
- **[docs/TUI.md](TUI.md)** — die Datei für das Erscheinungsbild des Cockpits (`$HOME/.baton/TUI.yaml`): das Farb-Theme
  und die Layouts der Gruppenaufteilung (Presets und eigene Raster).
- **[docs/LIMITS.md](LIMITS.md)** — Ressourcenlimits: die Konfiguration, die zwei Ebenen, das Hot Reload und wo sie
  tatsächlich durchgesetzt werden.
- **[docs/ISOLATION.md](ISOLATION.md)** — Container-Isolation: die Konfiguration pro Profil, was der Agent behält, wie
  die Limits im Container durchgesetzt werden, und wogegen sie keine Grenze ist.
- **[docs/RESTART.md](RESTART.md)** — die Neustart-Richtlinie: was als Fehler zählt und was nicht, Backoff und Limit,
  und warum es kein `always` gibt.
- **[docs/GIT.md](GIT.md)** — das Git-Menü: jede Operation, der Ablauf im Commit-Editor, Worktrees und die Konfiguration.
- **[docs/LOGGING.md](LOGGING.md)** — die Panel-Protokollierung: was geschrieben wird, wo die Datei landet, die
  Sitzungsmarker, die Rotation, und wofür sie keine Grenze ist.
- **[docs/REMOTE.md](REMOTE.md)** — der Fernzugriff über SSH: die `--stdio`-Brücke, was die Passkey ist und was
  nicht, die Verbindungsliste von `C-t @`, und die Fehler, die er meldet.
- **[docs/USAGE.md](USAGE.md)** — die Fußzeile zur Kontonutzung: die lokale Quelle und die Admin-API-Quelle, die
  Konfiguration und die Vorbehalte.
- **[docs/PLUGIN.md](PLUGIN.md)** — die Lua-Plugin-API: das `baton`-Objekt, Events, Befehle und Konfiguration.
- **[docs/CONTROL.md](CONTROL.md)** — die Flotte per Agent steuern: der Conductor, das `baton ctl`-CLI, die
  `baton mcp`-Tools und die Leitplanken.

## DDD (Dream-Driven Development)

Dieses Projekt folgt DDD (Dream-Driven Development): Jedes Feature entsteht aus dem, was ich mir erträume und brauche.
