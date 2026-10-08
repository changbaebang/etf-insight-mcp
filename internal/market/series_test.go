package market

import (
	"errors"
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return t
}

func sample() *Series {
	return &Series{
		Meta: Meta{Symbol: "TST", Currency: "USD"},
		Bars: []Bar{
			{Date: d("2024-01-02"), Close: 10, AdjClose: 10},
			{Date: d("2024-01-03"), Close: 11, AdjClose: 11},
			{Date: d("2024-01-05"), Close: 12, AdjClose: 12},
		},
	}
}

func TestValidate(t *testing.T) {
	if err := sample().Validate(); err != nil {
		t.Fatalf("valid series rejected: %v", err)
	}
	bad := sample()
	bad.Bars[1].Close = 0
	if err := bad.Validate(); err == nil {
		t.Error("non-positive close accepted")
	}
	dup := sample()
	dup.Bars[2].Date = dup.Bars[1].Date
	if err := dup.Validate(); err == nil {
		t.Error("duplicate date accepted")
	}
	var nilSeries *Series
	if err := nilSeries.Validate(); err == nil {
		t.Error("nil series accepted")
	}
}

func TestIndexOn(t *testing.T) {
	s := sample()
	tests := []struct {
		date   string
		want   int
		wantOK bool
	}{
		{"2024-01-01", 0, false}, // before first bar
		{"2024-01-02", 0, true},  // exact
		{"2024-01-04", 1, true},  // holiday -> previous bar
		{"2024-01-05", 2, true},
		{"2024-02-01", 2, true}, // after last -> last
	}
	for _, tt := range tests {
		got, ok := s.IndexOn(d(tt.date))
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("IndexOn(%s) = %d,%v want %d,%v", tt.date, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestBetween(t *testing.T) {
	s := sample()
	if got := s.Between(d("2024-01-03"), d("2024-01-04")); len(got) != 1 || got[0].Close != 11 {
		t.Errorf("Between(03,04) = %v", got)
	}
	if got := s.Between(time.Time{}, time.Time{}); len(got) != 3 {
		t.Errorf("unbounded Between returned %d bars", len(got))
	}
	if got := s.Between(d("2024-01-06"), time.Time{}); len(got) != 0 {
		t.Errorf("Between after last returned %d bars", len(got))
	}
}

func TestDayAndParse(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	got := Day(time.Date(2024, 1, 2, 23, 30, 0, 0, loc))
	if !got.Equal(d("2024-01-02")) || got.Location() != time.UTC {
		t.Errorf("Day() = %v", got)
	}
	if _, err := ParseDate("2024/01/02"); err == nil {
		t.Error("ParseDate accepted wrong layout")
	}
}

func TestErrNotFoundWraps(t *testing.T) {
	err := errorsJoin(ErrNotFound)
	if !errors.Is(err, ErrNotFound) {
		t.Error("wrapped ErrNotFound not detected")
	}
}

func errorsJoin(err error) error { return errors.Join(errors.New("ctx"), err) }
