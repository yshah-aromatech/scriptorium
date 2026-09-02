package cli_test

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/cli"
)

// ---------------------------------------------------------------------
// --mcp: the exact PS error string (scriptorium.ps1:102) when
// MCP_AUTH_TOKEN is unset, before any socket is ever touched.
// ---------------------------------------------------------------------

func TestMcpNoTokenExitsOneWithExactMessage(t *testing.T) {
	setupApp(t)
	t.Setenv("MCP_AUTH_TOKEN", "")

	var out, errw bytes.Buffer
	code := cli.Main([]string{"--mcp"}, &out, &errw)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	want := "MCP_AUTH_TOKEN is not set — add it to .env next to this script (see .env.example). Refusing to start an unauthenticated server."
	if strings.TrimSpace(errw.String()) != want {
		t.Errorf("stderr = %q, want %q", errw.String(), want)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty (no listener should ever have started)", out.String())
	}
}

// ---------------------------------------------------------------------
// --install-mcp-service: the Linux-only guard. Exercised on whatever
// platform actually runs this test suite — real root/non-root install
// LOGIC is tested exhaustively (and platform-independently) in
// internal/mcp/service_test.go via the injected Installer; this test is
// only the CLI-level OS guard sitting in front of it.
// ---------------------------------------------------------------------

func TestInstallMcpServiceIsLinuxOnly(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this guard only fires off-Linux; on Linux the call proceeds into the real (root-requiring) install flow, out of scope for a unit test")
	}
	setupApp(t)
	t.Setenv("MCP_AUTH_TOKEN", "a-token-value-1234")

	var out, errw bytes.Buffer
	code := cli.Main([]string{"--install-mcp-service"}, &out, &errw)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	want := "--install-mcp-service needs systemd (Linux only)"
	if strings.TrimSpace(errw.String()) != want {
		t.Errorf("stderr = %q, want %q", errw.String(), want)
	}
}
