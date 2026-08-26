// Command example writes the shipped lerp.example.toml to stdout. The
// example is a derived artifact — the stock template rendered for one team
// with the permission grant accepted — and this is what derives it:
//
//	go run ./internal/config/example > lerp.example.toml
//
// TestStockMatchesExample pins the committed file to this output, so the
// only way to change the example is to change the template and rerun this.
// Hand-editing the example is how it drifted before the pin existed.
package main

import (
	"fmt"
	"os"

	"github.com/mattwalters/lerp/internal/config"
)

func main() {
	if _, err := fmt.Print(config.StockRepoConfig([]string{"LERP"}, true)); err != nil {
		fmt.Fprintln(os.Stderr, "example:", err)
		os.Exit(1)
	}
}
