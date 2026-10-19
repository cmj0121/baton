# Baton

> Ein erweiterbarer, agentenfreundlicher Terminal-Multiplexer.

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md) · [한국어](README.ko.md) ·
[Français](README.fr.md) · **Deutsch** · [Español](README.es.md)

Mehrere AI-Coding-Agents gleichzeitig am Laufen? Das wird schnell unübersichtlich — Fenster, die du jonglieren musst,
über Tabs verstreute Sessions, und keine einzige Stelle, an der du siehst, wer arbeitet, wer feststeckt und wer auf dich
wartet. Baton ist für AI-Agents das, was tmux für Shells ist: **ein einziges, tastaturgesteuertes Cockpit**, ein
Live-Dashboard aller Agents, gruppiert nach den Aufgaben, zu denen sie gehören — jeder nur einen Tastendruck entfernt. Du
hältst den Taktstock. Die Agents spielen. Du dirigierst. 🎼

![Baton-Cockpit-Demo — die Tastenliste, gestartete Panels, der geöffnete Conductor, zwei zu einem Work Item gruppiert und dasselbe ? noch einmal im Split und im Zoom](assets/baton-demo.png)

_`?` zeigt die Tasten der Ansicht, in der du gerade stehst: Panels starten, `n C` ruft den Conductor, `g g` und dann
`g c` gruppieren zwei zu einem Work Item — und `?` im Split, `C-t ?` im Zoom zeigen drei verschiedene Tabellen._

## Erste Schritte

Baton ist eine einzelne, statisch gelinkte Binärdatei: `brew install cmj0121/tap/baton` unter macOS, oder unter Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

… oder `go install github.com/cmj0121/baton/cmd/baton@latest` mit [Go](https://go.dev) 1.26+. Dann `baton` ausführen, und:

1. **`A`** startet einen Agent (du wählst ein Arbeitsverzeichnis; `enter` landet hier, `w` isoliert auf einem Branch).
2. **`enter`** zoomt hinein, um ihm bei der Arbeit zuzusehen; **`C-t d`** bringt dich zurück zum Dashboard.
3. **`q`** koppelt dich ab — alles läuft weiter. Komm jederzeit mit `baton` zurück.

Verirrt? **`?`** zeigt die Tasten für die Stelle, an der du gerade bist. Für eine ganze Flotte öffnet **`n C`** einen
Conductor, der deinen stehenden Befehlen in `$HOME/.baton/CONDUCTOR.md` folgt — siehe **[docs/CONTROL.md](CONTROL.md)**.

## Konzept

Ein **Panel** ist ein Live-Terminal, in dem ein Agent-CLI oder eine Shell läuft. Ein **Work Item** gruppiert die Panels
einer Aufgabe. Eine **Task** ist ein Auftrag, den du an einen Agent schickst, eingereiht, bis er frei ist. Der
**Conductor** ist ein Agent, der den Rest der Flotte für dich steuert.

Du siehst sie über drei Ansichten: das **Dashboard** (jedes Panel als Karte oder in einem Live-Baum), eine **Gruppe**
(die Panels eines Work Items nebeneinander streamend) und einen **Zoom** (ein Panel als dein einziges Terminal, mit dem
Leader `C-t` steigst du aus). Dahinter besitzt ein headless Server jedes Terminal über einen Unix-Socket, sodass du dich
abkoppeln und wieder verbinden kannst, ohne etwas zu verlieren. Siehe **[docs/SPEC.md](SPEC.md)**.

## Warum Baton

tmux gibt dir Fenster und hat keine Ahnung, was in ihnen steckt. Baton setzt voraus, dass in jedem Pane ein Agent sitzt,
und baut darauf auf:

- **Du siehst, wer dich braucht.** Jedes Panel trägt einen Live-Zustand, und die, die auf einen Menschen warten, stehen
  in einem einzigen Posteingang an, statt durch Abklappern der Fenster gefunden zu werden.
- **Arbeit bleibt gruppiert.** Panels, die einer Aufgabe dienen, bilden ein Work Item, mit zwei Tasten angelegt und als
  eines gesteuert.
- **Du verteilst Arbeit, keine Tastendrücke.** Schick einen Auftrag an einen Agent oder eine ganze Gruppe, oder lass
  einen Conductor die Flotte führen.
- **Die Flotte hat Grenzen.** Caps halten den gesamten Prozessbaum eines entlaufenen Builds, und der Verbrauch wird bis
  auf das Panel zurückverfolgt, das ihn verursacht hat.
- **Die Flotte erinnert sich.** Was Agents immer wieder lernen, wird gerankt und dem nächsten Auftrag mitgegeben.

Baton will deine Shells nicht — lass es in tmux laufen, wenn du dort zu Hause bist.

## Funktionen

- **Aufmerksamkeit** — Panels eingestuft als `running`, `idle`, `done` oder `stuck`; `C-t a` ist der Posteingang.
  [ATTENTION](ATTENTION.md)
- **Conductor** — `n C` steuert die Flotte über `baton ctl` oder `baton mcp`, eingezäunt gegen seinen Host.
  [CONTROL](CONTROL.md)
- **Tasks** — `T` schickt einen Auftrag an einen Agent oder ein Work Item; `Q` ist ein Rückstau, den ein Scheduler auf
  freie Agents abfließen lässt.
- **Score** — was Agents immer wieder lernen, steigt eine Leiter hinauf und führt jeden Auftrag an, abgelegt in
  `score.md`. [SCORE](SCORE.md)
- **Caps** — CPU-, Speicher- und Prozessgrenzen über den gesamten Prozessbaum eines Panels. [LIMITS](LIMITS.md)
- **Verbrauch** — `v u` zeigt Tokens, Kosten und Quota-Balken; `v U` zeigt, welche Panels sie verbrauchen.
  [USAGE](USAGE.md)
- **Isolation** — `isolate: docker` lässt die Panels eines Profils in einem Container auf deinem Worktree laufen.
  [ISOLATION](ISOLATION.md)
- **Flotten-Grep** — `/` durchsucht die Ausgabe aller Panels; `C-t [` scrollt und kopiert über OSC 52, auch über SSH.
- **Backends** — `claude`, `codex`, `gemini`, `aider`, `opencode` und `grok` werden erkannt; eigene kommen unter
  `panel.agents`.
- **Fernzugriff** — `baton --remote` hängt sich über dein ssh an eine Flotte auf einer anderen Maschine.
  [REMOTE](REMOTE.md)
- **Plugins** — eine Lua-Datei, `$HOME/.baton/plug-in.lua`, hängt sich an Events und fügt Befehle hinzu.
  [PLUGIN](PLUGIN.md)
- **Das Übliche** — Diff (`D`), ein Git-Menü (`C-t G`), Signale (`s`), Panel-Logs, serielle Ports, eine
  Restart-Policy, Hot Reload (`C-t R`), Themes und Split-Layouts.

## Wichtige Tasten

Auf dem Dashboard ist jede Aktion eine Taste; im Zoom steuern deine Tastendrücke das Programm, also stell den Leader
`C-t` voran.

| Wo         | Taste         | Wirkung                                                              |
| ---------- | ------------- | -------------------------------------------------------------------- |
| Dashboard  | `jk` / `↑↓`   | den Cursor bewegen                                                   |
|            | `enter`       | die Auswahl öffnen / hineinzoomen                                    |
|            | `A`           | neuer Agent — Arbeitsverzeichnis, dann Enter hier oder `w` isolieren |
|            | `n C`         | den Conductor öffnen (ein Agent, der die Flotte steuert)             |
|            | `g g` / `g c` | markieren / markierte Panels gruppieren                              |
|            | `T` / `Q`     | eine Task vergeben / die Warteschlange verwalten                     |
|            | `w` / `x x`   | die Auswahl schließen / Beendete entfernen                           |
| Nach `C-t` | `d` / `b`     | zum Dashboard springen / eine Ebene zurück                           |
|            | `a`           | Attention-Posteingang — erledigen, was einen Menschen braucht        |
|            | `q`           | abkoppeln (der Server läuft weiter)                                  |

Alles Weitere, mit `C-t k` neu belegbar, steht in **[docs/KEYS.md](KEYS.md)**.

## Dokumentation

- **Benutzung** — [SPEC](SPEC.md) · [KEYS](KEYS.md) · [ATTENTION](ATTENTION.md) · [TUI](TUI.md) · [GIT](GIT.md) ·
  [LOGGING](LOGGING.md)
- **Flotte** — [CONTROL](CONTROL.md) · [SCORE](SCORE.md) · [USAGE](USAGE.md) · [LIMITS](LIMITS.md) ·
  [ISOLATION](ISOLATION.md) · [RESTART](RESTART.md)
- **Erweitern** — [PLUGIN](PLUGIN.md) · [REMOTE](REMOTE.md) · [SERIAL](SERIAL.md)
- **Interna** — [DAEMON](DAEMON.md)

## DDD (Dream-Driven Development)

Dieses Projekt folgt DDD (Dream-Driven Development): Jedes Feature entsteht aus dem, was ich mir erträume und brauche.
