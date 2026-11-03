package main

// legacy.go serves MCP revisions 2025-11-25 and earlier: an initialize
// handshake that mints a session id, then plain JSON-RPC requests. This is the
// original pingmcp behavior and must not change, because load tests depend on
// it.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// newSessionID returns a random opaque session id for the initialize response.
func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// handleLegacy answers one legacy JSON-RPC request.
func handleLegacy(w http.ResponseWriter, req rpcRequest) {
	switch req.Method {
	case "initialize":
		w.Header().Set(sessionHeader, newSessionID())
		writeResult(w, req.ID, legacyInitializeResult(req.Params))
	case "notifications/initialized":
		// A notification: acknowledge with 202 and no body.
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		writeResult(w, req.ID, map[string]any{})
	case "tools/list":
		writeResult(w, req.ID, map[string]any{"tools": []any{echoTool()}})
	case "tools/call":
		writeResult(w, req.ID, callEcho(req.Params))
	default:
		writeError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// legacyInitializeResult echoes the client's requested protocol version.
func legacyInitializeResult(params json.RawMessage) map[string]any {
	proto := defaultProtoc
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
		proto = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": proto,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
	}
}
