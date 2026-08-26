// Package initcmd contains setup-time operations for lerp init.
// It deliberately has no dependency on the runtime loop.
package initcmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

// Board is the small setup-time Linear surface used by Init.
// EnsureWorkflowStates reports the category of every state the team has
// after the call, keyed by state name.
type Board interface {
	EnsureTeam(ctx context.Context, key, name string) error
	TeamStates(ctx context.Context, teamKey string) ([]string, error)
	EnsureWorkflowStates(ctx context.Context, teamKey string, states []linear.StateSpec) (map[string]string, error)
}

// Init fits lerp onto the team's existing board, writing this repo's config
// when it has none. Repeating it verifies the existing config rather than
// replacing the operator's choices.
//
// When lerp.toml is absent, init is a short conversation on out/answers:
// orient on the team's statuses, choose the optional stages, map the
// pipeline onto the board, then decide the stock runner's bypassPermissions
// grant. answers is where it reads — os.Stdin at a terminal, a scripted
// reader in tests, nil for the stock answer to everything (the full
// pipeline, stock status names, bypass declined). Either way init reports
// loudly which statuses it creates and which existing ones it uses; existing
// statuses are never modified.
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
	if teamKey == "" {
		return false, fmt.Errorf("team key must not be empty")
	}
	if teamName == "" {
		teamName = teamKey
	}
	if out == nil {
		out = io.Discard
	}
	path := filepath.Join(repoRoot, config.RepoConfigFile)
	var cfg *config.RepoConfig
	fresh := false
	if _, statErr := os.Stat(path); statErr == nil {
		if cfg, err = loadFor(path, teamKey); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(statErr) {
		return false, fmt.Errorf("check repo config: %w", statErr)
	} else {
		fresh = true
	}
	if err := board.EnsureTeam(ctx, teamKey, teamName); err != nil {
		return false, fmt.Errorf("ensure team %q: %w", teamKey, err)
	}
	existing, err := board.TeamStates(ctx, teamKey)
	if err != nil {
		return false, fmt.Errorf("read statuses of team %q: %w", teamKey, err)
	}
	stock := ""
	if fresh {
		choices := converse(out, answers, teamKey, existing)
		stock = choices.Render()
		// Parsing what we are about to install catches a broken assembly here —
		// a declined stage, a mapping that folds two queues onto one status —
		// not on the operator's first run.
		if cfg, err = config.ParseRepoConfig(stock, path); err != nil {
			return false, fmt.Errorf("assembled repo config: %w", err)
		}
	}
	reportStatuses(out, teamKey, cfg, existing)
	categories, err := board.EnsureWorkflowStates(ctx, teamKey, stateSpecs(cfg))
	if err != nil {
		return false, fmt.Errorf("ensure workflow states for %q: %w", teamKey, err)
	}
	reportExits(out, cfg, categories)
	reportStatusOwnership(out, teamKey)
	// Every init, not only a fresh one: an adopter who set this repo up with
	// an earlier lerp picks the ignore up by repeating init.
	ignoreStateDir(out, repoRoot)
	if !fresh {
		return false, nil
	}
	return writeRepoConfig(path, teamKey, stock)
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

// reportStatusOwnership states the price of lerp's central bet, at the one
// moment the operator is configuring this team: a queue is a status, a stage
// finishes by moving the ticket, so lerp needs the status field on the teams
// it serves. An automation that moves a ticket mid-stage takes that stage's
// move away — the loop keeps whatever move it finds, since that is how an
// agent escalates — and the on_success hop never happens. Nothing in the
// config expresses that requirement, so an adopter who is never told pays it
// by surprise, one silently skipped stage at a time.
//
// Setup time is where it belongs (SCOPE invariant 6): the team's settings
// screen is the one part of setup lerp does not do, and this is the moment
// the operator is already on the board.
//
// Deliberately short, and deliberately not a list of trigger names. The
// startup check reads the team's actual automations and names each mid-stage
// rule whose target the config never mentions, what each queue loses to it and
// the fix; repeating any of that here would be a second copy going stale
// against the real one. What init adds is the rule itself, before there is
// anything to detect — an operator who hears "lerp owns this field" while
// setting the team up does not have to trip the collision to learn it.
func reportStatusOwnership(out io.Writer, teamKey string) {
	fmt.Fprintf(out, "lerp now drives team %s by moving tickets between statuses, so it needs that\n", teamKey)
	fmt.Fprintf(out, "  field: an automation that moves a ticket while a stage is running takes the\n")
	fmt.Fprintf(out, "  stage's own move away, and the hop it would have made never happens. Under\n")
	fmt.Fprintf(out, "  team %s's workflow settings, set the pull-request triggers that fire while a\n", teamKey)
	fmt.Fprintf(out, "  pull request is open to No action; the one for a merged pull request is the\n")
	fmt.Fprintf(out, "  keeper, unless your pipeline has a stage that runs after the merge. Every\n")
	fmt.Fprintf(out, "  `lerp` start re-reads this team's automations and names the mid-stage ones\n")
	fmt.Fprintf(out, "  that point somewhere %s does not.\n", config.RepoConfigFile)
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
		fmt.Fprintf(out, "%s already ignores %s\n", gitignoreFile, stateDirPattern)
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

// converse runs the short init conversation and returns the choices it
// collected. A nil answers reader — a piped init, or --yes — takes the stock
// answer to everything: every stage included, stock status names, bypass
// declined. EOF mid-conversation answers the remaining questions the same
// way each question's default would.
func converse(out io.Writer, answers io.Reader, teamKey string, existing []string) config.Stock {
	s := config.Stock{Teams: []string{teamKey}, Plan: true, Review: true}
	if len(existing) == 0 {
		fmt.Fprintf(out, "team %s has no statuses yet\n", teamKey)
	} else {
		fmt.Fprintf(out, "team %s has: %s\n", teamKey, strings.Join(existing, ", "))
	}
	if answers == nil {
		return s
	}
	in := bufio.NewReader(answers)
	s.Plan = askYesNo(out, in, "Include a planning stage?", true)
	s.Review = askYesNo(out, in, "Review each change before it exits?", true)
	mapStatuses(out, in, teamKey, existing, &s)
	s.Bypass = askBypass(out, in)
	return s
}

// slot is one status the chosen pipeline references: where a queue runs, or
// where it exits. Each maps onto an existing status or creates its stock
// name.
type slot struct {
	label string  // "implement runs in"
	stock string  // the stock status name, created when no existing one is chosen
	dest  *string // the config.Stock field this slot fills
}

// pipelineSlots lists the statuses s's stages reference, in pipeline order.
// The review pass has no slot: it runs inside the implement queue and names
// no status of its own.
func pipelineSlots(s *config.Stock) []slot {
	slots := []slot{}
	if s.Plan {
		slots = append(slots,
			slot{"plan runs in", config.StockPlanStatus, &s.PlanStatus},
			slot{"plans wait for approval in", config.StockPlanReviewStatus, &s.PlanReviewStatus},
		)
	}
	slots = append(slots, slot{"implement runs in", config.StockImplementStatus, &s.ImplementStatus})
	return append(slots,
		slot{"finished work exits to", config.StockExitStatus, &s.ExitStatus},
		slot{"failures exit to", config.StockAttentionStatus, &s.AttentionStatus},
	)
}

// mapStatuses maps the chosen pipeline onto the board: the fast path accepts
// the stock names in one answer, customize picks per referenced status.
func mapStatuses(out io.Writer, in *bufio.Reader, teamKey string, existing []string, s *config.Stock) {
	slots := pipelineSlots(s)
	has := map[string]bool{}
	for _, name := range existing {
		has[name] = true
	}
	names := make([]string, len(slots))
	missing := 0
	for i, sl := range slots {
		names[i] = sl.stock
		if has[sl.stock] {
			names[i] += " (exists)"
		} else {
			missing++
		}
	}
	fmt.Fprintf(out, "the pipeline references: %s\n", strings.Join(names, ", "))
	var question string
	switch {
	case missing == len(slots):
		question = fmt.Sprintf("Create these %d statuses on team %s?", missing, teamKey)
	case missing == 1:
		question = fmt.Sprintf("Create the missing status on team %s?", teamKey)
	case missing > 1:
		question = fmt.Sprintf("Create the %d missing statuses on team %s?", missing, teamKey)
	default:
		question = fmt.Sprintf("Use these existing statuses on team %s?", teamKey)
	}
	for {
		fmt.Fprintf(out, "%s [Y]es / [c]ustomize ", question)
		answer, eof := readAnswer(out, in)
		switch answer {
		case "", "y", "yes":
			return // the stock names; Render fills them in
		case "c", "customize":
			for _, sl := range slots {
				*sl.dest = pickStatus(out, in, sl, existing)
			}
			return
		}
		if eof {
			return
		}
	}
}

// pickStatus asks where one referenced status lands: a numbered existing
// status, or create the stock name. When the stock name already exists on
// the board it is the default pick, and there is nothing to create.
func pickStatus(out io.Writer, in *bufio.Reader, sl slot, existing []string) string {
	var menu strings.Builder
	fmt.Fprintf(&menu, "%s: ", sl.label)
	def := "c"
	for i, name := range existing {
		fmt.Fprintf(&menu, " %d) %s ", i+1, name)
		if name == sl.stock {
			def = strconv.Itoa(i + 1)
		}
	}
	if def == "c" {
		fmt.Fprintf(&menu, " c) create %q ", sl.stock)
	}
	fmt.Fprintf(&menu, " [%s] ", def)
	for {
		fmt.Fprint(out, menu.String())
		answer, eof := readAnswer(out, in)
		if answer == "" {
			answer = def
		}
		if answer == "c" && def == "c" {
			return sl.stock
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(existing) {
			return existing[n-1]
		}
		if eof {
			return sl.stock
		}
	}
}

// askYesNo asks a yes/no question whose empty answer (or EOF) means def.
func askYesNo(out io.Writer, in *bufio.Reader, question string, def bool) bool {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	for {
		fmt.Fprintf(out, "%s %s ", question, hint)
		answer, eof := readAnswer(out, in)
		switch answer {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		case "":
			return def
		}
		if eof {
			return def
		}
	}
}

// askBypass asks whether the stock runner keeps its bypassPermissions grant,
// now that the operator has heard what init will do to the board. Anything
// but an explicit yes — including EOF — declines.
func askBypass(out io.Writer, in *bufio.Reader) bool {
	fmt.Fprintln(out, "The stock Claude runner can include --permission-mode bypassPermissions,")
	fmt.Fprintln(out, "letting agents edit files and run commands unattended with your full user")
	fmt.Fprintln(out, "account. Declining writes a runner without the flag; unattended runs will")
	fmt.Fprintln(out, "fail at the first tool they are not allowed to use until you widen it in")
	fmt.Fprintln(out, config.RepoConfigFile+", in review, deliberately.")
	fmt.Fprint(out, "Include --permission-mode bypassPermissions? [y/N] ")
	answer, _ := readAnswer(out, in)
	return answer == "y" || answer == "yes"
}

// readAnswer reads one lowercased answer line. EOF is reported so callers
// stop re-asking; it also ends the prompt's line on out, since no echoed
// Enter will.
func readAnswer(out io.Writer, in *bufio.Reader) (answer string, eof bool) {
	line, err := in.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(line))
	if err != nil {
		fmt.Fprintln(out)
		return answer, true
	}
	return answer, false
}

// reportStatuses says what init is about to do to the board — created vs
// found, never silent (SCOPE invariant 6 discipline: create deliberately,
// touch nothing silently). Existing statuses are annotated with their part
// in the pipeline; created ones are the names the operator just chose.
func reportStatuses(out io.Writer, teamKey string, cfg *config.RepoConfig, existing []string) {
	roles := statusRoles(cfg)
	has := map[string]bool{}
	for _, name := range existing {
		has[name] = true
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
	watched := map[string]bool{}
	for _, q := range cfg.Queues {
		watched[q.Status] = true
	}
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
// "started" category.
//
// "started" is deliberate, not a default: whether a status ends work is a
// fact about the operator's process that queue topology cannot reveal. An
// on_success target no queue watches is just as often a human column
// ("Ready to Merge") as a terminal one, and creating a human column as
// completed silently stops its tickets from blocking their dependents
// (see linear.StateSpec) — work becomes eligible before it is done.
// Created as "started", the failure mode is at least loud: a finished
// blocker that still blocks is something a human notices. reportExits
// turns that residual risk into an explicit instruction.
func stateSpecs(cfg *config.RepoConfig) []linear.StateSpec {
	names := map[string]bool{}
	for _, q := range cfg.Queues {
		names[q.Status] = true
		names[q.OnSuccess] = true
		if q.OnFailure != "" {
			names[q.OnFailure] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	slices.Sort(sorted)
	specs := make([]linear.StateSpec, 0, len(sorted))
	for _, name := range sorted {
		specs = append(specs, linear.StateSpec{Name: name, Type: "started"})
	}
	return specs
}

// pipelineExits are the on_success targets no queue watches: the statuses
// where work leaves the automated path.
func pipelineExits(cfg *config.RepoConfig) []string {
	watched := map[string]bool{}
	for _, q := range cfg.Queues {
		watched[q.Status] = true
	}
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
