// Package loop is the reconciler: desired state is the board, actual
// state is the agent processes on this machine; the loop starts,
// adopts, or reaps until they match. There is exactly one loop.
package loop
