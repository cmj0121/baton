# Baton

> Un multiplexor de terminal extensible y pensado para agentes.

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md) · [한국어](README.ko.md) ·
[Français](README.fr.md) · [Deutsch](README.de.md) · **Español**

Ejecutar varios agentes de programación con IA a la vez se descontrola rápido: ventanas que hacer malabares, sesiones
desperdigadas entre pestañas y ningún sitio donde ver quién trabaja, quién está atascado y quién te está esperando.
Baton es a los agentes de IA lo que tmux es a las shells: **una única cabina gobernada por el teclado**, un tablero en
vivo con todos los agentes agrupados en las tareas a las que pertenecen, cualquiera de ellos a una tecla de distancia.
Tú llevas la batuta. Los agentes tocan. Tú diriges. 🎼

![Demostración de la cabina de Baton: la lista de teclas, paneles abiertos, el conductor a la vista, dos agrupados en un work item y la misma ? pulsada de nuevo en la vista dividida y en el zoom](assets/baton-demo.png)

_`?` lista las teclas de la vista en la que estás: se abren paneles, `n C` llama al conductor, `g g` y luego `g c`
agrupan dos en un work item; y `?` de nuevo en la vista dividida y `C-t ?` en el zoom muestran tres tablas distintas._

## Primeros pasos

Baton es un único binario estático: `brew install cmj0121/tap/baton` en macOS, o en Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

…o `go install github.com/cmj0121/baton/cmd/baton@latest` con [Go](https://go.dev) 1.26+. Después ejecuta `baton`, y:

1. **`A`** abre un agente (elige un directorio de trabajo; `enter` lo deja aquí, `w` lo aísla en una rama).
2. **`enter`** hace zoom para verlo trabajar; **`C-t d`** te devuelve al tablero.
3. **`q`** te desacopla: todo sigue en marcha. Vuelve cuando quieras con `baton`.

¿Perdido? **`?`** muestra las teclas del sitio donde estés. Para toda una flota, **`n C`** abre un conductor que sigue tus
órdenes permanentes en `$HOME/.baton/CONDUCTOR.md`; ver **[docs/CONTROL.md](CONTROL.md)**.

## Conceptos

Un **panel** es una terminal en vivo que ejecuta una CLI de agente o una shell. Un **work item** agrupa los paneles de
una misma tarea. Una **task** es un encargo que despachas a un agente, encolado hasta que esté libre. El **conductor** es
un agente que maneja el resto de la flota por ti.

Los ves a través de tres vistas: el **tablero** (cada panel como tarjeta o en un árbol en vivo), un **grupo** (los
paneles de un work item transmitiendo uno al lado del otro) y un **zoom** (un panel como tu única terminal, con la tecla
líder `C-t` para salir). Detrás de ellas, un servidor headless posee todas las terminales a través de un socket Unix, así
que te desacoplas y te vuelves a conectar sin perder nada. Ver **[docs/SPEC.md](SPEC.md)**.

## Por qué Baton

tmux te da ventanas y no tiene ni idea de qué hay dentro. Baton da por hecho que en cada panel hay un agente, y todo lo
demás sale de ahí:

- **Ves quién te necesita.** Cada panel lleva un estado vivo, y los que esperan a una persona se ponen en cola en una
  única bandeja en lugar de descubrirse pasando por las ventanas una a una.
- **El trabajo sigue agrupado.** Los paneles que sirven a una tarea forman un work item, hecho con dos teclas y manejado
  como uno solo.
- **Repartes trabajo, no pulsaciones.** Despacha un encargo a un agente o a todo un grupo, o deja que un conductor maneje
  la flota.
- **La flota tiene límites.** Los topes sujetan todo el árbol de procesos de una compilación desbocada, y el consumo se
  atribuye al panel que lo gastó.
- **La flota recuerda.** Lo que los agentes aprenden una y otra vez se ordena y se entrega con el siguiente encargo.

Baton no quiere tus shells: si vives dentro de tmux, ejecútalo ahí.

## Funcionalidades

- **Atención** — paneles ordenados en `running`, `idle`, `done` o `stuck`; `C-t a` es la bandeja.
  [ATTENTION](ATTENTION.md)
- **Conductor** — `n C` maneja la flota mediante `baton ctl` o `baton mcp`, vallado frente a su anfitrión.
  [CONTROL](CONTROL.md)
- **Tareas** — `T` despacha un encargo a un agente o a un work item; `Q` es una cola que un planificador vacía sobre los
  agentes libres.
- **Score** — lo que los agentes aprenden una y otra vez sube por una escalera y encabeza cada encargo, guardado en
  `score.md`. [SCORE](SCORE.md)
- **Topes** — límites de CPU, memoria y procesos sostenidos sobre todo el árbol de procesos de un panel.
  [LIMITS](LIMITS.md)
- **Consumo** — `v u` muestra tokens, coste y barras de cuota; `v U` muestra qué paneles los gastan. [USAGE](USAGE.md)
- **Aislamiento** — `isolate: docker` ejecuta los paneles de un perfil en un contenedor sobre tu worktree.
  [ISOLATION](ISOLATION.md)
- **Grep a la flota** — `/` busca en la salida de todos los paneles; `C-t [` desplaza y copia por OSC 52, incluso sobre
  SSH.
- **Backends** — se detectan `claude`, `codex`, `gemini`, `aider`, `opencode` y `grok`; añade los tuyos bajo
  `panel.agents`.
- **Remoto** — `baton --remote` se engancha a una flota de otra máquina por tu ssh. [REMOTE](REMOTE.md)
- **Plugins** — un solo archivo Lua, `$HOME/.baton/plug-in.lua`, engancha eventos y añade comandos. [PLUGIN](PLUGIN.md)
- **Lo de siempre** — diff (`D`), un menú de git (`C-t G`), señales (`s`), registros de panel, puertos serie, una
  política de reinicio, recarga en caliente (`C-t R`), temas y disposiciones de la división.

## Teclas esenciales

En el tablero cada acción es una sola tecla; en un zoom tus pulsaciones manejan el programa, así que antepón la tecla
líder `C-t`.

| Dónde       | Tecla         | Hace                                                      |
| ----------- | ------------- | --------------------------------------------------------- |
| Dashboard   | `jk` / `↑↓`   | mover el cursor                                           |
|             | `enter`       | abrir / hacer zoom en la selección                        |
|             | `A`           | nuevo agente — directorio, luego intro aquí o `w` aislar  |
|             | `n C`         | abrir el conductor (un agente que maneja la flota)        |
|             | `g g` / `g c` | marcar / agrupar los paneles marcados                     |
|             | `T` / `Q`     | despachar una tarea / gestionar la cola                   |
|             | `w` / `x x`   | cerrar la selección / purgar los terminados               |
| After `C-t` | `d` / `b`     | ir al tablero / volver un nivel                           |
|             | `a`           | bandeja de atención — despachar lo que necesita a alguien |
|             | `q`           | desacoplarse (el servidor sigue en marcha)                |

Todo lo demás, reasignable con `C-t k`, está en **[docs/KEYS.md](KEYS.md)**.

## Documentación

- **Uso** — [SPEC](SPEC.md) · [KEYS](KEYS.md) · [ATTENTION](ATTENTION.md) · [TUI](TUI.md) · [GIT](GIT.md) ·
  [LOGGING](LOGGING.md)
- **Flota** — [CONTROL](CONTROL.md) · [SCORE](SCORE.md) · [USAGE](USAGE.md) · [LIMITS](LIMITS.md) ·
  [ISOLATION](ISOLATION.md) · [RESTART](RESTART.md)
- **Extensión** — [PLUGIN](PLUGIN.md) · [REMOTE](REMOTE.md) · [SERIAL](SERIAL.md)
- **Internos** — [DAEMON](DAEMON.md)

## DDD (Dream-Driven Development, desarrollo guiado por sueños)

Este proyecto sigue el DDD (desarrollo guiado por sueños): cada funcionalidad se construye a partir de lo que sueño y
necesito.
