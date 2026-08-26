package childenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestInheritedDropsTheLinearAPIKey(t *testing.T) {
	t.Setenv(LinearAPIKeyEnv, "lin_api_secret")
	t.Setenv("LERP_TEST_KEEP", "kept")

	env := Inherited()
	if slices.Contains(env, LinearAPIKeyEnv+"=lin_api_secret") {
		t.Error("Inherited kept the Linear API key")
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, LinearAPIKeyEnv+"=") {
			t.Errorf("Inherited kept %q", entry)
		}
	}
	if !slices.Contains(env, "LERP_TEST_KEEP=kept") {
		t.Error("Inherited dropped an unrelated variable")
	}
	if want := len(os.Environ()) - 1; len(env) != want {
		t.Errorf("len(Inherited()) = %d, want %d — only the key should go", len(env), want)
	}
}

func TestInheritedAppendsExtrasAfterTheEnvironment(t *testing.T) {
	t.Setenv(LinearAPIKeyEnv, "lin_api_secret")

	env := Inherited("LERP_TICKET=LERP-37", "LERP_LANE=2")
	if got := env[len(env)-2:]; !slices.Equal(got, []string{"LERP_TICKET=LERP-37", "LERP_LANE=2"}) {
		t.Errorf("tail of Inherited = %q, want the extras in order", got)
	}
}

// An empty key is still a key: os.Environ reports LINEAR_API_KEY= for a
// variable exported with no value, and a child that sees it set to the empty
// string is told something different from a child that never sees it.
func TestInheritedDropsAnEmptyLinearAPIKey(t *testing.T) {
	t.Setenv(LinearAPIKeyEnv, "")

	if slices.Contains(Inherited(), LinearAPIKeyEnv+"=") {
		t.Error("Inherited kept an empty Linear API key")
	}
}

// The scrub is only worth as much as its coverage: a spawn site added later
// that builds its own environment from os.Environ() puts the key back in
// reach, and nothing about it looks wrong in review. Two source-level
// invariants keep that from happening quietly — no package outside this one
// builds a child environment from os.Environ(), and any package that reads
// the key drops it from lerp's own environment, which is what covers the
// other half: an exec.Command left with a nil Env inherits this process
// whole, and no grep can see that one.
//
// The files are parsed rather than searched, because prose about os.Environ
// is exactly what this change is full of and a comment must never fail the
// gate. Dot-directories are skipped, the rule the go tool applies to a
// package pattern, and for a sharper reason here: an operator's clone holds
// lerp's own lane workspaces under .lerp/workspaces and agent worktrees
// under .claude, each a full checkout of some other branch — files not in
// this module that no edit on this branch can reach.
func TestNothingElsePutsTheKeyWithinReachOfAChild(t *testing.T) {
	root := filepath.Join("..", "..")
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			skip := abs == self ||
				d.Name() == "testdata" ||
				(path != root && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")))
			if skip {
				return fs.SkipDir
			}
			return nil
		case !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		var reads, drops int
		ast.Inspect(file, func(n ast.Node) bool {
			name, arg := osCall(n)
			switch {
			case name == "Environ":
				t.Errorf("%s calls os.Environ(); a child's environment comes from childenv.Inherited, which drops %s", path, LinearAPIKeyEnv)
			case name == "Getenv" && namesTheKey(arg):
				reads++
			case name == "Unsetenv" && namesTheKey(arg):
				drops++
			}
			return true
		})
		if reads > 0 && drops == 0 {
			t.Errorf("%s reads %s without dropping it; a child spawned with a nil Env inherits it", path, LinearAPIKeyEnv)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// osCall reports the function name and first argument of a call on package
// os — "Environ", "Getenv", "Unsetenv" — and "" for anything else. It reads
// the identifier `os`, not an import path: a file that renames the import is
// not something this repository writes.
func osCall(n ast.Node) (string, ast.Expr) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return "", nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", nil
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
		return "", nil
	}
	var arg ast.Expr
	if len(call.Args) > 0 {
		arg = call.Args[0]
	}
	return sel.Sel.Name, arg
}

// namesTheKey matches every spelling of the variable: the constant, the
// constant through this package, and the literal a future change might type
// out instead.
func namesTheKey(arg ast.Expr) bool {
	switch a := arg.(type) {
	case *ast.Ident:
		return a.Name == "LinearAPIKeyEnv"
	case *ast.SelectorExpr:
		return a.Sel.Name == "LinearAPIKeyEnv"
	case *ast.BasicLit:
		return a.Kind == token.STRING && a.Value == strconv.Quote(LinearAPIKeyEnv)
	}
	return false
}
