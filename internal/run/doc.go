// Package run models a single run — a pid, a log file, a ticket, a
// workspace — and its on-disk evidence under .lerp/runs. See SCOPE.md
// invariant 1: evidence of running processes, never durable state.
package run
