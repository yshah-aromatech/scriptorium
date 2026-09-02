package mcp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/mcp"
	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
)

func doAPI(t *testing.T, h http.Handler, method, path, token string, body []byte) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func decodeAPIBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("response body is not a JSON object: %v", err)
	}
	return m
}

// ---------------------------------------------------------------------
// 1. Auth / method — same gate as /mcp, before any route runs.
// ---------------------------------------------------------------------

func TestAPIRequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doAPI(t, srv.Handler(), http.MethodGet, "/api/v1/scripts", "", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAPIWrongMethodIs405(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doAPI(t, srv.Handler(), http.MethodPost, "/api/v1/scripts", testToken, nil)
	if resp.StatusCode != 405 {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestAPIUnknownRouteIs404(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doAPI(t, srv.Handler(), http.MethodGet, "/api/v1/nope", testToken, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------
// 2. Route table — every route, happy path + auth 401 + wrong method 405.
// ---------------------------------------------------------------------

func TestAPIRouteTable(t *testing.T) {
	srv, a := newTestServer(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`)
	h := srv.Handler()

	cases := []struct {
		name         string
		method, path string
		body         string
		wrongMethod  string
	}{
		{"list scripts", http.MethodGet, "/api/v1/scripts", "", http.MethodPost},
		{"script details", http.MethodGet, "/api/v1/scripts/hello", "", http.MethodPost},
		{"run script", http.MethodPost, "/api/v1/scripts/hello/run", "{}", http.MethodGet},
		{"install deps", http.MethodPost, "/api/v1/scripts/hello/deps/install", "", http.MethodGet},
		{"history", http.MethodGet, "/api/v1/history", "", http.MethodPost},
		{"sync", http.MethodPost, "/api/v1/sync", "", http.MethodGet},
		{"schedules list", http.MethodGet, "/api/v1/schedules", "", http.MethodPost},
		{"schedules set", http.MethodPut, "/api/v1/schedules/hello", `{"cron":"@daily"}`, http.MethodGet},
		{"schedules remove", http.MethodDelete, "/api/v1/schedules/hello", "", http.MethodGet},
		{"update app", http.MethodPost, "/api/v1/update/app", "", http.MethodGet},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// happy path: authenticated, status must not be 401/404/405
			resp := doAPI(t, h, tc.method, tc.path, testToken, []byte(tc.body))
			if resp.StatusCode == 401 || resp.StatusCode == 404 || resp.StatusCode == 405 {
				body, _ := decodeAPIBodyRaw(resp)
				t.Errorf("happy path status = %d, body %s", resp.StatusCode, body)
			}

			// auth 401
			resp = doAPI(t, h, tc.method, tc.path, "wrong-token", []byte(tc.body))
			if resp.StatusCode != 401 {
				t.Errorf("unauthenticated status = %d, want 401", resp.StatusCode)
			}

			// wrong method 405
			resp = doAPI(t, h, tc.wrongMethod, tc.path, testToken, nil)
			if resp.StatusCode != 405 {
				t.Errorf("wrong method (%s) status = %d, want 405", tc.wrongMethod, resp.StatusCode)
			}
		})
	}
}

func decodeAPIBodyRaw(resp *http.Response) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}

// ---------------------------------------------------------------------
// 3. Error mapping (ruling 1).
// ---------------------------------------------------------------------

func TestAPIUnknownScriptIs404(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doAPI(t, srv.Handler(), http.MethodGet, "/api/v1/scripts/nope", testToken, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body := decodeAPIBody(t, resp)
	if !strings.Contains(body["error"].(string), "unknown script") {
		t.Errorf("error = %v", body["error"])
	}
}

func TestAPIInvalidCronIs400WithExampleFormsMessage(t *testing.T) {
	srv, a := newTestServer(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`)
	resp := doAPI(t, srv.Handler(), http.MethodPut, "/api/v1/schedules/hello", testToken, []byte(`{"cron":"every tuesday"}`))
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeAPIBody(t, resp)
	if !strings.Contains(body["error"].(string), "@hourly") {
		t.Errorf("error = %v, want the example-forms message", body["error"])
	}
}

func TestAPIBadLogIDShapeIs400(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doAPI(t, srv.Handler(), http.MethodGet, "/api/v1/logs/../etc/passwd.log", testToken, nil)
	// the traversal segments make this a different (unmatched or
	// re-segmented) path; a straightforward invalid-shape id is the
	// unambiguous case:
	if resp.StatusCode != 404 && resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 or 404 for a traversal-shaped path", resp.StatusCode)
	}

	resp = doAPI(t, srv.Handler(), http.MethodGet, "/api/v1/logs/no-extension", testToken, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 for a badly-shaped (but not traversal) log_id", resp.StatusCode)
	}
}

func TestAPIRunResultIsAlways200EvenOnFailure(t *testing.T) {
	pwshtest.RequirePwsh(t)
	srv, a := newTestServer(t)
	writePS(t, a.Paths.DataDir, "failer", `exit 1`)
	resp := doAPI(t, srv.Handler(), http.MethodPost, "/api/v1/scripts/failer/run", testToken, []byte("{}"))
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (a run's own failure is not an HTTP error)", resp.StatusCode)
	}
	body := decodeAPIBody(t, resp)
	if body["status"] != "failure" {
		t.Errorf("status field = %v, want failure", body["status"])
	}
}

func TestAPISyncFailureIs200WithOkFalse(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := doAPI(t, srv.Handler(), http.MethodPost, "/api/v1/sync", testToken, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (the body's own ok field speaks)", resp.StatusCode)
	}
	body := decodeAPIBody(t, resp)
	if body["ok"] != false {
		t.Errorf("ok = %v, want false (no repo configured)", body["ok"])
	}
}

// TestAPIPanicIs500ViaTopLevelRecover: a nil App inside Ops makes any real
// call panic before ever reaching apiReply — proving the SAME top-level
// recover (server.go) that backs /mcp's 500 also covers /api/v1. This is
// deliberately distinct from apiReply's own `err != nil` branch (see
// TestAPIOpsExceptionIs500Redacted below), which this test never reaches.
func TestAPIPanicIs500ViaTopLevelRecover(t *testing.T) {
	srv, err := mcp.New(&mcp.Ops{}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	resp := doAPI(t, srv.Handler(), http.MethodGet, "/api/v1/scripts", testToken, nil)
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

// TestAPIOpsExceptionIs500Redacted is I-2's fix: reaches apiReply's own
// `err != nil` branch (api.go) — a real dispatch-level Ops error, not a
// panic — via a failing crontab runner behind PUT /api/v1/schedules/hello,
// and requires the planted marker (see plantedLeakMarker,
// server_test.go) to come back scrubbed, proving api.go's own
// `Sec.Redact(err.Error())` call actually runs.
func TestAPIOpsExceptionIs500Redacted(t *testing.T) {
	a := newFailingCrontabApp(t)
	srv, err := mcp.New(&mcp.Ops{App: a}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	resp := doAPI(t, srv.Handler(), http.MethodPut, "/api/v1/schedules/hello", testToken, []byte(`{"cron":"@daily"}`))
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	body := decodeAPIBody(t, resp)
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "crontab read failed") {
		t.Errorf("error = %q, want it to carry the underlying error", msg)
	}
	if strings.Contains(msg, plantedLeakMarker) {
		t.Fatalf("planted marker leaked verbatim through the API 500 path (Sec.Redact was not applied): %q", msg)
	}
	if !strings.Contains(msg, "***") {
		t.Errorf("error = %q, want a redaction marker in place of the planted text", msg)
	}
}

// ---------------------------------------------------------------------
// 4. Parity assertion: same Ops, so the API body and the MCP tool content
// must be byte-for-byte the same JSON for list/details/history.
// ---------------------------------------------------------------------

func TestAPIAndMCPShareTheSameOpsResult(t *testing.T) {
	srv, a := newTestServer(t)
	writePS(t, a.Paths.DataDir, "hello", `exit 0`)
	h := srv.Handler()

	mcpCall := func(tool string, args map[string]any) []byte {
		req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}}
		b, _ := json.Marshal(req)
		resp := doRequest(t, h, http.MethodPost, "/mcp", testToken, b)
		var env map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatal(err)
		}
		text := env["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		return []byte(text)
	}

	cases := []struct {
		name     string
		apiPath  string
		toolName string
		args     map[string]any
	}{
		{"list_scripts", "/api/v1/scripts", "list_scripts", map[string]any{}},
		{"get_script_details", "/api/v1/scripts/hello", "get_script_details", map[string]any{"script": "hello"}},
		{"get_history", "/api/v1/history", "get_history", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiResp := doAPI(t, h, http.MethodGet, tc.apiPath, testToken, nil)
			apiBody, _ := decodeAPIBodyRaw(apiResp)
			mcpBody := mcpCall(tc.toolName, tc.args)

			var apiAny, mcpAny any
			if err := json.Unmarshal(apiBody, &apiAny); err != nil {
				t.Fatalf("API body not JSON: %s", apiBody)
			}
			if err := json.Unmarshal(mcpBody, &mcpAny); err != nil {
				t.Fatalf("MCP text not JSON: %s", mcpBody)
			}
			apiJSON, _ := json.Marshal(apiAny)
			mcpJSON, _ := json.Marshal(mcpAny)
			if string(apiJSON) != string(mcpJSON) {
				t.Errorf("API and MCP disagree:\n API: %s\n MCP: %s", apiJSON, mcpJSON)
			}
		})
	}
}
