package initui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/mattwalters/lerp/internal/theme"
)

type keymap struct {
	Up     key.Binding
	Down   key.Binding
	Left   key.Binding
	Right  key.Binding
	Toggle key.Binding
	Enter  key.Binding
	Back   key.Binding
	Quit   key.Binding
}

func newKeymap() keymap {
	return keymap{
		Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "move up")),
		Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "move down")),
		Left:   key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "previous")),
		Right:  key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next")),
		Toggle: key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
		Enter:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue")),
		Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "cancel")),
	}
}

func renderHelp(bindings ...key.Binding) string {
	var parts []string
	for _, b := range bindings {
		h := b.Help()
		if h.Key == "" || h.Desc == "" {
			continue
		}
		parts = append(parts, theme.Ticket.Render(h.Key)+" "+theme.Faint.Render(h.Desc))
	}
	return strings.Join(parts, theme.Faint.Render("  ·  "))
}
