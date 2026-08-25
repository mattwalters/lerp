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

# A pause between lines, so the board's log tail scrolls the way it does under
# a real agent instead of arriving in one frame. Ticket identifiers are
# [A-Z]+-[0-9]+, so nothing in the substitution can be read as sed syntax.
sed "s/__TICKET__/$LERP_TICKET/g" "$LERP_DEMO_DIR/agent.log" |
	while IFS= read -r line; do
		printf '%s\n' "$line"
		sleep 0.4
	done
