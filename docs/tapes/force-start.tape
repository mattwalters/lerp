# Starting past the limit: force-starting a queued ticket past the lane capacity.
#
# Rendered by `make casts` into mp4 and webm for the docs site.
Source docs/tapes/house.tape

Output ".demo/out/force-start.mp4"
Output ".demo/out/force-start.webm"

Type "lerp"
Sleep 800ms
Enter

Wait+Screen /3\/3 running/

# 2 switches to the work panel.
Type "2"
Sleep 1s
Up 6
Sleep 500ms

# Down to ACME-32 queued in the plan queue.
Down 4
Wait+Screen /ACME-32/
Sleep 1500ms

# Shift+S force-starts the ticket past the lane limit.
Type "S"
Wait+Screen /· \+1 over/
Sleep 4s

Type "q"
Sleep 2s
Hide
Sleep 4s
