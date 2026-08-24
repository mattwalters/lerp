// Package tui is the Bubble Tea interface: one screen with the
// needs-you, running, and up-next panels beside a main pane that
// follows focus, plus promote and eject. The TUI drives the loop;
// there is no daemon.
//
// Text from Linear is untrusted — a ticket title is written by whoever
// can file one, and the View string reaches the terminal as-is. The
// model's apply is the boundary: it cleans every Linear-sourced string
// on the way in (see sanitize.go), so model state is already inert and
// the views stay plain string building.
package tui
