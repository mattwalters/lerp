package vendors

import "strings"

// claude adapts Claude Code, headless: `claude -p {{prompt}} --session-id
// {{session}} --output-format stream-json --verbose` streams every event as
// a JSON line while the run is live — without it, `claude -p` prints only
// the final result at exit, and the board's log tail stays empty for the
// whole run.
type claude struct{}

// Command appends --model and --effort, in that order, only when set, then
// Args verbatim and last — so an operator's args can override an earlier
// flag on a last-wins CLI. Model and Effort are values, so they are
// shell-quoted here: aliases like "sonnet[1m]" carry glob characters that
// run.Execute's placeholder expansion would otherwise pass through
// unescaped. Args is not quoted: it is shell text, the same as the command
// field it stands in for.
func (claude) Command(o Options) string {
	parts := []string{"claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose"}
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

// Resume opens the session in the directory Claude Code filed it under: the
// `cd` is load-bearing, since --resume pasted anywhere else would not find
// it.
func (claude) Resume(Options) string {
	return "cd {{workdir}} && claude --resume {{session}}"
}

// quote shell-quotes value for the command template it goes into. Duplicated
// from run.shellQuote: vendors imports nothing of lerp's, and run already
// imports config, which must import vendors — sharing the helper would
// cycle.
func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
