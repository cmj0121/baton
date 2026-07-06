# Baton README demo — recorded with VHS (https://github.com/charmbracelet/vhs).
#
# Regenerate the animated README hero (docs/assets/baton-demo.png) from the
# repo root:
#
#   vhs docs/assets/baton-demo.tape
#   ffmpeg -y -i baton-demo.mp4 \
#     -vf "fps=12,palettegen=max_colors=256:stats_mode=full" palette.png
#   ffmpeg -y -i baton-demo.mp4 -i palette.png \
#     -lavfi "fps=12[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=3" \
#     -plays 0 -f apng docs/assets/baton-demo.png
#
# VHS has no APNG output, so it renders an mp4 and the two-pass ffmpeg palette
# step turns it into a small, crisp, looping APNG. Needs: vhs, ttyd, ffmpeg.

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

# --- Dashboard: your home base -------------------------------------------
Sleep 2s

# Spawn a few panels — a live grid of everything at once
Type "p"
Sleep 1.4s
Type "p"
Sleep 1.4s
Type "p"
Sleep 1.8s

# Move the cursor around the grid
Type "l"
Sleep 0.7s
Type "h"
Sleep 0.9s

# --- Zoom: one panel as your only terminal -------------------------------
Enter
Sleep 1.2s
Type "echo 'a real terminal — drive it directly' && uname -sm"
Sleep 0.4s
Enter
Sleep 2s

# Step back out to the dashboard (leader C-t, then d)
Ctrl+T
Sleep 0.4s
Type "d"
Sleep 1.4s

# --- Work items: group panels into one task ------------------------------
Type "g"
Sleep 0.5s
Type "l"
Sleep 0.4s
Type "g"
Sleep 0.6s
Type "G"
Sleep 1s
Type "refactor-api"
Sleep 0.5s
Enter
Sleep 2s

# --- Help is always a keystroke away -------------------------------------
Type "?"
Sleep 3s
Escape
Sleep 1.5s
