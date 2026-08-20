# Baton README demo — recorded with VHS (https://github.com/charmbracelet/vhs).
#
# Regenerate the animated README hero (docs/assets/baton-demo.png) from the
# repo root:
#
#   vhs docs/assets/baton-demo.tape
#   ffmpeg -y -i baton-demo.mp4 \
#     -vf "fps=12,palettegen=max_colors=64:stats_mode=full" palette.png
#   ffmpeg -y -i baton-demo.mp4 -i palette.png \
#     -lavfi "fps=12[x];[x][1:v]paletteuse=dither=none" \
#     -plays 0 -f apng docs/assets/baton-demo.png
#
# VHS has no APNG output, so it renders an mp4 and the two-pass ffmpeg palette
# step turns it into a small, crisp, looping APNG. Needs: vhs, ttyd, ffmpeg.
#
# 64 colours and no dithering, because this is a terminal: the cockpit paints
# flat blocks out of a small palette, so a bigger one buys nothing, and dithering
# scatters noise across areas that are identical frame to frame — which is
# exactly what an APNG compresses. At 256 colours with bayer dithering the same
# take weighs 2.08MB; at 64 with none it is 1.36MB and the two are impossible to
# tell apart side by side.
#
# --- What this clip is about ---------------------------------------------
#
# The key list, three times over. `?` has three faces — one for the dashboard,
# one for a group split, one for a zoom — and the clip opens it in all three so
# the promise the README makes in words ("`?` is always there for the keys") is
# a thing you watch happen rather than a thing you are told.
#
# Everything else is arranged around that: the fleet is spawned so there is
# something to group, grouped so there is a split to open, and the split is
# zoomed so the third list has somewhere to be. The landing keys (`n`, `g`, `v`)
# are pressed with a pause on them, because the status bar naming a family is
# how a family is meant to be found.
#
# The recording runs on a throwaway $HOME with its agent CLI stood in for — see
# baton-demo.sh and demo-agent.sh for exactly what is and is not real.

Output baton-demo.gif
Output baton-demo.mp4

Set Shell bash
Set FontSize 15
Set Width 1200
Set Height 720
Set Padding 22
Set TypingSpeed 55ms
Set Framerate 24

# Launch the cockpit (isolated, fresh state). Run vhs from the repo root so
# this relative path resolves.
Hide
Type "bash docs/assets/baton-demo.sh"
Enter
Sleep 3.5s
Show

# --- Cold open: press ? before learning anything -------------------------
# An empty dashboard is the honest starting point, and the first key pressed on
# it is the one that explains every other key. The list is tabbed by purpose, so
# the arrows walk it: Navigation, then Panels — where the `n …` family sits
# indented under its landing — then Work items.
Sleep 1.2s
Type "?"
Sleep 1.3s
Right
Sleep 2.4s
Right
Sleep 1.9s
Escape
Sleep 0.8s

# --- Grow a fleet --------------------------------------------------------
# Two shells and an agent. `A` goes straight to the workdir prompt with the
# path already filled in, because the recording's machine has exactly one agent
# backend and a list of one is a keystroke, not a choice.
Type "p"
Sleep 1s
Type "p"
Sleep 1s
Type "A"
Sleep 1.1s
Enter
Sleep 1.8s

# Move the cursor around the grid
Type "l"
Sleep 0.5s
Type "h"
Sleep 0.6s

# --- The conductor, reached through the `n` landing -----------------------
# `n` does nothing on its own: it opens the spawns that are not the everyday `p`
# and `A`. The run so far shows as `n …` in the footer while it waits.
#
# (The which-key line that names the family is not filmed here. Its labels are
# full sentences — ~200 columns for this family — and statusBar drops the hint
# whole when it will not fit, so at this terminal size there is nothing to film.
# The Panels tab of the key list, two shots up, carries the family instead.)
Type "n"
Sleep 0.6s

# `C` finds-or-creates the singleton conductor and zooms it. The agent CLI is a
# stand-in (see demo-agent.sh), but the control wiring it shows is real: the
# `baton ctl tree` below goes over the same socket, under the same fenced
# conductor role, as a real conductor's would. The long sleep is for that call —
# sampling the OS process table takes a few seconds while vhs hogs the machine.
Type "C"
Sleep 6.5s

# Back out: the conductor is a mark in the FLEET heading, not a card.
Ctrl+T
Sleep 0.4s
Type "d"
Sleep 1.2s

# --- Group two panels into one work item ---------------------------------
# `g g` marks — the cheapest key in its family, the same finger twice — and
# `g c` creates the work item from what is marked.
Type "g"
Sleep 0.4s
Type "g"
Sleep 0.7s
Type "l"
Sleep 0.5s
Type "g"
Sleep 0.4s
Type "g"
Sleep 0.7s
Type "g"
Sleep 0.4s
Type "c"
Sleep 0.8s
Type "refactor-api"
Sleep 0.5s
Enter
Sleep 1.6s

# --- The second ?: a group split has its own list ------------------------
# `enter` on a work item opens its live split, every member streaming at once.
# The same `?` opens a different table here — tab, +/-, pin, interact — because
# the list is the view's, not the application's.
Enter
Sleep 1.6s
Type "?"
Sleep 2.2s
Escape
Sleep 0.7s

# --- The third ?: in a zoom the keys belong to the program ----------------
# `enter` drops into one panel as your only terminal, and the keystrokes go
# straight through to it. So the key list needs the leader in front of it —
# C-t ? — which is the whole modal rule in one keystroke.
Enter
Sleep 1.2s
Type "uname -sm"
Sleep 0.3s
Enter
Sleep 1.3s
Ctrl+T
Sleep 0.4s
Type "?"
Sleep 2.4s
Escape
Sleep 0.7s

# --- Close on the dashboard that scales ----------------------------------
# `v` is the landing for what the cockpit draws and never for what the fleet
# does: `v l` swaps the cards for the tree, `v p` puts a detail pane beside it.
Ctrl+T
Sleep 0.4s
Type "d"
Sleep 1.2s
Type "v"
Sleep 1s
Type "l"
Sleep 1.8s
Type "v"
Sleep 0.9s
Type "p"
Sleep 2.5s
