package vendors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// claude adapts Claude Code, headless: `claude -p {{prompt}} --session-id
// {{session}} --output-format stream-json --verbose` streams every event as
// a JSON line while the run is live — without it, `claude -p` prints only
// the final result at exit, and the board's log tail stays empty for the
// whole run.
type claude struct{}

// CLIName returns the executable name for Claude Code.
func (claude) CLIName() string {
	return "claude"
}

// MCPRegisterHTTP returns the command to register Linear MCP directly via HTTP.
func (claude) MCPRegisterHTTP() []string {
	return []string{"claude", "mcp", "add", "--transport", "http", "linear", "https://mcp.linear.app/mcp"}
}

// MCPRegisterBridge returns the command to register Linear MCP via the mcp-remote bridge.
func (claude) MCPRegisterBridge() []string {
	return []string{"claude", "mcp", "add", "linear", "--", "npx", "-y", "mcp-remote", "https://mcp.linear.app/mcp"}
}

// AuthInstruction returns instructions on how to authenticate after registration.
func (claude) AuthInstruction() string {
	return "/mcp in claude"
}

type claudeConfigFile struct {
	McpServers               map[string]any `json:"mcpServers"`
	ClaudeAiMcpEverConnected []string       `json:"claudeAiMcpEverConnected"`
	Projects                 map[string]struct {
		McpServers map[string]any `json:"mcpServers"`
	} `json:"projects"`
}

// HasLinearMCP checks whether Claude Code has a Linear MCP server registered
// in ~/.claude.json, ~/.claude/, or project-level .mcp.json.
func (claude) HasLinearMCP(repoRoot string) bool {
	home, err := os.UserHomeDir()
	if err == nil {
		if checkClaudeFile(filepath.Join(home, ".claude.json")) {
			return true
		}
		if checkClaudeFile(filepath.Join(home, ".claude", "settings.json")) {
			return true
		}
		if checkClaudeFile(filepath.Join(home, ".claude", "mcp.json")) {
			return true
		}
	}
	if repoRoot != "" {
		if checkClaudeFile(filepath.Join(repoRoot, ".mcp.json")) {
			return true
		}
	}
	return false
}

func checkClaudeFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg claudeConfigFile
	if err := json.Unmarshal(data, &cfg); err == nil {
		for name, val := range cfg.McpServers {
			if isLinearServer(name, val) {
				return true
			}
		}
		for _, name := range cfg.ClaudeAiMcpEverConnected {
			if strings.Contains(strings.ToLower(name), "linear") || strings.Contains(name, "mcp.linear.app") {
				return true
			}
		}
		for _, p := range cfg.Projects {
			for name, val := range p.McpServers {
				if isLinearServer(name, val) {
					return true
				}
			}
		}
	}
	// Fallback to searching raw json for generic maps or mcp server definitions
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err == nil {
		if servers, ok := raw["mcpServers"].(map[string]any); ok {
			for name, val := range servers {
				if isLinearServer(name, val) {
					return true
				}
			}
		}
	}
	return false
}

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
