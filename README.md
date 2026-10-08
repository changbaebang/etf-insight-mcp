# etf-insight-mcp

MCP server that simulates small daily purchases of US ETFs and compares
trend-based allocation rules against plain dollar-cost averaging.

> Not investment advice. Past performance does not predict future results.
> The ETF universe is today's top list, so historical results carry
> survivorship bias.

## Status

Day 0: toolchain skeleton. One `ping` tool.

## Develop

```sh
make build   # bin/etf-insight-mcp
make test
make lint    # needs golangci-lint v2
```

## Connect

```json
{ "mcpServers": { "etf-insight": { "command": "/path/to/bin/etf-insight-mcp" } } }
```
