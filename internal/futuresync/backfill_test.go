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

// symbolPageSource 按「合约代码」翻页的假源：能区分主连和月份合约。
type symbolPageSource struct {
	*baseSource
	mu   sync.Mutex
	syms []string
	by   map[string][]futures.Bar
}

func (p *symbolPageSource) MinuteRangeSymbol(_ context.Context, symbol, period string, end time.Time, _ int) ([]futures.Bar, error) {
	if period != "1" {
		return nil, errSkip("周期")
	}
	p.mu.Lock()
	p.syms = append(p.syms, symbol)
	p.mu.Unlock()
	key := "latest"
	if !end.IsZero() {
		key = end.In(cst).Format("2006-01-02")
	}
	if bars, ok := p.by[symbol+"|"+key]; ok {
		return bars, nil
	}
	return nil, errSkip("空数据")
}

func (p *symbolPageSource) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.syms...)
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
	list, err := LocalCoverage(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	var jm *VarietySpan
	for i := range list {
		if list[i].Symbol == symbol {
			jm = &list[i]
		}
	}
	if jm != nil && jm.Kind != "main" {
		t.Fatalf("主连行的类别应是 main：%+v", jm)
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

func TestBackfillMonthContractUsesItsOwnSymbol(t *testing.T) {
	st := openStore(t)
	src := &symbolPageSource{
		baseSource: &baseSource{name: "em"},
		by: map[string][]futures.Bar{
			"JM2701|latest": {barAt(21, 10, 0, 10)},
			"JM0|latest":    {barAt(21, 10, 0, 99)},
		},
	}
	b := NewBackfiller(st, NewBarCache(), src)
	b.Pace = time.Millisecond
	b.SetVarieties([]futures.Variety{})
	if err := b.Enqueue(BackfillRequest{Symbol: "JM2701"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	var rows []store.FuturesBar
	for time.Now().Before(deadline) {
		rows, _ = st.FuturesBars("JM2701", "1", 0)
		if len(rows) >= 1 {
			cancel()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(rows) != 1 || rows[0].Close != 10 {
		t.Fatalf("月份合约应写到自己的代码下：%+v", rows)
	}
	if main, _ := st.FuturesBars("JM0", "1", 0); len(main) != 0 {
		t.Fatalf("补月份不该顺手写脏主连：%+v", main)
	}
	if got := src.seen(); len(got) == 0 || got[0] != "JM2701" {
		t.Fatalf("应按 JM2701 取数据：%v", got)
	}
}

func TestEnqueueAllRejectsBadSymbolBeforeQueuing(t *testing.T) {
	st := openStore(t)
	b := NewBackfiller(st, NewBarCache(), &symbolPageSource{baseSource: &baseSource{name: "em"}})
	b.SetVarieties([]futures.Variety{})
	// 合约代码和品种对不上（RB0 不是焦煤）→ 整批都不排
	if err := b.EnqueueAll([]BackfillRequest{{Prefix: "JM", Symbol: "RB0"}}); err == nil {
		t.Fatal("代码与品种不符应报错")
	}
	if err := b.EnqueueAll([]BackfillRequest{{Symbol: "JM2701"}, {Symbol: "NOPE99"}}); err == nil {
		t.Fatal("未知合约应报错")
	}
	if n := b.Status().Queued; n != 0 {
		t.Fatalf("校验失败时不该排进去：%d", n)
	}
	if err := b.EnqueueAll([]BackfillRequest{{Symbol: "JM2701"}, {Prefix: "JM"}}); err != nil {
		t.Fatal(err)
	}
	stt := b.Status()
	if stt.Queued != 2 || stt.Total != 2 {
		t.Fatalf("批量任务应排队 2 个：queued=%d total=%d", stt.Queued, stt.Total)
	}
}

func TestBackfillBatchCountsFinishedTargets(t *testing.T) {
	st := openStore(t)
	src := &symbolPageSource{
		baseSource: &baseSource{name: "em"},
		by: map[string][]futures.Bar{
			"JM0|latest":    {barAt(21, 10, 0, 1)},
			"JM2701|latest": {barAt(21, 10, 0, 2)},
		},
	}
	b := NewBackfiller(st, NewBarCache(), src)
	b.Pace = time.Millisecond
	b.SetVarieties([]futures.Variety{})
	if err := b.EnqueueAll([]BackfillRequest{{Prefix: "JM"}, {Symbol: "JM2701"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		main, _ := st.FuturesBars("JM0", "1", 0)
		month, _ := st.FuturesBars("JM2701", "1", 0)
		if len(main) > 0 && len(month) > 0 {
			cancel()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	main, _ := st.FuturesBars("JM0", "1", 0)
	month, _ := st.FuturesBars("JM2701", "1", 0)
	if len(main) == 0 || len(month) == 0 {
		t.Fatalf("主连和月份都该补上：main=%d month=%d", len(main), len(month))
	}
	// 整批跑完且队列空了才归零，归零前页面能看到 done==total
	if stt := b.Status(); stt.Queued != 0 {
		t.Fatalf("队列应已排空：%d", stt.Queued)
	}
}

func TestLocalCoverageListsMainAndMonth(t *testing.T) {
	st := openStore(t)
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		cacheBar("JM0", "1", 0, 10),
		cacheBar("JM2701", "1", 1, 11),
	}); err != nil {
		t.Fatal(err)
	}
	// JM2601/JM2611 是上游在交易的月份合约：本地一根都没有，也要全列出来才能从零开始补
	list, err := LocalCoverage(st, map[string][]string{"JM": {"JM2601", "JM2611"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]VarietySpan{}
	for _, item := range list {
		if item.Prefix == "JM" {
			got[item.Symbol] = item
		}
	}
	main, ok := got["JM0"]
	if !ok || main.Kind != "main" || main.Label != "主连" || len(main.Periods) != 1 {
		t.Fatalf("主连行不对：%+v", main)
	}
	month, ok := got["JM2701"]
	if !ok || month.Kind != "month" || month.Label != "2701" || len(month.Periods) != 1 {
		t.Fatalf("已存月份合约行不对：%+v", month)
	}
	for _, sym := range []string{"JM2601", "JM2611"} {
		fresh, ok := got[sym]
		if !ok || fresh.Kind != "month" || len(fresh.Periods) != 0 {
			t.Fatalf("在交易的月份合约 %s 也要列出来：%+v", sym, fresh)
		}
	}
}

// 一个品种缺了好几个月份（2611/2612…）时，这些月份要能整批排队补全。
func TestEnqueueAllMonthsOfVariety(t *testing.T) {
	st := openStore(t)
	src := &symbolPageSource{
		baseSource: &baseSource{name: "em"},
		by: map[string][]futures.Bar{
			"JM0|latest":    {barAt(21, 10, 0, 1)},
			"JM2611|latest": {barAt(21, 10, 0, 2)},
			"JM2612|latest": {barAt(21, 10, 0, 3)},
			"JM2701|latest": {barAt(21, 10, 0, 4)},
		},
	}
	b := NewBackfiller(st, NewBarCache(), src)
	b.Pace = time.Millisecond
	b.SetVarieties([]futures.Variety{})
	reqs := []BackfillRequest{{Prefix: "JM"}}
	for _, sym := range []string{"JM2611", "JM2612", "JM2701"} {
		reqs = append(reqs, BackfillRequest{Symbol: sym})
	}
	if err := b.EnqueueAll(reqs); err != nil {
		t.Fatal(err)
	}
	if stt := b.Status(); stt.Total != 4 {
		t.Fatalf("主连 + 3 个月份应排 4 个：total=%d", stt.Total)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := 0
		for _, sym := range []string{"JM0", "JM2611", "JM2612", "JM2701"} {
			if rows, _ := st.FuturesBars(sym, "1", 0); len(rows) > 0 {
				got++
			}
		}
		if got == 4 {
			cancel()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, sym := range []string{"JM0", "JM2611", "JM2612", "JM2701"} {
		rows, _ := st.FuturesBars(sym, "1", 0)
		if len(rows) == 0 {
			t.Fatalf("%s 没补上", sym)
		}
	}
}

func TestProgressPercent(t *testing.T) {
	start := time.Date(2026, 9, 21, 0, 0, 0, 0, cst)
	goal := time.Date(2026, 8, 21, 0, 0, 0, 0, cst)
	if got := progressPercent(start, goal, start); got != 0 {
		t.Fatalf("还没往前挖应是 0：%v", got)
	}
	if got := progressPercent(start, goal, goal); got != 100 {
		t.Fatalf("挖到起点应是 100：%v", got)
	}
	mid := time.Date(2026, 9, 5, 12, 0, 0, 0, cst)
	if got := progressPercent(start, goal, mid); got < 49 || got > 51 {
		t.Fatalf("中点应约 50%%：%v", got)
	}
	// 自动补全没有目标起点 → 测不出比例，返回 0（页面按不确定态显示）
	if got := progressPercent(start, time.Time{}, mid); got != 0 {
		t.Fatalf("没有目标起点应是 0：%v", got)
	}
	if got := progressPercent(start, goal, start.AddDate(0, 1, 0)); got != 0 {
		t.Fatalf("超出区间应夹到 0：%v", got)
	}
}

func TestParsePeriodsAllowsOneMinute(t *testing.T) {
	got, err := ParsePeriods("1,5,1d")
	if err != nil || len(got) != 3 || got[0] != "1" {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
