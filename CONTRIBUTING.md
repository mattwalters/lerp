# Contributing

Lerp is a small tool with a deliberately small surface, maintained by
one person. That shapes what contributing here looks like: the useful
work is almost never "write the code first".

## Read SCOPE.md first

[SCOPE.md](SCOPE.md) is the fence around this project — five concepts,
nine invariants, and litmus tests every change runs through. It exists
because lerp's predecessor died of bloat: the code and the conceptual
framework grew until nobody could hold the whole thing in their head.
A proposal that conflicts with it does not win on being well written.
Either the proposal loses, or the document is amended — deliberately,
never by drift — and an amendment is its own argument, made before the
code that depends on it.

So for anything non-trivial, **open an issue and make the scope case
before sending code**. Say what the change does and where in SCOPE.md
it lives. Run the litmus tests yourself and answer them there: does it
add a sixth concept, and if so which of the five does it remove; does
it put durable state anywhere but Linear; does it make a queue run
unsafe to kill; does it need a runner capability not every runner has;
does it require lerp to speak to an API other than Linear; could it be
config pointing at what already exists, instead of code. A "yes" to
any of the first five without an amendment is a "no" — a much cheaper
"no" in an issue than in a branch. A "yes" to the last one isn't a
"no" at all: it means the change is somebody's `lerp.toml` — a queue,
a runner, a prompt, a provision command — and the engine already does
its part.

Typo fixes, doc corrections, a clear bug with a clear fix: skip the
ceremony and send the PR.

## House rules

The same rules the agents work under (see `AGENTS.md`):

- Boring, small, direct. Prefer the standard library; the TUI uses the
  Bubble Tea ecosystem. New dependencies need a reason.
- Treat scope growth, speculative abstraction, and framework-building
  as bugs.
- Match the style of the surrounding code.

## Mechanics

- `make check` is the gate on the code — gofmt, vet, build, test. CI
  runs that same target rather than its own copy of the steps, so the
  two cannot drift; it runs it on Linux and macOS both.
- CI runs four more jobs, and they are the ones that can go red when
  your local check was green. `govulncheck` scans the dependency tree
  against a vulnerability database that is only ever current, so it
  can fail a PR that changed nothing — a newly published
  vulnerability, or a govulncheck release, is about the tree, not your
  diff.
- The other is `make demo`: it re-records the README's cast from
  `docs/demo.tape`. It fails if vhs errors, if the demo harness inside
  the recording exits non-zero — vhs would happily record a cast of
  that error and exit 0, so the harness reports its own status in a
  file — or if the GIF comes back over its size cap. Nothing diffs the
  bytes, so read the failure message before you read your diff; the
  cap is the usual answer, and the Makefile says what to do about it.
  Reproduce it locally with `make demo`, which needs
  [vhs](https://github.com/charmbracelet/vhs). Nothing checks that the
  cast still shows the *right* thing, so if your change dates it,
  commit the re-recorded `docs/demo.gif` too.
- The third is `release-config`: `goreleaser check` and then `make
  snapshot`, which cross-builds the release binaries for macOS and
  Linux without publishing anything. It exists because `.goreleaser.yaml`
  is otherwise only ever executed by a tag push, which is after the
  irreversible step — so a dependency that quietly needs cgo, or a
  schema rule a later goreleaser tightens, is a red PR here instead of a
  tag on origin with no release attached. Reproduce it locally with
  `make snapshot`, which needs
  [goreleaser](https://goreleaser.com/install/) v2.6 or newer.
- The docs site is the manual under `docs/content/docs/` plus this
  repo's own markdown — the README and SCOPE.md you are reading are
  mounted into it rather than copied — so a PR touching any of them
  builds the site as a gate. A new manual page reaches the sidebar
  only once it has a `[[menus.main]]` entry in `docs/hugo.toml` — the
  sidebar is curated, not derived — though its section index lists it
  either way. A page not ready to be read is `draft = true`.
  `make docs-serve` previews it locally and needs
  [hugo](https://gohugo.io); `make hugo-version` prints the version CI
  builds and deploys with, which is also what a local build warns about
  not being.
- PRs go against `main`. Lerp is pre-1.0 with no tagged releases;
  `main` is what `go install ...@latest` gets.
- One change per PR, and say in the description what it does and why.
  If an issue argued the scope case, link it.
- PR titles here often start with a `LERP-NN:` identifier. That is the
  maintainer's own ticket board leaking through — it is not something
  a contribution needs.
- Found a vulnerability? Don't open an issue —
  [SECURITY.md](SECURITY.md) has the private reporting path, and the
  trust model that says what counts as one.

## Maintainership, honestly

One maintainer, no triage rotation. Response times are best-effort; a
quiet week means a quiet week, not a silent rejection.

Small, focused PRs land — especially ones fixing something concrete,
or ones an issue already argued through. Large speculative changes,
new subsystems, and anything on SCOPE.md's "not yet, maybe never" list
mostly won't, and that is a decision about scope rather than about
your code. If you are unsure which of those yours is, the issue is the
place to find out before you spend the afternoon.
