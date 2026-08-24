package tui

import "github.com/charmbracelet/bubbles/key"

// keymap declares every binding once; the status bar hint and the ? overlay
// both render from it, so the help can never drift from the keys.
type keymap struct {
	Attention key.Binding
	Lanes     key.Binding
	UpNext    key.Binding
	NextPanel key.Binding
	PrevPanel key.Binding
	Up        key.Binding
	Down      key.Binding
	PageUp    key.Binding
	PageDown  key.Binding
	Top       key.Binding
	Bottom    key.Binding
	Open      key.Binding
	Help      key.Binding
	Quit      key.Binding
}

func newKeymap() keymap {
	return keymap{
		Attention: key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "attention")),
		Lanes:     key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "lanes")),
		UpNext:    key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "up next")),
		NextPanel: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next panel")),
		PrevPanel: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev panel")),
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "select up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "select down")),
		PageUp:    key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup/b", "scroll up")),
		PageDown:  key.NewBinding(key.WithKeys("pgdown", "f"), key.WithHelp("pgdn/f", "scroll down")),
		Top:       key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("home/g", "top")),
		Bottom:    key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("end/G", "follow")),
		Open:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open in Linear")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp and FullHelp make keymap a bubbles/help KeyMap: the ? overlay
// renders straight from these groups.
func (k keymap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Quit}
}

func (k keymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Attention, k.Lanes, k.UpNext, k.NextPanel, k.PrevPanel},
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		{k.Open, k.Help, k.Quit},
	}
}
