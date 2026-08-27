package vendors

import (
	"os"
	"strings"
	"testing"
)

func TestClaudeCommandDefault(t *testing.T) {
	got := claude{}.Command(Options{})
	want := "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose"
	if got != want {
		t.Errorf("Command(Options{}) = %q, want %q", got, want)
	}
}

func TestClaudeCommandModelAndEffortOnlyWhenSet(t *testing.T) {
	got := claude{}.Command(Options{Model: "sonnet", Effort: "high"})
	want := "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose" +
		" --model 'sonnet' --effort 'high'"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}

	got = claude{}.Command(Options{Effort: "high"})
	want = "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose --effort 'high'"
	if got != want {
		t.Errorf("Command with only Effort = %q, want %q", got, want)
	}
}

// A model alias can carry glob characters ("sonnet[1m]"), and the command
// this becomes is expanded and run by a shell — unquoted, that shell would
// try to glob it.
func TestClaudeCommandQuotesMetacharactersInModel(t *testing.T) {
	got := claude{}.Command(Options{Model: "sonnet[1m]"})
	want := "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose --model 'sonnet[1m]'"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

// Args is appended last and verbatim, never quoted: it is shell text, not a
// value, and an operator relies on that to override an earlier flag on a
// last-wins CLI.
func TestClaudeCommandArgsAppendedLastAndVerbatim(t *testing.T) {
	got := claude{}.Command(Options{Model: "sonnet", Args: "--allowedTools Read --model opus"})
	want := "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose" +
		" --model 'sonnet' --allowedTools Read --model opus"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestClaudeResume(t *testing.T) {
	got := claude{}.Resume(Options{Model: "sonnet"})
	want := "cd {{workdir}} && claude --resume {{session}}"
	if got != want {
		t.Errorf("Resume = %q, want %q", got, want)
	}
}

func TestClaudeCLINameAndMCPCommands(t *testing.T) {
	c := claude{}
	if got := c.CLIName(); got != "claude" {
		t.Errorf("CLIName() = %q, want %q", got, "claude")
	}
	wantHTTP := "claude mcp add --transport http linear https://mcp.linear.app/mcp"
	if got := strings.Join(c.MCPRegisterHTTP(), " "); got != wantHTTP {
		t.Errorf("MCPRegisterHTTP() = %q, want %q", got, wantHTTP)
	}
	wantBridge := "claude mcp add linear -- npx -y mcp-remote https://mcp.linear.app/mcp"
	if got := strings.Join(c.MCPRegisterBridge(), " "); got != wantBridge {
		t.Errorf("MCPRegisterBridge() = %q, want %q", got, wantBridge)
	}
	if got := c.AuthInstruction(); got != "/mcp in claude" {
		t.Errorf("AuthInstruction() = %q, want %q", got, "/mcp in claude")
	}
}

func TestClaudeHasLinearMCP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	c := claude{}
	if c.HasLinearMCP("") {
		t.Error("HasLinearMCP = true on empty home, want false")
	}

	// In ~/.claude.json mcpServers
	cfgPath := dir + "/.claude.json"
	if err := os.WriteFile(cfgPath, []byte(`{"mcpServers": {"linear": {"type": "http", "url": "https://mcp.linear.app/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !c.HasLinearMCP("") {
		t.Error("HasLinearMCP = false when ~/.claude.json has linear mcpServers, want true")
	}

	// In claudeAiMcpEverConnected
	if err := os.WriteFile(cfgPath, []byte(`{"claudeAiMcpEverConnected": ["claude.ai Linear"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !c.HasLinearMCP("") {
		t.Error("HasLinearMCP = false when ~/.claude.json has claudeAiMcpEverConnected, want true")
	}

	// In repo .mcp.json
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repoDir := t.TempDir()
	if err := os.WriteFile(repoDir+"/.mcp.json", []byte(`{"mcpServers": {"linear": {}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !c.HasLinearMCP(repoDir) {
		t.Error("HasLinearMCP = false when repo .mcp.json has linear, want true")
	}
}
