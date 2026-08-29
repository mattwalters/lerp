package initui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/theme"
	"github.com/mattwalters/lerp/internal/vendors"
)

// ErrCanceled is returned when the operator backs out of the wizard.
var ErrCanceled = errors.New("init canceled")

// MCPIntent describes whether and how to register the Linear MCP server.
type MCPIntent int

const (
	MCPIntentNone MCPIntent = iota
	MCPIntentHTTP
	MCPIntentBridge
)

// Choices represents the decisions made across the wizard screens.
type Choices struct {
	TeamKey    string
	TeamName   string
	CreateTeam bool
	Stock      config.Stock
	MCPIntent  MCPIntent
}

// Result is the outcome returned by Run.
type Result struct {
	TeamKey    string
	TeamName   string
	CreateTeam bool
	Stock      config.Stock
	MCPIntent  MCPIntent
}

// Options configures the init wizard.
type Options struct {
	WorkspaceTeams  []linear.TeamRef
	TeamKey         string
	TeamName        string
	AskTeam         bool
	AllowCreateTeam bool
	Fresh           bool
	ExistingConfig  *config.RepoConfig
	MCPConfigured   map[string]bool
	FetchStatuses   func(ctx context.Context, teamKey string) ([]linear.WorkflowState, error)
	Preview         func(choices Choices) (string, error)
}

type screen int

const (
	screenTeam screen = iota
	screenBoard
	screenShape
	screenMapping
	screenRunner
	screenPermissions
	screenMCP
	screenConfirm
)

type teamItem struct {
	Key      string
	Name     string
	IsCreate bool
}

type fetchStatusesMsg struct {
	states []linear.WorkflowState
	err    error
}

// Model is the Bubble Tea model for the init wizard.
type Model struct {
	ctx  context.Context
	opts Options
	keys keymap

	screens     []screen
	screenIndex int

	teamKey    string
	teamName   string
	createTeam bool
	stock      config.Stock
	mcpIntent  MCPIntent

	existingStatuses []linear.WorkflowState
	fetchingStatuses bool
	fetchErr         error

	previewText string
	previewErr  error

	done     bool
	canceled bool

	// Screen-specific state
	// Team
	teamList                []teamItem
	teamCursor              int
	teamCreating            bool
	teamCreateStep          int // 0: key, 1: name
	teamKeyInput            string
	teamNameInput           string
	teamConfirmingMissing   bool
	teamMissingKey          string
	teamConfirmCreateChoice bool

	// Shape
	shapeCursor int

	// Mapping
	mappingCursor   int
	mappingOptions  [][]string
	mappingSelected []int

	// MCP
	mcpChoice int // 0: No, 1: HTTP, 2: Bridge
}

// New constructs an initialized init wizard Model.
func New(ctx context.Context, opts Options) Model {
	km := newKeymap()

	m := Model{
		ctx:      ctx,
		opts:     opts,
		keys:     km,
		teamKey:  opts.TeamKey,
		teamName: opts.TeamName,
		stock: config.Stock{
			Teams:  []string{opts.TeamKey},
			Plan:   true,
			Review: true,
			Bypass: false,
		},
		mcpChoice: 0, // default No
	}

	m.buildScreens()
	m.initTeamState()

	return m
}

func (m *Model) hasUnconfiguredMCP() bool {
	if m.opts.MCPConfigured == nil {
		return false
	}
	var vendorsToCheck []string
	if m.opts.Fresh {
		vendorsToCheck = []string{"claude"}
	} else if m.opts.ExistingConfig != nil {
		for _, r := range m.opts.ExistingConfig.Runners {
			if r.Vendor != "" && !slices.Contains(vendorsToCheck, r.Vendor) {
				vendorsToCheck = append(vendorsToCheck, r.Vendor)
			}
		}
	}
	for _, v := range vendorsToCheck {
		if _, ok := vendors.Lookup(v); ok {
			if !m.opts.MCPConfigured[v] {
				return true
			}
		}
	}
	return false
}

func (m *Model) buildScreens() {
	var screens []screen
	if m.opts.AskTeam {
		screens = append(screens, screenTeam)
	}
	screens = append(screens, screenBoard)
	if m.opts.Fresh {
		screens = append(screens, screenShape, screenMapping, screenRunner, screenPermissions)
	}
	if m.hasUnconfiguredMCP() {
		screens = append(screens, screenMCP)
	}
	screens = append(screens, screenConfirm)
	m.screens = screens
	m.screenIndex = 0
}

func (m *Model) initTeamState() {
	if !m.opts.AskTeam {
		return
	}

	normSeedKey := strings.ToUpper(strings.TrimSpace(m.opts.TeamKey))
	if normSeedKey != "" && m.opts.ExistingConfig == nil {
		// Fresh init with team seed that wasn't found in workspace
		exists := false
		for _, t := range m.opts.WorkspaceTeams {
			if t.Key == normSeedKey {
				exists = true
				break
			}
		}
		if !exists {
			m.teamConfirmingMissing = true
			m.teamMissingKey = normSeedKey
			m.teamConfirmCreateChoice = true
			return
		}
	}

	if len(m.opts.WorkspaceTeams) == 0 && m.opts.ExistingConfig == nil {
		m.teamCreating = true
		m.teamCreateStep = 0
		m.teamKeyInput = ""
		m.teamNameInput = m.opts.TeamName
		return
	}

	var list []teamItem
	for _, t := range m.opts.WorkspaceTeams {
		list = append(list, teamItem{Key: t.Key, Name: t.Name})
	}
	if m.opts.AllowCreateTeam {
		list = append(list, teamItem{Name: "create a new team", IsCreate: true})
	}
	m.teamList = list
	m.teamCursor = 0
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	if !m.opts.AskTeam && m.teamKey != "" && !m.createTeam {
		return m.fetchStatusesCmd(m.teamKey)
	}
	return nil
}

func (m Model) fetchStatusesCmd(key string) tea.Cmd {
	if m.opts.FetchStatuses == nil || key == "" {
		return nil
	}
	return func() tea.Msg {
		states, err := m.opts.FetchStatuses(m.ctx, key)
		return fetchStatusesMsg{states: states, err: err}
	}
}

// CurrentScreen returns the active screen.
func (m Model) CurrentScreen() screen {
	if m.screenIndex >= 0 && m.screenIndex < len(m.screens) {
		return m.screens[m.screenIndex]
	}
	return screenConfirm
}

// Choices returns the current choices collected by the model.
func (m Model) Choices() Choices {
	return Choices{
		TeamKey:    m.teamKey,
		TeamName:   m.teamName,
		CreateTeam: m.createTeam,
		Stock:      m.stock,
		MCPIntent:  m.mcpIntent,
	}
}

// Result returns the wizard's final result.
func (m Model) Result() Result {
	return Result{
		TeamKey:    m.teamKey,
		TeamName:   m.teamName,
		CreateTeam: m.createTeam,
		Stock:      m.stock,
		MCPIntent:  m.mcpIntent,
	}
}

// IsDone reports whether the wizard finished with confirmation.
func (m Model) IsDone() bool { return m.done }

// IsCanceled reports whether the wizard was canceled.
func (m Model) IsCanceled() bool { return m.canceled }

func (m Model) advance() (Model, tea.Cmd) {
	cur := m.CurrentScreen()

	if cur == screenTeam {
		if !m.createTeam && m.teamKey != "" {
			m.fetchingStatuses = true
			m.fetchErr = nil
			m.existingStatuses = nil
		}
	}

	if cur == screenShape {
		m.initMappingOptions()
	}

	if m.screenIndex < len(m.screens)-1 {
		m.screenIndex++
		next := m.CurrentScreen()
		if next == screenBoard && m.fetchingStatuses {
			return m, m.fetchStatusesCmd(m.teamKey)
		}
		if next == screenConfirm {
			m.updatePreview()
		}
	} else if cur == screenConfirm && m.previewErr == nil {
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) back() (Model, tea.Cmd) {
	if m.screenIndex > 0 {
		m.screenIndex--
		return m, nil
	}
	return m, nil
}

func (m Model) cancel() (tea.Model, tea.Cmd) {
	m.canceled = true
	return m, tea.Quit
}

func (m *Model) updatePreview() {
	if m.opts.Preview == nil {
		return
	}
	m.stock.Teams = []string{m.teamKey}
	text, err := m.opts.Preview(m.Choices())
	m.previewText = text
	m.previewErr = err
}

func (m *Model) initMappingOptions() {
	slots := PipelineSlots(&m.stock)
	m.mappingOptions = make([][]string, len(slots))
	m.mappingSelected = make([]int, len(slots))
	defaults := defaultMapping(slots, m.existingStatuses)

	for i, sl := range slots {
		stockName := sl.Stock
		hasStock := false
		stockIdx := -1
		for idx, s := range m.existingStatuses {
			if s.Name == stockName {
				hasStock = true
				stockIdx = idx
				break
			}
		}

		var opts []string
		if !hasStock {
			opts = append(opts, fmt.Sprintf("create %q", stockName))
			for _, s := range m.existingStatuses {
				opts = append(opts, s.Name)
			}
		} else {
			for _, s := range m.existingStatuses {
				opts = append(opts, s.Name)
			}
		}
		m.mappingOptions[i] = opts

		def := defaults[i]
		if def != "" {
			for idx, opt := range opts {
				if opt == def {
					m.mappingSelected[i] = idx
					break
				}
			}
		} else {
			if !hasStock {
				m.mappingSelected[i] = 0
			} else {
				m.mappingSelected[i] = stockIdx
			}
		}

		m.applyMappingChoice(i)
	}
	m.mappingCursor = 0
}

func (m *Model) applyMappingChoice(slotIdx int) {
	slots := PipelineSlots(&m.stock)
	if slotIdx < 0 || slotIdx >= len(slots) || slotIdx >= len(m.mappingOptions) {
		return
	}
	opts := m.mappingOptions[slotIdx]
	sel := m.mappingSelected[slotIdx]
	if sel < 0 || sel >= len(opts) {
		return
	}
	opt := opts[sel]
	sl := slots[slotIdx]
	if strings.HasPrefix(opt, "create ") {
		sl.Set(&m.stock, "")
	} else {
		sl.Set(&m.stock, opt)
	}
}

func (m Model) effectiveMapping() []string {
	slots := PipelineSlots(&m.stock)
	effective := make([]string, len(slots))
	for i, sl := range slots {
		if i < len(m.mappingOptions) && i < len(m.mappingSelected) {
			sel := m.mappingSelected[i]
			opts := m.mappingOptions[i]
			if sel >= 0 && sel < len(opts) {
				opt := opts[sel]
				if strings.HasPrefix(opt, "create ") {
					effective[i] = sl.Stock
				} else {
					effective[i] = opt
				}
				continue
			}
		}
		effective[i] = sl.Stock
	}
	return effective
}

func (m Model) mappingConflicts() map[int]string {
	effective := m.effectiveMapping()
	counts := make(map[string]int, len(effective))
	for _, name := range effective {
		if name != "" {
			counts[name]++
		}
	}
	conflicts := make(map[int]string)
	for i, name := range effective {
		if counts[name] > 1 {
			conflicts[i] = name
		}
	}
	return conflicts
}

// Run executes the wizard Bubble Tea program.
func Run(ctx context.Context, opts Options) (Result, error) {
	if err := theme.UseBackground(); err != nil {
		return Result{}, err
	}
	m := New(ctx, opts)
	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return Result{}, err
	}
	resModel, ok := finalModel.(Model)
	if !ok || resModel.canceled {
		return Result{}, ErrCanceled
	}
	if !resModel.done {
		return Result{}, ErrCanceled
	}
	return resModel.Result(), nil
}
