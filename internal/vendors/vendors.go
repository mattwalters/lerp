package vendors

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// Options carries what a vendor runner block may set: Model and Effort add
// the matching flag when non-empty and are otherwise left to the vendor
// CLI's own default, and Args is the escape valve for a flag the adapter
// doesn't model, spliced in verbatim.
type Options struct {
	Model  string
	Effort string
	Args   string
}

// Adapter turns Options into the command and resume templates a vendor
// runner resolves to — the same shape a hand-written config.Runner fills,
// placeholders and all, so everything downstream needs no case for vendors.
type Adapter interface {
	Command(Options) string
	Resume(Options) string
	CLIName() string
	BypassArgs() string
	HasLinearMCP(repoRoot string) bool
	MCPRegisterHTTP() []string
	MCPRegisterBridge() []string
	AuthInstruction() string
}

// SessionNamer is implemented by an adapter whose CLI names its own session
// instead of accepting one lerp chose. Session reads a line of the run's log
// for that name. It exists so internal/run can ask a capability question
// instead of carrying a vendor's name in an if.
type SessionNamer interface {
	Session(line string) (string, bool)
}

// AbortReporter is implemented by an adapter whose CLI writes a plain-text
// abort notice to standard error (landing in the run's log) on a failure path
// that exits 0 anyway. Aborted reads a line for that notice and returns the
// reason to report to the operator. It exists so internal/loop can check for
// vendor abort signals without carrying vendor names or vendor-specific string
// matching in reconciler logic.
type AbortReporter interface {
	Aborted(line string) (string, bool)
}

// adapters is the whole set of vendors lerp ships. Adding one is one file
// plus one entry here — logfmt's detect carries the same comment on the
// decoding side. A map rather than a switch is what keeps Names from
// drifting away from what Lookup accepts.
var adapters = map[string]Adapter{
	"claude":      claude{},
	"codex":       codex{},
	"antigravity": antigravity{},
}

// Lookup returns the adapter registered under name.
func Lookup(name string) (Adapter, bool) {
	a, ok := adapters[name]
	return a, ok
}

// Names lists the known vendor names, sorted, for an error message that
// names what config could have said instead.
func Names() []string {
	return slices.Sorted(maps.Keys(adapters))
}

// isLinearServer reports whether an MCP server name or configuration value
// references Linear MCP (by server name, URL, or command/args).
func isLinearServer(name string, val any) bool {
	if strings.Contains(strings.ToLower(name), "linear") {
		return true
	}
	if val == nil {
		return false
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return false
	}
	s := strings.ToLower(string(raw))
	return strings.Contains(s, "mcp.linear.app") || strings.Contains(s, "linear")
}
