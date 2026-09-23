package futuresync

import (
	"context"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

func TestBackfillStatusReportsProgress(t *testing.T) {
	st := openStore(t)
	src := &pageSource{
		baseSource: &baseSource{name: "em"},
		by: map[string][]futures.Bar{
			"latest":     {barAt(21, 10, 0, 10)},
			"2026-09-20": {barAt(18, 10, 0, 8)},
		},
	}
	v := mustVariety(t, "JM")
	b := NewBackfiller(st, newTestCache(), src)
	b.Pace = time.Millisecond
	b.SetVarieties([]futures.Variety{v})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b.visit(ctx, v, "", time.Time{}, time.Time{}, false)

	got := b.Status()
	if got.Total <= 0 {
		t.Fatalf("应报出计划页数：%+v", got)
	}
	if got.Done <= 0 || got.Done > got.Total {
		t.Fatalf("已翻页数应在 1~Total 之间：%+v", got)
	}
	if got.RoundAll != 1 {
		t.Fatalf("本轮品种数应为 1：%+v", got)
	}
	if got.Percent < 0 || got.Percent > 100 {
		t.Fatalf("百分比越界：%+v", got)
	}
	if got.Saved <= 0 {
		t.Fatalf("应记录存下的根数：%+v", got)
	}
}

func TestLocalCoverageReportsSyncedDays(t *testing.T) {
	st := openStore(t)
	cst := time.FixedZone("CST", 8*3600)
	mk := func(period string, d, h int) store.FuturesBar {
		return store.FuturesBar{
			Symbol: "PS0", Period: period,
			Time: time.Date(2026, 9, d, h, 0, 0, 0, cst).UTC(),
			Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Hold: 100,
		}
	}
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		mk("1", 21, 9), mk("1", 21, 10), mk("1", 22, 9), mk("1d", 22, 0),
	}); err != nil {
		t.Fatal(err)
	}

	items, err := LocalCoverage(st)
	if err != nil {
		t.Fatal(err)
	}
	var ps *VarietySpan
	for i := range items {
		if items[i].Prefix == "PS" {
			ps = &items[i]
		}
	}
	if ps == nil {
		t.Fatal("没找到 PS")
	}
	for _, p := range ps.Periods {
		switch p.Period {
		case "1":
			if p.Days != 2 {
				t.Fatalf("1 分钟应覆盖 2 天（同一天多根只算 1 天）：%+v", p)
			}
		case "1d":
			if p.Days != 1 {
				t.Fatalf("日线应覆盖 1 天：%+v", p)
			}
		}
	}
}
