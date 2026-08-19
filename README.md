# pingmcp

A tiny, fast [MCP](https://modelcontextprotocol.io) server written in Go that speaks the **streamable-http** transport and exposes a single `echo` tool. Zero external dependencies (Go standard library only), ships as a ~10 MB static binary / distroless image.

## Why

It exists to be a **fast upstream** for load-testing an MCP gateway. Most real MCP servers (or the API endpoints behind a gateway) are the bottleneck under load, which hides the cost of the gateway's own per-request work (auth checks, routing). `pingmcp` responds in microseconds, so a load test through a gateway is bounded by the gateway, not by the server, letting you measure the gateway's true overhead and throughput ceiling.

It is also a minimal, readable reference for implementing MCP streamable-http in Go without a framework.

## The `echo` tool

```json
{
  "name": "echo",
  "description": "Echo back the provided message.",
  "inputSchema": {
    "type": "object",
    "properties": { "message": { "type": "string" } },
    "required": ["message"]
  }
}
```

`tools/call echo {"message": "hi"}` returns `{"content": [{"type": "text", "text": "hi"}]}`.

## Run

```bash
# from source
go build -o pingmcp . && PORT=8100 ./pingmcp

# or Docker
docker build -t pingmcp . && docker run --rm -p 8100:8100 pingmcp
```

Environment:

| Var | Default | Purpose |
|-----|---------|---------|
| `PORT` | `8100` | listen port |

Endpoints: `POST /mcp` (JSON-RPC), `GET /health`.

## Protocol

Implements the MCP streamable-http transport with a direct `application/json` response per POST (the spec permits this for a POST carrying a single request):

- `initialize` -> `serverInfo` + `capabilities.tools`; sets an `Mcp-Session-Id` response header.
- `notifications/initialized` -> `202 Accepted`.
- `ping` -> `{}`.
- `tools/list` -> the `echo` tool.
- `tools/call` (`echo`) -> the echoed text.

A `GET /mcp` returns `405` (no standalone SSE stream); clients that POST and read the direct JSON response work fine.

## Quick check

```bash
curl -s -X POST http://localhost:8100/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}'
```

## License

Apache-2.0
