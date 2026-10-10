package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxSplitRows caps the split list; the most recent rows are kept.
const maxSplitRows = 100

type getSplitsInput struct {
	Symbol string `json:"symbol" jsonschema:"ticker symbol, e.g. TQQQ; case-insensitive"`
}

// splitRow is one share split on the wire.
type splitRow struct {
	Date        string  `json:"date"`
	Ratio       string  `json:"ratio" jsonschema:"new shares : old shares, e.g. 2:1 for a 2-for-1 split, 1:10 for a 1-for-10 reverse split"`
	Numerator   float64 `json:"numerator"`
	Denominator float64 `json:"denominator"`
	Reverse     bool    `json:"reverse" jsonschema:"true for a reverse split (fewer shares afterwards)"`
}

type getSplitsOutput struct {
	Symbol      string     `json:"symbol"`
	HistoryFrom string     `json:"history_from" jsonschema:"first bar of the history the splits were read from"`
	HistoryTo   string     `json:"history_to"`
	Count       int        `json:"count"`
	Splits      []splitRow `json:"splits"`
	Notes       []string   `json:"notes"`
	Warnings    []string   `json:"warnings,omitempty"`
}

func (d Deps) registerGetSplits(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_splits",
		Title:       "Get splits",
		Description: "Share splits and reverse splits of one symbol over its whole cached price history, oldest first, as ratio new:old (2:1 doubles the share count, 1:10 is a reverse split). Use it to explain a sudden jump in raw share prices or share counts; prices and dividend amounts in every other tool are already restated for splits to today's share count, so no correction is needed (get_dividends also shows the cash actually paid before a split). At most the 100 most recent splits are listed.",
		Annotations: readOnly("Get splits", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getSplitsInput) (*mcp.CallToolResult, getSplitsOutput, error) {
		out, err := d.getSplits(ctx, in)
		return nil, out, err
	})
}

// getSplits lists the splits recorded with the symbol's price history.
func (d Deps) getSplits(ctx context.Context, in getSplitsInput) (getSplitsOutput, error) {
	s, err := d.fetchSeries(ctx, in.Symbol)
	if err != nil {
		return getSplitsOutput{}, err
	}
	first, _ := s.First()
	last, _ := s.Last()
	out := getSplitsOutput{
		Symbol:      s.Meta.Symbol,
		HistoryFrom: formatDate(first.Date),
		HistoryTo:   formatDate(last.Date),
		Count:       len(s.Splits),
		Splits:      []splitRow{},
		Notes:       []string{"prices and dividend amounts in every tool are already split-adjusted: values dated before a split are restated to today's share count"},
		Warnings:    d.staleWarnings(s.Meta.Symbol),
	}
	splits := s.Splits
	if len(splits) > maxSplitRows {
		splits = splits[len(splits)-maxSplitRows:]
		out.Notes = append(out.Notes, fmt.Sprintf("%d splits recorded, only the most recent %d are listed", len(s.Splits), maxSplitRows))
	}
	for _, sp := range splits {
		out.Splits = append(out.Splits, splitRow{
			Date:        formatDate(sp.Date),
			Ratio:       sp.Ratio(),
			Numerator:   sp.Numerator,
			Denominator: sp.Denominator,
			Reverse:     sp.Numerator < sp.Denominator,
		})
	}
	if out.Count == 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("no split recorded between %s and %s", out.HistoryFrom, out.HistoryTo))
	}
	return out, nil
}
