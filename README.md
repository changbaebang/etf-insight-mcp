English | [한국어](README.ko.md)

# etf-insight-mcp

MCP server that simulates small recurring purchases (dollar-cost averaging)
of US ETFs and projects the range of outcomes such a plan could have.
It also looks up quotes, dividends and fund data, compares and screens
ETFs, and reviews what a plan costs. Runs locally as a single Go binary
over stdio. No database, no API key.

> Not investment advice. Past performance does not predict future results.
> The ETF universe is a hand-picked list of funds that exist today, so historical results carry
> survivorship bias.

## Status

26 tools work end to end, grouped below: 12 for data, 4 for analysis, 6
for simulation and 4 for the cache and operations (`ping` among them),
together with one resource (`etf://universe`) and one prompt
(`dca_report`). Price history, quotes, fund data, search and news come
from Yahoo Finance's unofficial APIs, delayed. Price history is cached on
disk and brought up to date incrementally; fund data is cached for a day.

`project_dca_outcomes` was called `forecast_dca` until 2026-10-10. It
projects a range of outcomes from resampled history and never forecasts
prices, so the name now says what it does.

Next: trend-based allocation rules (for example "buy only while the price
is above its 200-day average") and comparisons of those rules against
plain dollar-cost averaging.

## Tools

The server lists its tools in alphabetical order. The tables group them
by purpose and keep that order within each group.

### Data

| Tool | What it answers | Key inputs |
| --- | --- | --- |
| `get_dividends` | How much does this fund pay, how often, and is it growing? Every payment (the 120 most recent) restated for later splits to today's share count, with `amount_as_paid` for the cash paid before a split; the trailing 52-week total and yield; payments per year over the last 3 full years with a frequency label; calendar-year totals and 5-year growth of the yearly total. | `symbol`, `start`, `end` |
| `get_etf_info` | What is this fund and how has it behaved? Universe entry, provider metadata, trailing returns (1m to max), 1-year volatility, drawdowns, trailing 52-week dividends, 52-week range and a rule-based trend reading. Warnings flag less than 52 weeks of history, an index or exchange rate, and an `as_of` after the latest bar. | `symbol`, `as_of` |
| `get_fund_performance` | How has the fund done by the provider's own figures? Trailing returns (ytd to 10 years) measured to `trailing_as_of`, usually the last month-end; calendar-year returns next to the category average; and 3/5/10-year risk statistics (alpha, beta, Sharpe, standard deviation, R-squared, mean monthly return, Treynor), with alpha, beta and R-squared measured against the provider's standard index. Periods longer than the fund has existed are null or left out. | `symbol` |
| `get_fund_profile` | What does the fund cost and what is it? Expense ratio (4 decimals, from the latest annual report) with the yearly cost on a 10,000 holding, fund family, category, inception date next to the first day of the price history (`price_history_from`), turnover, net assets of the whole fund across all its share classes, and yield. | `symbol` |
| `get_holdings` | What does the fund own? Top holdings (at most 25) and their combined weight, sector weights as shares of the whole fund, the stock/bond/cash/other split, bond rating grades with the US government share apart, and equity statistics such as P/E and P/B as ordinary multiples. Bond duration and maturity are not shown. | `symbol` |
| `get_news` | Is there news that explains a move? Recent headlines with publisher, link and time; headlines only, no article text. The query must be a ticker or words in Latin letters. | `query`, `limit` |
| `get_price_history` | Give me the bars for a chart or my own calculation. Daily, weekly or monthly close and adjusted close with the dividends paid in each point's period, thinned to `max_points`; a kept point also carries the dividends of the points dropped before it, so the dividend column sums to what was paid. | `symbol`, `start`, `end`, `interval`, `max_points` |
| `get_quote` | Where is it trading now? Delayed quotes for 1 to 50 symbols: price, change, day range, volume, 52-week range, 50/200-day averages, the provider's trailing dividend yield, market state and `quote_time`. Unknown symbols are listed in `missing` and failed fetches in `failed`; a quote kept from an earlier fetch is marked `stale`, with a warning. | `symbols` |
| `get_splits` | Why did the raw share price jump? Splits and reverse splits over the whole history (at most 100). Prices and dividend amounts in every other tool are already split-adjusted. | `symbol` |
| `list_etfs` | Which ETFs can I look at? Filters the built-in universe of about 125 widely held funds; every word of `query` must appear in the symbol, name, issuer or note. No network. | `category`, `issuer`, `query`, `include_leveraged` |
| `market_overview` | How are markets doing today? Quotes and change for SPY, QQQ, DIA, IWM, VEA, VWO, TLT, BND, GLD and the VIX, with each ETF's trend state and distance from its 200-day average. A quote kept from an earlier fetch is marked `stale`, with a warning. | none |
| `search_symbols` | What is the ticker for this fund? Yahoo Finance search by symbol, name or English words (Latin letters only); US-listed ETFs only by default, with foreign listings named in a note, and `in_universe` marks funds `list_etfs` knows. | `query`, `limit`, `etf_only`, `us_only` |

### Analysis

| Tool | What it answers | Key inputs |
| --- | --- | --- |
| `compare_etfs` | How do these funds differ? 2 to 10 USD-quoted ETFs side by side with a benchmark (default SPY): expense ratio, trailing returns (1m to 10y), return and max drawdown over their common range, 1-year volatility and drawdown, dividend yield, trend, beta and correlation to the benchmark, and a correlation matrix over the common range. | `symbols`, `start`, `end`, `benchmark` |
| `find_alternatives` | I buy this ETF regularly; is there something similar, and how does it differ? Universe funds ranked by correlation over the last 3 years, then by lower expense ratio, with beta, expense ratio difference, yield, returns, drawdown, tracking difference and a one-line observation, plus up to 3 funds correlated below 0.9 for context. The symbol must be quoted in USD. Facts, not advice. | `symbol`, `limit`, `min_correlation`, `include_other_categories` |
| `get_technical_indicators` | What do the usual chart indicators say on a given day? RSI, MACD, Bollinger bands with %B, ATR, simple and exponential moving averages, rule hits in plain words and the trend block; an indicator without enough history is null. Descriptions of the price path, not trading signals. | `symbol`, `as_of` |
| `screen_universe` | Which funds rank highest on one metric? The universe sorted by 12-1 momentum, 1-year or 3-month return, volatility, drawdown, dividend yield or distance from the 200-day average; every row carries all seven metrics. The first call loads every screened fund (10 to 90 seconds). | `sort_by`, `descending`, `category`, `include_leveraged`, `limit`, `as_of` |

### Simulation

| Tool | What it answers | Key inputs |
| --- | --- | --- |
| `project_dca_outcomes` | What range of outcomes could the plan have over N years? A block bootstrap of the symbols' own history (at least one year of it): p5 to p95 of the final value and return, probability of loss, assumptions in words. Not a price prediction. | `symbol` or `allocations`, `amount`, `currency`, `cadence`, `horizon_years`, `fee_rate`, `commission_fixed`, `simulations`, `seed`, `block_length`, `lookback_years`, `expected_annual_return_pct` |
| `review_dca_plan` | What does my small recurring plan really cost, and how have plans like it done? One USD plan of one ETF: commission and expense ratio as amounts and percentages, history, short-term and long-term outcomes from real history, a bootstrap projection and factual observations. Never recommends. | `symbol`, `amount`, `cadence`, `horizon_years`, `short_horizon_months`, `fee_rate`, `commission_fixed`, `reinvest_dividends` |
| `simulate_dca` | What would buying this ETF every day, week or month since a date have done, and how does it compare with SPY? | `symbol`, `amount`, `currency`, `cadence`, `start`, `end`, `fee_rate`, `commission_fixed`, `reinvest_dividends`, `compare_with` |
| `simulate_lump_sum_vs_dca` | Invest it all now or spread it out? The same total put in on the first day versus split over every contribution day, on one calendar with the same fees, and the difference in final value and return. | `symbol` or `allocations`, `total_amount`, `currency`, `cadence`, `start`, `end`, `fee_rate`, `commission_fixed`, `reinvest_dividends` |
| `simulate_portfolio_dca` | The same as `simulate_dca` for a weighted basket such as 60/40, without rebalancing. Weights may sum to 1 or to 100, within 1%. | `allocations` (`[{symbol, weight}]`) plus the other `simulate_dca` fields (no `symbol`) |
| `simulate_rolling_dca` | How much did the start date matter? The same plan over every window of N years in the history, one window per step, each starting on the first of a month: percentiles of the outcomes, the share of windows that ended below cost, best and worst window. | `symbol` or `allocations`, `amount`, `currency`, `cadence`, `duration_years`, `step_months`, `fee_rate`, `commission_fixed`, `reinvest_dividends` |

### Cache & ops

| Tool | What it answers | Key inputs |
| --- | --- | --- |
| `cache_status` | What is in the local cache? Directory, file count and size, and per symbol the bar count, date range, last top-up, last full fetch, size and last failed fetch, with warnings. Local disk only. | none |
| `clear_cache` | Delete cached data for some symbols or for all of them. Without `confirm=true` it deletes nothing and fails with a tool error that previews what would go. | `symbols`, `all`, `confirm` |
| `ping` | Is the server alive? Echoes a message with the version. | `message` |
| `refresh_prices` | Download the full price history again, ignoring the cache age. With no symbols it refreshes every cached symbol; `universe=true` refreshes the whole universe plus any symbols given. | `symbols`, `universe` |

Conventions: dates are `YYYY-MM-DD` and points in time (quote time, fetch
time, news time) are RFC 3339 in UTC; `amount` is the size of one
contribution in the given currency (`USD` or `KRW`); money is rounded to
2 decimals, and share counts and per-share amounts (dividends, indicator
values) to 4; every field ending in `_pct` is a plain percentage (7.5
means 7.5%), and expense ratios keep 4 decimals (0.0945). The simulation
and comparison tools take only funds quoted in USD (US listings). A
simulation's share counts are the shares actually held on its end date,
and its timeline holds at most 120 points (month-ends, or quarter- or
year-ends for long ranges, as `timeline_step` says). Outputs that could
be read as advice carry a `disclaimer` field. Every tool is marked
read-only except `clear_cache` and `refresh_prices`. Unknown symbols and
bad input come back as tool errors whose message says what to change, so
the model can correct itself.

Also exposed: the resource `etf://universe` (the universe as CSV) and the
prompt `dca_report` (`symbol`, `amount`, `currency`, `start`, `cadence`,
`fee_rate`, `commission_fixed`), which asks the model to run
`get_etf_info`, `get_fund_profile`, `get_holdings`, `simulate_dca` and
`project_dca_outcomes` with those costs and write a short report that ends with
the disclaimer. By default it reviews a daily plan started three years
ago, without costs.

### Costs matter for small daily purchases

Two inputs model trading costs. `fee_rate` is a fraction of each purchase
(0.001 is 0.1%). `commission_fixed` is a flat amount in the plan currency
charged on every order, after `fee_rate`: as brokers do, it is charged
once for each ETF a contribution buys, so a contribution split over three
ETFs pays it three times, and each ETF's share of the contribution must
be larger than it. Every simulation tool accepts both.
`simulate_lump_sum_vs_dca` charges the commission once per ETF for the
lump sum and once per ETF on every contribution day for the DCA leg;
`project_dca_outcomes` folds it into the fee rate as
`fee_rate + (number of ETFs × commission_fixed) / amount`.

A flat commission weighs heavily on small purchases. Buying 5 USD of an
ETF every trading day with a 0.99 USD commission sends 19.8% of each
purchase to the commission: 249.48 USD a year on an outlay of 1,260 USD
(252 purchases). `review_dca_plan` lays this out in one call; for VOO,
whose expense ratio is 0.03%, it put the first-year cost at 19.81% of the
outlay in October 2026, almost all of it commission. Expense ratios are
already reflected in the adjusted closes the simulations use, so they are
not subtracted a second time.

## Data and cache

Daily price history comes from Yahoo Finance's unofficial chart API with
adjusted closes, dividends and splits; quotes, fund profiles, holdings,
performance, search and news come from Yahoo Finance as well. All of it
is delayed. KRW plans use the `KRW=X` rate (KRW per USD) from the same
source. Symbols outside the universe work when Yahoo knows them.

The cache is persistent and incremental. Each symbol's full daily history
is stored once, as `<SYMBOL>.json` in `~/Library/Caches/etf-insight-mcp`
(or the directory given by `-cache-dir` or `$ETF_INSIGHT_CACHE_DIR`).
Within 6 hours (`-cache-ttl`) a file is served without any network call.
After that, only the tail is fetched: the request starts 7 days before the
last cached bar, the tail must hold every cached day from there through
the last cached bar, the prices of at least one of those days other than
the newest are checked against the file, and the new bars are appended.
The whole history is fetched again instead when a cached day is missing
from the tail or no day could be checked, when the tail shows a new or
changed dividend, a new split, rewritten prices or a changed trading
calendar (each of these changes earlier adjusted prices), and in any case
once the last full fetch is 30 days old (`-full-refresh-days`). One
exception: shortly after the close the provider may not yet have
published the newest cached day; when nothing newer has arrived either,
that bar, captured during the session, is dropped and the next top-up
brings the settled one. When a download fails and a file exists, the file
is served and the tool result carries a warning.

A bar fetched while its trading session is still open is an intraday
price, not a close. The data source marks it, every tool whose figures
use that bar says so in its warnings or notes, and the cache treats the
file as stale as soon as the session ends, so the next read brings the
settled close.

Loading many symbols for the first time takes a while: a cold
`screen_universe` fetches about 126 histories. Tools that load several
symbols send MCP progress notifications, one per symbol, when the client
asks for them with a progress token; `refresh_prices` does the same.

When the fund data source reports an inception date, `get_etf_info`,
`get_fund_profile` and `review_dca_plan` warn if the price history starts
long before it (a predecessor product, such as the Semiconductor HOLDRS
behind the early SMH prices) or long after it.

Fund data lives under `fund/` in the same directory, one file per symbol
and kind (`<SYMBOL>.profile.json`, `<SYMBOL>.holdings.json`,
`<SYMBOL>.performance.json`), reused for 24 hours. Quotes are kept in
memory for 15 minutes; when a refresh fails, the last quote is shown and
marked `stale`. Search and news are not cached.

Three tools manage the cache from a conversation:

- `cache_status` reads the local disk only. Its `warnings` flag
  unreadable files (a truncated one included), failed price fetches or
  writes, a file larger than 10 MiB, a file not fetched for 90 days, more
  than 150 files, more than 300 MiB in total and a `fund/` directory that
  is a symlink (neither counted nor cleared). Failed fetches or writes of
  fund files are not tracked.
- `clear_cache` deletes the price and fund files of the given symbols, or
  of every symbol with `all=true`. It needs `confirm=true`; without it the
  call deletes nothing and fails with a tool error whose message previews
  the price and fund files and bytes that would go.
- `refresh_prices` downloads the full history of the given symbols again
  and replaces their files. With no symbols it refreshes every symbol
  already cached; `universe=true` refreshes every universe symbol plus
  any symbols given, and leaves other cached symbols alone (4 at a time,
  at most 200 per call). A failed download, or one that could not be
  written, keeps the previous file, and its row reports `ok=false`. A
  refresh that meets a top-up of the same symbol waits for it and then
  downloads the full history.

Flags, shown for the binary `make build` puts in `./bin` (after
`make install` it is `$(go env GOPATH)/bin/etf-insight-mcp`):

```sh
./bin/etf-insight-mcp -cache-dir DIR          # default: $ETF_INSIGHT_CACHE_DIR, else ~/Library/Caches/etf-insight-mcp
./bin/etf-insight-mcp -cache-ttl 6h           # how long a cached symbol is reused before it is topped up
./bin/etf-insight-mcp -full-refresh-days 30   # days of top-ups before the whole history is fetched again (1 to 36500)
./bin/etf-insight-mcp -clear-cache            # delete every file the server wrote in the cache directory, for all symbols, and exit
./bin/etf-insight-mcp -version                # print the version and exit
```

`-clear-cache` deletes the whole cache, every symbol at once and without
asking; to drop one symbol use `clear_cache` with `symbols` and
`confirm=true`. It removes only files the server wrote: price and fund
files, recognized by their name and by the cache header at the start of
the file, and the server's own temporary files left over for more than
ten minutes. Other files in the directory, such as another program's
`*.tmp` files or a file of your own named `2024-01-01.json`, are left
alone, and a `fund/` directory that is a symlink is not followed. Every
server start also sweeps the server's own temporary files older than ten
minutes. `-clear-cache` reports on stderr what it actually removed, and
lists after `still there:` anything it could not remove (exit status 1).
After the `simulate_dca` smoke test in
[Build and run locally](#build-and-run-locally), for example:

```sh
$ ./bin/etf-insight-mcp -cache-dir /tmp/etf-insight-cache -clear-cache
etf-insight-mcp: removed 3 files (3377013 bytes) from /tmp/etf-insight-cache
  price history: KRW=X, SPY, VOO
```

## Example prompts

Things to type in Claude once the server is connected:

1. "List the dividend ETFs and tell me which one has the highest trailing dividend yield right now."
2. "What would 10,000 KRW into VOO every trading day since 2021 be worth today, and how does that compare with SPY?"
3. "Simulate 300,000 KRW a month into 60% VOO / 40% SCHD from 2020 with a 0.1% fee, dividends reinvested. Show me the drawdown and the FX effect."
4. "Forecast 5 years of 100 USD a month into QQQ: give me the p10, p50 and p90 outcomes and the chance of ending below what I put in."
5. "Is SCHD above its 200-day average? Show monthly closes for the last two years and explain the trend reading."
6. "I buy 5 USD of VOO every trading day and pay a 0.99 USD commission each time. Review the plan: what do the costs come to, and how did plans like this do over 3 months and over 5 years?"
7. "Find alternatives to VOO: which funds have moved most closely with it over the last 3 years, and how do their expense ratios and tracking differences compare?"
8. "I have 12,000 USD for QQQ. Would putting it all in at the start of 2022 have done better than spreading it over 12 monthly purchases that year?"
9. "Run a 3-year monthly plan of 200 USD into SCHD from every start month in its history. How often did it end below what was put in, and what were the best and worst windows?"
10. "How are markets doing today? Then rank the dividend ETFs by trailing yield and compare the top three with SPY."

## Requirements

- Go 1.27.1+ (`brew install go`)
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
`./bin/etf-insight-mcp`, a path relative to the directory Claude Code
starts in. Build once, then start Claude Code in the repo root:

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

Remove that registration with `claude mcp remove --scope user etf-insight`.
Without `--scope`, in the repo root the name also matches the project's
`.mcp.json`, so the command either refuses or rewrites that file.

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

- **"Failed to connect"**: if `claude mcp list` shows it for
  `./bin/etf-insight-mcp`, check that Claude Code was started in the repo
  root and that `make build` has run, since the path is relative to the
  directory Claude Code starts in; from a subdirectory the server fails to
  connect. Otherwise run the binary directly in a terminal. If it prints a
  Go error to stderr, that is the cause. If it just waits, the path in the
  config is probably wrong.
- **Changed the code but Claude sees the old tool list**: rebuild, then
  restart the client. Claude Code re-spawns the server on `/mcp` reconnect;
  Desktop needs a full restart.
- **`claude mcp list` does not show etf-insight**: you started Claude Code
  outside the repo, or you declined the trust prompt. Run
  `claude mcp reset-project-choices` and start again.
- **A symbol returns "not found" although it exists**: "not found" means
  Yahoo answered 404, so the ticker is unknown, delisted or renamed; look
  it up with `search_symbols`. Rate limits show up as HTTP 429 errors
  instead, are retried automatically, and pass if you try again later.
- **A tool call fails with "invalid arguments"**: the message names the
  input and what is wrong with it: a missing input, a wrong type, a value
  outside its bounds, or an input that belongs to another tool, such as
  `allocations`, which `simulate_portfolio_dca` takes.
- **Cached prices look wrong or out of date**: ask for `cache_status` to
  see when each symbol was last fetched and whether a fetch failed, then
  `refresh_prices` for the symbol. To delete one symbol's files instead,
  use `clear_cache` with `symbols` and `confirm=true`;
  `./bin/etf-insight-mcp -clear-cache` in a terminal deletes the whole
  cache, every symbol, without asking.

## Develop

```sh
make build   # bin/etf-insight-mcp
make test    # go test -race -cover
make lint    # golangci-lint v2
make vet
```

Changes aim to add one tool or one Go concept at a time. CI runs vet,
tests and lint.
