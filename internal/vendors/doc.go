// Package vendors holds lerp's built-in runner adapters — one file per
// vendor CLI (SCOPE concept 3), each turning a config.Runner's Model, Effort
// and Args overrides into the command and resume templates run.Execute
// already knows how to expand.
//
// It cannot live in internal/run: config has to reject an unknown vendor at
// load, and run already imports config. vendors imports nothing of lerp's,
// so config -> vendors adds no cycle.
package vendors
