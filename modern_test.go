package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
	`"io.modelcontextprotocol/clientInfo":{"name":"t","version":"0"},` +
	`"io.modelcontextprotocol/clientCapabilities":{"elicitation":{"form":{}}}}`

// withEra runs a test with the server in the given era, then restores it.
func withEra(t *testing.T, value string) {
	t.Helper()
	previous := era
	era = value
	initStateKey()
	t.Cleanup(func() { era = previous })
}

// postModern sends a modern request. headers overrides the defaults; an empty
// value removes that header.
func postModern(t *testing.T, method string, params string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{` + testMeta
	if params != "" {
		body += "," + params
	}
	body += "}}"
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(protocolVersionHeader, modernVersion)
	req.Header.Set(methodHeader, method)
	for name, value := range headers {
		if value == "" {
			req.Header.Del(name)
		} else {
			req.Header.Set(name, value)
		}
	}
	rr := httptest.NewRecorder()
	handleMCP(rr, req)
	return rr
}

func errorCode(t *testing.T, rr *httptest.ResponseRecorder) float64 {
	t.Helper()
	e, ok := decode(t, rr)["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %s", rr.Body.String())
	}
	return e["code"].(float64)
}

func result(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	r, ok := decode(t, rr)["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result, got %d %s", rr.Code, rr.Body.String())
	}
	return r
}

func TestModernDiscover(t *testing.T) {
	withEra(t, eraModern)
	res := result(t, postModern(t, "server/discover", "", nil))
	versions := res["supportedVersions"].([]any)
	if len(versions) != 1 || versions[0] != modernVersion || res["resultType"] != "complete" {
		t.Fatalf("unexpected discover result: %v", res)
	}
}

func TestModernRejectsInitializeNamingVersions(t *testing.T) {
	withEra(t, eraModern)
	rr := post(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	if rr.Code != http.StatusBadRequest || errorCode(t, rr) != codeUnsupportedVersion {
		t.Fatalf("want 400 -32022, got %d %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get(sessionHeader) != "" {
		t.Fatal("modern mode must not mint a session id")
	}
}

func TestModernUnsupportedVersion(t *testing.T) {
	withEra(t, eraModern)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set(protocolVersionHeader, "1900-01-01")
	req.Header.Set(methodHeader, "tools/list")
	rr := httptest.NewRecorder()
	handleMCP(rr, req)
	if rr.Code != http.StatusBadRequest || errorCode(t, rr) != codeUnsupportedVersion {
		t.Fatalf("want 400 -32022, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestModernHeaderMismatches(t *testing.T) {
	withEra(t, eraModern)
	cases := map[string]map[string]string{
		"wrong method":     {methodHeader: "tools/list"},
		"missing name":     {nameHeader: ""},
		"wrong name":       {nameHeader: "other"},
		"missing version":  {protocolVersionHeader: ""},
		"version mismatch": {protocolVersionHeader: "2025-11-25"},
	}
	for label, headers := range cases {
		if _, ok := headers[nameHeader]; !ok {
			headers[nameHeader] = "echo"
		}
		rr := postModern(t, "tools/call", `"name":"echo","arguments":{"message":"x"}`, headers)
		if rr.Code != http.StatusBadRequest || errorCode(t, rr) != codeHeaderMismatch {
			t.Errorf("%s: want 400 -32020, got %d %s", label, rr.Code, rr.Body.String())
		}
	}
}

func TestModernBase64NameHeader(t *testing.T) {
	withEra(t, eraModern)
	encoded := "=?base64?" + base64.StdEncoding.EncodeToString([]byte("echo")) + "?="
	rr := postModern(t, "tools/call", `"name":"echo","arguments":{"message":"b64"}`, map[string]string{nameHeader: encoded})
	if rr.Code != http.StatusOK {
		t.Fatalf("base64 Mcp-Name must decode, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestModernToolsListIsCacheable(t *testing.T) {
	withEra(t, eraModern)
	res := result(t, postModern(t, "tools/list", "", nil))
	if res["resultType"] != "complete" || res["cacheScope"] != "public" || res["ttlMs"] == nil {
		t.Fatalf("missing cache fields: %v", res)
	}
	if len(res["tools"].([]any)) != len(modernTools()) {
		t.Fatalf("expected the full catalog on one page")
	}
}

func TestModernToolsListPagination(t *testing.T) {
	withEra(t, eraModern)
	pageSize = 3
	t.Cleanup(func() { pageSize = 0 })
	seen := 0
	cursor := ""
	for page := 0; page < 10; page++ {
		params := ""
		if cursor != "" {
			params = `"cursor":"` + cursor + `"`
		}
		res := result(t, postModern(t, "tools/list", params, nil))
		seen += len(res["tools"].([]any))
		next, more := res["nextCursor"].(string)
		if !more {
			break
		}
		cursor = next
	}
	if seen != len(modernTools()) {
		t.Fatalf("pagination returned %d tools, want %d", seen, len(modernTools()))
	}
}

func TestModernUnknownMethodIs404(t *testing.T) {
	withEra(t, eraModern)
	rr := postModern(t, "ping", "", nil)
	if rr.Code != http.StatusNotFound || errorCode(t, rr) != codeMethodNotFound {
		t.Fatalf("want 404 -32601 (ping is removed in 2026-07-28), got %d %s", rr.Code, rr.Body.String())
	}
}

func TestHeaderParamRequiresMirroredHeader(t *testing.T) {
	withEra(t, eraModern)
	params := `"name":"header_param","arguments":{"region":"us-west1"}`
	rr := postModern(t, "tools/call", params, map[string]string{nameHeader: "header_param"})
	if rr.Code != http.StatusBadRequest || errorCode(t, rr) != codeHeaderMismatch {
		t.Fatalf("missing Mcp-Param-Region: want 400 -32020, got %d", rr.Code)
	}
	rr = postModern(t, "tools/call", params, map[string]string{nameHeader: "header_param", "Mcp-Param-Region": "us-west1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("matching header: want 200, got %d %s", rr.Code, rr.Body.String())
	}
}

// needsInputCall runs needs_input with an optional retry payload.
func needsInputCall(t *testing.T, extra string, auth string) *httptest.ResponseRecorder {
	t.Helper()
	params := `"name":"needs_input","arguments":{"topic":"t1"}`
	if extra != "" {
		params += "," + extra
	}
	return postModern(t, "tools/call", params, map[string]string{nameHeader: "needs_input", "Authorization": auth})
}

func TestNeedsInputRoundTrip(t *testing.T) {
	withEra(t, eraModern)
	first := result(t, needsInputCall(t, "", "Bearer alice"))
	if first["resultType"] != "input_required" || first["inputRequests"] == nil {
		t.Fatalf("first call must ask for input: %v", first)
	}
	state, _ := json.Marshal(first["requestState"])
	retry := `"requestState":` + string(state) + `,"inputResponses":{"confirm":{"action":"accept","content":{"answer":"42"}}}`

	done := result(t, needsInputCall(t, retry, "Bearer alice"))
	if done["resultType"] != "complete" || !strings.Contains(done["content"].([]any)[0].(map[string]any)["text"].(string), "42") {
		t.Fatalf("retry must complete with the answer: %v", done)
	}
	if code := errorCode(t, needsInputCall(t, retry, "Bearer mallory")); code != codeInvalidParams {
		t.Fatalf("state from another caller must be rejected, got %v", code)
	}
	tampered := strings.Replace(retry, `"requestState":"`, `"requestState":"x`, 1)
	if code := errorCode(t, needsInputCall(t, tampered, "Bearer alice")); code != codeInvalidParams {
		t.Fatalf("tampered state must be rejected, got %v", code)
	}
}

func TestDualServesBothEras(t *testing.T) {
	withEra(t, eraDual)
	rr := post(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	if rr.Code != http.StatusOK || rr.Header().Get(sessionHeader) == "" {
		t.Fatalf("dual must answer legacy initialize with a session, got %d", rr.Code)
	}
	res := result(t, postModern(t, "server/discover", "", nil))
	if len(res["supportedVersions"].([]any)) < 2 {
		t.Fatalf("dual must list modern and legacy versions: %v", res)
	}
	legacyTools := decode(t, post(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))["result"].(map[string]any)["tools"].([]any)
	if len(legacyTools) != 1 {
		t.Fatalf("legacy clients must still see only echo, got %d tools", len(legacyTools))
	}
}

func TestDualRejectsModernHeaderOnInitialize(t *testing.T) {
	withEra(t, eraDual)
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	req.Header.Set(protocolVersionHeader, modernVersion)
	rr := httptest.NewRecorder()
	handleMCP(rr, req)
	if rr.Code != http.StatusBadRequest || errorCode(t, rr) != codeHeaderMismatch {
		t.Fatalf("want 400 -32020, got %d %s", rr.Code, rr.Body.String())
	}
}

// The 2026-07-28 schema requires "type": "object" at every inputSchema root.
// A client that validates the listing rejects the whole page otherwise.
func TestEveryToolSchemaHasObjectRoot(t *testing.T) {
	for _, entry := range modernTools() {
		tool := entry.(map[string]any)
		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Errorf("tool %v: inputSchema root type is %v, want \"object\"", tool["name"], schema["type"])
		}
	}
}
