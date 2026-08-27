package telemetry

import (
	"io"

	"github.com/mattwalters/lerp/internal/logfmt"
)

// Summary is one run's log, totaled: every call's Usage summed over the
// whole stream, and the Cost and Model the terminal result/init events
// reported, when they did.
type Summary struct {
	Tokens int
	Cost   float64
	Model  string
}

// Summarize reads a run's whole log through logfmt once — at run exit, on
// the settling lane's own goroutine, the one new cost this package adds —
// and totals it. Usage is already a per-call delta in every decoder, so
// summing it over the log is the run's total.
//
// A log that decodes as raw text — a command-template runner, or a runner
// logfmt has no decoder for — totals zero, which the caller reports as an
// absent field rather than "tokens":0. A log that is empty, unreadable, or
// truncated mid-line totals whatever full lines it had: this is a summary of
// nothing, never an error.
func Summarize(r io.Reader) Summary {
	var stream logfmt.Stream
	var sum Summary
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		for _, ev := range stream.Feed(buf[:n]) {
			sum.Tokens += ev.Usage
			if ev.Cost != 0 {
				sum.Cost = ev.Cost
			}
			if ev.Model != "" {
				sum.Model = ev.Model
			}
		}
		if err != nil {
			return sum
		}
	}
}
