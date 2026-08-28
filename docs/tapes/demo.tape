# lerp's README cast, and the site's demo video — one recording, three
# formats. `make demo` renders the GIF (the README's committed asset);
# `make casts` renders every tape in this directory, this one included, into
# mp4/webm for the docs site. Neither writes anywhere a human would notice
# until the render is under its size cap — see the Makefile.
#
# What is being recorded is the real TUI over the real reconciler
# (internal/demo): only Linear and the coding agent are faked, so a change to
# the panes or the loop that breaks this tape breaks it loudly. The
# Wait+Screen lines below are that guarantee at the level of a single beat: a
# keystroke that no longer drives the UI it was written against times out and
# fails the render, where a Sleep only records the wrong thing and exits 0.
# The remaining Sleeps are dwell time for a reader, not synchronization, and
# carry margin.
#
# The loop polls, so nothing below is frame-exact. The stub agent replays a
# ~15s log, which sets the rhythm: three lanes fill at once, run together,
# and turn over roughly every fifteen seconds.
Source docs/tapes/house.tape

Output ".demo/out/demo.gif"
Output ".demo/out/demo.mp4"
Output ".demo/out/demo.webm"

Type "lerp"
Sleep 800ms
Enter

# The board opens on the summary strip — each stage and its ticket count in
# board order — above the inbox: what waits on a human, at full width, with
# every column the table packs in — the leverage, the real Linear status, the
# project, the priority. Below it the work panel, three lanes already picking
# tickets up. The folded backlog line is the last thing the inbox draws, so
# waiting for it is waiting for the whole first frame.
Wait+Screen /Backlog \d+ · /
Wait+Screen /waiting to enter the pipeline/
Screenshot ".demo/out/og.png"
Sleep 3s

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

# `2` focuses the work panel, which owns its screen the same way — both
# panels draw every frame regardless of focus, so nothing about this
# keystroke alone is worth waiting on; the panel's own row count is already
# known (Up clamps at the first row, so the count only has to be at least
# the distance).
Type "2"
Sleep 1s
Up 6
Sleep 2500ms

# `enter` opens that lane's log tail in the main pane, formatted from the
# agent's stream: thinking, tool calls, the result. The footer's `r raw`
# hint (keymap.panelHelp, drawn by model.panelBody) only appears while a
# lane's log pane is open — "o open" otherwise — so waiting for it is
# waiting for the pane to have actually opened on a running lane rather
# than guessing how long that takes. Down walks the other lanes through
# the pane the same way.
Enter
Wait+Screen /r raw/
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
# "In Review — a run finished here". The inbox already seeds two tickets at
# "In Review" before any run finishes, so "In Review" alone is on screen
# from the first frame and proves nothing here — the wait instead names
# DEMO-24/26/27, the three tickets running above (internal/demo/main.go's
# board, in identifier order — linear.Fake.ListIssues sorts by identifier,
# and DEMO-22 is blocked, so these three are always the first wave),
# immediately followed on their own row by the status it lands in, which
# only that ticket's own move can produce. [ \t] rather than \s so the
# match cannot stretch across a line break onto some other row's
# "In Review".
Type "1"
Wait+Screen /DEMO-2[467][ \t]+\S+[ \t]+In Review/
Sleep 3s
# Every run so far has landed here, which is the shape of the thing: the loop
# works the board down, and what it finishes stacks up waiting on a human.
Down
Sleep 3s

# The inbox browses stages in Linear board order: `]` cycles through the
# status slices — all, Triage, Backlog, Plan Review — and `[` walks them
# back. Slices are display over the list already fetched.
Type "]"
Wait+Screen /· Triage/
Sleep 2s
Type "]"
Wait+Screen /· Backlog/
Sleep 2500ms
Type "]"
Wait+Screen /· Plan Review/
Sleep 2500ms
Type "["
Wait+Screen /· Backlog/
Sleep 1500ms
Type "["
Wait+Screen /· Triage/
Sleep 1500ms
Type "["
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
