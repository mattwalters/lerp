# Agent brief

Wand is a small, reliable CLI, written in Go, that orchestrates
software work through Linear.

Before proposing or implementing anything, read `SCOPE.md`. It is the
fence around this project: five concepts, nine invariants, and litmus
tests to run any change through. When a proposal conflicts with it, the
proposal loses or the document is amended deliberately — never by
drift.

House rules:

- Boring, small, direct. Prefer the standard library; the TUI uses the
  Bubble Tea ecosystem. New dependencies need a reason.
- Treat scope growth, speculative abstraction, and framework-building
  as bugs.
- Match the style of surrounding code.

Build and test commands will be documented here once code exists.
