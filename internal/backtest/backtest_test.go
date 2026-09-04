package backtest

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/wbscoder2026/stock-x/internal/store"
	"github.com/wbscoder2026/stock-x/internal/strategy"
)

type alwaysPick struct{}

func (alwaysPick) ID() string                     { return "always" }
func (alwaysPick) Name() string                   { return "always" }
func (alwaysPick) DefaultParams() strategy.Params { return nil }
func (alwaysPick) Run(bars map[string][]store.Bar, asOf string, _ strategy.Params) []strategy.Pick {
	if len(bars["000001"]) == 0 {
		return nil
	}
	return []strategy.Pick{{Symbol: "000001"}}
}

func TestRunHoldReturn(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bars := []store.Bar{
		{Symbol: "000001", Date: "2024-01-02", Close: 10},
		{Symbol: "000001", Date: "2024-01-03", Close: 11},
		{Symbol: "000001", Date: "2024-01-04", Close: 12},
		{Symbol: "000001", Date: "2024-01-05", Close: 9},
	}
	if err := st.UpsertBars(bars); err != nil {
		t.Fatal(err)
	}
	res, err := Run(st, alwaysPick{}, nil, "2024-01-02", "2024-01-04", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trades) != 3 {
		t.Fatalf("trades=%d %+v", len(res.Trades), res.Trades)
	}
	if math.Abs(res.Trades[0].Ret-0.1) > 1e-9 { // 10 -> 11
		t.Fatalf("ret0=%v", res.Trades[0].Ret)
	}
	if res.WinRate <= 0 || res.WinRate >= 1 {
		t.Fatalf("win_rate=%v", res.WinRate)
	}
}
