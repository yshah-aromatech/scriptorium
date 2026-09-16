package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/config"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

func TestMCPEnabled(t *testing.T) {
	t.Setenv("MCP_AUTH_TOKEN", "legacy-token")
	t.Setenv("MCP_ENABLED", "")
	if err := os.Unsetenv("MCP_ENABLED"); err != nil {
		t.Fatal(err)
	}
	if enabled, err := config.MCPEnabled(); !enabled || err != nil {
		t.Fatalf("absent: %v, %v", enabled, err)
	}
	t.Setenv("MCP_AUTH_TOKEN", "")
	if enabled, err := config.MCPEnabled(); enabled || err != nil {
		t.Fatalf("absent without token: %v, %v", enabled, err)
	}
	for _, tc := range []struct {
		value            string
		enabled, invalid bool
	}{
		{"true", true, false}, {"false", false, false}, {"1", true, false}, {"", false, true}, {"typo", false, true},
	} {
		t.Setenv("MCP_ENABLED", tc.value)
		enabled, err := config.MCPEnabled()
		if enabled != tc.enabled || (err != nil) != tc.invalid {
			t.Fatalf("%q: %v, %v", tc.value, enabled, err)
		}
	}
}

func TestMCPEnvDiskAndOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCP_ENABLED", "true")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("MCP_ENABLED=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.LoadAppEnv(dir, secret.NewRegistry()); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := config.MCPEnabled(); !enabled {
		t.Fatal("exported value must win")
	}
	if err := os.Unsetenv("MCP_ENABLED"); err != nil {
		t.Fatal(err)
	}
	if err := config.LoadAppEnv(dir, secret.NewRegistry()); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := config.MCPEnabled(); enabled {
		t.Fatal("fresh process must read disk value")
	}
}
