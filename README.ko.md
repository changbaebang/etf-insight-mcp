[English](README.md) | 한국어

# etf-insight-mcp

미국 ETF를 소액으로 꾸준히 사 모으는 적립식 투자(dollar-cost averaging)를
시뮬레이션하고, 그런 계획이 가져올 수 있는 결과의 범위를 추정하는 MCP 서버다.
시세, 배당, 펀드 정보도 조회하고, ETF를 비교·선별하며, 계획에 드는 비용을
점검한다. 단일 Go 바이너리가 stdio 위에서 로컬로 동작한다. 데이터베이스도,
API 키도 필요 없다.

> 투자 조언이 아니다. 과거의 성과는 미래의 결과를 예측하지 않는다.
> ETF 유니버스는 오늘 존재하는 펀드를 직접 고른 목록이므로 과거 결과에는 생존 편향이
> 섞여 있다.

## 상태

도구 26개가 모두 실제로 동작한다. 아래처럼 데이터 12개, 분석 4개,
시뮬레이션 6개, 캐시와 운영 4개(`ping` 포함)로 나뉜다. 여기에 리소스
하나(`etf://universe`)와 프롬프트 하나(`dca_report`)가 있다. 가격 이력, 시세,
펀드 정보, 검색, 뉴스는 Yahoo Finance의 비공식 API에서 가져오며 지연
데이터다. 가격 이력은 디스크에 캐시하고 증분으로 최신화하며, 펀드 정보는
하루 동안 캐시한다.

다음 단계: 추세 기반 배분 규칙(예: "가격이 200일 이동평균 위에 있을 때만
산다")과 그 규칙을 단순 적립식 투자와 비교하는 기능이다.

## 도구

표는 서버가 도구를 나열하는 순서를 따른다.

### 데이터

| 도구 | 답하는 질문 | 주요 입력 |
| --- | --- | --- |
| `get_dividends` | 이 펀드는 배당을 얼마나, 얼마나 자주 주고, 늘고 있나? 모든 지급 내역(최근 120건), 최근 12개월 합계와 배당수익률, 빈도 라벨이 붙은 연간 지급 횟수, 연도별 합계, 연간 합계의 5년 증가율. | `symbol`, `start`, `end` |
| `get_etf_info` | 이 펀드는 무엇이고 어떻게 움직여 왔나? 유니버스 항목, 제공자 메타데이터, 기간별 수익률(1개월~전체), 1년 변동성, 낙폭, 최근 12개월 배당, 52주 범위, 규칙 기반 추세 판정. | `symbol`, `as_of` |
| `get_fund_performance` | 제공자가 보고한 기준으로 이 펀드는 같은 카테고리와 비교해 어떤가? 기간별·연도별 수익률을 카테고리 평균과 나란히 보여 주고, 3·5·10년 위험 통계(알파, 베타, 샤프, 표준편차, 결정계수, 트레이너)를 준다. | `symbol` |
| `get_fund_profile` | 이 펀드는 비용이 얼마이고 어떤 펀드인가? 총보수(소수 넷째 자리)와 10,000 보유 시 연간 비용, 운용사, 카테고리, 설정일, 회전율, 순자산, 수익률. | `symbol` |
| `get_holdings` | 이 펀드는 무엇을 담고 있나? 상위 보유 종목(최대 25개)과 그 합계 비중, 섹터 비중, 주식·채권·현금 구성, 채권 신용등급, 포트폴리오 통계. | `symbol` |
| `get_news` | 이 움직임을 설명하는 뉴스가 있나? 최근 헤드라인과 매체, 링크, 시각. 헤드라인만 주고 기사 본문은 주지 않는다. | `query`, `limit` |
| `get_price_history` | 차트나 직접 계산에 쓸 가격 데이터를 달라. 일간·주간·월간 종가, 수정 종가, 배당을 `max_points` 개수로 솎아서 돌려준다. | `symbol`, `start`, `end`, `interval`, `max_points` |
| `get_quote` | 지금 얼마에 거래되고 있나? 심볼 1~50개의 지연 시세: 가격, 등락, 당일 범위, 거래량, 52주 범위, 50·200일 평균, 시장 상태. 모르는 심볼은 `missing`에 담긴다. | `symbols` |
| `get_splits` | 원래 주가가 왜 갑자기 뛰었나? 전체 이력의 액면분할과 액면병합(최대 100건). 다른 도구의 가격은 이미 분할이 반영되어 있다. | `symbol` |
| `list_etfs` | 어떤 ETF를 볼 수 있나? 널리 보유되는 약 125개 펀드의 내장 유니버스를 걸러 준다. 네트워크를 쓰지 않는다. | `category`, `issuer`, `query`, `include_leveraged` |
| `market_overview` | 오늘 시장은 어떤가? SPY, QQQ, DIA, IWM, VEA, VWO, TLT, BND, GLD와 VIX의 시세와 등락, 그리고 ETF별 추세 상태와 200일 평균 대비 거리. | 없음 |
| `search_symbols` | 이 펀드의 티커는 무엇인가? 심볼, 이름, 단어로 Yahoo Finance를 검색한다. 기본값은 ETF만이며, `in_universe`는 `list_etfs`가 아는 펀드를 표시한다. | `query`, `limit`, `etf_only` |

### 분석

| 도구 | 답하는 질문 | 주요 입력 |
| --- | --- | --- |
| `compare_etfs` | 이 펀드들은 어떻게 다른가? ETF 2~10개를 벤치마크(기본 SPY)와 나란히 놓는다: 총보수, 기간별 수익률, 변동성, 낙폭, 배당수익률, 추세, 벤치마크 대비 베타와 상관계수, 그리고 공통 기간의 상관 행렬. | `symbols`, `start`, `end`, `benchmark` |
| `find_alternatives` | 이 ETF를 꾸준히 사고 있는데, 비슷한 것이 있나? 어떻게 다른가? 유니버스 펀드를 최근 3년 상관계수 순으로, 같으면 총보수가 낮은 순으로 정렬하고, 베타, 총보수 차이, 배당수익률, 수익률, 낙폭, 추적 차이, 한 줄 관찰을 붙인다. 참고용으로 상관이 가장 낮은 펀드 3개도 준다. 조언이 아니라 사실이다. | `symbol`, `limit`, `min_correlation`, `include_other_categories` |
| `get_technical_indicators` | 특정 날짜에 흔히 쓰는 차트 지표는 어떤가? RSI, MACD, 볼린저 밴드와 %B, ATR, 단순·지수 이동평균, 규칙 충족 여부를 문장으로, 그리고 추세 블록. 가격 경로를 설명할 뿐 매매 신호가 아니다. | `symbol`, `as_of` |
| `screen_universe` | 한 지표에서 어떤 펀드가 가장 높은가? 유니버스를 12-1 모멘텀, 1년·3개월 수익률, 변동성, 낙폭, 배당수익률, 200일 평균 대비 거리 중 하나로 정렬한다. 각 행에는 일곱 지표가 모두 들어 있다. 첫 호출은 선별 대상 펀드를 전부 불러온다(10~90초). | `sort_by`, `descending`, `category`, `include_leveraged`, `limit`, `as_of` |

### 시뮬레이션

| 도구 | 답하는 질문 | 주요 입력 |
| --- | --- | --- |
| `forecast_dca` | 이 계획을 N년 이어가면 결과 범위는 어떻게 되나? 종목 자신의 과거 수익률을 블록 부트스트랩으로 재추출한다: 최종 가치와 수익률의 p5~p95, 손실 확률, 가정을 문장으로 설명. 가격 예측이 아니다. | `symbol` 또는 `allocations`, `amount`, `currency`, `cadence`, `horizon_years`, `fee_rate`, `simulations`, `seed`, `block_length`, `lookback_years`, `expected_annual_return_pct` |
| `review_dca_plan` | 소액 적립 계획에 실제로 드는 비용은 얼마이고, 비슷한 계획은 어떻게 됐나? ETF 하나를 USD로 사는 계획 하나에 대해 수수료와 총보수를 금액과 비율로, 과거 이력, 실제 이력 기반 단기·장기 결과, 부트스트랩 추정, 사실 관찰을 준다. 추천은 하지 않는다. | `symbol`, `amount`, `cadence`, `horizon_years`, `short_horizon_months`, `fee_rate`, `commission_fixed`, `reinvest_dividends` |
| `simulate_dca` | 이 ETF를 어느 날부터 매일·매주·매월 샀다면 어떻게 됐을까? SPY와 비교하면? | `symbol`, `amount`, `currency`, `cadence`, `start`, `end`, `fee_rate`, `commission_fixed`, `reinvest_dividends`, `compare_with` |
| `simulate_lump_sum_vs_dca` | 한 번에 넣을까, 나눠 넣을까? 같은 총액을 첫날 한꺼번에 넣은 경우와 모든 납입일에 나눠 넣은 경우를 같은 거래일과 같은 수수료로 비교하고 차이를 준다. | `symbol` 또는 `allocations`, `total_amount`, `currency`, `cadence`, `start`, `end`, `fee_rate`, `commission_fixed`, `reinvest_dividends` |
| `simulate_portfolio_dca` | 60/40 같은 가중 포트폴리오로 `simulate_dca`와 같은 질문. 리밸런싱은 하지 않는다. 가중치 합은 1 또는 100이면 된다. | `allocations` (`[{symbol, weight}]`)와 `simulate_dca`의 입력 |
| `simulate_rolling_dca` | 시작 시점이 얼마나 중요했나? 같은 계획을 이력 안의 모든 N년 구간에 단계마다 하나씩 돌려, 결과의 백분위수, 원금 아래로 끝난 구간의 비율, 가장 좋았던 구간과 나빴던 구간을 준다. | `symbol` 또는 `allocations`, `amount`, `currency`, `cadence`, `duration_years`, `step_months`, `fee_rate`, `commission_fixed`, `reinvest_dividends` |

### 캐시와 운영

| 도구 | 답하는 질문 | 주요 입력 |
| --- | --- | --- |
| `cache_status` | 로컬 캐시에 무엇이 있나? 디렉터리, 파일 수와 크기, 심볼별 봉 개수, 날짜 범위, 마지막 최신화, 마지막 전체 수신, 크기, 마지막 수신 실패, 그리고 경고. 로컬 디스크만 읽는다. | 없음 |
| `clear_cache` | 일부 심볼 또는 전체 심볼의 캐시 데이터를 지운다. `confirm=true`가 없으면 아무것도 지우지 않고 미리보기를 돌려준다. | `symbols`, `all`, `confirm` |
| `ping` | 서버가 살아 있나? 메시지를 버전과 함께 되돌려 준다. | `message` |
| `refresh_prices` | 캐시 나이와 상관없이 전체 가격 이력을 다시 받는다. 심볼을 주지 않으면 캐시에 있는 모든 심볼을 다시 받고, `universe=true`면 유니버스 전체를 더한다. | `symbols`, `universe` |

규약: 날짜는 `YYYY-MM-DD`이고, 시각(시세 시각, 수신 시각, 뉴스 시각)은
UTC 기준 RFC 3339다. `amount`는 지정한 통화(`USD` 또는 `KRW`)로 표시한 1회
납입 금액이다. 금액은 소수 둘째 자리, 주식 수는 넷째 자리까지 반올림한다.
이름이 `_pct`로 끝나는 필드는 그대로 퍼센트 값이고(7.5는 7.5%), 총보수는
소수 넷째 자리까지 유지한다(0.0945). 조언으로 읽힐 수 있는 출력에는
`disclaimer` 필드가 들어 있다. `clear_cache`와 `refresh_prices`를 뺀 모든
도구는 읽기 전용으로 표시된다. 모르는 심볼과 잘못된 입력은 무엇을 고쳐야
하는지 알려 주는 도구 오류로 돌아오므로 모델이 스스로 바로잡을 수 있다.

그 밖에 리소스 `etf://universe`(유니버스 CSV)와 프롬프트
`dca_report`(`symbol`, `amount`, `currency`, `start`)를 제공한다. 이 프롬프트는
모델에게 `get_etf_info`, `simulate_dca`, `forecast_dca`를 차례로 실행하고
면책 문구로 끝나는 짧은 보고서를 쓰도록 지시한다.

### 소액 매일 매수에서는 비용이 중요하다

거래 비용은 두 입력으로 반영한다. `fee_rate`는 매수 금액에 대한 비율이다
(0.001은 0.1%). `commission_fixed`는 매수할 때마다 `fee_rate` 다음에 붙는
계획 통화 기준 고정 금액이다. `simulate_dca`,
`simulate_portfolio_dca`(심볼마다가 아니라 납입마다 한 번),
`simulate_lump_sum_vs_dca`(일시 투자는 한 번, 적립 쪽은 매수마다 한 번),
`simulate_rolling_dca`, `review_dca_plan`은 둘 다 받는다. `forecast_dca`는
`fee_rate`만 받는다.

고정 수수료는 소액 매수에 크게 작용한다. 매 거래일 ETF를 5 USD씩 사고
매번 0.99 USD를 수수료로 내면 매수 금액의 19.8%가 수수료로 나간다. 1년
납입액 1,260 USD(매수 252회) 중 249.48 USD다. `review_dca_plan`은 이를 한
번의 호출로 정리한다. 총보수가 0.03%인 VOO의 경우 2026년 10월 기준 첫해
비용을 납입액의 19.81%로 계산했고, 그 대부분이 수수료였다. 총보수는
시뮬레이션이 쓰는 수정 종가에 이미 반영되어 있으므로 다시 빼지 않는다.

## 데이터와 캐시

일간 가격 이력은 Yahoo Finance의 비공식 차트 API에서 수정 종가, 배당,
분할과 함께 가져온다. 시세, 펀드 개요, 보유 종목, 성과, 검색, 뉴스도
Yahoo Finance에서 가져온다. 모두 지연 데이터다. KRW 계획은 같은 출처의
`KRW=X` 환율(1 USD당 KRW)을 쓴다. 유니버스 밖의 심볼도 Yahoo가 아는 것이면
동작한다.

캐시는 영구적이고 증분 방식이다. 심볼마다 전체 일간 이력을
`~/Library/Caches/etf-insight-mcp`(또는 `-cache-dir`나
`$ETF_INSIGHT_CACHE_DIR`로 지정한 디렉터리)에 `<SYMBOL>.json`으로 한 번만
저장한다. 6시간(`-cache-ttl`) 안에는 네트워크 호출 없이 파일을 그대로 쓴다.
그 뒤에는 끝부분만 받는다. 마지막으로 캐시된 봉보다 7일 앞에서부터 요청해
겹치는 날을 파일과 대조한 다음 새 봉을 덧붙인다. 끝부분에 새로 생기거나
바뀐 배당, 새 분할, 다시 쓰인 가격, 바뀐 거래일이 보이면(모두 과거의 수정
가격을 바꾼다) 덧붙이는 대신 전체 이력을 다시 받는다. 그렇지 않더라도
마지막 전체 수신이 30일(`-full-refresh-days`)을 넘기면 전체를 다시 받는다.
다운로드가 실패했는데 파일이 있으면 그 파일을 쓰고 도구 결과에 경고를
붙인다.

펀드 정보는 같은 디렉터리의 `fund/` 아래에 심볼과 종류별로 파일 하나씩
(`<SYMBOL>.profile.json`, `<SYMBOL>.holdings.json`,
`<SYMBOL>.performance.json`) 두고 24시간 동안 재사용한다. 시세는 메모리에
15분 동안 보관하며, 검색과 뉴스는 캐시하지 않는다.

대화 안에서 캐시를 다루는 도구는 셋이다:

- `cache_status`는 로컬 디스크만 읽는다. `warnings`는 읽을 수 없는 파일,
  수신이나 쓰기 실패, 10 MiB보다 큰 파일, 90일 동안 받지 않은 파일, 150개를
  넘는 파일 수, 합계 300 MiB 초과를 알린다.
- `clear_cache`는 지정한 심볼의 가격 파일과 펀드 파일을 지우고, `all=true`면
  모든 심볼의 파일을 지운다. `confirm=true`가 필요하며, 없으면 아무것도
  지우지 않고 지워질 파일 수와 바이트를 미리 보여 준다.
- `refresh_prices`는 지정한 심볼의 전체 이력을 다시 받아 파일을 바꾼다.
  심볼을 주지 않으면 이미 캐시된 모든 심볼을 다시 받고, `universe=true`면
  유니버스의 모든 심볼을 더한다(동시에 4개, 호출당 최대 200개). 다운로드가
  실패하면 이전 파일을 그대로 둔다.

플래그:

```sh
etf-insight-mcp -cache-dir DIR          # default: $ETF_INSIGHT_CACHE_DIR, else ~/Library/Caches/etf-insight-mcp
etf-insight-mcp -cache-ttl 6h           # how long a cached symbol is reused before it is topped up
etf-insight-mcp -full-refresh-days 30   # days of top-ups before the whole history is fetched again
etf-insight-mcp -clear-cache            # delete every file the server wrote in the cache directory and exit
etf-insight-mcp -version                # print the version and exit
```

`-clear-cache`는 서버가 쓴 파일(가격 파일, 펀드 파일, 남은 임시 파일)만
지우고 지운 내용을 stderr로 알린다. 예를 들어
[로컬 빌드와 실행](#로컬-빌드와-실행)의 `simulate_dca` 스모크 테스트를 돌린
뒤라면 이렇게 나온다:

```sh
$ ./bin/etf-insight-mcp -cache-dir /tmp/etf-insight-cache -clear-cache
etf-insight-mcp: removed 3 files (3376445 bytes) from /tmp/etf-insight-cache
  price history: KRW=X, SPY, VOO
```

## 프롬프트 예시

서버를 연결한 뒤 Claude에 이렇게 입력하면 된다:

1. "배당 ETF 목록을 보여 주고, 지금 최근 12개월 배당수익률이 가장 높은 것을 알려 줘."
2. "2021년부터 매 거래일 VOO에 10,000원씩 넣었다면 지금 얼마가 됐고, SPY와 비교하면 어때?"
3. "2020년부터 매월 30만 원을 VOO 60% / SCHD 40%로, 수수료 0.1%, 배당 재투자로 시뮬레이션해 줘. 낙폭과 환율 효과도 보여 줘."
4. "QQQ에 매월 100달러씩 5년 넣는 계획을 예측해 줘: p10, p50, p90 결과와 원금 아래로 끝날 확률을 알려 줘."
5. "SCHD가 200일 이동평균 위에 있어? 최근 2년 월간 종가를 보여 주고 추세 판정을 설명해 줘."
6. "매 거래일 VOO를 5달러씩 사고 매번 수수료로 0.99달러를 내고 있어. 이 계획을 점검해 줘: 비용이 얼마나 되고, 이런 계획이 3개월과 5년 동안 어떻게 됐는지 알려 줘."
7. "VOO의 대안을 찾아 줘: 최근 3년 동안 VOO와 가장 비슷하게 움직인 펀드는 무엇이고, 총보수와 추적 차이는 어떻게 달라?"
8. "QQQ에 넣을 12,000달러가 있어. 2022년 초에 한꺼번에 넣는 것이 그해 12번에 나눠 매월 사는 것보다 나았을까?"
9. "SCHD에 매월 200달러씩 3년 넣는 계획을 이력의 모든 시작 월에서 돌려 줘. 원금 아래로 끝난 경우는 얼마나 됐고, 가장 좋았던 구간과 나빴던 구간은 언제야?"
10. "오늘 시장은 어때? 그다음 배당 ETF를 최근 배당수익률 순으로 줄 세우고 상위 3개를 SPY와 비교해 줘."

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

바이너리는 stdin/stdout으로 MCP 프로토콜을 주고받는다. 클라이언트 없이도 JSON-RPC 줄을
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
열어 두고 캐시 디렉터리를 임시 디렉터리로 지정한다:

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
- **`claude mcp list`에 etf-insight가 없다**: 저장소 루트가 아니거나 신뢰 프롬프트를
  거절한 경우다. `claude mcp reset-project-choices`를 실행하고 다시 시작한다.
- **존재하는 심볼인데 "not found"가 돌아온다**: Yahoo 차트 API는 상장 폐지되거나
  이름이 바뀐 티커에 404를 주고, 가끔 요청을 제한한다(429, 자동 재시도). 다시
  시도하거나, 그 심볼에 `refresh_prices`를 요청해 전체를 강제로 다시 받는다.
- **캐시된 가격이 틀렸거나 오래돼 보인다**: `cache_status`를 요청해 심볼마다
  언제 받았는지, 수신이 실패했는지 확인한 뒤 그 심볼에 `refresh_prices`를
  요청한다. 파일을 아예 지우려면 `clear_cache`(또는 터미널에서
  `etf-insight-mcp -clear-cache`)를 쓴다.

## 개발

```sh
make build   # bin/etf-insight-mcp
make test    # go test -race -cover
make lint    # golangci-lint v2
make vet
```

PR 하나가 도구 하나 또는 Go 개념 하나를 추가한다. CI는 vet, 테스트, lint를
실행한다.
