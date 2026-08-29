// Package initcmd contains setup-time operations for lerp init.
// It deliberately has no dependency on the runtime loop.
package initcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mattwalters/lerp/internal/childenv"
	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/gitauto"
	"github.com/mattwalters/lerp/internal/initui"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/vendors"
)

// ErrCanceled is returned when init is canceled by the operator.
var ErrCanceled = initui.ErrCanceled

// Board is the small setup-time Linear surface used by Init.
// EnsureWorkflowStates reports the category of every state the team has
// after the call, keyed by state name.
type Board interface {
	EnsureTeam(ctx context.Context, key, name string) error
	Teams(ctx context.Context) ([]linear.TeamRef, error)
	TeamWorkflowStates(ctx context.Context, teamKey string) ([]linear.WorkflowState, error)
	EnsureWorkflowStates(ctx context.Context, teamKey string, states []linear.StateSpec) (map[string]string, error)
	TeamGitAutomations(ctx context.Context, teamKey string) ([]linear.GitAutomation, error)
}

// CommandRunner runs an external command with context and arguments.
type CommandRunner func(ctx context.Context, name string, args ...string) error

var defaultCommandRunner CommandRunner = func(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = childenv.Inherited()
	return cmd.Run()
}

// SetCommandRunner overrides the command runner used for CLI registrations,
// returning a restore function. Used in tests to avoid running real CLI commands.
func SetCommandRunner(runner CommandRunner) func() {
	prev := defaultCommandRunner
	defaultCommandRunner = runner
	return func() { defaultCommandRunner = prev }
}

// LookPath wraps exec.LookPath to find executables on PATH.
type LookPath func(file string) (string, error)

var defaultLookPath LookPath = exec.LookPath

// SetLookPath overrides the lookPath function used to probe installed CLIs,
// returning a restore function. Used in tests.
func SetLookPath(lp LookPath) func() {
	prev := defaultLookPath
	defaultLookPath = lp
	return func() { defaultLookPath = prev }
}

// WizardRunner runs the Bubble Tea init wizard.
type WizardRunner func(ctx context.Context, opts initui.Options) (initui.Result, error)

var defaultWizardRunner WizardRunner = initui.Run

// SetWizardRunner overrides the wizard runner used for interactive init,
// returning a restore function. Used in tests to avoid running interactive TUI.
func SetWizardRunner(runner WizardRunner) func() {
	prev := defaultWizardRunner
	defaultWizardRunner = runner
	return func() { defaultWizardRunner = prev }
}

type readState struct {
	teamKey        string
	teamName       string
	workspaceTeams []linear.TeamRef
	repoRoot       string
	configPath     string
	fresh          bool
	existingCfg    *config.RepoConfig
	needsGitignore bool
	mcpConfigured  map[string]bool
	cliInstalled   map[string]bool
}

type mcpIntent int

const (
	mcpIntentNone mcpIntent = iota
	mcpIntentHTTP
	mcpIntentBridge
)

type mcpCLIState int

const (
	mcpStateConfigured mcpCLIState = iota
	mcpStateRegisteredNow
	mcpStateDeclined
)

type mcpCLIInfo struct {
	vendorName string
	adapter    vendors.Adapter
	state      mcpCLIState
	intent     mcpIntent
}

type plan struct {
	teamKey          string
	teamName         string
	createTeam       bool
	existingStatuses []linear.WorkflowState
	automations      []linear.GitAutomation
	automationsErr   error
	repoRoot         string
	configPath       string
	writeConfig      bool
	stockText        string
	cfg              *config.RepoConfig
	needsGitignore   bool
	mcpInfos         []mcpCLIInfo
}

// Init fits lerp onto the team's existing board, writing this repo's config
// when it has none. Repeating it verifies the existing config rather than
// replacing the operator's choices.
//
// When no repo config is present, init runs the Bubble Tea wizard on
// answers (or defaults to stock answers if answers is nil):
// orient on the team's statuses, choose the optional stages, map the
// pipeline onto the board, then decide the stock runner's bypassPermissions
// grant.
//
// Init runs in four phases:
//  1. Read: inspects existing repo config, queries team statuses from Linear,
//     checks .gitignore, and probes runner CLI MCP configuration.
//  2. Decide: runs the wizard (or applies defaults), renders stock config,
//     validates repo config, and collects MCP registration choices.
//  3. Confirm: reports all planned writes (team creation, status creation/adoption,
//     config path, .gitignore update, and MCP registrations).
//  4. Execute: creates the team if needed, creates workflow states, writes repo
//     config, ignores state directory, registers MCP servers, and reports exits/summary.
//
// The file is written only after the board calls succeed, so a failed board
// call leaves nothing behind; created reports whether this invocation wrote
// it. The file lands uncommitted in the working tree, where any grant it
// carries is reviewed and checked in like any other code.
//
// Every init restates what lerp needs of the team — the status field — since
// the team's automation settings are the one part of setup lerp does not do
// itself.
//
// Init also makes sure the repository ignores lerp's state directory, since
// a first run fills it with things nobody wants staged. That comes before
// the config is written, so an init that fails on the config can leave the
// ignore behind — a line that costs nothing and is right either way.
//
// out receives the whole conversation and report; nil discards it.
func Init(ctx context.Context, board Board, out io.Writer, answers io.Reader, repoRoot, teamKey, teamName string) (created bool, err error) {
	if out == nil {
		out = io.Discard
	}

	r, err := read(ctx, board, repoRoot, teamKey, teamName)
	if err != nil {
		return false, err
	}

	p, err := decide(ctx, board, out, answers, r)
	if err != nil {
		return false, err
	}

	reportPlan(out, p)

	return execute(ctx, board, out, p, defaultCommandRunner)
}

func read(ctx context.Context, board Board, repoRoot, teamKey, teamName string) (readState, error) {
	foundPath, findErr := config.FindRepoConfig(repoRoot)
	var cfg *config.RepoConfig
	fresh := false
	path := filepath.Join(repoRoot, config.RepoConfigFile)
	if findErr == nil {
		path = foundPath
		c, err := config.LoadRepoConfig(path)
		if err != nil {
			return readState{}, fmt.Errorf("existing repo config: %w", err)
		}
		cfg = c
	} else if errors.Is(findErr, fs.ErrNotExist) {
		fresh = true
	} else {
		return readState{}, findErr
	}

	normKey := strings.ToUpper(strings.TrimSpace(teamKey))
	if !fresh && cfg != nil && normKey != "" {
		if !slices.Contains(cfg.Teams, normKey) {
			return readState{}, fmt.Errorf("existing repo config %s does not serve team %q", path, normKey)
		}
	}

	teams, err := board.Teams(ctx)
	if err != nil {
		return readState{}, fmt.Errorf("read workspace teams: %w", err)
	}

	gitIgnorePath := filepath.Join(repoRoot, gitignoreFile)
	existingGitignore, gitErr := os.ReadFile(gitIgnorePath)
	needsGitignore := true
	if gitErr == nil && ignoresStateDir(existingGitignore) {
		needsGitignore = false
	}

	mcpConfigured := make(map[string]bool)
	for _, name := range vendors.Names() {
		if adapter, ok := vendors.Lookup(name); ok {
			mcpConfigured[name] = adapter.HasLinearMCP(repoRoot)
		}
	}

	cliInstalled := make(map[string]bool)
	for _, name := range vendors.Names() {
		if adapter, ok := vendors.Lookup(name); ok {
			_, err := defaultLookPath(adapter.CLIName())
			cliInstalled[name] = (err == nil)
		}
	}

	return readState{
		teamKey:        normKey,
		teamName:       teamName,
		workspaceTeams: teams,
		repoRoot:       repoRoot,
		configPath:     path,
		fresh:          fresh,
		existingCfg:    cfg,
		needsGitignore: needsGitignore,
		mcpConfigured:  mcpConfigured,
		cliInstalled:   cliInstalled,
	}, nil
}

func buildMCPInfos(cfg *config.RepoConfig, mcpConfigured map[string]bool, intent initui.MCPIntent) []mcpCLIInfo {
	seen := make(map[string]bool)
	var vendorNames []string
	if cfg != nil {
		for _, runner := range cfg.Runners {
			if runner.Vendor != "" && !seen[runner.Vendor] {
				seen[runner.Vendor] = true
				vendorNames = append(vendorNames, runner.Vendor)
			}
		}
	}
	slices.Sort(vendorNames)

	var infos []mcpCLIInfo
	for _, vName := range vendorNames {
		adapter, ok := vendors.Lookup(vName)
		if !ok {
			continue
		}
		info := mcpCLIInfo{vendorName: vName, adapter: adapter}
		if mcpConfigured[vName] {
			info.state = mcpStateConfigured
		} else {
			info.state = mcpStateDeclined
			switch intent {
			case initui.MCPIntentHTTP:
				info.intent = mcpIntentHTTP
			case initui.MCPIntentBridge:
				info.intent = mcpIntentBridge
			default:
				info.intent = mcpIntentNone
			}
		}
		infos = append(infos, info)
	}
	return infos
}

func decide(ctx context.Context, board Board, out io.Writer, answers io.Reader, r readState) (plan, error) {
	if answers == nil {
		// Non-interactive path (--yes, piped init)
		teamKey, teamName, createTeam, ask, err := resolveTeamRules(r.workspaceTeams, r.existingCfg, r.configPath, r.teamKey, r.teamName, false)
		if err != nil {
			return plan{}, err
		}
		if ask {
			return plan{}, errors.New("team key must not be empty (--team is required)")
		}

		if !r.fresh && r.existingCfg != nil {
			if !slices.Contains(r.existingCfg.Teams, teamKey) {
				return plan{}, fmt.Errorf("existing repo config %s does not serve team %q", r.configPath, teamKey)
			}
		}

		var existing []linear.WorkflowState
		var automations []linear.GitAutomation
		var automationsErr error
		if !createTeam {
			states, err := board.TeamWorkflowStates(ctx, teamKey)
			if err != nil {
				if errors.Is(err, linear.ErrNotFound) {
					createTeam = true
					existing = nil
				} else {
					return plan{}, fmt.Errorf("read statuses of team %q: %w", teamKey, err)
				}
			} else {
				existing = states
				automations, automationsErr = board.TeamGitAutomations(ctx, teamKey)
			}
		}

		var cfg *config.RepoConfig
		stock := ""
		if r.fresh {
			choices := converse(out, teamKey, existing)
			stock = choices.Render()
			if cfg, err = config.ParseRepoConfig(stock, r.configPath); err != nil {
				return plan{}, fmt.Errorf("assembled repo config: %w", err)
			}
		} else {
			cfg = r.existingCfg
		}

		infos := buildMCPInfos(cfg, r.mcpConfigured, initui.MCPIntentNone)

		return plan{
			teamKey:          teamKey,
			teamName:         teamName,
			createTeam:       createTeam,
			existingStatuses: existing,
			automations:      automations,
			automationsErr:   automationsErr,
			repoRoot:         r.repoRoot,
			configPath:       r.configPath,
			writeConfig:      r.fresh,
			stockText:        stock,
			cfg:              cfg,
			needsGitignore:   r.needsGitignore,
			mcpInfos:         infos,
		}, nil
	}

	// Interactive path: Bubble Tea wizard
	teamKey, teamName, createTeam, ask, err := resolveTeamRules(r.workspaceTeams, r.existingCfg, r.configPath, r.teamKey, r.teamName, true)
	if err != nil {
		return plan{}, err
	}

	preview := func(choices initui.Choices) (string, error) {
		var cfg *config.RepoConfig
		var stockText string
		if r.fresh {
			stockText = choices.Stock.Render()
			var err error
			cfg, err = config.ParseRepoConfig(stockText, r.configPath)
			if err != nil {
				return "", fmt.Errorf("assembled repo config: %w", err)
			}
		} else {
			cfg = r.existingCfg
		}

		var existing []linear.WorkflowState
		var automations []linear.GitAutomation
		var automationsErr error
		if !choices.CreateTeam {
			existing, _ = board.TeamWorkflowStates(ctx, choices.TeamKey)
			automations, automationsErr = board.TeamGitAutomations(ctx, choices.TeamKey)
		}

		infos := buildMCPInfos(cfg, r.mcpConfigured, choices.MCPIntent)

		p := plan{
			teamKey:          choices.TeamKey,
			teamName:         choices.TeamName,
			createTeam:       choices.CreateTeam,
			existingStatuses: existing,
			automations:      automations,
			automationsErr:   automationsErr,
			repoRoot:         r.repoRoot,
			configPath:       r.configPath,
			writeConfig:      r.fresh,
			stockText:        stockText,
			cfg:              cfg,
			needsGitignore:   r.needsGitignore,
			mcpInfos:         infos,
		}

		var buf bytes.Buffer
		reportPlan(&buf, p)
		return buf.String(), nil
	}

	opts := initui.Options{
		WorkspaceTeams:  r.workspaceTeams,
		TeamKey:         teamKey,
		TeamName:        teamName,
		AskTeam:         ask,
		AllowCreateTeam: r.existingCfg == nil,
		Fresh:           r.fresh,
		ExistingConfig:  r.existingCfg,
		MCPConfigured:   r.mcpConfigured,
		CLIInstalled:    r.cliInstalled,
		FetchStatuses:   board.TeamWorkflowStates,
		Preview:         preview,
	}

	res, err := defaultWizardRunner(ctx, opts)
	if err != nil {
		return plan{}, err
	}

	teamKey = res.TeamKey
	teamName = res.TeamName
	createTeam = res.CreateTeam

	if !r.fresh && r.existingCfg != nil {
		if !slices.Contains(r.existingCfg.Teams, teamKey) {
			return plan{}, fmt.Errorf("existing repo config %s does not serve team %q", r.configPath, teamKey)
		}
	}

	var existing []linear.WorkflowState
	var automations []linear.GitAutomation
	var automationsErr error
	if !createTeam {
		states, err := board.TeamWorkflowStates(ctx, teamKey)
		if err != nil {
			if errors.Is(err, linear.ErrNotFound) {
				createTeam = true
				existing = nil
			} else {
				return plan{}, fmt.Errorf("read statuses of team %q: %w", teamKey, err)
			}
		} else {
			existing = states
			automations, automationsErr = board.TeamGitAutomations(ctx, teamKey)
		}
	}

	var cfg *config.RepoConfig
	stock := ""
	if r.fresh {
		stock = res.Stock.Render()
		if cfg, err = config.ParseRepoConfig(stock, r.configPath); err != nil {
			return plan{}, fmt.Errorf("assembled repo config: %w", err)
		}
	} else {
		cfg = r.existingCfg
	}

	infos := buildMCPInfos(cfg, r.mcpConfigured, res.MCPIntent)

	return plan{
		teamKey:          teamKey,
		teamName:         teamName,
		createTeam:       createTeam,
		existingStatuses: existing,
		automations:      automations,
		automationsErr:   automationsErr,
		repoRoot:         r.repoRoot,
		configPath:       r.configPath,
		writeConfig:      r.fresh,
		stockText:        stock,
		cfg:              cfg,
		needsGitignore:   r.needsGitignore,
		mcpInfos:         infos,
	}, nil
}

func reportPlan(out io.Writer, p plan) {
	if p.createTeam {
		if p.teamName != "" && p.teamName != p.teamKey {
			fmt.Fprintf(out, "creating team %s (%s)\n", p.teamKey, p.teamName)
		} else {
			fmt.Fprintf(out, "creating team %s\n", p.teamKey)
		}
	}
	reportStatuses(out, p.teamKey, p.cfg, p.existingStatuses)
	if p.writeConfig {
		fmt.Fprintf(out, "writing %s\n", p.configPath)
	} else {
		fmt.Fprintf(out, "using existing %s\n", p.configPath)
	}
	if p.needsGitignore {
		fmt.Fprintf(out, "adding %s to %s\n", stateDirPattern, gitignoreFile)
	} else {
		fmt.Fprintf(out, "%s already ignores %s\n", gitignoreFile, stateDirPattern)
	}
	for _, info := range p.mcpInfos {
		cli := info.adapter.CLIName()
		label := info.vendorName
		if info.vendorName != cli {
			label = fmt.Sprintf("%s (%s)", info.vendorName, cli)
		}
		switch info.intent {
		case mcpIntentHTTP:
			fmt.Fprintf(out, "registering %s Linear MCP\n", label)
		case mcpIntentBridge:
			fmt.Fprintf(out, "registering %s Linear MCP (bridge)\n", label)
		}
	}
	if !p.createTeam {
		reportGitAutomations(out, p.cfg, p.teamKey, p.automations, p.automationsErr)
	}
}

func execute(ctx context.Context, board Board, out io.Writer, p plan, runner CommandRunner) (created bool, err error) {
	if p.createTeam {
		if err := board.EnsureTeam(ctx, p.teamKey, p.teamName); err != nil {
			return false, fmt.Errorf("ensure team %q: %w", p.teamKey, err)
		}
		// A newly created team did not exist during the read phase, so
		// its automations are checked now that EnsureTeam has created it.
		findings := gitauto.Check(ctx, board, p.cfg, p.teamKey)
		reportGitAutomationsFindings(out, p.teamKey, findings)
	}
	categories, err := board.EnsureWorkflowStates(ctx, p.teamKey, stateSpecs(p.cfg))
	if err != nil {
		return false, fmt.Errorf("ensure workflow states for %q: %w", p.teamKey, err)
	}
	reportExits(out, p.cfg, categories)
	ignoreStateDir(out, p.repoRoot)
	if p.writeConfig {
		if created, err = writeRepoConfig(p.configPath, p.teamKey, p.stockText); err != nil {
			return false, err
		}
	}
	for i := range p.mcpInfos {
		info := &p.mcpInfos[i]
		if info.intent != mcpIntentNone {
			var cmdArgs []string
			if info.intent == mcpIntentBridge {
				cmdArgs = info.adapter.MCPRegisterBridge()
			} else {
				cmdArgs = info.adapter.MCPRegisterHTTP()
			}
			if len(cmdArgs) > 0 {
				if err := runner(ctx, cmdArgs[0], cmdArgs[1:]...); err == nil {
					info.state = mcpStateRegisteredNow
				} else {
					fmt.Fprintf(out, "could not register %s MCP: %v\n", info.adapter.CLIName(), err)
				}
			}
		}
	}
	reportMCPSummary(out, p.mcpInfos)
	return created, nil
}

func loadFor(path, teamKey string) (*config.RepoConfig, error) {
	c, err := config.LoadRepoConfig(path)
	if err != nil {
		return nil, fmt.Errorf("existing repo config: %w", err)
	}
	if !slices.Contains(c.Teams, teamKey) {
		return nil, fmt.Errorf("existing repo config %s does not serve team %q", path, teamKey)
	}
	return c, nil
}

func writeRepoConfig(path, teamKey, stock string) (created bool, err error) {
	// O_EXCL makes creation race-safe and, importantly, never overwrites a
	// configuration that appeared since Init looked.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			_, err := loadFor(path, teamKey)
			return false, err
		}
		return false, fmt.Errorf("create repo config: %w", err)
	}
	_, writeErr := f.WriteString(stock)
	closeErr := f.Close()
	if writeErr != nil {
		return false, fmt.Errorf("write repo config: %w", writeErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close repo config: %w", closeErr)
	}
	return true, nil
}

func reportGitAutomations(out io.Writer, cfg *config.RepoConfig, teamKey string, automations []linear.GitAutomation, readErr error) {
	findings := gitauto.Findings(cfg, teamKey, automations, readErr)
	reportGitAutomationsFindings(out, teamKey, findings)
}

func reportGitAutomationsFindings(out io.Writer, teamKey string, findings []string) {
	reportStatusOwnership(out, teamKey)
	if len(findings) == 0 {
		fmt.Fprintf(out, "  team %s has no pull-request automation that would move a ticket mid-stage\n", teamKey)
		return
	}
	for _, line := range findings {
		fmt.Fprintln(out, line)
	}
}

// reportStatusOwnership states the price of lerp's central bet, at the one
// moment the operator is configuring this team: a queue is a status, a stage
// finishes by moving the ticket, so lerp needs the status field on the teams
// it serves. An automation that moves a ticket mid-stage takes that stage's
// move away.
//
// Setup time is where it belongs (SCOPE invariant 6): the team's settings
// screen is the one part of setup lerp does not do, and this is the moment
// the operator is already on the board.
//
// Deliberately short: the rule itself, followed by the team's actual findings
// or the clean line saying none were found.
func reportStatusOwnership(out io.Writer, teamKey string) {
	fmt.Fprintf(out, "lerp now drives team %s by moving tickets between statuses, so it needs that\n", teamKey)
	fmt.Fprintf(out, "  field: an automation that moves a ticket while a stage is running takes the\n")
	fmt.Fprintf(out, "  stage's own move away, and the hop it would have made never happens.\n")
}

// gitignoreFile is the ignore list init appends to, at the repository root.
const gitignoreFile = ".gitignore"

// stateDirPattern ignores lerp's state directory. A first run fills .lerp
// with run records, the loop log, the lock, and a full git worktree per
// workspace: noise in every git status, and gitlinks that `git add .` will
// stage as embedded repositories. None of it is the adopter's code. The
// directory itself is internal/evidence's; this is the only place setup-time
// code names it, so the constant lives here rather than making init depend
// on the runtime.
const stateDirPattern = ".lerp/"

// stateDirBlock is what init appends: the pattern under a comment saying
// whose directory it is, since the adopter reads this file later without us.
const stateDirBlock = "# lerp's run records, workspaces and logs\n" + stateDirPattern + "\n"

// ignoreStateDir makes sure the repository ignores lerp's state directory
// and says what it did, in init's loud style.
//
// A .gitignore lerp cannot write is reported and survived, never fatal: the
// ignore is a convenience, and failing over it would leave the operator with
// a board init had already changed and no lerp.toml at all — a state no
// repeat of init could repair, since the next run fails at the same file.
func ignoreStateDir(out io.Writer, repoRoot string) {
	err := appendStateDirIgnore(out, repoRoot)
	if err == nil {
		return
	}
	fmt.Fprintf(out, "could not ignore %s: %v\n", stateDirPattern, err)
	fmt.Fprintf(out, "  Add %s to %s yourself — until you do, lerp's run records and\n", stateDirPattern, gitignoreFile)
	fmt.Fprintf(out, "  workspace worktrees show up in this repo's git status.\n")
}

// appendStateDirIgnore appends stateDirPattern to the repository's
// .gitignore, creating the file when there is none. A repository that
// already ignores the directory is left untouched.
func appendStateDirIgnore(out io.Writer, repoRoot string) error {
	path := filepath.Join(repoRoot, gitignoreFile)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", gitignoreFile, err)
	}
	if ignoresStateDir(existing) {
		return nil
	}
	// Never continue somebody's last line, and keep one blank line between
	// the block and whatever section it lands after — no more, on a file
	// that already ends blank.
	block := stateDirBlock
	switch {
	case len(existing) == 0 || bytes.HasSuffix(existing, []byte("\n\n")):
	case bytes.HasSuffix(existing, []byte("\n")):
		block = "\n" + block
	default:
		block = "\n\n" + block
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", gitignoreFile, err)
	}
	_, writeErr := f.WriteString(block)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("write %s: %w", gitignoreFile, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", gitignoreFile, closeErr)
	}
	fmt.Fprintf(out, "added %s to %s — check that in alongside %s, or only this\n",
		stateDirPattern, gitignoreFile, config.RepoConfigFile)
	fmt.Fprintf(out, "  clone is covered: a colleague who clones this repo never runs lerp init.\n")
	return nil
}

// ignoresStateDir reports whether an existing .gitignore already lists the
// state directory, in any of the spellings that mean the same thing at the
// repository root. It reads lines, not gitignore semantics: a pattern that
// covers .lerp some other way (a "**/" prefix, a global excludes file) costs
// one redundant line the first time, and is recognised on every run after.
func ignoresStateDir(gitignore []byte) bool {
	for _, line := range strings.Split(string(gitignore), "\n") {
		switch strings.TrimSpace(line) {
		case ".lerp", ".lerp/", "/.lerp", "/.lerp/":
			return true
		}
	}
	return false
}

// converse prints the existing board statuses (if any) and returns the stock answers
// for non-interactive / piped / --yes init.
func converse(out io.Writer, teamKey string, existing []linear.WorkflowState) config.Stock {
	s := config.Stock{Teams: []string{teamKey}, Plan: true, Review: true}
	if len(existing) == 0 {
		fmt.Fprintf(out, "team %s has no statuses yet\n", teamKey)
	} else {
		type categoryGroup struct {
			category string
			names    []string
		}
		var groups []categoryGroup
		categoryIndex := map[string]int{}
		for _, state := range existing {
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
		fmt.Fprintf(out, "team %s has:\n", teamKey)
		for _, g := range groups {
			fmt.Fprintf(out, "  %-9s  %s\n", g.category, strings.Join(g.names, ", "))
		}
	}
	return s
}

// reportStatuses says what init is about to do to the board — created vs
// found, never silent (SCOPE invariant 6 discipline: create deliberately,
// touch nothing silently). Existing statuses are annotated with their part
// in the pipeline; created ones are the names the operator just chose.
func reportStatuses(out io.Writer, teamKey string, cfg *config.RepoConfig, existing []linear.WorkflowState) {
	roles := statusRoles(cfg)
	has := map[string]bool{}
	for _, state := range existing {
		has[state.Name] = true
	}
	var create, use []string
	for _, name := range slices.Sorted(maps.Keys(roles)) {
		if has[name] {
			use = append(use, fmt.Sprintf("%s (%s)", name, strings.Join(roles[name], ", ")))
		} else {
			create = append(create, name)
		}
	}
	switch {
	case len(use) == 0:
		fmt.Fprintf(out, "creating on team %s: %s\n", teamKey, strings.Join(create, ", "))
	case len(create) == 0:
		fmt.Fprintf(out, "using existing on team %s: %s\n", teamKey, strings.Join(use, ", "))
	default:
		fmt.Fprintf(out, "creating on team %s: %s  ·  using existing: %s\n",
			teamKey, strings.Join(create, ", "), strings.Join(use, ", "))
	}
}

// statusRoles names each referenced status's part in the pipeline: the queue
// that runs in it, "<queue> exit" for an on_success no queue watches, and
// "failure exit" for an on_failure no queue watches.
func statusRoles(cfg *config.RepoConfig) map[string][]string {
	watched := cfg.WatchedStatuses()
	roles := map[string][]string{}
	add := func(name, role string) {
		if !slices.Contains(roles[name], role) {
			roles[name] = append(roles[name], role)
		}
	}
	for _, qname := range slices.Sorted(maps.Keys(cfg.Queues)) {
		add(cfg.Queues[qname].Status, qname)
	}
	for _, qname := range slices.Sorted(maps.Keys(cfg.Queues)) {
		q := cfg.Queues[qname]
		if !watched[q.OnSuccess] {
			add(q.OnSuccess, qname+" exit")
		}
		if q.OnFailure != "" && !watched[q.OnFailure] {
			add(q.OnFailure, "failure exit")
		}
	}
	return roles
}

// stateSpecs names every status the queues reference, all in Linear's
// "started" category. The names are statusRoles' keys, not a second walk of
// the queues: what init creates and what it just reported creating are then
// the same set by construction, rather than two loops that happen to encode
// the same rule. Grow Queue another status-valued field and the pair still
// cannot drift apart — reporting a status init never creates, or creating
// one it never reported and failing loop.Verify on the first run.
func stateSpecs(cfg *config.RepoConfig) []linear.StateSpec {
	names := slices.Sorted(maps.Keys(statusRoles(cfg)))
	specs := make([]linear.StateSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, linear.StateSpec{Name: name, Type: "started"})
	}
	return specs
}

// pipelineExits are the on_success targets no queue watches: the statuses
// where work leaves the automated path.
func pipelineExits(cfg *config.RepoConfig) []string {
	watched := cfg.WatchedStatuses()
	seen := map[string]bool{}
	exits := []string{}
	for _, q := range cfg.Queues {
		if watched[q.OnSuccess] || seen[q.OnSuccess] {
			continue
		}
		seen[q.OnSuccess] = true
		exits = append(exits, q.OnSuccess)
	}
	slices.Sort(exits)
	return exits
}

// reportExits tells the operator, for each pipeline exit, whether Linear's
// category for that status ends work. Lerp never sets a completed category
// itself — that guess is wrong for a human column and its cost is silent —
// so a genuinely terminal exit is the one piece of board setup only the
// operator can finish, and this report is where init says so.
func reportExits(out io.Writer, cfg *config.RepoConfig, categories map[string]string) {
	for _, name := range pipelineExits(cfg) {
		category, ok := categories[name]
		if category == "completed" || category == "canceled" {
			fmt.Fprintf(out, "pipeline exit %q: Linear categorises it as %s; tickets that land there stop blocking their dependents.\n", name, category)
			continue
		}
		if !ok {
			category = "unknown"
		}
		fmt.Fprintf(out, "pipeline exit %q: Linear categorises it as %s, not completed.\n", name, category)
		fmt.Fprintf(out, "  Tickets that land there keep blocking their dependents — right if a human\n")
		fmt.Fprintf(out, "  still acts on them there, wrong if %q means the work is done. Lerp\n", name)
		fmt.Fprintf(out, "  will not guess: if that status truly ends work, set its category to Done\n")
		fmt.Fprintf(out, "  in Linear yourself.\n")
	}
}

func reportMCPSummary(out io.Writer, infos []mcpCLIInfo) {
	if len(infos) == 0 {
		return
	}
	fmt.Fprintln(out)
	for _, info := range infos {
		cli := info.adapter.CLIName()
		label := info.vendorName
		if info.vendorName != cli {
			label = fmt.Sprintf("%s (%s)", info.vendorName, cli)
		}
		switch info.state {
		case mcpStateConfigured:
			fmt.Fprintf(out, "%s: Linear MCP already configured\n", label)
		case mcpStateRegisteredNow:
			fmt.Fprintf(out, "%s: registered Linear MCP — one-time authentication still needed: %s\n",
				label, info.adapter.AuthInstruction())
		case mcpStateDeclined:
			fmt.Fprintf(out, "%s: Linear MCP not configured\n", label)
			fmt.Fprintf(out, "  register: %s\n", strings.Join(info.adapter.MCPRegisterHTTP(), " "))
			fmt.Fprintf(out, "  alternative (shared OAuth): %s\n", strings.Join(info.adapter.MCPRegisterBridge(), " "))
			fmt.Fprintf(out, "  then authenticate: %s\n", info.adapter.AuthInstruction())
		}
	}
}

// resolveTeamRules resolves the team to use for init, reporting whether user
// interaction (ask) is needed.
func resolveTeamRules(teams []linear.TeamRef, cfg *config.RepoConfig, configPath, teamKey, teamName string, interactive bool) (resolvedKey string, resolvedName string, shouldCreate bool, ask bool, err error) {
	teamKey = strings.ToUpper(strings.TrimSpace(teamKey))
	teamName = strings.TrimSpace(teamName)

	if cfg != nil {
		if teamKey != "" {
			for _, t := range teams {
				if t.Key == teamKey {
					name := teamName
					if name == "" {
						name = t.Name
					}
					return teamKey, name, false, false, nil
				}
			}
			return "", "", false, false, fmt.Errorf("team %q configured in %s does not exist in workspace", teamKey, configPath)
		}

		var filtered []linear.TeamRef
		for _, t := range teams {
			if slices.Contains(cfg.Teams, t.Key) {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			return "", "", false, false, fmt.Errorf("none of the teams configured in %s (%s) exist in workspace", configPath, strings.Join(cfg.Teams, ", "))
		}
		if len(filtered) == 1 {
			return filtered[0].Key, filtered[0].Name, false, false, nil
		}
		if !interactive {
			return "", "", false, false, errors.New("team key must not be empty (--team is required)")
		}
		return "", "", false, true, nil
	}

	if teamKey == "" && !interactive {
		return "", "", false, false, errors.New("team key must not be empty (--team is required)")
	}

	if teamKey != "" {
		for _, t := range teams {
			if t.Key == teamKey {
				name := teamName
				if name == "" {
					name = t.Name
				}
				return teamKey, name, false, false, nil
			}
		}
		if teamName == "" {
			teamName = teamKey
		}
		if !interactive {
			return teamKey, teamName, true, false, nil
		}
		return teamKey, teamName, true, true, nil
	}

	if len(teams) == 0 {
		return "", "", true, true, nil
	}

	return "", "", false, true, nil
}
