//go:build unix

// Package evidence keeps the local, disposable facts needed to reconcile
// running agents. Linear remains the durable record of work.
//
// macOS and Linux only: the clone lock is an advisory flock and liveness is
// a signal-0 kill. Every file here carries a //go:build unix constraint, so
// a new one needs it too.
package evidence
