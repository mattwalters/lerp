// Command example writes the shipped lerp.example.toml to stdout. The
// example is a derived artifact — config.ExampleRepoConfig's output, which
// is the stock template rendered for one team with the permission grant
// accepted — and this is what derives it:
//
//	make example
//
// TestStockMatchesExample pins the committed file to this output, so the way
// to change the example is to change internal/config/stock.toml and rerun
// this. Hand-editing the example is how it drifted before the pin existed.
//
// It lives under internal/ so `go build ./...` compiles it and `go install
// ./cmd/lerp` never ships it, the same trade internal/demo makes. (`go
// install ./...` would drop a binary named `example` in GOBIN, as it builds
// every main in the module; `make install` is the documented path and names
// ./cmd/lerp.)
package main

import (
	"fmt"
	"os"

	"github.com/mattwalters/lerp/internal/config"
)

func main() {
	if _, err := fmt.Print(config.ExampleRepoConfig()); err != nil {
		fmt.Fprintln(os.Stderr, "example:", err)
		os.Exit(1)
	}
}
