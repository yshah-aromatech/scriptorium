// Command scriptorium is the Go rebuild of the scriptorium script runner —
// the headless CLI surface today (a byte-for-byte port of scriptorium.ps1);
// the TUI and MCP server arrive in later phases.
package main

import (
	"os"

	"github.com/yshah-aromatech/scriptorium/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
