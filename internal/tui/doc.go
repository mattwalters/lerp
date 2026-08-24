// Package tui is the Bubble Tea interface: one screen with the
// needs-you and work panels beside a main pane that follows the
// selected row, plus promote and eject. The TUI drives the loop;
// there is no daemon.
//
// A running ticket's row reads its log as agent activity — decoded at
// render time by internal/logfmt, never on disk — with a raw toggle
// for when the decoding is wrong, and plain text as the floor.
//
// Text from Linear is untrusted — a ticket title is written by whoever
// can file one, and the View string reaches the terminal as-is. The
// model's apply is the boundary: it cleans every Linear-sourced string
// on the way in (see sanitize.go), so model state is already inert and
// the views stay plain string building. Agent output is untrusted the
// same way and cleaned where it becomes a row (see logview.go).
package tui
