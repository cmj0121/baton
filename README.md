# Baton

> A next-gen, extensible, agent-friendly terminal multiplexer.

Baton is to AI agents what tmux is to shells. Instead of juggling windows, tabs, and a dozen scattered CLI sessions,
you run a single keyboard-driven cockpit: a live dashboard of every agent you have running, with the ability to group
them into work items and zoom into any one on demand.

You hold the baton. The agents play. You conduct.

## Concept

- **Agents, not shells.** The unit of work is a running agent, not a window you have to babysit.
- **Dashboard, not windows.** You watch a live overview of everything at once, not a pile of tabs to remember.
- **Headless core, replaceable frontends.** The brain is a background daemon; the face that renders it is swappable.

The **Baton** is your collective control over every agent — one place to manage and interact with them all. Every panel
is an agent, or a shell panel you open to run commands on the host machine.

You always can see the overview of all your agents, and you can zoom into any one of them to see the details, or the shell
panel to run commands on the host machine. You can also group your agents into work items, and you can zoom into any panel
to focus on it.

### Core concepts

A small vocabulary. Everything in Baton is built from these few nouns.

| Concept       | What it is                                                                                           |
| ------------- | ---------------------------------------------------------------------------------------------------- |
| **Panel**     | One live terminal — an _agent_ panel (an agent CLI, run directly) or a _shell_ panel (a host shell). |
| **Work item** | A named group of panels that belong to one task or goal.                                             |
| **Dashboard** | The live grid overview of every panel, with status and a preview.                                    |
| **Zoom**      | Focus one panel (or work item) and drive it as your only terminal.                                   |
| **Baton**     | You, the conductor — the single point of control over every agent.                                   |

## Architecture

A headless **baton server** (a background daemon) owns all state and every terminal. Pluggable frontends attach over a
single Unix domain socket — sending commands up, receiving events down — so you can detach and reattach without losing
a thing. Every request, whether from the socket or from Lua, lands on one shared layer of core actions.

See [docs/SPEC.md](docs/SPEC.md) for the full diagram, the component breakdown, and the two-view interaction model.

## DDD (Dream-Driven Development)

This project is based on the DDD (dream-driven development) methodology which means
the project is based on what I dream of.

All the features are based on my needs and my dreams.
