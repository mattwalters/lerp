# Lerp installs the way Go programs install: `go install` puts the binary in
# Go's bin dir, which is already on a Go developer's PATH. This file wraps
# that; it is not a build system.

PKG := github.com/mattwalters/lerp

# Resolve the install dir the way the go tool does: GOBIN when set, else
# GOPATH/bin. Reading only GOPATH/bin would make `install` and `uninstall`
# disagree on a machine that sets GOBIN.
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

# A local build names the commit it came from and whether the tree was dirty,
# so the version it reports is never a guess. Recursively assigned: `make
# check` never shells out to git.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS = -X $(PKG)/internal/version.Version=$(VERSION)

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk -F':.*?## ' '{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: install
install: ## Build and install lerp into Go's bin dir
	go install -ldflags "$(LDFLAGS)" ./cmd/lerp
	@printf 'installed %s to %s\n' '$(VERSION)' '$(GOBIN)/lerp'
	@printf 'PATH resolves lerp to: %s\n' \
	  "$$(command -v lerp || echo '(nothing — is $(GOBIN) on your PATH?)')"

.PHONY: uninstall
uninstall: ## Remove the installed binary
	rm -f $(GOBIN)/lerp
	@printf 'removed %s\n' '$(GOBIN)/lerp'

.PHONY: check
check: ## The gate, on Linux and macOS in CI: gofmt, vet, build, test
	@test -z "$$(gofmt -l .)" || { echo 'unformatted files:'; gofmt -l .; exit 1; }
	go vet ./...
	go build ./...
	go test ./...

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -w .

# --------------------------------------------------------------------------
# Releases
# --------------------------------------------------------------------------

# The two halves of cutting a release. `snapshot` builds what a tag would
# build, without publishing anything; `release` pushes the tag and stops,
# because the build itself belongs to .github/workflows/release.yml — a
# release nobody can reproduce from a clean checkout is a release built on
# somebody's laptop.
#
# Neither is in `check`. The gate needs nothing installed beyond Go, and
# cross-building four binaries is not what a pre-commit run is for.

.PHONY: snapshot
snapshot: ## Build the release binaries locally, publishing nothing (needs goreleaser)
# Presence is all this checks. There is a version floor too — v2.6, where
# `archives.formats` replaced `format` — but nothing here enforces it: an
# older v2 fails on the config a moment later, saying which key, which is a
# legible enough answer not to be worth parsing `goreleaser --version` for.
# The floor is in the message so it is at hand when that happens.
	@command -v goreleaser >/dev/null || { \
	  echo 'snapshot: goreleaser (v2.6+) is not installed — see https://goreleaser.com/install/'; \
	  exit 1; }
	goreleaser release --snapshot --clean
# The hint names this platform's binary so the stamp is one command away.
# Guarded like demo's cap and example's `test -s`: goreleaser has renamed
# these directories before — `arm64` became `arm64_v8.0` when it grew
# `goarm64` — and a glob that misses must not print a sentence with a hole
# where the path goes.
	@bin=$$(ls -d dist/lerp_$$(go env GOOS)_$$(go env GOARCH)*/lerp 2>/dev/null | head -1); \
	  if [ -n "$$bin" ]; then \
	    printf 'built into dist/ — check the stamp with: %s version\n' "$$bin"; \
	  else \
	    printf 'built into dist/\n'; \
	  fi

# Semver's own grammar, spelled out because the looser pattern this replaces
# waved through tags goreleaser then refused — and it refuses at `parsing tag`,
# which is after the push. `v1.0.0-01` and `v1.0.0-rc.01` (a plausible typo for
# `-rc.1`) are the cases that cost a version number: semver forbids leading
# zeros in numeric identifiers, so an identifier is either a zeroless number or
# something containing a non-digit. Build metadata (`+…`) is left out; nothing
# here has a use for it.
SEMVER_NUM := (0|[1-9][0-9]*)
SEMVER_ID := ($(SEMVER_NUM)|[0-9]*[A-Za-z-][0-9A-Za-z-]*)
SEMVER_RE := ^v$(SEMVER_NUM)\.$(SEMVER_NUM)\.$(SEMVER_NUM)(-$(SEMVER_ID)(\.$(SEMVER_ID))*)?

.PHONY: release
release: ## Tag main and push it, which starts the release build (VERSION=v0.1.0)
# Every guard below is here because pushing a tag is the one thing in this
# file that cannot be undone: the workflow fires on the push, and a published
# release is not something to move afterwards.
#
# VERSION defaults to `git describe` output for the benefit of `install`, and
# that default must never become a tag. `origin` is how make tells a value it
# supplied itself from one the operator did — and the test is for `command
# line` specifically, not merely "not the default": an exported VERSION left
# over from another project by a shell profile or a direnv `.envrc` is not
# somebody asking for a release, and this is not the command to guess on.
	@test '$(origin VERSION)' = 'command line' || { \
	  echo 'release: name the tag on the command line — make release VERSION=v0.1.0'; \
	  exit 1; }
	@printf '%s' '$(VERSION)' | grep -Eq '$(SEMVER_RE)$$' || { \
	  printf 'release: %s is not a version tag — vMAJOR.MINOR.PATCH, optionally -rc1\n' \
	    '$(VERSION)'; exit 1; }
	@test -z "$$(git status --porcelain)" || { \
	  echo 'release: the tree is dirty — commit or stash before tagging'; exit 1; }
# Cut from merged main and nothing else, so the commit inside the release is
# one everybody else can already see rather than something local. Note what
# this does not prove: main having a commit is not CI having finished with it,
# so a release cut a minute after a merge tags a commit whose run is still
# going. Checking that would mean asking GitHub, which is a second API and a
# different tool's job.
	git fetch --quiet origin main
	@test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" || { \
	  echo 'release: HEAD is not origin/main — releases are cut from merged main'; \
	  exit 1; }
# Origin is asked first, because whether the tag is published is the fact the
# local check below needs. --exit-code so the three answers stay three: 0 the
# tag is there, 2 it is not, anything else means the question was never put to
# origin — and a guard that reads "unreachable" as "absent" is not a guard.
# stderr is deliberately not redirected: a revoked key, a renamed repo and a
# dropped VPN all land in the `*` branch below, and git has already said which
# one it was.
	@git ls-remote --exit-code --tags origin 'refs/tags/$(VERSION)' >/dev/null; \
	  case $$? in \
	  0) printf 'release: %s already exists on origin — pick the next version\n' \
	       '$(VERSION)'; exit 1 ;; \
	  2) ;; \
	  *) printf 'release: could not reach origin to check whether %s exists\n' \
	       '$(VERSION)'; exit 1 ;; \
	  esac
# Origin does not have the tag, so a local one is the wreckage of an earlier
# attempt whose push did not land — a dropped VPN, an expired key. If it names
# the commit being released it is exactly the tag this run would create, so
# reuse it and push. Refusing here would tell the operator to burn a version
# number on a failed connection. A local tag on some *other* commit is the
# real thing this refuses: moving a tag people may already have fetched.
	@if git rev-parse -q --verify 'refs/tags/$(VERSION)' >/dev/null \
	   && test "$$(git rev-parse 'refs/tags/$(VERSION)^{commit}')" != "$$(git rev-parse HEAD)"; then \
	  printf 'release: %s already exists here on another commit — a tag is never moved\n' \
	    '$(VERSION)'; exit 1; fi
	@git rev-parse -q --verify 'refs/tags/$(VERSION)' >/dev/null \
	  || git tag -a '$(VERSION)' -m 'lerp $(VERSION)'
	git push origin 'refs/tags/$(VERSION)'
	@printf 'pushed %s — the release build takes it from here:\n' '$(VERSION)'
	@printf '  https://github.com/mattwalters/lerp/actions/workflows/release.yml\n'

# Where the example generator writes before it is moved into place. Gitignored
# and beside the target, not in TMPDIR, so the move is a same-filesystem
# rename rather than a copy.
EXAMPLE_TMP := .lerp.example.toml.tmp

.PHONY: example
example: ## Regenerate lerp.example.toml from internal/config/stock.toml
# Moved into place only once the generator has written something, for the same
# reason the demo recipe stages its render: `go run ... > lerp.example.toml`
# truncates the committed file before the generator starts, so a package that
# does not compile leaves an empty example behind and a deletion in the diff.
# `test -s` is the same guard demo puts on a silent vhs — a generator that
# exits 0 having written nothing must not pass either.
#
# The mode is set explicitly rather than inherited: the rename carries the
# temp file's mode onto the committed example, and under a restrictive umask
# the redirect would have created it 0600. Git tracks only the exec bit, so
# that change would show up in nothing.
	@go run ./internal/config/example > '$(EXAMPLE_TMP)' \
	  && test -s '$(EXAMPLE_TMP)' \
	  && chmod 644 '$(EXAMPLE_TMP)' \
	  && mv '$(EXAMPLE_TMP)' lerp.example.toml \
	  && printf 'regenerated lerp.example.toml\n' \
	  || { rm -f '$(EXAMPLE_TMP)'; exit 1; }

# --------------------------------------------------------------------------
# Casts: the README's GIF, and the docs site's videos
# --------------------------------------------------------------------------

# Where the demo harness builds to. render-tape puts this dir on PATH so a
# cast records `lerp`, not a path into a build directory.
DEMO_BIN := .demo
TAPES_DIR := docs/tapes
# Where vhs writes before any cap is checked. Gitignored (see .gitignore's
# /.demo/), and render-tape empties it before every render, so measuring what
# lands here is an existence check too — a vhs that exited 0 without writing
# anything cannot pass by measuring a previous render.
DEMO_RENDER_DIR := $(DEMO_BIN)/out
# Where the harness leaves its own exit status. render-tape exports this path
# as LERP_DEMO_EXIT; the two spellings have to agree.
DEMO_EXIT := $(DEMO_BIN)/exit
DEMO_GIF := docs/demo.gif
OG_PNG := docs/static/og.png
CASTS_DIR := docs/static/casts
# GIF bytes are not reproducible, so nothing here diffs them. The caps are the
# only thing standing between "a couple of MB" and drift. mp4/webm get a
# smaller cap per file since a cast plays two of them; LERP-132 is where that
# knob gets argued with. og.png is the social card, rendered alongside the
# GIF by demo.tape's Screenshot; a TUI change wants `make demo` re-run.
DEMO_MAX_BYTES := 3145728
OG_MAX_BYTES := 524288
CAST_MAX_BYTES := 2097152

# Renders docs/tapes/$(TAPE).tape into $(DEMO_RENDER_DIR), gated on the
# harness's own exit status — nothing about file size, which the caller
# checks per output file afterward, since a GIF and an mp4 carry different
# caps. Shared by `demo` and `casts` through recursive make ($(MAKE)
# render-tape TAPE=name) so the two renders cannot drift apart: the same
# command builds the harness, runs vhs with PATH and LERP_DEMO_EXIT set on
# its own environment (which is what lets a sourced house.tape's `Require
# lerp` see it, and retires the tape's old habit of exporting both into the
# recorded shell by hand), and reads back the harness's own exit status.
# Not meant to be run directly.
.PHONY: render-tape
render-tape:
	@test -n "$(TAPE)" || { echo 'render-tape: TAPE is required'; exit 1; }
	@command -v vhs >/dev/null || { \
	  echo 'render-tape: vhs is not installed — see https://github.com/charmbracelet/vhs'; \
	  exit 1; }
	go build -o $(DEMO_BIN)/lerp ./internal/demo
	rm -rf $(DEMO_RENDER_DIR)
	mkdir -p $(DEMO_RENDER_DIR)
	rm -f $(DEMO_EXIT)
	PATH="$(CURDIR)/$(DEMO_BIN):$$PATH" LERP_DEMO_EXIT="$(CURDIR)/$(DEMO_EXIT)" \
	  vhs $(TAPES_DIR)/$(TAPE).tape
# The harness runs inside the terminal vhs is recording, so its exit code
# reaches bash and stops there: vhs exits 0 whether the board opened or the
# harness died at startup, and a cast of a bash error still renders under any
# cap. This is that exit code, carried out of the recording in a file the
# Makefile points the harness at above. Removed before the render, so a
# previous tape's status cannot answer for this one; missing means the
# harness never got far enough to write it, which is a failure too.
	@status=$$(cat $(DEMO_EXIT) 2>/dev/null); \
	  if [ -z "$$status" ]; then \
	    printf 'render-tape: %s — the harness never reported an exit status — it crashed, it never started (is lerp on PATH?), or it was still shutting down when the tape ended\n' \
	      '$(TAPE)'; exit 1; \
	  elif [ "$$status" != 0 ]; then \
	    printf 'render-tape: %s — the harness exited %s, a recording of that and not of lerp\n' \
	      '$(TAPE)' "$$status"; exit 1; \
	  fi

.PHONY: demo
demo: ## Re-record docs/demo.gif and docs/static/og.png from docs/tapes/demo.tape (needs vhs)
	$(MAKE) render-tape TAPE=demo
# Moved into place only once it is under the cap, so a render that fails or
# comes back oversized leaves the committed asset exactly where it was.
	@size=$$(wc -c < $(DEMO_RENDER_DIR)/demo.gif | tr -d ' '); \
	  test "$$size" -le $(DEMO_MAX_BYTES) || { \
	    printf 'demo: %s came back %s bytes, over the %s cap — shorten the tape or drop the framerate (left at %s)\n' \
	      '$(DEMO_GIF)' "$$size" '$(DEMO_MAX_BYTES)' '$(DEMO_RENDER_DIR)/demo.gif'; exit 1; }; \
	  mv $(DEMO_RENDER_DIR)/demo.gif $(DEMO_GIF) && \
	  printf 'rendered %s (%s bytes)\n' '$(DEMO_GIF)' "$$size"
	@size=$$(wc -c < $(DEMO_RENDER_DIR)/og.png | tr -d ' '); \
	  test "$$size" -le $(OG_MAX_BYTES) || { \
	    printf 'demo: %s came back %s bytes, over the %s cap (left at %s)\n' \
	      '$(OG_PNG)' "$$size" '$(OG_MAX_BYTES)' '$(DEMO_RENDER_DIR)/og.png'; exit 1; }; \
	  mv $(DEMO_RENDER_DIR)/og.png $(OG_PNG) && \
	  printf 'rendered %s (%s bytes)\n' '$(OG_PNG)' "$$size"

# The GIF cap is checked here too, even though nothing under docs/ needs the
# file itself: only demo.tape declares one (see its Output lines), and `demo`
# is not on any CI path, so a cap only `make demo` checked would be a cap
# nobody but a human running it locally ever hit — a PR that grows the
# README's tape past DEMO_MAX_BYTES would go green here and fail only later,
# on whoever next runs `make demo` to refresh the committed asset.
.PHONY: casts
casts: ## Render every tape under docs/tapes/ into docs/static/casts/ (needs vhs)
	@rm -rf $(CASTS_DIR)
	@mkdir -p $(CASTS_DIR)
	@for f in $(TAPES_DIR)/*.tape; do \
	  name=$$(basename "$$f" .tape); \
	  test "$$name" = "house" && continue; \
	  $(MAKE) render-tape TAPE="$$name" || exit 1; \
	  if [ -e $(DEMO_RENDER_DIR)/$$name.gif ]; then \
	    size=$$(wc -c < $(DEMO_RENDER_DIR)/$$name.gif | tr -d ' '); \
	    test "$$size" -le $(DEMO_MAX_BYTES) || { \
	      printf 'casts: %s.gif came back %s bytes, over the %s cap\n' \
	        "$$name" "$$size" '$(DEMO_MAX_BYTES)'; exit 1; }; \
	  fi; \
	  for out in $(DEMO_RENDER_DIR)/$$name.mp4 $(DEMO_RENDER_DIR)/$$name.webm; do \
	    test -e "$$out" || continue; \
	    size=$$(wc -c < "$$out" | tr -d ' '); \
	    test "$$size" -le $(CAST_MAX_BYTES) || { \
	      printf 'casts: %s came back %s bytes, over the %s cap\n' \
	        "$$out" "$$size" '$(CAST_MAX_BYTES)'; exit 1; }; \
	    cp "$$out" $(CASTS_DIR)/; \
	  done; \
	  printf 'rendered %s\n' "$$name"; \
	done
# The rot check house.tape's Require and demo.tape's Wait+Screen lines do not
# cover: a `{{< cast >}}` shortcode (docs/layouts/_shortcodes/cast.html)
# names its webm/mp4 by path from the site root, and every one of those paths
# must resolve to a file this run just staged — a page embedding a cast no
# tape renders is otherwise a silently blank frame on the published site.
# Nothing to find until a page calls the shortcode; it is here so it exists
# before one does.
	@missing=0; \
	  for ref in $$(grep -rhoE '(webm|mp4)="casts/[^"]+"' docs/content 2>/dev/null \
	                | sed -E 's/.*"casts\/([^"]+)"/\1/'); do \
	    test -e "$(CASTS_DIR)/$$ref" || { \
	      echo "casts: docs/content references casts/$$ref, which no tape rendered"; \
	      missing=1; }; \
	  done; \
	  test "$$missing" -eq 0

# --------------------------------------------------------------------------
# The docs site
# --------------------------------------------------------------------------

# The one place Hugo's version is pinned. CI reads it back out of here
# (`make -s hugo-version` in .github/workflows/docs.yml) rather than naming a
# version of its own: a site that builds locally and breaks on release day is
# what two pins drifting apart looks like.
HUGO_VERSION := 0.165.0

.PHONY: hugo-version
hugo-version: ## Print the pinned Hugo version (CI reads this)
	@printf '%s\n' '$(HUGO_VERSION)'

# A locally installed hugo is whatever the package manager last gave you, and
# asking a contributor to hold a specific one to edit a markdown file is not
# worth it — so a mismatch says so and builds anyway. CI is what builds on
# the pin, and CI is what deploys.
define hugo-preflight
@command -v hugo >/dev/null || { \
  printf 'docs: hugo not found — brew install hugo (CI builds with %s)\n' '$(HUGO_VERSION)'; \
  exit 1; }
@hugo version | grep -qF 'v$(HUGO_VERSION)' || \
  printf 'docs: local hugo is not the pinned %s — CI builds with the pin\n' '$(HUGO_VERSION)'
endef

.PHONY: docs
docs: ## Build the docs site into docs/public (needs hugo)
	$(hugo-preflight)
	hugo --source docs

.PHONY: docs-serve
docs-serve: ## Serve the docs site at http://localhost:1313/ with live reload
	$(hugo-preflight)
	hugo server --source docs --baseURL http://localhost:1313/
