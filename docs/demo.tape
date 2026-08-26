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
#
# LERP_DEMO_EXIT is where the harness leaves its own exit status. vhs exits 0
# on whatever the recorded shell did, so without this a harness that dies at
# startup renders a cast of a bash error and passes; `make demo` reads the
# file and refuses. The path is .demo/exit on both sides — keep it in step
# with the Makefile's DEMO_EXIT.
#
# Typed single-quoted so the expansions can carry their own double quotes: a
# clone in a directory with a space in its name would otherwise word-split
# both assignments, and the render would fail as a harness that never reported
# a status rather than as the quoting bug it is.
Hide
Type 'export PATH="$PWD/.demo:$PATH" LERP_DEMO_EXIT="$PWD/.demo/exit"'
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

# Both lists own the screen until one is asked for a ticket. Down to a review
# that has landed, then `enter` to read it: the main pane opens beside the
# table with the ticket's body, its real Linear status and why it is sitting
# with a human, and `esc` gives the width back.
Down
Sleep 2500ms
Enter
Sleep 4500ms
Escape
Sleep 2s

# `2` focuses the work panel, which owns its screen the same way. Up to the
# first of the running lanes — Up clamps at the first row, so the count only
# has to be at least the distance — and `enter` opens that lane's log tail in
# the main pane, formatted from the agent's stream: thinking, tool calls, the
# result. Down walks the other lanes through the pane the same way.
Type "2"
Sleep 3s
Up 6
Sleep 2500ms
Enter
Sleep 4s
Down
Sleep 4s
Down
Sleep 4s

# The first three runs exit while their logs are on screen, their tickets move
# to "In Review", and the next three come off the queue into the lanes that
# just freed.
Sleep 5s

# `1` back to the inbox, where the finished tickets have just landed under
# "In Review — a run finished here", and the table is at full width again.
Type "1"
Sleep 4s
# Every run so far has landed here, which is the shape of the thing: the loop
# works the board down, and what it finishes stacks up waiting on a human.
Down
Sleep 3s

# What the panel has not been showing: the tickets that have not entered the
# pipeline yet, folded to the summary line at the foot of the table. `B`
# unfolds them in place — same panel, same table — and again folds them back,
# which is the whole of the control.
Type "B"
Sleep 3500ms
Type "B"
Sleep 2s

Type "q"
Sleep 2s

# Off the recording. Quitting is not instant — tui.Run waits out a
# reconciliation pass already in flight, and the harness removes its throwaway
# root behind that — and the exit status is written last of all. vhs tears the
# terminal down when the tape ends, so a margin too thin turns a good render
# into "the harness never reported a status". Frames after Hide are not
# recorded, so this costs the cast nothing.
Hide
Sleep 4s
