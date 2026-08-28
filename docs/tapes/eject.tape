# Ejecting: stopping a running agent, taking the resume command, and freeing the lane.
#
# Rendered by `make casts` into mp4 and webm for the docs site.
Source docs/tapes/house.tape

Output ".demo/out/eject.mp4"
Output ".demo/out/eject.webm"

Type "lerp"
Sleep 800ms
Enter

Wait+Screen /waiting to enter the pipeline/
Sleep 2s

# 2 switches to the work panel, Up selects the first running lane.
Type "2"
Sleep 1s
Up 6
Sleep 1500ms

# e opens the eject confirmation overlay.
Type "e"
Wait+Screen /eject ACME-/
Wait+Screen /stops[ \t]+the agent in lane/
Sleep 3s

# enter confirms the eject; the resume command is displayed.
Enter
Wait+Screen /resume/
Wait+Screen /agent --resume/
Sleep 4s

# esc dismisses the overlay and returns to the work panel with the lane freed.
Escape
Wait+Screen /2\/3 running/
Sleep 3s

Type "q"
Sleep 2s
Hide
Sleep 4s
