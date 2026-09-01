// Command scriptorium is the Go rebuild of the scriptorium script runner.
// Phase 0 stub: prints the version and exits; the real CLI arrives in P6.
package main

import (
	"fmt"

	"github.com/yshah-aromatech/scriptorium/internal/buildinfo"
)

func main() {
	fmt.Println("scriptorium (go rebuild, phase 0) " + buildinfo.Version)
}
