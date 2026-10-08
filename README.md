# etf-insight-mcp

MCP server that simulates small daily purchases of US ETFs and compares
trend-based allocation rules against plain dollar-cost averaging.
Runs locally as a single Go binary over stdio. No database, no API key.

> Not investment advice. Past performance does not predict future results.
> The ETF universe is today's top list, so historical results carry
> survivorship bias.

## Status

Day 0: toolchain skeleton. One `ping` tool. Price data and simulations
land in the next PRs.

## Requirements

- Go 1.27+ (`brew install go`)
- Optional: `golangci-lint` v2 for `make lint`

## Build and run locally

```sh
git clone https://github.com/changbaebang/etf-insight-mcp.git
cd etf-insight-mcp
make build          # -> ./bin/etf-insight-mcp
make test
```

The binary speaks MCP over stdin/stdout. You can poke it without any
client by piping JSON-RPC lines in (stdin is held open for a second so the
replies have time to flush):

```sh
( printf '%s\n%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping","arguments":{"message":"hello"}}}' ; sleep 1 ) \
  | ./bin/etf-insight-mcp
```

Expected: two JSON lines, the second containing `"reply":"hello"`.

Anything the server logs goes to stderr. stdout is reserved for the
protocol, so never `fmt.Println` from server code.

## Connect to Claude Code

The repo ships a project-scoped [`.mcp.json`](.mcp.json) that points at
`./bin/etf-insight-mcp`. Build once, then start Claude Code inside the
repo directory:

```sh
make build
claude            # from the repo root
```

Claude Code asks once whether to trust the project's MCP servers. Check
the connection with:

```sh
claude mcp list   # etf-insight: ./bin/etf-insight-mcp - ✓ Connected
```

Then ask, for example: "use etf-insight ping with message hi".

To use it from any directory instead of only this repo, install the
binary and register it at user scope:

```sh
make install                                   # -> ~/go/bin/etf-insight-mcp
claude mcp add --scope user etf-insight -- "$(go env GOPATH)/bin/etf-insight-mcp"
```

Remove with `claude mcp remove etf-insight`.

## Connect to Claude Desktop

Claude Desktop reads
`~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS). Add the server with an **absolute** path; Desktop does not
resolve relative paths or `~`:

```json
{
  "mcpServers": {
    "etf-insight": {
      "command": "/Users/you/go/bin/etf-insight-mcp"
    }
  }
}
```

Restart Claude Desktop. The server appears under the tools icon in a new
chat. Logs are in `~/Library/Logs/Claude/mcp-server-etf-insight.log` when
something goes wrong.

## Troubleshooting

- **"Failed to connect"**: run the binary directly in a terminal. If it
  prints a Go error to stderr, that is the cause. If it just waits, the
  path in the config is probably wrong.
- **Changed the code but Claude sees the old tool list**: rebuild, then
  restart the client. Claude Code re-spawns the server on `/mcp` reconnect;
  Desktop needs a full restart.
- **`claude mcp list` shows nothing**: you are not in the repo root, or you
  declined the trust prompt. Run `claude mcp reset-project-choices` and
  start again.

## Develop

```sh
make build   # bin/etf-insight-mcp
make test    # go test -race -cover
make lint    # golangci-lint v2
make vet
```

Each PR adds one tool or one Go concept. CI runs vet, tests and lint.
