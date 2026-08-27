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

// fromBuildInfo picks a version out of build info recorded by the toolchain.
// `go install pkg@version` stamps info.Main.Version with that version; a
// plain `go build` in a checkout leaves it "(devel)" and stamps the VCS
// revision into Settings instead.
func fromBuildInfo(info *debug.BuildInfo) string {
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}
