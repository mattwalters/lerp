# Finding tickets: sorting modes, project filtering, searching, and status slices.
#
# Rendered by `make casts` into mp4 and webm for the docs site.
Source docs/tapes/house.tape

Output ".demo/out/finding.mp4"
Output ".demo/out/finding.webm"

Type "lerp"
Sleep 800ms
Enter

Wait+Screen /Backlog \d+ · /
Wait+Screen /waiting to enter the pipeline/
Sleep 2s

# s cycles the sort mode: by project, by leverage, by priority, and back to status.
Type "s"
Wait+Screen /by project/
Sleep 1500ms
Type "s"
Wait+Screen /by leverage/
Sleep 1500ms
Type "s"
Wait+Screen /by priority/
Sleep 1500ms
Type "s"
Wait+Screen /by status/
Sleep 1500ms

# P opens the project filter shortcut directly.
Type "P"
Wait+Screen /v0.4/
Sleep 1500ms
Down
Enter
Wait+Screen /v0.4/
Sleep 2s

# F reopens the filter to clear it back to all projects.
Type "F"
Wait+Screen /all project/
Sleep 1500ms
Up
Enter
Sleep 1500ms

# / searches across identifier, title, status and project.
Type "/"
Wait+Screen /\/.*filter the inbox/
Type "sanit"
Wait+Screen /DEMO-21/
Sleep 2s
Escape
Sleep 1500ms

# ] and [ cycle through Linear status slices in board order.
Type "]"
Wait+Screen /· Triage/
Sleep 1500ms
Type "]"
Wait+Screen /· Backlog/
Sleep 1500ms
Type "]"
Wait+Screen /· Plan Review/
Sleep 1500ms
Type "["
Wait+Screen /· Backlog/
Sleep 1200ms
Type "["
Wait+Screen /· Triage/
Sleep 1200ms
Type "["
Sleep 1500ms

Type "q"
Sleep 2s
Hide
Sleep 4s
