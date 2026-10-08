English | [한국어](README.ko.md)

# etf-insight-mcp

MCP server that simulates small recurring purchases (dollar-cost averaging)
of US ETFs and projects the range of outcomes such a plan could have.
Runs locally as a single Go binary over stdio. No database, no API key.

> Not investment advice. Past performance does not predict future results.
> The ETF universe is today's top list, so historical results carry
> survivorship bias.

## Status

All six tools work end to end: `list_etfs`, `get_etf_info`,
`get_price_history`, `simulate_dca`, `simulate_portfolio_dca` and
`forecast_dca` (plus `ping`), together with one resource
(`etf://universe`) and one prompt (`dca_report`). Prices come from Yahoo
Finance's unofficial chart API, delayed and cached on disk.

Next: trend-based allocation rules (for example "buy only while the price
is above its 200-day average") and comparisons of those rules against
plain dollar-cost averaging.

## Tools

| Tool | What it answers | Key inputs |
| --- | --- | --- |
| `list_etfs` | Which ETFs can I look at? Filters the built-in universe of about 100 widely held funds. No network. | `category`, `issuer`, `query`, `include_leveraged` |
| `get_etf_info` | What is this fund and how has it behaved? Universe entry, provider metadata, trailing returns (1m to max), 1-year volatility, drawdowns, trailing-12-month dividends, 52-week range and a rule-based trend reading. | `symbol`, `as_of` |
| `get_price_history` | Give me the bars for a chart or my own calculation. Daily, weekly or monthly close, adjusted close and dividend, thinned to `max_points`. | `symbol`, `start`, `end`, `interval`, `max_points` |
| `simulate_dca` | What would buying this ETF every day, week or month since a date have done, and how does it compare with SPY? | `symbol`, `amount`, `currency`, `cadence`, `start`, `end`, `fee_rate`, `reinvest_dividends`, `compare_with` |
| `simulate_portfolio_dca` | The same for a weighted basket such as 60/40, without rebalancing. Weights may sum to 1 or to 100. | `allocations` (`[{symbol, weight}]`) plus the `simulate_dca` fields |
| `forecast_dca` | What range of outcomes could the plan have over N years? A block bootstrap of the symbols' own history: p5 to p95 of the final value and return, probability of loss, assumptions in words. Not a price prediction. | `symbol` or `allocations`, `amount`, `currency`, `cadence`, `horizon_years`, `simulations`, `seed`, `block_length`, `lookback_years`, `expected_annual_return_pct` |
| `ping` | Is the server alive? Echoes a message with the version. | `message` |

Conventions: dates are `YYYY-MM-DD`; `amount` is the size of one
contribution in the given currency (`USD` or `KRW`); money is rounded to
2 decimals and share counts to 4; every field ending in `_pct` is a plain
percentage (7.5 means 7.5%). Every simulation and forecast output carries
a `disclaimer` field. Unknown symbols and bad input come back as tool
errors whose message says what to change, so the model can correct itself.

Also exposed: the resource `etf://universe` (the universe as CSV) and the
prompt `dca_report` (`symbol`, `amount`, `currency`, `start`), which asks
the model to run `get_etf_info`, `simulate_dca` and `forecast_dca` and
write a short report that ends with the disclaimer.

### Data and cache

Prices come from Yahoo Finance's unofficial chart API: the full daily
history of each symbol with adjusted closes and dividends, delayed. Each
symbol is cached as one JSON file under `~/Library/Caches/etf-insight-mcp`
and reused for 6 hours. KRW plans use the `KRW=X` rate (KRW per USD) from
the same source. Symbols outside the universe work when Yahoo knows them.

Flags:

```sh
etf-insight-mcp -cache-dir DIR     # default: $ETF_INSIGHT_CACHE_DIR, else ~/Library/Caches/etf-insight-mcp
etf-insight-mcp -cache-ttl 6h      # how long a cached symbol is reused
etf-insight-mcp -version           # print the version and exit
```

## Example prompts

Things to type in Claude once the server is connected:

1. "List the dividend ETFs and tell me which one has the highest trailing dividend yield right now."
2. "What would 10,000 KRW into VOO every trading day since 2021 be worth today, and how does that compare with SPY?"
3. "Simulate 300,000 KRW a month into 60% VOO / 40% SCHD from 2020 with a 0.1% fee, dividends reinvested. Show me the drawdown and the FX effect."
4. "Forecast 5 years of 100 USD a month into QQQ: give me the p10, p50 and p90 outcomes and the chance of ending below what I put in."
5. "Is SCHD above its 200-day average? Show monthly closes for the last two years and explain the trend reading."

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

A real simulation needs the network on first use, so hold stdin open a
little longer and point the cache at a scratch directory:

```sh
( printf '%s\n%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"simulate_dca","arguments":{"symbol":"VOO","amount":10000,"currency":"KRW","cadence":"daily","start":"2021-01-01"}}}' ; sleep 15 ) \
  | ./bin/etf-insight-mcp -cache-dir /tmp/etf-insight-cache
```

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
- **A symbol returns "not found" although it exists**: Yahoo's chart API
  answers 404 for delisted or renamed tickers and sometimes rate-limits
  (429, retried automatically). Try again, or delete the symbol's file in
  `~/Library/Caches/etf-insight-mcp` to force a refetch.

## Develop

```sh
make build   # bin/etf-insight-mcp
make test    # go test -race -cover
make lint    # golangci-lint v2
make vet
```

Each PR adds one tool or one Go concept. CI runs vet, tests and lint.
