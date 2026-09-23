package futuresync

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

type pageSource struct {
	*baseSource
	mu   sync.Mutex
	ends []string
	by   map[string][]futures.Bar
}

func (p *pageSource) MinuteRange(_ context.Context, _ futures.Variety, period string, end time.Time, _ int) ([]futures.Bar, error) {
	if period != "1" {
		return nil, errSkip("周期")
	}
	key := "latest"
	if !end.IsZero() {
		key = end.In(cst).Format("2006-01-02")
	}
	p.mu.Lock()
	p.ends = append(p.ends, key)
	p.mu.Unlock()
	if bars, ok := p.by[key]; ok {
		return bars, nil
	}
	return nil, errSkip("东财 空数据")
}

type errSkip string

func (e errSkip) Error() string { return string(e) }

func barAt(day, hour, min int, close float64) futures.Bar {
	return futures.Bar{
		Time: time.Date(2026, 9, day, hour, min, 0, 0, cst),
		Open: close, High: close, Low: close, Close: close, Volume: 10,
	}
}

func TestBackfillPagesBackwardAndStores(t *testing.T) {
	st := openStore(t)
	src := &pageSource{
		baseSource: &baseSource{name: "em"},
		by: map[string][]futures.Bar{
			"latest":     {barAt(21, 10, 0, 10), barAt(21, 10, 1, 11)},
			"2026-09-20": {barAt(18, 10, 0, 8)},
		},
	}
	v := mustVariety(t, "JM")
	b := NewBackfiller(st, NewBarCache(), src)
	b.Pace = time.Millisecond
	b.SetVarieties([]futures.Variety{v})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = b.Run(ctx)
		close(done)
	}()

	symbol := futures.MainSymbol(v)
	deadline := time.Now().Add(3 * time.Second)
	var rows []store.FuturesBar
	for time.Now().Before(deadline) {
		rows, _ = st.FuturesBars(symbol, "1", 0)
		if len(rows) >= 3 {
			cancel()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("补全没有在取消后退出")
	}
	if len(rows) != 3 || rows[0].Close != 8 || rows[2].Close != 11 {
		t.Fatalf("应把更早的一页也落库：%+v", rows)
	}
	src.mu.Lock()
	ends := append([]string(nil), src.ends...)
	src.mu.Unlock()
	if len(ends) < 2 || ends[0] != "latest" || ends[1] != "2026-09-20" {
		t.Fatalf("应先取最新再按前一天翻页：%v", ends)
	}
}

func TestBackfillPauseResume(t *testing.T) {
	st := openStore(t)
	src := &pageSource{
		baseSource: &baseSource{name: "em"},
		by:         map[string][]futures.Bar{"latest": {barAt(21, 10, 0, 10)}},
	}
	b := NewBackfiller(st, NewBarCache(), src)
	b.Pace = time.Millisecond
	b.SetVarieties([]futures.Variety{mustVariety(t, "JM")})
	if err := b.Pause(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx) }()

	time.Sleep(40 * time.Millisecond)
	src.mu.Lock()
	n := len(src.ends)
	src.mu.Unlock()
	if n != 0 {
		t.Fatal("暂停时不该打接口")
	}
	if !b.Status().Paused {
		t.Fatal("状态应为暂停")
	}
	if err := b.Resume(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		src.mu.Lock()
		n = len(src.ends)
		src.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("继续之后应该开始补全")
}

func TestBackfillManualRangeStopsAtFrom(t *testing.T) {
	st := openStore(t)
	src := &pageSource{
		baseSource: &baseSource{name: "em"},
		by: map[string][]futures.Bar{
			"latest":     {barAt(21, 10, 0, 10)},
			"2026-09-20": {barAt(18, 10, 0, 8)},
		},
	}
	b := NewBackfiller(st, NewBarCache(), src)
	b.Pace = time.Millisecond
	b.SetVarieties([]futures.Variety{})
	from := time.Date(2026, 9, 21, 0, 0, 0, 0, cst)
	to := time.Date(2026, 9, 21, 23, 59, 0, 0, cst)
	if err := b.Enqueue(BackfillRequest{Prefix: "JM", From: from, To: to}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx) }()

	symbol := futures.MainSymbol(mustVariety(t, "JM"))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := st.FuturesBars(symbol, "1", 0)
		if len(rows) == 1 && rows[0].Close == 10 {
			src.mu.Lock()
			n := len(src.ends)
			src.mu.Unlock()
			if n >= 2 {
				cancel()
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, _ := st.FuturesBars(symbol, "1", 0)
	src.mu.Lock()
	ends := append([]string(nil), src.ends...)
	src.mu.Unlock()
	t.Fatalf("指定范围不应留下 9 月 18 日：rows=%+v ends=%v", rows, ends)
}

func TestLocalCoverageShowsDates(t *testing.T) {
	st := openStore(t)
	symbol := futures.MainSymbol(mustVariety(t, "JM"))
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		cacheBar(symbol, "1", 0, 10),
		{Symbol: symbol, Period: "1d", Time: time.Date(2026, 9, 18, 15, 0, 0, 0, cst), Close: 99},
	}); err != nil {
		t.Fatal(err)
	}
	list, err := LocalCoverage(st)
	if err != nil {
		t.Fatal(err)
	}
	var jm *VarietySpan
	for i := range list {
		if list[i].Prefix == "JM" {
			jm = &list[i]
		}
	}
	if jm == nil || len(jm.Periods) != 2 {
		t.Fatalf("焦煤应有两个周期：%+v", jm)
	}
	if jm.Periods[0].Period != "1" || jm.Periods[0].First == "" || jm.Periods[0].Bars != 1 {
		t.Fatalf("1分钟覆盖不对：%+v", jm.Periods[0])
	}
	if jm.Periods[1].Period != "1d" || jm.Periods[1].First != "2026-09-18" {
		t.Fatalf("日线日期不对：%+v", jm.Periods[1])
	}
	if len(list) < 10 {
		t.Fatalf("没数据的品种也该列出来：%d", len(list))
	}
}

func TestParsePeriodsAllowsOneMinute(t *testing.T) {
	got, err := ParsePeriods("1,5,1d")
	if err != nil || len(got) != 3 || got[0] != "1" {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
