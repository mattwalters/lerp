# Promoting: reading a plan in Plan Review, promoting to Implementing,
# and watching the work queue pick it up.
#
# Rendered by `make casts` into mp4 and webm for the docs site.
Source docs/tapes/house.tape

Output ".demo/out/promote.mp4"
Output ".demo/out/promote.webm"

Type "lerp"
Sleep 800ms
Enter

Wait+Screen /waiting to enter the pipeline/
Sleep 2s

# Select DEMO-25 in Plan Review and read the plan in the main pane.
Down
Wait+Screen /DEMO-25/
Sleep 1500ms
Enter
Wait+Screen /One page under docs/
Sleep 3s

# Press p to open the promote picker.
Type "p"
Wait+Screen /promote DEMO-25/
Sleep 1500ms

# Pick Implementing and confirm with enter.
Down 2
Wait+Screen /▸ Implementing/
Sleep 1500ms
Enter
Sleep 2s

# 2 across to the work panel to see DEMO-25 in the queue.
Type "2"
Wait+Screen /DEMO-25/
Sleep 4s

Type "q"
Sleep 2s
Hide
Sleep 4s
