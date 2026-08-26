package tui

import (
	"slices"
	"strings"
	"unicode"

	// Aliased: cursor is this package's own type, the focus window's
	// reading of where the selection sits (see theme.go).
	textcursor "github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mattwalters/lerp/internal/loop"
)

// Search is the inbox's third session-only control, beside the sort mode and
// the project scope: `/` opens a prompt on the panel and the rows narrow as
// the operator types. Like the other two it is display over the one list the
// pass already fetched — it never becomes a query sent to Linear, nothing is
// saved, and there is no filter grammar behind it (SCOPE, the interface).

// newSearchInput builds the prompt the panel opens on `/`. The cursor is
// static: a blinking one is its own tick stream, and this TUI runs on one
// clock (see pollEvery).
func newSearchInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter the inbox"
	ti.PromptStyle = styleFocus
	ti.PlaceholderStyle = styleFaint
	ti.Cursor.SetMode(textcursor.CursorStatic)
	return ti
}

// openSearch puts the prompt on the inbox panel. It opens empty, the way `/`
// does in vim: the query it starts from is the one esc restores, not the one
// it edits.
func (m *model) openSearch() {
	m.searching = true
	m.searchWas = m.search
	m.searchSelWas = ""
	if it := m.selectedAttention(); it != nil {
		m.searchSelWas = it.Ticket
	}
	m.searchInput.SetValue("")
	m.searchInput.Focus()
	m.setSearch("")
}

// closeSearch hands the keyboard back to the list. Accepting leaves the
// filter in place — finding a ticket in order to promote it is the whole
// point of enter — while cancelling puts back the list the prompt opened
// over.
func (m *model) closeSearch(accept bool) {
	m.searching = false
	m.searchInput.Blur()
	if accept {
		// Onto the list, wherever the keys were when the prompt opened.
		// Narrowing is something the operator does in order to pick a row
		// out of what is left, so the first j after enter has to walk the
		// matches rather than scroll the ticket body the pane was holding.
		// Cancelling takes this back too: nothing was narrowed, and the
		// list as it was includes the keys as they were.
		m.keysInMain = false
	}
	if !accept {
		// The list as it was is the rows and the cursor both: a prompt that
		// narrowed down to nothing left setSearch no selection to carry
		// back, and the operator would return to the top of a list they
		// were reading the middle of.
		m.setSearch(m.searchWas)
		m.selectTicket(m.searchSelWas)
	}
	m.searchWas, m.searchSelWas = "", ""
}

// handleSearchKey drives the prompt. It swallows every key while it is open:
// a `p` or a `q` typed into a search has to land in the box rather than
// promote a ticket or quit the program. ctrl+c is the one exception — it
// quits from anywhere, the way it does everywhere else.
func (m model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.closeSearch(false)
	case tea.KeyEnter:
		m.closeSearch(true)
	default:
		return m.updateSearch(msg)
	}
	// The prompt closing hands the panel's last line back to the key hints,
	// and the list under it just changed height either way.
	m.refreshMain()
	m.layout()
	return m, m.wantDetail()
}

// updateSearch hands one message to the box and re-filters from what it
// holds afterwards. Keys arrive here from handleSearchKey; everything else
// the widget answers to — the clipboard read behind ctrl+v — from Update,
// which is the only place those messages come back to.
func (m model) updateSearch(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	// Operator-typed rather than Linear-sourced, but it reaches the panel
	// title and the no-match line the same way every other string does, so
	// it crosses the same boundary: the widget's own sanitizer already drops
	// control runes from a paste, and clean is what the rest of this package
	// trusts.
	m.setSearch(clean(m.searchInput.Value()))
	m.refreshMain()
	m.layout()
	return m, tea.Batch(cmd, m.wantDetail())
}

// setSearch re-filters the list under a new query. The selection follows its
// ticket while the ticket still matches; when it stops matching the cursor
// goes to the first row, which is what the operator is narrowing toward —
// clamping the old index instead would park it on whatever row happened to
// slide under it.
func (m *model) setSearch(query string) {
	was := ""
	if it := m.selectedAttention(); it != nil {
		was = it.Ticket
	}
	m.search = query
	m.resort()
	if it := m.selectedAttention(); it == nil || it.Ticket != was {
		m.attnSel = 0
	}
}

// selectTicket puts the cursor back on a ticket by identifier, if that
// ticket is still one of the rows on screen. Nothing to find is nothing to
// move.
func (m *model) selectTicket(ticket string) {
	if ticket == "" {
		return
	}
	if i := slices.IndexFunc(m.shown, func(it loop.AttentionItem) bool {
		return it.Ticket == ticket
	}); i >= 0 {
		m.attnSel = i
	}
}

// matchesSearch reports whether the query occurs in the facts the row shows:
// identifier, title, status and project. Plain substring, case-insensitive —
// predictable beats clever for something typed without looking, and fuzzy
// would mean a scorer or a dependency.
func matchesSearch(it loop.AttentionItem, query string) bool {
	if query == "" {
		return true
	}
	q := fold(query)
	for _, field := range []string{it.Ticket, it.Title, it.Status, it.Project} {
		if foldIndex(fold(field), q) >= 0 {
			return true
		}
	}
	return false
}

// highlight renders s with base, marking every case-insensitive occurrence of
// query. It runs at render time, over model state the apply boundary has
// already cleaned (see sanitize.go): the escapes it inserts land in text that
// is already inert, never the other way round.
func highlight(s, query string, base lipgloss.Style) string {
	if query == "" {
		return base.Render(s)
	}
	rs, folded, q := []rune(s), fold(s), fold(query)
	// The mark layers over the cell's own style rather than replacing it:
	// the identifier column is bold, and it must not lose its weight on the
	// very characters the search is pointing at.
	mark := styleMatch.Inherit(base)
	var b strings.Builder
	for len(rs) > 0 {
		i := foldIndex(folded, q)
		if i < 0 {
			b.WriteString(base.Render(string(rs)))
			break
		}
		if i > 0 {
			b.WriteString(base.Render(string(rs[:i])))
		}
		b.WriteString(mark.Render(string(rs[i : i+len(q)])))
		rs, folded = rs[i+len(q):], folded[i+len(q):]
	}
	return b.String()
}

// fold lowercases s rune by rune. Rune-wise rather than strings.ToLower over
// the whole string: a handful of runes lowercase to a different byte length,
// and a byte offset into that copy would then cut the original apart
// mid-character — which is how a highlight ends up splicing a rune in half.
func fold(s string) []rune {
	rs := []rune(s)
	for i, r := range rs {
		rs[i] = unicode.ToLower(r)
	}
	return rs
}

// foldIndex is strings.Index over already-folded runes: where q first occurs
// in s, or -1. A row is short and a query shorter, so the naive scan is the
// whole implementation.
func foldIndex(s, q []rune) int {
	if len(q) == 0 {
		return 0
	}
	for i := 0; i+len(q) <= len(s); i++ {
		if slices.Equal(s[i:i+len(q)], q) {
			return i
		}
	}
	return -1
}
