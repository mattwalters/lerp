package tui

import (
	"cmp"
	"fmt"
	"slices"

	textcursor "github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mattwalters/lerp/internal/loop"
)

// filterField represents the field being filtered on.
type filterField int

const (
	filterFieldNone filterField = iota
	filterFieldProject
	filterFieldStatus
	filterFieldPriority
	filterFields
)

func (f filterField) String() string {
	switch f {
	case filterFieldProject:
		return "project"
	case filterFieldStatus:
		return "status"
	case filterFieldPriority:
		return "priority"
	default:
		return ""
	}
}

var filterFieldsList = []filterField{
	filterFieldProject,
	filterFieldStatus,
	filterFieldPriority,
}

// filterLevel is the level of navigation in the filter modal.
type filterLevel int

const (
	filterLevelField filterLevel = iota
	filterLevelValue
)

// filterItem is one row in the filter modal's value list.
type filterItem struct {
	label string
	value string
	count int
	isAll bool
}

// newFilterInput builds the type-ahead prompt for narrowing filter values.
func newFilterInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "type to narrow"
	ti.PromptStyle = styleFocus
	ti.PlaceholderStyle = styleFaint
	ti.Cursor.SetMode(textcursor.CursorStatic)
	return ti
}

// activeValue is what the given field is currently narrowed to, and whether
// it is narrowed at all. Status answers from the slice and everything else
// from the slot, which is the same split applyFilter writes through.
func (m *model) activeValue(field filterField) (string, bool) {
	if field == filterFieldStatus {
		return m.slice, m.slice != ""
	}
	if m.filterField != field {
		return "", false
	}
	return m.filterValue, true
}

// descend opens the value list for one field, putting the cursor on the
// value that field is already narrowed to so reopening a filter lands on it
// rather than on the all-row above it.
func (m *model) descend(field filterField) {
	m.filterLevel = filterLevelValue
	m.filterFieldCur = field
	m.filterInput.SetValue("")
	m.filterInput.Focus()
	m.filterSel = 0
	value, active := m.activeValue(field)
	if !active {
		return
	}
	for i, it := range m.filterValues(field) {
		if !it.isAll && it.value == value {
			m.filterSel = i
			break
		}
	}
}

// openFilter opens the filter modal. If something is already narrowing the
// inbox — the slot, or the slice the status row writes — it opens directly
// at that field's value list with the active value selected. Otherwise it
// opens at the field level.
func (m *model) openFilter() {
	m.dropVisual()
	m.filtering = true
	switch {
	case m.filterField != filterFieldNone:
		m.descend(m.filterField)
	case m.slice != "":
		m.descend(filterFieldStatus)
	default:
		m.filterLevel = filterLevelField
		m.filterFieldCur = filterFieldNone
		m.filterSel = 0
		m.filterInput.SetValue("")
		m.filterInput.Blur()
	}
}

// openProjectFilter opens the filter modal directly at the project value list.
func (m *model) openProjectFilter() {
	m.dropVisual()
	m.filtering = true
	m.descend(filterFieldProject)
}

// handleFilterKey drives the filter modal, swallowing keys while it is open.
func (m model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	}
	if m.filterLevel == filterLevelField {
		return m.handleFilterFieldKey(msg)
	}
	return m.handleFilterValueKey(msg)
}

func (m model) handleFilterFieldKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyCtrlC:
		return m, tea.Quit
	case msg.Type == tea.KeyEsc || (msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'q'):
		m.filtering = false
	case msg.Type == tea.KeyUp || (msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'k'):
		if m.filterSel > 0 {
			m.filterSel--
		}
	case msg.Type == tea.KeyDown || (msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'j'):
		if m.filterSel < len(filterFieldsList)-1 {
			m.filterSel++
		}
	case msg.Type == tea.KeyEnter:
		m.descend(filterFieldsList[m.filterSel])
	}
	m.layout()
	return m, nil
}

func (m model) handleFilterValueKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.filterLevel = filterLevelField
		m.filterInput.Blur()
		m.filterInput.SetValue("")
		m.filterSel = 0
		for i, f := range filterFieldsList {
			if f == m.filterFieldCur {
				m.filterSel = i
				break
			}
		}
		m.layout()
		return m, nil
	case tea.KeyEnter:
		items := m.matchingFilterValues()
		if len(items) > 0 && m.filterSel >= 0 && m.filterSel < len(items) {
			m.applyFilter(m.filterFieldCur, items[m.filterSel])
			m.dropVisual()
			m.resort()
		}
		m.filtering = false
		m.filterInput.Blur()
		m.refreshMain()
		m.layout()
		return m, m.wantDetail()
	case tea.KeyUp:
		if m.filterSel > 0 {
			m.filterSel--
		}
		m.layout()
		return m, nil
	case tea.KeyDown:
		items := m.matchingFilterValues()
		if m.filterSel < len(items)-1 {
			m.filterSel++
		}
		m.layout()
		return m, nil
	default:
		var cmd tea.Cmd
		m.filterInput, cmd = m.filterInput.Update(msg)
		items := m.matchingFilterValues()
		if len(items) == 0 {
			m.filterSel = 0
		} else if m.filterSel >= len(items) {
			m.filterSel = len(items) - 1
		}
		m.layout()
		return m, cmd
	}
}

// filterValues builds {label, value, count} for one field's value list.
//
// Project and priority narrow the slot, so their values and counts come off
// m.sliced() — the rows the active slice already lets through, which is
// exactly what applying one of them would leave. Status is the slice's own
// axis: picking a value there replaces the slice rather than narrowing under
// it, so its values are built over the whole pass instead (see
// statusFilterValues). Either way, a row's count is what selecting it shows.
func (m *model) filterValues(field filterField) []filterItem {
	if field == filterFieldStatus {
		return m.statusFilterValues()
	}
	items := m.sliced()
	totalCount := len(items)

	switch field {
	case filterFieldProject:
		res := []filterItem{
			{label: "all project", value: "", isAll: true, count: totalCount},
		}
		counts := make(map[string]int)
		for _, it := range items {
			counts[it.Project]++
		}
		var names []string
		for name := range counts {
			names = append(names, name)
		}
		slices.SortFunc(names, compareProject)
		for _, name := range names {
			label := name
			if name == "" {
				label = "no project"
			}
			res = append(res, filterItem{
				label: label,
				value: name,
				count: counts[name],
			})
		}
		return res

	case filterFieldPriority:
		res := []filterItem{
			{label: "all priority", value: "", isAll: true, count: totalCount},
		}
		pCounts := make(map[int]int)
		for _, it := range items {
			pCounts[it.Priority]++
		}
		var priorities []int
		for p := range pCounts {
			priorities = append(priorities, p)
		}
		slices.SortFunc(priorities, func(a, b int) int {
			return cmp.Compare(priorityRank(a), priorityRank(b))
		})
		for _, p := range priorities {
			label := priorityLabel(p)
			val := label
			if p == 0 || p > 4 {
				label = "no priority"
				val = ""
			}
			res = append(res, filterItem{
				label: label,
				value: val,
				count: pCounts[p],
			})
		}
		return res

	default:
		return nil
	}
}

// matchingFilterValues returns the values for m.filterFieldCur filtered by type-ahead query.
func (m *model) matchingFilterValues() []filterItem {
	all := m.filterValues(m.filterFieldCur)
	q := clean(m.filterInput.Value())
	if q == "" {
		return all
	}
	var out []filterItem
	for _, it := range all {
		if foldIndex(fold(it.label), fold(q)) >= 0 {
			out = append(out, it)
		}
	}
	return out
}

// statusFilterValues is the status row's value list: every status the pass
// carries, in Linear board order, plus the all-statuses row that is the
// slice's own "" stop. Selecting one sets the slice (see applyFilter), so
// the list is built over m.attention rather than m.sliced() — a list built
// under the active slice would offer only the status already showing, and
// picking it would be the one stop that changes nothing.
//
// The counts follow the same rule: a named status counts every row carrying
// it, and the all-statuses row counts what the all-slice shows, which is the
// unfolded rows rather than the whole pass.
func (m *model) statusFilterValues() []filterItem {
	res := []filterItem{{
		label: "all status",
		value: "",
		isAll: true,
		count: len(filterAttention(m.attention, filterFieldNone, "", "", "")),
	}}
	counts := make(map[string]int)
	for _, it := range m.attention {
		if it.Status != "" {
			counts[it.Status]++
		}
	}
	statuses := make([]string, 0, len(counts))
	for st := range counts {
		statuses = append(statuses, st)
	}
	slices.SortFunc(statuses, func(a, b string) int {
		return compareStatusPosition(a, b, m.statusIndex)
	})
	for _, st := range statuses {
		res = append(res, filterItem{label: st, value: st, count: counts[st]})
	}
	return res
}

// applyFilter commits one row of a value list. Status lands on the slice and
// every other field on the slot, so the modal writes to exactly one of the
// two controls and they can never disagree about a status.
func (m *model) applyFilter(field filterField, item filterItem) {
	if field == filterFieldStatus {
		m.slice = item.value
		return
	}
	if item.isAll {
		m.filterField = filterFieldNone
		m.filterValue = ""
		return
	}
	m.filterField = field
	m.filterValue = item.value
}

// matchesFilter reports whether an item matches the active filter slot.
// Status is never in the slot — the modal routes it to the slice — but the
// switch stays total so a slot that somehow held one would still narrow to
// it rather than silently match every row.
func matchesFilter(it loop.AttentionItem, field filterField, value string) bool {
	switch field {
	case filterFieldProject:
		return it.Project == value
	case filterFieldStatus:
		return it.Status == value
	case filterFieldPriority:
		if value == "" {
			return it.Priority == 0 || it.Priority > 4
		}
		return priorityLabel(it.Priority) == value
	default:
		return true
	}
}

// matchesFilters checks both the single filter slot and search query.
func matchesFilters(it loop.AttentionItem, field filterField, value, query string) bool {
	return matchesFilter(it, field, value) && matchesSearch(it, query)
}

// filterDisplayValue returns the human-readable string for m.filterValue.
func (m model) filterDisplayValue() string {
	if m.filterValue == "" {
		switch m.filterField {
		case filterFieldProject, filterFieldPriority:
			return "none"
		default:
			return ""
		}
	}
	return m.filterValue
}

// priorityLabel returns the string representation of a priority number.
func priorityLabel(p int) string {
	switch p {
	case 1:
		return widestPriority
	case 2:
		return "High"
	case 3:
		return "Medium"
	case 4:
		return "Low"
	default:
		return "—"
	}
}

// filterContentSize computes the inner dimensions for the filter modal.
func (m model) filterContentSize() (w, h int) {
	if m.filterLevel == filterLevelField {
		w = lipgloss.Width("filter") + 2
		for _, f := range filterFieldsList {
			w = max(w, lipgloss.Width(f.String())+2)
		}
		h = len(filterFieldsList)
		return w, h
	}

	title := "filter " + m.filterFieldCur.String()
	w = lipgloss.Width(title) + 2
	w = max(w, lipgloss.Width(m.filterInput.Placeholder)+4)
	w = max(w, lipgloss.Width(m.filterInput.Value())+4)

	allItems := m.filterValues(m.filterFieldCur)
	labelW, countW := 0, 0
	for _, it := range allItems {
		labelW = max(labelW, lipgloss.Width(it.label))
		countW = max(countW, lipgloss.Width(fmt.Sprintf("%d", it.count)))
	}
	if len(allItems) > 0 {
		w = max(w, 2+labelW+2+countW)
	} else {
		w = max(w, lipgloss.Width("  no matches"))
	}
	items := m.matchingFilterValues()
	h = len(items) + 2
	return w, h
}

// filterPicker renders the filter modal at its current level.
func (m model) filterPicker(w, h int) string {
	if m.filterLevel == filterLevelField {
		title := "filter"
		var rows []string
		for i, f := range filterFieldsList {
			if i == m.filterSel {
				rows = append(rows, styleFocus.Render("▸ "+f.String()))
			} else {
				rows = append(rows, "  "+f.String())
			}
		}
		rows = windowRows(rows, cursor{at: m.filterSel, span: 1}, h-2)
		return panelBox(styleTitleFocus.Render(title), true, w, h, rows, padMain, nil)
	}

	title := "filter " + m.filterFieldCur.String()
	allItems := m.filterValues(m.filterFieldCur)
	labelW, countW := 0, 0
	for _, it := range allItems {
		labelW = max(labelW, lipgloss.Width(it.label))
		countW = max(countW, lipgloss.Width(fmt.Sprintf("%d", it.count)))
	}

	m.filterInput.Width = max(1, w-4-lipgloss.Width(m.filterInput.Prompt))

	rows := []string{m.filterInput.View(), ""}
	items := m.matchingFilterValues()
	if len(items) == 0 {
		rows = append(rows, styleFaint.Render("  no matches"))
	} else {
		for i, it := range items {
			countStr := padTo(fmt.Sprintf("%d", it.count), countW)
			if i == m.filterSel {
				rows = append(rows, styleFocus.Render("▸ "+padTo(it.label, labelW)+"  "+countStr))
			} else {
				rows = append(rows, "  "+padTo(it.label, labelW)+"  "+styleFaint.Render(countStr))
			}
		}
	}
	rows = windowRows(rows, cursor{at: 2 + m.filterSel, span: 1}, h-2)
	return panelBox(styleTitleFocus.Render(title), true, w, h, rows, padMain, nil)
}
