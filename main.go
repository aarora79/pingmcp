// Command pingmcp is a tiny, fast MCP server that speaks the streamable-http
// transport. It exists to be a FAST upstream behind a gateway, so an
// end-to-end load test is bounded by the gateway and not by the upstream. It is
// also a test upstream for MCP protocol compatibility work.
//
// MCP_ERA selects which MCP protocol revisions the server speaks:
//
//	legacy (default) - 2025-11-25 and earlier: initialize handshake, sessions.
//	                   This is the original behavior, unchanged.
//	modern           - 2026-07-28 only: no initialize, no sessions, every
//	                   request carries its version in params._meta.
//	dual             - both, on the same endpoint. A request that carries the
//	                   modern _meta version is served as modern; anything else
//	                   is served as legacy.
//
// Design notes: stdlib only. Each JSON-RPC POST gets a single application/json
// response, except slow_progress, which streams Server-Sent Events.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
)

const (
	serverName    = "pingmcp"
	serverVersion = "1.1.0"
	defaultProtoc = "2025-06-18"
	sessionHeader = "Mcp-Session-Id"
	maxBodyBytes  = 1 << 20

	eraLegacy = "legacy"
	eraModern = "modern"
	eraDual   = "dual"
)

// era is set once at startup from MCP_ERA. Tests change it directly.
var era = eraLegacy

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent/null => notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// writeResult sends a JSON-RPC success response with HTTP 200.
func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNull(id),
		"result":  result,
	})
}

// writeError sends a JSON-RPC error response with HTTP 200.
func writeError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeErrorStatus(w, http.StatusOK, id, code, msg, nil)
}

// writeErrorStatus sends a JSON-RPC error response with the given HTTP status.
func writeErrorStatus(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNull(id),
		"error":   rpcError{Code: code, Message: msg, Data: data},
	})
}

func rawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

// handleMCP is the streamable-http endpoint: one JSON-RPC message per POST.
func handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// No standalone GET stream and no sessions to DELETE in any era.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, nil, -32700, "parse error")
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorStatus(w, http.StatusBadRequest, nil, -32700, "parse error", nil)
		return
	}

	switch era {
	case eraModern:
		handleModern(w, r, req)
	case eraDual:
		if isModernRequest(r, req) {
			handleModern(w, r, req)
		} else {
			handleLegacy(w, req)
		}
	default:
		handleLegacy(w, req)
	}
}

// readEra validates MCP_ERA and falls back to legacy for an empty value.
func readEra() string {
	value := os.Getenv("MCP_ERA")
	switch value {
	case "", eraLegacy:
		return eraLegacy
	case eraModern, eraDual:
		return value
	}
	log.Fatalf("MCP_ERA must be legacy, modern or dual, got %q", value)
	return ""
}

// readPageSize reads PINGMCP_PAGE_SIZE: the modern tools/list page size.
// Empty or 0 returns every tool on one page.
func readPageSize() int {
	value := os.Getenv("PINGMCP_PAGE_SIZE")
	if value == "" {
		return 0
	}
	size, err := strconv.Atoi(value)
	if err != nil || size < 0 {
		log.Fatalf("PINGMCP_PAGE_SIZE must be a non-negative integer, got %q", value)
	}
	return size
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8100"
	}
	era = readEra()
	pageSize = readPageSize()
	initStateKey()

	mux := http.NewServeMux()
	// The gateway proxies /pingmcp/<x> -> http://pingmcp-server:PORT/<x>, so the
	// MCP endpoint arrives as /mcp. Handle /mcp, /mcp/, and / for robustness.
	mux.HandleFunc("/mcp", handleMCP)
	mux.HandleFunc("/mcp/", handleMCP)
	mux.HandleFunc("/", handleMCP)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	addr := ":" + port
	log.Printf("pingmcp %s listening on %s (streamable-http, era=%s)", serverVersion, addr, era)
	log.Fatal(http.ListenAndServe(addr, mux))
}
