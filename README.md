# pingmcp

A tiny, fast [MCP](https://modelcontextprotocol.io) server written in Go that speaks the **streamable-http** transport. Zero external dependencies (Go standard library only), ships as a ~10 MB static binary / distroless image.

## Why

It exists to be a **fast upstream** for load-testing an MCP gateway. Most real MCP servers (or the API endpoints behind a gateway) are the bottleneck under load, which hides the cost of the gateway's own per-request work (auth checks, routing). `pingmcp` responds in microseconds, so a load test through a gateway is bounded by the gateway, not by the server, letting you measure the gateway's true overhead and throughput ceiling.

It is also a test upstream for MCP protocol compatibility. `MCP_ERA` picks which protocol revisions it speaks, so one binary can stand in for a legacy server, a server that speaks only [2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/changelog), or a server that speaks both.

## Protocol eras

| `MCP_ERA` | Speaks | Behavior |
|---|---|---|
| `legacy` (default) | `2025-11-25` and earlier | `initialize` mints an `Mcp-Session-Id`. The only tool is `echo`. This is the original behavior and does not change. |
| `modern` | `2026-07-28` only | No `initialize` and no sessions. Every request carries its version in `params._meta`. `initialize` gets HTTP 400 with `-32022` and the supported versions. |
| `dual` | both | A request with the modern `_meta` version is served as `2026-07-28`. Anything else is served as legacy. Legacy clients still see only `echo`. |

In `modern` and `dual` mode, a modern request goes through the checks the 2026-07-28 Streamable HTTP transport requires:

- `MCP-Protocol-Version` must be present and equal the `_meta` version. Otherwise the server returns 400 with `-32020` (HeaderMismatch).
- A version other than `2026-07-28` gets 400 with `-32022` and `data.supported`.
- `Mcp-Method` must equal the body `method`. `Mcp-Name` must equal `params.name` (or `params.uri`) on `tools/call`, `resources/read` and `prompts/get`, after base64 sentinel decoding (`=?base64?...?=`).
- An unknown method, including the removed `ping`, gets 404 with `-32601`.
- `Mcp-Session-Id` is ignored, and GET and DELETE get 405.

Every `inputSchema` has `"type": "object"` at its root, which the 2026-07-28 schema requires. Modern requests also get `server/discover`, results with `resultType` and `_meta` server identity, and a `tools/list` with `ttlMs` and `cacheScope`.

## Tools

Every era has `echo`: `tools/call echo {"message": "hi"}` returns `{"content": [{"type": "text", "text": "hi"}]}`.

Modern clients also see these test tools:

| Tool | What it does |
|---|---|
| `inspect_request` | Returns the HTTP headers and `_meta` the server received, so you can see what a gateway passed through. `Authorization`, `Cookie` and similar headers are redacted to their length. |
| `slow_progress` | Runs for `seconds` (default 5, max 120). With `Accept: text/event-stream` and a `_meta.progressToken`, it streams one `notifications/progress` per second, then the result. It stops when the client disconnects and logs how long it ran. |
| `needs_input` | A multi round-trip request. The first call returns `resultType: "input_required"` with a form `elicitation/create` and a signed `requestState`. The retry with `inputResponses` and the same `requestState` completes. The state is HMAC-signed, expires after 2 minutes, and is bound to the caller (a digest of `Authorization`, else `X-User`), the tool and its arguments. The client must declare `elicitation` in `clientCapabilities`. |
| `ref_schema` | Has an `inputSchema` whose root is a `$ref` into `$defs`, beside `"type": "object"`. |
| `composed_schema` | Has an `inputSchema` whose root combines two schemas with `allOf`. |
| `header_param` | Its `region` parameter has `x-mcp-header: Region`, so the request must carry a matching `Mcp-Param-Region` header. |

## Run

```bash
# from source
go build -o pingmcp . && PORT=8100 ./pingmcp

# a 2026-07-28-only server
MCP_ERA=modern PORT=8101 ./pingmcp

# or Docker
docker build -t pingmcp . && docker run --rm -p 8100:8100 -e MCP_ERA=dual pingmcp
```

Environment:

| Var | Default | Purpose |
|-----|---------|---------|
| `PORT` | `8100` | Listen port. |
| `MCP_ERA` | `legacy` | `legacy`, `modern` or `dual`. Any other value stops the server at startup. |
| `PINGMCP_PAGE_SIZE` | `0` | Page size for modern `tools/list`. `0` returns every tool on one page. |
| `PINGMCP_STATE_KEY` | random per process | HMAC key for `needs_input` `requestState`. Set it when several instances sit behind one load balancer. |

Endpoints: `POST /mcp` (JSON-RPC), `GET /health`.

## Quick check

```bash
# legacy
curl -s -X POST http://localhost:8100/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}'

# modern (MCP_ERA=modern or dual)
curl -s -X POST http://localhost:8100/mcp \
  -H 'Content-Type: application/json' \
  -H 'MCP-Protocol-Version: 2026-07-28' -H 'Mcp-Method: server/discover' \
  -d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}'
```

## Test

```bash
go test ./...
```

## License

Apache-2.0
