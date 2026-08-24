// Command lerp orchestrates software work through Linear: tickets go
// on a board, lerp runs coding agents to move them across it.
package main

import (
	"fmt"

	"github.com/mattwalters/lerp/internal/version"
)

func main() {
	fmt.Printf("lerp %s\n", version.Version)
}
