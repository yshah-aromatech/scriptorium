package mcp_test

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/mcp"
)

// ---------------------------------------------------------------------
// 1. ServiceUnit — byte golden (§11.10, ruling 3: ExecStart is the one
// deliberate change from the PS original).
// ---------------------------------------------------------------------

func TestServiceUnitGolden(t *testing.T) {
	got := mcp.ServiceUnit("/opt/scriptorium", "/usr/local/bin/scriptorium")
	want := "[Unit]\n" +
		"Description=scriptorium MCP server\n" +
		"After=network.target\n" +
		"\n" +
		"[Service]\n" +
		"ExecStart=/usr/local/bin/scriptorium --mcp\n" +
		"WorkingDirectory=/opt/scriptorium\n" +
		"# system units without User= don't set HOME, and the app expands ~/.scriptorium\n" +
		"# with it — %h is the service manager's home (/root for the system manager)\n" +
		"Environment=HOME=%h\n" +
		"Restart=always\n" +
		"RestartSec=5\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
	if got != want {
		t.Errorf("ServiceUnit() =\n%q\nwant\n%q", got, want)
	}
}

// ---------------------------------------------------------------------
// 2. Install() — token guard, no side effects on failure.
// ---------------------------------------------------------------------

func TestInstallRequiresTokenBeforeAnySideEffect(t *testing.T) {
	calls := 0
	in := &mcp.Installer{
		Root: t.TempDir(),
		Run:  func(string, ...string) error { calls++; return nil },
	}
	err := in.Install("/opt/scriptorium", "/usr/local/bin/scriptorium", "")
	if err == nil {
		t.Fatal("Install with empty token = nil error, want an error")
	}
	want := "MCP_AUTH_TOKEN is not set — add it to .env next to the app first (the service would just crash-loop without it)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if calls != 0 {
		t.Errorf("Run called %d times, want 0", calls)
	}
}

// ---------------------------------------------------------------------
// 3. Root-scope install: exact command sequence, unit bytes, no
// enable --now, no linger.
// ---------------------------------------------------------------------

func TestInstallRootPath(t *testing.T) {
	root := t.TempDir()
	var calls []string
	in := &mcp.Installer{
		Root:   root,
		IsRoot: func() bool { return true },
		Run: func(bin string, args ...string) error {
			calls = append(calls, bin+" "+strings.Join(args, " "))
			return nil
		},
		Out: func(string) {},
	}
	if err := in.Install("/opt/scriptorium", "/usr/local/bin/scriptorium", "tok"); err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{
		"systemctl daemon-reload",
		"systemctl enable scriptorium-mcp",
		"systemctl restart scriptorium-mcp",
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
	for i, w := range wantCalls {
		if calls[i] != w {
			t.Errorf("call[%d] = %q, want %q", i, calls[i], w)
		}
	}
	for _, forbidden := range []string{"enable --now", "--user"} {
		for _, c := range calls {
			if strings.Contains(c, forbidden) {
				t.Errorf("root-scope call %q must not contain %q", c, forbidden)
			}
		}
	}

	unitFile := filepath.Join(root, "/etc/systemd/system/scriptorium-mcp.service")
	got, err := os.ReadFile(unitFile)
	if err != nil {
		t.Fatalf("unit file not written at %s: %v", unitFile, err)
	}
	if string(got) != mcp.ServiceUnit("/opt/scriptorium", "/usr/local/bin/scriptorium") {
		t.Errorf("unit file content mismatch:\n%s", got)
	}
}

// ---------------------------------------------------------------------
// 4. Non-root install: user unit path, --user flag throughout, linger.
// ---------------------------------------------------------------------

func TestInstallUserPath(t *testing.T) {
	root := t.TempDir()
	var calls []string
	in := &mcp.Installer{
		Root:     root,
		IsRoot:   func() bool { return false },
		HomeDir:  func() (string, error) { return "/home/sto", nil },
		Username: func() string { return "sto" },
		Run: func(bin string, args ...string) error {
			calls = append(calls, bin+" "+strings.Join(args, " "))
			return nil
		},
		Out: func(string) {},
	}
	if err := in.Install("/opt/scriptorium", "/usr/local/bin/scriptorium", "tok"); err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable scriptorium-mcp",
		"systemctl --user restart scriptorium-mcp",
		"loginctl enable-linger sto",
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
	for i, w := range wantCalls {
		if calls[i] != w {
			t.Errorf("call[%d] = %q, want %q", i, calls[i], w)
		}
	}

	unitFile := filepath.Join(root, "/home/sto/.config/systemd/user/scriptorium-mcp.service")
	got, err := os.ReadFile(unitFile)
	if err != nil {
		t.Fatalf("unit file not written at %s: %v", unitFile, err)
	}
	if string(got) != mcp.ServiceUnit("/opt/scriptorium", "/usr/local/bin/scriptorium") {
		t.Errorf("unit file content mismatch:\n%s", got)
	}
}

// TestLingerIsUserScopeOnly is the explicit negative: a root install must
// never call loginctl at all.
func TestLingerIsUserScopeOnly(t *testing.T) {
	root := t.TempDir()
	var calls []string
	in := &mcp.Installer{
		Root:   root,
		IsRoot: func() bool { return true },
		Run: func(bin string, args ...string) error {
			calls = append(calls, bin)
			return nil
		},
		Out: func(string) {},
	}
	if err := in.Install("/opt/scriptorium", "/usr/local/bin/scriptorium", "tok"); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c == "loginctl" {
			t.Fatal("root-scope install must never call loginctl")
		}
	}
}

// ---------------------------------------------------------------------
// 5. Legacy (pre-rename) unit retirement, both scopes: disable --now +
// delete, BEFORE the new unit is written; ordering matters (a leftover
// legacy service must not still fight over the port).
// ---------------------------------------------------------------------

func TestInstallRetiresLegacyUnitBothScopes(t *testing.T) {
	for _, scope := range []string{"root", "user"} {
		t.Run(scope, func(t *testing.T) {
			root := t.TempDir()
			var legacyPath string
			if scope == "root" {
				legacyPath = filepath.Join(root, "/etc/systemd/system/psscripts-mcp.service")
			} else {
				legacyPath = filepath.Join(root, "/home/sto/.config/systemd/user/psscripts-mcp.service")
			}
			if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacyPath, []byte("legacy unit"), 0o644); err != nil {
				t.Fatal(err)
			}

			var calls []string
			in := &mcp.Installer{
				Root:     root,
				IsRoot:   func() bool { return scope == "root" },
				HomeDir:  func() (string, error) { return "/home/sto", nil },
				Username: func() string { return "sto" },
				Run: func(bin string, args ...string) error {
					calls = append(calls, bin+" "+strings.Join(args, " "))
					return nil
				},
				Out: func(string) {},
			}
			if err := in.Install("/opt/scriptorium", "/usr/local/bin/scriptorium", "tok"); err != nil {
				t.Fatal(err)
			}

			if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
				t.Errorf("legacy unit file %s still exists", legacyPath)
			}
			wantDisable := "systemctl disable --now psscripts-mcp"
			if scope == "user" {
				wantDisable = "systemctl --user disable --now psscripts-mcp"
			}
			if calls[0] != wantDisable {
				t.Fatalf("first call = %q, want the legacy retirement %q (ordering)", calls[0], wantDisable)
			}
		})
	}
}

// TestInstallNoLegacyUnitSkipsRetirement: absent legacy unit -> no disable
// call at all, straight to the new-unit sequence.
func TestInstallNoLegacyUnitSkipsRetirement(t *testing.T) {
	root := t.TempDir()
	var calls []string
	in := &mcp.Installer{
		Root:   root,
		IsRoot: func() bool { return true },
		Run: func(bin string, args ...string) error {
			calls = append(calls, bin+" "+strings.Join(args, " "))
			return nil
		},
		Out: func(string) {},
	}
	if err := in.Install("/opt/scriptorium", "/usr/local/bin/scriptorium", "tok"); err != nil {
		t.Fatal(err)
	}
	if calls[0] != "systemctl daemon-reload" {
		t.Errorf("first call = %q, want daemon-reload (no legacy unit present)", calls[0])
	}
}

// ---------------------------------------------------------------------
// 6. Real loopback serve smoke — mcp.New + Serve over an actual
// net.Listener (127.0.0.1:0, never a fixed/public port).
// ---------------------------------------------------------------------

func TestRealLoopbackServeSmoke(t *testing.T) {
	a := newTestApp(t)
	srv, err := mcp.New(&mcp.Ops{App: a}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()

	base := "http://" + l.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	reqBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	tools, _ := env["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 12 {
		t.Errorf("tools/list over the real listener returned %d tools, want 12", len(tools))
	}

	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v after a clean Close, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after the listener was closed")
	}
}
