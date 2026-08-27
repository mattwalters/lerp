//go:build unix

package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/logfmt"
	"github.com/mattwalters/lerp/internal/run"
	"github.com/mattwalters/lerp/internal/telemetry"
	"github.com/mattwalters/lerp/internal/workspace"
)

// DefaultInterval is how often the reconciler polls when the caller does not
// choose an interval. Polling is the design: no webhooks, and nothing listens
// on a port.
const DefaultInterval = 12 * time.Second

// DefaultResyncEvery is how many delta reads the attention pass makes before
// it re-lists a team's board from scratch. At DefaultInterval that is a full
// re-list about every four minutes, which is the healing rate for every kind
// of drift a delta cannot report — the archived issue no query returns at
// all, and the rest of the catalogue in relistBoard — and the point at which
// a delta that keeps failing falls back to the two listings.
const DefaultResyncEvery = 20

// deltaOverlap is how far back of its own cursor each delta read asks.
//
// Linear answers these reads from replicas that do not all hold the same
// writes: the same listing, run twice a second apart, comes back several rows
// different, and a filter on the last minute of changes can come back empty
// and then full again. The pass that re-listed the whole backlog absorbed
// that by rebuilding from scratch every time — one stale pass, corrected by
// the next. A cursor cannot: a read that returns a recent row while missing
// an older one moves the cursor past the row it missed, and nothing short of
// the periodic re-list would go back for it.
//
// Re-asking for the trailing window costs the rows changed inside it, which
// is a handful on any real team, and buys back the property the full listing
// had — that a row a lagging replica hid arrives on a later pass rather than
// waiting for the resync.
//
// It covers the resync's own listings too, which lag exactly as much and get
// to overwrite the board wholesale. A re-list that came back short drops a
// row; one served from behind hands back a ticket a colleague has since
// claimed, which the delta had already evicted. Both are a stale answer about
// a change made recently — that is what makes a replica disagree in the first
// place — so both are inside this window, and the next pass's delta puts the
// board right rather than the mistake standing until the resync after.
const deltaOverlap = 2 * time.Minute

// EventType names what the reconciler just did.
type EventType string

const (
	// EventProvisioning reports that this process claimed a ticket and is
	// preparing its workspace: the lane is occupied, but no agent runs yet.
	EventProvisioning EventType = "provisioning"
	// EventStarted reports that this process claimed a ticket and started an
	// agent in a lane.
	EventStarted EventType = "started"
	// EventAdopted reports that a run left behind by a previous process is
	// still alive, and now occupies a lane here. The event carries the log
	// path so a subscriber can reattach its tail.
	EventAdopted EventType = "adopted"
	// EventReaped reports that a recorded run's process is dead without
	// having recorded how it ended: its workspace was disposed and its
	// record removed. The ticket is left wherever Linear says it is. A dead
	// run that did record an exit status is settled instead, and reports
	// EventExited.
	EventReaped EventType = "reaped"
	// EventExited reports that a run finished and the queue's move rule was
	// applied — a run this process started, or an adopted run that finished
	// with nobody waiting on it and recorded its own exit status.
	EventExited EventType = "exited"
	// EventEjected reports that the operator took a lane's run over: the
	// agent was killed, the lane is free, and the ticket was left exactly
	// where the run had it — claim, status and workspace all untouched. It
	// is EventExited's opposite number, and the reason the two are separate
	// events: nothing settled here.
	EventEjected EventType = "ejected"
	// EventError reports a pass or a run that failed in a way worth
	// surfacing. The loop keeps ticking; whatever still needs repair is
	// retried on a later pass.
	EventError EventType = "error"
	// EventQueues reports what the pass saw in the configured queues before
	// filling lanes: every ticket sitting in each queue's status, in pickup
	// order, with its eligibility. Emitted once per pass, whether or not any
	// lane was free, so the queue view always mirrors exactly the listing the
	// loop fills from.
	EventQueues EventType = "queues"
	// EventAttention reports what the board says is waiting on the operator,
	// recomputed once per pass. The event carries the whole list — including
	// an empty one, so a subscriber can show the goal state when the last
	// item clears.
	EventAttention EventType = "attention"
	// EventTicked reports that Tick has finished emitting everything this
	// pass will ever emit — fill and attention having both returned, or
	// (LERP-44) the pass having been skipped outright because a rate limit
	// is still in effect. It is always last, over the same channel every
	// other event this pass produced, so channel order — not wall-clock
	// order in some other goroutine — is what guarantees it arrives after
	// them. A subscriber that needs to know "nothing more is coming this
	// pass" (the TUI splash, see model.go) has to learn that from this event
	// rather than from Tick's own return: Tick returning and this event
	// being read off the channel are two different goroutines racing a
	// buffered channel, and the goroutine that only has to return wins that
	// race often enough to matter.
	EventTicked EventType = "ticked"
)

// QueueTicket is one ticket as a queue snapshot saw it, with why it will or
// will not be picked up. An eligible ticket runs, in listed order, as soon as
// a lane is free; the rest are gated by a blocker or an existing claim.
type QueueTicket struct {
	ID         string
	Identifier string // human identifier, e.g. LERP-42
	Title      string
	URL        string // Linear's own web URL for the ticket
	Eligible   bool
	Assigned   bool     // claimed by someone; an assigned ticket is never eligible
	BlockedBy  []string // identifiers of the unfinished blockers, when blocked
}

// QueueSnapshot is one configured queue's listing for one team, tickets in
// pickup order.
type QueueSnapshot struct {
	Team    string
	Name    string // queue name
	Status  string // the queue's Linear status
	Tickets []QueueTicket
}

// StatusRelevance is what the configured pipeline says about the status a
// waiting ticket rests in, derived from lerp.toml and nothing else. It is
// the inbox table's status ordering, and the reason a ticket that left
// the pipeline is worth marking on sight.
type StatusRelevance int

const (
	// StatusUnknown is the zero value: nothing has said what the pipeline
	// makes of this status. attention() sets a real rank on every item, so
	// an item carrying this is a bug — it sorts first and says so, rather
	// than impersonating a failed run the way the zero value used to.
	StatusUnknown StatusRelevance = iota
	// StatusFailed is a status some queue's on_failure points at: a run
	// failed here.
	StatusFailed
	// StatusFinished is an on_success target no queue serves: a run
	// finished here, and the next move is a human's.
	StatusFinished
	// StatusUnnamed is a status the pipeline never names — neither a
	// queue's own status nor any on_success or on_failure target — and that
	// Linear does not file under a waiting category. It is the fingerprint
	// of a ticket that left the pipeline: an external automation moved it,
	// or a human dragged it.
	StatusUnnamed
	// StatusBacklog is a status the pipeline never names either, but one
	// Linear files as somewhere work has not started: the ticket has not
	// entered the pipeline, rather than fallen out of it. It is the
	// ordinary resting place of most of a board, which is why it is not
	// StatusUnnamed — a fingerprint the whole board carries identifies
	// nothing.
	StatusBacklog
	// StatusOther is a status the pipeline serves. A waiting ticket is
	// never in one, since attention lists only unserved statuses; the rank
	// exists so the ordering is total for any status handed to it.
	StatusOther
)

// Note is the short phrase the inbox table prints beside a status
// header, so an ordering derived from config still explains itself.
func (r StatusRelevance) Note() string {
	switch r {
	case StatusFailed:
		return "a run failed here"
	case StatusFinished:
		return "a run finished here"
	case StatusUnnamed:
		return "the pipeline never names it"
	case StatusBacklog:
		return "waiting to enter the pipeline"
	case StatusOther:
		return "a queue serves it"
	}
	return "relevance unknown"
}

// AttentionItem is one ticket waiting on the operator: what it is, why it
// needs a human, and Linear's URL for it. TicketID is Linear's internal id —
// what Promote and MoveIssue take; Ticket is the human identifier shown on
// screen.
type AttentionItem struct {
	Ticket   string // human identifier, e.g. LERP-42
	TicketID string
	Title    string
	Status   string // the real Linear status name, never a synonym
	// Project is Linear's own project for the ticket, empty when it has
	// none — a column on the row and the one thing the table's filter
	// scopes to.
	Project string
	// Relevance is what the pipeline says about Status. Nothing in the loop
	// acts on it; it is carried so the table can order and mark statuses
	// without a second reading of the config.
	Relevance StatusRelevance
	// Claimed is whether the ticket is assigned to the operator rather than
	// waiting unassigned. Nothing in the loop acts on it; it is carried
	// because Relevance cannot answer it — that is derived from Linear's
	// status category alone, so a claimed ticket resting in an intake status
	// reads as StatusBacklog, and only this says the difference between one
	// that has not entered the pipeline and one that fell back out of it and
	// is stranded there while the claim stands.
	Claimed bool
	Reason  string
	URL     string
	// Priority is Linear's own scale: 0 none, 1 urgent, 2 high, 3 medium,
	// 4 low. It is a sort key and a column on the row; nothing in the loop
	// acts on it.
	Priority int
	// BlockedBy names every unfinished ticket holding this one up, listed
	// or not; Blocks names the unfinished tickets it holds up. Unblocks is
	// how many other listed tickets it transitively frees — the leverage of
	// routing it, and the table's default sort key.
	BlockedBy []string
	Blocks    []string
	Unblocks  int
}

// Event is one observation from the loop. Fields beyond Type are filled as
// far as they are known: an adopted or reaped run is known only from its
// local record, which carries the ticket's ID but not its human identifier.
type Event struct {
	Type     EventType
	RunID    string
	Lane     int
	TicketID string
	Ticket   string // human identifier, e.g. LERP-42
	Queue    string
	LogPath  string
	// StartedAt is when the run began, from its record — for an adopted run
	// that is the original start under a previous process, not the adoption.
	// Zero when no run exists yet.
	StartedAt time.Time
	// ExitCode is how the run ended: waited for on the live path, read back
	// from the run's own exit file for an adopted one. EventExited only.
	ExitCode int
	// Note is a remark about how a run settled, for the operator to read:
	// today, the on_success or on_failure hop conclude did not make because
	// the ticket left the queue's status mid-run. Empty when there is
	// nothing to say, which is the happy path. EventExited only.
	Note string
	// Cost is what the run's own log states it cost, in dollars — read here,
	// before the record (and the log with it) is discarded, rather than by a
	// subscriber reacting to this event after the fact: by the time this
	// event reaches one, Evidence.Remove may already have deleted the file.
	// Zero for a runner whose stream never states a cost. EventExited only.
	Cost      float64
	Queues    []QueueSnapshot // meaningful only for EventQueues
	Attention []AttentionItem // EventAttention only: everything waiting on the operator
	Err       error
}

// ReconcilerOptions configures the loop. Client, Repo, RepoDir, Evidence, and
// Lanes are required. Execute, Provision, Dispose, and Alive default to the
// real implementations; they are injectable so the loop can be tested against
// the fake Linear client with stub runners and processes.
type ReconcilerOptions struct {
	Client   linear.Client
	Repo     *config.RepoConfig
	RepoDir  string
	Evidence *evidence.Evidence
	Lanes    int           // N: at most this many agents at once
	Interval time.Duration // polling interval for Run; DefaultInterval when zero
	// ResyncEvery is how many delta reads the attention pass makes per team
	// before re-listing that team's board in full, DefaultResyncEvery when
	// zero. It is a knob for tests and for the pathologically large board,
	// not a lerp.toml setting: nothing an operator configures should be able
	// to decide how stale their inbox may get.
	ResyncEvery int

	// Events, when set, receives every Event the loop emits. It is called
	// from the loop's goroutines and must be safe for concurrent use; a
	// subscriber that can block should hand events off to its own channel.
	Events func(Event)
	// Log receives human-readable diagnostics, including provision, dispose,
	// and runner output from every lane. Lanes run concurrently, so the loop
	// serializes writes itself; any writer will do. May be nil.
	Log io.Writer

	Execute   ExecuteFunc
	Provision ProvisionFunc
	Dispose   DisposeFunc
	Alive     func(evidence.Record) bool
	// Kill signals a run's process group, defaulting to syscall.Kill. It is
	// injectable for the same reason the rest are: a test that needs a kill
	// to fail cannot provoke one for real without picking a victim.
	Kill func(pid int, sig syscall.Signal) error
	// Telemetry receives one Run for every settled run — executeLane and reap
	// call it after their Evidence.Remove succeeds, which is what makes "one
	// line or none, never two" the same guarantee as the record removal
	// itself. Defaults to appending to the local telemetry file (SCOPE.md
	// invariant 1) and logging a write failure without ever failing the run;
	// injectable so loop tests can collect Runs instead of writing them.
	Telemetry func(telemetry.Run)
}

// Reconciler is the loop — there is exactly one. Desired state is the board,
// actual state is the agent processes on this machine; every pass compares
// the two and starts, adopts, or reaps until they match. It does not care who
// moved a ticket: humans, agents, and Linear automations are all legitimate.
//
// The caller owns the clone lock (evidence.AcquireLock); two loops
// reconciling one clone would fight over its lanes.
type Reconciler struct {
	o ReconcilerOptions

	mu     sync.Mutex
	active []*laneRun
	wg     sync.WaitGroup
	// ejected remembers the runs the operator took over, by run ID. Eject
	// removes a run's record and frees its lane, but a pass already holding
	// the listing that named it would otherwise adopt or reap it — and a reap
	// disposes the very workspace eject just handed over. The set is one
	// entry per eject and never consulted after the run is gone from disk.
	ejected map[string]bool

	// boards is what the attention pass reads instead of re-listing the
	// whole backlog every 12 seconds: one cached reading of each team's
	// board, kept current by a delta read per pass. It is owned by the pass
	// — attention is called only from Tick, which is the loop's own
	// goroutine and never runs concurrently with itself — so unlike active
	// and ejected it needs no lock. Nothing durable lives here (SCOPE.md
	// invariant 1): losing it to a restart costs one full re-list.
	boards map[string]*teamBoard

	// resumeAt is when a pass may next ask Linear anything, set by fail when
	// an error chain carries *linear.RateLimitError. It has its own lock
	// rather than sharing mu: fail runs under mu already held when it is
	// reporting ejectLane's own failures (a filesystem error today, never
	// this one, but fail has no way to know that) — sharing mu would
	// deadlock that call the moment it ever did carry one.
	rateLimitMu sync.Mutex
	resumeAt    time.Time
}

// teamBoard is the attention pass's cached reading of one team: the issues
// the inbox could ever draw, and where the delta cursor stands.
//
// issues holds only what passes the inbox's own test — unassigned or the
// operator's, and unfinished — because that is what the two full listings
// return, and the delta's job is to keep this equal to what they would say
// now. An issue that stops passing that test is deleted rather than kept
// with a flag: the pass renders whatever is in here.
type teamBoard struct {
	issues map[string]linear.Issue // by Linear issue ID
	// since is the delta cursor: the newest UpdatedAt this board has seen.
	// It comes from the issues themselves rather than from a clock, so no
	// skew between this machine and Linear can skip an update.
	since time.Time
	// deltas counts the delta reads made since the last full re-list, which
	// is what ResyncEvery is compared against.
	deltas int
}

// laneRun is one occupied lane: either a run this process started or a live
// run adopted from a previous process.
//
// record is the run's evidence as it was last written: for an adopted run
// that is what the previous process left, for a started one what Attach
// returned when the agent's PID first existed. Both kinds carry it because
// eject needs the PID, the workspace and the session id whoever started the
// run recorded.
type laneRun struct {
	lane     int
	ticketID string
	adopted  bool
	record   evidence.Record
	// settling marks a started run whose agent has exited and whose outcome
	// is now being applied to the board. Eject and settling are mutually
	// exclusive, and both are decided under r.mu: without that handshake an
	// eject arriving while conclude was mid-flight would kill nothing, report
	// success, and leave the workspace undisposed and the ticket hopped.
	settling bool
}

// NewReconciler validates o and returns a loop ready to Run or Tick.
func NewReconciler(o ReconcilerOptions) (*Reconciler, error) {
	switch {
	case o.Client == nil:
		return nil, fmt.Errorf("reconciler: client is required")
	case o.Repo == nil:
		return nil, fmt.Errorf("reconciler: repo config is required")
	case o.RepoDir == "":
		return nil, fmt.Errorf("reconciler: repo directory is required")
	case o.Evidence == nil:
		return nil, fmt.Errorf("reconciler: run evidence is required")
	case o.Lanes < 1:
		return nil, fmt.Errorf("reconciler: lanes must be at least 1")
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.ResyncEvery <= 0 {
		o.ResyncEvery = DefaultResyncEvery
	}
	if o.Execute == nil {
		o.Execute = run.Execute
	}
	if o.Provision == nil {
		o.Provision = workspace.Provision
	}
	if o.Dispose == nil {
		o.Dispose = workspace.Dispose
	}
	if o.Alive == nil {
		o.Alive = evidence.Alive
	}
	if o.Kill == nil {
		o.Kill = syscall.Kill
	}
	if o.Log != nil {
		o.Log = &syncWriter{w: o.Log}
	}
	if o.Telemetry == nil {
		log := o.Log
		o.Telemetry = func(run telemetry.Run) {
			if err := telemetry.Append(run); err != nil && log != nil {
				fmt.Fprintf(log, "telemetry: %v\n", err)
			}
		}
	}
	return &Reconciler{o: o, ejected: map[string]bool{}, boards: map[string]*teamBoard{}}, nil
}

// syncWriter serializes the Log writes that arrive concurrently from the tick
// loop, each lane's goroutine, and the subprocesses whose output streams into
// the shared writer.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Run ticks until ctx is cancelled, waits for the runs this process started
// to wind down (cancellation kills their agents, see run.Execute), and
// returns ctx's error. Adopted processes are not children of this process and
// keep running; the next loop adopts them again.
func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.o.Interval)
	defer ticker.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			r.wg.Wait()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Tick runs one reconciliation pass: read the local run evidence, adopt live
// runs and reap dead ones, then start eligible tickets into free lanes.
// Started runs proceed in their own goroutines, so a tick never blocks on an
// agent. A failed pass is reported as an EventError, never returned: the loop
// repairs drift, and a pass that could not finish is just drift for the next
// one. EventTicked always closes the pass, whichever branch above ran or
// failed to — a paused pass included.
//
// A pass still paused from a rate limit skips reconcileEvidence, fill and
// attention entirely — every Linear call a pass could make, not just the
// read that failed — and reports how much longer it will wait instead. That
// is the check here rather than in Run: Run's own ticker is one caller of
// Tick, but production paces it from the TUI instead (tui.Options.Interval
// drives Engine.Tick directly, no daemon behind it), so the gate has to sit
// where every caller passes through, not in the loop only one of them uses.
//
// The pause has no ceiling of its own: Linear's own Retry-After is trusted
// whole, however long, rather than clamped — a cap is retry-strategy
// machinery the ticket asked not to grow. Promote, Eject and ForceStart are
// still live through a long one; only the pass that starts, adopts and
// reaps stands down.
func (r *Reconciler) Tick(ctx context.Context) {
	if wait, ok := r.paused(); ok {
		r.emit(Event{Type: EventError, Err: fmt.Errorf("linear: rate limited, resuming in %s", wait.Round(time.Second))})
		r.emit(Event{Type: EventTicked})
		return
	}
	if r.reconcileEvidence(ctx) {
		r.fill(ctx)
	}
	r.attention(ctx)
	r.emit(Event{Type: EventTicked})
}

// paused reports whether a rate limit set by fail is still in effect. It
// clears resumeAt once the deadline has passed, so a pass need only check
// this — no jitter, no retry count, the one deadline the ticket asked for.
func (r *Reconciler) paused() (time.Duration, bool) {
	r.rateLimitMu.Lock()
	defer r.rateLimitMu.Unlock()
	if r.resumeAt.IsZero() {
		return 0, false
	}
	if wait := time.Until(r.resumeAt); wait > 0 {
		return wait, true
	}
	r.resumeAt = time.Time{}
	return 0, false
}

// pauseFor extends resumeAt to at least the later of d (Linear's own
// Retry-After) and the next interval, so a 429 that carried no usable delay
// still costs one interval's pause rather than none — the polling this
// replaces would have waited that long anyway. It only ever extends: fill
// and attention can each fail with their own RateLimitError in the same
// pass, and a header-less second one must not cut a first one's long wait
// down to one interval.
func (r *Reconciler) pauseFor(d time.Duration) {
	if d < r.o.Interval {
		d = r.o.Interval
	}
	until := time.Now().Add(d)
	r.rateLimitMu.Lock()
	if until.After(r.resumeAt) {
		r.resumeAt = until
	}
	r.rateLimitMu.Unlock()
}

// AttentionDefinition is the operator-facing one-line description of the
// rule attention implements, rendered by the TUI's empty state. It lives
// here, next to the rule, so the two change in the same hunk.
const AttentionDefinition = "unclaimed tickets, and your claimed tickets, sitting in statuses no queue serves"

// attention recomputes what needs the operator and emits it as one
// EventAttention carrying the whole list, in identifier order. Ordering is
// display and belongs to the view that offers the operator a choice of it;
// what the pass owes the table is the facts each row is sorted, grouped and
// filtered by — leverage, priority, project, and what the pipeline says
// about the status.
//
// The v0 definition of the inbox, in full: a ticket needs the operator
// when it sits in a status no queue serves, and is either unassigned —
// nobody has put it on the board yet — or assigned to the operating user,
// resting there deliberately or landed there by a failed run's on_failure
// move. That split is a fact of the two queries and never a heading: the
// status the ticket is actually in says far more about what to do with it.
// Deliberately out of v0: a claimed ticket sitting in a queue status with
// no live run in a lane (a failed run whose queue has no on_failure route).
// From one Linear read that state is indistinguishable from a live run
// under the same user on another machine, so v0 leaves it to the log line
// conclude writes — and, since it sits in a served status, to the row the
// work panel already draws for it, where S takes the claim back (LERP-113). This is a reading of the board plus a place to route
// from, not a catch-all for anything that might want attention; resist
// growing it further.
//
// A pass that could not read every team emits nothing: the failure is
// reported and the subscriber keeps its last full list, because a partial
// one could falsely read as an empty inbox.
//
// What the pass reads is a per-team cache (teamBoard), refreshed by one
// delta query per team per pass and re-listed in full every ResyncEvery
// passes — not the two full backlog listings it used to run, which on an
// established team paged thousands of issues a minute and spent the API
// budget the claims and moves need. The rule the inbox implements is
// unchanged; only where the pass gets the board from is.
func (r *Reconciler) attention(ctx context.Context) {
	viewerID, err := r.o.Client.Viewer(ctx)
	if err != nil {
		r.fail(fmt.Errorf("attention: read viewer: %w", err))
		return
	}
	served := r.o.Repo.WatchedStatuses()
	relevance := statusRelevance(r.o.Repo)
	var items []AttentionItem
	// claimed is how the ticket came to rest here — the difference between
	// the two queries. It reaches the row twice over: as the first word of
	// the sentence explaining it, which is where it belongs, and as a fact
	// the table can read, because a display that has to parse that sentence
	// to know is a display coupled to its wording.
	add := func(issue linear.Issue, claimed bool) {
		if served[issue.Status] {
			return
		}
		claim := "unassigned"
		if claimed {
			claim = "claimed"
		}
		rel := relevance(issue.Status, issue.StatusType)
		items = append(items, AttentionItem{
			Ticket:    issue.Identifier,
			TicketID:  issue.ID,
			Title:     issue.Title,
			Status:    issue.Status,
			Project:   issue.Project,
			Relevance: rel,
			Claimed:   claimed,
			Reason:    fmt.Sprintf("%s in %q — %s", claim, issue.Status, rel.Note()),
			URL:       issue.URL,
			Priority:  issue.Priority,
			BlockedBy: issue.BlockedBy,
			Blocks:    issue.Blocks,
		})
	}
	for _, team := range r.o.Repo.Teams {
		board, err := r.refreshBoard(ctx, team, viewerID)
		if err != nil {
			r.fail(err)
			return
		}
		for _, issue := range board.issues {
			// The board holds only unassigned tickets and the operator's
			// own, so an assignee at all is the operator's claim — the
			// distinction the two listings used to draw by which one an
			// issue arrived in.
			add(issue, issue.AssigneeID != "")
		}
	}
	countUnblocks(items)
	sort.Slice(items, func(i, j int) bool { return items[i].Ticket < items[j].Ticket })
	r.emit(Event{Type: EventAttention, Attention: items})
}

// refreshBoard brings one team's cached board up to date and returns it. It
// re-lists in full when there is nothing to update, when the cursor is
// unusable, or when the delta reads since the last full re-list have reached
// ResyncEvery; otherwise it applies one delta read.
//
// A failed read returns the error and leaves the board exactly as it was —
// same issues, same cursor — so the retry next pass asks the same question
// again. That is the discipline the pass has always had: a read that half
// happened must never reach the inbox, where it would read as tickets having
// gone away. The attempt is still counted, so a delta that keeps failing
// escalates to the re-list rather than retrying forever.
func (r *Reconciler) refreshBoard(ctx context.Context, team, viewerID string) (*teamBoard, error) {
	board := r.boards[team]
	// A zero cursor is not a cheap delta but the most expensive query lerp
	// can send: unfiltered by state, "everything since the beginning" is the
	// team's entire history, completed issues included. It happens when the
	// full listing came back empty — an inbox with nothing in it — and the
	// answer is to keep asking the two listings, which for that board are
	// two empty pages.
	if board == nil || board.since.IsZero() || board.deltas >= r.o.ResyncEvery {
		return r.relistBoard(ctx, team, viewerID)
	}
	// Counted before the read, so failures count too. A delta that keeps
	// failing — a gateway error, a query Linear stopped accepting, or
	// (paced far slower, on its own resumeAt rather than every interval —
	// see Tick) a 429 — would otherwise never let the counter reach
	// ResyncEvery, and the re-list that is this board's only other source
	// would never run again: the inbox would freeze at whatever the cold
	// start saw, for the life of the process, while the pass kept reissuing
	// the same failing read. Escalating to the two listings after
	// ResyncEvery attempts is the fallback that leaves, and they are
	// cheaper than the delta that is failing: filtered by state and
	// assignee, where it is filtered by neither.
	board.deltas++
	// Asked from behind the cursor, never from the cursor itself: see
	// deltaOverlap. The answers are deduped by issue id, so a row this
	// window returns again simply replaces itself.
	delta, err := r.o.Client.ListTeamIssuesUpdatedSince(ctx, team, board.since.Add(-deltaOverlap))
	if err != nil {
		return nil, fmt.Errorf("attention: read changes for team %s: %w", team, err)
	}
	for _, issue := range delta {
		if inboxCanDraw(issue, viewerID) {
			board.issues[issue.ID] = issue
		} else {
			// Finished, or somebody else's now. Either way it has left the
			// inbox, and this arriving is the only notice of that the pass
			// gets — which is why the delta query filters on nothing but the
			// team and the timestamp.
			delete(board.issues, issue.ID)
		}
		if issue.UpdatedAt.After(board.since) {
			board.since = issue.UpdatedAt
		}
	}
	return board, nil
}

// relistBoard rebuilds a team's board from the two full listings — the cold
// start, and the periodic heal. It is what repairs the drift a delta cannot
// report — every kind of change that does not arrive as a row on the issue
// whose cached row is wrong:
//
//   - An archived or deleted ticket changes nothing a delta could return, so
//     without this it would sit in the inbox until lerp restarted.
//   - A row's blocking relations are refreshed only when that row itself is
//     touched, and completing a blocker stamps the blocker. So a ticket can
//     read as blocked by a ticket that is already done, for as long as the
//     resync interval — where the pass that re-listed everything had it right
//     within one tick. It is the price of the delta, it is bounded by
//     ResyncEvery, and it costs a stale mark on a row rather than a wrong
//     move: nothing in the loop acts on these fields (see AttentionItem), and
//     fill still lists every queue fresh, so what a run picks up is
//     unaffected.
//   - A ticket moved from one served team to another leaves by a door the
//     first team's delta cannot see: that query filters on the team, so the
//     row it left behind is never mentioned again. The new team's delta adds
//     it under its new identifier, and until the old team re-lists the inbox
//     shows the same work twice.
//
// This read is no fresher than any other — it is served from the same
// replicas, and can come back short or come back stale (see deltaOverlap,
// which is what puts a board right that it got wrong).
func (r *Reconciler) relistBoard(ctx context.Context, team, viewerID string) (*teamBoard, error) {
	unassigned, err := r.o.Client.ListUnassignedIssues(ctx, team)
	if err != nil {
		return nil, fmt.Errorf("attention: list unassigned tickets for team %s: %w", team, err)
	}
	assigned, err := r.o.Client.ListAssignedIssues(ctx, team, viewerID)
	if err != nil {
		return nil, fmt.Errorf("attention: list claimed tickets for team %s: %w", team, err)
	}
	board := &teamBoard{issues: make(map[string]linear.Issue, len(unassigned)+len(assigned))}
	// The cursor only ever moves forward, across a re-list included. Taking
	// it from these rows alone would walk it backwards on every resync — the
	// listings return what the inbox can draw, which on a team that churns on
	// other people's tickets is older than the last thing the delta saw — and
	// the next delta would then ask for every issue the team touched in
	// between, completed ones included. That is the paging this exists to
	// stop, arriving once every resync interval.
	if prev := r.boards[team]; prev != nil {
		board.since = prev.since
	}
	// Keyed by issue id, which also settles the case where the two listings
	// disagree — a ticket claimed between the two reads, or the same ticket
	// served from two replicas that do not agree about its assignee, comes
	// back in both and is one row here.
	for _, issue := range append(unassigned, assigned...) {
		board.issues[issue.ID] = issue
		// The cursor is the newest thing the listing saw, never the local
		// clock: asked with gte, it re-reads the boundary issue on the next
		// pass, which costs nothing, where a clock running fast would skip
		// whatever changed in the gap — and would do it silently, since
		// nothing here can tell a quiet board from a cursor set past the end
		// of one.
		//
		// The price is at cold start, where there is no previous cursor to
		// raise it: these rows are the ones the inbox can draw, so a board
		// whose tickets have all sat still for a fortnight seeds a fortnight
		// old cursor, and the first delta pages the team's whole churn since.
		// That is one catch-up read per process — the delta that follows it
		// starts from whatever it saw, which is now — and it is the same
		// order of cost as the cold-start listing beside it.
		if issue.UpdatedAt.After(board.since) {
			board.since = issue.UpdatedAt
		}
	}
	r.boards[team] = board
	return board, nil
}

// inboxCanDraw is the test the two full listings apply server-side, applied
// here to a delta row that arrives with no filtering at all: a ticket the
// inbox could show is unfinished, and either unclaimed or claimed by the
// operator. Someone else's ticket, and a finished one, wait on nobody.
func inboxCanDraw(issue linear.Issue, viewerID string) bool {
	if issue.StatusType == linear.CategoryCompleted || issue.StatusType == linear.CategoryCanceled {
		return false
	}
	return issue.AssigneeID == "" || issue.AssigneeID == viewerID
}

// countUnblocks fills in Unblocks: how many other items each one transitively
// frees. The graph is the listed set and nothing else — a ticket blocked by
// something outside the listing is still blocked, but a ticket blocking
// something outside it gets no credit for work this list cannot show. Both
// directions of the relation contribute the same edge, so an item whose own
// relations were trimmed still counts through its blocked neighbours.
func countUnblocks(items []AttentionItem) {
	listed := make(map[string]bool, len(items))
	for _, it := range items {
		listed[it.Ticket] = true
	}
	blocks := make(map[string]map[string]bool, len(items))
	edge := func(from, to string) {
		if from == to || !listed[from] || !listed[to] {
			return
		}
		if blocks[from] == nil {
			blocks[from] = map[string]bool{}
		}
		blocks[from][to] = true
	}
	for _, it := range items {
		for _, downstream := range it.Blocks {
			edge(it.Ticket, downstream)
		}
		for _, blocker := range it.BlockedBy {
			edge(blocker, it.Ticket)
		}
	}
	for i, it := range items {
		// Depth-first from the ticket, counting what it reaches. Marking on
		// the way in keeps a cycle — Linear allows one — from looping here.
		seen := map[string]bool{it.Ticket: true}
		stack := []string{it.Ticket}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for next := range blocks[cur] {
				if !seen[next] {
					seen[next] = true
					stack = append(stack, next)
				}
			}
		}
		items[i].Unblocks = len(seen) - 1
	}
}

// Promote is the TUI's write action: move a selected ticket into a status,
// then settle the claim by the same rule conclude uses. A ticket parked in a
// status no queue serves keeps its claim — that is what parks it on the
// operator — so promoting it back into a queue has to release that claim, or
// the ticket lands in the queue's status still assigned, where Eligible
// skips it on every pass. No run, no error, nothing: the promote would look
// like it worked and do nothing at all.
//
// Promoting into a status no queue serves is the other half, and leaves the
// claim exactly where it is: that is how a ticket gets parked deliberately.
//
// Unlike conclude, which releases a claim it just won and ran, promote acts
// on a ticket the operator merely selected — so the release goes through
// releaseClaim, which verifies the assignee is the operating user first and
// leaves a colleague's claim alone.
func (r *Reconciler) Promote(ctx context.Context, ticketID, status string) error {
	if err := r.o.Client.MoveIssue(ctx, ticketID, status); err != nil {
		return err
	}
	if !r.o.Repo.WatchedStatuses()[status] {
		return nil
	}
	viewerID, err := r.o.Client.Viewer(ctx)
	if err != nil {
		return fmt.Errorf("promote %s: read viewer: %w", ticketID, err)
	}
	if err := releaseClaim(ctx, r.o.Client, ticketID, viewerID); err != nil {
		return fmt.Errorf("promote %s: %w", ticketID, err)
	}
	return nil
}

// Ejection is what eject hands back: the run it stopped, and the command
// that reopens that run as the operator's own interactive session.
type Ejection struct {
	Ticket    string // human identifier, e.g. LERP-42
	TicketID  string
	Lane      int
	Workspace string
	Resume    string
}

// Eject is the escape hatch (SCOPE, "The interface"): kill the lane's agent,
// free the lane, drop the run record, and hand back the runner's own resume
// command. The headless run becomes the operator's session.
//
// Nothing here touches Linear — the ticket keeps its claim and its status,
// because ejecting is taking the work over, not abandoning it. That is also
// why the workspace is not disposed: it is the operator's now, and the git
// worktree in it is theirs to remove when they are done. The context is taken
// for symmetry with the TUI's other actions and deliberately unused; a local
// kill needs nothing to wait on.
//
// Every refusal happens before anything changes: a ticket in no lane, a run
// with no agent yet or no recorded session, and a queue whose runner has no
// resume template all return an error having killed nothing. The error text
// is what the TUI shows as the reason.
func (r *Reconciler) Eject(_ context.Context, ticketID string) (Ejection, error) {
	ej, record, adopted, err := r.ejectLane(ticketID)
	if err != nil {
		return Ejection{}, err
	}
	if err := r.o.Evidence.Remove(record.RunID); err != nil {
		// The agent is already dead and the lane already free, so this is not
		// a failed eject: it is a stale run directory, which the next pass
		// reads as a dead run and reaps. Report it and hand over the command.
		r.fail(fmt.Errorf("eject %s: %w", ej.Ticket, err))
	}
	// The resume command is the one string this whole action exists to
	// produce, and the TUI holding it is one keystroke from being closed.
	if r.o.Log != nil {
		fmt.Fprintf(r.o.Log, "ejected run %s on lane %d, workspace %s left in place, resume with: %s\n",
			record.RunID, ej.Lane, ej.Workspace, ej.Resume)
	}
	if adopted {
		// A run this process started emits its own EventEjected as its
		// goroutine winds down, after runLane has freed the lane. An adopted
		// run has no goroutine: forgetting its lane entry above was the whole
		// of its ending, so the event belongs here.
		r.emit(Event{Type: EventEjected, RunID: record.RunID, Lane: ej.Lane,
			TicketID: ej.TicketID, Ticket: ej.Ticket, Queue: record.Queue,
			LogPath: record.LogPath})
	}
	return ej, nil
}

// ejectLane is eject's locked half, in the order that makes every failure
// harmless: refuse, kill, mark, disown, and free an adopted run's lane.
//
// Everything that can refuse refuses first, while nothing has changed — a
// failed kill included, which is why the kill comes before any bookkeeping.
// Past the kill nothing may refuse, because the agent is dead and the run is
// the operator's whatever the disk says next: the mark goes up, and the
// record is disowned so that even a lerp dying in the next instruction leaves
// a record that disposes nothing and settles nothing.
//
// The recorded PID is the wrapper shell that leads the run's process group,
// so the negated PID kills the agent and everything it spawned — the same
// signal Execute's own cancel and the PID-attach failure path send. It is
// checked against the recorded process start time first (Alive), and refused
// outright when there is no start time to check against: a stale PID may
// since have been reused, and signalling a process group lerp cannot identify
// would kill whatever the operator is running in it.
func (r *Reconciler) ejectLane(ticketID string) (Ejection, evidence.Record, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var lr *laneRun
	for _, other := range r.active {
		if other.ticketID == ticketID {
			lr = other
			break
		}
	}
	fail := func(format string, args ...any) (Ejection, evidence.Record, bool, error) {
		return Ejection{}, evidence.Record{}, false, fmt.Errorf(format, args...)
	}
	if lr == nil {
		return fail("no lane is running %s", ticketID)
	}
	// A map read with no side effects, so having it ahead of the switch below
	// does not disturb "everything that can refuse refuses first, while
	// nothing has changed" — it only lets the SessionID case ask the
	// question that decides whether an empty one is a defect at all.
	runner := r.runnerFor(lr.record.Queue)
	switch {
	// PID 1 is not a run: it is every process this user may signal, since
	// the kill below negates it.
	case lr.record.PID <= 1:
		return fail("%s has no agent to eject yet", ticketID)
	case r.ejected[lr.record.RunID]:
		// Already the operator's. A started run keeps its lane entry until
		// its goroutine winds down, and its killed agent is an unreaped
		// zombie for that moment — alive enough to pass every check below and
		// be ejected a second time.
		return fail("%s has already been ejected", ticketID)
	case lr.settling:
		// The agent has already exited and its run is being concluded. There
		// is no session left to take over, and pretending otherwise would
		// hand back a resume command for a dead session while the ticket
		// hops behind the operator's back.
		return fail("%s has already finished; its run is being settled", ticketID)
	case lr.record.SessionID == "" && run.OpensSession(runner):
		// An empty SessionID is only a defect for a runner that was supposed
		// to have minted one. A vendor that names its own session instead
		// (run.CapturesSession) never had one to record here — its session is
		// resolved from the log below, by ResumeCommand.
		return fail("%s recorded no session to resume", ticketID)
	case lr.record.ProcessStartedUnix == 0:
		// Alive falls back to a bare existence check when no start time could
		// be recorded, which is optimism the other readers can afford and
		// this one cannot: eject is the only caller that signals, and a PID
		// it cannot identify is a process group it must not kill.
		return fail("lerp cannot confirm which process is running %s, so it will not signal it", ticketID)
	case !r.o.Alive(lr.record):
		return fail("the agent for %s is no longer running", ticketID)
	}
	record := lr.record
	resume, err := run.ResumeCommand(runner, record)
	if err != nil {
		// The died-early case for a vendor that names its own session: the
		// log never said, so there is nothing to hand over. Refusing here,
		// before the kill, is what keeps that run from being killed for a
		// resume command that could not be produced.
		return fail("%s: %w", ticketID, err)
	}
	if resume == "" {
		return fail("the runner for queue %q has no resume command", record.Queue)
	}

	// The kill comes first, and a failed one changes nothing at all — not the
	// mark, not the record, not the lane. Including ESRCH: the agent died in
	// the moment between the liveness check and here, so there was never a
	// session to hand over, and the run is left to end the way it was
	// already ending.
	if err := r.o.Kill(-record.PID, syscall.SIGKILL); err != nil {
		return fail("killing %s: %w", ticketID, err)
	}
	// Past the kill nothing may refuse: the agent is dead, so this run is the
	// operator's whatever the disk says next. The mark is what tells the rest
	// of this process so, and it is set under the same lock the kill happened
	// under — every reader of it (adopt, reap, beginSettling) takes that lock,
	// so no pass and no winding-down goroutine can see the death without also
	// seeing the mark.
	r.ejected[record.RunID] = true
	if err := r.o.Evidence.Disown(record.RunID); err != nil {
		// Best effort, and reported rather than returned: the eject happened.
		// The mark keeps this process off the record; a successor that finds
		// it still owning a workspace and a ticket is the one case where a
		// leftover reap could still dispose an ejected workspace, and it
		// needs both this write and the removal below to have failed.
		r.fail(fmt.Errorf("eject %s: %w", ticketID, err))
	}
	if lr.adopted {
		// The lane is freed here for an adopted run, which nothing else is
		// waiting on; a started one is unregistered by its own runLane.
		r.forgetLocked(record.RunID)
	}
	return Ejection{Ticket: record.Ticket, TicketID: ticketID, Lane: record.Lane,
		Workspace: record.Workspace, Resume: resume}, record, lr.adopted, nil
}

// CanEject reports whether a run in this queue could be ejected at all, and
// why not when it could not. It answers for the queue rather than for a run,
// because that is the part the TUI has to know before the operator presses
// the key — a greyed-out key with a reason beats one that fails on press.
func (r *Reconciler) CanEject(queue string) (bool, string) {
	q, ok := r.o.Repo.Queues[queue]
	if !ok {
		return false, fmt.Sprintf("queue %q is not configured", queue)
	}
	runner := r.o.Repo.Runners[q.Runner]
	switch {
	case runner.Resume == "":
		return false, fmt.Sprintf("runner %q has no resume command", q.Runner)
	case !run.OpensSession(runner) && !run.CapturesSession(runner):
		// A resume template over a command that neither asks for {{session}}
		// nor names its own is a resume nobody can use: lerp has no session,
		// minted or captured, to hand back. Answering yes here would offer
		// the key on every such run and fail only once the operator had
		// confirmed it. This is honest but imperfect for a run.CapturesSession
		// queue: the key is offered per queue, and a press can still fail on a
		// run that died before its first event — the resolver's refusal is
		// what catches that one.
		return false, fmt.Sprintf("runner %q does not use {{session}}, so lerp has no session to resume", q.Runner)
	}
	return true, ""
}

// runnerFor is the runner a queue's runs use, zero when either the queue or
// its runner has since left the config — a zero runner has no resume
// template, which is the refusal the caller wants anyway.
func (r *Reconciler) runnerFor(queue string) config.Runner {
	return r.o.Repo.Runners[r.o.Repo.Queues[queue].Runner]
}

// wasEjected reports whether the operator took this run over.
func (r *Reconciler) wasEjected(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ejected[runID]
}

// ForceStart runs one selected ticket now, past the lane limit. It overrides
// exactly one thing — the lane count — and nothing else that gates a pickup:
// the claim protocol still runs in full (invariant 4), a ticket with an
// unfinished blocker is still refused, and a ticket somebody else holds is
// still left alone. Capacity is the operator's call; sequencing is not.
//
// The lane budget is already soft in the way this needs — adopted runs from a
// previous process routinely sit above N (see freeLanes) — so a forced run is
// an ordinary run on an out-of-range lane number, not a new kind of thing.
// Nothing downstream can tell it apart: same evidence record, same adoption,
// same reaping, same conclude.
//
// It returns once the run is launched; the refusals are what the operator
// waits on. A claim lost in the race window after that resolves exactly as
// fill's does — record removed, no run, no event, and the row simply never
// starts.
func (r *Reconciler) ForceStart(ctx context.Context, ticketID string) error {
	issue, err := r.o.Client.GetIssue(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("force-start %s: %w", ticketID, err)
	}
	name, queue, ok := queueForStatus(r.o.Repo, issue.Status)
	if !ok {
		return fmt.Errorf("force-start %s: no queue serves %q", issue.Identifier, issue.Status)
	}
	if issue.Blocked {
		return fmt.Errorf("force-start %s: blocked by %s", issue.Identifier,
			strings.Join(issue.BlockedBy, ", "))
	}
	if issue.AssigneeID != "" {
		// Somebody else's claim is refused — that is Eligible's rule, and
		// force-start overrides the lane count alone. The operating user's own
		// claim is not: a ticket claimed in a served status with no live run
		// is reachable by nothing else lerp has, and taking back your own
		// claim is the only way off the board's one dead end (LERP-113). The
		// claim protocol still runs; it simply reads the assignment back as
		// already yours.
		//
		// The state this cannot tell apart is a live run under the same user
		// on another machine, which one Linear read never could. Bounding it
		// is the keystroke itself: force-start is a human looking at a row and
		// asking for it, never a pass deciding on its own, and the worst case
		// is invariant 4's declared one — duplicated compute.
		viewerID, err := r.o.Client.Viewer(ctx)
		if err != nil {
			return fmt.Errorf("force-start %s: read viewer: %w", issue.Identifier, err)
		}
		if issue.AssigneeID != viewerID {
			return fmt.Errorf("force-start %s: claimed by someone else", issue.Identifier)
		}
	}
	// The refusals above each name their cause, which is what an operator
	// pressing a key needs; Eligible is the rule they spell out. This backstop
	// is what keeps them from drifting apart — a criterion added to Eligible
	// gates fill and would otherwise leave force-start running a ticket the
	// loop refuses, which is not overriding the lane count. Only the operating
	// user's own claim is allowed to differ, and by here that is the only
	// claim left standing, so the backstop reads the issue as releasing it
	// would leave it.
	unclaimed := issue
	unclaimed.AssigneeID = ""
	if !Eligible(unclaimed, map[string]bool{queue.Status: true}) {
		return fmt.Errorf("force-start %s: not eligible", issue.Identifier)
	}
	reserved, err := r.recordedLanes()
	if err != nil {
		return fmt.Errorf("force-start %s: %w", issue.Identifier, err)
	}
	lr, ok := r.registerForce(issue.ID, reserved)
	if !ok {
		return fmt.Errorf("force-start %s: already running here", issue.Identifier)
	}
	r.wg.Add(1)
	go r.runLane(ctx, lr, candidate{issue: issue, name: name, queue: queue})
	return nil
}

// recordedLanes is the set of lane numbers live run records hold, whether or
// not this process has adopted them yet. Every other lane number is chosen on
// the tick goroutine, after reconcileEvidence has adopted every live run it
// can see; force-start chooses between passes, so it reads the same evidence
// itself. Without this, pressing S while a pass is partway through adopting
// an orphan takes the lane that orphan is already running on, and two live
// runs share the LERP_LANE a project's provision isolates on.
//
// A record another lerp writes in the window after this read is still
// possible, exactly as it is for fill: lane numbers are not coordinated
// across processes, and this does not pretend otherwise.
//
// Evidence that cannot be read is a refusal, not an empty answer. It is the
// same state reconcileEvidence bails on — live orphans may hold lanes this
// process knows nothing about — and the pass that bailed left r.active empty
// too, so an empty answer here would start the forced run on lane 1 on top
// of whatever is already there.
func (r *Reconciler) recordedLanes() (map[int]bool, error) {
	records, err := r.o.Evidence.List()
	if err != nil {
		return nil, fmt.Errorf("list run evidence: %w", err)
	}
	taken := make(map[int]bool, len(records))
	for _, record := range records {
		if r.o.Alive(record) {
			taken[record.Lane] = true
		}
	}
	return taken, nil
}

// queueForStatus finds the queue that picks up from status. RepoConfig's
// validation refuses two queues watching one status, so the answer is
// unambiguous whenever there is one.
func queueForStatus(repo *config.RepoConfig, status string) (string, config.Queue, bool) {
	for name, q := range repo.Queues {
		if q.Status == status {
			return name, q, true
		}
	}
	return "", config.Queue{}, false
}

// IssueDetail reads the selected ticket's body and its comments for the
// TUI's inbox pane — the read SCOPE's "not a Linear client" bullet
// licenses. Like Promote it is a passthrough to the client, touching no
// lane, claim, or evidence; unlike Promote it writes nothing at all.
// Nothing in a pass calls it: attention() lists boards, and a per-ticket
// comment query in there would be N extra reads every interval for tickets
// nobody selected.
func (r *Reconciler) IssueDetail(ctx context.Context, ticketID string) (linear.IssueDetail, error) {
	return r.o.Client.GetIssueDetail(ctx, ticketID)
}

// reconcileEvidence converges the lanes with .lerp/runs: every record either
// belongs to a run this process is settling itself, or names a live process
// to adopt, or is the residue of a dead one to reap. It reports whether the
// evidence could be read at all: when it could not, live orphans may occupy
// lanes this pass knows nothing about, so the caller must not fill.
func (r *Reconciler) reconcileEvidence(ctx context.Context) bool {
	records, err := r.o.Evidence.List()
	if err != nil {
		r.fail(fmt.Errorf("list run evidence: %w", err))
		return false
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		seen[record.RunID] = true
		if r.wasEjected(record.RunID) {
			// The operator has taken this run over: its record is being
			// removed and its lane is free. Adopting it would put a dead run
			// back in a lane, and reaping it would dispose the workspace
			// eject just handed over — so a listing taken before the eject
			// must not act on it.
			continue
		}
		if r.ownsTicket(record.TicketID) {
			// A run this process started; its own goroutine settles the
			// ticket and the record when the agent exits.
			continue
		}
		if r.o.Alive(record) {
			r.adopt(record)
			continue
		}
		// The listing is a snapshot: a run this process was settling may have
		// concluded — record removed, lane unregistered — since it was taken.
		// Re-check the disk before treating the record as a dead orphan, or a
		// just-settled run would be reaped and releaseDead could unassign a
		// claim conclude deliberately held.
		if _, err := r.o.Evidence.Read(record.RunID); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if r.reap(ctx, record) {
			r.forget(record.RunID)
		}
	}
	// An adopted run whose record was deleted out from under it — local
	// evidence is disposable — is still a live process occupying a lane. Keep
	// watching the remembered record, and reap from it when the process dies.
	// The remembered record is the run's last trace, so it is forgotten only
	// once the reap finishes; dropping it first would leave a failed reap
	// nothing to retry from, leaking the dead run's claim.
	for _, record := range r.adoptedRecords() {
		if !seen[record.RunID] && !r.o.Alive(record) {
			if r.reap(ctx, record) {
				r.forget(record.RunID)
			}
		}
	}
	return true
}

// adopt takes ownership of a previous process's live run: the lane is marked
// occupied and the run's log path is announced so a subscriber can reattach
// its tail. The agent itself is untouched — adopting is remembering, not
// restarting.
func (r *Reconciler) adopt(record evidence.Record) {
	r.mu.Lock()
	if r.ejected[record.RunID] {
		// The operator took this run over while the pass was reading the
		// board. Adopting it would put a dead run back in a lane, and the
		// pass after that would reap it — disposing the workspace eject has
		// already handed over. The check is here, under the lock, because the
		// one in reconcileEvidence was made before Alive was consulted.
		r.mu.Unlock()
		return
	}
	for _, lr := range r.active {
		if lr.adopted && lr.record.RunID == record.RunID {
			r.mu.Unlock()
			return
		}
	}
	r.active = append(r.active, &laneRun{
		lane:     record.Lane,
		ticketID: record.TicketID,
		adopted:  true,
		record:   record,
	})
	r.mu.Unlock()
	// The work panel draws an adopted run exactly as it draws one this
	// process started, so the log is the only place the takeover is recorded.
	// It is worth recording: an adopted run concludes from its exit file, so
	// a torn or missing one is the one case where a finished run releases its
	// claim without hopping, and this line is what says afterwards that the
	// run had been taken over at all. The start time comes from the record,
	// which an older lerp may have written without one.
	if r.o.Log != nil {
		when := "start time unrecorded"
		if !record.StartedAt.IsZero() {
			when = "started " + record.StartedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(r.o.Log, "adopted run %s on lane %d, %s\n", record.RunID, record.Lane, when)
	}
	r.emit(Event{Type: EventAdopted, RunID: record.RunID, Lane: record.Lane,
		TicketID: record.TicketID, Queue: record.Queue, LogPath: record.LogPath,
		StartedAt: record.StartedAt})
}

// reapDisposeTimeout bounds every dispose command the loop runs. Reaps run on
// the tick loop itself, so a dispose that hangs would otherwise stall every
// later pass — and shutdown — indefinitely; on the normal exit path it would
// wedge the lane, its terminal event never emitted.
const reapDisposeTimeout = 2 * time.Minute

// disposeBounded runs the configured dispose command bounded by
// reapDisposeTimeout, detached from ctx's cancellation so that a loop or lane
// shutting down still finishes cleaning up its workspace.
func (r *Reconciler) disposeBounded(ctx context.Context, id workspace.Identity) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reapDisposeTimeout)
	defer cancel()
	r.o.Dispose(dctx, r.o.RepoDir, r.o.Repo.Dispose, id, r.o.Log)
}

// reap cleans up after a run whose process is dead: dispose the workspace,
// settle the ticket, and remove the local record.
//
// It reports whether the reap finished. One that could not keeps the record,
// so the next pass retries it.
func (r *Reconciler) reap(ctx context.Context, record evidence.Record) bool {
	if r.wasEjected(record.RunID) {
		// Not this loop's run any more: the operator took it over, and the
		// workspace and the claim are theirs. Reported as reaped so the
		// caller drops the lane entry, having cleaned up nothing.
		return true
	}
	// The process is gone, so the workspace is garbage whether or not the
	// provision command ever finished building it. Dispose reports its own
	// failures to the log and never blocks the reap. A record with no
	// workspace has none to dispose: that is a run this loop disowned on its
	// way out (see evidence.Disown), and running the dispose command on an
	// empty path would be a command nobody asked for.
	if record.Workspace != "" {
		id := workspace.Identity{Lane: record.Lane, TicketID: record.TicketID, Workspace: record.Workspace}
		r.disposeBounded(ctx, id)
	}
	ev, tel, ok := r.settleDead(ctx, record)
	if !ok {
		return false
	}
	if err := r.o.Evidence.Remove(record.RunID); err != nil {
		r.fail(fmt.Errorf("reap run %s: %w", record.RunID, err))
		return false
	}
	if tel != nil {
		r.o.Telemetry(*tel)
	}
	r.emit(ev)
	return true
}

// settleDead decides what a dead run's ticket is owed, and returns the
// terminal event that says so. It reports false when the board could not be
// settled, so the caller keeps the record and the next pass retries.
//
// A run that recorded its own exit status really finished, and gets the
// queue's move rule exactly as a live run would — adoption that only ever
// remembered would silently cost a whole stage every time lerp restarted
// across a run's finish. Anything else — no file, a torn one, a run killed
// before it could write — falls back to releasing the claim and leaving the
// status alone: a crash is drift, and re-running the stage repairs it (SCOPE
// invariant 3). Retrying is safe either way, since conclude only moves a
// ticket still sitting in the queue's status and only releases a claim still
// held by this user.
//
// "Exactly as a live run would" includes the one case where it differs from
// the old reap: a failed run whose queue has no on_failure route keeps its
// claim and stays put, rather than being released back into the queue. That
// is conclude's deliberate rule — releasing a ticket that fails every time
// would re-run it on every pass forever — and a run that really failed is the
// case it was written for. The ticket then rests claimed in a served status,
// which the inbox does not list (see attention); that gap is the one a live
// failed run has always had, not a new one.
// settleDead also returns the telemetry.Run reap writes once it has removed
// the record — one line for every settled run, whichever of the branches
// below decided it, all built by the one shared assembly helper in
// telemetry.go. It is nil for a record whose agent never ran: a claim
// protocol failure keeps a record with no PID ever attached, and eject
// disowns a record by blanking its TicketID — both land in the fallback
// branch below alongside a genuinely killed run, and neither is a run
// telemetry has anything to measure.
func (r *Reconciler) settleDead(ctx context.Context, record evidence.Record) (Event, *telemetry.Run, bool) {
	reaped := Event{Type: EventReaped, RunID: record.RunID, Lane: record.Lane,
		TicketID: record.TicketID, Queue: record.Queue, LogPath: record.LogPath}
	code, recorded := evidence.ExitStatus(record)
	at, durationMS := exitTiming(record, recorded)
	var exitCode *int
	if recorded {
		c := code
		exitCode = &c
	}

	// The queue the run started from may have been renamed, removed, or
	// pointed at a different status in lerp.toml since it started. Only a
	// queue still serving the status this run picked its ticket up from has a
	// move rule that means anything here: concluding against a queue whose
	// status has moved on would report a hop nobody skipped, and would judge
	// the claim by a served set the run never ran under.
	queue, configured := r.o.Repo.Queues[record.Queue]
	if !recorded || !configured || queue.Status != record.StartingStatus || record.TicketID == "" {
		if err := r.releaseDead(ctx, record); err != nil {
			r.fail(fmt.Errorf("reap run %s: %w", record.RunID, err))
			return Event{}, nil, false
		}
		if record.PID == 0 || record.TicketID == "" {
			// PID 0: Attach never ran, so no agent ever started — a claim
			// protocol failure kept this record for exactly this repair.
			// Blank TicketID: eject's Disown left this behind, and eject
			// already owns not reporting anything for it.
			return reaped, nil, true
		}
		// No move rule ran, so there is nothing to say about where the ticket
		// rested: status is absent.
		tel := buildTelemetryRun(r.o.Repo, r.o.RepoDir, record, record.Ticket, record.Queue, at, durationMS, exitCode, "")
		return reaped, &tel, true
	}

	issue, err := r.o.Client.GetIssue(ctx, record.TicketID)
	if errors.Is(err, linear.ErrNotFound) {
		// The ticket itself is gone; nothing to settle, and so nothing to
		// report as its resting status.
		tel := buildTelemetryRun(r.o.Repo, r.o.RepoDir, record, record.Ticket, record.Queue, at, durationMS, exitCode, "")
		return reaped, &tel, true
	}
	if err != nil {
		r.fail(fmt.Errorf("reap run %s: read ticket %s: %w", record.RunID, record.TicketID, err))
		return Event{}, nil, false
	}
	viewerID, err := r.o.Client.Viewer(ctx)
	if err != nil {
		r.fail(fmt.Errorf("reap run %s: read viewer: %w", record.RunID, err))
		return Event{}, nil, false
	}
	note, final, moveErr := conclude(ctx, r.o.Client, issue, queue, r.o.Repo, code, viewerID, r.o.Log)
	if moveErr != nil {
		r.fail(fmt.Errorf("reap run %s: %w", record.RunID, moveErr))
		return Event{}, nil, false
	}
	tel := buildTelemetryRun(r.o.Repo, r.o.RepoDir, record, issue.Identifier, record.Queue, at, durationMS, exitCode, final)
	return Event{Type: EventExited, RunID: record.RunID, Lane: record.Lane,
		TicketID: record.TicketID, Ticket: issue.Identifier, Queue: record.Queue,
		LogPath: record.LogPath, ExitCode: code, Note: note, Cost: runCost(record.LogPath)}, &tel, true
}

// releaseDead releases the claim of a dead run that never said how it ended,
// so its ticket becomes eligible again — but only when the board still looks
// exactly as the run left it:
// same status the run started from, still assigned to this operating user.
// Anything else means a human, an agent, or an automation acted since, and
// the loop leaves their work alone.
//
// The assignee half of that is releaseClaim's rule, and this deliberately
// does not route through it: both halves are read from one GetIssue here,
// and a ticket deleted since the run started is a no-op rather than the
// error a second read would raise.
func (r *Reconciler) releaseDead(ctx context.Context, record evidence.Record) error {
	if record.TicketID == "" {
		return nil
	}
	issue, err := r.o.Client.GetIssue(ctx, record.TicketID)
	if errors.Is(err, linear.ErrNotFound) {
		return nil // the ticket itself is gone; nothing to release
	}
	if err != nil {
		return fmt.Errorf("read ticket %s: %w", record.TicketID, err)
	}
	if issue.Status != record.StartingStatus {
		return nil
	}
	viewerID, err := r.o.Client.Viewer(ctx)
	if err != nil {
		return fmt.Errorf("read viewer: %w", err)
	}
	if issue.AssigneeID != viewerID {
		return nil
	}
	if err := r.o.Client.UnassignIssue(ctx, record.TicketID); err != nil {
		return fmt.Errorf("release ticket %s: %w", record.TicketID, err)
	}
	return nil
}

// fill lists the configured queues, publishes what it saw, and starts
// eligible tickets into free lanes, up to N agents at once. The listing
// happens even when every lane is full: the queue view promises to show, on
// every pass, exactly what the loop would pick next, and the only listing
// that keeps that literally true is the loop's own. The cost is the same
// ListIssues calls a filling pass makes — polling is the design, and no new
// Linear API surface is involved.
func (r *Reconciler) fill(ctx context.Context) {
	listings, err := listQueues(ctx, r.o.Client, r.o.Repo)
	if err != nil {
		// Partial listings still fill lanes: one broken queue must not starve
		// the others while its outage lasts. The failure is reported and the
		// broken queue retried next pass.
		r.fail(err)
	}
	r.emit(Event{Type: EventQueues, Queues: snapshotQueues(listings)})
	for _, c := range candidatesFrom(listings) {
		// The free lanes are read per candidate rather than once for the
		// pass: register occupies its lane before this loop comes round, and
		// a force-start can take one mid-pass, which a snapshot taken up
		// front would keep handing out.
		lanes := r.freeLanes()
		if len(lanes) == 0 {
			return
		}
		lr, ok := r.register(lanes[0], c.issue.ID)
		if !ok {
			// This ticket is already occupying a lane — typically a run whose
			// claim Linear had not reflected when the board was listed — or a
			// force-start took this lane number in the window since the read
			// just above. Either way the next candidate reads the lanes
			// again, so one refusal costs one candidate, not the pass.
			continue
		}
		r.wg.Add(1)
		go r.runLane(ctx, lr, c)
	}
}

// snapshotQueues converts raw listings into the queue event's payload,
// computing per ticket why it will or will not be picked up — the same
// Eligible check, on the same issues, that candidatesFrom applies.
func snapshotQueues(listings []queueListing) []QueueSnapshot {
	snaps := make([]QueueSnapshot, 0, len(listings))
	for _, l := range listings {
		snap := QueueSnapshot{Team: l.team, Name: l.name, Status: l.queue.Status}
		for _, issue := range l.issues {
			snap.Tickets = append(snap.Tickets, QueueTicket{
				ID:         issue.ID,
				Identifier: issue.Identifier,
				Title:      issue.Title,
				URL:        issue.URL,
				Eligible:   Eligible(issue, map[string]bool{l.queue.Status: true}),
				Assigned:   issue.AssigneeID != "",
				BlockedBy:  issue.BlockedBy,
			})
		}
		snaps = append(snaps, snap)
	}
	return snaps
}

// runLane settles one claimed lane from start to finish, then frees the lane
// before emitting the terminal event: a subscriber reacting to the event must
// find the lane free and the ticket settled.
func (r *Reconciler) runLane(ctx context.Context, lr *laneRun, c candidate) {
	defer r.wg.Done()
	ev, ok := r.executeLane(ctx, lr, c)
	r.unregister(lr)
	if ok {
		r.emit(ev)
	}
}

// executeLane is one run: record, claim, provision, execute, move — with run
// evidence written around the agent so a later process can adopt or reap it.
func (r *Reconciler) executeLane(ctx context.Context, lr *laneRun, c candidate) (Event, bool) {
	issue := c.issue
	fail := func(err error) (Event, bool) {
		return Event{Type: EventError, Lane: lr.lane, TicketID: issue.ID,
			Ticket: issue.Identifier, Queue: c.name, Err: err}, true
	}

	// The record exists before the claim is attempted, so a crash anywhere
	// after winning leaves evidence behind: the next process reaps the dead
	// record and releases the claim. The reverse order would leave the ticket
	// assigned and recordless — invisible to every future pass — until a
	// human noticed.
	// The session id is minted here rather than inside Execute: it goes on
	// disk with the record, before the agent starts, because a run this
	// process will not live to see — one a later lerp adopts — can only be
	// ejected if its session id was recorded. A runner whose command never
	// asks for one gets "", and its runs are not ejectable.
	sessionID, err := run.NewSessionID(r.o.Repo.Runners[c.queue.Runner])
	if err != nil {
		return fail(fmt.Errorf("session id for issue %s: %w", issue.ID, err))
	}

	record, err := r.o.Evidence.Create(evidence.Record{
		Lane:           lr.lane,
		StartedAt:      time.Now().UTC(),
		TicketID:       issue.ID,
		Ticket:         issue.Identifier,
		Queue:          c.name,
		StartingStatus: c.queue.Status,
		SessionID:      sessionID,
	})
	if err != nil {
		return fail(fmt.Errorf("record run for issue %s: %w", issue.ID, err))
	}

	viewerID, won, err := claimForQueue(ctx, r.o.Client, issue.ID, c.queue.Status)
	if err != nil {
		// Keep the record: if the assign landed before the protocol failed
		// and its best-effort release did not stick, the next pass reaps the
		// dead record and releases the claim — the same repair a crash gets.
		// The run exists now, so the failure names it.
		ev, ok := fail(err)
		ev.RunID = record.RunID
		return ev, ok
	}
	if !won {
		if err := r.o.Evidence.Remove(record.RunID); err != nil {
			r.fail(fmt.Errorf("remove run record %s: %w", record.RunID, err))
		}
		return Event{}, false
	}

	// The claim is won but no agent runs while the workspace is prepared;
	// announce it so a subscriber shows the lane occupied, not idle.
	r.emit(Event{Type: EventProvisioning, RunID: record.RunID, Lane: lr.lane,
		TicketID: issue.ID, Ticket: issue.Identifier, Queue: c.name,
		LogPath: record.LogPath, StartedAt: record.StartedAt})

	ev, keepRecord, tel, ok := r.provisionAndRun(ctx, lr, c, record, viewerID)
	if !keepRecord {
		if err := r.o.Evidence.Remove(record.RunID); err != nil {
			r.fail(fmt.Errorf("remove run record %s: %w", record.RunID, err))
		} else if tel != nil {
			r.o.Telemetry(*tel)
		}
	}
	return ev, ok
}

// runCostCap bounds how much of a finished run's log runCost reads. Every
// vendor's stream that states a cost at all states it on one line near the
// end of an ordinary run's log — a few kilobytes to a few megabytes — so this
// reaches it with room to spare, the way pulse's own catchupChunks reaches an
// ordinary run's history in one poll. It exists for the run this is not: a
// multi-day agent whose log runs to hundreds of megabytes would otherwise
// have this reconcile's tick goroutine read the whole thing and JSON-decode
// every line of it, unbounded, on the same call reapDisposeTimeout exists to
// keep bounded. A run that pathological reports no cost rather than stalling
// the loop over one — the same trade pulse already makes against a log too
// long to catch up on in a poll.
const runCostCap = 8 << 20 // 8 MiB

// runCost reads a finished run's log for the dollar figure its runner's
// stream reported, decoding it exactly as the TUI's pulse does and summing
// every event's Cost. It must run before the record is discarded — a
// subscriber reacting to EventExited is already too late, since
// Evidence.Remove may have deleted the file by the time the event arrives —
// and after the runner has exited, so the log is complete rather than a
// moving target. Zero for a runner whose stream never states a cost, and for
// a log this cannot read: neither is worth failing a run over.
func runCost(path string) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, runCostCap))
	if err != nil {
		return 0
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		// Feed only decodes complete lines; the log is done being written
		// (the runner has already exited), so a missing trailing newline is
		// the file's own quirk, not a line still arriving.
		b = append(b, '\n')
	}
	var s logfmt.Stream
	var cost float64
	for _, ev := range s.Feed(b) {
		cost += ev.Cost
	}
	return cost
}

// provisionAndRun is the part of a run that owns a workspace. The deferred
// dispose runs when this function returns, before the caller discards the run
// record, so a settled run never leaves a workspace behind without the record
// that names it.
//
// tel is the telemetry.Run executeLane writes once it has removed the
// record, nil on every path where no agent finished a run to measure: an
// ejected run (the operator took the work over — the run did not finish),
// a provision failure and an execution error (no agent ran), and a shutdown
// (ctx.Err() != nil keeps the record; the next lerp's reap writes the line).
func (r *Reconciler) provisionAndRun(ctx context.Context, lr *laneRun, c candidate, record evidence.Record, viewerID string) (ev Event, keepRecord bool, tel *telemetry.Run, ok bool) {
	issue := c.issue
	id := workspace.Identity{Lane: lr.lane, TicketID: issue.ID, Workspace: record.Workspace}
	// Registered before provisioning, not after: a provision command that
	// created its workspace and then failed partway must still be cleaned up,
	// or the next attempt collides with what it left behind. Dispose reports
	// its own failures to the log; it is bounded by the same timeout as the
	// reap path, so a hung dispose command cannot wedge the lane — occupied,
	// its terminal event unemitted — forever.
	// ejected is decided once, by the handshake below, and read by this
	// defer: the operator took this run over, so the workspace is theirs —
	// agent output and half-finished work included, worktree and all — and
	// disposing it here would delete the very thing eject hands over.
	ejected := false
	defer func() {
		if ejected {
			return
		}
		r.disposeBounded(ctx, id)
	}()

	fail := func(err error) (Event, bool, *telemetry.Run, bool) {
		return Event{Type: EventError, RunID: record.RunID, Lane: lr.lane, TicketID: issue.ID,
			Ticket: issue.Identifier, Queue: c.name, LogPath: record.LogPath, Err: err}, false, nil, true
	}

	if err := r.o.Provision(ctx, r.o.RepoDir, r.o.Repo.Provision, id, r.o.Log); err != nil {
		// Provisioning never starts a lane. Release our claim so the queued
		// ticket remains eligible for a later attempt.
		err = fmt.Errorf("provision issue %s: %w", issue.ID, err)
		if releaseErr := releaseClaim(ctx, r.o.Client, issue.ID, viewerID); releaseErr != nil {
			err = fmt.Errorf("%w (%v)", err, releaseErr)
		}
		return fail(err)
	}

	r.emit(Event{Type: EventStarted, RunID: record.RunID, Lane: lr.lane, TicketID: issue.ID,
		Ticket: issue.Identifier, Queue: c.name, LogPath: record.LogPath,
		StartedAt: record.StartedAt})

	result, err := r.o.Execute(ctx, run.Invocation{
		Runner:  r.o.Repo.Runners[c.queue.Runner],
		Queue:   c.queue,
		Ticket:  issue.Identifier,
		Workdir: record.Workspace,
		LogPath: record.LogPath,
		// The run records its own exit status, so a lerp that restarts across
		// its finish can still apply the move rule. The live path below keeps
		// concluding from the code Wait() returned: the file is the fallback
		// for runs nobody was waiting on, never a second source of truth.
		ExitPath: record.ExitPath,
		// The id already on the record, so what the agent is told and what
		// eject would resume can never be two different sessions.
		SessionID: record.SessionID,
		Started: func(pid int) {
			// The PID makes the record adoptable; without it the run would be
			// reaped by the next process even while the agent is alive. An
			// agent whose PID cannot be recorded must not keep running: kill
			// its process group so the run fails now, visibly, instead of
			// surviving as an unadoptable orphan for a successor to reap —
			// workspace and all — out from under it.
			attached, err := r.o.Evidence.Attach(record.RunID, pid)
			if err != nil {
				r.fail(fmt.Errorf("attach pid of run %s: %w", record.RunID, err))
				_ = r.o.Kill(-pid, syscall.SIGKILL)
				return
			}
			// The lane keeps the attached record — the first moment a PID
			// exists is the first moment this run can be ejected.
			r.setRecord(lr, attached)
		},
	})
	// The agent is gone: either the operator ejected it, or it exited and
	// this run now owns settling the ticket. beginSettling decides which,
	// once, under the lock eject takes — so the two can never both happen,
	// and an eject arriving a moment too late is refused rather than
	// half-applied.
	if ejected = !r.beginSettling(lr); ejected {
		// Ejecting is taking the work over, not failing at it: the ticket
		// keeps its claim and its status, so there is no conclude to run and
		// nothing to say about the exit code of a process that was shot. The
		// record is gone already; dropping it again is harmless and keeps the
		// one rule ("keepRecord false means remove it") intact. No telemetry
		// either: the run did not finish, and any number written for it would
		// be a lie about a session that continues interactively.
		return Event{Type: EventEjected, RunID: record.RunID, Lane: lr.lane, TicketID: issue.ID,
			Ticket: issue.Identifier, Queue: c.name, LogPath: record.LogPath}, false, nil, true
	}
	if ctx.Err() != nil {
		// Shutdown, not failure: the agent was killed along with the loop.
		// Keep the claim and the record; the next lerp finds a dead run and
		// reaps it, so a deliberate stop and a crash repair identically —
		// telemetry included, since reap is what writes the line.
		return Event{}, true, nil, false
	}
	if err != nil {
		// The runner could not be executed at all. Keep the claim — releasing
		// it would retry a broken runner forever — and drop the local record,
		// which never gained a process worth adopting. No agent ran, so
		// nothing to measure.
		return fail(fmt.Errorf("run issue %s: %w", issue.ID, err))
	}

	// The move rule: on_success on a clean exit, on_failure otherwise, and
	// only if the agent didn't move the ticket itself. A move failure rides
	// on the exit event; the ticket stays claimed for a human to settle. A
	// move that was skipped because the ticket left mid-run rides along too,
	// as a note: the run log alone is not somewhere anyone is looking.
	note, final, moveErr := conclude(ctx, r.o.Client, issue, c.queue, r.o.Repo, result.ExitCode, viewerID, r.o.Log)
	exitCode := result.ExitCode
	settled := buildTelemetryRun(r.o.Repo, r.o.RepoDir, record, issue.Identifier, c.name, time.Now().UTC(), result.Duration.Milliseconds(), &exitCode, final)
	return Event{Type: EventExited, RunID: record.RunID, Lane: lr.lane, TicketID: issue.ID,
		Ticket: issue.Identifier, Queue: c.name, LogPath: record.LogPath,
		ExitCode: result.ExitCode, Note: note, Err: moveErr, Cost: runCost(record.LogPath)}, false, &settled, true
}

// freeLanes returns the lane numbers new runs may start in: lanes 1..N not in
// use, capped so adopted runs on out-of-range lanes still count against N.
func (r *Reconciler) freeLanes() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	used := make(map[int]bool, len(r.active))
	for _, lr := range r.active {
		used[lr.lane] = true
	}
	capacity := r.o.Lanes - len(r.active)
	var free []int
	for lane := 1; lane <= r.o.Lanes && len(free) < capacity; lane++ {
		if !used[lane] {
			free = append(free, lane)
		}
	}
	return free
}

// register occupies a lane for a ticket, refusing tickets already in a lane:
// a just-claimed run's assignment may not be visible on the board yet. It
// refuses an occupied lane number too. fill picks its lanes from a freeLanes
// snapshot and registers them one at a time, so a force-start landing in that
// window can take a lane the snapshot still calls free; two live runs sharing
// a lane number would share the LERP_LANE a project's provision isolates on.
// The racing candidate is simply skipped — the next pass sees the lane gone
// and picks another.
func (r *Reconciler) register(lane int, ticketID string) (*laneRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, lr := range r.active {
		if lr.ticketID == ticketID || lr.lane == lane {
			return nil, false
		}
	}
	lr := &laneRun{lane: lane, ticketID: ticketID}
	r.active = append(r.active, lr)
	return lr, true
}

// registerForce occupies a lane for a forced run, choosing the number under
// the same lock that checks it: the lowest free lane in 1..N, or one above
// the highest occupied lane when every configured one is busy. Choosing
// inside the hold is the point — two force-starts a frame apart must not pick
// the same number. Like register, it refuses a ticket already in a lane.
//
// reserved is recordedLanes' answer: lanes held by live runs this process has
// not adopted yet, which are as occupied as the ones in r.active.
func (r *Reconciler) registerForce(ticketID string, reserved map[int]bool) (*laneRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	used := make(map[int]bool, len(r.active)+len(reserved))
	highest := 0
	for lane := range reserved {
		used[lane] = true
		highest = max(highest, lane)
	}
	for _, lr := range r.active {
		if lr.ticketID == ticketID {
			return nil, false
		}
		used[lr.lane] = true
		highest = max(highest, lr.lane)
	}
	lane := highest + 1
	for n := 1; n <= r.o.Lanes; n++ {
		if !used[n] {
			lane = n
			break
		}
	}
	lr := &laneRun{lane: lane, ticketID: ticketID}
	r.active = append(r.active, lr)
	return lr, true
}

// beginSettling claims the right to settle a run whose agent has exited, and
// reports whether it got it: an ejected run is the operator's, and its ticket
// must not be concluded. Claiming marks the lane run, so an eject that
// arrives from here on is refused — the two decisions are made under one
// mutex precisely so neither can land on top of the other.
func (r *Reconciler) beginSettling(lr *laneRun) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ejected[lr.record.RunID] {
		return false
	}
	lr.settling = true
	return true
}

// setRecord records a started run's evidence on its lane, so eject can find
// the PID, the workspace and the session id without going back to disk.
func (r *Reconciler) setRecord(lr *laneRun, record evidence.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr.record = record
}

func (r *Reconciler) unregister(lr *laneRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, other := range r.active {
		if other == lr {
			r.active = append(r.active[:i], r.active[i+1:]...)
			return
		}
	}
}

// forget drops the adopted lane entry for a run, if there is one.
func (r *Reconciler) forget(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forgetLocked(runID)
}

// forgetLocked is forget for a caller already holding r.mu.
func (r *Reconciler) forgetLocked(runID string) {
	for i, lr := range r.active {
		if lr.adopted && lr.record.RunID == runID {
			r.active = append(r.active[:i], r.active[i+1:]...)
			return
		}
	}
}

// ownsTicket reports whether a run this process started holds the ticket.
// Ownership is by ticket, not run ID, because a lane is registered before its
// evidence exists: a record must never be reaped in the gap between this
// process creating it and attaching the agent's PID.
func (r *Reconciler) ownsTicket(ticketID string) bool {
	if ticketID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, lr := range r.active {
		if !lr.adopted && lr.ticketID == ticketID {
			return true
		}
	}
	return false
}

func (r *Reconciler) adoptedRecords() []evidence.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	var records []evidence.Record
	for _, lr := range r.active {
		if lr.adopted {
			records = append(records, lr.record)
		}
	}
	return records
}

func (r *Reconciler) emit(ev Event) {
	if ev.Err != nil && r.o.Log != nil {
		fmt.Fprintln(r.o.Log, ev.Err)
	}
	if r.o.Events != nil {
		r.o.Events(ev)
	}
}

func (r *Reconciler) fail(err error) {
	var rl *linear.RateLimitError
	if errors.As(err, &rl) {
		r.pauseFor(rl.RetryAfter)
	}
	r.emit(Event{Type: EventError, Err: err})
}
