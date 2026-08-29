package initui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

var testLinearDefaults = []linear.WorkflowState{
	{Name: "Backlog", Category: "backlog"},
	{Name: "Todo", Category: "unstarted"},
	{Name: "In Progress", Category: "started"},
	{Name: "Done", Category: "completed"},
	{Name: "Canceled", Category: "canceled"},
}

func keyPress(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

func defaultOpts() Options {
	return Options{
		WorkspaceTeams: []linear.TeamRef{
			{Key: "LERP", Name: "Lerp"},
			{Key: "ENG", Name: "Engineering"},
		},
		TeamKey:         "LERP",
		TeamName:        "Lerp",
		AskTeam:         false,
		AllowCreateTeam: true,
		Fresh:           true,
		MCPConfigured:   map[string]bool{"claude": true},
		FetchStatuses: func(ctx context.Context, teamKey string) ([]linear.WorkflowState, error) {
			return testLinearDefaults, nil
		},
		Preview: func(choices Choices) (string, error) {
			return "planned writes for " + choices.TeamKey, nil
		},
	}
}

func TestTeamScreenListSelection(t *testing.T) {
	opts := defaultOpts()
	opts.AskTeam = true
	opts.TeamKey = ""

	m := New(context.Background(), opts)
	if m.CurrentScreen() != screenTeam {
		t.Fatalf("expected screenTeam, got %v", m.CurrentScreen())
	}

	view := m.View()
	if !strings.Contains(view, "1) LERP") || !strings.Contains(view, "2) ENG") || !strings.Contains(view, "3) create a new team") {
		t.Fatalf("unexpected team view:\n%s", view)
	}

	// Move down to ENG and press enter
	m2, _ := m.Update(keyPress("down"))
	m = m2.(Model)
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)

	if m.teamKey != "ENG" || m.teamName != "Engineering" {
		t.Errorf("expected team ENG (Engineering), got %q (%q)", m.teamKey, m.teamName)
	}
	if m.createTeam {
		t.Error("createTeam = true, want false")
	}
	if m.CurrentScreen() != screenBoard {
		t.Errorf("expected screenBoard, got %v", m.CurrentScreen())
	}
}

func TestTeamScreenCreateRow(t *testing.T) {
	opts := defaultOpts()
	opts.AskTeam = true
	opts.TeamKey = ""

	m := New(context.Background(), opts)
	// Select create row (item 3)
	m2, _ := m.Update(keyPress("down"))
	m = m2.(Model)
	m2, _ = m.Update(keyPress("down"))
	m = m2.(Model)
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)

	if !m.teamCreating || m.teamCreateStep != 0 {
		t.Fatalf("expected teamCreating at step 0")
	}

	// Type team key "ACME"
	for _, r := range "acme" {
		m2, _ = m.Update(keyPress(string(r)))
		m = m2.(Model)
	}
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)

	if m.teamCreateStep != 1 || m.teamKeyInput != "ACME" {
		t.Fatalf("expected step 1 with key ACME, got %d, %q", m.teamCreateStep, m.teamKeyInput)
	}

	// Type team name "Acme Marketing"
	for _, r := range "Acme Marketing" {
		m2, _ = m.Update(keyPress(string(r)))
		m = m2.(Model)
	}
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)

	if m.teamKey != "ACME" || m.teamName != "Acme Marketing" || !m.createTeam {
		t.Errorf("expected ACME (Acme Marketing) create=true, got %q (%q) create=%v", m.teamKey, m.teamName, m.createTeam)
	}
	if m.CurrentScreen() != screenBoard {
		t.Errorf("expected screenBoard, got %v", m.CurrentScreen())
	}
}

func TestTeamScreenConfirmMissingKey(t *testing.T) {
	opts := defaultOpts()
	opts.AskTeam = true
	opts.TeamKey = "MISSING"

	m := New(context.Background(), opts)
	if !m.teamConfirmingMissing {
		t.Fatal("expected teamConfirmingMissing = true")
	}
	if !strings.Contains(m.View(), "Workspace has no team \"MISSING\"") {
		t.Fatalf("unexpected view:\n%s", m.View())
	}

	// Confirm Yes
	m2, _ := m.Update(keyPress("enter"))
	m = m2.(Model)

	if m.teamKey != "MISSING" || !m.createTeam {
		t.Errorf("expected MISSING create=true, got %q create=%v", m.teamKey, m.createTeam)
	}
	if m.CurrentScreen() != screenBoard {
		t.Errorf("expected screenBoard, got %v", m.CurrentScreen())
	}
}

func TestTeamScreenDeclineMissingKey(t *testing.T) {
	opts := defaultOpts()
	opts.AskTeam = true
	opts.TeamKey = "MISSING"

	m := New(context.Background(), opts)
	// Toggle to No
	m2, _ := m.Update(keyPress("right"))
	m = m2.(Model)
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)

	if !m.canceled {
		t.Error("expected canceled = true when declining missing team")
	}
}

func TestBoardScreenGroupingAndLoading(t *testing.T) {
	opts := defaultOpts()
	m := New(context.Background(), opts)
	if m.CurrentScreen() != screenBoard {
		t.Fatalf("expected screenBoard, got %v", m.CurrentScreen())
	}

	// Send loading state
	m.fetchingStatuses = true
	if !strings.Contains(m.View(), "Reading workflow states for team LERP") {
		t.Fatalf("expected loading view, got:\n%s", m.View())
	}

	// Send success
	m2, _ := m.Update(fetchStatusesMsg{states: testLinearDefaults})
	m = m2.(Model)

	view := m.View()
	for _, expected := range []string{"backlog", "Backlog", "unstarted", "Todo", "started", "In Progress", "completed", "Done", "canceled", "Canceled"} {
		if !strings.Contains(view, expected) {
			t.Errorf("board view missing %q:\n%s", expected, view)
		}
	}

	// Enter advances to Shape
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)
	if m.CurrentScreen() != screenShape {
		t.Errorf("expected screenShape, got %v", m.CurrentScreen())
	}
}

func TestBoardScreenFetchError(t *testing.T) {
	opts := defaultOpts()
	opts.AskTeam = true
	m := New(context.Background(), opts)
	m.screenIndex = 1 // Board screen

	// Receive error
	m2, _ := m.Update(fetchStatusesMsg{err: errors.New("network error")})
	m = m2.(Model)

	if !strings.Contains(m.View(), "network error") {
		t.Fatalf("expected error in view, got:\n%s", m.View())
	}

	// Esc goes back to team screen
	m2, _ = m.Update(keyPress("esc"))
	m = m2.(Model)
	if m.CurrentScreen() != screenTeam {
		t.Errorf("expected screenTeam after esc on board error, got %v", m.CurrentScreen())
	}
}

func TestShapeScreenTogglesAndDiagram(t *testing.T) {
	opts := defaultOpts()
	m := New(context.Background(), opts)
	m.existingStatuses = testLinearDefaults
	m.screenIndex = 1 // Shape screen (when askTeam=false, shape is index 1)

	if m.CurrentScreen() != screenShape {
		t.Fatalf("expected screenShape, got %v", m.CurrentScreen())
	}

	if !m.stock.Plan || !m.stock.Review {
		t.Errorf("expected defaults Plan=true, Review=true, got %v, %v", m.stock.Plan, m.stock.Review)
	}

	// Toggle Plan off with space
	m2, _ := m.Update(keyPress(" "))
	m = m2.(Model)
	if m.stock.Plan {
		t.Error("expected Plan=false after toggle")
	}
	if !strings.Contains(m.View(), "[ ] Planning stage") {
		t.Errorf("expected unchecked planning stage in view:\n%s", m.View())
	}

	// Move down to Review toggle and toggle off
	m2, _ = m.Update(keyPress("down"))
	m = m2.(Model)
	m2, _ = m.Update(keyPress(" "))
	m = m2.(Model)
	if m.stock.Review {
		t.Error("expected Review=false after toggle")
	}

	// Advance to Mapping
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)
	if m.CurrentScreen() != screenMapping {
		t.Errorf("expected screenMapping, got %v", m.CurrentScreen())
	}
}

func TestMappingScreenSlotCycling(t *testing.T) {
	opts := defaultOpts()
	m := New(context.Background(), opts)
	m.existingStatuses = testLinearDefaults
	m.screenIndex = 1 // Shape screen
	// Advance to mapping
	m2, _ := m.Update(keyPress("enter"))
	m = m2.(Model)

	if m.CurrentScreen() != screenMapping {
		t.Fatalf("expected screenMapping, got %v", m.CurrentScreen())
	}

	// Default first slot is "plan runs in", default choice is create "Planning"
	view := m.View()
	if !strings.Contains(view, "plan runs in:") || !strings.Contains(view, "create \"Planning\"") {
		t.Fatalf("unexpected mapping view:\n%s", view)
	}

	// Cycle choice with right arrow to Backlog
	m2, _ = m.Update(keyPress("right"))
	m = m2.(Model)
	if m.stock.PlanStatus != "Backlog" {
		t.Errorf("expected PlanStatus = Backlog, got %q", m.stock.PlanStatus)
	}

	// Move down to "plans wait for approval in"
	m2, _ = m.Update(keyPress("down"))
	m = m2.(Model)
	// Cycle right to Todo
	m2, _ = m.Update(keyPress("right"))
	m = m2.(Model)
	m2, _ = m.Update(keyPress("right"))
	m = m2.(Model)
	if m.stock.PlanReviewStatus != "Todo" {
		t.Errorf("expected PlanReviewStatus = Todo, got %q", m.stock.PlanReviewStatus)
	}

	// Advance to Runner
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)
	if m.CurrentScreen() != screenRunner {
		t.Errorf("expected screenRunner, got %v", m.CurrentScreen())
	}
}

func TestRunnerAndPermissionsScreens(t *testing.T) {
	opts := defaultOpts()
	m := New(context.Background(), opts)
	m.existingStatuses = testLinearDefaults
	m.screenIndex = 3 // Runner screen

	if m.CurrentScreen() != screenRunner {
		t.Fatalf("expected screenRunner, got %v", m.CurrentScreen())
	}
	if !strings.Contains(m.View(), "Claude Code") {
		t.Errorf("runner view missing Claude Code:\n%s", m.View())
	}

	// Advance to Permissions
	m2, _ := m.Update(keyPress("enter"))
	m = m2.(Model)
	if m.CurrentScreen() != screenPermissions {
		t.Fatalf("expected screenPermissions, got %v", m.CurrentScreen())
	}

	if m.stock.Bypass {
		t.Error("expected default Bypass=false")
	}

	// Toggle Bypass on
	m2, _ = m.Update(keyPress(" "))
	m = m2.(Model)
	if !m.stock.Bypass {
		t.Error("expected Bypass=true after toggle")
	}

	// Advance to Confirm (since MCPConfigured has claude)
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)
	if m.CurrentScreen() != screenConfirm {
		t.Fatalf("expected screenConfirm, got %v", m.CurrentScreen())
	}
}

func TestMCPScreenWhenUnconfigured(t *testing.T) {
	opts := defaultOpts()
	opts.MCPConfigured = map[string]bool{"claude": false} // unconfigured!

	m := New(context.Background(), opts)
	m.existingStatuses = testLinearDefaults
	m.screenIndex = 4 // Permissions screen (when AskTeam=false: Board[0], Shape[1], Mapping[2], Runner[3], Permissions[4], MCP[5], Confirm[6])

	// Advance from Permissions to MCP
	m2, _ := m.Update(keyPress("enter"))
	m = m2.(Model)
	if m.CurrentScreen() != screenMCP {
		t.Fatalf("expected screenMCP, got %v", m.CurrentScreen())
	}

	// Move to option 1 (HTTP)
	m2, _ = m.Update(keyPress("down"))
	m = m2.(Model)
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)

	if m.mcpIntent != MCPIntentHTTP {
		t.Errorf("expected MCPIntentHTTP, got %v", m.mcpIntent)
	}
	if m.CurrentScreen() != screenConfirm {
		t.Fatalf("expected screenConfirm, got %v", m.CurrentScreen())
	}
}

func TestConfirmScreenSuccessAndError(t *testing.T) {
	opts := defaultOpts()
	m := New(context.Background(), opts)
	m.existingStatuses = testLinearDefaults
	m.screenIndex = len(m.screens) - 1 // Confirm screen

	if m.CurrentScreen() != screenConfirm {
		t.Fatalf("expected screenConfirm, got %v", m.CurrentScreen())
	}

	// Normal preview
	m.updatePreview()
	if !strings.Contains(m.View(), "planned writes for LERP") {
		t.Fatalf("confirm view missing preview text:\n%s", m.View())
	}

	// Press Enter to confirm
	m2, cmd := m.Update(keyPress("enter"))
	m = m2.(Model)
	if !m.IsDone() {
		t.Error("expected IsDone = true after enter on confirm")
	}
	if cmd == nil {
		t.Error("expected quit cmd after enter on confirm")
	}

	// Error case
	m.done = false
	m.previewErr = errors.New("two queues watch status Planning")
	view := m.View()
	if !strings.Contains(view, "two queues watch status Planning") || !strings.Contains(view, "Press esc to go back") {
		t.Fatalf("unexpected confirm error view:\n%s", view)
	}

	// Press Enter does not finish when preview error is present
	m2, _ = m.Update(keyPress("enter"))
	m = m2.(Model)
	if m.IsDone() {
		t.Error("IsDone = true with error, want false")
	}
}

func TestEscWalksBackEveryScreen(t *testing.T) {
	opts := defaultOpts()
	opts.AskTeam = true
	opts.TeamKey = ""
	opts.MCPConfigured = map[string]bool{"claude": false}

	m := New(context.Background(), opts)

	// Walk forward: Team -> Board -> Shape -> Mapping -> Runner -> Permissions -> MCP -> Confirm
	screensOrder := []screen{
		screenTeam,
		screenBoard,
		screenShape,
		screenMapping,
		screenRunner,
		screenPermissions,
		screenMCP,
		screenConfirm,
	}

	// Choose team 1 from Team screen
	m2, _ := m.Update(keyPress("enter"))
	m = m2.(Model)

	// Deliver fetch statuses msg on Board screen
	m2, _ = m.Update(fetchStatusesMsg{states: testLinearDefaults})
	m = m2.(Model)

	for i := 1; i < len(screensOrder)-1; i++ {
		if m.CurrentScreen() != screensOrder[i] {
			t.Fatalf("at step %d expected screen %v, got %v", i, screensOrder[i], m.CurrentScreen())
		}
		m2, _ = m.Update(keyPress("enter"))
		m = m2.(Model)
	}
	if m.CurrentScreen() != screenConfirm {
		t.Fatalf("expected screenConfirm, got %v", m.CurrentScreen())
	}

	// Walk backward with Esc all the way to Team
	for i := len(screensOrder) - 1; i >= 0; i-- {
		if m.CurrentScreen() != screensOrder[i] {
			t.Fatalf("at backward step %d expected screen %v, got %v", i, screensOrder[i], m.CurrentScreen())
		}
		m2, _ = m.Update(keyPress("esc"))
		m = m2.(Model)
	}
}

func TestCancelFromScreenReturnsSentinel(t *testing.T) {
	opts := defaultOpts()
	m := New(context.Background(), opts)

	m2, cmd := m.Update(keyPress("q"))
	m = m2.(Model)
	if !m.IsCanceled() {
		t.Error("expected IsCanceled = true after 'q'")
	}
	if cmd == nil {
		t.Error("expected quit cmd on cancel")
	}
}

func TestRepeatInitScreenSequence(t *testing.T) {
	opts := defaultOpts()
	opts.Fresh = false
	opts.ExistingConfig = &config.RepoConfig{
		Teams: []string{"LERP"},
		Runners: map[string]config.Runner{
			"claude": {Vendor: "claude"},
		},
	}
	opts.MCPConfigured = map[string]bool{"claude": true}

	m := New(context.Background(), opts)
	// Repeat init should have Board -> Confirm
	if len(m.screens) != 2 || m.screens[0] != screenBoard || m.screens[1] != screenConfirm {
		t.Fatalf("expected [screenBoard, screenConfirm], got %v", m.screens)
	}
}
