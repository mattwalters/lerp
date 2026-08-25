#!/bin/sh
# The demo's coding agent. It replays a checked-in Claude stream-json log line
# by line and exits 0 — no network, no model, no cost, and the same frames
# every time the cast is re-recorded.
#
# LERP_DEMO_DIR is where the harness wrote this script and its fixture;
# LERP_TICKET is what lerp exports for every run, and the only thing that
# tells one lane's replay from another's.
set -eu

: "${LERP_DEMO_DIR:?the demo harness must export the demo root}"
: "${LERP_TICKET:?lerp must export the ticket identifier}"

log="$LERP_DEMO_DIR/agent.log"
[ -r "$log" ] || {
	echo "demo agent: cannot read $log" >&2
	exit 1
}

# Substituted up front rather than piped into the loop below. A pipeline's exit
# status is its right-hand side's, and POSIX sh has no pipefail, so a sed that
# failed would still have exited 0 — recording a lane that "succeeded" with an
# empty log pane, which is the one failure this stub can have. Ticket
# identifiers are [A-Z]+-[0-9]+, so nothing substituted can be read as sed
# syntax.
replay=$(sed "s/__TICKET__/$LERP_TICKET/g" "$log")

# A pause between lines, so the board's log tail scrolls the way it does under
# a real agent instead of arriving in one frame.
while IFS= read -r line; do
	printf '%s\n' "$line"
	sleep 0.4
done <<REPLAY
$replay
REPLAY
