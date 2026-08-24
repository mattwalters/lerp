// Package version holds the build version stamped into the binary.
package version

// Version is overridden at release time via
// -ldflags "-X github.com/mattwalters/lerp/internal/version.Version=...".
var Version = "dev"
