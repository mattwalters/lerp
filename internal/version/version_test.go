package version

import (
	"runtime/debug"
	"testing"
)

func TestVersionNonEmpty(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty")
	}
}

func TestFromBuildInfo(t *testing.T) {
	cases := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "module version from go install pkg@version",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.3.1"}},
			want: "v0.3.1",
		},
		{
			name: "devel build falls back to a shortened vcs revision",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc1234567890def"},
				},
			},
			want: "abc1234",
		},
		{
			name: "a dirty tree is marked, not silently reported as clean",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc1234567890def"},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			want: "abc1234-dirty",
		},
		{
			name: "nothing usable",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fromBuildInfo(c.info); got != c.want {
				t.Errorf("fromBuildInfo() = %q, want %q", got, c.want)
			}
		})
	}
}
