# Baton screensaver demo — recorded with VHS (https://github.com/charmbracelet/vhs).
#
# Regenerate the animated Matrix-rain clip (docs/assets/baton-screensaver.png)
# from the repo root:
#
#   vhs docs/assets/baton-screensaver.tape
#   ffmpeg -y -i baton-screensaver.mp4 \
#     -vf "fps=12,palettegen=max_colors=256:stats_mode=full" palette.png
#   ffmpeg -y -i baton-screensaver.mp4 -i palette.png \
#     -lavfi "fps=12[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=3" \
#     -plays 0 -f apng docs/assets/baton-screensaver.png
#
# VHS has no APNG output, so it renders an mp4 and the two-pass ffmpeg palette
# step turns it into a small, crisp, looping APNG. Needs: vhs, ttyd, ffmpeg.

Output baton-screensaver.gif
Output baton-screensaver.mp4

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

# --- Dashboard: a couple of panels so the saver has a live view to cover ---
Sleep 1.5s
Type "p"
Sleep 1.2s
Type "p"
Sleep 1.6s

# --- Summon the hidden Matrix-rain screensaver (leader C-t, then E) --------
Ctrl+T
Sleep 0.4s
Type "E"

# Let the digital rain fall around the BATON wordmark and the big clock.
Sleep 8s

# --- Any key dismisses it, restoring the view it took over ----------------
Space
Sleep 2s
