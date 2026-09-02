package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// protocolVersions/protocolDefault are the negotiated MCP protocol versions
// (§11.5): the three the server understands, defaulting to the middle one
// for an unrecognized (or absent) client version.
var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const protocolDefault = "2025-03-26"

// --- JSON-RPC envelopes -------------------------------------------------

type resultEnvelope struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Error   rpcErr `json:"error"`
}

func mkResult(id any, result any) (int, []byte) {
	b, _ := json.Marshal(resultEnvelope{JSONRPC: "2.0", ID: id, Result: result})
	return 200, b
}

func mkError(id any, code int, message string) (int, []byte) {
	b, _ := json.Marshal(errorEnvelope{JSONRPC: "2.0", ID: id, Error: rpcErr{Code: code, Message: message}})
	return 200, b
}

// HandleRequest is the pure port of Invoke-StoMcpRequest: string in
// (already authenticated and size-checked by the transport), (status, body)
// out. No sockets — the whole JSON-RPC surface is testable without a
// listener.
func HandleRequest(ops *Ops, body []byte) (status int, respBody []byte) {
	var generic any
	if err := json.Unmarshal(body, &generic); err != nil {
		return mkError(nil, -32700, "parse error: body is not a JSON object")
	}
	if _, isArray := generic.([]any); isArray {
		// Go-only, informational (ruling 2): PS's ConvertFrom-Json
		// -AsHashtable on a top-level array produces a shape its own
		// dispatch never anticipated; Go rejects it outright rather than
		// inherit that undefined behavior.
		return mkError(nil, -32600, "invalid request: batch requests are not supported")
	}
	req, isObject := generic.(map[string]any)
	if !isObject {
		return mkError(nil, -32700, "parse error: body is not a JSON object")
	}

	method := stringify(req["method"])
	if method == "" {
		return mkError(req["id"], -32600, "invalid request: missing 'method'")
	}

	// Notifications (no "id" KEY present at all — a presence check, not a
	// null check) get 202 + empty body per streamable HTTP.
	id, hasID := req["id"]
	if !hasID {
		return 202, nil
	}

	params, _ := req["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}

	switch method {
	case "initialize":
		clientVer, _ := params["protocolVersion"].(string)
		ver := protocolDefault
		for _, v := range protocolVersions {
			if v == clientVer {
				ver = clientVer
				break
			}
		}
		return mkResult(id, map[string]any{
			"protocolVersion": ver,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "scriptorium", "version": appVersion(ops.App.Paths.AppDir)},
		})
	case "ping":
		return mkResult(id, map[string]any{})
	case "tools/list":
		return mkResult(id, map[string]any{"tools": toolDefs})
	case "tools/call":
		return handleToolsCall(ops, id, params)
	default:
		return mkError(id, -32601, "method not found: "+method)
	}
}

func handleToolsCall(ops *Ops, id any, params map[string]any) (int, []byte) {
	toolName, _ := params["name"].(string)
	if toolName == "" {
		return mkError(id, -32602, "invalid params: missing tool 'name'")
	}
	if !validToolName[toolName] {
		return mkError(id, -32602, fmt.Sprintf("unknown tool '%s' — valid tools: %s", toolName, strings.Join(toolNames, ", ")))
	}
	toolArgs, _ := params["arguments"].(map[string]any)
	if toolArgs == nil {
		toolArgs = map[string]any{}
	}
	result, isErr, err := ops.Call(toolName, toolArgs)
	if err != nil {
		// exception text can carry a tokened URL — redact before it leaves
		return mkError(id, -32603, ops.App.Sec.Redact(fmt.Sprintf("internal error running tool '%s': %s", toolName, err.Error())))
	}
	return mkResult(id, toolCallResult(result, isErr))
}
