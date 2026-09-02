# Baton

> Un multiplexeur de terminal extensible et pensé pour les agents.

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md) · [한국어](README.ko.md) ·
**Français** · [Deutsch](README.de.md) · [Español](README.es.md)

Vous faites tourner plusieurs agents de code IA en même temps ? Ça devient vite le chaos — des fenêtres à jongler, des
sessions éparpillées dans des onglets, et aucun endroit unique pour voir qui travaille, qui est bloqué et qui vous attend.

Baton est aux agents IA ce que tmux est aux shells. Il vous offre **un cockpit unique, piloté au clavier** : un tableau de
bord en direct de chaque agent, regroupé selon les tâches auxquelles ils appartiennent, chacun à une frappe de distance.

Vous tenez la baguette. Les agents jouent. Vous dirigez. 🎼

![Démo du cockpit Baton — la liste des touches, des panneaux ouverts, le conductor appelé, deux panneaux regroupés en élément de travail, et le même ? pressé de nouveau dans la vue divisée et dans le zoom](assets/baton-demo.png)

_Une seule touche fait le tour : `?` liste les touches de l'endroit où vous êtes. Des panneaux s'ouvrent, `n C` appelle
le conductor, `g g` puis `g c` regroupent deux panneaux en élément de travail — et `?` dans la vue divisée, `C-t ?` dans
le zoom, donnent trois tableaux différents._

_Clip généré à partir de [`baton-demo.tape`](assets/baton-demo.tape) ; le CLI d'agent du conductor est une doublure
([`demo-agent.sh`](assets/demo-agent.sh)) afin que le clip s'enregistre à l'identique sur n'importe quelle machine, et la
flotte qu'il pilote via la socket est bien réelle._

## Démarrer

Baton est un unique binaire statique. Sur macOS, installez-le avec [Homebrew](https://brew.sh) :

```sh
brew install cmj0121/tap/baton
```

Sur Linux, une seule ligne suffit :

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

…ou, sur n'importe quelle plateforme, récupérez-le avec [Go](https://go.dev) 1.26+ :

```sh
go install github.com/cmj0121/baton/cmd/baton@latest
```

…ou compilez-le depuis un clone avec `make install`. Ensuite, lancez simplement :

```sh
baton
```

Baton démarre son serveur en arrière-plan et vous dépose sur le **tableau de bord** — votre camp de base. Votre première
minute :

1. Appuyez sur **`A`** pour lancer un agent (vous choisirez un répertoire de travail pour lui).
2. Appuyez sur **`enter`** pour zoomer et le regarder travailler ; **`C-t d`** vous ramène au tableau de bord.
3. Appuyez sur **`q`** pour vous détacher et partir — tout continue de tourner. Revenez quand vous voulez avec `baton`.

Perdu ? **`?`** affiche toujours les touches de l'endroit où vous vous trouvez.

## Pourquoi pas simplement tmux ?

Parce que tmux ignore ce qu'il y a dans le panneau. Il vous donne des fenêtres ; c'est à vous de retenir laquelle est
laquelle, et vous découvrez qu'un agent vous attendait en les parcourant une à une. Baton part du principe que le panneau
contient un agent, et tout le reste en découle :

| Ce que vous faites                 | tmux, à la main                        | Baton                                                                                              |
| ---------------------------------- | -------------------------------------- | -------------------------------------------------------------------------------------------------- |
| Trouver qui vous attend            | parcourir les panneaux et lire         | un état vivant par panneau, et une boîte `C-t a` de ceux qui se sont arrêtés pour un humain        |
| Garder ensemble ce qui va ensemble | nommer les fenêtres, retenir le schéma | des éléments de travail — un groupe nommé de panneaux, en deux touches                             |
| Distribuer le travail              | le taper vous-même dans chaque panneau | envoyer une tâche à un panneau ou à tout un groupe, ou laisser un conductor piloter la flotte      |
| Arrêter une compilation folle      | rien                                   | des plafonds CPU, mémoire et processus, tenus sur tout l'arbre de processus du panneau             |
| Savoir ce que coûte la flotte      | rien                                   | les tokens et le coût de la fenêtre de facturation, et vos barres de quota, rattachés à un panneau |

Baton ne remplace pas tmux et ne veut pas de vos shells — lancez-le dans tmux si c'est là que vous vivez.

## Concept

- **Des agents, pas des shells.** L'unité de travail est un agent en cours d'exécution, pas une fenêtre à surveiller.
- **Un tableau de bord, pas des fenêtres.** Une vue d'ensemble en direct de tout à la fois, pas un tas d'onglets.
- **Un cœur headless, des frontends remplaçables.** Le cerveau est un démon en arrière-plan ; le visage qui l'affiche est
  interchangeable.

| Concept          | Ce que c'est                                                                                                                                        |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Panel**        | Un terminal en direct — un panneau _agent_ (un CLI d'agent) ou un panneau _shell_.                                                                  |
| **Work item**    | Un groupe nommé de panneaux appartenant à une même tâche.                                                                                           |
| **Task**         | Un briefing que vous confiez à un agent — suivi tout au long de son cycle de vie, mis en file et planifié s'il doit attendre.                       |
| **Conductor**    | Un agent qui pilote la flotte à votre place — il ouvre des panneaux, les regroupe et envoie des prompts aux autres via la socket.                   |
| **Global shell** | Un shell hôte simple et unique que le serveur maintient dans `$HOME`, toujours à une frappe de distance — un camp de base, pas un pilote de flotte. |

## Vues

Vous pilotez Baton à travers trois vues, en passant de l'une à l'autre d'une seule frappe :

- **Tableau de bord (Dashboard)** — le centre de contrôle. Une petite flotte est une grille de **cartes**, une par panneau et
  une par élément de travail ; à partir de six choses au premier niveau, elle devient un **arbre** en direct de tous les
  panneaux : un élément de travail par ligne, ses sous-groupes indentés en dessous, ses panneaux en dessous encore. `space`
  affiche ou masque ce qui est imbriqué sous une ligne, à n'importe quelle profondeur ; `→` ouvre un élément de travail puis y
  entre, `←` le referme et remonte d'un cran — et hors du premier niveau, revient aux cartes. La ligne porte l'état, le
  répertoire de travail, la sparkline de sortie et la tâche confiée à mesure que le terminal s'élargit ; `v p` ouvre un volet de
  détail à côté. C'est ici que vous naviguez, ouvrez et fermez des panneaux, et les regroupez en éléments de travail.
- **Groupe (Group)** — la vue divisée en direct d'un élément de travail : ses panneaux côte à côte, tous diffusés en même
  temps. Les premiers défilent en tuiles vivantes ; les autres se replient dans une unique **tuile de résumé** sur laquelle
  vous pouvez zoomer. Épinglez-en quelques-uns pour les garder toujours actifs, pilotez sur place celui qui a le focus avec
  **`i`**, ou faites **`enter`** pour entrer dedans.
- **Zoom** — un seul panneau comme unique terminal. Les frappes vont droit au programme ; le préfixe **`C-t`** est votre
  moyen d'agir ou de revenir en arrière.

## Touches

Les touches sont **modales** : sur le tableau de bord et dans un groupe, chaque action tient en une touche ; en zoom ou en
interaction, vos frappes pilotent le programme, donc une action Baton devient le préfixe **`C-t`** suivi de la touche.
Appuyez sur **`?`** pour la liste complète et reconfigurable de la vue courante, et sur **`C-t k`** pour éditer la table de
touches.
Quatre touches sont des _landings_ : elles n'agissent pas seules et ouvrent une famille — `n` lance, `v` dessine,
`g` groupe, `x` est le double appui qui se confirme lui-même — et la barre d'état annonce ce que chacune accepte ensuite.

| Où              | Touche                | Action                                                                  |
| --------------- | --------------------- | ----------------------------------------------------------------------- |
| Après `C-t`     | `d` / `b`             | aller au tableau de bord / revenir d'un niveau                          |
|                 | `a`                   | la boîte d'attention — traiter ce qui réclame un humain                 |
|                 | `[`                   | entrer en mode défilement                                               |
|                 | `l` / `L`             | journaliser le panneau dans un fichier / relire ce journal              |
|                 | `R` / `S`             | recharger la config / forcer le redémarrage du serveur                  |
|                 | `q`                   | se détacher (le serveur continue de tourner)                            |
| Tableau de bord | `jk` / `↑↓`           | déplacer le curseur                                                     |
|                 | `hl` / `←→`           | changer de carte · dans l'arbre : replier / déplier un élément          |
|                 | `space`               | afficher / masquer ce qui est imbriqué sous la ligne                    |
|                 | `v p` / `v g`         | volet de détail / cycle du groupement : élément, dossier, profil, état  |
|                 | `v l`                 | la mise en page du tableau : cartes ou arbre                            |
|                 | `m`                   | saisir une ligne — les flèches la portent, `enter` la dépose            |
|                 | `enter`               | ouvrir / zoomer la sélection                                            |
|                 | `p` / `A` / `n c`     | nouveau panneau shell / agent / choix de commande                       |
|                 | `n .`                 | nouveau panneau shell dans le répertoire du panneau focalisé            |
|                 | `n C`                 | ouvrir le conductor (un agent qui pilote la flotte)                     |
|                 | `n h`                 | ouvrir le global shell (un shell hôte dans `$HOME`)                     |
|                 | `w` / `x x`           | fermer la sélection / purger les panneaux terminés                      |
|                 | `r`                   | relancer le ou les panneaux terminés sous le focus                      |
|                 | `g g` / `g c` / `g u` | marquer / grouper les panneaux marqués / dégrouper                      |
|                 | `s` / `f` / `D`       | envoyer un signal / rechercher / diff sur la sélection                  |
|                 | `/`                   | chercher dans la sortie de chaque panneau (grep de la flotte)           |
|                 | `T` / `Q`             | assigner une tâche / gérer la file de tâches                            |
|                 | `v u`                 | faire défiler le pied de page d'usage : off / fenêtre / panneau / quota |
|                 | `v U`                 | usage du compte — barres de quota, et qui les consomme                  |
|                 | `v k`                 | afficher/masquer le rappel des touches dans le pied de page             |
| Groupe          | `tab`                 | passer au panneau suivant                                               |
|                 | `+` / `-`             | afficher plus / moins de tuiles vivantes                                |
|                 | `L`                   | faire défiler la disposition des tuiles                                 |
|                 | `p` / `i`             | épingler / interagir avec le panneau focalisé                           |
|                 | `enter`               | zoomer le panneau focalisé                                              |
| Zoom            | taper                 | piloter le programme directement                                        |
|                 | `C-t f` / `C-t G`     | chercher dans l'historique / menu git (agent)                           |

Voir **[docs/KEYS.md](KEYS.md)** pour la référence complète des touches, et **[docs/SPEC.md](SPEC.md)** pour la
conception derrière chaque vue.

## Fonctionnalités

Cinq choses qu'un multiplexeur de terminal ne fait pas :

- **De l'attention, pas de la surveillance** — une flotte va bien la plupart du temps ; si vous regardez l'écran, c'est
  pour les quelques panneaux qui ne vont pas. Une seule horloge silencieuse les classe tous — `running`, `idle` au bout
  de dix secondes, `done` pour un agent qui a fini son tour, `stuck` quand cela dure trop — et un agent peut lever la
  main lui-même, au-dessus de toute l'échelle. `C-t a` ouvre la boîte de réception depuis n'importe quelle vue, et la
  file se vide là ; `settings.notify` envoie une notification de bureau OSC 9 quand personne ne regarde, groupée, et
  jamais pour `done`. Voir **[docs/ATTENTION.md](ATTENTION.md)**.
- **Un conductor** — `n C` ouvre un agent qui pilote la flotte pour vous : il ouvre, regroupe, signale et sollicite les
  autres panneaux via la socket, à travers `baton ctl` ou les outils `baton mcp`, clôturé pour qu'il ne puisse pas
  casser son propre hôte. Son objectif se règle dans `$HOME/.baton/CONDUCTOR.md`. Voir
  **[docs/CONTROL.md](CONTROL.md)**.
- **Des tâches et une file** — `T` envoie une consigne à un agent, ou la diffuse à tout un élément de travail ; elle est
  inscrite sur la carte et remise quand l'agent est prêt. `Q` gère une file persistante qu'un ordonnanceur côté serveur
  déverse sur les agents libres. Un hook Lua `task.pre` peut réécrire ou refuser une consigne ; `task.change` la
  surveille.
- **Des plafonds sur tout l'arbre de processus** — plafonnez le CPU, la mémoire et les processus d'un panneau, et tenez
  tout son arbre de processus à ce plafond, pour qu'une compilation folle n'emporte pas la machine. Un plancher commun à
  la flotte avec des surcharges par agent, appliqué à la flotte en cours par `C-t R`, imposé par cgroup v2 sous Linux —
  et le panneau le dit clairement quand un hôte ne peut pas l'imposer. Voir **[docs/LIMITS.md](LIMITS.md)**.
- **La consommation rattachée à un panneau** — `v u` fait défiler un affichage en pied de page : les tokens et le coût de
  la fenêtre de facturation avec un compte à rebours (`⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31`), la part du panneau ciblé,
  ou les barres de quota du compte (`⊙ 5h ▓▓▓▓▓░░░ 2:14:31 · 7d ▓▓▓░░░░░ 3d4h`). `v U` ouvre le tout — chaque fenêtre de
  quota, le crédit supplémentaire, et les panneaux qui les consomment. Voir **[docs/USAGE.md](USAGE.md)**.

Quatre autres que la plupart n'ont pas non plus :

- **Isolation par conteneur** — au choix, par profil d'agent : avec `isolate: docker`, les panneaux de ce profil tournent
  dans un conteneur avec votre worktree monté. C'est vous qui nommez l'image (Baton n'en fournit aucune) ; `mount`,
  `network`, `env-allow` et `user` décident de ce qui passe en plus, et rien de votre environnement ne passe sans être
  nommé. Désactivé par défaut, et ce n'est pas une frontière face à un agent hostile. Voir
  **[docs/ISOLATION.md](ISOLATION.md)**.
- **Grep sur toute la flotte** — `/` cherche dans la sortie de tous les panneaux à la fois et regroupe les occurrences par
  panneau ; `enter` zoome celui que vous choisissez, posé sur l'occurrence. `C-t f` cherche par expression régulière dans
  un seul historique, et le mode défilement (`C-t [`) sélectionne et copie via OSC 52, donc cela marche à travers SSH
  sans binaire d'appoint.
- **Backends d'agent** — Baton connaît un catalogue de CLI d'agents (`claude`, `codex`, `gemini`, `aider`, `opencode`,
  `grok`) et détecte lesquels sont réellement installés sur la machine où tourne la flotte. `A` lance celui que vous
  choisissez ; `C-t P` fixe le défaut de la flotte et indique où obtenir ceux qui manquent ; `C-t R` redétecte après une
  installation. Ajoutez les vôtres sous `panel.agents`.
- **Accès à distance** — `baton --remote` rattache le même cockpit à une flotte sur une autre machine, par le ssh que
  vous utilisez déjà : aucun port en écoute, pas de TLS, aucun échange de clés propre à Baton. Désactivé par défaut ;
  `C-t @` l'active, forge une passkey qui n'est jamais écrite sur disque, et liste chaque connexion vivante pour
  l'expulser, la renouveler ou tout couper. Voir **[docs/REMOTE.md](REMOTE.md)**.

Et le cockpit qu'on attend d'un multiplexeur, chaque chose à une touche :

| Fonction                 | Touche          | Ce que ça fait                                                                                         |
| ------------------------ | --------------- | ------------------------------------------------------------------------------------------------------ |
| Diff                     | `D`             | le diff de l'arbre de travail du panneau — indexé et non indexé d'un coup, non suivis compris          |
| Git                      | `C-t G`         | diff, log, status, index, commit, push, branches et worktrees — **[docs/GIT.md](GIT.md)**              |
| Signaux                  | `s`             | n'importe quel signal à la sélection, à la tuile ciblée ou à tout le groupe                            |
| Recherche                | `f`             | filtrer la flotte par titre ou par groupe                                                              |
| Disposition de groupe    | `+` `-` `L`     | combien de membres diffusent en direct, et la forme de la division                                     |
| Shell global             | `n h`           | un shell hôte simple tenu par le serveur dans `$HOME`, toujours à une touche                           |
| Répertoire mémorisé      | `n .`           | les panneaux suivent leur répertoire via OSC 7 — **[docs/RESTART.md](RESTART.md)**                     |
| Journal de panneau       | `C-t l` `C-t L` | rediriger la sortie d'un panneau vers un fichier, et la relire — **[docs/LOGGING.md](LOGGING.md)**     |
| Persistance              | `r`             | la flotte survit à un redémarrage en emplacements à relancer depuis leur spec                          |
| Politique de redémarrage | —               | `panel.restart: on-failure` ramène un panneau avec un backoff et une limite                            |
| Rechargement à chaud     | `C-t R`         | la configuration sans redémarrer la flotte — ou un `SIGHUP` au démon                                   |
| Apparence                | —               | thème et grilles de division dans `$HOME/.baton/TUI.yaml` — **[docs/TUI.md](TUI.md)**                  |
| Économiseur d'écran      | —               | une pluie numérique plein écran quand le cockpit se repose — **[docs/TUI.md](TUI.md)**                 |
| Souris                   | —               | désactivée par défaut, pour garder la sélection propre au terminal                                     |
| Langue                   | —               | la liste des touches se lit en anglais ou en chinois traditionnel — **[docs/TUI.md](TUI.md#language)** |

## Architecture

Un **baton server** headless (un démon en arrière-plan) détient tout l'état et chaque terminal. Des frontends
interchangeables s'y rattachent via une unique socket de domaine Unix — les commandes montent, les événements descendent —
pour que vous puissiez vous détacher et vous rattacher sans rien perdre.

Voir **[docs/SPEC.md](SPEC.md)** pour le diagramme complet et le modèle d'interaction.

## Plugins

Un seul fichier Lua (`$HOME/.baton/plug-in.lua`) remodèle Baton selon votre flux de travail : réagir aux événements du
cycle de vie (vous alerter quand un agent a besoin de vous, enchaîner l'étape suivante quand l'un d'eux termine), piloter
la flotte, ajouter vos propres commandes et définir la config — le tout à travers un unique objet `baton`. Voir
**[docs/PLUGIN.md](PLUGIN.md)**.

## Documentation

- **[docs/SPEC.md](SPEC.md)** — la spécification complète : les vues, le cycle de vie d'un panneau, les éléments de
  travail, les signaux, le diff, la persistance, la référence des touches vue par vue et le diagramme d'architecture.
- **[docs/ATTENTION.md](ATTENTION.md)** — l'attention à grande échelle : l'échelle du silence (`done`, `stuck`,
  failed), la boîte `C-t a`, les deux repliements du tableau de bord, les notifications de bureau et tous leurs réglages.
- **[docs/TUI.md](TUI.md)** — le fichier d'apparence du cockpit (`$HOME/.baton/TUI.yaml`) : le thème de couleurs et les
  dispositions de la vue divisée des groupes (préréglages et grilles personnalisées).
- **[docs/LIMITS.md](LIMITS.md)** — les limites de ressources : la config, les deux couches, le rechargement à chaud et où
  elles sont réellement imposées.
- **[docs/ISOLATION.md](ISOLATION.md)** — l'isolation par conteneur : la config par profil, ce que l'agent conserve,
  comment les limites sont imposées dans un conteneur, et ce dont ce n'est pas une frontière.
- **[docs/RESTART.md](RESTART.md)** — la politique de redémarrage : ce qui compte comme un échec et ce qui n'en est
  pas, le backoff et la limite, et pourquoi `always` n'existe pas.
- **[docs/GIT.md](GIT.md)** — le menu git : chaque opération, le flux de l'éditeur de commit, les worktrees et la config.
- **[docs/LOGGING.md](LOGGING.md)** — la journalisation des panneaux : ce qui est écrit, où le fichier atterrit, les
  marqueurs de session, la rotation, et ce dont ce n'est pas une frontière.
- **[docs/REMOTE.md](REMOTE.md)** — l'accès distant en SSH : le pont `--stdio`, ce que la passkey est et n'est pas,
  la liste des connexions de `C-t @`, et les échecs qu'il rapporte.
- **[docs/USAGE.md](USAGE.md)** — le pied de page d'usage du compte : les sources locale et Admin-API, la config et les
  réserves.
- **[docs/PLUGIN.md](PLUGIN.md)** — l'API des plugins Lua : l'objet `baton`, les événements, les commandes et la config.
- **[docs/CONTROL.md](CONTROL.md)** — piloter la flotte par agent : le conductor, le CLI `baton ctl`, les outils
  `baton mcp` et les garde-fous.
- **[docs/SCORE.md](SCORE.md)** — Score, la mémoire de la flotte : le fichier `score.md` et son unique moyen
  d'annulation, l'échelle de paliers, les poids du classement, le compactage, et ce dont il n'est pas une frontière.
- **[docs/DAEMON.md](DAEMON.md)** — le daemon : l'ordre dans lequel il démarre, les sondes de readiness, et quoi faire
  quand `baton` annonce que le serveur n'est pas monté.

## DDD (Dream-Driven Development)

Ce projet suit le DDD (dream-driven development, développement piloté par le rêve) : chaque fonctionnalité naît de ce dont
je rêve et de ce dont j'ai besoin.
