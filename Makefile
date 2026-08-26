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
# Where the harness leaves its own exit status. The tape exports this path as
# LERP_DEMO_EXIT; the two spellings have to agree.
DEMO_EXIT := $(DEMO_BIN)/exit
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
	rm -f $(DEMO_RENDER) $(DEMO_EXIT)
	vhs -o $(DEMO_RENDER) $(DEMO_TAPE)
# The harness runs inside the terminal vhs is recording, so its exit code
# reaches bash and stops there: vhs exits 0 whether the board opened or the
# harness died at startup, and a cast of a bash error still renders under the
# cap. This is that exit code, carried out of the recording in a file the tape
# points the harness at. Read before the size check so a failed run is named
# as a failed run rather than as an oversized GIF — and so it never reaches
# the mv. Removed above, so a previous render's status cannot answer for this
# one; missing means the harness never got far enough to write it, which is a
# failure too.
	@status=$$(cat $(DEMO_EXIT) 2>/dev/null); \
	  test "$$status" = 0 || { \
	    printf 'demo: the harness exited %s — the cast would be a recording of that, not of lerp (left at %s)\n' \
	      "$${status:-without reporting a status}" '$(DEMO_RENDER)'; exit 1; }
	@size=$$(wc -c < $(DEMO_RENDER) | tr -d ' '); \
	  test "$$size" -le $(DEMO_MAX_BYTES) || { \
	    printf 'demo: %s came back %s bytes, over the %s cap — shorten the tape or drop the framerate (left at %s)\n' \
	      '$(DEMO_GIF)' "$$size" '$(DEMO_MAX_BYTES)' '$(DEMO_RENDER)'; exit 1; }; \
	  mv $(DEMO_RENDER) $(DEMO_GIF) && \
	  printf 'rendered %s (%s bytes)\n' '$(DEMO_GIF)' "$$size"
