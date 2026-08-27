package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/loop"
)

func filterTestBoard() loop.Event {
	return loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-1", TicketID: "id-1", Title: "Fix the build", Status: "Needs Attention",
			Project: "Core", Relevance: loop.StatusFailed, Priority: 1,
			Claimed: true, Reason: `claimed in "Needs Attention"`},
		{Ticket: "LERP-2", TicketID: "id-2", Title: "Tagged releases", Status: "Backlog",
			Project: "Core", Relevance: loop.StatusBacklog, Priority: 2},
		{Ticket: "LERP-3", TicketID: "id-3", Title: "Install script", Status: "Backlog",
			Project: "Tools", Relevance: loop.StatusBacklog, Priority: 3},
		{Ticket: "LERP-4", TicketID: "id-4", Title: "Unprioritized feature", Status: "In Progress",
			Project: "", Relevance: loop.StatusUnnamed, Priority: 0},
		{Ticket: "LERP-5", TicketID: "id-5", Title: "Documentation", Status: "In Review",
			Project: "Tools", Relevance: loop.StatusFinished, Priority: 4},
	}}
}

// Done-when: F opens the modal at field level, j/k or arrows move between
// fields, enter descends into the value list, and esc closes the modal.
func TestFilterModalFieldNavigation(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: filterTestBoard()})

	// Press F to open modal
	m = update(t, m, keyMsg("F"))
	if !m.filtering || m.filterLevel != filterLevelField {
		t.Fatalf("F did not open filter modal at field level: filtering=%v, level=%v", m.filtering, m.filterLevel)
	}
	if m.filterSel != 0 {
		t.Fatalf("filterSel = %d, want 0 (project)", m.filterSel)
	}

	// Move down with j to status
	m = update(t, m, keyMsg("j"))
	if m.filterSel != 1 {
		t.Fatalf("j did not move to status (sel=%d)", m.filterSel)
	}

	// Move down with down arrow to priority
	m = update(t, m, keyMsg("down"))
	if m.filterSel != 2 {
		t.Fatalf("down did not move to priority (sel=%d)", m.filterSel)
	}

	// Move down at bottom does not go past end
	m = update(t, m, keyMsg("down"))
	if m.filterSel != 2 {
		t.Fatalf("down at bottom moved out of bounds (sel=%d)", m.filterSel)
	}

	// Move up with k to status
	m = update(t, m, keyMsg("k"))
	if m.filterSel != 1 {
		t.Fatalf("k did not move up to status (sel=%d)", m.filterSel)
	}

	// Move up with up arrow to project
	m = update(t, m, keyMsg("up"))
	if m.filterSel != 0 {
		t.Fatalf("up did not move to project (sel=%d)", m.filterSel)
	}

	// Esc closes modal
	m = update(t, m, keyMsg("esc"))
	if m.filtering {
		t.Fatal("esc did not close filter modal")
	}

	// Q also closes modal at field level
	m = update(t, m, keyMsg("F"))
	if !m.filtering {
		t.Fatal("F did not open filter modal")
	}
	m = update(t, m, keyMsg("q"))
	if m.filtering {
		t.Fatal("q did not close filter modal at field level")
	}
}

// Done-when: selecting a status sets the status slice — the one control over
// that axis — renders it in the title, and shows the correct count. The value
// list is built over the whole pass, so a status the all-slice folds away
// (Backlog here) is still offered and still reachable.
func TestFilterByStatus(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: filterTestBoard()})

	// F -> move to status -> enter
	m = update(t, m, keyMsg("F"))
	m = update(t, m, keyMsg("j")) // status
	m = update(t, m, keyMsg("enter"))

	if !m.filtering || m.filterLevel != filterLevelValue || m.filterFieldCur != filterFieldStatus {
		t.Fatalf("enter did not descend into status value list: filtering=%v, level=%v, field=%v",
			m.filtering, m.filterLevel, m.filterFieldCur)
	}

	// Narrow by typing "needs"
	for _, c := range "needs" {
		m = update(t, m, keyMsg(string(c)))
	}
	m = update(t, m, keyMsg("enter"))

	if m.filtering {
		t.Fatal("enter did not close modal")
	}
	if m.slice != "Needs Attention" {
		t.Fatalf("slice = %q, want 'Needs Attention'", m.slice)
	}
	if m.filterField != filterFieldNone {
		t.Fatalf("status also landed in the filter slot: %v %q", m.filterField, m.filterValue)
	}
	if len(m.shown) != 1 || m.shown[0].Ticket != "LERP-1" {
		t.Fatalf("shown = %+v, want 1 ticket LERP-1", m.shown)
	}

	panel := m.attentionPanel(96, 14)
	if !strings.Contains(panel, "● 1") || !strings.Contains(panel, "Needs Attention") {
		t.Fatalf("title missing status slice indicator:\n%s", panel)
	}

	// The Backlog rows the all-slice folds away are still on the status list
	// and still reachable — the list is built over the pass, not the slice.
	m = update(t, m, keyMsg("F"))
	if m.filterFieldCur != filterFieldStatus {
		t.Fatalf("F reopened on %v, want the status list the slice is set from", m.filterFieldCur)
	}
	var offered []string
	for _, it := range m.filterValues(filterFieldStatus) {
		if !it.isAll {
			offered = append(offered, it.label)
		}
	}
	if !slices.Contains(offered, "Backlog") {
		t.Fatalf("status list offers %v, want it to include the folded Backlog rows", offered)
	}
}

// Done-when: selecting a priority filters the inbox by priority, including Urgent and No priority.
func TestFilterByPriority(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: filterTestBoard()})

	// Filter by Urgent (1)
	m = update(t, m, keyMsg("F"))
	m = update(t, m, keyMsg("down")) // status
	m = update(t, m, keyMsg("down")) // priority
	m = update(t, m, keyMsg("enter"))

	// The all-slice folds the two unclaimed Backlog rows away, so the values
	// are [all priority (3), Urgent (1), Low (1), no priority (1)].
	m = update(t, m, keyMsg("down")) // Urgent
	m = update(t, m, keyMsg("enter"))

	if m.filterField != filterFieldPriority || m.filterValue != "Urgent" {
		t.Fatalf("filter = %v %q, want priority Urgent", m.filterField, m.filterValue)
	}
	if len(m.shown) != 1 || m.shown[0].Ticket != "LERP-1" {
		t.Fatalf("shown = %+v, want 1 ticket LERP-1", m.shown)
	}
	panel := m.attentionPanel(96, 14)
	if !strings.Contains(panel, "priority Urgent") {
		t.Fatalf("title missing priority Urgent:\n%s", panel)
	}

	// Filter by "no priority" (Priority 0)
	m = update(t, m, keyMsg("F"))
	// Reopening an active filter goes straight to priority values with Urgent (index 1) selected
	if m.filterSel != 1 {
		t.Fatalf("reopened filterSel = %d, want 1 (Urgent)", m.filterSel)
	}
	// Navigate down to "no priority" (index 3)
	for range 2 {
		m = update(t, m, keyMsg("down"))
	}
	m = update(t, m, keyMsg("enter"))

	if m.filterField != filterFieldPriority || m.filterValue != "" {
		t.Fatalf("filter = %v %q, want priority '' (no priority)", m.filterField, m.filterValue)
	}
	if len(m.shown) != 1 || m.shown[0].Ticket != "LERP-4" {
		t.Fatalf("shown = %+v, want 1 ticket LERP-4 (no priority)", m.shown)
	}
	panel = m.attentionPanel(96, 14)
	if !strings.Contains(panel, "priority none") {
		t.Fatalf("title missing priority none:\n%s", panel)
	}
}

// Done-when: selecting "no project" filters tickets filed under no project.
func TestFilterByNoProject(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: filterTestBoard()})

	// P shortcut opens project values
	m = update(t, m, keyMsg("P"))
	// Values: [all project (5), Core (2), Tools (2), no project (1)]
	m = update(t, m, keyMsg("down")) // Core
	m = update(t, m, keyMsg("down")) // Tools
	m = update(t, m, keyMsg("down")) // no project
	m = update(t, m, keyMsg("enter"))

	if m.filterField != filterFieldProject || m.filterValue != "" {
		t.Fatalf("filter = %v %q, want project '' (no project)", m.filterField, m.filterValue)
	}
	if len(m.shown) != 1 || m.shown[0].Ticket != "LERP-4" {
		t.Fatalf("shown = %+v, want LERP-4", m.shown)
	}
	panel := m.attentionPanel(96, 14)
	if !strings.Contains(panel, "project none") {
		t.Fatalf("title missing project none:\n%s", panel)
	}
}

// Done-when: type-ahead in value list narrows values, swallows letters (like p, q, j, k),
// and allows arrow selection within filtered results.
func TestFilterTypeAhead(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: filterTestBoard()})

	m = update(t, m, keyMsg("P"))
	// Type "tool"
	m = update(t, m, keyMsg("t"))
	m = update(t, m, keyMsg("o"))
	m = update(t, m, keyMsg("o"))
	m = update(t, m, keyMsg("l"))

	// Matched items should only be "Tools"
	items := m.matchingFilterValues()
	if len(items) != 1 || items[0].value != "Tools" {
		t.Fatalf("matching values = %+v, want [Tools]", items)
	}

	// Press enter to apply "Tools"
	m = update(t, m, keyMsg("enter"))
	if m.filterField != filterFieldProject || m.filterValue != "Tools" {
		t.Fatalf("filter = %v %q, want Tools", m.filterField, m.filterValue)
	}
	if len(m.shown) != 1 || m.shown[0].Ticket != "LERP-5" {
		t.Fatalf("shown = %+v, want the one unfolded Tools row (LERP-5)", m.shown)
	}
}

// Done-when: esc in value list backs out to field level, clearing search query,
// and esc in field level closes the modal.
func TestFilterEscBacksOut(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: filterTestBoard()})

	m = update(t, m, keyMsg("F"))
	m = update(t, m, keyMsg("enter")) // project values
	m = update(t, m, keyMsg("x"))     // typed in query

	if m.filterInput.Value() != "x" {
		t.Fatalf("query = %q, want 'x'", m.filterInput.Value())
	}

	// Esc backs out to field level
	m = update(t, m, keyMsg("esc"))
	if !m.filtering || m.filterLevel != filterLevelField {
		t.Fatalf("esc did not back out to field level: filtering=%v, level=%v", m.filtering, m.filterLevel)
	}
	if m.filterInput.Value() != "" {
		t.Fatalf("query was not cleared: %q", m.filterInput.Value())
	}

	// Esc again closes modal
	m = update(t, m, keyMsg("esc"))
	if m.filtering {
		t.Fatal("esc did not close modal")
	}
}

// Done-when: selecting "all <field>" clears what that field set — the slice
// for status, the slot for everything else.
func TestFilterClearViaAllOption(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: filterTestBoard()})

	// Set status filter to Backlog
	m = update(t, m, keyMsg("F"))
	m = update(t, m, keyMsg("down")) // status
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, keyMsg("b")) // Backlog
	m = update(t, m, keyMsg("enter"))

	if m.slice != "Backlog" {
		t.Fatalf("slice = %q, want Backlog", m.slice)
	}

	// Reopen with F (opens at status values with Backlog selected)
	m = update(t, m, keyMsg("F"))
	// Navigate up to "all status" (index 0)
	for m.filterSel > 0 {
		m = update(t, m, keyMsg("up"))
	}
	m = update(t, m, keyMsg("enter"))

	if m.slice != "" {
		t.Fatalf("the slice did not clear: %q", m.slice)
	}
	if len(m.shown) != 3 {
		t.Fatalf("shown = %d, want the 3 rows the all-slice shows", len(m.shown))
	}
}

// Done-when: a pass that removes all rows matching the active filter clears the filter.
func TestFilterPassReset(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: filterTestBoard()})

	// Filter by Priority Urgent
	m = update(t, m, keyMsg("F"))
	m = update(t, m, keyMsg("down"))
	m = update(t, m, keyMsg("down"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, keyMsg("down"))
	m = update(t, m, keyMsg("enter"))

	if m.filterField != filterFieldPriority || m.filterValue != "Urgent" {
		t.Fatalf("filter = %v %q", m.filterField, m.filterValue)
	}

	// A pass arrives with no Urgent tickets
	m = update(t, m, eventMsg{ev: loop.Event{Type: loop.EventAttention, Attention: []loop.AttentionItem{
		{Ticket: "LERP-2", TicketID: "id-2", Title: "Tagged releases", Status: "Backlog",
			Project: "Core", Relevance: loop.StatusBacklog, Priority: 2},
	}}})

	if m.filterField != filterFieldNone || m.filterValue != "" {
		t.Fatalf("pass did not reset filter: %v %q", m.filterField, m.filterValue)
	}
}

// Done-when: the status bar shows modal instructions during filter modal.
func TestFilterStatusBar(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = resize(t, m, 120, 30)
	m = pastTheSplash(t, m)
	m = update(t, m, keyMsg("1"))
	m = update(t, m, eventMsg{ev: filterTestBoard()})

	m = update(t, m, keyMsg("F"))
	bar := m.statusBar()
	if !strings.Contains(bar, "filter") || !strings.Contains(bar, "cancel") {
		t.Fatalf("status bar missing filter instructions:\n%s", bar)
	}
}
