# Baton

> Un multiplexeur de terminal extensible et pensé pour les agents.

[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md) · [한국어](README.ko.md) ·
**Français** · [Deutsch](README.de.md) · [Español](README.es.md)

Vous faites tourner plusieurs agents de code IA en même temps ? Ça devient vite le chaos — des fenêtres à jongler, des
sessions éparpillées dans des onglets, et aucun endroit unique pour voir qui travaille, qui est bloqué et qui vous attend.

Baton est aux agents IA ce que tmux est aux shells. Il vous offre **un cockpit unique, piloté au clavier** : un tableau de
bord en direct de chaque agent, regroupé selon les tâches auxquelles ils appartiennent, chacun à une frappe de distance.

Vous tenez la baguette. Les agents jouent. Vous dirigez. 🎼

![Démo du cockpit Baton — des panneaux sur un tableau de bord, zoom pour en piloter un, regroupement en élément de travail, ouverture du conductor et du global shell](assets/baton-demo.png)

_Ouvrez des panneaux, zoomez sur l'un d'eux pour le piloter, regroupez-en deux en un élément de travail, appelez le
conductor avec `C` et le global shell avec `H` — et `?` est toujours là pour les touches._

_Clip généré à partir de [`baton-demo.tape`](assets/baton-demo.tape) — les étapes de régénération sont dans l'en-tête du
fichier tape. Le CLI d'agent du conductor est une doublure ([`demo-agent.sh`](assets/demo-agent.sh)) afin que le clip
s'enregistre à l'identique sur n'importe quelle machine ; la flotte qu'il pilote via la socket, elle, est bien réelle._

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

- **Tableau de bord (Dashboard)** — le centre de contrôle. Une grille en direct (un arbre dès que ça se remplit) de chaque
  panneau avec son statut et un aperçu. C'est ici que vous naviguez, ouvrez et fermez des panneaux, et les regroupez en
  éléments de travail.
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

| Où              | Touche            | Action                                                          |
| --------------- | ----------------- | --------------------------------------------------------------- |
| Après `C-t`     | `d` / `b`         | aller au tableau de bord / revenir d'un niveau                  |
|                 | `[`               | entrer en mode défilement                                       |
|                 | `R` / `S`         | recharger la config / forcer le redémarrage du serveur          |
|                 | `q`               | se détacher (le serveur continue de tourner)                    |
| Tableau de bord | `hjkl` / flèches  | déplacer le curseur                                             |
|                 | `enter`           | ouvrir / zoomer la sélection                                    |
|                 | `p` / `A` / `c`   | nouveau panneau shell / agent / choix de commande               |
|                 | `C`               | ouvrir le conductor (un agent qui pilote la flotte)             |
|                 | `H`               | ouvrir le global shell (un shell hôte dans `$HOME`)             |
|                 | `w` / `x`         | fermer la sélection / purger les panneaux terminés              |
|                 | `r`               | relancer le ou les panneaux terminés sous le focus              |
|                 | `g` / `G` / `u`   | marquer / grouper les panneaux marqués / dégrouper              |
|                 | `s` / `f` / `D`   | envoyer un signal / rechercher / diff sur la sélection          |
|                 | `/`               | chercher dans la sortie de chaque panneau (grep de la flotte)   |
|                 | `T` / `Q`         | assigner une tâche / gérer la file de tâches                    |
|                 | `U`               | faire défiler le pied de page d'usage : off / fenêtre / panneau |
|                 | `K`               | afficher/masquer le rappel des touches dans le pied de page     |
| Groupe          | `tab`             | passer au panneau suivant                                       |
|                 | `+` / `-`         | afficher plus / moins de tuiles vivantes                        |
|                 | `L`               | faire défiler la disposition des tuiles                         |
|                 | `p` / `i`         | épingler / interagir avec le panneau focalisé                   |
|                 | `enter`           | zoomer le panneau focalisé                                      |
| Zoom            | taper             | piloter le programme directement                                |
|                 | `C-t f` / `C-t g` | chercher dans l'historique / menu git (agent)                   |

Voir **[docs/SPEC.md](SPEC.md)** pour la référence complète des touches vue par vue et la conception derrière chaque vue.

## Fonctionnalités

Tout ce dont vous avez besoin pour mener une flotte, à une frappe de distance :

- **Signaux** — `s` envoie n'importe quel signal à la sélection, à la tuile focalisée ou au groupe entier ; le sélecteur
  liste les plus courants, `o` permet de saisir n'importe quel nom ou numéro.
- **Rechercher, chercher, copier** — `f` filtre la flotte par titre ou par groupe ; `/` grep la sortie de tous les panneaux
  d'un coup et liste les correspondances regroupées par panneau — `enter` zoome celle que vous choisissez, positionnée sur
  la correspondance ; `C-t f` fait une recherche regex dans l'historique d'un panneau ; le mode défilement (`C-t [`)
  sélectionne et copie via OSC52, donc ça marche par SSH sans binaire auxiliaire.
- **Diff** — `D` (ou `C-t D` en zoom) fait apparaître le diff de l'arbre de travail du panneau agent — indexé et non indexé
  d'un seul coup, fichiers non suivis compris — dans une superposition maître-détail.
- **Git** — `C-t g` ouvre un menu git sur l'agent zoomé : diff, log, status, indexation, commit, push, branches et
  worktrees. Voir **[docs/GIT.md](GIT.md)**.
- **Conductor et contrôle** — `C` ouvre un conductor : un agent qui pilote la flotte à votre place. Il ouvre des panneaux,
  les regroupe, envoie des signaux et des prompts aux autres panneaux via la socket — à travers `baton ctl` ou les outils
  `baton mcp` — avec des garde-fous pour qu'il ne puisse pas saccager son propre hôte. Définissez son objectif dans
  `$HOME/.baton/CONDUCTOR.md`. Voir **[docs/CONTROL.md](CONTROL.md)**.
- **Global shell** — `H` ouvre le global shell : un unique shell hôte simple que le serveur maintient dans `$HOME`,
  toujours à une frappe de distance. Comme le conductor, c'est une marque dans l'en-tête FLEET plutôt qu'une carte, et le
  serveur n'en garde qu'un — il survit à un redémarrage sous la forme d'un emplacement terminé que vous relancez avec `r`.
  Contrairement au conductor, il ne pilote rien : pas de rôle restreint, pas d'espace de travail géré. (À distinguer du
  shell **scratch** flottant `C-t ~`, qui est éphémère et disparaît au détachement.)
- **Tâches et file d'attente** — `T` confie un briefing à un agent (ou le diffuse à tout un élément de travail), consigné
  sur la carte et livré quand l'agent est prêt. `Q` gère un backlog persistant qu'un ordonnanceur intégré au serveur écoule
  vers les agents libres — le flux **vous → conductor → flotte**. Un hook Lua `task.pre` peut réécrire ou refuser un
  briefing ; `task.change` le surveille.
- **Groupes et résumé** — `+` / `-` règlent combien de membres sont diffusés en tuiles vivantes ; les autres se replient
  dans une unique tuile de résumé. Les membres épinglés sont toujours diffusés. `L` fait défiler la **disposition** de la
  vue divisée — la grille uniforme, `main-vertical`, `main-horizontal`, `stack`, ou vos propres grilles définies dans
  `TUI.yaml`.
- **Limites de ressources** — plafonnez ce qu'un panneau peut consommer — CPU, mémoire, processus — et appliquez-le à
  **tout son arbre de processus**, pour qu'une compilation qui déraille n'emporte pas la machine avec elle. Définissez un
  plancher pour toute la flotte et des surcharges par agent dans la config ou sous `C-t P` ; `C-t R` les applique à la
  flotte en cours. Imposées par cgroup v2 sous Linux, et le panneau le dit franchement quand un hôte ne peut pas les
  imposer. Voir **[docs/LIMITS.md](LIMITS.md)**.
- **Apparence** — `$HOME/.baton/TUI.yaml` remodèle le cockpit : un **thème** de couleurs et les **dispositions** de la vue
  divisée des groupes, rechargés à chaud avec `C-t R`. Voir **[docs/TUI.md](TUI.md)**.
- **Pied de page d'usage** — `U` affiche ou masque un pied de page indiquant la consommation de tokens et le coût du jour
  (`⊙ 1.2M tok · ≈$12.34 API`). Il lit par défaut les transcripts de Claude Code (fonctionne avec un abonnement Pro/Max) ou
  l'Anthropic Admin API avec une clé. Le coût est un équivalent API, pas un montant facturé sur l'abonnement. Voir
  **[docs/USAGE.md](USAGE.md)**.
- **Persistance et relance** — Baton se souvient de sa flotte d'un redémarrage à l'autre ; les panneaux reviennent sous
  forme d'emplacements terminés et inertes, et `r` les relance à partir de leur spécification conservée.
- **Rechargement** — `C-t R` (ou un `SIGHUP` envoyé au démon) recharge la config à chaud sans redémarrer la flotte.
- **Souris** — désactivée par défaut pour que la sélection propre à votre terminal reste disponible ; activez-la dans la
  table de touches pour défiler et sélectionner à la molette.
- **Langue** — la liste de touches de `?` et la table de touches `C-t k` s'affichent en anglais ou en 繁體中文. Réglez
  `settings.language`, faites-la défiler en direct depuis la table de touches, ou laissez simplement votre `$LANG` décider.
  Voir **[docs/TUI.md](TUI.md#language)**.

## Économiseur d'écran

Partez et laissez-le tourner. Après quelques minutes d'inactivité — ou sur le `C-t E` caché — le cockpit bascule dans une
pluie de code Matrix en plein écran, avec le logotype **BATON** et une grande horloge flottant au milieu. C'est une prise
de contrôle purement côté frontend : rien n'est envoyé au serveur, et n'importe quelle touche ou clic vous ramène
directement à votre vue.

![Économiseur d'écran Baton — une pluie numérique Matrix avec le logotype BATON et une grande horloge](assets/baton-screensaver.png)

_Clip généré à partir de [`baton-screensaver.tape`](assets/baton-screensaver.tape) — les étapes de régénération sont dans l'en-tête du fichier tape._

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
- **[docs/TUI.md](TUI.md)** — le fichier d'apparence du cockpit (`$HOME/.baton/TUI.yaml`) : le thème de couleurs et les
  dispositions de la vue divisée des groupes (préréglages et grilles personnalisées).
- **[docs/LIMITS.md](LIMITS.md)** — les limites de ressources : la config, les deux couches, le rechargement à chaud et où
  elles sont réellement imposées.
- **[docs/RESTART.md](RESTART.md)** — la politique de redémarrage : ce qui compte comme un échec et ce qui n'en est
  pas, le backoff et la limite, et pourquoi `always` n'existe pas.
- **[docs/GIT.md](GIT.md)** — le menu git : chaque opération, le flux de l'éditeur de commit, les worktrees et la config.
- **[docs/USAGE.md](USAGE.md)** — le pied de page d'usage du compte : les sources locale et Admin-API, la config et les
  réserves.
- **[docs/PLUGIN.md](PLUGIN.md)** — l'API des plugins Lua : l'objet `baton`, les événements, les commandes et la config.
- **[docs/CONTROL.md](CONTROL.md)** — piloter la flotte par agent : le conductor, le CLI `baton ctl`, les outils
  `baton mcp` et les garde-fous.

## DDD (Dream-Driven Development)

Ce projet suit le DDD (dream-driven development, développement piloté par le rêve) : chaque fonctionnalité naît de ce dont
je rêve et de ce dont j'ai besoin.
