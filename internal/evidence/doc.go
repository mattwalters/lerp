//go:build unix

// Package evidence keeps the local, disposable facts needed to reconcile
// running agents. Linear remains the durable record of work.
//
// Unix only: the clone lock is an advisory flock and liveness is a signal-0
// kill. Every file here carries a //go:build unix constraint, so a new one
// needs it too. Of the platforms that constraint admits, macOS and Linux
// are the supported ones — see the README's Install section.
package evidence
