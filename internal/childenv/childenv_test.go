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

// Done-when (LERP-110): no LINEAR_* credential reaches a child process
// under either auth mode (API key or OAuth). Under API key mode the key is
// dropped from the environment; under OAuth mode the token lives in a file
// and was never in the environment to begin with.
func TestInheritedHasNoLinearCredentialsUnderEitherAuthMode(t *testing.T) {
	t.Run("API key mode", func(t *testing.T) {
		t.Setenv(LinearAPIKeyEnv, "lin_api_secret")
		for _, entry := range Inherited() {
			if strings.HasPrefix(entry, "LINEAR_") {
				t.Errorf("Inherited kept %q under API key mode", entry)
			}
		}
	})

	t.Run("OAuth mode", func(t *testing.T) {
		t.Setenv(LinearAPIKeyEnv, "")
		for _, entry := range Inherited() {
			if strings.HasPrefix(entry, "LINEAR_") {
				t.Errorf("Inherited has %q under OAuth mode", entry)
			}
		}
	})
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
		undropped, environ := keyUse(file)
		if environ {
			t.Errorf("%s calls os.Environ(); a child's environment comes from childenv.Inherited, which drops %s", path, LinearAPIKeyEnv)
		}
		for _, fn := range undropped {
			t.Errorf("%s: %s reads %s without dropping it; a child spawned with a nil Env inherits it", path, fn, LinearAPIKeyEnv)
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

// keyUse reports how one parsed file touches the key: the names of the
// declarations that read it through os.Getenv without dropping it with
// os.Unsetenv, and whether the file builds a child environment from
// os.Environ. It is a function so the gate above and
// TestTheGateCatchesAPrivateConstAlias run the same code over a real tree
// and over written-out examples.
//
// Reads and drops are matched inside one declaration rather than tallied
// across the file. A file-wide tally lets a drop anywhere cover a read
// anywhere, and the names below come from the source with no scope
// attached — so a local variable that happens to reuse the name of a
// package const, dropped in some unrelated function, would silently cover
// a genuine leak in another. Per declaration, the drop has to sit with the
// read that needs it, which is the only shape that is actually safe and is
// what the one real reader does.
func keyUse(file *ast.File) (undropped []string, environ bool) {
	alias := aliases(file)
	within := func(name string, n ast.Node) {
		var reads, drops int
		ast.Inspect(n, func(n ast.Node) bool {
			call, arg := osCall(n)
			switch {
			case call == "Environ":
				environ = true
			case call == "Getenv" && namesTheKey(arg, alias):
				reads++
			case call == "Unsetenv" && namesTheKey(arg, alias):
				drops++
			}
			return true
		})
		if reads > 0 && drops == 0 {
			undropped = append(undropped, name)
		}
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			within(fn.Name.Name, fn)
			continue
		}
		// A package-level var initializer can read it too, and has nowhere
		// to drop it — there is no ordering it could rely on.
		within("a package-level declaration", decl)
	}
	return undropped, environ
}

// aliases returns the names a file binds the key to itself — `const
// apiKeyEnv = "LINEAR_API_KEY"`, or a var or short assignment doing the
// same. A read through one of those is a read of the key, and it reaches
// os.Getenv as an *ast.Ident spelled however that package chose, which the
// spellings below cannot know. A package that had every reason to use the
// exported constant reached for a private one instead, and this gate went
// green on it; the literal has to count wherever it is written, not only
// where it is passed to os.
//
// One level, and only names: a literal returned by a function or held in a
// struct field still reaches os.Getenv as a shape this cannot read. That is
// a blind spot on purpose — closing it wants type information, and the gate
// is here to catch the reasonable way to write a leak, not a determined
// one, which is the same line childenv itself draws.
func aliases(file *ast.File) map[string]bool {
	found := map[string]bool{}
	bind := func(names []*ast.Ident, values []ast.Expr) {
		for i, v := range values {
			// Bound with no alias set of its own: one level, the literal or
			// the exported constant. A name for a name for the key is not a
			// shape this repository writes.
			if i < len(names) && namesTheKey(v, nil) {
				found[names[i].Name] = true
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.ValueSpec:
			bind(d.Names, d.Values)
		case *ast.AssignStmt:
			names := make([]*ast.Ident, len(d.Lhs))
			for i, lhs := range d.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					names[i] = id
				} else {
					names[i] = ast.NewIdent("_")
				}
			}
			bind(names, d.Rhs)
		}
		return true
	})
	return found
}

// namesTheKey matches every spelling of the variable: the constant, the
// constant through this package, the literal a future change might type out
// instead, and any name the file under inspection bound to one of those.
func namesTheKey(arg ast.Expr, alias map[string]bool) bool {
	switch a := arg.(type) {
	case *ast.Ident:
		return a.Name == "LinearAPIKeyEnv" || alias[a.Name]
	case *ast.SelectorExpr:
		return a.Sel.Name == "LinearAPIKeyEnv"
	case *ast.BasicLit:
		return a.Kind == token.STRING && a.Value == strconv.Quote(LinearAPIKeyEnv)
	}
	return false
}

// The gate is only as good as the spellings it recognizes, and the one it
// missed is not hypothetical: internal/credentials read the key through its
// own unexported const and the gate stayed silent, which is the whole shape
// of a leak nobody sees in review. These are the cases run through the same
// keyUse the walk above uses.
func TestTheGateCatchesAPrivateConstAlias(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{{
		name: "private const alias, read and not dropped",
		src: `package p
import "os"
const apiKeyEnv = "LINEAR_API_KEY"
func f() string { return os.Getenv(apiKeyEnv) }`,
		want: []string{"f"},
	}, {
		name: "private const alias, read and dropped",
		src: `package p
import "os"
const apiKeyEnv = "LINEAR_API_KEY"
func f() string { k := os.Getenv(apiKeyEnv); os.Unsetenv(apiKeyEnv); return k }`,
		want: nil,
	}, {
		name: "alias of the exported constant",
		src: `package p
import (
	"os"

	"github.com/mattwalters/lerp/internal/childenv"
)
var envName = childenv.LinearAPIKeyEnv
func f() string { return os.Getenv(envName) }`,
		want: []string{"f"},
	}, {
		// The drop has to sit with the read. Tallied across the file, the
		// unrelated local below would cover the leak in f and the gate
		// would go green on it.
		name: "a drop in another function does not cover the read",
		src: `package p
import "os"
const apiKeyEnv = "LINEAR_API_KEY"
func f() string { return os.Getenv(apiKeyEnv) }
func g() { apiKeyEnv := "SOMETHING_ELSE"; os.Unsetenv(apiKeyEnv) }`,
		want: []string{"f"},
	}, {
		name: "a package-level read has nowhere to drop it",
		src: `package p
import "os"
const apiKeyEnv = "LINEAR_API_KEY"
var key = os.Getenv(apiKeyEnv)`,
		want: []string{"a package-level declaration"},
	}, {
		name: "the literal declared but never read",
		src: `package p
const apiKeyEnv = "LINEAR_API_KEY"`,
		want: nil,
	}, {
		name: "an unrelated variable read through a const",
		src: `package p
import "os"
const homeEnv = "HOME"
func f() string { return os.Getenv(homeEnv) }`,
		want: nil,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "p.go", tt.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			undropped, environ := keyUse(file)
			if !slices.Equal(undropped, tt.want) {
				t.Errorf("keyUse flagged %q, want %q", undropped, tt.want)
			}
			if environ {
				t.Error("keyUse reported os.Environ in a file that has none")
			}
		})
	}
}
