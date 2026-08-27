//go:build unix

package loop

import (
	"os"
	"strings"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/telemetry"
)

// buildTelemetryRun assembles the line telemetry gets for one finished run.
// provisionAndRun and settleDead each know something the other does not —
// when the run ended, how long it took, its exit code, where the ticket came
// to rest, and the human ticket identifier itself — and hand those in;
// everything else here comes from config and the evidence record the same
// way on both paths, plus one read of the run's own log.
//
// ticket is a parameter rather than read off record.Ticket internally
// because a caller holding a freshly-read issue has a better source than a
// record field that is empty on any run started before it existed (like
// evidence.Record.ExitPath was).
//
// durationMS zero and exitCode nil both mean "unknown" rather than zero: an
// omitempty on the first and a pointer on the second are what let the line
// this returns tell the two apart from a real zero-length run or a clean
// exit.
func buildTelemetryRun(repo *config.RepoConfig, repoDir string, record evidence.Record, ticket, queueName string, at time.Time, durationMS int64, exitCode *int, status string) telemetry.Run {
	line := telemetry.Run{
		At:         at,
		Repo:       repoDir,
		Team:       telemetryTeam(ticket),
		Ticket:     ticket,
		Queue:      queueName,
		Session:    record.SessionID,
		DurationMS: durationMS,
		ExitCode:   exitCode,
		Status:     status,
	}
	if queue, ok := repo.Queues[queueName]; ok {
		line.Runner = queue.Runner
		if runner, ok := repo.Runners[queue.Runner]; ok {
			line.Vendor = runner.Vendor
			line.Model = runner.Model
		}
	}
	if record.LogPath != "" {
		if f, err := os.Open(record.LogPath); err == nil {
			defer f.Close()
			summary := telemetry.Summarize(f)
			line.Tokens = summary.Tokens
			line.CostUSD = summary.Cost
			if summary.Model != "" {
				// The stream's own account of what it ran outranks what config
				// pinned: a runner may resolve a model alias ("sonnet") to
				// something more specific than the config that requested it.
				line.Model = summary.Model
			}
		}
	}
	return line
}

// telemetryTeam is a ticket identifier's team prefix — LERP-138 -> LERP.
// Linear guarantees the shape, so no ticket lookup is needed to get it.
func telemetryTeam(ticket string) string {
	if i := strings.IndexByte(ticket, '-'); i > 0 {
		return ticket[:i]
	}
	return ""
}

// exitTiming is when a reaped run ended and how long it ran, read from its
// exit file's mtime — that file is written by the run's own wrapper shell at
// exit, so its mtime is the finish time. A run that never recorded one
// (killed mid-run, or a record predating the exit file) reports the
// settlement time and no duration: "at" always has a value, "duration_ms"
// only when there is evidence for one.
func exitTiming(record evidence.Record, recorded bool) (at time.Time, durationMS int64) {
	if !recorded || record.ExitPath == "" {
		return time.Now().UTC(), 0
	}
	fi, err := os.Stat(record.ExitPath)
	if err != nil {
		return time.Now().UTC(), 0
	}
	mtime := fi.ModTime().UTC()
	// A record predating StartedAt (see reconciler.go's own IsZero check on
	// it) has nothing to subtract from, and Sub against the zero time would
	// saturate to Duration's ~292-year max rather than report "unknown".
	// Clock skew across machines can put mtime before StartedAt too; either
	// way a duration that isn't positive is not a real measurement.
	if record.StartedAt.IsZero() {
		return mtime, 0
	}
	if d := mtime.Sub(record.StartedAt); d > 0 {
		return mtime, d.Milliseconds()
	}
	return mtime, 0
}
