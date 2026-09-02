// api.go is the REST surface — a Go-only addition (not in the PowerShell
// app) requested alongside the MCP server. It is co-hosted on the exact
// same listener, Bearer token and 1MB cap as /mcp (server.go enforces both
// before routing here), and every route is a thin mapper onto the shared
// Ops layer (ops.go) — the identical methods tools.go's tools/call
// dispatches onto. Neither frontend ever forks tool logic (ruling 1).
//
// Responses are the Ops result JSON directly — no JSON-RPC envelope.
package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// apiPrefix is every REST route's common prefix.
const apiPrefix = "/api/v1"

// serveAPI routes one request whose path (already stripped of apiPrefix,
// and of any trailing "/") and body have passed the shared auth/cap gate in
// serveHTTP.
func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	segs := splitPath(rest)
	m := r.Method

	var result any
	var isErr bool
	var err error
	switch {
	case len(segs) == 1 && segs[0] == "scripts" && m == http.MethodGet:
		result, isErr, err = s.ops.ListScripts()
	case len(segs) == 2 && segs[0] == "scripts" && m == http.MethodGet:
		result, isErr, err = s.ops.GetScriptDetails(pathArg("script", segs[1]))
	case len(segs) == 3 && segs[0] == "scripts" && segs[2] == "run" && m == http.MethodPost:
		args := decodeJSONObject(body)
		args["script"] = segs[1]
		result, isErr, err = s.ops.RunScript(args)
	case len(segs) == 4 && segs[0] == "scripts" && segs[2] == "deps" && segs[3] == "install" && m == http.MethodPost:
		result, isErr, err = s.ops.InstallDeps(pathArg("script", segs[1]))
	case len(segs) == 1 && segs[0] == "history" && m == http.MethodGet:
		result, isErr, err = s.ops.GetHistory(queryArgs(r, "script", "limit"))
	case len(segs) == 2 && segs[0] == "logs" && m == http.MethodGet:
		args := queryArgs(r, "tail_kb")
		args["log_id"] = segs[1]
		result, isErr, err = s.ops.GetRunLog(args)
	case len(segs) == 1 && segs[0] == "sync" && m == http.MethodPost:
		result, isErr, err = s.ops.SyncRepos()
	case len(segs) == 1 && segs[0] == "schedules" && m == http.MethodGet:
		result, isErr, err = s.ops.GetSchedules()
	case len(segs) == 2 && segs[0] == "schedules" && m == http.MethodPut:
		args := decodeJSONObject(body)
		args["script"] = segs[1]
		result, isErr, err = s.ops.SetSchedule(args)
	case len(segs) == 2 && segs[0] == "schedules" && m == http.MethodDelete:
		result, isErr, err = s.ops.RemoveSchedule(pathArg("script", segs[1]))
	case len(segs) == 2 && segs[0] == "update" && segs[1] == "app" && m == http.MethodPost:
		result, isErr, err = s.ops.UpdateApp()
	case len(segs) == 2 && segs[0] == "update" && segs[1] == "packages" && m == http.MethodPost:
		result, isErr, err = s.ops.UpdatePackages()
	default:
		if apiPathShapeKnown(segs) {
			writeText(w, http.StatusMethodNotAllowed, `{"error":"method not allowed"}`, "application/json")
		} else {
			writeText(w, http.StatusNotFound, `{"error":"not found"}`, "application/json")
		}
		return
	}
	s.apiReply(w, result, isErr, err)
}

// apiPathShapeKnown reports whether segs matches some route's shape
// regardless of method — the 404-vs-405 split.
func apiPathShapeKnown(segs []string) bool {
	switch {
	case len(segs) == 1 && (segs[0] == "scripts" || segs[0] == "history" || segs[0] == "sync" || segs[0] == "schedules"):
		return true
	case len(segs) == 2 && (segs[0] == "scripts" || segs[0] == "logs" || segs[0] == "schedules"):
		return true
	case len(segs) == 2 && segs[0] == "update" && (segs[1] == "app" || segs[1] == "packages"):
		return true
	case len(segs) == 3 && segs[0] == "scripts" && segs[2] == "run":
		return true
	case len(segs) == 4 && segs[0] == "scripts" && segs[2] == "deps" && segs[3] == "install":
		return true
	}
	return false
}

// apiReply maps one Ops call's (result, isErr, err) onto an HTTP response
// per ruling 1's error table:
//   - err != nil (a dispatch-level exception)      -> 500, redacted message
//   - result is *ToolError, NotFound               -> 404, its message
//   - result is *ToolError, !NotFound               -> 400, its message
//   - anything else (isErr true or false)          -> 200, the result JSON
//     as-is — run results are always 200, and an isErr result that carries
//     a structured body (sync_repos/install_deps/update_app/update_packages
//     reporting ok:false) speaks through its own fields, not the status code.
func (s *Server) apiReply(w http.ResponseWriter, result any, isErr bool, err error) {
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": s.ops.App.Sec.Redact(err.Error())})
		return
	}
	if te, ok := result.(*ToolError); ok {
		status := http.StatusBadRequest
		if te.NotFound {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": te.Message})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeText(w, http.StatusInternalServerError, `{"error":"internal"}`, "application/json")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// splitPath splits a leading-slash path into its non-empty segments;
// "" and "/" both yield no segments.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func pathArg(key, value string) map[string]any { return map[string]any{key: value} }

// queryArgs reads keys from the request's query string into a tool-argument
// map, omitting any key that was not actually present — preserving the
// ContainsKey-vs-absent distinction get_history's limit and get_run_log's
// tail_kb both depend on.
func queryArgs(r *http.Request, keys ...string) map[string]any {
	m := map[string]any{}
	q := r.URL.Query()
	for _, k := range keys {
		if q.Has(k) {
			m[k] = q.Get(k)
		}
	}
	return m
}

// decodeJSONObject decodes an optional JSON object body into a tool-argument
// map. An empty body is valid (every REST body in this API is optional or
// entirely absent for GET-shaped semantics); a malformed one is treated the
// same as empty — the underlying Ops method's own validation (e.g.
// set_schedule's cron check) is what actually gates correctness.
func decodeJSONObject(body []byte) map[string]any {
	m := map[string]any{}
	if len(bytes.TrimSpace(body)) == 0 {
		return m
	}
	_ = json.Unmarshal(body, &m)
	return m
}
