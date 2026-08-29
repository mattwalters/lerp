package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const canonicalTOML = `
teams = ["LERP", "OPS"]
provision = "git worktree add {{workdir}}"
dispose = "git worktree remove {{workdir}}"

[runners.claude]
vendor = "claude"
model = "opus"
effort = "high"
args = "--permission-mode bypassPermissions"
context = 200000

[runners.cmd]
command = "my-agent -p {{prompt}}"
resume = "my-agent --resume {{session}}"

[queues.plan]
status = "Planning"
prompt = "Plan {{ticket}} in {{status}}."
runner = "claude"
on_success = "Plan Review"
on_failure = "Blocked"

[queues.implement]
status = "Implementing"
prompt = "Implement {{ticket}}."
runner = "cmd"
on_success = "In Review"
`

const canonicalYAML = `
teams:
  - LERP
  - OPS
provision: git worktree add {{workdir}}
dispose: git worktree remove {{workdir}}
runners:
  claude:
    vendor: claude
    model: opus
    effort: high
    args: --permission-mode bypassPermissions
    context: 200000
  cmd:
    command: my-agent -p {{prompt}}
    resume: my-agent --resume {{session}}
queues:
  plan:
    status: Planning
    prompt: Plan {{ticket}} in {{status}}.
    runner: claude
    on_success: Plan Review
    on_failure: Blocked
  implement:
    status: Implementing
    prompt: Implement {{ticket}}.
    runner: cmd
    on_success: In Review
`

const canonicalJSON = `{
  "teams": ["LERP", "OPS"],
  "provision": "git worktree add {{workdir}}",
  "dispose": "git worktree remove {{workdir}}",
  "runners": {
    "claude": {
      "vendor": "claude",
      "model": "opus",
      "effort": "high",
      "args": "--permission-mode bypassPermissions",
      "context": 200000
    },
    "cmd": {
      "command": "my-agent -p {{prompt}}",
      "resume": "my-agent --resume {{session}}"
    }
  },
  "queues": {
    "plan": {
      "status": "Planning",
      "prompt": "Plan {{ticket}} in {{status}}.",
      "runner": "claude",
      "on_success": "Plan Review",
      "on_failure": "Blocked"
    },
    "implement": {
      "status": "Implementing",
      "prompt": "Implement {{ticket}}.",
      "runner": "cmd",
      "on_success": "In Review"
    }
  }
}`

func TestSameConfigThreeEnvelopes(t *testing.T) {
	tomlCfg, err := ParseRepoConfig(canonicalTOML, "lerp.toml")
	if err != nil {
		t.Fatalf("ParseRepoConfig(TOML) = %v", err)
	}
	yamlCfg, err := ParseRepoConfig(canonicalYAML, "lerp.yaml")
	if err != nil {
		t.Fatalf("ParseRepoConfig(YAML) = %v", err)
	}
	ymlCfg, err := ParseRepoConfig(canonicalYAML, "lerp.yml")
	if err != nil {
		t.Fatalf("ParseRepoConfig(YML) = %v", err)
	}
	jsonCfg, err := ParseRepoConfig(canonicalJSON, "lerp.json")
	if err != nil {
		t.Fatalf("ParseRepoConfig(JSON) = %v", err)
	}

	if !reflect.DeepEqual(tomlCfg, yamlCfg) {
		t.Errorf("YAML config != TOML config:\nYAML: %+v\nTOML: %+v", yamlCfg, tomlCfg)
	}
	if !reflect.DeepEqual(tomlCfg, ymlCfg) {
		t.Errorf("YML config != TOML config:\nYML: %+v\nTOML: %+v", ymlCfg, tomlCfg)
	}
	if !reflect.DeepEqual(tomlCfg, jsonCfg) {
		t.Errorf("JSON config != TOML config:\nJSON: %+v\nTOML: %+v", jsonCfg, tomlCfg)
	}

	// Also verify vendor resolution happened identically
	r := tomlCfg.Runners["claude"]
	if !strings.Contains(r.Command, "--model 'opus'") || !strings.Contains(r.Command, "--effort 'high'") {
		t.Errorf("resolved command = %q, want model/effort overrides", r.Command)
	}
	if r.Resume == "" {
		t.Error("resolved resume is empty")
	}
}

func TestSameFailuresThreeEnvelopes(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		yaml    string
		json    string
		wantErr string
	}{
		{
			name: "empty teams",
			toml: `
teams = []
provision = "p"
dispose = "d"
[runners.c]
command = "c"
[queues.q]
status = "S"
prompt = "P"
runner = "c"
on_success = "D"
`,
			yaml: `
teams: []
provision: p
dispose: d
runners:
  c:
    command: c
queues:
  q:
    status: S
    prompt: P
    runner: c
    on_success: D
`,
			json: `{
  "teams": [],
  "provision": "p",
  "dispose": "d",
  "runners": {"c": {"command": "c"}},
  "queues": {"q": {"status": "S", "prompt": "P", "runner": "c", "on_success": "D"}}
}`,
			wantErr: "teams must list at least one Linear team key",
		},
		{
			name: "unknown key at root",
			toml: `
teams = ["L"]
provision = "p"
dispose = "d"
team_lead = "matt"
[runners.c]
command = "c"
[queues.q]
status = "S"
prompt = "P"
runner = "c"
on_success = "D"
`,
			yaml: `
teams: ["L"]
provision: p
dispose: d
team_lead: matt
runners:
  c:
    command: c
queues:
  q:
    status: S
    prompt: P
    runner: c
    on_success: D
`,
			json: `{
  "teams": ["L"],
  "provision": "p",
  "dispose": "d",
  "team_lead": "matt",
  "runners": {"c": {"command": "c"}},
  "queues": {"q": {"status": "S", "prompt": "P", "runner": "c", "on_success": "D"}}
}`,
			wantErr: "unknown key(s): team_lead",
		},
		{
			name: "unknown key under runners.claude",
			toml: `
teams = ["L"]
provision = "p"
dispose = "d"
[runners.claude]
command = "c"
timeout = 30
[queues.q]
status = "S"
prompt = "P"
runner = "claude"
on_success = "D"
`,
			yaml: `
teams: ["L"]
provision: p
dispose: d
runners:
  claude:
    command: c
    timeout: 30
queues:
  q:
    status: S
    prompt: P
    runner: claude
    on_success: D
`,
			json: `{
  "teams": ["L"],
  "provision": "p",
  "dispose": "d",
  "runners": {
    "claude": {
      "command": "c",
      "timeout": 30
    }
  },
  "queues": {"q": {"status": "S", "prompt": "P", "runner": "claude", "on_success": "D"}}
}`,
			wantErr: "unknown key(s): runners.claude.timeout",
		},
		{
			name: "unknown key under queues.plan",
			toml: `
teams = ["L"]
provision = "p"
dispose = "d"
[runners.c]
command = "c"
[queues.plan]
status = "S"
prompt = "P"
runner = "c"
on_success = "D"
when = "always"
`,
			yaml: `
teams: ["L"]
provision: p
dispose: d
runners:
  c:
    command: c
queues:
  plan:
    status: S
    prompt: P
    runner: c
    on_success: D
    when: always
`,
			json: `{
  "teams": ["L"],
  "provision": "p",
  "dispose": "d",
  "runners": {"c": {"command": "c"}},
  "queues": {
    "plan": {
      "status": "S",
      "prompt": "P",
      "runner": "c",
      "on_success": "D",
      "when": "always"
    }
  }
}`,
			wantErr: "unknown key(s): queues.plan.when",
		},
		{
			name: "two queues on one status",
			toml: `
teams = ["L"]
provision = "p"
dispose = "d"
[runners.c]
command = "c"
[queues.q1]
status = "S"
prompt = "P"
runner = "c"
on_success = "D"
[queues.q2]
status = "S"
prompt = "P"
runner = "c"
on_success = "D"
`,
			yaml: `
teams: ["L"]
provision: p
dispose: d
runners:
  c:
    command: c
queues:
  q1:
    status: S
    prompt: P
    runner: c
    on_success: D
  q2:
    status: S
    prompt: P
    runner: c
    on_success: D
`,
			json: `{
  "teams": ["L"],
  "provision": "p",
  "dispose": "d",
  "runners": {"c": {"command": "c"}},
  "queues": {
    "q1": {"status": "S", "prompt": "P", "runner": "c", "on_success": "D"},
    "q2": {"status": "S", "prompt": "P", "runner": "c", "on_success": "D"}
  }
}`,
			wantErr: `queues "q1" and "q2" both watch status "S"; a status may drive at most one queue`,
		},
		{
			name: "model on a command runner",
			toml: `
teams = ["L"]
provision = "p"
dispose = "d"
[runners.mine]
command = "c"
model = "opus"
[queues.q]
status = "S"
prompt = "P"
runner = "mine"
on_success = "D"
`,
			yaml: `
teams: ["L"]
provision: p
dispose: d
runners:
  mine:
    command: c
    model: opus
queues:
  q:
    status: S
    prompt: P
    runner: mine
    on_success: D
`,
			json: `{
  "teams": ["L"],
  "provision": "p",
  "dispose": "d",
  "runners": {"mine": {"command": "c", "model": "opus"}},
  "queues": {"q": {"status": "S", "prompt": "P", "runner": "mine", "on_success": "D"}}
}`,
			wantErr: `runner "mine": model is set on a command runner; it belongs to a vendor`,
		},
		{
			name: "on_failure placeholder without on_failure set",
			toml: `
teams = ["L"]
provision = "p"
dispose = "d"
[runners.c]
command = "c"
[queues.q]
status = "S"
prompt = "fail: {{on_failure}}"
runner = "c"
on_success = "D"
`,
			yaml: `
teams: ["L"]
provision: p
dispose: d
runners:
  c:
    command: c
queues:
  q:
    status: S
    prompt: "fail: {{on_failure}}"
    runner: c
    on_success: D
`,
			json: `{
  "teams": ["L"],
  "provision": "p",
  "dispose": "d",
  "runners": {"c": {"command": "c"}},
  "queues": {"q": {"status": "S", "prompt": "fail: {{on_failure}}", "runner": "c", "on_success": "D"}}
}`,
			wantErr: `queue "q": prompt references {{on_failure}} but on_failure is not set`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formats := []struct {
				ext    string
				source string
			}{
				{".toml", tt.toml},
				{".yaml", tt.yaml},
				{".json", tt.json},
			}

			for _, f := range formats {
				label := "repo" + f.ext
				_, err := ParseRepoConfig(f.source, label)
				if err == nil {
					t.Fatalf("%s: want error, got nil", f.ext)
				}
				wantErrStr := label + ": " + tt.wantErr
				if err.Error() != wantErrStr {
					t.Errorf("%s error =\n%q\nwant\n%q", f.ext, err.Error(), wantErrStr)
				}
			}
		})
	}
}

func TestFindRepoConfig(t *testing.T) {
	t.Run("each name found alone", func(t *testing.T) {
		for _, name := range RepoConfigNames {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := FindRepoConfig(dir)
			if err != nil {
				t.Errorf("%s: FindRepoConfig error = %v", name, err)
			}
			if got != path {
				t.Errorf("%s: FindRepoConfig = %q, want %q", name, got, path)
			}
		}
	})

	t.Run("zero found wraps ErrNotExist", func(t *testing.T) {
		dir := t.TempDir()
		_, err := FindRepoConfig(dir)
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error %v does not wrap fs.ErrNotExist", err)
		}
	})

	t.Run("two configs refuse naming both in RepoConfigNames order", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "lerp.yaml"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "lerp.toml"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := FindRepoConfig(dir)
		if err == nil {
			t.Fatal("want error, got nil")
		}
		want := "more than one repo config found: lerp.toml, lerp.yaml"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want containing %q", err.Error(), want)
		}
	})

	t.Run("three configs refuse naming all three", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "lerp.json"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "lerp.yaml"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "lerp.toml"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := FindRepoConfig(dir)
		if err == nil {
			t.Fatal("want error, got nil")
		}
		want := "more than one repo config found: lerp.toml, lerp.yaml, lerp.json"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want containing %q", err.Error(), want)
		}
	})
}
