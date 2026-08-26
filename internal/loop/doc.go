//go:build unix

// Package loop is the reconciler: desired state is the board, actual
// state is the agent processes on this machine; the loop starts,
// adopts, or reaps until they match. There is exactly one loop.
//
// macOS and Linux only: reaping kills by process-group id. Every file here
// carries a //go:build unix constraint, so a new one needs it too.
package loop
