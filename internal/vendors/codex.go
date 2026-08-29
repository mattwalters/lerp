package vendors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// codex adapts the Codex CLI, headless: `codex exec --json` streams one JSON
// event per line while the run is live, which is what internal/logfmt's
// codex decoder reads — the board's log pane needs nothing extra for it.
//
// Codex names its own session instead of accepting one lerp chose, so codex
// also implements SessionNamer: Session reads a run's own log for the thread
// id Codex announced, rather than lerp minting one up front. See
// internal/run/session.go for where that read happens.
type codex struct{}

// CLIName returns the executable name for Codex.
func (codex) CLIName() string {
	return "codex"
}

// BypassArgs returns the flag string that grants unattended execution.
func (codex) BypassArgs() string {
	return "--dangerously-bypass-approvals-and-sandbox"
}

// MCPRegisterHTTP returns the command to register Linear MCP directly via HTTP.
func (codex) MCPRegisterHTTP() []string {
	return []string{"codex", "mcp", "add", "linear", "--url", "https://mcp.linear.app/mcp"}
}

// MCPRegisterBridge returns the command to register Linear MCP via the mcp-remote bridge.
func (codex) MCPRegisterBridge() []string {
	return []string{"codex", "mcp", "add", "linear", "--", "npx", "-y", "mcp-remote", "https://mcp.linear.app/mcp"}
}

// AuthInstruction returns instructions on how to authenticate after registration.
func (codex) AuthInstruction() string {
	return "codex mcp login linear"
}

type codexMCPConfig struct {
	MCPServers     map[string]any `toml:"mcp_servers"`
	MCPServersDash map[string]any `toml:"mcp-servers"`
}

// HasLinearMCP checks whether Codex has a Linear MCP server registered in
// ~/.codex/config.toml or repo-level codex config.
func (codex) HasLinearMCP(repoRoot string) bool {
	home, err := os.UserHomeDir()
	if err == nil {
		if checkCodexFile(filepath.Join(home, ".codex", "config.toml")) {
			return true
		}
	}
	if repoRoot != "" {
		if checkCodexFile(filepath.Join(repoRoot, ".codex", "config.toml")) {
			return true
		}
		if checkCodexFile(filepath.Join(repoRoot, "codex.toml")) {
			return true
		}
	}
	return false
}

func checkCodexFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg codexMCPConfig
	if err := toml.Unmarshal(data, &cfg); err == nil {
		for name, val := range cfg.MCPServers {
			if isLinearServer(name, val) {
				return true
			}
		}
		for name, val := range cfg.MCPServersDash {
			if isLinearServer(name, val) {
				return true
			}
		}
	}
	return false
}

// Command appends --model, then the effort override, then Args verbatim,
// then the prompt — positional on this CLI (`codex exec [OPTIONS] [PROMPT]`),
// so it has to follow the flags rather than fill one of them the way
// claude's does. The leading `--` keeps a prompt that opens with a dash from
// being parsed as one.
func (codex) Command(o Options) string {
	parts := []string{"codex exec --json"}
	if o.Model != "" {
		parts = append(parts, "--model "+quote(o.Model))
	}
	if o.Effort != "" {
		// Codex has no --effort flag; the equivalent is a config override, and
		// the whole key=value pair is one shell word.
		parts = append(parts, "-c "+quote("model_reasoning_effort="+o.Effort))
	}
	if o.Args != "" {
		parts = append(parts, o.Args)
	}
	parts = append(parts, "-- {{prompt}}")
	return strings.Join(parts, " ")
}

// Resume opens the session in the directory Codex filed it under: `codex
// resume` filters its sessions by working directory, the same reason the
// `cd` is load-bearing on claude's Resume.
func (codex) Resume(Options) string {
	return "cd {{workdir}} && codex resume {{session}}"
}

// codexThreadLine is the one event Session looks for, out of the whole
// stream `internal/logfmt`'s codex decoder reads. It is unmarshalled here
// rather than through that decoder: the normalized Event exists for display,
// not for the raw id, and vendors imports nothing of lerp's regardless.
type codexThreadLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
}

// Session reads one line of a codex run's log for the thread.started event
// Codex writes first, and returns the thread id it named. It answers false
// for any other event, for prose that is not JSON at all, and for a
// thread.started line that somehow carries no id.
func (codex) Session(line string) (string, bool) {
	var l codexThreadLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return "", false
	}
	if l.Type != "thread.started" || l.ThreadID == "" {
		return "", false
	}
	return l.ThreadID, true
}
