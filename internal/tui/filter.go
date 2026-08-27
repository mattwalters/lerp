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

// openFilter opens the filter modal. If a filter is already active, it opens
// directly at the value level for that field with the active value selected.
// Otherwise it opens at the field level.
func (m *model) openFilter() {
	m.dropVisual()
	m.filtering = true
	if m.filterField != filterFieldNone {
		m.filterLevel = filterLevelValue
		m.filterFieldCur = m.filterField
		m.filterInput.SetValue("")
		m.filterInput.Focus()
		values := m.filterValues(m.filterField)
		m.filterSel = 0
		for i, it := range values {
			if !it.isAll && it.value == m.filterValue {
				m.filterSel = i
				break
			}
		}
	} else {
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
	m.filterLevel = filterLevelValue
	m.filterFieldCur = filterFieldProject
	m.filterInput.SetValue("")
	m.filterInput.Focus()
	values := m.filterValues(filterFieldProject)
	m.filterSel = 0
	if m.filterField == filterFieldProject {
		for i, it := range values {
			if !it.isAll && it.value == m.filterValue {
				m.filterSel = i
				break
			}
		}
	}
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
	case msg.Type == tea.KeyEsc:
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
		field := filterFieldsList[m.filterSel]
		m.filterLevel = filterLevelValue
		m.filterFieldCur = field
		m.filterInput.SetValue("")
		m.filterInput.Focus()
		values := m.filterValues(field)
		m.filterSel = 0
		if m.filterField == field {
			for i, it := range values {
				if !it.isAll && it.value == m.filterValue {
					m.filterSel = i
					break
				}
			}
		}
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
			item := items[m.filterSel]
			if item.isAll {
				m.filterField = filterFieldNone
				m.filterValue = ""
			} else {
				m.filterField = m.filterFieldCur
				m.filterValue = item.value
			}
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

// filterValues builds {label, value, count} from m.unfolded().
func (m *model) filterValues(field filterField) []filterItem {
	items := m.unfolded()
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

	case filterFieldStatus:
		res := []filterItem{
			{label: "all status", value: "", isAll: true, count: totalCount},
		}
		counts := make(map[string]int)
		for _, it := range items {
			counts[it.Status]++
		}
		var statuses []string
		for s := range counts {
			statuses = append(statuses, s)
		}
		slices.SortFunc(statuses, func(a, b string) int {
			return compareStatusPosition(a, b, m.statusIndex)
		})
		for _, s := range statuses {
			res = append(res, filterItem{
				label: s,
				value: s,
				count: counts[s],
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

// matchesFilter reports whether an item matches the active filter slot.
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
