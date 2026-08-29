package vendors

import (
	"os"
	"strings"
	"testing"
)

func TestAntigravityCommandDefault(t *testing.T) {
	got := antigravity{}.Command(Options{})
	want := "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h"
	if got != want {
		t.Errorf("Command(Options{}) = %q, want %q", got, want)
	}
}

func TestAntigravityCommandModelAndEffortOnlyWhenSet(t *testing.T) {
	got := antigravity{}.Command(Options{Model: "gemini-3-pro", Effort: "high"})
	want := "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h" +
		" --model 'gemini-3-pro' --effort 'high'"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}

	got = antigravity{}.Command(Options{Effort: "high"})
	want = "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h --effort 'high'"
	if got != want {
		t.Errorf("Command with only Effort = %q, want %q", got, want)
	}
}

func TestAntigravityCommandQuotesMetacharactersInModel(t *testing.T) {
	got := antigravity{}.Command(Options{Model: "gemini[3]"})
	want := "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h --model 'gemini[3]'"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

// Args is appended last and verbatim, never quoted — the same rule claude's
// adapter follows, so an operator relies on it to override an earlier flag on
// agy's last-wins parsing.
func TestAntigravityCommandArgsAppendedLastAndVerbatim(t *testing.T) {
	got := antigravity{}.Command(Options{Model: "gemini-3-pro", Args: "--sandbox --print-timeout 1h0m0s"})
	want := "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h" +
		" --model 'gemini-3-pro' --sandbox --print-timeout 1h0m0s"
	if got != want {
		t.Errorf("Command = %q, want %q", got, want)
	}
}

// No permission-skipping flag anywhere in a generated command, under any
// combination of options: like every vendor lerp ships, that grant reaches a
// command only by being written into a runner's own checked-in args.
func TestAntigravityCommandNeverSkipsPermissions(t *testing.T) {
	got := antigravity{}.Command(Options{Model: "m", Effort: "high", Args: "--sandbox"})
	if want := "--dangerously-skip-permissions"; strings.Contains(got, want) {
		t.Errorf("Command = %q, contains %q", got, want)
	}
}

func TestAntigravityResume(t *testing.T) {
	got := antigravity{}.Resume(Options{})
	want := "cd {{workdir}} && agy --conversation {{session}}"
	if got != want {
		t.Errorf("Resume = %q, want %q", got, want)
	}
}

func TestAntigravitySessionReadsInit(t *testing.T) {
	line := `{"event":"init","conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98","init":{"cwd":"/tmp"}}`
	got, ok := antigravity{}.Session(line)
	if !ok || got != "ffd2f49a-85bf-45ab-bfad-80aed96a9b98" {
		t.Errorf("Session = (%q, %v), want the conversation id and true", got, ok)
	}
}

func TestAntigravitySessionIgnoresOtherLines(t *testing.T) {
	lines := []string{
		`{"event":"step_update","step_update":{"conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98"}}`,
		`{"event":"result","result":{"conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98"}}`,
		`{"event":"init"}`,
		"not json",
		"",
	}
	for _, line := range lines {
		if got, ok := (antigravity{}).Session(line); ok {
			t.Errorf("Session(%q) = (%q, true), want it dropped", line, got)
		}
	}
}

func TestAntigravityImplementsSessionNamer(t *testing.T) {
	var _ SessionNamer = antigravity{}
}

func TestAntigravityRegisteredByName(t *testing.T) {
	a, ok := Lookup("antigravity")
	if !ok {
		t.Fatal(`Lookup("antigravity") = false, want true`)
	}
	if _, ok := a.(SessionNamer); !ok {
		t.Error("the registered antigravity adapter does not implement SessionNamer")
	}
}

func TestAntigravityCLINameAndMCPCommands(t *testing.T) {
	a := antigravity{}
	if got := a.CLIName(); got != "agy" {
		t.Errorf("CLIName() = %q, want %q", got, "agy")
	}
	if got := a.BypassArgs(); got != "--dangerously-skip-permissions" {
		t.Errorf("BypassArgs() = %q, want %q", got, "--dangerously-skip-permissions")
	}
	wantHTTP := "agy mcp add linear https://mcp.linear.app/mcp"
	if got := strings.Join(a.MCPRegisterHTTP(), " "); got != wantHTTP {
		t.Errorf("MCPRegisterHTTP() = %q, want %q", got, wantHTTP)
	}
	wantBridge := "agy mcp add linear -- npx -y mcp-remote https://mcp.linear.app/mcp"
	if got := strings.Join(a.MCPRegisterBridge(), " "); got != wantBridge {
		t.Errorf("MCPRegisterBridge() = %q, want %q", got, wantBridge)
	}
	if got := a.AuthInstruction(); got != "the /mcp overlay in agy" {
		t.Errorf("AuthInstruction() = %q, want %q", got, "the /mcp overlay in agy")
	}
}

func TestAntigravityHasLinearMCP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	a := antigravity{}
	if a.HasLinearMCP("") {
		t.Error("HasLinearMCP = true on empty home, want false")
	}

	// In ~/.gemini/antigravity-cli/mcp/linear directory
	mcpDir := dir + "/.gemini/antigravity-cli/mcp/linear"
	if err := os.MkdirAll(mcpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.HasLinearMCP("") {
		t.Error("HasLinearMCP = false when ~/.gemini/antigravity-cli/mcp/linear exists, want true")
	}

	// In ~/.gemini/antigravity-cli/mcp_oauth_tokens.json
	if err := os.RemoveAll(dir + "/.gemini"); err != nil {
		t.Fatal(err)
	}
	geminiDir := dir + "/.gemini/antigravity-cli"
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(geminiDir+"/mcp_oauth_tokens.json", []byte(`{"https://mcp.linear.app/mcp": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !a.HasLinearMCP("") {
		t.Error("HasLinearMCP = false when mcp_oauth_tokens.json has linear, want true")
	}

	// In repo .gemini/mcp/linear
	if err := os.WriteFile(geminiDir+"/mcp_oauth_tokens.json", []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repoDir := t.TempDir()
	if err := os.MkdirAll(repoDir+"/.gemini/mcp/linear", 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.HasLinearMCP(repoDir) {
		t.Error("HasLinearMCP = false when repo .gemini/mcp/linear exists, want true")
	}
}
