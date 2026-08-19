# Baton

> Un multiplexor de terminal extensible y pensado para agentes.

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

![Demostración de la cabina de Baton: paneles en el tablero, zoom para manejar uno, agrupar dos en un work item, abrir el conductor y la global shell](assets/baton-demo.png)

_Abre paneles, haz zoom en uno para manejarlo, junta dos en un work item, llama al conductor con `C` y a la global shell
con `H`; y `?` siempre está ahí para recordarte las teclas._

_Vídeo generado a partir de [`baton-demo.tape`](assets/baton-demo.tape); los pasos para regenerarlo están en la cabecera
del tape. La CLI de agente del conductor es un doble ([`demo-agent.sh`](assets/demo-agent.sh)) para que el vídeo salga
igual en cualquier máquina; la flota que maneja a través del socket sí es real._

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

- **Tablero (Dashboard)** — control de misión. Un **árbol** en vivo con todos los paneles: un work item por fila, sus
  subgrupos indentados debajo y sus paneles debajo de esos. `→` abre un work item y entra en él, `←` lo cierra y sube un
  nivel. La fila lleva el estado, el directorio de trabajo, la sparkline de salida y la tarea asignada a medida que la
  terminal se ensancha; `v` abre un panel de detalle al lado. Aquí navegas, abres y cierras paneles, y los agrupas en
  work items.
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

| Dónde       | Tecla             | Hace                                                                  |
| ----------- | ----------------- | --------------------------------------------------------------------- |
| After `C-t` | `d` / `b`         | ir al tablero / volver un nivel                                       |
|             | `a`               | bandeja de atención — despachar lo que necesita a alguien             |
|             | `[`               | entrar en modo de desplazamiento                                      |
|             | `l` / `L`         | registrar el panel en un archivo / releer ese registro                |
|             | `R` / `S`         | recargar la configuración / forzar reinicio del servidor              |
|             | `q`               | desacoplarse (el servidor sigue en marcha)                            |
| Dashboard   | `jk` / `↑↓`       | mover el cursor                                                       |
|             | `hl` / `←→`       | plegar / desplegar un work item — salir / entrar                      |
|             | `v` / `z`         | panel de detalle / agrupar por: work item, directorio, perfil, estado |
|             | `space`           | coger una fila — las flechas la llevan, `enter` la suelta             |
|             | `enter`           | abrir / hacer zoom en la selección                                    |
|             | `p` / `A` / `c`   | nuevo panel de shell / agente / elegir comando                        |
|             | `.`               | nuevo panel de shell en el directorio del panel enfocado              |
|             | `C`               | abrir el conductor (un agente que maneja la flota)                    |
|             | `H`               | abrir la global shell (una shell del anfitrión en `$HOME`)            |
|             | `w` / `x`         | cerrar la selección / purgar los terminados                           |
|             | `r`               | reejecutar los paneles terminados bajo el foco                        |
|             | `g` / `G` / `u`   | marcar / agrupar los marcados / desagrupar                            |
|             | `s` / `f` / `D`   | enviar señal / buscar / diff de la selección                          |
|             | `/`               | buscar en la salida de todos los paneles (grep a la flota)            |
|             | `T` / `Q`         | despachar una tarea / gestionar la cola de tareas                     |
|             | `U`               | alternar el pie de uso: apagado / ventana / panel enfocado            |
|             | `K`               | alternar el indicador de teclas en el pie                             |
| Group       | `tab`             | dar el foco al siguiente panel                                        |
|             | `+` / `-`         | mostrar más / menos mosaicos en vivo                                  |
|             | `L`               | rotar la disposición de los mosaicos                                  |
|             | `p` / `i`         | fijar / interactuar con el panel enfocado                             |
|             | `enter`           | hacer zoom en el panel enfocado                                       |
| Zoom        | escribir          | manejar el programa directamente                                      |
|             | `C-t f` / `C-t g` | buscar en el historial / menú de git (agente)                         |

Consulta **[docs/SPEC.md](SPEC.md)** para la referencia completa de teclas por vista y el diseño que hay detrás de cada
una.

## Funcionalidades

Todo lo que necesitas mientras pastoreas una flota, a una tecla de distancia:

- **Backends de agente** — baton conoce un catálogo de CLIs de agente (`claude`, `codex`, `gemini`, `aider`,
  `opencode`) y detecta cuáles tiene realmente la máquina donde corre la flota. `A` lista los que puedes ejecutar y crea
  el que elijas; `C-t P` fija el valor por defecto de la flota desde esa misma lista; `C-t R` vuelve a detectar tras una
  instalación. Añade los tuyos — o cambia el comando, los argumentos, los límites o el contenedor de un preajuste — bajo
  `panel.agents`. Nada de esto añade una tecla nueva.
- **Señales** — `s` envía cualquier señal a la selección, al mosaico enfocado o a todo el grupo; el selector lista las
  más habituales y con `o` escribes cualquier nombre o número.
- **Buscar, encontrar, copiar** — `f` filtra la flota por título o grupo; `/` hace grep en la salida de todos los paneles
  a la vez y lista las coincidencias agrupadas por panel: `enter` hace zoom en la que elijas, aterrizando sobre la
  coincidencia; `C-t f` busca con expresiones regulares en el historial de un panel; el modo de desplazamiento (`C-t [`)
  selecciona y copia por OSC52, así que funciona sobre SSH sin ningún binario auxiliar.
- **Diff** — `D` (o `C-t D` dentro de un zoom) muestra el diff del árbol de trabajo del panel de agente —lo preparado y
  lo no preparado a la vez, incluidos los archivos sin seguimiento— en una superposición maestro-detalle.
- **Git** — `C-t g` abre un menú de git sobre el agente ampliado: diff, log, status, preparar, commit, push, ramas y
  worktrees. Consulta **[docs/GIT.md](GIT.md)**.
- **Conductor y control** — `C` abre un conductor: un agente que maneja la flota por ti. Abre paneles, los agrupa, les
  envía señales y les da instrucciones a través del socket —mediante `baton ctl` o las herramientas de `baton mcp`—,
  con vallas para que no pueda destrozar su propio anfitrión. Define su objetivo en `$HOME/.baton/CONDUCTOR.md`.
  Consulta **[docs/CONTROL.md](CONTROL.md)**.
- **Global shell** — `H` abre la global shell: una única shell del anfitrión que el servidor mantiene en `$HOME`, siempre
  a una tecla de distancia. Igual que el conductor, es una marca en el encabezado FLEET en vez de una tarjeta, y el
  servidor solo guarda una: sobrevive a un reinicio como una ranura muerta que reejecutas con `r`. A diferencia del
  conductor, no maneja nada: ni rol acotado ni espacio de trabajo gestionado. (Distinta de la shell **scratch** flotante
  `C-t ~`, que es transitoria y muere al desacoplarte.)
- **Tareas y la cola** — `T` despacha un encargo a un agente (o lo reparte a todo un work item), queda anotado en la
  tarjeta y se entrega cuando el agente está listo. `Q` gestiona un backlog persistente que un planificador propio del
  servidor va vaciando sobre los agentes libres: el flujo **tú → conductor → flota**. Un hook Lua `task.pre` puede
  reescribir o vetar un encargo; `task.change` lo vigila.
- **Grupos y resumen** — `+` / `-` ajustan cuántos miembros se transmiten como mosaicos en vivo; el resto se pliega en un
  solo mosaico de resumen. Los miembros fijados siempre se transmiten. `L` rota la **disposición** de la división: la
  cuadrícula uniforme, `main-vertical`, `main-horizontal`, `stack` o tus propias cuadrículas de `TUI.yaml`.
- **Límites de recursos** — limita lo que puede usar un panel —CPU, memoria, procesos— y sujeta a ese límite **todo su
  árbol de procesos**, para que una compilación desbocada no se lleve la máquina por delante. Define un mínimo para toda
  la flota y sobrescrituras por agente en la configuración o bajo `C-t P`; `C-t R` los aplica a la flota en marcha. Se
  imponen con cgroup v2 en Linux, y el panel lo dice claramente cuando un anfitrión no puede imponerlos. Consulta
  **[docs/LIMITS.md](LIMITS.md)**.
- **Aislamiento por contenedor** — opcional, por perfil de agente: `isolate: docker` ejecuta los paneles de ese perfil
  dentro de un contenedor con tu árbol de trabajo montado, de modo que un agente que se equivoca queda confinado a un
  espacio de trabajo. Tú nombras la imagen (Baton no incluye ninguna); `mount`, `network`, `env-allow` y `user` deciden
  qué más cruza, y nada de tu entorno cruza si no lo nombras. Los límites siguen aplicándose, impuestos por el runtime.
  Desactivado por defecto, y no es una frontera contra un agente hostil. Ver **[docs/ISOLATION.md](ISOLATION.md)**.
- **Apariencia** — `$HOME/.baton/TUI.yaml` remodela la cabina: un **tema** de color y las **disposiciones** de la división
  de grupo, recargadas en caliente con `C-t R`. Consulta **[docs/TUI.md](TUI.md)**.
- **Pie de uso** — `U` alterna un pie que muestra el uso de tokens y el coste del día (`⊙ 1.2M tok · ≈$12.34 API`). Por
  defecto lee las propias transcripciones de Claude Code (funciona con una suscripción Pro/Max) o la Admin API de
  Anthropic con una clave. El coste es el equivalente en API, no un cargo de la suscripción. Consulta
  **[docs/USAGE.md](USAGE.md)**.
- **Registro de paneles** — `C-t l` envía la salida de un panel a un archivo en la máquina donde corre la flota,
  volcando primero el búfer de repetición, de modo que se conserva justo aquello que te hizo pulsar la tecla; `C-t L`
  lo relee en un panel temporal que sigue el archivo. Texto plano, secuencias de escape eliminadas, rotación en
  `log-max-mb`. Desactivado hasta que definas `panel.log-dir`; un perfil puede registrar desde que arranca. Consulta
  **[docs/LOGGING.md](LOGGING.md)**.
- **Acceso remoto** — `baton --remote` conecta el mismo puesto de mando a una flota que corre en **otra máquina**, a
  través del ssh que ya usas para llegar a ella: sin puerto a la escucha, sin TLS y sin ningún intercambio de claves
  propio de baton. Desactivado por defecto; `settings.remote` o `C-t r` lo activa y acuña una passkey de 8 caracteres
  que nunca se escribe en disco. `C-t r` también lista cada conexión viva con su origen, su rol y su duración — `k`
  expulsa una, `n` renueva la passkey, `x` apaga el acceso remoto. Consulta **[docs/REMOTE.md](REMOTE.md)**.
- **Persistencia y renacimiento** — Baton recuerda su flota entre reinicios; los paneles vuelven como ranuras inertes ya
  terminadas y `r` los reejecuta a partir de la especificación que se conservó.
- **Recarga** — `C-t R` (o un `SIGHUP` al demonio) recarga la configuración en caliente sin reiniciar la flota.
- **Ratón** — desactivado por defecto para que la selección propia de tu terminal siga disponible; actívalo en el mapa de
  teclas para desplazarte y seleccionar con la rueda.
- **Idioma** — la lista de teclas de `?` y el mapa de teclas de `C-t k` se leen en inglés o en 繁體中文. Configura
  `settings.language`, rótalo en vivo desde el mapa de teclas o deja que lo decida tu `$LANG`. Consulta
  **[docs/TUI.md](TUI.md#language)**.

## Salvapantallas

Vete y déjalo reposar. Tras unos minutos de inactividad —o con el `C-t E` oculto— la cabina cae en una lluvia digital
Matrix a pantalla completa, con el logotipo **BATON** y un gran reloj flotando en el centro. Es una toma de control solo
del frontend: no se envía nada al servidor, y cualquier tecla o clic te devuelve directamente a tu vista.

![Salvapantallas de Baton: una lluvia digital Matrix con el logotipo BATON y un gran reloj](assets/baton-screensaver.png)

_Vídeo generado a partir de [`baton-screensaver.tape`](assets/baton-screensaver.tape); los pasos para regenerarlo están en la cabecera del tape._

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
  la lista de conexiones de `C-t r`, y los fallos que informa.
- **[docs/USAGE.md](USAGE.md)** — el pie de uso de la cuenta: las fuentes local y de Admin API, la configuración y las
  advertencias.
- **[docs/PLUGIN.md](PLUGIN.md)** — la API de plugins en Lua: el objeto `baton`, eventos, comandos y configuración.
- **[docs/CONTROL.md](CONTROL.md)** — manejar la flota mediante un agente: el conductor, la CLI `baton ctl`, las
  herramientas de `baton mcp` y las barreras de seguridad.

## DDD (Dream-Driven Development, desarrollo guiado por sueños)

Este proyecto sigue el DDD (desarrollo guiado por sueños): cada funcionalidad se construye a partir de lo que sueño y
necesito.
