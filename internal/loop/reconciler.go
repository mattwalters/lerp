package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/run"
	"github.com/mattwalters/lerp/internal/workspace"
)

// DefaultInterval is how often the reconciler polls when the caller does not
// choose an interval. Polling is the design: no webhooks, and nothing listens
// on a port.
const DefaultInterval = 12 * time.Second

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
	// EventReaped reports that a recorded run's process is dead: its
	// workspace was disposed and its record removed. The ticket is left
	// wherever Linear says it is.
	EventReaped EventType = "reaped"
	// EventExited reports that a run this process started finished and the
	// queue's move rule was applied.
	EventExited EventType = "exited"
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

// AttentionGroup names which half of the needs-you view an item belongs to.
type AttentionGroup string

const (
	// ToRoute is unclaimed work sitting in a status no queue serves: nobody
	// has put it on the board yet.
	ToRoute AttentionGroup = "to route"
	// Parked is a ticket the operator claimed that sits in a status no
	// queue serves — parked there deliberately, or landed there by a failed
	// run's on_failure move.
	Parked AttentionGroup = "parked on you"
)

// AttentionItem is one ticket waiting on the operator: what it is, why it
// needs a human, and Linear's URL for it. TicketID is Linear's internal id —
// what Promote and MoveIssue take; Ticket is the human identifier shown on
// screen.
type AttentionItem struct {
	Group    AttentionGroup
	Ticket   string // human identifier, e.g. LERP-42
	TicketID string
	Title    string
	Status   string
	Reason   string
	URL      string
	// Priority is Linear's own scale: 0 none, 1 urgent, 2 high, 3 medium,
	// 4 low. It orders the to-route group under leverage and is shown on
	// the row; nothing in the loop acts on it.
	Priority int
	// BlockedBy names every unfinished ticket holding this one up, listed
	// or not; Blocks names the unfinished tickets it holds up. Unblocks is
	// how many other listed tickets it transitively frees — the leverage of
	// routing it, and the to-route group's first sort key.
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
	ExitCode  int             // meaningful only for EventExited
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
}

// laneRun is one occupied lane: either a run this process started or a live
// run adopted from a previous process.
type laneRun struct {
	lane     int
	ticketID string
	adopted  bool
	record   evidence.Record // adopted runs only; started runs settle their own records
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
	if o.Log != nil {
		o.Log = &syncWriter{w: o.Log}
	}
	return &Reconciler{o: o}, nil
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
// one.
func (r *Reconciler) Tick(ctx context.Context) {
	if r.reconcileEvidence(ctx) {
		r.fill(ctx)
	}
	r.attention(ctx)
}

// AttentionDefinition is the operator-facing one-line description of the
// rule attention implements, rendered by the TUI's empty state. It lives
// here, next to the rule, so the two change in the same hunk.
const AttentionDefinition = "unclaimed tickets, and your claimed tickets, sitting in statuses no queue serves"

// attention recomputes what needs the operator and emits it as one
// EventAttention carrying the whole list, to-route items before parked ones.
//
// The v0 definition of "needs you", in full: a ticket needs the operator
// when it sits in a status no queue serves, and is either unassigned (to
// route — nobody has put it on the board yet) or assigned to the operating
// user (parked on them — deliberately, or landed there by a failed run's
// on_failure move). Deliberately out of v0: a claimed ticket sitting in a
// queue status with no live run in a lane (a failed run whose queue has no
// on_failure route). From one Linear read that state is indistinguishable
// from a live run under the same user on another machine, so v0 leaves it to
// the log line conclude writes. This is a reading of the board plus a place
// to route from, not a general inbox; resist growing it further.
//
// A pass that could not list every team emits nothing: the failure is
// reported and the subscriber keeps its last full list, because a partial
// one could falsely read as "nothing needs you".
func (r *Reconciler) attention(ctx context.Context) {
	viewerID, err := r.o.Client.Viewer(ctx)
	if err != nil {
		r.fail(fmt.Errorf("attention: read viewer: %w", err))
		return
	}
	served := servedStatuses(r.o.Repo)
	var toRoute, parked []AttentionItem
	for _, team := range r.o.Repo.Teams {
		unassigned, err := r.o.Client.ListUnassignedIssues(ctx, team)
		if err != nil {
			r.fail(fmt.Errorf("attention: list unassigned tickets for team %s: %w", team, err))
			return
		}
		for _, issue := range unassigned {
			if served[issue.Status] {
				continue
			}
			toRoute = append(toRoute, AttentionItem{
				Group:     ToRoute,
				Ticket:    issue.Identifier,
				TicketID:  issue.ID,
				Title:     issue.Title,
				Status:    issue.Status,
				Reason:    fmt.Sprintf("unassigned in %q — no queue serves it", issue.Status),
				URL:       issue.URL,
				Priority:  issue.Priority,
				BlockedBy: issue.BlockedBy,
				Blocks:    issue.Blocks,
			})
		}

		assigned, err := r.o.Client.ListAssignedIssues(ctx, team, viewerID)
		if err != nil {
			r.fail(fmt.Errorf("attention: list claimed tickets for team %s: %w", team, err))
			return
		}
		for _, issue := range assigned {
			if served[issue.Status] {
				continue
			}
			parked = append(parked, AttentionItem{
				Group:     Parked,
				Ticket:    issue.Identifier,
				TicketID:  issue.ID,
				Title:     issue.Title,
				Status:    issue.Status,
				Reason:    fmt.Sprintf("claimed in %q — no queue serves it", issue.Status),
				URL:       issue.URL,
				Priority:  issue.Priority,
				BlockedBy: issue.BlockedBy,
				Blocks:    issue.Blocks,
			})
		}
	}
	items := make([]AttentionItem, 0, len(toRoute)+len(parked))
	items = append(items, toRoute...)
	items = append(items, parked...)
	countUnblocks(items)
	route, park := items[:len(toRoute)], items[len(toRoute):]
	// To route is ordered by leverage: what frees the most other listed
	// tickets first, so the promote worth making is the top row. Parked
	// keeps its identifier order — nothing is waiting on those.
	sort.Slice(route, func(i, j int) bool {
		a, b := route[i], route[j]
		// A blocked ticket cannot usefully be routed anywhere yet, so it
		// sorts below every ticket that can be — however much it would
		// unblock once its own blocker clears.
		if ablocked, bblocked := len(a.BlockedBy) > 0, len(b.BlockedBy) > 0; ablocked != bblocked {
			return bblocked
		}
		if a.Unblocks != b.Unblocks {
			return a.Unblocks > b.Unblocks
		}
		if ra, rb := priorityRank(a.Priority), priorityRank(b.Priority); ra != rb {
			return ra < rb
		}
		return a.Ticket < b.Ticket
	})
	sort.Slice(park, func(i, j int) bool { return park[i].Ticket < park[j].Ticket })
	r.emit(Event{Type: EventAttention, Attention: items})
}

// priorityRank turns Linear's priority into a sort key. The scale runs
// urgent (1) to low (4) but puts "no priority" at 0, which would otherwise
// sort ahead of urgent; unset means unranked, so it goes last.
func priorityRank(p int) int {
	if p == 0 {
		return 5
	}
	return p
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

// Promote is the TUI's one write action, the SCOPE amendment this ticket
// makes: move a selected ticket straight into a status by calling the same
// client the loop reads with. It touches no lane, claim, or evidence — the
// next pass simply finds the ticket wherever this left it, exactly as it
// would find a move a human made in Linear.
func (r *Reconciler) Promote(ctx context.Context, ticketID, status string) error {
	return r.o.Client.MoveIssue(ctx, ticketID, status)
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
	r.emit(Event{Type: EventAdopted, RunID: record.RunID, Lane: record.Lane,
		TicketID: record.TicketID, Queue: record.Queue, LogPath: record.LogPath,
		StartedAt: record.StartedAt})
}

// reapDisposeTimeout bounds every dispose command the loop runs. Reaps run on
// the tick loop itself, so a dispose that hangs would otherwise stall every
// later pass — and shutdown — indefinitely; on the normal exit path it would
// wedge the lane, its terminal event never emitted.
const reapDisposeTimeout = 2 * time.Minute

// reap cleans up after a run whose process is dead: dispose the workspace,
// release the dead run's claim when the board still shows it, and remove the
// local record. The ticket's status is left wherever Linear says it is — a
// crash is drift, and re-running the stage repairs it (SCOPE invariant 3).
//
// It reports whether the reap finished. One that could not keeps the record,
// so the next pass retries it.
func (r *Reconciler) reap(ctx context.Context, record evidence.Record) bool {
	// The process is gone, so the workspace is garbage whether or not the
	// provision command ever finished building it. Dispose reports its own
	// failures to the log and never blocks the reap.
	id := workspace.Identity{Lane: record.Lane, TicketID: record.TicketID, Workspace: record.Workspace}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reapDisposeTimeout)
	r.o.Dispose(dctx, r.o.RepoDir, r.o.Repo.Dispose, id, r.o.Log)
	cancel()
	if err := r.releaseDead(ctx, record); err != nil {
		r.fail(fmt.Errorf("reap run %s: %w", record.RunID, err))
		return false
	}
	if err := r.o.Evidence.Remove(record.RunID); err != nil {
		r.fail(fmt.Errorf("reap run %s: %w", record.RunID, err))
		return false
	}
	r.emit(Event{Type: EventReaped, RunID: record.RunID, Lane: record.Lane,
		TicketID: record.TicketID, Queue: record.Queue, LogPath: record.LogPath})
	return true
}

// releaseDead releases a dead run's claim so its ticket becomes eligible
// again — but only when the board still looks exactly as the run left it:
// same status the run started from, still assigned to this operating user.
// Anything else means a human, an agent, or an automation acted since, and
// the loop leaves their work alone.
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
	lanes := r.freeLanes()
	for _, c := range candidatesFrom(listings) {
		if len(lanes) == 0 {
			return
		}
		lr, ok := r.register(lanes[0], c.issue.ID)
		if !ok {
			// This ticket is already occupying a lane — typically a run whose
			// claim Linear had not reflected when the board was listed.
			continue
		}
		lanes = lanes[1:]
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

// executeLane is one run: record, claim, provision, execute, move. It mirrors
// Once's single-lane flow with run evidence added around the agent.
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
	record, err := r.o.Evidence.Create(evidence.Record{
		Lane:           lr.lane,
		StartedAt:      time.Now().UTC(),
		TicketID:       issue.ID,
		Queue:          c.name,
		StartingStatus: c.queue.Status,
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

	ev, keepRecord, ok := r.provisionAndRun(ctx, lr, c, record, viewerID)
	if !keepRecord {
		if err := r.o.Evidence.Remove(record.RunID); err != nil {
			r.fail(fmt.Errorf("remove run record %s: %w", record.RunID, err))
		}
	}
	return ev, ok
}

// provisionAndRun is the part of a run that owns a workspace. The deferred
// dispose runs when this function returns, before the caller discards the run
// record, so a settled run never leaves a workspace behind without the record
// that names it.
func (r *Reconciler) provisionAndRun(ctx context.Context, lr *laneRun, c candidate, record evidence.Record, viewerID string) (ev Event, keepRecord, ok bool) {
	issue := c.issue
	id := workspace.Identity{Lane: lr.lane, TicketID: issue.ID, Workspace: record.Workspace}
	// Registered before provisioning, not after: a provision command that
	// created its workspace and then failed partway must still be cleaned up,
	// or the next attempt collides with what it left behind. Dispose reports
	// its own failures to the log; it is bounded by the same timeout as the
	// reap path, so a hung dispose command cannot wedge the lane — occupied,
	// its terminal event unemitted — forever.
	defer func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reapDisposeTimeout)
		defer cancel()
		r.o.Dispose(dctx, r.o.RepoDir, r.o.Repo.Dispose, id, r.o.Log)
	}()

	fail := func(err error) (Event, bool, bool) {
		return Event{Type: EventError, RunID: record.RunID, Lane: lr.lane, TicketID: issue.ID,
			Ticket: issue.Identifier, Queue: c.name, LogPath: record.LogPath, Err: err}, false, true
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
		Started: func(pid int) {
			// The PID makes the record adoptable; without it the run would be
			// reaped by the next process even while the agent is alive. An
			// agent whose PID cannot be recorded must not keep running: kill
			// its process group so the run fails now, visibly, instead of
			// surviving as an unadoptable orphan for a successor to reap —
			// workspace and all — out from under it.
			if _, err := r.o.Evidence.Attach(record.RunID, pid); err != nil {
				r.fail(fmt.Errorf("attach pid of run %s: %w", record.RunID, err))
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		},
	})
	if ctx.Err() != nil {
		// Shutdown, not failure: the agent was killed along with the loop.
		// Keep the claim and the record; the next lerp finds a dead run and
		// reaps it, so a deliberate stop and a crash repair identically.
		return Event{}, true, false
	}
	if err != nil {
		// The runner could not be executed at all. Keep the claim — releasing
		// it would retry a broken runner forever — and drop the local record,
		// which never gained a process worth adopting.
		return fail(fmt.Errorf("run issue %s: %w", issue.ID, err))
	}

	// The move rule: on_success on a clean exit, on_failure otherwise, and
	// only if the agent didn't move the ticket itself. A move failure rides
	// on the exit event; the ticket stays claimed for a human to settle.
	moveErr := conclude(ctx, r.o.Client, issue, c.queue, servedStatuses(r.o.Repo), result.ExitCode, r.o.Log)
	return Event{Type: EventExited, RunID: record.RunID, Lane: lr.lane, TicketID: issue.ID,
		Ticket: issue.Identifier, Queue: c.name, LogPath: record.LogPath,
		ExitCode: result.ExitCode, Err: moveErr}, false, true
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
// a just-claimed run's assignment may not be visible on the board yet.
func (r *Reconciler) register(lane int, ticketID string) (*laneRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, lr := range r.active {
		if lr.ticketID == ticketID {
			return nil, false
		}
	}
	lr := &laneRun{lane: lane, ticketID: ticketID}
	r.active = append(r.active, lr)
	return lr, true
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
	r.emit(Event{Type: EventError, Err: err})
}
