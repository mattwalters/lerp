package tui

import (
	"fmt"
	"slices"

	"github.com/charmbracelet/bubbles/key"
)

// keymap declares every binding once; the status bar hint and the ? overlay
// both render from it, so the help can never drift from the keys.
type keymap struct {
	Attention key.Binding
	Work      key.Binding
	NextPanel key.Binding
	PrevPanel key.Binding
	Up        key.Binding
	Down      key.Binding
	PageUp    key.Binding
	PageDown  key.Binding
	Top       key.Binding
	Bottom    key.Binding
	Detail    key.Binding
	Close     key.Binding
	Promote   key.Binding
	// Visual starts the inbox's multi-select: lazygit's own key for a range
	// selection, extended by the movement keys and fed straight to Promote.
	Visual     key.Binding
	Eject      key.Binding
	ForceStart key.Binding
	Sort       key.Binding
	Project    key.Binding
	// Backlog unfolds the inbox's summary line into the rows it stands for.
	Backlog key.Binding
	// Search opens the inbox's prompt; ClearSearch is the way back out of a
	// filter the prompt already closed on.
	Search      key.Binding
	ClearSearch key.Binding
	Open        key.Binding
	Raw         key.Binding
	Help        key.Binding
	Quit        key.Binding
}

func newKeymap() keymap {
	return keymap{
		Attention: key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "inbox")),
		Work:      key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "work")),
		// "cycle", not "next panel": tab reaches the open main pane too, and
		// it is the pane that most wants saying — the panels have 1 and 2
		// of their own. The word is also the one that fits. The ? overlay's
		// first column is as wide as its widest description, and this
		// column is already flush against a hundred-column terminal's main
		// pane, so "next surface" would truncate the row beside it.
		NextPanel: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "cycle")),
		PrevPanel: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "cycle back")),
		// "move", not "select": these keys move the row on a panel and the
		// pane a line at a time when the pane is what holds them, and the
		// overlay is the one place that lists every key. One word covering
		// both beats a description that is wrong on whichever surface the
		// operator is standing.
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "move up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "move down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup/b", "scroll up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "f"), key.WithHelp("pgdn/f", "scroll down")),
		Top:      key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("home/g", "top")),
		Bottom:   key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("end/G", "follow")),
		// Open is already o, open in Linear, so the pane's own keys take
		// names of their own. enter opens and esc closes; neither is a
		// flip-flop, so an operator who has lost track of the state can
		// press either and know what they will get.
		Detail:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open detail")),
		Close:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close detail")),
		Promote: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "promote")),
		Visual:  key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "select a range")),
		Eject:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "eject")),
		// S, not s: a letter that means two different things depending on
		// which panel has focus is worse than a letter nothing else uses.
		// The description is "past the limit", not LERP-53's "past the lane
		// limit": "lane" is the noun the TUI keeps to itself, and the longer
		// phrase is wide enough to push this whole help column off a
		// hundred-column terminal — every key in it, not just this one. It
		// is trimmed again now that enter and esc share the column: the
		// widest key and the widest description are added together, so the
		// detail pane's and the search's own keys cost this one three
		// characters back.
		ForceStart: key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "start past the limit")),
		Sort:       key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort inbox")),
		Project:    key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "filter by project")),
		// B, not b: b is already half of pgup, and a letter that means two
		// things depending on which panel has focus is what splitting S from
		// s was avoiding.
		Backlog:     key.NewBinding(key.WithKeys("B"), key.WithHelp("B", "browse the backlog")),
		Search:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search inbox")),
		ClearSearch: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear search")),
		Open:        key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open in Linear")),
		Raw:         key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "raw log")),
		Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp and FullHelp make keymap a bubbles/help KeyMap: the ? overlay
// renders straight from these groups.
func (k keymap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Quit}
}

// Two groups, not three: bubbles lays each one out as a column, and the
// main pane is narrower than the side panels' table left it. Getting about
// the terminal by moving through it, then everything a key does to the
// selection — the split reads, and it fits.
func (k keymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Attention, k.Work, k.NextPanel, k.PrevPanel,
			k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		{k.Detail, k.Close, k.Promote, k.Visual, k.Eject, k.ForceStart, k.Sort, k.Project, k.Backlog,
			k.Search, k.Open, k.Raw, k.Help, k.Quit},
	}
}

// rowKeys says which of a panel's keys are live where the cursor is
// standing: r renders something, o has a door to open, esc has a filter to
// clear, P has a project to cycle to, p has somewhere to promote into and
// the room to draw the picker, e has a live run whose runner can resume. An
// advertised key that does nothing is worse than one left out, because
// pressing it is how the operator finds out — and r would flip the raw
// toggle invisibly.
type rowKeys struct {
	hasLog     bool
	hasURL     bool
	filtered   bool
	projects   bool
	canPromote bool
	canEject   bool
	// visual is whether the inbox has a live multi-select; selected is how
	// many rows it spans — the line's "promote N" while one is on.
	visual   bool
	selected int
}

// panelHelp is the line a focused panel carries: the keys that act on the
// row under its cursor. These used to live in the main pane under a
// selected ticket, which made sort and project look like they did not exist
// until the operator had already picked a row. Navigation and the two global
// keys are left out — the status bar already carries "? help · q quit", and
// a hint that gets truncated away is a hint that was not there.
//
// Which is also why a filter swaps / for esc rather than adding it, why P
// drops out of a list with no project in it, why p drops out where there is
// no status to promote into or no room for the picker it opens, and why e
// shows only on a live run under a runner that can resume: the line is about
// forty columns wide, so a key that does nothing here costs one that does.
//
// The order is what survives a narrow panel, since bubbles drops hints off
// the end to fit: what acts on the row under the cursor first, then the two
// display cycles, whose state the panel title already carries in words. All
// five fit from about 120 columns; under that the cycles go first and the
// ellipsis says where to look for them.
func (k keymap) panelHelp(p panel, live rowKeys) []key.Binding {
	var b []key.Binding
	switch p {
	case panelAttention:
		// A selection is on: the six keys a row would otherwise advertise
		// give way to the two that end it, the way a filter swaps / for esc.
		if live.visual {
			return []key.Binding{
				short(k.Promote, fmt.Sprintf("promote %d", live.selected)),
				short(k.Close, "drop"),
			}
		}
		find := short(k.Search, "search")
		if live.filtered {
			find = short(k.ClearSearch, "clear")
		}
		b = []key.Binding{find}
		if live.canPromote {
			b = append([]key.Binding{k.Promote, short(k.Visual, "select")}, b...)
		}
		if live.hasURL {
			b = append(b, short(k.Open, "open"))
		}
		b = append(b, short(k.Sort, "sort"))
		if live.projects {
			b = append(b, short(k.Project, "project"))
		}
	case panelWork:
		if live.canEject {
			b = append(b, k.Eject)
		}
		if live.hasLog {
			b = append(b, short(k.Raw, "raw"))
		}
		if live.hasURL {
			b = append(b, short(k.Open, "open"))
		}
	}
	return b
}

// short re-labels a binding for the panel line: the ? overlay has room for
// "sort inbox", a forty-column panel does not, and on the inbox panel the
// noun is already in the title. The keys come from the binding
// itself, so the two lines can never disagree about what to press.
func short(b key.Binding, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(b.Keys()...), key.WithHelp(b.Help().Key, desc))
}

// promoteHelp is the promote picker's instruction line. The picker is
// modal, so this is the whole of what it answers to, and it is built from
// the same bindings handlePromoteKey matches — rebind Up and the picker and
// its line move together. Quit backs the picker out as well and is left
// off: two ways out is one more than the line has columns for, and esc is
// the one the rest of the TUI already means it by. That is a hint the
// picker does not give rather than one it gets wrong — rebind Quit and
// nothing here starts naming the wrong key.
// The order is what survives a narrow bar, the way panelHelp's is: bubbles
// drops hints off the end to fit, so the two keys that end the modal come
// before the pair that only moves inside it. A picker whose line has been
// cut down to "enter promote · esc cancel…" still says both ways out; one
// cut down to "↑/k ↓/j choose…" says neither. promoteExits is where that
// cutting stops.
func (k keymap) promoteHelp() []key.Binding {
	return []key.Binding{
		short(k.Detail, "promote"),
		short(k.Close, "cancel"),
		pair(k.Up, k.Down, "choose"),
	}
}

// promoteExits is what the picker's line falls back to rather than cut
// down: the two keys that end the modal. Taken from promoteHelp itself, so
// the floor can never come to name a key the line does not. It is a floor
// under what pickerLine drops, not under statusBar's truncation — a window
// too narrow to hold even these has the bar shear them like anything else.
func (k keymap) promoteExits() []key.Binding { return k.promoteHelp()[:2] }

// pair renders two bindings as one hint — "↑/k ↓/j choose" — where naming a
// direction twice would cost more than the line has. Reading the labels off
// the bindings is what this ticket is for, and "↑/k ↓/j" is four columns
// wider than the "↑/↓" the hardcoded line could afford to write; spelling up
// and down out as two hints costs seven. The bar's budget decides what a
// narrow window keeps, so the saving is in what is left to drop.
//
// Keys from both, so the binding is honest about what it stands for, though
// nothing matches against it — it is only ever rendered, and Help().Key is
// what the operator reads.
func pair(a, b key.Binding, desc string) key.Binding {
	return key.NewBinding(
		key.WithKeys(slices.Concat(a.Keys(), b.Keys())...),
		key.WithHelp(a.Help().Key+" "+b.Help().Key, desc))
}
