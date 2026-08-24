package initcmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
)

type fakeBoard struct {
	teamKey, teamName string
	states            []string
	err               error
}

func (b *fakeBoard) EnsureTeam(_ context.Context, key, name string) error {
	b.teamKey, b.teamName = key, name
	return b.err
}
func (b *fakeBoard) EnsureWorkflowStates(_ context.Context, _ string, states []string) error {
	b.states = append([]string(nil), states...)
	return b.err
}

func testGlobal() *config.Global {
	return &config.Global{Queues: map[string]config.Queue{
		"plan": {Status: "Planning", OnSuccess: "Implementing"},
		"code": {Status: "Implementing", OnSuccess: "Review", OnFailure: "Human Review"},
	}}
}

func TestInitCreatesConfigAndStates(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{}
	if err := Init(context.Background(), b, testGlobal(), dir, "LERP", "Lerp"); err != nil {
		t.Fatal(err)
	}
	if b.teamKey != "LERP" || b.teamName != "Lerp" {
		t.Errorf("EnsureTeam = (%q, %q)", b.teamKey, b.teamName)
	}
	if want := []string{"Human Review", "Implementing", "Planning", "Review"}; !reflect.DeepEqual(b.states, want) {
		t.Errorf("states = %v, want %v", b.states, want)
	}
	c, err := config.LoadRepoConfig(filepath.Join(dir, config.RepoConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Teams, []string{"LERP"}) {
		t.Errorf("teams = %v", c.Teams)
	}
}

func TestInitIsIdempotentAndDoesNotReplaceConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.RepoConfigFile)
	contents := "teams = [\"LERP\"]\nprovision = \"mine\"\ndispose = \"mine\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(context.Background(), &fakeBoard{}, testGlobal(), dir, "LERP", ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != contents {
		t.Errorf("config changed to %q", got)
	}
}

func TestInitRejectsExistingConfigForOtherTeam(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFile), []byte("teams = [\"OTHER\"]\nprovision = \"p\"\ndispose = \"d\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Init(context.Background(), &fakeBoard{}, testGlobal(), dir, "LERP", "")
	if err == nil || !strings.Contains(err.Error(), `does not serve team "LERP"`) {
		t.Fatalf("Init error = %v", err)
	}
}

func TestInitStopsWhenBoardFails(t *testing.T) {
	dir := t.TempDir()
	err := Init(context.Background(), &fakeBoard{err: errors.New("no access")}, testGlobal(), dir, "LERP", "")
	if err == nil || !strings.Contains(err.Error(), "no access") {
		t.Fatalf("Init error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.RepoConfigFile)); !os.IsNotExist(statErr) {
		t.Fatalf("config was created: %v", statErr)
	}
}
