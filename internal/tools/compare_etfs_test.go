package tools

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/changbaebang/etf-insight-mcp/internal/analytics"
	"github.com/changbaebang/etf-insight-mcp/internal/market"
)

func TestCompareETFs(t *testing.T) {
	src := newAnalysisSource()
	fund := newAnalysisFakeFund()
	sess := newSession(t, analysisDeps(src, fund))

	t.Run("benchmark first, levered beta 2, identical correlation 1", func(t *testing.T) {
		var out compareETFsOutput
		callOK(t, sess, "compare_etfs", map[string]any{"symbols": []string{"sso", "VOO", "IVV"}}, &out)
		if out.Benchmark != "SPY" || !slices.Equal(out.Symbols, []string{"SPY", "SSO", "VOO", "IVV"}) {
			t.Fatalf("benchmark/symbols = %s/%v, want SPY first", out.Benchmark, out.Symbols)
		}
		if len(out.ETFs) != 4 || len(out.CorrelationMatrix) != 4 {
			t.Fatalf("%d rows, %d matrix rows, want 4", len(out.ETFs), len(out.CorrelationMatrix))
		}
		spy, sso, voo, ivv := out.ETFs[0], out.ETFs[1], out.ETFs[2], out.ETFs[3]
		if !spy.IsBenchmark || sso.IsBenchmark || spy.BetaToBenchmark != 1 || spy.CorrelationToBenchmark != 1 {
			t.Errorf("benchmark row = %+v", spy)
		}
		if sso.BetaToBenchmark != 2 || sso.CorrelationToBenchmark != 1 {
			t.Errorf("SSO beta/corr = %v/%v, want 2/1 (log returns are exactly twice SPY's)", sso.BetaToBenchmark, sso.CorrelationToBenchmark)
		}
		if got := out.CorrelationMatrix[2][3]; got != 1 {
			t.Errorf("corr(VOO, IVV) = %v, want 1 (identical returns)", got)
		}
		if voo.BetaToBenchmark != ivv.BetaToBenchmark || voo.CorrelationToBenchmark != ivv.CorrelationToBenchmark {
			t.Errorf("VOO and IVV differ against SPY: %+v vs %+v", voo, ivv)
		}
		for i := range out.CorrelationMatrix {
			if out.CorrelationMatrix[i][i] != 1 {
				t.Errorf("diagonal %d = %v", i, out.CorrelationMatrix[i][i])
			}
			if out.CorrelationMatrix[i][0] != out.ETFs[i].CorrelationToBenchmark {
				t.Errorf("row %d: matrix %v != correlation_to_benchmark %v", i, out.CorrelationMatrix[i][0], out.ETFs[i].CorrelationToBenchmark)
			}
			for j := range out.CorrelationMatrix {
				if out.CorrelationMatrix[i][j] != out.CorrelationMatrix[j][i] {
					t.Errorf("matrix not symmetric at %d,%d", i, j)
				}
			}
		}

		// The figures are analytics.Compare's, rounded.
		series := []*market.Series{src.series["SPY"], src.series["SSO"], src.series["VOO"], src.series["IVV"]}
		cmp := analysisMust(analytics.Compare(series, time.Time{}, time.Time{}))(t)
		if out.CommonRange != (analysisRange{From: "2021-01-04", To: formatDate(cmp.To), Bars: cmp.Bars}) {
			t.Errorf("common_range = %+v, want 2021-01-04..%s (%d)", out.CommonRange, formatDate(cmp.To), cmp.Bars)
		}
		if got, want := voo.CorrelationToBenchmark, round4(cmp.Correlation[2][0]); got != want {
			t.Errorf("VOO corr to SPY = %v, want %v", got, want)
		}
		sum := toSummaryOutput(cmp.Summaries[2])
		if voo.Volatility1YPct != sum.Volatility1YPct || voo.MaxDrawdownAllPct != sum.MaxDrawdownAllPct || voo.TTMDividendYieldPct != sum.TTMDividendYieldPct || !reflect.DeepEqual(voo.Returns, sum.Windows) {
			t.Errorf("VOO summary fields do not match analytics: %+v", voo)
		}
		if voo.Name != "Vanguard S&P 500 ETF" || voo.Category != "US Large Cap" || voo.TrendState == "" {
			t.Errorf("VOO name/category/trend = %q/%q/%q", voo.Name, voo.Category, voo.TrendState)
		}
		if spy.ExpenseRatioPct == nil || *spy.ExpenseRatioPct != 0.0945 || voo.ExpenseRatioPct == nil || *voo.ExpenseRatioPct != 0.03 {
			t.Errorf("expense ratios SPY/VOO = %v/%v, want 0.0945/0.03", spy.ExpenseRatioPct, voo.ExpenseRatioPct)
		}
		if out.Disclaimer != Disclaimer || len(out.Notes) < 2 || len(out.Warnings) != 0 {
			t.Errorf("disclaimer/notes/warnings = %q/%v/%v", out.Disclaimer, out.Notes, out.Warnings)
		}
	})

	t.Run("custom benchmark already in the list", func(t *testing.T) {
		var out compareETFsOutput
		callOK(t, sess, "compare_etfs", map[string]any{"symbols": []string{"MTUM", "VOO"}, "benchmark": "voo"}, &out)
		if !slices.Equal(out.Symbols, []string{"VOO", "MTUM"}) || out.Benchmark != "VOO" {
			t.Fatalf("symbols = %v, want [VOO MTUM]", out.Symbols)
		}
		if mtum := out.ETFs[1]; mtum.BetaToBenchmark != 2 || mtum.CorrelationToBenchmark != 1 {
			t.Errorf("MTUM beta/corr to VOO = %v/%v, want 2/1", mtum.BetaToBenchmark, mtum.CorrelationToBenchmark)
		}
	})

	t.Run("a young fund sets the common range", func(t *testing.T) {
		var out compareETFsOutput
		callOK(t, sess, "compare_etfs", map[string]any{"symbols": []string{"SCHD", "VOO"}}, &out)
		if out.CommonRange.From != "2022-01-03" {
			t.Errorf("common_range.from = %s, want 2022-01-03", out.CommonRange.From)
		}
		if !slices.ContainsFunc(out.Notes, func(n string) bool { return strings.Contains(n, "because SCHD's history starts there") }) {
			t.Errorf("notes = %v, want the SCHD range note", out.Notes)
		}
	})

	t.Run("start and end bound the range and the as-of date", func(t *testing.T) {
		var out compareETFsOutput
		callOK(t, sess, "compare_etfs", map[string]any{"symbols": []string{"VOO", "QQQ"}, "start": "2022-01-01", "end": "2022-12-31"}, &out)
		if out.CommonRange.From != "2022-01-03" || out.CommonRange.To != "2022-12-30" {
			t.Errorf("common_range = %+v, want 2022-01-03..2022-12-30", out.CommonRange)
		}
		if w := out.ETFs[1].Returns[0]; w.Label != "1m" || !w.Available || w.From != "2022-11-30" {
			t.Errorf("first return window = %+v, want 1m ending 2022-12-30", w)
		}
	})

	t.Run("expense ratios are null without a fund source", func(t *testing.T) {
		var out compareETFsOutput
		callOK(t, newSession(t, analysisDeps(src, nil)), "compare_etfs", map[string]any{"symbols": []string{"VOO", "IVV"}}, &out)
		for _, row := range out.ETFs {
			if row.ExpenseRatioPct != nil {
				t.Errorf("%s expense_ratio_pct = %v, want null", row.Symbol, *row.ExpenseRatioPct)
			}
		}
		if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "no fund data source") {
			t.Errorf("warnings = %v, want the missing fund source", out.Warnings)
		}
	})

	t.Run("a failed expense lookup is a warning, not an error", func(t *testing.T) {
		failing := newAnalysisFakeFund()
		failing.fail["IVV"] = true
		var out compareETFsOutput
		callOK(t, newSession(t, analysisDeps(src, failing)), "compare_etfs", map[string]any{"symbols": []string{"VOO", "IVV"}}, &out)
		if out.ETFs[2].ExpenseRatioPct != nil || out.ETFs[1].ExpenseRatioPct == nil {
			t.Errorf("expense ratios = %v/%v, want VOO set and IVV null", out.ETFs[1].ExpenseRatioPct, out.ETFs[2].ExpenseRatioPct)
		}
		if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "failed for IVV") {
			t.Errorf("warnings = %v", out.Warnings)
		}
	})

	elevenSymbols := []string{"VOO", "IVV", "SPYM", "RSP", "DIA", "DFAC", "MTUM", "QQQ", "SCHD", "BND", "AGG"}
	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown symbol", map[string]any{"symbols": []string{"VOO", "XYZ"}}, "unknown symbol XYZ"},
		{"unknown benchmark", map[string]any{"symbols": []string{"VOO", "IVV"}, "benchmark": "NOPE"}, "unknown symbol NOPE"},
		{"universe symbol missing upstream", map[string]any{"symbols": []string{"VOO", "VTI"}}, "VTI is in the universe but the data source returned not found"},
		{"one symbol", map[string]any{"symbols": []string{"VOO"}}, "symbols"},
		{"duplicates", map[string]any{"symbols": []string{"VOO", " voo"}}, "at least 2 different tickers"},
		{"too many", map[string]any{"symbols": elevenSymbols}, "symbols"},
		{"end before start", map[string]any{"symbols": []string{"VOO", "IVV"}, "start": "2023-01-01", "end": "2022-01-01"}, "before start"},
		{"bad date", map[string]any{"symbols": []string{"VOO", "IVV"}, "start": "2023/01/01"}, "YYYY-MM-DD"},
		{"no common days", map[string]any{"symbols": []string{"VOO", "IVV"}, "start": "2022-06-18", "end": "2022-06-19"}, "0 common trading days between 2022-06-18 and 2022-06-19, need at least 2; data covers SPY 2021-01-04 to"},
		{"disjoint histories", map[string]any{"symbols": []string{"DIA", "COWZ"}}, "need at least 2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "compare_etfs", tt.args, tt.want)
		})
	}

	t.Run("compareSymbols counts distinct tickers", func(t *testing.T) {
		if _, _, err := compareSymbols(elevenSymbols, ""); err == nil || !strings.Contains(err.Error(), "at most 10") {
			t.Errorf("eleven symbols error = %v", err)
		}
		syms, bench, err := compareSymbols([]string{"qqq", "spy"}, " ")
		if err != nil || bench != "SPY" || !slices.Equal(syms, []string{"SPY", "QQQ"}) {
			t.Errorf("compareSymbols = %v/%s/%v", syms, bench, err)
		}
	})
}
