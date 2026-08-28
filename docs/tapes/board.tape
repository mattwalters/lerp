# Reading the board: opens on the inbox, reads a ticket in the main pane,
# switches across to the work panel, and opens a lane's live log tail.
#
# Rendered by `make casts` into mp4 and webm for the docs site.
Source docs/tapes/house.tape

Output ".demo/out/board.mp4"
Output ".demo/out/board.webm"

Type "lerp"
Sleep 800ms
Enter

# The board opens on the inbox: what waits on a human, at full width.
Wait+Screen /waiting to enter the pipeline/
Sleep 2s

# Down to ACME-25 in Plan Review, then enter to read its plan in the main pane.
Down
Wait+Screen /ACME-25/
Sleep 1500ms
Enter
Wait+Screen /One page under docs/
Sleep 3s
Escape
Sleep 2s

# 2 switches to the work panel; enter on a running lane opens its live tail.
Type "2"
Sleep 1s
Up 6
Sleep 1500ms
Enter
Wait+Screen /r raw/
Sleep 4s
Escape
Sleep 2s

# 1 back to the inbox.
Type "1"
Sleep 2s

Type "q"
Sleep 2s
Hide
Sleep 4s
