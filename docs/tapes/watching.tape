# Watching a run: live log tailing, scrollback, raw output toggle, and following.
#
# Rendered by `make casts` into mp4 and webm for the docs site.
Source docs/tapes/house.tape

Output ".demo/out/watching.mp4"
Output ".demo/out/watching.webm"

Type "lerp"
Sleep 800ms
Enter

Wait+Screen /Backlog \d+ · /
Wait+Screen /waiting to enter the pipeline/
Sleep 2s

# 2 switches to the work panel, Up selects the first running lane.
Type "2"
Sleep 1s
Up 6
Sleep 1500ms

# enter opens the live tail in the main pane.
Enter
Wait+Screen /r raw/
Sleep 5s

# pgup scrolls up into history.
PageUp
Sleep 2500ms

# r toggles the raw JSON stream view.
Type "r"
Wait+Screen /"type":/
Sleep 3s

# r toggles back to the formatted agent log.
Type "r"
Wait+Screen /r raw/
Sleep 2s

# G resumes following the live stream.
Type "G"
Sleep 2500ms

# esc closes the log pane.
Escape
Sleep 2s

Type "q"
Sleep 2s
Hide
Sleep 4s
