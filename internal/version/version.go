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

// shortHashLen matches the abbreviated hash `make install` stamps via `git
// describe` (see Makefile), so a fallback build's version has the same shape
// as a proper one instead of a bare 40-character hex string.
const shortHashLen = 7

// fromBuildInfo picks a version out of build info recorded by the toolchain.
// `go install pkg@version` stamps info.Main.Version with that version; a
// plain `go build` in a checkout leaves it "(devel)" and stamps the VCS
// revision into Settings instead — a full commit hash, with a separate
// setting saying whether the tree was dirty, so both are folded in rather
// than reporting a clean-looking hash for a dirty build.
func fromBuildInfo(info *debug.BuildInfo) string {
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return ""
	}
	if len(revision) > shortHashLen {
		revision = revision[:shortHashLen]
	}
	if modified {
		revision += "-dirty"
	}
	return revision
}
