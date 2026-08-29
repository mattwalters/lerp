package initui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/theme"
)

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case fetchStatusesMsg:
		m.fetchingStatuses = false
		m.fetchErr = msg.err
		m.existingStatuses = msg.states
		return m, nil

	case tea.KeyMsg:
		// When typing in team text input, backspace/esc/enter and runes are handled by updateTeam
		if m.CurrentScreen() == screenTeam && m.teamCreating {
			return m.updateTeam(msg)
		}

		if key.Matches(msg, m.keys.Quit) {
			return m.cancel()
		}

		switch m.CurrentScreen() {
		case screenTeam:
			return m.updateTeam(msg)
		case screenBoard:
			return m.updateBoard(msg)
		case screenShape:
			return m.updateShape(msg)
		case screenMapping:
			return m.updateMapping(msg)
		case screenRunner:
			return m.updateRunner(msg)
		case screenPermissions:
			return m.updatePermissions(msg)
		case screenMCP:
			return m.updateMCP(msg)
		case screenConfirm:
			return m.updateConfirm(msg)
		}
	}

	return m, nil
}

func (m Model) updateTeam(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.teamConfirmingMissing {
		switch {
		case key.Matches(msg, m.keys.Left, m.keys.Right, m.keys.Up, m.keys.Down, m.keys.Toggle):
			m.teamConfirmCreateChoice = !m.teamConfirmCreateChoice
		case key.Matches(msg, m.keys.Enter):
			if m.teamConfirmCreateChoice {
				m.teamKey = m.teamMissingKey
				m.teamName = m.opts.TeamName
				if m.teamName == "" {
					m.teamName = m.teamMissingKey
				}
				m.createTeam = true
				return m.advance()
			}
			return m.cancel()
		case key.Matches(msg, m.keys.Back):
			return m.cancel()
		}
		return m, nil
	}

	if m.teamCreating {
		switch msg.Type {
		case tea.KeyEnter:
			if m.teamCreateStep == 0 {
				k := strings.ToUpper(strings.TrimSpace(m.teamKeyInput))
				if k != "" {
					m.teamKeyInput = k
					m.teamCreateStep = 1
					m.teamNameInput = ""
				}
			} else {
				name := strings.TrimSpace(m.teamNameInput)
				if name == "" {
					name = m.opts.TeamName
				}
				if name == "" {
					name = m.teamKeyInput
				}
				m.teamKey = m.teamKeyInput
				m.teamName = name
				m.createTeam = true
				return m.advance()
			}
		case tea.KeyBackspace:
			if m.teamCreateStep == 0 {
				if len(m.teamKeyInput) > 0 {
					m.teamKeyInput = m.teamKeyInput[:len(m.teamKeyInput)-1]
				}
			} else {
				if len(m.teamNameInput) > 0 {
					m.teamNameInput = m.teamNameInput[:len(m.teamNameInput)-1]
				}
			}
		case tea.KeyEsc:
			if len(m.teamList) > 0 {
				m.teamCreating = false
				m.teamKeyInput = ""
				m.teamNameInput = ""
			} else {
				return m.cancel()
			}
		case tea.KeyRunes, tea.KeySpace:
			if m.teamCreateStep == 0 {
				m.teamKeyInput += strings.ToUpper(string(msg.Runes))
			} else {
				m.teamNameInput += string(msg.Runes)
			}
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Up):
		m.teamCursor = max(0, m.teamCursor-1)
	case key.Matches(msg, m.keys.Down):
		m.teamCursor = min(len(m.teamList)-1, m.teamCursor+1)
	case key.Matches(msg, m.keys.Enter):
		if m.teamCursor >= 0 && m.teamCursor < len(m.teamList) {
			item := m.teamList[m.teamCursor]
			if item.IsCreate {
				m.teamCreating = true
				m.teamCreateStep = 0
				m.teamKeyInput = ""
				m.teamNameInput = ""
			} else {
				m.teamKey = item.Key
				m.teamName = item.Name
				m.createTeam = false
				return m.advance()
			}
		}
	case key.Matches(msg, m.keys.Back):
		return m.cancel()
	}
	return m, nil
}

func (m Model) updateBoard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.fetchErr != nil {
		if key.Matches(msg, m.keys.Back) {
			if m.opts.AskTeam {
				return m.back()
			}
			return m.cancel()
		}
		if key.Matches(msg, m.keys.Enter) {
			m.fetchErr = nil
			m.fetchingStatuses = true
			return m, m.fetchStatusesCmd(m.teamKey)
		}
		return m, nil
	}

	if m.fetchingStatuses {
		if key.Matches(msg, m.keys.Back) && m.opts.AskTeam {
			return m.back()
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Enter):
		return m.advance()
	case key.Matches(msg, m.keys.Back):
		if m.opts.AskTeam {
			return m.back()
		}
	}
	return m, nil
}

func (m Model) updateShape(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.shapeCursor = max(0, m.shapeCursor-1)
	case key.Matches(msg, m.keys.Down):
		m.shapeCursor = min(1, m.shapeCursor+1)
	case key.Matches(msg, m.keys.Toggle):
		if m.shapeCursor == 0 {
			m.stock.Plan = !m.stock.Plan
		} else {
			m.stock.Review = !m.stock.Review
		}
	case key.Matches(msg, m.keys.Enter):
		return m.advance()
	case key.Matches(msg, m.keys.Back):
		return m.back()
	}
	return m, nil
}

func (m Model) updateMapping(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	slots := PipelineSlots(&m.stock)
	if len(slots) == 0 {
		return m.advance()
	}

	switch {
	case key.Matches(msg, m.keys.Up):
		m.mappingCursor = max(0, m.mappingCursor-1)
	case key.Matches(msg, m.keys.Down):
		m.mappingCursor = min(len(slots)-1, m.mappingCursor+1)
	case key.Matches(msg, m.keys.Left):
		i := m.mappingCursor
		if i >= 0 && i < len(m.mappingOptions) {
			opts := m.mappingOptions[i]
			if len(opts) > 0 {
				m.mappingSelected[i] = (m.mappingSelected[i] - 1 + len(opts)) % len(opts)
				m.applyMappingChoice(i)
			}
		}
	case key.Matches(msg, m.keys.Right, m.keys.Toggle):
		i := m.mappingCursor
		if i >= 0 && i < len(m.mappingOptions) {
			opts := m.mappingOptions[i]
			if len(opts) > 0 {
				m.mappingSelected[i] = (m.mappingSelected[i] + 1) % len(opts)
				m.applyMappingChoice(i)
			}
		}
	case key.Matches(msg, m.keys.Enter):
		return m.advance()
	case key.Matches(msg, m.keys.Back):
		return m.back()
	}
	return m, nil
}

func (m Model) updateRunner(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		return m.advance()
	case key.Matches(msg, m.keys.Back):
		return m.back()
	}
	return m, nil
}

func (m Model) updatePermissions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Toggle):
		m.stock.Bypass = !m.stock.Bypass
	case key.Matches(msg, m.keys.Enter):
		return m.advance()
	case key.Matches(msg, m.keys.Back):
		return m.back()
	}
	return m, nil
}

func (m Model) updateMCP(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.mcpChoice = max(0, m.mcpChoice-1)
	case key.Matches(msg, m.keys.Down):
		m.mcpChoice = min(2, m.mcpChoice+1)
	case key.Matches(msg, m.keys.Enter, m.keys.Toggle):
		switch m.mcpChoice {
		case 0:
			m.mcpIntent = MCPIntentNone
		case 1:
			m.mcpIntent = MCPIntentHTTP
		case 2:
			m.mcpIntent = MCPIntentBridge
		}
		if key.Matches(msg, m.keys.Enter) {
			return m.advance()
		}
	case key.Matches(msg, m.keys.Back):
		return m.back()
	}
	return m, nil
}

func (m Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		if m.previewErr == nil {
			m.done = true
			return m, tea.Quit
		}
	case key.Matches(msg, m.keys.Back):
		return m.back()
	}
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder

	// Header
	b.WriteString(theme.TitleFocus.Render("lerp init") + " " + theme.Faint.Render("·") + " " + theme.Ticket.Render(m.screenTitle()) + "\n\n")

	// Body
	switch m.CurrentScreen() {
	case screenTeam:
		b.WriteString(m.viewTeam())
	case screenBoard:
		b.WriteString(m.viewBoard())
	case screenShape:
		b.WriteString(m.viewShape())
	case screenMapping:
		b.WriteString(m.viewMapping())
	case screenRunner:
		b.WriteString(m.viewRunner())
	case screenPermissions:
		b.WriteString(m.viewPermissions())
	case screenMCP:
		b.WriteString(m.viewMCP())
	case screenConfirm:
		b.WriteString(m.viewConfirm())
	}

	// Footer help bar
	b.WriteString("\n" + m.screenHelp() + "\n")

	return b.String()
}

func (m Model) screenTitle() string {
	switch m.CurrentScreen() {
	case screenTeam:
		return "Team"
	case screenBoard:
		return "Board"
	case screenShape:
		return "Pipeline Shape"
	case screenMapping:
		return "Mapping"
	case screenRunner:
		return "Runner"
	case screenPermissions:
		return "Permissions"
	case screenMCP:
		return "Linear MCP Server"
	case screenConfirm:
		return "Confirm Planned Writes"
	default:
		return ""
	}
}

func (m Model) screenHelp() string {
	switch m.CurrentScreen() {
	case screenTeam:
		if m.teamConfirmingMissing {
			return renderHelp(m.keys.Left, m.keys.Right, m.keys.Enter, m.keys.Quit)
		}
		if m.teamCreating {
			return renderHelp(m.keys.Enter, m.keys.Back, m.keys.Quit)
		}
		return renderHelp(m.keys.Up, m.keys.Down, m.keys.Enter, m.keys.Quit)
	case screenBoard:
		if m.fetchErr != nil {
			return renderHelp(m.keys.Enter, m.keys.Back, m.keys.Quit)
		}
		return renderHelp(m.keys.Enter, m.keys.Back, m.keys.Quit)
	case screenShape:
		return renderHelp(m.keys.Up, m.keys.Down, m.keys.Toggle, m.keys.Enter, m.keys.Back, m.keys.Quit)
	case screenMapping:
		return renderHelp(m.keys.Up, m.keys.Down, m.keys.Left, m.keys.Right, m.keys.Enter, m.keys.Back, m.keys.Quit)
	case screenRunner:
		return renderHelp(m.keys.Enter, m.keys.Back, m.keys.Quit)
	case screenPermissions:
		return renderHelp(m.keys.Toggle, m.keys.Enter, m.keys.Back, m.keys.Quit)
	case screenMCP:
		return renderHelp(m.keys.Up, m.keys.Down, m.keys.Toggle, m.keys.Enter, m.keys.Back, m.keys.Quit)
	case screenConfirm:
		if m.previewErr != nil {
			return renderHelp(m.keys.Back, m.keys.Quit)
		}
		confirmBinding := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm & write"))
		return renderHelp(confirmBinding, m.keys.Back, m.keys.Quit)
	default:
		return renderHelp(m.keys.Quit)
	}
}

func (m Model) viewTeam() string {
	var b strings.Builder

	if m.teamConfirmingMissing {
		b.WriteString(fmt.Sprintf("Workspace has no team %q.\n\n", m.teamMissingKey))
		b.WriteString(fmt.Sprintf("Create team %s?\n\n", m.teamMissingKey))
		if m.teamConfirmCreateChoice {
			b.WriteString("  " + theme.Focus.Render("▸ [Yes]") + "   " + theme.Faint.Render("[No]") + "\n")
		} else {
			b.WriteString("  " + theme.Faint.Render("[Yes]") + "   " + theme.Focus.Render("▸ [No]") + "\n")
		}
		return b.String()
	}

	if m.teamCreating {
		b.WriteString("Create a new team:\n\n")
		def := m.opts.TeamName
		if def == "" {
			def = m.teamKeyInput
		}
		if m.teamCreateStep == 0 {
			b.WriteString("  " + theme.Ticket.Render("Team key: ") + m.teamKeyInput + theme.Focus.Render("█") + "\n")
		} else {
			b.WriteString("  " + theme.Ticket.Render("Team key:  ") + m.teamKeyInput + "\n")
			b.WriteString("  " + theme.Ticket.Render(fmt.Sprintf("Team name [%s]: ", def)) + m.teamNameInput + theme.Focus.Render("█") + "\n")
		}
		return b.String()
	}

	b.WriteString("Pick a team:\n\n")
	for i, item := range m.teamList {
		prefix := "    "
		if i == m.teamCursor {
			prefix = "  " + theme.Focus.Render("▸ ")
		}
		if item.IsCreate {
			if i == m.teamCursor {
				b.WriteString(prefix + theme.Focus.Render(fmt.Sprintf("%d) %s", i+1, item.Name)) + "\n")
			} else {
				b.WriteString(prefix + theme.Faint.Render(fmt.Sprintf("%d) %s", i+1, item.Name)) + "\n")
			}
		} else {
			row := fmt.Sprintf("%d) %-6s  %s", i+1, item.Key, item.Name)
			if i == m.teamCursor {
				b.WriteString(prefix + theme.Ticket.Render(row) + "\n")
			} else {
				b.WriteString(prefix + row + "\n")
			}
		}
	}

	return b.String()
}

func (m Model) viewBoard() string {
	var b strings.Builder

	if m.fetchingStatuses {
		b.WriteString(fmt.Sprintf("Reading workflow states for team %s...\n", m.teamKey))
		return b.String()
	}

	if m.fetchErr != nil {
		b.WriteString(theme.Err.Render(fmt.Sprintf("Could not read statuses of team %q: %v\n\n", m.teamKey, m.fetchErr)))
		b.WriteString("Press enter to retry, or esc to go back.\n")
		return b.String()
	}

	if len(m.existingStatuses) == 0 {
		b.WriteString(fmt.Sprintf("Team %s has no statuses yet.\n", m.teamKey))
		return b.String()
	}

	type categoryGroup struct {
		category string
		names    []string
	}
	var groups []categoryGroup
	categoryIndex := map[string]int{}
	for _, state := range m.existingStatuses {
		cat := state.Category
		if cat == "" {
			cat = "unknown"
		}
		idx, ok := categoryIndex[cat]
		if !ok {
			idx = len(groups)
			categoryIndex[cat] = idx
			groups = append(groups, categoryGroup{category: cat})
		}
		groups[idx].names = append(groups[idx].names, state.Name)
	}

	b.WriteString(fmt.Sprintf("Team %s has:\n\n", m.teamKey))
	for _, g := range groups {
		b.WriteString(fmt.Sprintf("  %-10s  %s\n", theme.Faint.Render(g.category), strings.Join(g.names, ", ")))
	}

	return b.String()
}

func (m Model) viewShape() string {
	var b strings.Builder

	b.WriteString("Choose which optional stages to include in the pipeline:\n\n")

	p0 := "    "
	if m.shapeCursor == 0 {
		p0 = "  " + theme.Focus.Render("▸ ")
	}
	chk0 := "[ ]"
	if m.stock.Plan {
		chk0 = theme.Focus.Render("[x]")
	}
	b.WriteString(p0 + chk0 + " " + theme.Ticket.Render("Planning stage") + theme.Faint.Render("    (plan in Planning -> approve in Plan Review)\n"))

	p1 := "    "
	if m.shapeCursor == 1 {
		p1 = "  " + theme.Focus.Render("▸ ")
	}
	chk1 := "[ ]"
	if m.stock.Review {
		chk1 = theme.Focus.Render("[x]")
	}
	b.WriteString(p1 + chk1 + " " + theme.Ticket.Render("Review pass") + theme.Faint.Render("       (review-and-fix loop inside Implementing)\n\n"))

	b.WriteString(theme.Faint.Render("Pipeline Diagram:\n"))
	b.WriteString(Diagram(m.stock))

	return b.String()
}

func (m Model) viewMapping() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Map the pipeline onto team %s's statuses:\n\n", m.teamKey))

	slots := PipelineSlots(&m.stock)
	for i, sl := range slots {
		prefix := "    "
		isSelected := (i == m.mappingCursor)
		if isSelected {
			prefix = "  " + theme.Focus.Render("▸ ")
		}

		optText := ""
		if i < len(m.mappingOptions) && len(m.mappingOptions[i]) > 0 {
			sel := m.mappingSelected[i]
			optText = m.mappingOptions[i][sel]
			// If it's an existing status, find its category
			for _, st := range m.existingStatuses {
				if st.Name == optText {
					optText = fmt.Sprintf("%s (%s)", st.Name, st.Category)
					break
				}
			}
		}

		label := fmt.Sprintf("%-28s", sl.Label+":")
		if isSelected {
			b.WriteString(prefix + theme.Ticket.Render(label) + "  " + theme.Focus.Render("◀  ") + theme.Ticket.Render(optText) + theme.Focus.Render("  ▶") + "\n")
		} else {
			b.WriteString(prefix + theme.Faint.Render(label) + "     " + optText + "\n")
		}
	}

	return b.String()
}

func (m Model) viewRunner() string {
	var b strings.Builder

	b.WriteString("The stock pipeline uses " + theme.Ticket.Render("Claude Code (claude)") + ".\n\n")
	b.WriteString("Unattended runs use Claude's print mode to execute steps and tool calls.\n\n")
	b.WriteString(theme.Faint.Render("Choosing between agent CLIs (Claude Code, Antigravity, Codex, ...) will\narrive in a future update (LERP-221).\n"))

	return b.String()
}

func (m Model) viewPermissions() string {
	var b strings.Builder

	b.WriteString("The stock Claude runner can include " + theme.Ticket.Render("--permission-mode bypassPermissions") + ",\n")
	b.WriteString("letting agents edit files and run commands unattended with your full user\n")
	b.WriteString("account. Declining writes a runner without the flag; unattended runs will\n")
	b.WriteString("fail at the first tool they are not allowed to use until you widen it in\n")
	b.WriteString(config.RepoConfigFile + ", in review, deliberately.\n\n")

	chk := "[ ]"
	if m.stock.Bypass {
		chk = theme.Focus.Render("[x]")
	}
	b.WriteString("  " + theme.Focus.Render("▸ ") + chk + " " + theme.Ticket.Render("Include --permission-mode bypassPermissions") + "\n")

	return b.String()
}

func (m Model) viewMCP() string {
	var b strings.Builder

	b.WriteString("Each runner CLI needs its own Linear MCP server to read tickets and post stage artifacts.\n")
	b.WriteString("Registration writes into each CLI's configuration; interactive OAuth authentication\n")
	b.WriteString("remains to be done once in each CLI afterward.\n\n")

	b.WriteString(theme.Faint.Render("Alternative single-auth bridge:\n"))
	b.WriteString(theme.Faint.Render("  Registering the stdio bridge (npx -y mcp-remote https://mcp.linear.app/mcp)\n"))
	b.WriteString(theme.Faint.Render("  shares one OAuth token across all CLIs via ~/.mcp-auth.\n"))
	b.WriteString(theme.Faint.Render("  Caveats: requires Node/npx in agent PATH and caches a single Linear account across all CLIs.\n\n"))

	options := []string{
		"Do not configure Linear MCP now",
		"Register Linear MCP (HTTP)",
		"Register Linear MCP (shared OAuth bridge)",
	}

	for i, opt := range []int{0, 1, 2} {
		prefix := "    "
		if i == m.mcpChoice {
			prefix = "  " + theme.Focus.Render("▸ ")
		}
		radio := "( )"
		if i == m.mcpChoice {
			radio = theme.Focus.Render("(•)")
		}
		row := radio + " " + options[opt]
		if i == m.mcpChoice {
			b.WriteString(prefix + theme.Ticket.Render(row) + "\n")
		} else {
			b.WriteString(prefix + row + "\n")
		}
	}

	return b.String()
}

func (m Model) viewConfirm() string {
	var b strings.Builder

	if m.previewErr != nil {
		b.WriteString(theme.Err.Render("Cannot write configuration:\n\n"))
		b.WriteString("  " + m.previewErr.Error() + "\n\n")
		b.WriteString(theme.Faint.Render("Press esc to go back and fix the mapping.\n"))
		return b.String()
	}

	b.WriteString("The following actions will be performed:\n\n")
	b.WriteString(m.previewText)

	return b.String()
}
