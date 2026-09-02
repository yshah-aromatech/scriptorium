package cli

import "testing"

// TestMcpBindAddrResolution and TestResolveMcpPortPrecedence are pure,
// socket-free checks of --mcp's binding/port resolution rules (§11
// activation: "config.mcpBind:mcpPort (--port override wins when >0)").
func TestMcpBindAddrResolution(t *testing.T) {
	cases := []struct {
		cfgBind string
		port    int
		want    string
	}{
		{"all", 8765, ":8765"},
		{"localhost", 8765, "127.0.0.1:8765"},
		{"", 9000, ":9000"}, // any other value behaves like "all"
	}
	for _, tc := range cases {
		if got := mcpBindAddr(tc.cfgBind, tc.port); got != tc.want {
			t.Errorf("mcpBindAddr(%q, %d) = %q, want %q", tc.cfgBind, tc.port, got, tc.want)
		}
	}
}

func TestResolveMcpPortPrecedence(t *testing.T) {
	cases := []struct{ cfgPort, override, want int }{
		{8765, 0, 8765},    // no --port given
		{8765, -1, 8765},   // never produced by parseFlags, still defensive
		{8765, 9999, 9999}, // --port wins when positive
	}
	for _, tc := range cases {
		if got := resolveMcpPort(tc.cfgPort, tc.override); got != tc.want {
			t.Errorf("resolveMcpPort(%d, %d) = %d, want %d", tc.cfgPort, tc.override, got, tc.want)
		}
	}
}
