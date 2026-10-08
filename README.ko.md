[English](README.md) | 한국어

# etf-insight-mcp

미국 ETF를 소액으로 꾸준히 사 모으는 적립식 투자(dollar-cost averaging)를
시뮬레이션하고, 그런 계획이 가져올 수 있는 결과의 범위를 추정하는 MCP 서버다.
단일 Go 바이너리가 stdio 위에서 로컬로 동작한다. 데이터베이스도, API 키도
필요 없다.

> 투자 조언이 아니다. 과거의 성과는 미래의 결과를 예측하지 않는다.
> ETF 유니버스는 오늘 기준의 상위 목록이므로 과거 결과에는 생존 편향이
> 섞여 있다.

## 상태

여섯 개 도구가 모두 끝까지 동작한다: `list_etfs`, `get_etf_info`,
`get_price_history`, `simulate_dca`, `simulate_portfolio_dca`,
`forecast_dca` (그리고 `ping`). 여기에 리소스 하나(`etf://universe`)와
프롬프트 하나(`dca_report`)가 있다. 가격은 Yahoo Finance의 비공식 차트
API에서 가져오며, 지연 데이터이고 디스크에 캐시된다.

다음 단계: 추세 기반 배분 규칙(예: "가격이 200일 이동평균 위에 있을 때만
산다")과 그 규칙을 단순 적립식 투자와 비교하는 기능이다.

## 도구

| 도구 | 답하는 질문 | 주요 입력 |
| --- | --- | --- |
| `list_etfs` | 어떤 ETF를 볼 수 있나? 널리 거래되는 약 100개 펀드의 내장 유니버스를 걸러 준다. 네트워크를 쓰지 않는다. | `category`, `issuer`, `query`, `include_leveraged` |
| `get_etf_info` | 이 펀드는 무엇이고 어떻게 움직여 왔나? 유니버스 항목, 제공자 메타데이터, 기간별 수익률(1개월~전체), 1년 변동성, 낙폭, 최근 12개월 배당, 52주 범위, 규칙 기반 추세 판정. | `symbol`, `as_of` |
| `get_price_history` | 차트나 직접 계산에 쓸 가격 데이터를 달라. 일간·주간·월간 종가, 수정 종가, 배당을 `max_points` 개수로 솎아서 돌려준다. | `symbol`, `start`, `end`, `interval`, `max_points` |
| `simulate_dca` | 이 ETF를 어느 날부터 매일·매주·매월 샀다면 어떻게 됐을까? SPY와 비교하면? | `symbol`, `amount`, `currency`, `cadence`, `start`, `end`, `fee_rate`, `reinvest_dividends`, `compare_with` |
| `simulate_portfolio_dca` | 60/40 같은 가중 포트폴리오로 같은 질문. 리밸런싱은 하지 않는다. 가중치 합은 1 또는 100이면 된다. | `allocations` (`[{symbol, weight}]`)와 `simulate_dca`의 입력 |
| `forecast_dca` | 이 계획을 N년 이어가면 결과 범위는 어떻게 되나? 종목 자신의 과거 수익률을 블록 부트스트랩으로 재추출한다: 최종 가치와 수익률의 p5~p95, 손실 확률, 가정을 문장으로 설명. 가격 예측이 아니다. | `symbol` 또는 `allocations`, `amount`, `currency`, `cadence`, `horizon_years`, `simulations`, `seed`, `block_length`, `lookback_years`, `expected_annual_return_pct` |
| `ping` | 서버가 살아 있나? 메시지를 버전과 함께 되돌려 준다. | `message` |

규약: 날짜는 `YYYY-MM-DD`다. `amount`는 지정한 통화(`USD` 또는 `KRW`)로
표시한 1회 납입 금액이다. 금액은 소수 둘째 자리, 주식 수는 넷째 자리까지
반올림한다. 이름이 `_pct`로 끝나는 필드는 그대로 퍼센트 값이다(7.5는
7.5%). 모든 시뮬레이션과 예측 출력에는 `disclaimer` 필드가 들어 있다. 모르는
심볼과 잘못된 입력은 무엇을 고쳐야 하는지 알려 주는 도구 오류로 돌아오므로
모델이 스스로 바로잡을 수 있다.

그 밖에 리소스 `etf://universe`(유니버스 CSV)와 프롬프트
`dca_report`(`symbol`, `amount`, `currency`, `start`)를 제공한다. 이 프롬프트는
모델에게 `get_etf_info`, `simulate_dca`, `forecast_dca`를 차례로 실행하고
면책 문구로 끝나는 짧은 보고서를 쓰도록 지시한다.

### 데이터와 캐시

가격은 Yahoo Finance의 비공식 차트 API에서 가져온다. 각 심볼의 전체 일간
이력과 수정 종가, 배당을 받으며 지연 데이터다. 심볼마다 JSON 파일 하나로
`~/Library/Caches/etf-insight-mcp` 아래에 캐시하고 6시간 동안 재사용한다.
KRW 계획은 같은 출처의 `KRW=X` 환율(1 USD당 KRW)을 쓴다. 유니버스 밖의 심볼도
Yahoo가 아는 것이면 동작한다.

플래그:

```sh
etf-insight-mcp -cache-dir DIR     # default: $ETF_INSIGHT_CACHE_DIR, else ~/Library/Caches/etf-insight-mcp
etf-insight-mcp -cache-ttl 6h      # how long a cached symbol is reused
etf-insight-mcp -version           # print the version and exit
```

## 프롬프트 예시

서버를 연결한 뒤 Claude에 이렇게 입력하면 된다:

1. "배당 ETF 목록을 보여 주고, 지금 최근 12개월 배당수익률이 가장 높은 것을 알려 줘."
2. "2021년부터 매 거래일 VOO에 10,000원씩 넣었다면 지금 얼마가 됐고, SPY와 비교하면 어때?"
3. "2020년부터 매월 30만 원을 VOO 60% / SCHD 40%로, 수수료 0.1%, 배당 재투자로 시뮬레이션해 줘. 낙폭과 환율 효과도 보여 줘."
4. "QQQ에 매월 100달러씩 5년 넣는 계획을 예측해 줘: p10, p50, p90 결과와 원금 아래로 끝날 확률을 알려 줘."
5. "SCHD가 200일 이동평균 위에 있어? 최근 2년 월간 종가를 보여 주고 추세 판정을 설명해 줘."

## 요구 사항

- Go 1.27+ (`brew install go`)
- 선택: `make lint`용 `golangci-lint` v2

## 로컬 빌드와 실행

```sh
git clone https://github.com/changbaebang/etf-insight-mcp.git
cd etf-insight-mcp
make build          # -> ./bin/etf-insight-mcp
make test
```

바이너리는 stdin/stdout으로 MCP를 말한다. 클라이언트 없이도 JSON-RPC 줄을
파이프로 넣어 찔러 볼 수 있다(응답이 흘러나올 시간을 주기 위해 stdin을 1초
열어 둔다):

```sh
( printf '%s\n%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping","arguments":{"message":"hello"}}}' ; sleep 1 ) \
  | ./bin/etf-insight-mcp
```

기대 결과: JSON 두 줄이 나오고, 두 번째 줄에 `"reply":"hello"`가 들어 있다.

실제 시뮬레이션은 처음 쓸 때 네트워크가 필요하므로 stdin을 조금 더 오래
열어 두고 캐시를 임시 디렉터리로 돌린다:

```sh
( printf '%s\n%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"simulate_dca","arguments":{"symbol":"VOO","amount":10000,"currency":"KRW","cadence":"daily","start":"2021-01-01"}}}' ; sleep 15 ) \
  | ./bin/etf-insight-mcp -cache-dir /tmp/etf-insight-cache
```

서버가 남기는 로그는 전부 stderr로 간다. stdout은 프로토콜 전용이므로 서버
코드에서 절대 `fmt.Println`을 쓰지 않는다.

## Claude Code 연결

저장소에는 `./bin/etf-insight-mcp`를 가리키는 프로젝트 범위의
[`.mcp.json`](.mcp.json)이 들어 있다. 한 번 빌드한 뒤 저장소 디렉터리 안에서
Claude Code를 시작한다:

```sh
make build
claude            # from the repo root
```

Claude Code는 프로젝트의 MCP 서버를 신뢰할지 한 번 묻는다. 연결은 다음으로
확인한다:

```sh
claude mcp list   # etf-insight: ./bin/etf-insight-mcp - ✓ Connected
```

그다음 예를 들어 이렇게 묻는다: "use etf-insight ping with message hi".

이 저장소 안에서만이 아니라 어느 디렉터리에서나 쓰려면 바이너리를 설치하고
사용자 범위로 등록한다:

```sh
make install                                   # -> ~/go/bin/etf-insight-mcp
claude mcp add --scope user etf-insight -- "$(go env GOPATH)/bin/etf-insight-mcp"
```

제거는 `claude mcp remove etf-insight`로 한다.

## Claude Desktop 연결

Claude Desktop은
`~/Library/Application Support/Claude/claude_desktop_config.json`(macOS)을
읽는다. 서버를 **절대 경로**로 추가한다. Desktop은 상대 경로나 `~`를 풀어
주지 않는다:

```json
{
  "mcpServers": {
    "etf-insight": {
      "command": "/Users/you/go/bin/etf-insight-mcp"
    }
  }
}
```

Claude Desktop을 재시작한다. 새 대화의 도구 아이콘 아래에 서버가 나타난다.
문제가 생기면 로그는 `~/Library/Logs/Claude/mcp-server-etf-insight.log`에
있다.

## 문제 해결

- **"Failed to connect"**: 터미널에서 바이너리를 직접 실행해 본다. stderr에
  Go 오류가 찍히면 그것이 원인이다. 그냥 기다리기만 하면 설정의 경로가 틀렸을
  가능성이 크다.
- **코드를 바꿨는데 Claude가 옛 도구 목록을 본다**: 다시 빌드한 뒤
  클라이언트를 재시작한다. Claude Code는 `/mcp` 재연결 시 서버를 다시 띄우고,
  Desktop은 완전히 재시작해야 한다.
- **`claude mcp list`에 아무것도 없다**: 저장소 루트가 아니거나 신뢰 프롬프트를
  거절한 경우다. `claude mcp reset-project-choices`를 실행하고 다시 시작한다.
- **존재하는 심볼인데 "not found"가 돌아온다**: Yahoo 차트 API는 상장 폐지되거나
  이름이 바뀐 티커에 404를 주고, 가끔 요청을 제한한다(429, 자동 재시도). 다시
  시도하거나 `~/Library/Caches/etf-insight-mcp`에서 해당 심볼 파일을 지워 강제로
  다시 받는다.

## 개발

```sh
make build   # bin/etf-insight-mcp
make test    # go test -race -cover
make lint    # golangci-lint v2
make vet
```

PR 하나가 도구 하나 또는 Go 개념 하나를 추가한다. CI는 vet, 테스트, lint를
실행한다.
