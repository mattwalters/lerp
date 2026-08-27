// opening beside them on enter and closing on esc, plus promote — of
// a single row, or of a run of adjacent ones held in the inbox's
// visual mode — and eject. The promote picker, eject and help float
// over the board as modals (see modal.go). An open pane is a surface
// tab reaches, and the keys scroll it while it has them; the row it is
// reading is still the panel's. The TUI drives the loop; there is no daemon.
//
// A running ticket's row reads its log as agent activity — decoded at
// render time by internal/logfmt, never on disk — with a raw toggle
// for when the decoding is wrong, and plain text as the floor.
//
// The row reads the same log for its own second line: how long since
// the log grew, and a sparkline of recent activity (see pulse.go).
// That is a reading for the operator,
// never a timeout — SCOPE defers hang detection, and nothing here
// holds a threshold or acts on a number.
//
// A ticket the operator selects is read out of Linear into that same
// pane, description and comments alike, rendered as the markdown
// Linear stores rather than as its source (see markdown.go).
//
// Text from Linear is untrusted — a ticket title is written by whoever
// can file one, and the View string reaches the terminal as-is. The
// model's apply is the boundary: it cleans every Linear-sourced string
// on the way in (see sanitize.go), so model state is already inert and
// the views stay plain string building. Agent output is untrusted the
// same way and cleaned where it becomes a row (see logview.go).
package tui
