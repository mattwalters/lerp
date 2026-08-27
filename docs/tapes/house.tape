# Sourced first by every tape in this directory (never rendered itself — see
# the Makefile's render-tape recipe), so the terminal geometry, timing and
# typing speed have one spelling instead of one per cast. `Source` and every
# tape's `Output` are resolved against vhs's own working directory, not this
# file's — render-tape always runs vhs from the repo root, which is what a
# tape's own `Source docs/tapes/house.tape` line assumes.
#
# Require reads the environment the render-tape recipe invokes vhs with, not
# the recorded shell's — it runs as a preflight before a single keystroke is
# typed — which is also what makes the preamble below trivial. The recipe
# puts the harness binary on PATH and points LERP_DEMO_EXIT at its own
# scratch file before it ever calls vhs, and the shell vhs spawns to record
# inherits both, so there is nothing left for a tape to export and no
# directory-with-a-space quoting to defend against.
Require lerp

Set Shell "bash"
Set FontSize 14
Set Width 1200
Set Height 640
Set Padding 10
Set Framerate 16
Set TypingSpeed 60ms
# A Wait or Wait+Screen line that never finds its target waits this long
# before failing the render — above the stub agent's own ~15s log replay
# (internal/demo/agent.sh), so a tape that anchors on something the replay
# only writes near its end still gets there, and still short enough that a
# tape that will never match fails in one run rather than hanging the job.
Set WaitTimeout 20s

Hide
Type "clear"
Enter
Sleep 500ms
Show
