// Package telemetry writes one append-only JSONL line per finished run — a
// local, deliberate fourth resident beside config, credentials, and evidence
// (SCOPE.md invariant 1): history, not state, written once at run exit and
// read by nothing in the loop, never sent to Linear (invariant 7).
package telemetry
