// Package version holds the build version stamped into the binary.
package version

import "runtime/debug"

// Version is overridden at release time via
// -ldflags "-X github.com/mattwalters/lerp/internal/version.Version=...".
// A `go install .../lerp@latest` build never runs ldflags, so init falls
// back to the module version Go itself records in the binary.
var Version = "dev"

func init() {
	if Version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := fromBuildInfo(info); v != "" {
			Version = v
		}
	}
}

// fromBuildInfo reads the module version the toolchain itself records. go.mod
// pins go 1.25, and since Go 1.24 that alone means info.Main.Version is never
// "(devel)" when the binary was built inside a VCS checkout: `go install
// pkg@version` stamps the requested version, and a plain `go build` there
// stamps a pseudo-version with a "+dirty" suffix if the tree had changes —
// dirty-tree honesty is the toolchain's job, not this function's. "(devel)"
// (or "") remains only for a build with no VCS info to read at all, such as
// `-buildvcs=false` or a source archive with no `.git`.
func fromBuildInfo(info *debug.BuildInfo) string {
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return ""
}
