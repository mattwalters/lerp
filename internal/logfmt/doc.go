// Package logfmt reads a runner's log as agent activity instead of bytes.
//
// It changes nothing about the runner contract (SCOPE concept 3): lerp still
// hands a runner a prompt and a working directory and reads an exit code, and
// nothing on disk changes — the log file stays byte-identical to what the
// runner wrote, so --resume, eject and post-mortem inspection are untouched.
// This is display, applied at render time to a file lerp already owns.
//
// Raw text is the floor. A stream whose format is not recognized decodes as
// one plain-text event per line, which is what the board showed before this
// package existed, so a runner nobody has written a decoder for stays exactly
// as first-class as one that has.
package logfmt
