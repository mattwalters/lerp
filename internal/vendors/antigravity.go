package vendors

import (
	"encoding/json"
	"strings"
)

// antigravity adapts Antigravity CLI (`agy`), headless: `agy -p {{prompt}}
// --output-format stream-json --add-dir {{workdir}}` streams every event as a
// JSON line while the run is live, the same reason claude's command carries
// --output-format stream-json.
//
// Two things this adapter alone needs, both found by running the installed
// CLI rather than assumed from its --help:
//
//   - `--add-dir {{workdir}}` is load-bearing, not decoration. Unlike every
//     other vendor lerp adapts, agy ignores the directory it is started in —
//     a probed run with cwd A and no --add-dir wrote its files under
//     ~/.gemini/antigravity-cli/scratch/ instead, never touching A. Without
//     the flag, a lane's agent edits land outside the lane's workspace,
//     invisible to the pipeline that is supposed to move the ticket on them.
//   - `--print-timeout` defaults to five minutes, which a real plan or
//     implement run routinely outlives; the adapter sets it far above that
//     default rather than letting the CLI's own guillotine end a live run.
//
// agy names its own conversation rather than accepting a caller-chosen id —
// unlike claude's --session-id, there is no flag to pre-assign one — so the
// command has no {{session}} placeholder. antigravity also implements
// SessionNamer for that reason: Session reads the id back off the run's own
// first line, the seam LERP-137 built for codex's identical shape.
type antigravity struct{}

// printTimeout is set far above agy's own five-minute default: a plan or
// implement run on this repo's own pipeline routinely takes longer than
// that, and a run the CLI itself kills at five minutes is indistinguishable
// from one that hung.
const printTimeout = "24h"

// Command appends --model and --effort, in that order, only when set, then
// Args verbatim and last — the same shape claude's Command follows, so an
// operator's args can still override an earlier flag on a last-wins CLI.
//
// No --dangerously-skip-permissions here or anywhere else in this adapter:
// like every vendor lerp ships, that grant reaches a command only by being
// written into a runner's checked-in, reviewed args.
func (antigravity) Command(o Options) string {
	parts := []string{"agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout " + printTimeout}
	if o.Model != "" {
		parts = append(parts, "--model "+quote(o.Model))
	}
	if o.Effort != "" {
		parts = append(parts, "--effort "+quote(o.Effort))
	}
	if o.Args != "" {
		parts = append(parts, o.Args)
	}
	return strings.Join(parts, " ")
}

// Resume opens the conversation agy itself named. Unlike claude's --resume,
// this does not depend on being run from the workspace it was started in — a
// probed resume from an unrelated cwd answered with the earlier conversation's
// context intact — but the `cd` still puts the operator in the workspace lerp
// left behind, which is the point of eject.
func (antigravity) Resume(Options) string {
	return "cd {{workdir}} && agy --conversation {{session}}"
}

// antigravityInitLine is the one event Session looks for, out of the whole
// stream internal/logfmt's antigravity decoder reads. It is unmarshalled
// here rather than through that decoder: the normalized Event exists for
// display, not for the raw id, and vendors imports nothing of lerp's
// regardless — the same reasoning codex.go's Session gives.
type antigravityInitLine struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
}

// Session reads one line of an antigravity run's log for the init event agy
// writes first, and returns the conversation id it named. It answers false
// for any other event, for prose that is not JSON at all, and for an init
// line that somehow carries no id.
func (antigravity) Session(line string) (string, bool) {
	var l antigravityInitLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return "", false
	}
	if l.Event != "init" || l.ConversationID == "" {
		return "", false
	}
	return l.ConversationID, true
}
