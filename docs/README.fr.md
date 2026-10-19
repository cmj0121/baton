# Baton

> Un multiplexeur de terminal extensible et pensé pour les agents.

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md) · [한국어](README.ko.md) ·
**Français** · [Deutsch](README.de.md) · [Español](README.es.md)

Faire tourner plusieurs agents de code IA en même temps devient vite le chaos — des fenêtres à jongler, des sessions
éparpillées dans des onglets, et aucun endroit unique pour voir qui travaille, qui est bloqué et qui vous attend. Baton
est aux agents IA ce que tmux est aux shells : **un cockpit unique, piloté au clavier**, un tableau de bord en direct de
chaque agent, regroupé selon les tâches auxquelles ils appartiennent, chacun à une frappe de distance. Vous tenez la
baguette. Les agents jouent. Vous dirigez. 🎼

![Démo du cockpit Baton — la liste des touches, des panneaux ouverts, le conductor appelé, deux panneaux regroupés en élément de travail, et le même ? pressé de nouveau dans la vue divisée et dans le zoom](assets/baton-demo.png)

_`?` liste les touches de l'endroit où vous êtes : des panneaux s'ouvrent, `n C` appelle le conductor, `g g` puis `g c`
regroupent deux panneaux en élément de travail — et `?` dans la vue divisée, `C-t ?` dans le zoom, donnent trois
tableaux différents._

## Démarrer

Baton est un unique binaire statique : `brew install cmj0121/tap/baton` sur macOS, ou sur Linux :

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

…ou `go install github.com/cmj0121/baton/cmd/baton@latest` avec [Go](https://go.dev) 1.26+. Lancez ensuite `baton`, et :

1. **`A`** lance un agent (choisissez un répertoire de travail ; `enter` le pose ici, `w` l'isole sur une branche).
2. **`enter`** zoome pour le regarder travailler ; **`C-t d`** vous ramène au tableau de bord.
3. **`q`** vous détache — tout continue de tourner. Revenez quand vous voulez avec `baton`.

Perdu ? **`?`** affiche les touches de l'endroit où vous vous trouvez. Pour toute une flotte, **`n C`** ouvre un
conductor qui suit vos ordres permanents dans `$HOME/.baton/CONDUCTOR.md` — voir **[docs/CONTROL.md](CONTROL.md)**.

## Concepts

Un **panneau** (panel) est un terminal en direct qui exécute un CLI d'agent ou un shell. Un **élément de travail** (work
item) regroupe les panneaux d'une même tâche. Une **tâche** (task) est une consigne que vous confiez à un agent, mise en
file jusqu'à ce qu'il soit libre. Le **conductor** est un agent qui pilote le reste de la flotte à votre place.

Vous les voyez à travers trois vues : le **tableau de bord** (chaque panneau en cartes ou en arbre vivant), un **groupe**
(les panneaux d'un élément de travail diffusés côte à côte) et un **zoom** (un seul panneau comme unique terminal, le
préfixe `C-t` pour en sortir). Derrière elles, un serveur headless détient chaque terminal via une socket Unix, pour que
vous puissiez vous détacher et vous rattacher sans rien perdre. Voir **[docs/SPEC.md](SPEC.md)**.

## Pourquoi Baton

tmux vous donne des fenêtres et ignore ce qu'elles contiennent. Baton part du principe que chaque panneau contient un
agent, et tout le reste en découle :

- **Vous voyez qui a besoin de vous.** Chaque panneau porte un état vivant, et ceux qui attendent un humain se rangent
  dans une seule boîte de réception au lieu d'être découverts en parcourant les fenêtres.
- **Le travail reste groupé.** Les panneaux d'une même tâche forment un élément de travail, créé en deux touches et
  piloté comme un tout.
- **Vous distribuez du travail, pas des frappes.** Envoyez une consigne à un agent ou à tout un groupe, ou laissez un
  conductor piloter la flotte.
- **La flotte est bornée.** Des plafonds tiennent tout l'arbre de processus d'une compilation folle, et la consommation
  est rattachée au panneau qui l'a dépensée.
- **La flotte se souvient.** Ce que les agents ne cessent d'apprendre est classé et transmis à la consigne suivante.

Baton ne veut pas de vos shells — lancez-le dans tmux si c'est là que vous vivez.

## Fonctionnalités

- **Attention** — panneaux classés `running`, `idle`, `done` ou `stuck` ; `C-t a` est la boîte de réception.
  [ATTENTION](ATTENTION.md)
- **Conductor** — `n C` pilote la flotte via `baton ctl` ou `baton mcp`, clôturé face à son hôte.
  [CONTROL](CONTROL.md)
- **Tâches** — `T` envoie une consigne à un agent ou à un élément de travail ; `Q` est une file qu'un ordonnanceur
  déverse sur les agents libres.
- **Score** — ce que les agents ne cessent d'apprendre gravit une échelle et ouvre chaque consigne, conservé dans
  `score.md`. [SCORE](SCORE.md)
- **Plafonds** — limites CPU, mémoire et processus tenues sur tout l'arbre de processus d'un panneau.
  [LIMITS](LIMITS.md)
- **Usage** — `v u` affiche tokens, coût et barres de quota ; `v U` montre quels panneaux les consomment.
  [USAGE](USAGE.md)
- **Isolation** — `isolate: docker` fait tourner les panneaux d'un profil dans un conteneur sur votre worktree.
  [ISOLATION](ISOLATION.md)
- **Grep de la flotte** — `/` cherche dans la sortie de chaque panneau ; `C-t [` fait défiler et copie via OSC 52, même
  à travers SSH.
- **Backends** — `claude`, `codex`, `gemini`, `aider`, `opencode` et `grok` détectés ; ajoutez les vôtres sous
  `panel.agents`.
- **Accès à distance** — `baton --remote` se rattache à une flotte sur une autre machine par votre ssh.
  [REMOTE](REMOTE.md)
- **Plugins** — un seul fichier Lua, `$HOME/.baton/plug-in.lua`, réagit aux événements et ajoute des commandes.
  [PLUGIN](PLUGIN.md)
- **L'habituel** — diff (`D`), un menu git (`C-t G`), signaux (`s`), journaux de panneau, ports série, une politique de
  redémarrage, rechargement à chaud (`C-t R`), thèmes et dispositions divisées.

## Touches essentielles

Sur le tableau de bord, chaque action tient en une touche ; en zoom, vos frappes pilotent le programme, donc préfixez
avec `C-t`.

| Où              | Touche        | Action                                                   |
| --------------- | ------------- | -------------------------------------------------------- |
| Tableau de bord | `jk` / `↑↓`   | déplacer le curseur                                      |
|                 | `enter`       | ouvrir / zoomer la sélection                             |
|                 | `A`           | nouvel agent — répertoire, puis entrée ici ou `w` isoler |
|                 | `n C`         | ouvrir le conductor (un agent qui pilote la flotte)      |
|                 | `g g` / `g c` | marquer / grouper les panneaux marqués                   |
|                 | `T` / `Q`     | assigner une tâche / gérer la file                       |
|                 | `w` / `x x`   | fermer la sélection / purger les panneaux terminés       |
| Après `C-t`     | `d` / `b`     | aller au tableau de bord / revenir d'un niveau           |
|                 | `a`           | la boîte d'attention — traiter ce qui réclame un humain  |
|                 | `q`           | se détacher (le serveur continue de tourner)             |

Tout le reste, reconfigurable avec `C-t k`, se trouve dans **[docs/KEYS.md](KEYS.md)**.

## Documentation

- **Utiliser** — [SPEC](SPEC.md) · [KEYS](KEYS.md) · [ATTENTION](ATTENTION.md) · [TUI](TUI.md) · [GIT](GIT.md) ·
  [LOGGING](LOGGING.md)
- **Flotte** — [CONTROL](CONTROL.md) · [SCORE](SCORE.md) · [USAGE](USAGE.md) · [LIMITS](LIMITS.md) ·
  [ISOLATION](ISOLATION.md) · [RESTART](RESTART.md)
- **Étendre** — [PLUGIN](PLUGIN.md) · [REMOTE](REMOTE.md) · [SERIAL](SERIAL.md)
- **Fonctionnement interne** — [DAEMON](DAEMON.md)

## DDD (Dream-Driven Development)

Ce projet suit le DDD (dream-driven development, développement piloté par le rêve) : chaque fonctionnalité naît de ce
dont je rêve et de ce dont j'ai besoin.
