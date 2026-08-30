# Baton

> Un multiplexor de terminal extensible y pensado para agentes.

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · [繁體中文](README.zh-TW.md) · [日本語](README.ja.md) · [한국어](README.ko.md) ·
[Français](README.fr.md) · [Deutsch](README.de.md) · **Español**

¿Ejecutas varios agentes de programación con IA a la vez? La cosa se descontrola rápido: ventanas que hacer malabares,
sesiones desperdigadas entre pestañas y ningún sitio donde ver de un vistazo quién trabaja, quién está atascado y quién
te está esperando.

Baton es a los agentes de IA lo que tmux es a las shells. Te da **una única cabina gobernada por el teclado**: un tablero
en vivo con todos los agentes, agrupados en las tareas a las que pertenecen, y cualquiera de ellos a una tecla de distancia.

Tú llevas la batuta. Los agentes tocan. Tú diriges. 🎼

![Demostración de la cabina de Baton: la lista de teclas, paneles abiertos, el conductor a la vista, dos agrupados en un work item y la misma ? pulsada de nuevo en la vista dividida y en el zoom](assets/baton-demo.png)

_Una sola tecla hace el recorrido: `?` lista las teclas de la vista en la que estás. Se abren paneles, `n C` llama al
conductor, `g g` y luego `g c` agrupan dos en un work item; y `?` en la vista dividida y `C-t ?` en el zoom son tres
tablas distintas._

_Vídeo generado a partir de [`baton-demo.tape`](assets/baton-demo.tape); la CLI de agente del conductor es un doble
([`demo-agent.sh`](assets/demo-agent.sh)) para que el vídeo salga igual en cualquier máquina, y la flota que maneja a
través del socket sí es real._

## Primeros pasos

Baton es un único binario estático. En macOS, instálalo con [Homebrew](https://brew.sh):

```sh
brew install cmj0121/tap/baton
```

En Linux, basta con una línea:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

…o, en cualquier plataforma, consíguelo con [Go](https://go.dev) 1.26+:

```sh
go install github.com/cmj0121/baton/cmd/baton@latest
```

…o compílalo desde un clon con `make install`. Después basta con ejecutar:

```sh
baton
```

Baton arranca su servidor en segundo plano y te deja en el **tablero**, tu base de operaciones. Tu primer minuto:

1. Pulsa **`A`** para abrir un agente (elegirás un directorio de trabajo para él).
2. Pulsa **`enter`** para hacer zoom y verlo trabajar; **`C-t d`** te devuelve al tablero.
3. Pulsa **`q`** para desacoplarte y marcharte: todo sigue en marcha. Vuelve cuando quieras con `baton`.

¿Perdido? **`?`** siempre muestra las teclas del sitio donde estés.

## ¿Por qué no simplemente tmux?

Porque tmux no sabe qué hay dentro del panel. Te da ventanas; cuál es cuál lo recuerdas tú, y que un agente lleva un rato
esperándote lo descubres pasando por ellas una a una. Baton da por hecho que en el panel hay un agente, y todo lo demás
sale de ahí:

| Lo que estás haciendo            | tmux, a mano                               | Baton                                                                                             |
| -------------------------------- | ------------------------------------------ | ------------------------------------------------------------------------------------------------- |
| Ver quién te necesita            | pasar por los paneles y leer               | un estado vivo por panel y una bandeja `C-t a` con los que se han parado a esperar a una persona  |
| Mantener junto lo que va junto   | nombrar las ventanas y recordar el esquema | work items: un grupo con nombre de paneles, hecho con dos teclas                                  |
| Repartir el trabajo              | teclearlo tú en cada panel                 | mandar una tarea a uno o a todo un grupo, o dejar que un conductor maneje la flota                |
| Frenar una compilación desbocada | nada                                       | topes de CPU, memoria y procesos, sostenidos sobre todo el árbol de procesos del panel            |
| Saber lo que cuesta la flota     | nada                                       | los tokens y el coste de la ventana de facturación, y tus barras de cuota, atribuibles a un panel |

Baton no sustituye a tmux ni quiere tus shells: si vives dentro de tmux, ejecútalo ahí.

## Concepto

- **Agentes, no shells.** La unidad de trabajo es un agente en ejecución, no una ventana a la que hacer de niñera.
- **Un tablero, no ventanas.** Una vista general y en vivo de todo a la vez, no un montón de pestañas.
- **Núcleo headless, frontends reemplazables.** El cerebro es un demonio en segundo plano; la cara que lo dibuja es
  intercambiable.

| Concepto         | Qué es                                                                                                                        |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| **Panel**        | Una terminal en vivo: un panel _agent_ (una CLI de agente) o un panel _shell_.                                                |
| **Work item**    | Un grupo con nombre de paneles que pertenecen a una misma tarea.                                                              |
| **Task**         | Un encargo que despachas a un agente, seguido a lo largo de su ciclo de vida, encolado y planificado si tiene que esperar.    |
| **Conductor**    | Un agente que maneja la flota por ti: abre paneles, los agrupa y da instrucciones a los demás a través del socket.            |
| **Global shell** | Una única shell del anfitrión que el servidor mantiene en `$HOME`, siempre a una tecla: una base, no un director de la flota. |

## Vistas

Manejas Baton a través de tres vistas, y te mueves entre ellas con una tecla:

- **Tablero (Dashboard)** — control de misión. Una flota pequeña es una rejilla de **tarjetas**, una por panel y una por work
  item; a partir de seis cosas de primer nivel se convierte en un **árbol** en vivo con todos los paneles: un work item por
  fila, sus subgrupos indentados debajo y sus paneles debajo de esos. `space` muestra u oculta lo que hay anidado bajo una
  fila, a cualquier profundidad; `→` abre un work item y entra en él, `←` lo cierra y sube un nivel — y desde el primer nivel,
  de vuelta a las tarjetas. La fila lleva el estado, el directorio de trabajo, la sparkline de salida y la tarea asignada a
  medida que la terminal se ensancha; `v p` abre un panel de detalle al lado. Aquí navegas, abres y cierras paneles, y los
  agrupas en work items.
- **Grupo (Group)** — la división en vivo de un work item: sus paneles en mosaico, uno al lado del otro, todos
  transmitiendo a la vez. Los primeros se transmiten como mosaicos en vivo; el resto se pliega en un único **mosaico de
  resumen** al que puedes hacer zoom. Fija unos cuantos para tenerlos siempre activos, maneja el que tiene el foco allí
  mismo con **`i`**, o pulsa **`enter`** para meterte dentro.
- **Zoom** — un solo panel como tu única terminal. Las pulsaciones van directas al programa; la tecla líder **`C-t`** es
  como actúas o das un paso atrás.

## Teclas

Las teclas son **modales**: en el tablero y dentro de un grupo cada acción es una sola tecla; en un zoom o en modo
interactivo tus pulsaciones manejan el programa, así que una acción de Baton es la tecla líder **`C-t`** seguida de la
tecla. Pulsa **`?`** para ver la lista completa y reasignable de la vista actual, y **`C-t k`** para editar el mapa de
teclas.
Cuatro teclas son _landings_: no hacen nada por sí solas y abren una familia — `n` abre, `v` dibuja, `g` agrupa,
`x` es el doble toque que se confirma a sí mismo — y la barra de estado dice qué acepta cada una a continuación.

| Dónde       | Tecla                 | Hace                                                                  |
| ----------- | --------------------- | --------------------------------------------------------------------- |
| After `C-t` | `d` / `b`             | ir al tablero / volver un nivel                                       |
|             | `a`                   | bandeja de atención — despachar lo que necesita a alguien             |
|             | `[`                   | entrar en modo de desplazamiento                                      |
|             | `l` / `L`             | registrar el panel en un archivo / releer ese registro                |
|             | `R` / `S`             | recargar la configuración / forzar reinicio del servidor              |
|             | `q`                   | desacoplarse (el servidor sigue en marcha)                            |
| Dashboard   | `jk` / `↑↓`           | mover el cursor                                                       |
|             | `hl` / `←→`           | mover una tarjeta · en el árbol: plegar / desplegar un work item      |
|             | `space`               | mostrar / ocultar lo que hay anidado bajo la fila                     |
|             | `v p` / `v g`         | panel de detalle / agrupar por: work item, directorio, perfil, estado |
|             | `v l`                 | el diseño del tablero: tarjetas o árbol                               |
|             | `m`                   | coger una fila — las flechas la llevan, `enter` la suelta             |
|             | `enter`               | abrir / hacer zoom en la selección                                    |
|             | `p` / `A` / `n c`     | nuevo panel de shell / agente / elegir comando                        |
|             | `n .`                 | nuevo panel de shell en el directorio del panel enfocado              |
|             | `n C`                 | abrir el conductor (un agente que maneja la flota)                    |
|             | `n h`                 | abrir la global shell (una shell del anfitrión en `$HOME`)            |
|             | `w` / `x x`           | cerrar la selección / purgar los terminados                           |
|             | `r`                   | reejecutar los paneles terminados bajo el foco                        |
|             | `g g` / `g c` / `g u` | marcar / agrupar los marcados / desagrupar                            |
|             | `s` / `f` / `D`       | enviar señal / buscar / diff de la selección                          |
|             | `/`                   | buscar en la salida de todos los paneles (grep a la flota)            |
|             | `T` / `Q`             | despachar una tarea / gestionar la cola de tareas                     |
|             | `v u`                 | alternar el pie de uso: apagado / ventana / panel enfocado / cuota    |
|             | `v U`                 | uso de la cuenta — barras de cuota y quién las consume                |
|             | `v k`                 | alternar el indicador de teclas en el pie                             |
| Group       | `tab`                 | dar el foco al siguiente panel                                        |
|             | `+` / `-`             | mostrar más / menos mosaicos en vivo                                  |
|             | `L`                   | rotar la disposición de los mosaicos                                  |
|             | `p` / `i`             | fijar / interactuar con el panel enfocado                             |
|             | `enter`               | hacer zoom en el panel enfocado                                       |
| Zoom        | escribir              | manejar el programa directamente                                      |
|             | `C-t f` / `C-t G`     | buscar en el historial / menú de git (agente)                         |

Consulta **[docs/KEYS.md](KEYS.md)** para la referencia completa de teclas, y **[docs/SPEC.md](SPEC.md)** para el diseño
que hay detrás de cada
una.

## Funcionalidades

Cinco cosas que un multiplexor de terminal no hace:

- **Atención, no rondas** — una flota está bien casi siempre; si miras la pantalla es por los pocos paneles que no lo
  están. Un único reloj silencioso los ordena a todos — `running`, `idle` a los diez segundos, `done` para un agente que
  ha terminado su turno, `stuck` cuando se alarga demasiado — y un agente puede levantar la mano por su cuenta, por
  encima de toda la escalera. `C-t a` abre la bandeja desde cualquier vista y la cola se vacía ahí; `settings.notify`
  manda una notificación de escritorio OSC 9 cuando no hay nadie mirando, agrupada, y nunca por un `done`. Ver
  **[docs/ATTENTION.md](ATTENTION.md)**.
- **Un conductor** — `n C` abre un agente que maneja la flota por ti: abre paneles, los agrupa, les manda señales y les
  pasa prompts a través del socket, mediante `baton ctl` o las herramientas `baton mcp`, vallado para que no pueda
  destrozar su propio anfitrión. Su objetivo se fija en `$HOME/.baton/CONDUCTOR.md`. Ver
  **[docs/CONTROL.md](CONTROL.md)**.
- **Tareas y una cola** — `T` reparte un encargo a un agente, o a todo un work item; queda anotado en la tarjeta y se
  entrega cuando el agente está listo. `Q` gestiona una cola persistente que un planificador del servidor va vaciando
  sobre los agentes libres. Un hook Lua `task.pre` puede reescribir o vetar un encargo; `task.change` lo vigila.
- **Topes sobre todo el árbol de procesos** — limita la CPU, la memoria y los procesos de un panel, y sujeta a ese tope
  su árbol de procesos entero, para que una compilación desbocada no se lleve la máquina por delante. Un suelo para toda
  la flota con anulaciones por agente, aplicado a la flota en marcha con `C-t R`, impuesto con cgroup v2 en Linux — y el
  panel lo dice claramente cuando un anfitrión no puede imponerlo. Ver **[docs/LIMITS.md](LIMITS.md)**.
- **Consumo atribuible a un panel** — `v u` va rotando una lectura en el pie: los tokens y el coste de la ventana de
  facturación con cuenta atrás (`⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31`), la parte del panel enfocado, o las barras de
  límite de tu cuenta (`⊙ 5h ▓▓▓▓▓░░░ 2:14:31 · 7d ▓▓▓░░░░░ 3d4h`). `v U` lo abre todo: cada ventana de cuota, el
  crédito extra y los paneles que se los están comiendo. Ver **[docs/USAGE.md](USAGE.md)**.

Otras cuatro que la mayoría tampoco tiene:

- **Aislamiento en contenedor** — opcional por perfil de agente: con `isolate: docker`, los paneles de ese perfil corren
  dentro de un contenedor con tu worktree montado. La imagen la pones tú (Baton no trae ninguna); `mount`, `network`,
  `env-allow` y `user` deciden qué más cruza, y de tu entorno no cruza nada que no nombres. Desactivado por defecto, y no
  es una frontera frente a un agente hostil. Ver **[docs/ISOLATION.md](ISOLATION.md)**.
- **Grep a toda la flota** — `/` busca en la salida de todos los paneles a la vez y agrupa los aciertos por panel;
  `enter` amplía el que elijas, posado sobre el acierto. `C-t f` busca por expresión regular en un solo historial, y el
  modo de desplazamiento (`C-t [`) selecciona y copia por OSC 52, así que funciona sobre SSH sin binario auxiliar.
- **Backends de agente** — Baton conoce un catálogo de CLI de agentes (`claude`, `codex`, `gemini`, `aider`, `opencode`,
  `grok`) y detecta cuáles hay realmente en la máquina donde corre la flota. `A` lanza el que elijas; `C-t P` fija el
  valor por defecto de la flota y dice dónde conseguir los que faltan; `C-t R` vuelve a detectar tras una instalación.
  Los tuyos van bajo `panel.agents`.
- **Acceso remoto** — `baton --remote` engancha la misma cabina a una flota de otra máquina, por el ssh que ya usas: sin
  puerto a la escucha, sin TLS y sin ningún intercambio de claves propio de Baton. Desactivado por defecto; `C-t @` lo
  enciende, acuña una passkey que nunca se escribe en disco, y lista cada conexión viva para echarla, renovarla o
  cerrarlo todo. Ver **[docs/REMOTE.md](REMOTE.md)**.

Y la cabina que se espera de un multiplexor, cada cosa a una tecla:

| Función              | Tecla           | Qué hace                                                                                             |
| -------------------- | --------------- | ---------------------------------------------------------------------------------------------------- |
| Diff                 | `D`             | el diff del árbol de trabajo del panel — preparado y sin preparar a la vez, sin seguimiento incluido |
| Git                  | `C-t G`         | diff, log, status, stage, commit, push, ramas y worktrees — **[docs/GIT.md](GIT.md)**                |
| Señales              | `s`             | cualquier señal a la selección, a la tesela enfocada o a todo el grupo                               |
| Buscar               | `f`             | filtrar la flota por título o por grupo                                                              |
| Disposición de grupo | `+` `-` `L`     | cuántos miembros se emiten en vivo, y la forma de la división                                        |
| Shell global         | `n h`           | un shell anfitrión simple que el servidor mantiene en `$HOME`, siempre a una tecla                   |
| Directorio recordado | `n .`           | los paneles siguen su directorio por OSC 7 — **[docs/RESTART.md](RESTART.md)**                       |
| Registro de panel    | `C-t l` `C-t L` | volcar la salida de un panel a un fichero y volver a leerla — **[docs/LOGGING.md](LOGGING.md)**      |
| Persistencia         | `r`             | la flota sobrevive a un reinicio como huecos que relanzas desde su spec                              |
| Política de reinicio | —               | `panel.restart: on-failure` devuelve un panel con espera y un límite                                 |
| Recarga en caliente  | `C-t R`         | la configuración sin reiniciar la flota — o un `SIGHUP` al demonio                                   |
| Apariencia           | —               | tema y rejillas propias en `$HOME/.baton/TUI.yaml` — **[docs/TUI.md](TUI.md)**                       |
| Salvapantallas       | —               | una lluvia digital a pantalla completa cuando la cabina descansa — **[docs/TUI.md](TUI.md)**         |
| Ratón                | —               | desactivado por defecto, para no quitarte la selección del terminal                                  |
| Idioma               | —               | la lista de teclas se lee en inglés o en chino tradicional — **[docs/TUI.md](TUI.md#language)**      |

## Arquitectura

Un **baton server** headless (un demonio en segundo plano) posee todo el estado y todas las terminales. Los frontends
enchufables se conectan por un único socket de dominio Unix —comandos hacia arriba, eventos hacia abajo—, de modo que te
desacoplas y te vuelves a conectar sin perder nada.

Consulta **[docs/SPEC.md](SPEC.md)** para el diagrama completo y el modelo de interacción.

## Plugins

Un solo archivo Lua (`$HOME/.baton/plug-in.lua`) remodela Baton según tu flujo de trabajo: reacciona a eventos del ciclo
de vida (avisarte cuando un agente te necesita, encadenar el siguiente paso cuando uno termina), maneja la flota, añade
tus propios comandos y define configuración, todo a través de un único objeto `baton`. Consulta
**[docs/PLUGIN.md](PLUGIN.md)**.

## Documentación

- **[docs/SPEC.md](SPEC.md)** — la especificación completa: vistas, el ciclo de vida del panel, work items, señales, diff,
  persistencia, la referencia de teclas por vista y el diagrama de arquitectura.
- **[docs/ATTENTION.md](ATTENTION.md)** — atención a escala: la escalera del silencio (`done`, `stuck`, failed), la
  bandeja `C-t a`, los dos plegados del tablero, las notificaciones de escritorio y todos sus ajustes.
- **[docs/TUI.md](TUI.md)** — el archivo de apariencia de la cabina (`$HOME/.baton/TUI.yaml`): el tema de color y las
  disposiciones de la división de grupo (preajustes y cuadrículas propias).
- **[docs/LIMITS.md](LIMITS.md)** — límites de recursos: la configuración, las dos capas, la recarga en caliente y dónde
  se imponen realmente.
- **[docs/ISOLATION.md](ISOLATION.md)** — aislamiento por contenedor: la configuración por perfil, qué conserva el
  agente, cómo se imponen los límites dentro de un contenedor y de qué no es frontera.
- **[docs/RESTART.md](RESTART.md)** — la política de reinicio: qué cuenta como fallo y qué no, el backoff y el límite,
  y por qué no existe `always`.
- **[docs/GIT.md](GIT.md)** — el menú de git: cada operación, el flujo del editor de commits, los worktrees y la
  configuración.
- **[docs/LOGGING.md](LOGGING.md)** — el registro de paneles: qué se escribe, dónde aterriza el archivo, los marcadores
  de sesión, la rotación, y de qué no es una frontera.
- **[docs/REMOTE.md](REMOTE.md)** — el acceso remoto por SSH: el puente `--stdio`, qué es y qué no es la passkey,
  la lista de conexiones de `C-t @`, y los fallos que informa.
- **[docs/USAGE.md](USAGE.md)** — el pie de uso de la cuenta: las fuentes local y de Admin API, la configuración y las
  advertencias.
- **[docs/PLUGIN.md](PLUGIN.md)** — la API de plugins en Lua: el objeto `baton`, eventos, comandos y configuración.
- **[docs/CONTROL.md](CONTROL.md)** — manejar la flota mediante un agente: el conductor, la CLI `baton ctl`, las
  herramientas de `baton mcp` y las barreras de seguridad.

## DDD (Dream-Driven Development, desarrollo guiado por sueños)

Este proyecto sigue el DDD (desarrollo guiado por sueños): cada funcionalidad se construye a partir de lo que sueño y
necesito.
