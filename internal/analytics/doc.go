// Package analytics computes descriptive statistics, trend readings and
// technical indicators (RSI, MACD, Bollinger bands, ATR) for one price
// series, compares several series over their common history (return
// correlation, beta to a benchmark), scores and ranks series for
// screening, and runs a block-bootstrap Monte Carlo that projects the
// outcome distribution of a recurring-purchase plan.
//
// Nothing in this package predicts prices. Every number is computed from
// past bars under assumptions that are spelled out in the doc comments
// and, for the Monte Carlo, repeated in MCResult.Assumptions. Read the
// output as "the range of outcomes history would have produced under
// these rules", never as a forecast.
//
// The package depends only on the standard library and on package market.
// It does not import package sim; the two share only the cadence strings.
package analytics

import "errors"

// ErrInvalidInput is wrapped by every error caused by a bad argument
// (unknown cadence, weights that do not sum to 1, a missing symbol, ...),
// as opposed to a series that is simply too short.
var ErrInvalidInput = errors.New("analytics: invalid input")

// ErrInsufficientHistory is wrapped when a series has no bar usable for
// the requested date or too few bars for the requested computation.
var ErrInsufficientHistory = errors.New("analytics: insufficient history")
