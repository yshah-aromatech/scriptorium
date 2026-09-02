// Package mcp is the built-in MCP server: a hand-rolled JSON-RPC-over-HTTP
// endpoint so an AI agent (e.g. n8n's MCP Client Tool) can list and run
// scripts over the LAN, plus a co-hosted REST API (api.go, a Go-only
// addition) on the same listener/token/cap. Port of src/Mcp.psm1.
//
// Speaks the MCP streamable-HTTP transport in its simplest legal form:
// stateless (no Mcp-Session-Id), no SSE stream, plain application/json
// response per POST, one JSON-RPC message per request. Auth is a shared
// Bearer token (MCP_AUTH_TOKEN); the server refuses to start without one.
//
// Layering: HandleRequest is a pure function (no sockets), so tests can
// drive the whole protocol without a listener; Server is a thin
// net/http.Handler around it. This package is a declared frontend
// (internal/importlint's allowlist) — it is the one place under internal/
// allowed to own net/http handlers.
package mcp

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
)

// maxBodyBytes is the 1MB request cap (§11.4), enforced twice: a
// Content-Length fast path, then a defensive bounded read regardless of what
// Content-Length claimed (a chunked request reports -1).
const maxBodyBytes = 1 << 20

// bearerRe implements the case-sensitive `^\s*Bearer\s+(.+?)\s*$` auth
// header match; the captured token is compared case-sensitively too (Go's
// == is inherently ordinal, matching PS's -ceq).
var bearerRe = regexp.MustCompile(`^\s*Bearer\s+(.+?)\s*$`)

// Server is the MCP + REST listener. Construct with New; Handler returns the
// net/http.Handler tests drive via httptest, Serve runs it against a real
// net.Listener.
type Server struct {
	ops   *Ops
	token string
}

// New returns a Server for ops, authenticated by token. An empty token is
// refused (§11.3: the server never starts unauthenticated).
func New(ops *Ops, token string) (*Server, error) {
	if token == "" {
		return nil, errors.New("MCP_AUTH_TOKEN is empty — refusing to start an unauthenticated server")
	}
	return &Server{ops: ops, token: token}, nil
}

// Handler returns the whole HTTP surface: GET /healthz, POST /mcp (bare "/"
// also accepted), the REST routes under /api/v1 (api.go), 404 elsewhere.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

// Serve runs the server on l until it is closed, matching
// Start-StoMcpServer's foreground-loop shape (systemd, or the caller, owns
// the lifecycle). A clean Close of l is not an error.
func (s *Server) Serve(l net.Listener) error {
	srv := &http.Server{Handler: s.Handler()}
	err := srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	// 500 is reserved for exactly this: an unhandled exception escaping the
	// whole request (§11.6) — distinct from a JSON-RPC -32603, which stays a
	// 200 with a structured error body.
	defer func() {
		if rec := recover(); rec != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"internal"}`)
		}
	}()

	path := strings.TrimRight(r.URL.Path, "/")

	if r.Method == http.MethodGet && path == "/healthz" {
		writeText(w, http.StatusOK, "ok", "text/plain")
		return
	}

	// The REST API (api.go, a Go-only addition) is co-hosted: same listener,
	// same auth, same cap — only the routing and the response shape differ
	// from the JSON-RPC /mcp endpoint below.
	isAPI := path == apiPrefix || strings.HasPrefix(path, apiPrefix+"/")

	if !isAPI {
		if path != "" && path != "/mcp" {
			writeText(w, http.StatusNotFound, `{"error":"not found"}`, "application/json")
			return
		}
		if r.Method != http.MethodPost {
			// no SSE stream (GET) and no session to delete (DELETE) — stateless
			writeText(w, http.StatusMethodNotAllowed, `{"error":"method not allowed"}`, "application/json")
			return
		}
	}

	// Auth BEFORE reading the body — an unauthenticated client must not be
	// able to drive server-side allocation.
	if !checkAuth(r.Header.Get("Authorization"), s.token) {
		writeText(w, http.StatusUnauthorized, `{"error":"unauthorized"}`, "application/json")
		return
	}
	if r.ContentLength > maxBodyBytes {
		writeText(w, http.StatusRequestEntityTooLarge, `{"error":"payload too large"}`, "application/json")
		return
	}
	body, err := readBounded(r.Body, maxBodyBytes)
	if err != nil {
		writeText(w, http.StatusRequestEntityTooLarge, `{"error":"payload too large"}`, "application/json")
		return
	}

	if isAPI {
		s.serveAPI(w, r, strings.TrimPrefix(path, apiPrefix), body)
		return
	}

	status, respBody := HandleRequest(s.ops, body)
	if respBody == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

// checkAuth implements §11.3's auth check exactly: a case-sensitive regex
// match on the header, then a case-sensitive comparison of the captured
// token.
func checkAuth(header, token string) bool {
	m := bearerRe.FindStringSubmatch(header)
	return m != nil && m[1] == token
}

// readBounded reads at most cap+1 bytes; a result longer than cap means the
// body exceeded the limit regardless of what Content-Length claimed.
func readBounded(r io.Reader, capBytes int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, capBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > capBytes {
		return nil, errTooLarge
	}
	return buf, nil
}

var errTooLarge = errors.New("payload too large")

func writeText(w http.ResponseWriter, code int, body, contentType string) {
	if body != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(code)
	if body != "" {
		_, _ = io.WriteString(w, body)
	}
}

// appVersion is the port of Get-StoAppVersion: the app checkout's short
// commit hash, or "" when it can't be determined (not a git checkout, git
// missing, etc.) — informational only, never fatal.
func appVersion(appDir string) string {
	out, err := exec.Command("git", "-C", appDir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
