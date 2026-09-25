package futuresync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

func day(h, m int) time.Time { return time.Date(2026, 9, 23, h, m, 0, 0, backfillCST) }

// bar1 造一根 1 分钟线（标注结束时刻）
func bar1(min int, o, h, l, c, v float64) store.FuturesBar {
	return store.FuturesBar{
		Symbol: "JM0", Period: "1", Time: day(9, min),
		Open: o, High: h, Low: l, Close: c, Volume: v,
	}
}

// span1 造一段连续的 1 分钟线（含端点）
func span1(from, to time.Time) []store.FuturesBar {
	out := []store.FuturesBar{}
	for ts := from; !ts.After(to); ts = ts.Add(time.Minute) {
		out = append(out, store.FuturesBar{
			Symbol: "JM0", Period: "1", Time: ts,
			Open: 10, High: 11, Low: 9, Close: 10, Volume: 1,
		})
	}
	return out
}

func stamps(bars []store.FuturesBar) []string {
	out := make([]string, 0, len(bars))
	for _, b := range bars {
		out = append(out, b.Time.In(backfillCST).Format("01-02 15:04"))
	}
	return out
}

func wantStamps(t *testing.T, got []store.FuturesBar, want []string) {
	t.Helper()
	g := stamps(got)
	if len(g) != len(want) {
		t.Fatalf("应是 %v，实际 %v", want, g)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("应是 %v，实际 %v", want, g)
		}
	}
}

func TestResampleFiveMinutesAggregatesOHLCV(t *testing.T) {
	var rows []store.FuturesBar
	for i := 1; i <= 10; i++ {
		rows = append(rows, bar1(i, float64(i), float64(i)+2, float64(i)-1, float64(i), 10))
	}
	got := ResampleBarsAt(rows, 5, day(23, 0)) // 时钟已过当天 → 末尾那根也收
	wantStamps(t, got, []string{"09-23 09:05", "09-23 09:10"})
	first, second := got[0], got[1]
	if first.Open != 1 || first.Close != 5 || first.High != 7 || first.Low != 0 {
		t.Fatalf("第一根的 OHLC 不对：%+v", first)
	}
	if second.Open != 6 || second.Close != 10 || second.Volume != 50 {
		t.Fatalf("第二根的 OHLC/量不对：%+v", second)
	}
	if first.Period != "5" || first.Symbol != "JM0" {
		t.Fatalf("周期/代码没带上：%+v", first)
	}
}

// 真实口径：60 分钟线是 10:00 / 11:15 / 14:15 / 15:00
// ——上午小节休息（10:15-10:30）和午休都不算交易分钟，且午休**不换天、不收半根**。
func TestResampleSixtyMinutesMatchesUpstreamLayout(t *testing.T) {
	rows := span1(day(9, 1), day(10, 15)) // 上午（含 10:15-10:30 小节休息）
	rows = append(rows, span1(day(10, 31), day(11, 30))...)
	rows = append(rows, span1(day(13, 31), day(15, 0))...)

	// 时钟推到次日：昨天那根 45 分钟的 15:00 已经定稿
	got := ResampleBarsAt(rows, 60, time.Date(2026, 9, 24, 10, 0, 0, 0, backfillCST))
	wantStamps(t, got, []string{"09-23 10:00", "09-23 11:15", "09-23 14:15", "09-23 15:00"})
	if got[3].Volume != 45 {
		t.Fatalf("日盘最后一根应含 45 个交易分钟：%+v", got[3])
	}
}

// 夜盘属次日：日盘收盘才换交易日并收掉半根，夜里和次日日盘算同一天、不收半根。
func TestResampleFlushesAtTradingDayBoundary(t *testing.T) {
	d := func(dd, h, m int) time.Time { return time.Date(2026, 9, dd, h, m, 0, 0, backfillCST) }
	rows := span1(d(22, 21, 1), d(22, 23, 0)) // 22 日夜盘（属 23 日这个交易日）
	rows = append(rows, span1(d(23, 9, 1), d(23, 10, 0))...)

	got := ResampleBarsAt(rows, 60, d(23, 10, 30))
	wantStamps(t, got, []string{"09-22 22:00", "09-22 23:00", "09-23 10:00"})
}

func TestTradingDayPutsNightIntoNextDay(t *testing.T) {
	d := func(dd, h, m int) time.Time { return time.Date(2026, 9, dd, h, m, 0, 0, backfillCST) }
	if tradingDay(d(23, 15, 0)) != "2026-09-23" {
		t.Fatalf("日盘归当日：%s", tradingDay(d(23, 15, 0)))
	}
	if tradingDay(d(23, 21, 0)) != "2026-09-24" {
		t.Fatalf("夜盘归次日：%s", tradingDay(d(23, 21, 0)))
	}
	// 过了午夜的夜盘仍属同一个交易日
	if tradingDay(d(24, 0, 30)) != "2026-09-24" || tradingDay(d(23, 23, 0)) != "2026-09-24" {
		t.Fatalf("跨午夜的夜盘应同一交易日：%s / %s", tradingDay(d(24, 0, 30)), tradingDay(d(23, 23, 0)))
	}
}

// 还在当天、没凑满的那根不能落库：否则会写出上游根本不存在的半根 K 线。
func TestResampleDropsIncompleteTrailingBar(t *testing.T) {
	var rows []store.FuturesBar
	for i := 1; i <= 7; i++ { // 只凑了 7 分钟，不到 15
		rows = append(rows, bar1(i, 1, 2, 0, 1, 1))
	}
	if got := ResampleBarsAt(rows, 15, day(9, 30)); len(got) != 0 {
		t.Fatalf("没走完的一根不该落库：%v", stamps(got))
	}
	// 同一个交易日过完了 → 定稿，收出来
	if got := ResampleBarsAt(rows, 15, time.Date(2026, 9, 24, 9, 0, 0, 0, backfillCST)); len(got) != 1 {
		t.Fatalf("当天过完后应收出定稿的那根：%v", stamps(got))
	}
}

// 本地 1 分钟线的开头是半截的（翻页翻到哪算哪），从半截的日子分组会把边界带偏，
// 合成出上游根本不存在的 K 线 —— 这种交易日整段跳过。
func TestResampleSkipsTruncatedHeadDay(t *testing.T) {
	d := func(dd, h, m int) time.Time { return time.Date(2026, 9, dd, h, m, 0, 0, backfillCST) }
	// 22 日 22:31 是半截的（夜盘没从头开始）；22 日夜盘 + 23 日日盘算同一个交易日 09-23
	rows := span1(d(22, 22, 31), d(22, 23, 0))
	rows = append(rows, span1(d(23, 9, 1), d(23, 11, 30))...)
	// 23 日夜盘从 21:01 开盘那根开始 → 交易日 09-24 是完整的（日盘含 10:15-10:30 小节休息）
	rows = append(rows, span1(d(23, 21, 1), d(23, 23, 0))...)
	rows = append(rows, span1(d(24, 9, 1), d(24, 10, 15))...)
	rows = append(rows, span1(d(24, 10, 31), d(24, 11, 30))...)

	got := ResampleBarsAt(rows, 60, d(24, 12, 0))
	for _, b := range got {
		if tradingDay(b.Time) == tradingDay(rows[0].Time) {
			t.Fatalf("半截的交易日(09-23)不该合成：%v", stamps(got))
		}
	}
	wantStamps(t, got, []string{"09-23 22:00", "09-23 23:00", "09-24 10:00", "09-24 11:15"})
}

func TestIsSessionOpen(t *testing.T) {
	d := func(dd, h, m int) time.Time { return time.Date(2026, 9, dd, h, m, 0, 0, backfillCST) }
	open := []time.Time{d(23, 9, 1), d(23, 9, 31), d(23, 21, 1)} // 日盘/无夜盘品种/夜盘
	for _, ts := range open {
		if !isSessionOpen(ts) {
			t.Fatalf("%s 应是开盘那根", ts.Format("15:04"))
		}
	}
	// 半截的开始（翻页翻到这儿）不算开盘
	for _, ts := range []time.Time{d(23, 10, 31), d(23, 13, 31), d(22, 22, 31), d(23, 15, 0)} {
		if isSessionOpen(ts) {
			t.Fatalf("%s 不该算开盘", ts.Format("15:04"))
		}
	}
}

func TestMinutePeriod(t *testing.T) {
	if n, ok := minutePeriod("5"); !ok || n != 5 {
		t.Fatalf("5 → %d %v", n, ok)
	}
	// 基频和日线不能合成
	if _, ok := minutePeriod("1"); ok {
		t.Fatal("1 分钟不该被合成")
	}
	if _, ok := minutePeriod("1d"); ok {
		t.Fatal("日线不该被合成")
	}
	if _, ok := minutePeriod(""); ok {
		t.Fatal("空周期不该被合成")
	}
}

func TestDerivePeriodWritesAndSkipsBaseAndDaily(t *testing.T) {
	st := openStore(t)
	symbol := "JM0"
	var rows []store.FuturesBar
	for i := 1; i <= 20; i++ {
		rows = append(rows, bar1(i, float64(i), float64(i), float64(i), float64(i), 1))
	}
	if _, err := st.UpsertFuturesBars(rows); err != nil {
		t.Fatal(err)
	}
	n, err := DerivePeriod(st, NewBarCache(), symbol, "5")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("20 根 1 分钟应合成 4 根 5 分钟：%d", n)
	}
	got, err := st.FuturesBars(symbol, "5", 0)
	if err != nil || len(got) != 4 {
		t.Fatalf("5 分钟线应落库：%v %v", got, err)
	}
	// 日线/基频调用合成应直接跳过（返回 0，不写脏数据）
	if n, err := DerivePeriod(st, nil, symbol, "1d"); err != nil || n != 0 {
		t.Fatalf("日线不该合成：%d %v", n, err)
	}
	if n, err := DerivePeriod(st, nil, symbol, "1"); err != nil || n != 0 {
		t.Fatalf("基频不该合成：%d %v", n, err)
	}
}

// 自适应：1 分钟没这个周期深时继续问上游，追平之后才切成合成。
func TestShouldDeriveOnlyAfterOneMinuteCatchesUp(t *testing.T) {
	st := openStore(t)
	symbol := "JM0"
	// 5 分钟线历史比 1 分钟深得多（和线上现状一样）
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		{Symbol: symbol, Period: "5", Time: time.Date(2026, 8, 1, 9, 5, 0, 0, backfillCST), Close: 1},
		{Symbol: symbol, Period: "5", Time: time.Date(2026, 9, 23, 9, 5, 0, 0, backfillCST), Close: 2},
		{Symbol: symbol, Period: "1", Time: time.Date(2026, 9, 23, 9, 1, 0, 0, backfillCST), Close: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if shouldDerive(st, symbol, "5") {
		t.Fatal("1 分钟还没挖到 5 分钟前面，不该切成合成（会缩水历史）")
	}
	// 把 1 分钟补到 5 分钟之前
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		{Symbol: symbol, Period: "1", Time: time.Date(2026, 7, 1, 9, 1, 0, 0, backfillCST), Close: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if !shouldDerive(st, symbol, "5") {
		t.Fatal("1 分钟更深了就该切成合成")
	}
	// 本地没有这个周期的历史 → 照旧问上游先把历史铺起来
	if shouldDerive(st, symbol, "15") {
		t.Fatal("这个周期本地还没有时不该合成")
	}
	// 基频/日线永不合成
	if shouldDerive(st, symbol, "1") || shouldDerive(st, symbol, "1d") {
		t.Fatal("基频和日线不该走合成")
	}
}

// 合成不该删掉上游已经拿到的更早历史（upsert 是叠加的）。
func TestDeriveKeepsOlderUpstreamHistory(t *testing.T) {
	st := openStore(t)
	symbol := "JM0"
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		{Symbol: symbol, Period: "5", Time: time.Date(2026, 8, 1, 9, 5, 0, 0, backfillCST), Close: 99},
		{Symbol: symbol, Period: "1", Time: time.Date(2026, 9, 23, 9, 1, 0, 0, backfillCST), Close: 1},
		{Symbol: symbol, Period: "1", Time: time.Date(2026, 9, 23, 9, 2, 0, 0, backfillCST), Close: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := DerivePeriod(st, nil, symbol, "5"); err != nil {
		t.Fatal(err)
	}
	rows, err := st.FuturesBars(symbol, "5", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 1 || rows[0].Close != 99 {
		t.Fatalf("上游已有的更早历史不该被动到：%+v", rows)
	}
}

// 端到端：Sync 打开 Derive 且 1 分钟够深时，这个周期一个接口都不打。
func TestSyncDerivesInsteadOfFetching(t *testing.T) {
	st := openStore(t)
	live := &baseSource{name: "live", minute: []futures.Bar{{Time: day(9, 5), Close: 100}}}
	symbol := futures.MainSymbol(mustVariety(t, "JM"))
	// 1 分钟挖得比 5 分钟深 → 5 分钟改本地合成
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		{Symbol: symbol, Period: "5", Time: day(9, 5), Close: 1},
		{Symbol: symbol, Period: "1", Time: day(9, 1), Close: 1},
		{Symbol: symbol, Period: "1", Time: day(9, 2), Close: 2},
		{Symbol: symbol, Period: "1", Time: day(9, 3), Close: 3},
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := Sync(context.Background(), st, []futures.BarSource{live}, Options{
		Periods:  []string{"5"},
		Prefixes: []string{"JM"},
		Cache:    NewBarCache(),
		Derive:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&live.minuteCalls) != 0 {
		t.Fatalf("该合成时就别打接口：calls=%d", atomic.LoadInt32(&live.minuteCalls))
	}
	if stats.Fetched != 0 {
		t.Fatalf("合成不该算网络拉取：fetched=%d", stats.Fetched)
	}
	if stats.Saved == 0 {
		t.Fatal("应写出合成出来的 K 线")
	}
}

// 关掉 Derive 就回到原来的全网络拉取。
func TestSyncWithoutDeriveStillFetches(t *testing.T) {
	st := openStore(t)
	live := &baseSource{name: "live", minute: []futures.Bar{{Time: day(9, 5), Close: 100}}}
	symbol := futures.MainSymbol(mustVariety(t, "JM"))
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		{Symbol: symbol, Period: "5", Time: day(9, 5), Close: 1},
		{Symbol: symbol, Period: "1", Time: day(9, 1), Close: 1},
		{Symbol: symbol, Period: "1", Time: day(9, 2), Close: 2},
		{Symbol: symbol, Period: "1", Time: day(9, 3), Close: 3},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(context.Background(), st, []futures.BarSource{live}, Options{
		Periods: []string{"5"}, Prefixes: []string{"JM"}, Cache: NewBarCache(),
	}); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&live.minuteCalls) != 1 {
		t.Fatalf("没开合成时应照旧打接口：calls=%d", atomic.LoadInt32(&live.minuteCalls))
	}
}
