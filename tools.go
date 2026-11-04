package main

// tools.go holds the tool catalog. Legacy clients see only echo, exactly as
// before. Modern clients also see test tools for MCP gateway work:
//
//	inspect_request - returns the headers and _meta this server received
//	slow_progress   - streams progress over SSE for N seconds, cancel-aware
//	needs_input     - multi round-trip request with a signed requestState
//	ref_schema      - inputSchema with a root $ref into $defs
//	composed_schema - inputSchema whose root uses allOf
//
// The 2026-07-28 schema requires "type": "object" at the root of every
// inputSchema, and allows any other JSON Schema 2020-12 keyword beside it.
// Both schema tools follow that rule, so a conforming client accepts them.
//	header_param    - a parameter mirrored into the Mcp-Param-Region header

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSlowSeconds = 5
	maxSlowSeconds     = 120
	regionHeaderName   = "Region"
)

// redactedHeaders are never echoed back or logged with their value.
var redactedHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Cookie":              true,
	"X-Authorization":     true,
	"X-Internal-Token":    true,
}

func echoTool() map[string]any {
	return map[string]any{
		"name":        "echo",
		"description": "Echo back the provided message. Minimal tool for load testing.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string", "description": "Text to echo back"},
			},
			"required": []string{"message"},
		},
	}
}

// callEcho handles tools/call for the echo tool (both eras).
func callEcho(params json.RawMessage) map[string]any {
	var p struct {
		Arguments struct {
			Message string `json:"message"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	msg := p.Arguments.Message
	if msg == "" {
		msg = "pong"
	}
	return textResult(msg)
}

func textResult(text string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": false,
	}
}

func errorResult(text string) map[string]any {
	result := textResult(text)
	result["isError"] = true
	return result
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func tool(name string, description string, inputSchema map[string]any) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": inputSchema}
}

// modernTools is the catalog modern clients see, in a fixed order.
func modernTools() []any {
	return []any{
		echoTool(),
		tool("inspect_request", "Return the HTTP headers and _meta this server received. Credentials are redacted.",
			objectSchema(map[string]any{})),
		tool("slow_progress", "Run for N seconds, sending a progress notification each second over SSE.",
			objectSchema(map[string]any{
				"seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maxSlowSeconds},
			})),
		tool("needs_input", "Ask the client for an answer through form elicitation, then finish on the retry.",
			objectSchema(map[string]any{"topic": map[string]any{"type": "string"}})),
		tool("ref_schema", "Tool whose inputSchema root is a $ref into $defs.", refSchema()),
		tool("composed_schema", "Tool whose inputSchema root combines two schemas with allOf.", composedSchema()),
		tool("header_param", "Tool whose region parameter is mirrored into the Mcp-Param-Region header.",
			objectSchema(map[string]any{
				"region":  map[string]any{"type": "string", "x-mcp-header": regionHeaderName},
				"message": map[string]any{"type": "string"},
			}, "region")),
	}
}

func refSchema() map[string]any {
	return map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"$ref":    "#/$defs/Query",
		"$defs": map[string]any{
			"Query": objectSchema(map[string]any{
				"query": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer", "minimum": 1},
			}, "query"),
		},
	}
}

func composedSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"allOf": []any{
			objectSchema(map[string]any{"name": map[string]any{"type": "string"}}, "name"),
			objectSchema(map[string]any{"count": map[string]any{"type": "integer", "minimum": 0}}),
		},
	}
}

func parseCursor(cursor string, total int) int {
	start, err := strconv.Atoi(cursor)
	if err != nil || start < 0 || start > total {
		return total
	}
	return start
}

func formatCursor(index int) string {
	return strconv.Itoa(index)
}

// principal identifies the caller for requestState binding. It never stores
// the credential itself, only a short digest of it.
func principal(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		sum := sha256.Sum256([]byte(auth))
		return "auth:" + hex.EncodeToString(sum[:8])
	}
	if user := r.Header.Get("X-User"); user != "" {
		return "user:" + user
	}
	return "anonymous"
}

// callModernTool runs tools/call for a modern client.
func callModernTool(w http.ResponseWriter, r *http.Request, req rpcRequest, p modernParams) {
	switch p.Name {
	case "echo":
		writeResult(w, req.ID, completeResult(callEcho(req.Params)))
	case "inspect_request":
		writeResult(w, req.ID, completeResult(inspectRequest(r, p)))
	case "slow_progress":
		slowProgress(w, r, req, p)
	case "needs_input":
		writeNeedsInput(w, r, req, p)
	case "ref_schema", "composed_schema":
		args, _ := json.Marshal(p.Arguments)
		writeResult(w, req.ID, completeResult(textResult("received arguments: "+string(args))))
	case "header_param":
		headerParam(w, r, req, p)
	default:
		writeErrorStatus(w, http.StatusOK, req.ID, codeInvalidParams, "unknown tool: "+p.Name, nil)
	}
}

// inspectRequest reports the request as this server saw it.
func inspectRequest(r *http.Request, p modernParams) map[string]any {
	names := make([]string, 0, len(r.Header))
	for name := range r.Header {
		names = append(names, name)
	}
	sort.Strings(names)
	headers := map[string]string{}
	for _, name := range names {
		value := strings.Join(r.Header.Values(name), ", ")
		if redactedHeaders[name] {
			value = fmt.Sprintf("<redacted, %d bytes>", len(value))
		}
		headers[name] = value
	}
	report := map[string]any{"headers": headers, "meta": p.Meta, "principal": principal(r)}
	text, _ := json.Marshal(report)
	result := textResult(string(text))
	result["structuredContent"] = report
	return result
}

// headerParam checks the Mcp-Param-Region header against the region argument.
func headerParam(w http.ResponseWriter, r *http.Request, req rpcRequest, p modernParams) {
	region, present := p.Arguments["region"].(string)
	header, hasHeader := r.Header[http.CanonicalHeaderKey(paramHeaderPrefix+regionHeaderName)]
	if present && !hasHeader {
		headerMismatch(w, req, "the body carries region but the Mcp-Param-Region header is absent")
		return
	}
	if present && decodeHeaderValue(header[0]) != region {
		headerMismatch(w, req, "Mcp-Param-Region does not match the region argument")
		return
	}
	writeResult(w, req.ID, completeResult(textResult("region="+region+" header="+strings.Join(header, ","))))
}

// slowSeconds reads and clamps the seconds argument.
func slowSeconds(p modernParams) int {
	seconds := defaultSlowSeconds
	if value, ok := p.Arguments["seconds"].(float64); ok {
		seconds = int(value)
	}
	if seconds < 1 {
		seconds = 1
	}
	if seconds > maxSlowSeconds {
		seconds = maxSlowSeconds
	}
	return seconds
}

// slowProgress runs for N seconds. With an SSE-capable client it sends one
// progress notification per second, then the result. It stops as soon as the
// client disconnects and logs how long it ran, so a test can tell whether a
// gateway passed the cancellation on.
func slowProgress(w http.ResponseWriter, r *http.Request, req rpcRequest, p modernParams) {
	seconds := slowSeconds(p)
	var token any
	_ = json.Unmarshal(p.Meta["progressToken"], &token)
	flusher, canStream := w.(http.Flusher)
	stream := canStream && strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}
	started := time.Now()
	log.Printf("slow_progress started: seconds=%d stream=%t", seconds, stream)
	for step := 1; step <= seconds; step++ {
		select {
		case <-r.Context().Done():
			log.Printf("slow_progress cancelled by client after %.1fs (step %d of %d)",
				time.Since(started).Seconds(), step-1, seconds)
			return
		case <-time.After(time.Second):
		}
		if stream && token != nil {
			writeSSE(w, flusher, map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/progress",
				"params":  map[string]any{"progressToken": token, "progress": step, "total": seconds},
			})
		}
	}
	log.Printf("slow_progress finished after %.1fs", time.Since(started).Seconds())
	result := completeResult(textResult(fmt.Sprintf("slow_progress finished after %d seconds", seconds)))
	if !stream {
		writeResult(w, req.ID, result)
		return
	}
	writeSSE(w, flusher, map[string]any{"jsonrpc": "2.0", "id": rawOrNull(req.ID), "result": result})
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, message map[string]any) {
	data, _ := json.Marshal(message)
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	flusher.Flush()
}
