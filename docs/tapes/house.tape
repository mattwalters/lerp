# Sourced first by every tape in this directory (never rendered itself — see
# the Makefile's render-tape recipe), so the terminal geometry, timing and
# typing speed have one spelling instead of one per cast.
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
# before failing the render — long enough for the slowest beat below (the
# stub agent's ~15s log replay) to land, short enough that a tape that will
# never match fails in one run rather than hanging the job.
Set WaitTimeout 10s

Hide
Type "clear"
Enter
Sleep 500ms
Show
