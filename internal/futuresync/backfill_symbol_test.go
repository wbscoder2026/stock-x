package futuresync

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
)

// symbolSource 支持按合约代码翻页的假源（模拟东财的 MinuteRangeSymbol）
type symbolSource struct {
	*baseSource
	mu    sync.Mutex
	calls []string
	by    map[string][]futures.Bar
}

func (s *symbolSource) key(symbol string, end time.Time) string {
	k := "latest"
	if !end.IsZero() {
		k = end.In(cst).Format("2006-01-02")
	}
	return symbol + "|" + k
}

func (s *symbolSource) MinuteRangeSymbol(_ context.Context, symbol, period string, end time.Time, _ int) ([]futures.Bar, error) {
	s.mu.Lock()
	s.calls = append(s.calls, symbol)
	s.mu.Unlock()
	if bars, ok := s.by[s.key(symbol, end)]; ok {
		return bars, nil
	}
	return nil, errSkip("东财 空数据")
}

func TestBackfillContractSymbolStoresUnderContract(t *testing.T) {
	st := openStore(t)
	src := &symbolSource{
		baseSource: &baseSource{name: "em"},
		by: map[string][]futures.Bar{
			"JM2601|latest":     {barAt(21, 10, 0, 10), barAt(21, 10, 1, 11)},
			"JM2601|2026-09-20": {barAt(18, 10, 0, 8)},
		},
	}
	v := mustVariety(t, "JM")
	b := NewBackfiller(st, newTestCache(), src)
	b.Pace = time.Millisecond
	b.SetVarieties([]futures.Variety{v})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b.visit(ctx, v, "JM2601", time.Time{}, time.Time{}, true)

	// 数据要落在合约代码下，而不是主连 JM0
	days, err := st.FuturesCoverageDays("JM2601", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 {
		t.Fatalf("合约 JM2601 应有 2 天数据，实际 %+v", days)
	}
	mainDays, err := st.FuturesCoverageDays("JM0", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(mainDays) != 0 {
		t.Fatalf("不该写进主连 JM0：%+v", mainDays)
	}

	got := b.Status()
	if got.Symbol != "JM2601" {
		t.Fatalf("状态里应带上合约代码：%+v", got)
	}
	for _, c := range src.calls {
		if c != "JM2601" {
			t.Fatalf("翻页也应按合约代码请求：%+v", src.calls)
		}
	}
}

// 老数据源（只支持品种级 MinuteRange）必须照常工作，不能因为加了合约支持就退化
func TestBackfillFallsBackToVarietyLevel(t *testing.T) {
	st := openStore(t)
	src := &pageSource{
		baseSource: &baseSource{name: "em"},
		by:         map[string][]futures.Bar{"latest": {barAt(21, 10, 0, 10)}},
	}
	v := mustVariety(t, "JM")
	b := NewBackfiller(st, newTestCache(), src)
	b.Pace = time.Millisecond
	b.SetVarieties([]futures.Variety{v})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 传空代码 = 主连，走老的品种级接口
	b.visit(ctx, v, "", time.Time{}, time.Time{}, true)

	days, err := st.FuturesCoverageDays("JM0", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("主连应照常有数据：%+v", days)
	}
}
