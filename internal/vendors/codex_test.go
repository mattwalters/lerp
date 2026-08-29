package vendors

import (
	"os"
	"strings"
	"testing"
)

func TestCodexCommandDefault(t *testing.T) {
	got := codex{}.Command(Options{})
	want := "codex exec --json -- {{prompt}}"
	if got != want {
		t.Errorf("Command(Options{}) = %q, want %q", got, want)
	}
}

func TestCodexCommandModelAndEffortOnlyWhenSet(t *testing.T) {
	got := codex{}.Command(Options{Model: "o3", Effort: "high"})
	want := "codex exec --json --model 'o3' -c 'model_reasoning_effort=high' -- {{prompt}}"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}

	got = codex{}.Command(Options{Effort: "high"})
	want = "codex exec --json -c 'model_reasoning_effort=high' -- {{prompt}}"
	if got != want {
		t.Errorf("Command with only Effort = %q, want %q", got, want)
	}
}

// A model alias can carry shell metacharacters, and the command this becomes
// is expanded and run by a shell — unquoted, that shell would try to
// interpret them.
func TestCodexCommandQuotesMetacharactersInModel(t *testing.T) {
	got := codex{}.Command(Options{Model: "o3[preview]"})
	want := "codex exec --json --model 'o3[preview]' -- {{prompt}}"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

// Args sits after the modeled flags and before the prompt, and is never
// quoted: it is shell text, not a value, the same rule claude's Args
// follows.
func TestCodexCommandArgsAppendedBeforePromptAndVerbatim(t *testing.T) {
	got := codex{}.Command(Options{Model: "o3", Args: "--sandbox workspace-write"})
	want := "codex exec --json --model 'o3' --sandbox workspace-write -- {{prompt}}"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

func TestCodexResume(t *testing.T) {
	got := codex{}.Resume(Options{Model: "o3"})
	want := "cd {{workdir}} && codex resume {{session}}"
	if got != want {
		t.Errorf("Resume = %q, want %q", got, want)
	}
}

func TestCodexSessionReadsThreadStarted(t *testing.T) {
	got, ok := codex{}.Session(`{"type":"thread.started","thread_id":"01a03575-0a83-7601-bdcc-1a734ee2b1b2"}`)
	if !ok || got != "01a03575-0a83-7601-bdcc-1a734ee2b1b2" {
		t.Errorf("Session = (%q, %v), want the thread id and true", got, ok)
	}
}

func TestCodexSessionIgnoresOtherEvents(t *testing.T) {
	lines := []string{
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"hi"}}`,
		`{"type":"turn.completed"}`,
		`{"type":"thread.started"}`, // no thread_id
		"not json at all",
		"",
	}
	for _, line := range lines {
		if got, ok := (codex{}).Session(line); ok {
			t.Errorf("Session(%q) = (%q, true), want it dropped", line, got)
		}
	}
}

func TestCodexCLINameAndMCPCommands(t *testing.T) {
	c := codex{}
	if got := c.CLIName(); got != "codex" {
		t.Errorf("CLIName() = %q, want %q", got, "codex")
	}
	if got := c.BypassArgs(); got != "--dangerously-bypass-approvals-and-sandbox" {
		t.Errorf("BypassArgs() = %q, want %q", got, "--dangerously-bypass-approvals-and-sandbox")
	}
	wantHTTP := "codex mcp add linear --url https://mcp.linear.app/mcp"
	if got := strings.Join(c.MCPRegisterHTTP(), " "); got != wantHTTP {
		t.Errorf("MCPRegisterHTTP() = %q, want %q", got, wantHTTP)
	}
	wantBridge := "codex mcp add linear -- npx -y mcp-remote https://mcp.linear.app/mcp"
	if got := strings.Join(c.MCPRegisterBridge(), " "); got != wantBridge {
		t.Errorf("MCPRegisterBridge() = %q, want %q", got, wantBridge)
	}
	if got := c.AuthInstruction(); got != "codex mcp login linear" {
		t.Errorf("AuthInstruction() = %q, want %q", got, "codex mcp login linear")
	}
}

func TestCodexHasLinearMCP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	c := codex{}
	if c.HasLinearMCP("") {
		t.Error("HasLinearMCP = true on empty home, want false")
	}

	codexDir := dir + "/.codex"
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := codexDir + "/config.toml"
	if err := os.WriteFile(cfgPath, []byte("[mcp_servers.linear]\nurl = \"https://mcp.linear.app/mcp\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !c.HasLinearMCP("") {
		t.Error("HasLinearMCP = false when ~/.codex/config.toml has linear, want true")
	}

	// In repo codex config
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	repoDir := t.TempDir()
	if err := os.WriteFile(repoDir+"/codex.toml", []byte("[mcp_servers.linear]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !c.HasLinearMCP(repoDir) {
		t.Error("HasLinearMCP = false when repo codex.toml has linear, want true")
	}
}
