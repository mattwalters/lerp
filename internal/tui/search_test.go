package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mattwalters/lerp/internal/loop"
)

// searching opens the prompt on a board-loaded inbox and types query into
// it, one key at a time — the way an operator does, so every assertion after
// it is about a list that narrowed incrementally.
func searching(t *testing.T, query string) model {
	t.Helper()
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	return typeSearch(t, update(t, m, keyMsg("/")), query)
}

func typeSearch(t *testing.T, m model, query string) model {
	t.Helper()
	for _, r := range query {
		m = update(t, m, keyMsg(string(r)))
	}
	return m
}

// shownTickets is the inbox as the panel would draw it, by identifier.
func shownTickets(m model) []string {
	out := make([]string, 0, len(m.shown))
	for _, it := range m.shown {
		out = append(out, it.Ticket)
	}
	return out
}

// Done-when: the list narrows as the operator types, with no enter needed to
// see it, and the panel title says how far it narrowed and what to.
func TestSearchNarrowsAsYouType(t *testing.T) {
	m := searching(t, "gore")

	if !m.searching {
		t.Fatal("/ did not open the prompt")
	}
	if got, want := shownTickets(m), []string{"LERP-22"}; !slices.Equal(got, want) {
		t.Fatalf("shown = %v, want %v (filtering is incremental — no enter yet)", got, want)
	}

	panel := m.attentionPanel(96, 14)
	if !strings.Contains(panel, "1/6") {
		t.Fatalf("the title does not say how much of the list is on screen:\n%s", panel)
	}
	if !strings.Contains(panel, "/gore") {
		t.Fatalf("the title does not carry the query:\n%s", panel)
	}
	if strings.Contains(panel, "LERP-1 ") {
		t.Fatalf("a row that does not match is still on the panel:\n%s", panel)
	}
	// A deleted character widens the list again: the filter follows the box.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := len(m.shown); got != 1 {
		t.Fatalf(`"gor" shows %d rows, want the one GoReleaser ticket`, got)
	}
	m = typeSearch(t, m, "x")
	if got := len(m.shown); got != 0 {
		t.Fatalf(`"gorx" shows %d rows, want none`, got)
	}
}

// Done-when: the four facts a row already shows are the four the search
// matches, case-insensitively, as a plain substring.
func TestSearchMatchesTheColumnsOnTheRow(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"lerp-48", []string{"LERP-48"}},         // identifier, folded
		{"CURL", []string{"LERP-23"}},            // title, folded
		{"in review", []string{"LERP-48"}},       // status, with a space in it
		{"tui redesign", []string{"LERP-48"}},    // project
		{"work", []string{"LERP-60", "LERP-70"}}, // a substring of two titles
		{"zzz", nil},                             // nothing
		{"", []string{"LERP-1", "LERP-22", "LERP-23", // every row, in the sort's order
			"LERP-48", "LERP-60", "LERP-70"}},
	} {
		m := searching(t, tc.query)
		got := shownTickets(m)
		slices.Sort(got)
		want := slices.Clone(tc.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("search %q shows %v, want %v", tc.query, got, want)
		}
	}
}

// Done-when: the prompt takes the keyboard while it is open. A p or a q
// typed into a search is text, not a promote and not a quit — ctrl+c is the
// one key that still means what it always means.
func TestSearchSwallowsKeysWhileTheBoxIsOpen(t *testing.T) {
	m := searching(t, "p")
	m, cmd := updateCmd(t, m, keyMsg("q"))

	if m.promoting {
		t.Fatal("p typed into the search opened the promote picker")
	}
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("q typed into the search quit lerp")
		}
	}
	if got := m.searchInput.Value(); got != "pq" {
		t.Fatalf("the box holds %q, want the keys that were typed", got)
	}
	// The other panel keys are text too: none of them may act.
	before := m.sortMode
	m = typeSearch(t, m, "sP?2")
	if m.sortMode != before || m.helpOn || m.focus != panelAttention {
		t.Fatalf("a key typed into the search acted on the panel: sort=%v help=%v focus=%v",
			m.sortMode, m.helpOn, m.focus)
	}

	m, cmd = updateCmd(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in the search produced no command")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("ctrl+c in the search did not quit")
	}
}

// Done-when: enter closes the box and keeps the rows narrowed, and the keys
// go back to the list — the point of searching is to promote what you found.
func TestSearchEnterKeepsTheFilterAndGivesTheKeysBack(t *testing.T) {
	m := update(t, searching(t, "gore"), keyMsg("enter"))

	if m.searching {
		t.Fatal("enter left the prompt open")
	}
	if m.search != "gore" || len(m.shown) != 1 {
		t.Fatalf("enter dropped the filter: query %q, %d rows", m.search, len(m.shown))
	}
	m = update(t, m, keyMsg("p"))
	if !m.promoting {
		t.Fatal("p after an accepted search did not reach the list")
	}
	if got := m.selectedAttention().Ticket; got != "LERP-22" {
		t.Fatalf("the picker is aimed at %s, want the ticket the search found", got)
	}
}

// Done-when: esc from the prompt puts back the list it opened over, and esc
// with a filter applied and no prompt open clears the filter.
func TestSearchEscCancelsThenClears(t *testing.T) {
	m := update(t, searching(t, "gore"), keyMsg("enter"))

	// A second search over the first: the prompt opens empty, so the list is
	// whole again while it is being typed into.
	m = update(t, m, keyMsg("/"))
	if len(m.shown) != 6 {
		t.Fatalf("the prompt opened onto %d rows, want the whole list", len(m.shown))
	}
	m = typeSearch(t, m, "curl")
	if got := shownTickets(m); !slices.Equal(got, []string{"LERP-23"}) {
		t.Fatalf("shown = %v, want the ticket the second search matched", got)
	}

	m = update(t, m, keyMsg("esc"))
	if m.searching {
		t.Fatal("esc left the prompt open")
	}
	if m.search != "gore" || !slices.Equal(shownTickets(m), []string{"LERP-22"}) {
		t.Fatalf("esc did not put the list back as it was: query %q, rows %v",
			m.search, shownTickets(m))
	}

	m = update(t, m, keyMsg("esc"))
	if m.search != "" || len(m.shown) != 6 {
		t.Fatalf("esc with no prompt open did not clear the filter: query %q, %d rows",
			m.search, len(m.shown))
	}
}

// Done-when: the three controls compose. Search filters, sort orders, the
// project scope narrows — and none of them resets the others.
func TestSearchComposesWithSortAndProject(t *testing.T) {
	m := update(t, searching(t, "work"), keyMsg("enter"))

	m = update(t, m, keyMsg("s")) // status -> project
	if m.search != "work" {
		t.Fatalf("sorting cleared the search: %q", m.search)
	}
	if got := shownTickets(m); !slices.Equal(got, []string{"LERP-60", "LERP-70"}) {
		t.Fatalf("shown after sorting = %v, want the same two rows", got)
	}

	// Both filters at once intersect: the OSS project holds no row matching
	// "work", and the panel says so with both facts on the line.
	m = update(t, m, keyMsg("P"))
	if m.project != "Open-source readiness" || m.search != "work" {
		t.Fatalf("the two filters did not compose: project %q, query %q", m.project, m.search)
	}
	if len(m.shown) != 0 {
		t.Fatalf("shown = %v, want no row in both the project and the search", shownTickets(m))
	}
	panel := m.attentionPanel(96, 14)
	if !strings.Contains(panel, "no match for /work in Open-source readiness") {
		t.Fatalf("the panel does not say why it is empty:\n%s", panel)
	}

	// Clearing the search leaves the project scope standing.
	m = update(t, m, keyMsg("esc"))
	if m.project != "Open-source readiness" {
		t.Fatalf("clearing the search cleared the project scope: %q", m.project)
	}
	if got := len(m.shown); got != 3 {
		t.Fatalf("the project scope shows %d rows, want the 3 it had", got)
	}
}

// Done-when: the selected ticket survives the filter when it still matches,
// and the cursor goes to the first row rather than vanishing when it does
// not.
func TestSearchKeepsTheSelectionOrTakesItToTheTop(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})
	m = update(t, m, keyMsg("j")) // LERP-48, second under the status default

	m = typeSearch(t, update(t, m, keyMsg("/")), "read")
	if got := m.selectedAttention().Ticket; got != "LERP-48" {
		t.Fatalf("selection = %s, want the ticket the search kept", got)
	}

	// A search matching nothing has nothing to select.
	m = typeSearch(t, m, "@")
	if m.selectedAttention() != nil {
		t.Fatalf("a search matching nothing still has a selection: %v", *m.selectedAttention())
	}

	// The row the cursor was on stops matching while other rows still do:
	// the cursor goes to the first of them. Clamping the old index instead
	// would leave it on LERP-22, which is only where it lands because that
	// is what slid under it.
	m = update(t, m, keyMsg("esc"))
	if got := m.selectedAttention().Ticket; got != "LERP-48" {
		t.Fatalf("selection after cancelling = %s, want the row it started on", got)
	}
	m = typeSearch(t, update(t, m, keyMsg("/")), "b")
	if got := shownTickets(m); len(got) < 3 || got[0] != "LERP-1" {
		t.Fatalf("shown = %v, want several rows headed by LERP-1", got)
	}
	if got := m.attnSel; got != 0 {
		t.Fatalf("selection index = %d, want the first row", got)
	}
	if got := m.selectedAttention().Ticket; got != "LERP-1" {
		t.Fatalf("selection = %s, want the first row of the filtered list", got)
	}
}

// Done-when: a pass landing under a live filter changes the rows and leaves
// the filter alone — a pass arrives every few seconds, and a search silently
// cleared by one is a list that widens under the operator's hands. An inbox
// that empties is the exception: there is nothing left to narrow, and the
// title stops carrying the query along with the count.
func TestSearchSurvivesAPass(t *testing.T) {
	m := update(t, searching(t, "work"), keyMsg("enter"))

	m = update(t, m, eventMsg{ev: board()})
	if m.search != "work" {
		t.Fatalf("a pass cleared the accepted search: %q", m.search)
	}
	if got := shownTickets(m); !slices.Equal(got, []string{"LERP-60", "LERP-70"}) {
		t.Fatalf("shown after a pass = %v, want the filtered rows", got)
	}

	// The same holds with the prompt still open.
	m = typeSearch(t, update(t, m, keyMsg("/")), "curl")
	m = update(t, m, eventMsg{ev: board()})
	if !m.searching || m.search != "curl" {
		t.Fatalf("a pass closed the prompt: open %v, query %q", m.searching, m.search)
	}
	if got := shownTickets(m); !slices.Equal(got, []string{"LERP-23"}) {
		t.Fatalf("shown = %v, want the row the open prompt matched", got)
	}

	// An empty inbox takes the prompt and the query with it, so the pass
	// that repopulates the board is not narrowed by a query nothing shows.
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention}})
	if m.searching || m.search != "" {
		t.Fatalf("an empty inbox kept the prompt: open %v, query %q", m.searching, m.search)
	}
	m = update(t, m, eventMsg{ev: board()})
	if got := len(m.shown); got != 6 {
		t.Fatalf("the board came back filtered: %d rows, want all 6", got)
	}
}

// Done-when: the box's own messages reach it. A clipboard read on ctrl+v
// comes back as a message this model has no case for, and dropping it would
// leave the paste unseen and the rows unnarrowed.
func TestSearchTakesTheBoxsOwnMessages(t *testing.T) {
	type widgetMsg struct{}
	m := searching(t, "")
	m.searchInput.SetValue("curl") // where a clipboard read leaves it

	m = update(t, m, widgetMsg{})
	if m.search != "curl" {
		t.Fatalf("query = %q, want what the box holds after its own message", m.search)
	}
	if got := shownTickets(m); !slices.Equal(got, []string{"LERP-23"}) {
		t.Fatalf("shown = %v, want the rows the pasted query matches", got)
	}

	// With the prompt closed the same message is not the box's, and the
	// value left in it means nothing.
	m = update(t, m, keyMsg("esc"))
	m.searchInput.SetValue("gore")
	m = update(t, m, widgetMsg{})
	if m.search != "" || len(m.shown) != 6 {
		t.Fatalf("a closed box still filtered the list: query %q, %d rows", m.search, len(m.shown))
	}
}

// Done-when: a search that matches nothing says so. An empty box would read
// as an empty board, which is the failure this feature has to design
// against — and the way back out is on the panel and in the pane.
func TestSearchWithNoMatchesSaysSo(t *testing.T) {
	m := update(t, searching(t, "zzz"), keyMsg("enter"))

	panel := m.attentionPanel(96, 14)
	if !strings.Contains(panel, "no match for /zzz") {
		t.Fatalf("a search that matched nothing renders an empty box:\n%s", panel)
	}
	if !strings.Contains(panel, "esc clear search") {
		t.Fatalf("the panel does not offer the key that puts the rows back:\n%s", panel)
	}
	if strings.Contains(panel, "the inbox is empty") {
		t.Fatalf("a filtered list claims the goal state:\n%s", panel)
	}
	if view := m.View(); !strings.Contains(view, "(esc clears the search)") {
		t.Fatalf("the main pane does not say how to get the list back:\n%s", view)
	}
}

// Done-when: `/` is on the panel's key line, and it is the prompt that line
// carries once the box is open.
func TestSearchIsOnTheKeyLineAndTakesIt(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: board()})

	panel := m.attentionPanel(96, 14)
	if !strings.Contains(panel, "/ search") {
		t.Fatalf("the inbox panel does not offer /:\n%s", panel)
	}

	m = update(t, m, keyMsg("/"))
	panel = m.attentionPanel(96, 14)
	if strings.Contains(panel, "/ search") || strings.Contains(panel, "p promote") {
		t.Fatalf("the key line is still there with the prompt open:\n%s", panel)
	}
	if !strings.Contains(panel, "filter the inbox") {
		t.Fatalf("the prompt is not on the panel:\n%s", panel)
	}
	if !strings.Contains(m.View(), "enter accept · esc cancel") {
		t.Fatalf("the status bar does not say how to leave the prompt:\n%s", m.View())
	}

	// A panel squeezed to two inner lines spends them on rows: the prompt
	// follows the same rule the key hints do, because the title carries the
	// query either way and a panel showing only "⋯ n more" has lost the
	// cursor.
	m = typeSearch(t, m, "back")
	squeezed := m.attentionPanel(60, 4)
	// Once in the title, and not a second time as a prompt on the last row.
	if got := strings.Count(squeezed, "/back"); got != 1 {
		t.Fatalf("the query is on %d lines of a squeezed panel, want the title only:\n%s",
			got, squeezed)
	}
	if !strings.Contains(squeezed, "LERP-22") {
		t.Fatalf("a squeezed panel lost the row under the cursor:\n%s", squeezed)
	}
}

// Done-when: the geometry buys the line the prompt is drawn on. It replaces
// the key hints, which the focused panel had already bought — so opening the
// prompt must not cost a row on top of it.
func TestTheOpenPromptBuysItsOwnLine(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = fillBoard(t, m, 15)

	view := update(t, m, keyMsg("/")).View()
	if !strings.Contains(view, "filter the inbox") {
		t.Fatalf("the prompt is not on the panel:\n%s", view)
	}
	// LERP-9 is the last row in this list's order: one line short and the
	// panel would window it away behind a "⋯ n more".
	if !strings.Contains(view, "LERP-9 ") {
		t.Fatalf("opening the prompt cost a row the geometry did not buy:\n%s", view)
	}
}

// forceColour makes the styles render the escapes they would in a terminal.
// A test binary writes to a pipe, so lipgloss picks the ASCII profile and
// every style renders as plain text — which would let an assertion about a
// mark pass over a highlight that marked nothing. The package runs its
// tests in sequence and the old profile goes back on the way out.
func forceColour(t *testing.T) {
	t.Helper()
	was := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(was) })
}

// Done-when: matches are marked inside the row the panel draws — not just
// by highlight in isolation.
func TestSearchHighlightsTheMatchInTheRow(t *testing.T) {
	forceColour(t)
	m := searching(t, "rele")
	// Not rowOf: with the profile forced, the identifier cell carries the
	// escapes that make it bold and the marks split the title, so the row is
	// found by a span of it that neither touches.
	row := lineWith(t, m.attentionPanel(96, 14), "tagged")

	if !strings.Contains(row, "aser: tagged "+styleMatch.Render("rele")+"ases") {
		t.Fatalf("the match is not marked inside the row:\n%q", row)
	}
	if !strings.Contains(row, "Go"+styleMatch.Render("Rele")+"aser") {
		t.Fatalf("the first occurrence in the same cell is not marked:\n%q", row)
	}
}

func TestHighlightMarksEveryOccurrence(t *testing.T) {
	forceColour(t)
	mark := styleMatch.Render
	for _, tc := range []struct{ s, query, want string }{
		{"LERP-22", "22", "LERP-" + mark("22")},
		{"GoReleaser", "e", "GoR" + mark("e") + "l" + mark("e") + "as" + mark("e") + "r"},
		{"Needs Attention", "NEEDS", mark("Needs") + " Attention"},
		{"curl install", "zzz", "curl install"},
		{"curl install", "", "curl install"},
		{"", "curl", ""},
		// A rune that lowercases to a different byte length: folding
		// rune-wise is what keeps the mark on the character it matched
		// instead of splicing one in half.
		{"KİLİM", "lim", "Kİ" + mark("LİM")},
	} {
		if got := highlight(tc.s, tc.query, stylePlain); got != tc.want {
			t.Fatalf("highlight(%q, %q) = %q, want %q", tc.s, tc.query, got, tc.want)
		}
	}
	if !styleMatch.GetUnderline() {
		t.Fatal("the search mark is colour alone: it has to read on a 16-colour terminal too")
	}
	// The mark layers over the cell's own style rather than replacing it:
	// the identifier column is bold, and the characters the search points at
	// are the last ones that should lose their weight.
	got := highlight("LERP-22", "22", styleTicket)
	want := styleTicket.Render("LERP-") + styleMatch.Inherit(styleTicket).Render("22")
	if got != want {
		t.Fatalf("highlight over a bold cell = %q, want %q", got, want)
	}
}

// Done-when: esc means "close what is open" before it means "clear the
// filter", and clearing works from either panel — the filter is on the list
// wherever the operator is standing.
func TestEscClosesAnOverlayBeforeItClearsTheFilter(t *testing.T) {
	m := update(t, searching(t, "gore"), keyMsg("enter"))

	m = update(t, m, keyMsg("?"))
	m = update(t, m, keyMsg("esc"))
	if m.helpOn {
		t.Fatal("esc did not close the help overlay")
	}
	if m.search != "gore" {
		t.Fatalf("esc reached past the overlay and cleared the filter: %q", m.search)
	}

	// From the work panel, with no overlay in the way.
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("esc"))
	if m.search != "" || len(m.shown) != 6 {
		t.Fatalf("esc off the inbox panel left the filter on: %q, %d rows", m.search, len(m.shown))
	}
}

// Done-when: highlighting runs after the sanitizing boundary, never before.
// A hostile title is already inert in model state, so the escapes highlight
// inserts are the only ones on the row — and searching it cannot put the
// ticket's own back.
func TestSearchOverAHostileTitleStaysInert(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: hostile, Status: "Backlog", Reason: "unclaimed"},
	}}})

	m = typeSearch(t, update(t, m, keyMsg("/")), "pwn")
	if len(m.shown) != 1 {
		t.Fatalf("the cleaned title did not match its own text: %d rows", len(m.shown))
	}
	view := m.View()
	escapeFree(t, "a highlighted hostile title", view)
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("a highlighted row is %d cells wide in a %d-column window:\n%s",
				got, m.width, view)
		}
	}
}
