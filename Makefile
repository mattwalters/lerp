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
	  | awk -F':.*?## ' '{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

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
# The README cast
# --------------------------------------------------------------------------

# Where the demo harness builds to. The tape puts this dir on PATH so the cast
# records `lerp`, not a path into a build directory.
DEMO_BIN := .demo
DEMO_TAPE := docs/demo.tape
DEMO_GIF := docs/demo.gif
# Where vhs writes before the cap is checked; see the demo recipe.
DEMO_RENDER := $(DEMO_BIN)/demo.gif
# GIF bytes are not reproducible, so nothing here diffs them. The cap is the
# only thing standing between "a couple of MB" and drift.
DEMO_MAX_BYTES := 3145728

.PHONY: demo
demo: ## Re-record docs/demo.gif from docs/demo.tape (needs vhs)
	@command -v vhs >/dev/null || { \
	  echo 'demo: vhs is not installed — see https://github.com/charmbracelet/vhs'; \
	  exit 1; }
	go build -o $(DEMO_BIN)/lerp ./internal/demo
# Rendered into the scratch dir and moved into place only once it is under the
# cap. -o overrides the tape's own Output; the scratch dir is gitignored and
# never holds a GIF beforehand, so measuring it is an existence check too — a
# vhs that exited 0 without writing anything cannot pass by measuring the
# committed file. And a render that fails, or one that comes back oversized,
# leaves the committed asset exactly where it was — the rm is for the leftover
# an oversized render puts there, which a later silent vhs could measure.
	rm -f $(DEMO_RENDER)
	vhs -o $(DEMO_RENDER) $(DEMO_TAPE)
	@size=$$(wc -c < $(DEMO_RENDER) | tr -d ' '); \
	  test "$$size" -le $(DEMO_MAX_BYTES) || { \
	    printf 'demo: %s came back %s bytes, over the %s cap — shorten the tape or drop the framerate (left at %s)\n' \
	      '$(DEMO_GIF)' "$$size" '$(DEMO_MAX_BYTES)' '$(DEMO_RENDER)'; exit 1; }; \
	  mv $(DEMO_RENDER) $(DEMO_GIF) && \
	  printf 'rendered %s (%s bytes)\n' '$(DEMO_GIF)' "$$size"
