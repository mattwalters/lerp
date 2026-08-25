# lerp's README cast. Render it with `make demo`, which builds the demo
# harness and hands this file to vhs; nothing else regenerates docs/demo.gif.
#
# What is being recorded is the real TUI over the real reconciler
# (internal/demo): only Linear and the coding agent are faked, so a change to
# the panes or the loop that breaks this tape breaks it loudly.
#
# The loop polls, so nothing below is frame-exact. Every Sleep carries margin.
# The stub agent replays a ~15s log, which sets the rhythm: three lanes fill
# at once, run together, and turn over roughly every fifteen seconds.

# `make demo` overrides this with `vhs -o`, rendering into the scratch dir and
# moving the result here only once it is under the size cap. Running vhs on
# this file by hand writes the committed asset straight out, uncapped.
Output docs/demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1200
Set Height 640
Set Padding 10
Set Framerate 16
Set TypingSpeed 60ms

# The harness builds as `lerp` into .demo. Putting that on PATH is what makes
# the prompt below read the way a reader's own would.
Hide
Type "export PATH=$PWD/.demo:$PATH"
Enter
Type "clear"
Enter
Sleep 500ms
Show

Type "lerp"
Sleep 800ms
Enter

# The board opens on the inbox: what waits on a human, at full width, with
# every column the table packs in — the leverage, the real Linear status, the
# project, the priority. Below it the work panel, three lanes already picking
# tickets up.
Sleep 5s

# Down to a review that has landed, then `enter` to read it. The list owns the
# screen until you ask for the ticket; the main pane opens beside the table
# with the ticket's body, and `esc` gives the width back.
Down
Sleep 2500ms
Down
Sleep 3s
Enter
Sleep 4500ms
Escape
Sleep 2s

# `2` focuses the work panel, whose pane is open by default — no `enter`
# behind the log. Up to the first of the running lanes, then down through
# them. Each one opens its own log tail, formatted from the agent's stream —
# thinking, tool calls, the result. (Up clamps at the first row, so the count
# only has to be at least the distance.)
Type "2"
Sleep 3s
Up 6
Sleep 4s
Down
Sleep 4s
Down
Sleep 4s

# The first three runs exit, their tickets move to "In Review", and the next
# three come off the queue into the lanes that just freed.
Sleep 5s

# `1` back to the inbox, where the finished tickets have just landed under
# "In Review — a run finished here", and the table is at full width again.
Type "1"
Sleep 4s
Down
Sleep 3s

Type "q"
Sleep 2s
