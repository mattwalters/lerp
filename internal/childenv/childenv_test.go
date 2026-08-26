package childenv

import (
	"os"
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
