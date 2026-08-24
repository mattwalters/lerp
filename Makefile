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
check: ## What CI runs: gofmt, vet, build, test
	@test -z "$$(gofmt -l .)" || { echo 'unformatted files:'; gofmt -l .; exit 1; }
	go vet ./...
	go build ./...
	go test ./...

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -w .
