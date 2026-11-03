package main

// modern.go serves MCP revision 2026-07-28: no initialize handshake and no
// sessions. Every request carries its protocol version in params._meta, and the
// HTTP headers MCP-Protocol-Version, Mcp-Method and Mcp-Name must match the
// body. A mismatch is rejected with HTTP 400 and code -32020 (HeaderMismatch).

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

const (
	modernVersion = "2026-07-28"

	metaVersionKey      = "io.modelcontextprotocol/protocolVersion"
	metaCapabilitiesKey = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfoKey   = "io.modelcontextprotocol/serverInfo"

	codeHeaderMismatch     = -32020
	codeUnsupportedVersion = -32022
	codeMethodNotFound     = -32601
	codeInvalidParams      = -32602
	base64SentinelPrefix   = "=?base64?"
	base64SentinelSuffix   = "?="
	listTTLMilliseconds    = 30000
	listCacheScope         = "public"
	protocolVersionHeader  = "MCP-Protocol-Version"
	methodHeader           = "Mcp-Method"
	nameHeader             = "Mcp-Name"
	paramHeaderPrefix      = "Mcp-Param-"
)

// legacyVersions are the handshake-era revisions a dual-era server also serves.
var legacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// pageSize is the tools/list page size for modern clients. 0 means one page.
var pageSize = 0

// modernParams holds the params fields the modern handlers read.
type modernParams struct {
	Meta           map[string]json.RawMessage `json:"_meta"`
	Name           string                     `json:"name"`
	URI            string                     `json:"uri"`
	Cursor         string                     `json:"cursor"`
	Arguments      map[string]any             `json:"arguments"`
	InputResponses map[string]json.RawMessage `json:"inputResponses"`
	RequestState   string                     `json:"requestState"`
}

// metaString returns a string-valued _meta entry, or "" if absent.
func (p modernParams) metaString(key string) string {
	var value string
	_ = json.Unmarshal(p.Meta[key], &value)
	return value
}

// supportedVersions lists the revisions this server answers in its current era.
func supportedVersions() []string {
	if era == eraDual {
		return append([]string{modernVersion}, legacyVersions...)
	}
	return []string{modernVersion}
}

// isModernRequest decides, in dual mode, whether a request uses 2026-07-28.
// The _meta version is the primary signal. The header alone also counts, so
// that a modern header on a legacy-shaped body is caught as a mismatch.
func isModernRequest(r *http.Request, req rpcRequest) bool {
	var p modernParams
	_ = json.Unmarshal(req.Params, &p)
	if p.metaString(metaVersionKey) != "" {
		return true
	}
	return strings.HasPrefix(r.Header.Get(protocolVersionHeader), "2026-")
}

// decodeHeaderValue undoes the =?base64?...?= sentinel encoding, if present.
func decodeHeaderValue(value string) string {
	if !strings.HasPrefix(value, base64SentinelPrefix) || !strings.HasSuffix(value, base64SentinelSuffix) {
		return value
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(value, base64SentinelPrefix), base64SentinelSuffix)
	decoded, err := base64.StdEncoding.DecodeString(inner)
	if err != nil {
		return value
	}
	return string(decoded)
}

// headerMismatch writes a 400 HeaderMismatch error and logs why.
func headerMismatch(w http.ResponseWriter, req rpcRequest, reason string) {
	log.Printf("modern %s rejected: header mismatch: %s", req.Method, reason)
	writeErrorStatus(w, http.StatusBadRequest, req.ID, codeHeaderMismatch,
		"Header mismatch: "+reason, nil)
}

// checkVersion validates the version header and _meta version. It writes the
// error response and returns false when the request must stop.
func checkVersion(w http.ResponseWriter, r *http.Request, req rpcRequest, p modernParams) bool {
	header := r.Header.Get(protocolVersionHeader)
	body := p.metaString(metaVersionKey)
	if header == "" {
		headerMismatch(w, req, "missing "+protocolVersionHeader+" header")
		return false
	}
	if body == "" {
		headerMismatch(w, req, "params._meta lacks "+metaVersionKey)
		return false
	}
	if header != body {
		headerMismatch(w, req, protocolVersionHeader+" "+header+" does not match _meta version "+body)
		return false
	}
	if body != modernVersion {
		log.Printf("modern %s rejected: unsupported version %q", req.Method, body)
		writeErrorStatus(w, http.StatusBadRequest, req.ID, codeUnsupportedVersion,
			"Unsupported protocol version: "+body,
			map[string]any{"supported": supportedVersions(), "requested": body})
		return false
	}
	return true
}

// checkStandardHeaders validates Mcp-Method and, where required, Mcp-Name.
func checkStandardHeaders(w http.ResponseWriter, r *http.Request, req rpcRequest, p modernParams) bool {
	method := r.Header.Get(methodHeader)
	if method != req.Method {
		headerMismatch(w, req, "Mcp-Method '"+method+"' does not match body method '"+req.Method+"'")
		return false
	}
	bodyName := ""
	switch req.Method {
	case "tools/call", "prompts/get":
		bodyName = p.Name
	case "resources/read":
		bodyName = p.URI
	default:
		return true
	}
	name := decodeHeaderValue(r.Header.Get(nameHeader))
	if name != bodyName {
		headerMismatch(w, req, "Mcp-Name '"+name+"' does not match body value '"+bodyName+"'")
		return false
	}
	return true
}

// logModernRequest records what arrived, so gateway behavior can be read from
// this server's log. Header values that carry credentials are not logged.
func logModernRequest(r *http.Request, req rpcRequest) {
	log.Printf("modern request method=%s %s=%q %s=%q %s=%q session=%t",
		req.Method,
		protocolVersionHeader, r.Header.Get(protocolVersionHeader),
		methodHeader, r.Header.Get(methodHeader),
		nameHeader, r.Header.Get(nameHeader),
		r.Header.Get(sessionHeader) != "")
}

// handleModern answers one 2026-07-28 request. Mcp-Session-Id is ignored.
func handleModern(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	logModernRequest(r, req)
	if len(req.ID) == 0 {
		// A notification. This revision defines none over HTTP; accept it.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method == "initialize" {
		rejectInitialize(w, req)
		return
	}
	var p modernParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeErrorStatus(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid params", nil)
		return
	}
	if !checkVersion(w, r, req, p) || !checkStandardHeaders(w, r, req, p) {
		return
	}
	dispatchModern(w, r, req, p)
}

// rejectInitialize answers a handshake sent to the modern code path. In
// modern-only mode the error names the supported versions, which is the only
// diagnostic a legacy client can show its user.
func rejectInitialize(w http.ResponseWriter, req rpcRequest) {
	if era == eraDual {
		headerMismatch(w, req, "an initialize request (legacy handshake) was sent with modern request metadata")
		return
	}
	log.Printf("modern initialize rejected: this server supports only %s", modernVersion)
	writeErrorStatus(w, http.StatusBadRequest, req.ID, codeUnsupportedVersion,
		"initialize is not supported: this server speaks only "+modernVersion,
		map[string]any{"supported": supportedVersions()})
}

// dispatchModern routes a validated modern request to its handler.
func dispatchModern(w http.ResponseWriter, r *http.Request, req rpcRequest, p modernParams) {
	switch req.Method {
	case "server/discover":
		writeResult(w, req.ID, completeResult(discoverResult()))
	case "tools/list":
		writeResult(w, req.ID, completeResult(listToolsPage(p.Cursor)))
	case "tools/call":
		callModernTool(w, r, req, p)
	default:
		log.Printf("modern %s: method not found", req.Method)
		writeErrorStatus(w, http.StatusNotFound, req.ID, codeMethodNotFound, "Method not found", nil)
	}
}

// completeResult marks an ordinary result and adds the server identity.
func completeResult(result map[string]any) map[string]any {
	if _, ok := result["resultType"]; !ok {
		result["resultType"] = "complete"
	}
	result["_meta"] = map[string]any{
		metaServerInfoKey: map[string]any{"name": serverName, "version": serverVersion},
	}
	return result
}

// discoverResult is the server/discover answer.
func discoverResult() map[string]any {
	return map[string]any{
		"supportedVersions": supportedVersions(),
		"capabilities":      map[string]any{"tools": map[string]any{"listChanged": false}},
		"instructions":      "pingmcp test server (era=" + era + "). Tools exist to test MCP gateways.",
	}
}

// listToolsPage returns one page of the modern tool catalog. The cursor is
// the index of the first tool on the page.
func listToolsPage(cursor string) map[string]any {
	tools := modernTools()
	start := 0
	if cursor != "" {
		start = parseCursor(cursor, len(tools))
	}
	end := len(tools)
	if pageSize > 0 && start+pageSize < end {
		end = start + pageSize
	}
	result := map[string]any{
		"tools":      tools[start:end],
		"ttlMs":      listTTLMilliseconds,
		"cacheScope": listCacheScope,
	}
	if end < len(tools) {
		result["nextCursor"] = formatCursor(end)
	}
	return result
}
