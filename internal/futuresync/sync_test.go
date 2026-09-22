package futuresync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

var cst = time.FixedZone("CST", 8*3600)

func minuteBar(day int, minute int, close float64) futures.Bar {
	t := time.Date(2026, 9, day, 9, 0, 0, 0, cst).Add(time.Duration(minute) * time.Minute)
	return futures.Bar{Time: t, Open: close - 1, High: close + 1, Low: close - 2, Close: close, Volume: 100, Hold: 1000}
}

func dailyBar(day int, close float64) futures.Daily {
	return futures.Daily{
		Date: time.Date(2026, 9, day, 0, 0, 0, 0, cst),
		Open: close - 1, High: close + 1, Low: close - 2, Close: close, Volume: 1000,
	}
}

// baseSource 基础假源（不含翻页能力）
type baseSource struct {
	name        string
	minute      []futures.Bar
	days        []futures.Daily
	err         error
	minuteCalls int32
	dailyCalls  int32
}

func (b *baseSource) Name() string { return b.name }

func (b *baseSource) Minute(_ context.Context, _ futures.Variety, _ string) ([]futures.Bar, error) {
	atomic.AddInt32(&b.minuteCalls, 1)
	if b.err != nil {
		return nil, b.err
	}
	return b.minute, nil
}

func (b *baseSource) Daily(_ context.Context, _ futures.Variety) ([]futures.Daily, error) {
	atomic.AddInt32(&b.dailyCalls, 1)
	if b.err != nil {
		return nil, b.err
	}
	return b.days, nil
}

// pagedSource 带翻页能力的假源：按 end 返回不同页
type pagedSource struct {
	*baseSource
	pages map[string][]futures.Daily
	ends  []string
}

func (p *pagedSource) DailyRange(_ context.Context, _ futures.Variety, end time.Time, _ int) ([]futures.Daily, error) {
	key := "latest"
	if !end.IsZero() {
		key = end.Format("2006-01-02")
	}
	p.ends = append(p.ends, key)
	if list, ok := p.pages[key]; ok {
		return list, nil
	}
	return nil, fmt.Errorf("没有更早的数据了")
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestParsePeriods(t *testing.T) {
	got, err := ParsePeriods("1d,5,15")
	if err != nil || len(got) != 3 || got[0] != "1d" || got[2] != "15" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if dedup, _ := ParsePeriods("5,5"); len(dedup) != 1 {
		t.Fatalf("应去重：%v", dedup)
	}
	if _, err := ParsePeriods("7"); err == nil {
		t.Fatal("非法周期应报错")
	}
	if _, err := ParsePeriods(" , "); err == nil {
		t.Fatal("空列表应报错")
	}
	if def, _ := ParsePeriods(DefaultPeriods); len(def) != 2 || def[0] != "1d" {
		t.Fatalf("默认周期不对：%v", def)
	}
}

func TestSyncIncrementalOnlyWritesNew(t *testing.T) {
	st := openStore(t)
	src := &baseSource{name: "fake", minute: []futures.Bar{
		minuteBar(21, 0, 100), minuteBar(21, 5, 101), minuteBar(21, 10, 102),
	}}
	ctx := context.Background()

	first, err := Sync(ctx, st, []futures.BarSource{src}, Options{Periods: []string{"5"}, Prefixes: []string{"JM"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Saved != 3 || first.Fetched != 3 || len(first.Failed) != 0 {
		t.Fatalf("首轮 %+v", first)
	}

	// 再跑一轮：网络仍拉一个窗口，但本地已存在 → 不重复写
	second, err := Sync(ctx, st, []futures.BarSource{src}, Options{Periods: []string{"5"}, Prefixes: []string{"JM"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Saved != 0 || second.Fetched != 3 {
		t.Fatalf("第二轮不该重复写：%+v", second)
	}
	stored, _ := st.FuturesBars("JM0", "5", 0)
	if len(stored) != 3 {
		t.Fatalf("本地应有 3 根：%d", len(stored))
	}

	// 上游多了一根新的 → 只写这一根
	src.minute = append(src.minute, minuteBar(21, 15, 103))
	third, _ := Sync(ctx, st, []futures.BarSource{src}, Options{Periods: []string{"5"}, Prefixes: []string{"JM"}})
	if third.Saved != 1 {
		t.Fatalf("增量应只写 1 根：%+v", third)
	}
	if all, _ := st.FuturesBars("JM0", "5", 0); len(all) != 4 {
		t.Fatalf("本地应有 4 根：%d", len(all))
	}
}

func TestSyncDailyAndMinutePeriods(t *testing.T) {
	st := openStore(t)
	src := &baseSource{
		name:   "fake",
		minute: []futures.Bar{minuteBar(21, 0, 100)},
		days:   []futures.Daily{dailyBar(18, 99), dailyBar(19, 100)},
	}
	st2, err := Sync(context.Background(), st, []futures.BarSource{src}, Options{
		Periods: []string{"1d", "5"}, Prefixes: []string{"JM", "RB"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st2.Jobs != 4 || st2.Saved != 6 {
		t.Fatalf("stats=%+v", st2)
	}
	days, _ := st.FuturesBars("JM0", "1d", 0)
	if len(days) != 2 {
		t.Fatalf("日线 %d", len(days))
	}
	if days[0].Open != 98 || days[1].Close != 100 {
		t.Fatalf("日线字段不对：%+v", days)
	}
	mins, _ := st.FuturesBars("RB0", "5", 0)
	if len(mins) != 1 {
		t.Fatalf("分钟线 %d", len(mins))
	}
}

func TestSyncDeepDailyPages(t *testing.T) {
	st := openStore(t)
	base := &baseSource{name: "fake"}
	page1 := []futures.Daily{dailyBar(19, 100), dailyBar(20, 101)}
	page2 := []futures.Daily{dailyBar(12, 90), dailyBar(13, 91)}
	page3 := []futures.Daily{dailyBar(5, 80), dailyBar(6, 81)}
	src := &pagedSource{baseSource: base, pages: map[string][]futures.Daily{
		"latest":     page1,
		"2026-09-18": page2, // 第一页最早 09-19 → end = 09-18
		"2026-09-11": page3, // 第二页最早 09-12 → end = 09-11
	}}

	stats, err := Sync(context.Background(), st, []futures.BarSource{src}, Options{
		Periods: []string{"1d"}, Prefixes: []string{"JM"}, Deep: true, Pages: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Saved != 4 {
		t.Fatalf("两页应存 4 根：%+v", stats)
	}
	if len(src.ends) != 2 || src.ends[0] != "latest" || src.ends[1] != "2026-09-18" {
		t.Fatalf("翻页顺序不对：%v", src.ends)
	}
	all, _ := st.FuturesBars("JM0", "1d", 0)
	if len(all) != 4 {
		t.Fatalf("本地 4 根：%d", len(all))
	}

	// 再跑一轮（Pages=4）：第一页没新东西，继续往下补历史
	src.pages["2026-09-11"] = page3
	stats2, err := Sync(context.Background(), st, []futures.BarSource{src}, Options{
		Periods: []string{"1d"}, Prefixes: []string{"JM"}, Deep: true, Pages: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Saved < 2 {
		t.Fatalf("应补到更早的历史：%+v", stats2)
	}
	if all, _ := st.FuturesBars("JM0", "1d", 0); len(all) != 6 {
		t.Fatalf("本地应攒到 6 根：%d", len(all))
	}
}

func TestSyncDeepStopsAtStartDate(t *testing.T) {
	st := openStore(t)
	src := &pagedSource{baseSource: &baseSource{name: "fake"}, pages: map[string][]futures.Daily{
		"latest":     {dailyBar(19, 100)},
		"2026-09-18": {dailyBar(12, 90)},
	}}
	_, err := Sync(context.Background(), st, []futures.BarSource{src}, Options{
		Periods: []string{"1d"}, Prefixes: []string{"JM"},
		Deep: true, Pages: 5, Start: time.Date(2026, 9, 15, 0, 0, 0, 0, cst),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(src.ends) != 2 {
		t.Fatalf("到 09-15 就该停：%v", src.ends)
	}
}

func TestSyncSkipsFailingSourceAfterBurst(t *testing.T) {
	st := openStore(t)
	broken := &baseSource{name: "broken", err: errors.New("HTTP 456")}
	good := &baseSource{name: "good", minute: []futures.Bar{minuteBar(21, 0, 100)}}

	stats, err := Sync(context.Background(), st, []futures.BarSource{broken, good}, Options{
		Periods: []string{"5"}, Prefixes: []string{"JM", "RB", "CU", "AL", "ZN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Failed) != 0 || stats.Saved != 5 {
		t.Fatalf("备用源应全部成功：%+v", stats)
	}
	if got := atomic.LoadInt32(&broken.minuteCalls); got > sourceFailLimit {
		t.Fatalf("坏源连续失败后本轮应被跳过：calls=%d", got)
	}
}

func TestSyncCollectsPerSymbolFailures(t *testing.T) {
	st := openStore(t)
	src := &baseSource{name: "broken", err: errors.New("boom")}
	stats, err := Sync(context.Background(), st, []futures.BarSource{src}, Options{
		Periods: []string{"5"}, Prefixes: []string{"JM", "RB"},
	})
	if err != nil {
		t.Fatalf("单品种失败不该让整轮报错：%v", err)
	}
	if len(stats.Failed) != 2 {
		t.Fatalf("应记 2 个失败：%+v", stats)
	}
}

func TestSyncRejectsBadInput(t *testing.T) {
	st := openStore(t)
	if _, err := Sync(context.Background(), st, nil, Options{}); err == nil {
		t.Fatal("缺源应报错")
	}
	if _, err := Sync(context.Background(), nil, []futures.BarSource{&baseSource{name: "x"}}, Options{}); err == nil {
		t.Fatal("缺存储应报错")
	}
	if _, err := Sync(context.Background(), st, []futures.BarSource{&baseSource{name: "x"}}, Options{Prefixes: []string{"XX"}}); err == nil {
		t.Fatal("未知品种应报错")
	}
}

func TestStoredSourcePrefersLocal(t *testing.T) {
	st := openStore(t)
	symbol := futures.MainSymbol(mustVariety(t, "JM"))
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{{
		Symbol: symbol, Period: "5", Time: time.Date(2026, 9, 21, 9, 0, 0, 0, cst),
		Open: 10, High: 12, Low: 9, Close: 11, Volume: 5,
	}}); err != nil {
		t.Fatal(err)
	}
	live := &baseSource{name: "live", minute: []futures.Bar{minuteBar(21, 55, 999)}}
	src := NewStoredSource(st, live)

	bars, err := src.Minute(context.Background(), mustVariety(t, "JM"), "5")
	if err != nil || len(bars) != 1 {
		t.Fatalf("bars=%d err=%v", len(bars), err)
	}
	if bars[0].Close != 11 {
		t.Fatalf("应读本地：%+v", bars[0])
	}
	if atomic.LoadInt32(&live.minuteCalls) != 0 {
		t.Fatal("本地有数据就不该走网络")
	}
	if src.Name() != "store+live" {
		t.Fatalf("name=%s", src.Name())
	}
}

func TestStoredSourceFallsBackAndWarms(t *testing.T) {
	st := openStore(t)
	live := &baseSource{
		name:   "live",
		days:   []futures.Daily{dailyBar(18, 99)},
		minute: []futures.Bar{minuteBar(21, 0, 100)},
	}
	src := NewStoredSource(st, live)
	v := mustVariety(t, "RB")

	if _, err := src.Daily(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Minute(context.Background(), v, "5"); err != nil {
		t.Fatal(err)
	}
	// 回写：本地已经有数据了
	if rows, _ := st.FuturesBars(futures.MainSymbol(v), "1d", 0); len(rows) != 1 {
		t.Fatalf("日线没回写：%d", len(rows))
	}
	if rows, _ := st.FuturesBars(futures.MainSymbol(v), "5", 0); len(rows) != 1 {
		t.Fatalf("分钟线没回写：%d", len(rows))
	}
	// 第二次就完全走本地
	before := atomic.LoadInt32(&live.dailyCalls)
	if _, err := src.Daily(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&live.dailyCalls) != before {
		t.Fatal("回写后不该再走网络")
	}
}

func TestStoredSourceDailyRoundTrip(t *testing.T) {
	st := openStore(t)
	live := &baseSource{name: "live", days: []futures.Daily{dailyBar(18, 99), dailyBar(19, 100)}}
	src := NewStoredSource(st, live)
	v := mustVariety(t, "CU")

	days, err := src.Daily(context.Background(), v)
	if err != nil || len(days) != 2 {
		t.Fatalf("days=%d err=%v", len(days), err)
	}
	back, err := src.Daily(context.Background(), v) // 走本地
	if err != nil || len(back) != 2 {
		t.Fatalf("回读 days=%d err=%v", len(back), err)
	}
	if !back[0].Date.Equal(days[0].Date) || back[1].Close != 100 || back[0].Low != 97 {
		t.Fatalf("往返不一致：%+v vs %+v", back[0], days[0])
	}
}

func mustVariety(t *testing.T, prefix string) futures.Variety {
	t.Helper()
	for _, v := range futures.ListVarieties() {
		if v.Prefix == prefix {
			return v
		}
	}
	t.Fatalf("未知品种 %s", prefix)
	return futures.Variety{}
}
