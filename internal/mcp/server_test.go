package mcp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/app"
	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/mcp"
	"github.com/yshah-aromatech/scriptorium/internal/psfixtures"
)

const testToken = "test-token-abc123"

// newTestApp opens a real *app.App rooted at an isolated temp dir, with a
// fake crontab runner so NOTHING in this package ever touches the real
// crontab.
func newTestApp(t *testing.T) *app.App {
	t.Helper()
	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	cfgJSON := fmt.Sprintf(`{"dataDir":%q}`, dataDir)
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCrontab := func(string, ...string) (string, bool) { return "", true }
	a, err := app.OpenWith(appDir, fakeCrontab)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func newTestServer(t *testing.T) (*mcp.Server, *app.App) {
	t.Helper()
	a := newTestApp(t)
	srv, err := mcp.New(&mcp.Ops{App: a}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	return srv, a
}

func doRequest(t *testing.T, h http.Handler, method, path, token string, body []byte) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// ---------------------------------------------------------------------
// 0. New() / construction.
// ---------------------------------------------------------------------

func TestNewRejectsEmptyToken(t *testing.T) {
	if _, err := mcp.New(&mcp.Ops{}, ""); err == nil {
		t.Fatal("New with empty token = nil error, want an error")
	}
}

// ---------------------------------------------------------------------
// 1. Transport: endpoints, methods, auth, cap (§11.1-§11.4, §11.6).
// ---------------------------------------------------------------------

func TestHealthzIsUnauthenticated(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doRequest(t, srv.Handler(), http.MethodGet, "/healthz", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain" {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

func TestUnknownPathIs404(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doRequest(t, srv.Handler(), http.MethodGet, "/nope", testToken, nil)
	assertJSONBody(t, resp, 404, `{"error":"not found"}`)
}

func TestBareSlashIsAcceptedAsMcpEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/", testToken, body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestWrongMethodOnMcpIs405(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, m := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		resp := doRequest(t, srv.Handler(), m, "/mcp", testToken, nil)
		assertJSONBody(t, resp, 405, `{"error":"method not allowed"}`)
	}
}

func TestUnauthorizedIs401(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, tok := range []string{"", "wrong-token", "Bearer-shaped-but-wrong"} {
		resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", tok, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		assertJSONBody(t, resp, 401, `{"error":"unauthorized"}`)
	}
}

func TestAuthTokenComparisonIsCaseSensitive(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", strings.ToUpper(testToken), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	assertJSONBody(t, resp, 401, `{"error":"unauthorized"}`)
}

func TestOversizedBodyIs413ViaContentLengthFastPath(t *testing.T) {
	srv, _ := newTestServer(t)
	big := bytes.Repeat([]byte("a"), (1<<20)+1)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", testToken, big)
	assertJSONBody(t, resp, 413, `{"error":"payload too large"}`)
}

// TestOversizedBodyIs413ViaBoundedReadWhenContentLengthLies exercises the
// defensive bounded-read path: a request whose declared Content-Length is
// small but whose actual body exceeds the cap must still be rejected (a
// chunked request reports -1, so the fast path alone is not enough).
func TestOversizedBodyIs413ViaBoundedReadWhenContentLengthLies(t *testing.T) {
	srv, _ := newTestServer(t)
	big := bytes.Repeat([]byte("a"), (1<<20)+1)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.ContentLength = -1 // as a chunked request would report
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assertJSONBody(t, rec.Result(), 413, `{"error":"payload too large"}`)
}

// TestAuthPrecedesBodyCap is the ordering assertion: an oversized body with
// a BAD token must come back 401, not 413 — auth is checked before either
// cap enforcement (§11.3: "before reading the body").
func TestAuthPrecedesBodyCap(t *testing.T) {
	srv, _ := newTestServer(t)
	big := bytes.Repeat([]byte("a"), (1<<20)+1)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", "wrong-token", big)
	assertJSONBody(t, resp, 401, `{"error":"unauthorized"}`)
}

func TestPanicInsideDispatchBecomes500(t *testing.T) {
	// A nil App makes any real tool call panic — proving the top-level
	// recover produces the listener-level 500, distinct from a JSON-RPC
	// -32603 (which stays HTTP 200).
	srv, err := mcp.New(&mcp.Ops{}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_scripts","arguments":{}}}`)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", testToken, body)
	assertJSONBody(t, resp, 500, `{"error":"internal"}`)
}

func assertJSONBody(t *testing.T, resp *http.Response, wantStatus int, wantBody string) {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Errorf("status = %d, want %d (body %s)", resp.StatusCode, wantStatus, body)
	}
	if strings.TrimSpace(string(body)) != wantBody {
		t.Errorf("body = %q, want %q", body, wantBody)
	}
}

// ---------------------------------------------------------------------
// 2. Notifications: 202, empty body, presence-check not null-check.
// ---------------------------------------------------------------------

func TestNotificationReturns202Empty(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", testToken, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 202 {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want empty", body)
	}
}

// TestExplicitNullIDIsNotANotification: `id` present as JSON null is a real
// (if unusual) request — ContainsKey, not a null-check.
func TestExplicitNullIDIsNotANotification(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", testToken, []byte(`{"jsonrpc":"2.0","id":null,"method":"ping"}`))
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if _, ok := got["id"]; !ok {
		t.Errorf("response missing id key entirely: %v", got)
	}
	if got["id"] != nil {
		t.Errorf("id = %v, want null echoed back", got["id"])
	}
}

// ---------------------------------------------------------------------
// 3. Array bodies (ruling 2 — Go-only, informational; byte shape NOT
// gated against the PS fixture for pair 11).
// ---------------------------------------------------------------------

func TestArrayBodyRejectedWithMinus32600(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", testToken, []byte(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`))
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (JSON-RPC errors ride 200)", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	errObj, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error object: %v", got)
	}
	if code, _ := errObj["code"].(float64); code != -32600 {
		t.Errorf("error.code = %v, want -32600", errObj["code"])
	}
}

// ---------------------------------------------------------------------
// 4. Status / JSON-RPC error code matrix (§11.6, §11.7 — every row).
// ---------------------------------------------------------------------

func TestStatusAndErrorCodeMatrix(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	cases := []struct {
		name       string
		method     string
		path       string
		token      string
		body       string
		wantStatus int
		wantRPC    int // 0 means "not a JSON-RPC error envelope"
	}{
		{"healthz", http.MethodGet, "/healthz", "", "", 200, 0},
		{"unknown path", http.MethodGet, "/nope", testToken, "", 404, 0},
		{"wrong method", http.MethodGet, "/mcp", testToken, "", 405, 0},
		{"unauthorized", http.MethodPost, "/mcp", "bad", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, 401, 0},
		{"parse error: not json", http.MethodPost, "/mcp", testToken, "not json at all", 200, -32700},
		{"parse error: scalar body", http.MethodPost, "/mcp", testToken, "42", 200, -32700},
		{"invalid request: missing method", http.MethodPost, "/mcp", testToken, `{"jsonrpc":"2.0","id":5}`, 200, -32600},
		{"array body", http.MethodPost, "/mcp", testToken, `[1,2]`, 200, -32600},
		{"method not found", http.MethodPost, "/mcp", testToken, `{"jsonrpc":"2.0","id":1,"method":"nope"}`, 200, -32601},
		{"missing tool name", http.MethodPost, "/mcp", testToken, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`, 200, -32602},
		{"unknown tool", http.MethodPost, "/mcp", testToken, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`, 200, -32602},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doRequest(t, h, tc.method, tc.path, tc.token, []byte(tc.body))
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.wantRPC == 0 {
				return
			}
			var env map[string]any
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("body not JSON: %s: %v", body, err)
			}
			errObj, ok := env["error"].(map[string]any)
			if !ok {
				t.Fatalf("no error object in %s", body)
			}
			if code, _ := errObj["code"].(float64); int(code) != tc.wantRPC {
				t.Errorf("error.code = %v, want %d", errObj["code"], tc.wantRPC)
			}
		})
	}
}

// plantedLeakMarker is registered as a fake "secret" whose VALUE is a
// substring of a real, fixed internal error message (cron.Crontab's
// read-failure text) — not an attacker-controlled string (none of ops.go's
// three `err`-producing paths carry one; see I-2 for why). Registering a
// chunk of the ACTUAL text that Sec.Redact must scrub is what makes the
// leak tests below traverse the real redaction call sites
// (rpc.go's -32603 branch, api.go's 500 branch) instead of re-testing
// internal/secret.Redact directly: if either call site's `Sec.Redact(...)`
// were ever deleted, this exact substring would survive verbatim on the
// wire and the assertion would fail.
const plantedLeakMarker = "unmanaged entries would be destroyed"

// newFailingCrontabApp is a real *app.App wired to a crontab runner whose
// reads always fail — every schedule tool's Cron.Set/Remove call returns
// cron's fixed "crontab read failed — refusing to write (unmanaged entries
// would be destroyed)" error, the one dispatch-level exception this suite
// can reliably trigger without touching a real crontab.
func newFailingCrontabApp(t *testing.T) *app.App {
	t.Helper()
	appDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(fmt.Sprintf(`{"dataDir":%q}`, dataDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	failingCrontab := func(string, ...string) (string, bool) { return "crontab: permission denied", false }
	a, err := app.OpenWith(appDir, failingCrontab)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.Paths.ScriptsDir, "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Paths.ScriptsDir, "hello", "main.ps1"), []byte("exit 0"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Sec.Add("PLANTED_TOKEN", plantedLeakMarker, true)
	return a
}

// TestDispatchExceptionBecomesMinus32603Redacted is the §11.7 -32603 row:
// a genuine dispatch-level exception (here, a crontab read failure inside
// set_schedule's Ops.Call — a real `err`, not a tool-reported isErr) rides
// HTTP 200 with a JSON-RPC error object, distinct from the 500 a PANIC
// produces (TestPanicInsideDispatchBecomes500). The message passes through
// the same Sec.Redact chokepoint every tool output does — proven by the
// planted marker (see plantedLeakMarker) never surviving verbatim.
func TestDispatchExceptionBecomesMinus32603Redacted(t *testing.T) {
	a := newFailingCrontabApp(t)
	srv, err := mcp.New(&mcp.Ops{App: a}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"set_schedule","arguments":{"script":"hello","cron":"@daily"}}}`)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", testToken, body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (JSON-RPC errors ride 200, not the transport's own 500)", resp.StatusCode)
	}
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error object (crontab read failure should have raised a dispatch exception): %v", env)
	}
	if code, _ := errObj["code"].(float64); int(code) != -32603 {
		t.Errorf("error.code = %v, want -32603", errObj["code"])
	}
	msg := errObj["message"].(string)
	if !strings.Contains(msg, "crontab read failed") {
		t.Errorf("message = %q, want it to carry the underlying error", msg)
	}
	if strings.Contains(msg, plantedLeakMarker) {
		t.Fatalf("planted marker leaked verbatim through the -32603 path (Sec.Redact was not applied): %q", msg)
	}
	if !strings.Contains(msg, "***") {
		t.Errorf("message = %q, want a redaction marker in place of the planted text", msg)
	}
}

func TestUnknownToolErrorListsAllValidNames(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doRequest(t, srv.Handler(), http.MethodPost, "/mcp", testToken,
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`))
	body, _ := io.ReadAll(resp.Body)
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	msg := env["error"].(map[string]any)["message"].(string)
	for _, name := range []string{"list_scripts", "get_script_details", "run_script", "get_history", "get_run_log",
		"sync_repos", "get_schedules", "set_schedule", "remove_schedule", "install_deps", "update_app", "update_packages"} {
		if !strings.Contains(msg, name) {
			t.Errorf("unknown-tool message missing %q: %s", name, msg)
		}
	}
}

// ---------------------------------------------------------------------
// 5. Fixture replay — the executable parity contract.
// ---------------------------------------------------------------------

// fixtureResponse is testdata/psfixtures/mcp/*.response.json's shape.
type fixtureResponse struct {
	StatusCode int     `json:"statusCode"`
	JSON       *string `json:"json"`
}

// TestFixtureReplay POSTs every recorded request (except pair 11, handled
// separately above as an informational Go-only assertion — ruling 2) and
// requires the response to deep-equal the recorded one, structurally
// (unmarshalled-and-compared, not byte-for-byte: JSON-RPC key order is not
// part of the contract). Two fields are legitimately volatile and are
// normalized before comparing:
//   - serverInfo.version (pairs 01/12): PS's Get-StoAppVersion is the
//     *PowerShell scriptorium checkout's* git short hash — an environment
//     fact of the machine that recorded the fixture, not a byte the Go
//     rebuild can or should reproduce. The Go equivalent (appVersion) reads
//     the same way from ITS OWN app dir, which in this test is a fresh temp
//     dir with no git history at all. Named explicitly in the fixtures'
//     README under "Volatile files" (it changes on every PS commit, not
//     just on a regeneration — a different axis of volatility than the
//     rest of that list).
//   - the get_history row's startedAt/logFile/logId/durationSec (pair 08):
//     the fixtures' own README lists this exact file under "Volatile
//     files" ("echoes the real-run row back") — regenerating the PS
//     fixtures changes these bytes on every run.
func TestFixtureReplay(t *testing.T) {
	fixDir, err := psfixtures.Dir()
	if err != nil {
		t.Fatal(err)
	}
	mcpDir := filepath.Join(fixDir, "mcp")

	a := newTestApp(t)
	// Seed exactly the shape pair 08 expects to see echoed back: one
	// successful run of a script named "fixture-script". The volatile
	// fields (startedAt/logFile/durationSec) are normalized away below, so
	// their actual values here are immaterial.
	dur := 0.3
	exit := 0
	logFile := filepath.Join(a.Paths.LogsDir, "fixture-script-seed.log")
	if err := a.Hist.Append(history.Row{
		Event: "script_run", Script: "fixture-script", Trigger: "manual", Status: "success",
		ExitCode: &exit, DurationSec: &dur, LogFile: &logFile,
	}); err != nil {
		t.Fatal(err)
	}

	srv, err := mcp.New(&mcp.Ops{App: a}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	for i := 1; i <= 12; i++ {
		if i == 11 {
			continue // ruling 2 — asserted separately, not against the PS fixture
		}
		i := i
		t.Run(fmt.Sprintf("pair%02d", i), func(t *testing.T) {
			reqPaths, err := filepath.Glob(filepath.Join(mcpDir, fmt.Sprintf("%02d-*.request.json", i)))
			if err != nil || len(reqPaths) != 1 {
				t.Fatalf("expected exactly one request fixture for pair %d, got %v (%v)", i, reqPaths, err)
			}
			reqBody, err := os.ReadFile(reqPaths[0])
			if err != nil {
				t.Fatal(err)
			}
			respPath := strings.TrimSuffix(reqPaths[0], ".request.json") + ".response.json"
			wantRaw, err := os.ReadFile(respPath)
			if err != nil {
				t.Fatal(err)
			}
			var want fixtureResponse
			if err := json.Unmarshal(wantRaw, &want); err != nil {
				t.Fatal(err)
			}

			token := testToken
			if i == 10 {
				token = "definitely-the-wrong-token" // pair 10: bad-token replay
			}
			resp := doRequest(t, h, http.MethodPost, "/mcp", token, reqBody)
			gotBody, _ := io.ReadAll(resp.Body)

			if resp.StatusCode != want.StatusCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, want.StatusCode)
			}
			if want.JSON == nil {
				if len(gotBody) != 0 {
					t.Fatalf("body = %q, want empty", gotBody)
				}
				return
			}
			comparePairJSON(t, i, gotBody, *want.JSON)
		})
	}
}

func comparePairJSON(t *testing.T, pair int, gotRaw []byte, wantRaw string) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(gotRaw, &got); err != nil {
		t.Fatalf("actual response is not JSON: %s: %v", gotRaw, err)
	}
	if err := json.Unmarshal([]byte(wantRaw), &want); err != nil {
		t.Fatalf("fixture response is not JSON: %s: %v", wantRaw, err)
	}

	switch pair {
	case 1, 12:
		zeroNested(got, "result", "serverInfo", "version")
		zeroNested(want, "result", "serverInfo", "version")
	case 8:
		normalizeHistoryPair(t, got)
		normalizeHistoryPair(t, want)
	}

	if !reflect.DeepEqual(got, want) {
		gotB, _ := json.Marshal(got)
		wantB, _ := json.Marshal(want)
		t.Errorf("pair %d mismatch:\n got:  %s\n want: %s", pair, gotB, wantB)
	}
}

// zeroNested overwrites a nested string field (when present) so it drops
// out of the DeepEqual comparison.
func zeroNested(root any, path ...string) {
	m, ok := root.(map[string]any)
	if !ok {
		return
	}
	for _, k := range path[:len(path)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			return
		}
		m = next
	}
	last := path[len(path)-1]
	if _, ok := m[last]; ok {
		m[last] = ""
	}
}

// normalizeHistoryPair digs into the tools/call envelope's
// result.content[0].text (itself a JSON-encoded string, per the MCP content
// shape), parses it, and drops the get_history row's volatile fields.
func normalizeHistoryPair(t *testing.T, root any) {
	t.Helper()
	m, ok := root.(map[string]any)
	if !ok {
		return
	}
	result, ok := m["result"].(map[string]any)
	if !ok {
		return
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		return
	}
	entry, ok := content[0].(map[string]any)
	if !ok {
		return
	}
	text, ok := entry["text"].(string)
	if !ok {
		return
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(text), &inner); err != nil {
		t.Fatalf("get_history text is not JSON: %s: %v", text, err)
	}
	runs, ok := inner["runs"].([]any)
	if !ok || len(runs) == 0 {
		return
	}
	row, ok := runs[0].(map[string]any)
	if !ok {
		return
	}
	for _, f := range []string{"startedAt", "logFile", "logId", "durationSec"} {
		delete(row, f)
	}
	// re-embed the normalized inner object directly (as a map, not a
	// re-serialized string) so both sides compare structurally regardless
	// of key order.
	entry["text"] = inner
}
