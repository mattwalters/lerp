package tui

import "github.com/charmbracelet/bubbles/key"

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
	Sort      key.Binding
	Project   key.Binding
	Open      key.Binding
	Raw       key.Binding
	Help      key.Binding
	Quit      key.Binding
}

func newKeymap() keymap {
	return keymap{
		Attention: key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "inbox")),
		Work:      key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "work")),
		NextPanel: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next panel")),
		PrevPanel: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev panel")),
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "select up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "select down")),
		PageUp:    key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup/b", "scroll up")),
		PageDown:  key.NewBinding(key.WithKeys("pgdown", "f"), key.WithHelp("pgdn/f", "scroll down")),
		Top:       key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("home/g", "top")),
		Bottom:    key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("end/G", "follow")),
		// Open is already o, open in Linear, so the pane's own keys take
		// names of their own. enter opens and esc closes; neither is a
		// flip-flop, so an operator who has lost track of the state can
		// press either and know what they will get.
		Detail:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open detail")),
		Close:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close detail")),
		Promote: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "promote")),
		Sort:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort inbox")),
		Project: key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "filter by project")),
		Open:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open in Linear")),
		Raw:     key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "raw log")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
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
		{k.Detail, k.Close, k.Promote, k.Sort, k.Project, k.Open, k.Raw, k.Help, k.Quit},
	}
}

// panelHelp is the line a focused panel carries: the keys that act on the
// row under its cursor. These used to live in the main pane under a
// selected ticket, which made sort and project look like they did not exist
// until the operator had already picked a row. Navigation and the two global
// keys are left out — the status bar already carries "? help · q quit", and
// a hint that gets truncated away is a hint that was not there.
//
// hasLog, hasURL and canPromote say which of these keys the row under the
// cursor actually answers to: r is inert on a ticket that has never run, o on
// a run whose ticket the pass no longer lists, and p where there is no status
// to promote into or no room for the picker the key opens. An advertised key
// that does nothing is worse than one left out, because pressing it is how
// the operator finds out — and r would flip the raw toggle invisibly.
func (k keymap) panelHelp(p panel, hasLog, hasURL, canPromote bool) []key.Binding {
	var b []key.Binding
	switch p {
	case panelAttention:
		b = []key.Binding{short(k.Sort, "sort"), short(k.Project, "project")}
		if canPromote {
			b = append([]key.Binding{k.Promote}, b...)
		}
	case panelWork:
		if hasLog {
			b = append(b, short(k.Raw, "raw"))
		}
	}
	if hasURL {
		b = append(b, short(k.Open, "open"))
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
