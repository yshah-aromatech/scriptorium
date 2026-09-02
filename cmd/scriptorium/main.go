// Command scriptorium is the Go rebuild of the scriptorium script runner: the
// headless CLI (a byte-for-byte port of scriptorium.ps1's flag loop), the MCP
// server behind --mcp, and — on a bare invocation — the TUI.
package main

import (
	"os"

	"github.com/yshah-aromatech/scriptorium/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
