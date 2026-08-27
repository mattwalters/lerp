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
			name: "a pseudo-version, dirty suffix included, is the toolchain's own to give",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260827041153-3c6bcf9d86e7+dirty"}},
			want: "v0.0.0-20260827041153-3c6bcf9d86e7+dirty",
		},
		{
			name: "no VCS info at all leaves devel unusable",
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
