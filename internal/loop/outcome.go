//go:build unix

package loop

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"

	"github.com/mattwalters/lerp/internal/logfmt"
	"github.com/mattwalters/lerp/internal/vendors"
)

// runCostCap bounds how much of a finished run's log readOutcome reads. Every
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

// readOutcome inspects a finished run's log for cost, a derived exit code, and
// any reason explaining why a clean exit was overturned.
//
// A non-zero exit code is never turned into a success. An exit code of 0 is
// treated as failed (exit code 1) when the run's own log says one of three
// things:
//
//  1. The terminal result event reports failure (e.g. status != "SUCCESS" in
//     antigravity, is_error in claude, turn.failed / error in codex).
//  2. The vendor's own abort line is in the log (read via AbortReporter).
//  3. The stream decoded end to end and nothing happened: a KindResult was
//     decoded with NoOutput true (success with no response), and no KindText
//     with prose and no non-error KindToolResult appeared anywhere in the
//     stream.
//
// Any log lerp cannot decode falls through to the process exit code.
func readOutcome(path, vendor string, exitCode int) (derivedCode int, cost float64, reason string) {
	derivedCode = exitCode
	if path == "" {
		return derivedCode, 0, ""
	}
	f, err := os.Open(path)
	if err != nil {
		return derivedCode, 0, ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, runCostCap))
	if err != nil {
		return derivedCode, 0, ""
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}

	var (
		s               logfmt.Stream
		hasResult       bool
		resultIsError   bool
		resultNoOutput  bool
		resultErrorText string
		hasProse        bool
		hasToolSuccess  bool
	)
	events := s.Feed(b)
	for _, ev := range events {
		cost += ev.Cost
		switch ev.Kind {
		case logfmt.KindResult:
			hasResult = true
			if ev.IsError {
				resultIsError = true
				resultErrorText = ev.Text
			} else if ev.NoOutput {
				resultNoOutput = true
			}
		case logfmt.KindText:
			if strings.TrimSpace(ev.Text) != "" {
				hasProse = true
			}
		case logfmt.KindToolResult:
			if !ev.IsError {
				hasToolSuccess = true
			}
		}
	}

	if exitCode != 0 {
		return derivedCode, cost, ""
	}

	// Signal 1: Terminal result event reports failure.
	if resultIsError {
		derivedCode = 1
		reason = resultErrorText
		if reason == "" {
			reason = "run reported failure"
		}
		return derivedCode, cost, reason
	}

	// Signal 2: Vendor abort line in log.
	if adapter, ok := vendors.Lookup(vendor); ok {
		if reporter, ok := adapter.(vendors.AbortReporter); ok {
			scanner := bufio.NewScanner(bytes.NewReader(b))
			for scanner.Scan() {
				if msg, ok := reporter.Aborted(scanner.Text()); ok {
					derivedCode = 1
					reason = msg
					return derivedCode, cost, reason
				}
			}
		}
	}

	// Signal 3: Stream decoded end to end and nothing happened.
	if hasResult && resultNoOutput && !hasProse && !hasToolSuccess {
		derivedCode = 1
		reason = "run produced no output"
		return derivedCode, cost, reason
	}

	return derivedCode, cost, ""
}

// runCost reads a finished run's log for the dollar figure its runner's stream
// reported.
func runCost(path string) float64 {
	_, cost, _ := readOutcome(path, "", 0)
	return cost
}
