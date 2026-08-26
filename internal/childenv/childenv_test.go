package childenv

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
// reach, and nothing about it looks wrong in review. So no package outside
// this one reads the environment wholesale — os.Getenv for one variable is
// untouched, and tests are their own business.
//
// Dot-directories are skipped, the same rule the go tool applies to a
// package pattern, and for a sharper reason here: an operator's clone holds
// lerp's own lane workspaces under .lerp/workspaces and any agent worktrees
// under .claude, each a full checkout of some other branch. Walking those
// would fail this repository's gate on files that are not in this module and
// that no edit on this branch can reach.
func TestNoOtherPackageReadsTheEnvironment(t *testing.T) {
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
			if abs == self || strings.HasPrefix(d.Name(), ".") && path != root {
				return fs.SkipDir
			}
			return nil
		case !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), "os.Environ()") {
			t.Errorf("%s calls os.Environ(); a child's environment comes from childenv.Inherited, which drops %s", path, LinearAPIKeyEnv)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
