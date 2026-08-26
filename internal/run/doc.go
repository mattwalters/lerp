//go:build unix

// Package run executes a runner and models its ephemeral local evidence:
// a process, a log file, a ticket, and a workspace. See SCOPE.md invariant 1:
// evidence of running processes, never durable state.
//
// Unix only: a run is its own process group, killed by process-group id.
// Every file here carries a //go:build unix constraint, so a new one needs
// it too.
package run
