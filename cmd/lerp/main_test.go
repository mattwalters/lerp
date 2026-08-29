package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/update"
	"github.com/mattwalters/lerp/internal/version"
)

// The operator's surface says concurrency, never lane. Lane stays the
// internal noun and the evidence record's field; two names for one number
// is exactly the clutter this project refuses.
func TestUsageDoesNotSayLane(t *testing.T) {
	if strings.Contains(strings.ToLower(usage), "lane") {
		t.Fatalf("usage names a lane:\n%s", usage)
	}
}

// Both new subcommands are discoverable from -h, not just from reading the
// source.
func TestUsageListsLoginAndLogout(t *testing.T) {
	for _, want := range []string{"lerp login", "lerp logout"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage does not mention %q:\n%s", want, usage)
		}
	}
}

// normalizeArgs is the only thing standing between --version and falling
// through to the bare-TUI flag set as an unknown flag; this pins the rewrite
// it does, though main still has to call it before its switch for the alias
// to reach `version` in practice.
func TestNormalizeArgsAliasesVersionFlag(t *testing.T) {
	got := normalizeArgs([]string{"--version"})
	if len(got) != 1 || got[0] != "version" {
		t.Errorf("normalizeArgs([--version]) = %v, want [version]", got)
	}
}

func TestNormalizeArgsLeavesOtherArgsAlone(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"init", "--team", "LERP"}, {"-concurrency", "3"}} {
		got := normalizeArgs(args)
		if len(got) != len(args) {
			t.Errorf("normalizeArgs(%v) = %v, want unchanged", args, got)
			continue
		}
		for i := range args {
			if got[i] != args[i] {
				t.Errorf("normalizeArgs(%v) = %v, want unchanged", args, got)
			}
		}
	}
}

// cliPage is the docs' reference page for the command line, which opens
// with this usage text verbatim.
const cliPage = "docs/content/docs/cli.md"

// The docs quote the usage block as the whole of lerp's surface, and a
// quoted string with nothing holding it to its source goes stale on the
// first flag, with a green gate — the same reasoning that pins the
// skipped-hop note to the page that quotes it, and lerp.example.toml to the
// stock config. Add a subcommand or move -concurrency's default and this
// fails here rather than on a reader's screen.
//
// The block is found by its own first line rather than by position, so
// rewriting the prose around it costs nothing.
func TestTheDocsQuoteTheUsage(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", cliPage))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), strings.TrimSuffix(usage, "\n")) {
		t.Errorf("%s does not quote main.go's usage. It should read:\n\n%s\n"+
			"main.go is the source. Change the usage there, then update the\n"+
			"block that opens %s.", cliPage, usage, cliPage)
	}
}

// A startup warning that scrolls past unread is the same as no warning: the
// TUI takes the alternate screen a moment later. So announce holds the run
// until the operator acknowledges it.
func TestAnnounceWaitsForTheOperator(t *testing.T) {
	var out strings.Builder
	// Type-ahead before the acknowledgement must not release the gate, and
	// the two keystrokes the TUI wants after it must survive.
	in := strings.NewReader("abc\n2j")
	announce(&out, in, []string{"team LERP: trouble", "fix: do the thing"}, true)
	for _, want := range []string{"team LERP: trouble", "fix: do the thing", "press enter"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q missing %q", out.String(), want)
		}
	}
	rest, err := io.ReadAll(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "2j" {
		t.Errorf("announce consumed past the newline, leaving %q for the TUI", rest)
	}
}

func TestAnnounceIsSilentWithoutWarnings(t *testing.T) {
	var out strings.Builder
	// Nothing is read either: a clean startup must not eat the first
	// keystroke the operator aims at the board.
	in := strings.NewReader("2")
	announce(&out, in, nil, true)
	if out.String() != "" {
		t.Errorf("output = %q, want nothing", out.String())
	}
	if in.Len() != 1 {
		t.Errorf("announce read %d bytes, want none", 1-in.Len())
	}
}

// An unreadable stdin is not a reason to refuse: the warning is on screen,
// and refusing here would turn a warning into the refusal it is not.
func TestAnnounceStartsAnywayWhenStdinIsClosed(t *testing.T) {
	var out strings.Builder
	announce(&out, strings.NewReader(""), []string{"team LERP: trouble"}, true)
	if !strings.Contains(out.String(), "team LERP: trouble") {
		t.Errorf("output %q missing the warning", out.String())
	}
}

// A terminal that is not translating carriage returns delivers enter as \r.
// A gate that only knows \n would swallow it and never open.
func TestAnnounceAcceptsACarriageReturn(t *testing.T) {
	var out strings.Builder
	in := strings.NewReader("\r2j")
	announce(&out, in, []string{"team LERP: trouble"}, true)
	rest, err := io.ReadAll(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "2j" {
		t.Errorf("a carriage return left %q for the TUI, want %q", rest, "2j")
	}
}

// Warnings redirected away with `lerp 2>/dev/null` reach nobody, so there is
// nothing for a keystroke to acknowledge — waiting for one would hang the
// launch behind a blank screen.
func TestAnnounceDoesNotWaitWhenNobodyCanSeeIt(t *testing.T) {
	var out strings.Builder
	in := strings.NewReader("2j")
	announce(&out, in, []string{"team LERP: trouble"}, false)
	if strings.Contains(out.String(), "press enter") {
		t.Errorf("output %q prompts for an answer nobody was asked for", out.String())
	}
	if in.Len() != 2 {
		t.Errorf("announce read %d bytes, want none", 2-in.Len())
	}
}

// A missing repo config is the most common first-run mistake; loadRepo points at
// lerp init in one line rather than surfacing a raw path error.
func TestLoadRepoMissingConfigPointsAtInit(t *testing.T) {
	dir := t.TempDir()
	_, err := loadRepo(dir)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if want := `no repo config: run "lerp init --team KEY"`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// A malformed repo config (syntax or validation) must not point at init — that
// would send an operator with a typo down a setup flow they already finished.
func TestLoadRepoMalformedConfigReportsDecoderError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFile), []byte("invalid = ["), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadRepo(dir)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), config.RepoConfigFile) {
		t.Errorf("error %q does not name %s", err.Error(), config.RepoConfigFile)
	}
	if strings.Contains(err.Error(), "lerp init") {
		t.Errorf("error %q points at lerp init for a malformed config", err.Error())
	}
}

func TestLoadRepoValidConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFile), []byte(config.ExampleRepoConfig()), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := loadRepo(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.Teams) == 0 {
		t.Errorf("teams empty in loaded config: %+v", repo)
	}
}

func TestLoadRepoValidYAMLConfig(t *testing.T) {
	dir := t.TempDir()
	yamlConfig := `
teams:
  - LERP
provision: p
dispose: d
runners:
  claude:
    command: claude -p {{prompt}}
queues:
  implement:
    status: Implementing
    prompt: do it
    runner: claude
    on_success: Done
`
	if err := os.WriteFile(filepath.Join(dir, "lerp.yaml"), []byte(yamlConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := loadRepo(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.Teams) != 1 || repo.Teams[0] != "LERP" {
		t.Errorf("teams in loaded YAML config: %+v", repo)
	}
}

func TestLoadRepoMultipleConfigsRefusesWithoutMentioningInit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lerp.toml"), []byte("teams = ['LERP']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lerp.yaml"), []byte("teams:\n  - LERP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadRepo(dir)
	if err == nil {
		t.Fatal("want error on multiple configs, got nil")
	}
	if strings.Contains(err.Error(), "lerp init") {
		t.Errorf("error %q points at lerp init for multiple configs", err.Error())
	}
	for _, want := range []string{"lerp.toml", "lerp.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err.Error(), want)
		}
	}
}

func TestBoardStatusesPreservesOrderAndDeduplicates(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("TEAM1", "Triage", "Backlog", "Todo", "In Progress", "Done")
	fake.SetTeamStates("TEAM2", "Backlog", "Todo", "Review", "Done", "Archived")

	got, err := boardStatuses(context.Background(), fake, []string{"TEAM1", "TEAM2"})
	if err != nil {
		t.Fatalf("boardStatuses: %v", err)
	}
	want := []string{"Triage", "Backlog", "Todo", "In Progress", "Done", "Review", "Archived"}
	if !slices.Equal(got, want) {
		t.Errorf("boardStatuses = %v, want %v", got, want)
	}
}

func TestPrintVersion(t *testing.T) {
	// Temporarily override version.Version for predictable testing
	origVersion := version.Version
	version.Version = "v0.1.0"
	defer func() { version.Version = origVersion }()

	t.Run("no cache prints one line", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		var out strings.Builder
		printVersion(&out)
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 1 {
			t.Errorf("lines = %d, want 1; output = %q", len(lines), out.String())
		}
		if lines[0] != "lerp v0.1.0" {
			t.Errorf("line = %q, want %q", lines[0], "lerp v0.1.0")
		}
	})

	t.Run("matching cache prints one line", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("XDG_STATE_HOME", tmp)
		path, err := update.Path()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"checked_at":"2026-08-27T00:00:00Z","latest":"v0.1.0"}`), 0o600); err != nil {
			t.Fatal(err)
		}

		var out strings.Builder
		printVersion(&out)
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 1 {
			t.Errorf("lines = %d, want 1; output = %q", len(lines), out.String())
		}
		if lines[0] != "lerp v0.1.0" {
			t.Errorf("line = %q, want %q", lines[0], "lerp v0.1.0")
		}
	})

	t.Run("newer cache prints two lines", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("XDG_STATE_HOME", tmp)
		path, err := update.Path()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"checked_at":"2026-08-27T00:00:00Z","latest":"v0.2.0"}`), 0o600); err != nil {
			t.Fatal(err)
		}

		var out strings.Builder
		printVersion(&out)
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("lines = %d, want 2; output = %q", len(lines), out.String())
		}
		if lines[0] != "lerp v0.1.0" {
			t.Errorf("lines[0] = %q, want %q", lines[0], "lerp v0.1.0")
		}
		if lines[1] != "latest v0.2.0 — brew upgrade lerp" {
			t.Errorf("lines[1] = %q, want %q", lines[1], "latest v0.2.0 — brew upgrade lerp")
		}
	})

	t.Run("corrupt cache prints exactly one line", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("XDG_STATE_HOME", tmp)
		path, err := update.Path()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{corrupt`), 0o600); err != nil {
			t.Fatal(err)
		}

		var out strings.Builder
		printVersion(&out)
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 1 {
			t.Errorf("lines = %d, want 1; output = %q", len(lines), out.String())
		}
		if lines[0] != "lerp v0.1.0" {
			t.Errorf("line = %q, want %q", lines[0], "lerp v0.1.0")
		}
	})
}

func TestAnchorFrom(t *testing.T) {
	t.Run("finds lerp.toml in start directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFile), []byte(config.ExampleRepoConfig()), 0o644); err != nil {
			t.Fatal(err)
		}
		resolvedDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := anchorFrom(dir)
		if err != nil {
			t.Fatalf("anchorFrom error: %v", err)
		}
		if got != resolvedDir {
			t.Errorf("anchorFrom(%q) = %q, want %q", dir, got, resolvedDir)
		}
	})

	t.Run("finds config several levels up", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(root, "a", "b", "c", "d")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, config.RepoConfigFile), []byte(config.ExampleRepoConfig()), 0o644); err != nil {
			t.Fatal(err)
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		got, err := anchorFrom(deep)
		if err != nil {
			t.Fatalf("anchorFrom(%q) error: %v", deep, err)
		}
		if got != resolvedRoot {
			t.Errorf("anchorFrom(%q) = %q, want %q", deep, got, resolvedRoot)
		}
	})

	t.Run("finds yaml config several levels up", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(root, "sub", "pkg")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "lerp.yaml"), []byte("teams:\n  - LERP\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		got, err := anchorFrom(deep)
		if err != nil {
			t.Fatalf("anchorFrom(%q) error: %v", deep, err)
		}
		if got != resolvedRoot {
			t.Errorf("anchorFrom(%q) = %q, want %q", deep, got, resolvedRoot)
		}
	})
}

func TestAnchorFromNearestAncestorWins(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "subproject")
	deep := filepath.Join(child, "src", "pkg")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.RepoConfigFile), []byte("# root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, config.RepoConfigFile), []byte("# child"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolvedChild, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatal(err)
	}

	got, err := anchorFrom(deep)
	if err != nil {
		t.Fatalf("anchorFrom(%q) error: %v", deep, err)
	}
	if got != resolvedChild {
		t.Errorf("anchorFrom(%q) = %q, want %q", deep, got, resolvedChild)
	}
}

func TestAnchorFromDirectoryNamedConfigIsNotAnchor(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, config.RepoConfigFile), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := anchorFrom(dir)
	if err == nil {
		t.Fatal("anchorFrom succeeded on a directory named lerp.toml, want error")
	}
	if !strings.Contains(err.Error(), "no repo config") {
		t.Errorf("error %q does not mention missing repo config", err.Error())
	}
}

func TestAnchorFromMissPointsAtInit(t *testing.T) {
	dir := t.TempDir()
	_, err := anchorFrom(dir)
	if err == nil {
		t.Fatal("want error on missing config, got nil")
	}
	want := fmt.Sprintf("no repo config in %s or any parent directory: run %q", dir, "lerp init --team KEY")
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if strings.Contains(strings.ToLower(err.Error()), "git") {
		t.Errorf("error %q mentions Git", err.Error())
	}
}

func TestAnchorFromMultipleConfigsRefuses(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lerp.toml"), []byte("teams = ['LERP']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lerp.yaml"), []byte("teams:\n  - LERP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := anchorFrom(deep)
	if err == nil {
		t.Fatal("want error on multiple configs, got nil")
	}
	for _, want := range []string{"lerp.toml", "lerp.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err.Error(), want)
		}
	}
}

func TestInitAnchorFrom(t *testing.T) {
	t.Run("returns ancestor holding config", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(root, "a", "b")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, config.RepoConfigFile), []byte(config.ExampleRepoConfig()), 0o644); err != nil {
			t.Fatal(err)
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		got := initAnchorFrom(deep)
		if got != resolvedRoot {
			t.Errorf("initAnchorFrom(%q) = %q, want %q", deep, got, resolvedRoot)
		}
	})

	t.Run("returns start directory when none found", func(t *testing.T) {
		dir := t.TempDir()
		resolvedDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		got := initAnchorFrom(dir)
		if got != resolvedDir {
			t.Errorf("initAnchorFrom(%q) = %q, want %q", dir, got, resolvedDir)
		}
	})
}

func TestNoGoSourceSpawnsGit(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "exec" {
				return true
			}
			if sel.Sel.Name == "Command" && len(call.Args) > 0 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Value == `"git"` {
					t.Errorf("%s: spawns git via exec.Command(%s)", path, lit.Value)
				}
			}
			if sel.Sel.Name == "CommandContext" && len(call.Args) > 1 {
				if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Value == `"git"` {
					t.Errorf("%s: spawns git via exec.CommandContext(..., %s)", path, lit.Value)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking repo: %v", err)
	}
}
