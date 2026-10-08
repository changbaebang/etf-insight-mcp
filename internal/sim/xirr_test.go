package sim

import (
	"math"
	"testing"
)

func TestXIRR(t *testing.T) {
	flow := func(t *testing.T, d string, amount float64) CashFlow {
		return CashFlow{Date: date(t, d), Amount: amount}
	}
	tests := []struct {
		name  string
		flows func(t *testing.T) []CashFlow
		want  float64
	}{
		{
			// 730 days apart (no leap day), so 121 = 100 × 1.1².
			name: "two flows 10 percent",
			flows: func(t *testing.T) []CashFlow {
				return []CashFlow{flow(t, "2021-01-01", -100), flow(t, "2023-01-01", 121)}
			},
			want: 0.10,
		},
		{
			// By hand at 5%: 1000 × 1.05² + 1000 × 1.05 = 1102.5 + 1050.
			name: "three flows 5 percent",
			flows: func(t *testing.T) []CashFlow {
				return []CashFlow{
					flow(t, "2021-01-01", -1000),
					flow(t, "2022-01-01", -1000),
					flow(t, "2023-01-01", 2152.5),
				}
			},
			want: 0.05,
		},
		{
			// 81 = 100 × 0.9², a loss of 10% per year.
			name: "negative rate",
			flows: func(t *testing.T) []CashFlow {
				return []CashFlow{flow(t, "2021-01-01", -100), flow(t, "2023-01-01", 81)}
			},
			want: -0.10,
		},
		{
			name: "order of flows does not matter",
			flows: func(t *testing.T) []CashFlow {
				return []CashFlow{flow(t, "2023-01-01", 121), flow(t, "2021-01-01", -100)}
			},
			want: 0.10,
		},
		{
			// 1.21^(365/730) = 1.1 over one year of 365 days.
			name: "one year uses 365 days",
			flows: func(t *testing.T) []CashFlow {
				return []CashFlow{flow(t, "2021-01-01", -100), flow(t, "2022-01-01", 110)}
			},
			want: 0.10,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := XIRR(tc.flows(t))
			if err != nil {
				t.Fatalf("XIRR: %v", err)
			}
			if math.Abs(got-tc.want) > 1e-6 {
				t.Errorf("XIRR = %.9f, want %.9f", got, tc.want)
			}
		})
	}
}

func TestXIRRErrors(t *testing.T) {
	flow := func(t *testing.T, d string, amount float64) CashFlow {
		return CashFlow{Date: date(t, d), Amount: amount}
	}
	tests := []struct {
		name  string
		flows func(t *testing.T) []CashFlow
	}{
		{"no flows", func(*testing.T) []CashFlow { return nil }},
		{"single flow", func(t *testing.T) []CashFlow { return []CashFlow{flow(t, "2021-01-01", -100)} }},
		{"all negative", func(t *testing.T) []CashFlow {
			return []CashFlow{flow(t, "2021-01-01", -100), flow(t, "2022-01-01", -100)}
		}},
		{"all positive", func(t *testing.T) []CashFlow {
			return []CashFlow{flow(t, "2021-01-01", 100), flow(t, "2022-01-01", 100)}
		}},
		{"same day", func(t *testing.T) []CashFlow {
			return []CashFlow{flow(t, "2021-01-01", -100), flow(t, "2021-01-01", 100)}
		}},
		{"NaN amount", func(t *testing.T) []CashFlow {
			return []CashFlow{flow(t, "2021-01-01", -100), flow(t, "2022-01-01", math.NaN())}
		}},
		{"rate above the bracket", func(t *testing.T) []CashFlow {
			// 100 → 2000 in one day is far beyond 1000% per year.
			return []CashFlow{flow(t, "2021-01-01", -100), flow(t, "2021-01-02", 2000)}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := XIRR(tc.flows(t))
			if err == nil {
				t.Fatalf("XIRR = %g, want an error", got)
			}
		})
	}
}
