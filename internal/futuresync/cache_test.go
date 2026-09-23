package futuresync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

func cacheBar(symbol, period string, minute int, close float64) store.FuturesBar {
	return store.FuturesBar{
		Symbol: symbol, Period: period,
		Time: time.Date(2026, 9, 21, 9, minute, 0, 0, cst),
		Open: close - 1, High: close + 1, Low: close - 2, Close: close, Volume: 10,
	}
}

func TestBarCacheFillAndMerge(t *testing.T) {
	c := NewBarCache()
	symbol := "JM0"

	if _, ok := c.Get(symbol, "5"); ok {
		t.Fatal("空缓存不该命中")
	}
	// 没有旧序列时，增量不能单独占坑，否则会把库里更早的历史盖掉
	c.Merge(symbol, "5", []store.FuturesBar{cacheBar(symbol, "5", 5, 12)})
	if _, ok := c.Get(symbol, "5"); ok {
		t.Fatal("缺席的序列不该被局部增量创建")
	}

	c.Fill(symbol, "5", []store.FuturesBar{cacheBar(symbol, "5", 0, 10)})
	c.Fill(symbol, "5", []store.FuturesBar{cacheBar(symbol, "5", 0, 1)}) // 已有则不覆盖
	c.Merge(symbol, "5", []store.FuturesBar{
		cacheBar(symbol, "5", 0, 11), // 同一根：后来的覆盖
		cacheBar(symbol, "5", 5, 12),
	})

	got, ok := c.Get(symbol, "5")
	if !ok || len(got) != 2 {
		t.Fatalf("合并后应有 2 根：ok=%v len=%d", ok, len(got))
	}
	if !got[0].Time.Before(got[1].Time) || got[0].Close != 11 || got[1].Close != 12 {
		t.Fatalf("应按时间升序且覆盖同一根：%+v", got)
	}
}

func TestPreloadThenReadSkipsDiskAndNetwork(t *testing.T) {
	st := openStore(t)
	symbol := futures.MainSymbol(mustVariety(t, "JM"))
	row := cacheBar(symbol, "5", 0, 11)
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{row}); err != nil {
		t.Fatal(err)
	}
	cache := NewBarCache()
	if err := Preload(st, cache); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	live := &baseSource{name: "live", minute: []futures.Bar{minuteBar(21, 55, 999)}}
	src := &StoredSource{Cache: cache, Live: live, Warm: true}
	bars, err := src.Minute(context.Background(), mustVariety(t, "JM"), "5")
	if err != nil || len(bars) != 1 || bars[0].Close != 11 {
		t.Fatalf("应读内存里的历史：bars=%v err=%v", bars, err)
	}
	if atomic.LoadInt32(&live.minuteCalls) != 0 {
		t.Fatal("内存里已有历史，不该再打接口")
	}
}

func TestStoredSourceKeepsWarmedSeriesInMemory(t *testing.T) {
	st := openStore(t)
	live := &baseSource{name: "live", minute: []futures.Bar{minuteBar(21, 0, 100)}}
	src := NewStoredSource(st, live)
	v := mustVariety(t, "RB")

	if _, err := src.Minute(context.Background(), v, "5"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	bars, err := src.Minute(context.Background(), v, "5")
	if err != nil || len(bars) != 1 || bars[0].Close != 100 {
		t.Fatalf("关库后仍应读内存：bars=%v err=%v", bars, err)
	}
	if atomic.LoadInt32(&live.minuteCalls) != 1 {
		t.Fatalf("回写后不该再打接口：calls=%d", live.minuteCalls)
	}
}

func TestStoredSourceLocalHitStaysInMemory(t *testing.T) {
	st := openStore(t)
	symbol := futures.MainSymbol(mustVariety(t, "CU"))
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{cacheBar(symbol, "1d", 0, 88)}); err != nil {
		t.Fatal(err)
	}
	live := &baseSource{name: "live", days: []futures.Daily{dailyBar(18, 999)}}
	src := NewStoredSource(st, live)
	v := mustVariety(t, "CU")

	if _, err := src.Daily(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	days, err := src.Daily(context.Background(), v)
	if err != nil || len(days) != 1 || days[0].Close != 88 {
		t.Fatalf("本地日线应留在内存：days=%v err=%v", days, err)
	}
	if atomic.LoadInt32(&live.dailyCalls) != 0 {
		t.Fatal("本地已有日线，不该打接口")
	}
}

func TestSyncMergesNewBarsIntoCache(t *testing.T) {
	st := openStore(t)
	v := mustVariety(t, "JM")
	symbol := futures.MainSymbol(v)
	old := cacheBar(symbol, "5", 0, 10)
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{old}); err != nil {
		t.Fatal(err)
	}
	cache := NewBarCache()
	cache.Fill(symbol, "5", []store.FuturesBar{old})

	live := &baseSource{name: "live", minute: []futures.Bar{
		minuteBar(21, 0, 10),
		minuteBar(21, 5, 12),
	}}
	stats, err := Sync(context.Background(), st, []futures.BarSource{live}, Options{
		Periods: []string{"5"}, Prefixes: []string{"JM"}, Cache: cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Saved != 1 {
		t.Fatalf("只该新增较新的那根：%+v", stats)
	}
	got, ok := cache.Get(symbol, "5")
	if !ok || len(got) != 2 || got[0].Close != 10 || got[1].Close != 12 {
		t.Fatalf("内存里应保留旧的并补上新的：ok=%v %+v", ok, got)
	}
}

func TestWarmSyncsOnceThenStops(t *testing.T) {
	st := openStore(t)
	live := &baseSource{
		name:   "live",
		minute: []futures.Bar{minuteBar(21, 0, 100)},
		days:   []futures.Daily{dailyBar(18, 99)},
	}
	cache := NewBarCache()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := Warm(ctx, st, cache, []futures.BarSource{live}, WarmConfig{
		Periods:  []string{"5"},
		Prefixes: []string{"JM"},
		Interval: time.Hour,
		Progress: func(done, total int, _ string) {
			if done == total {
				cancel()
			}
		},
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&live.minuteCalls) != 1 {
		t.Fatalf("一轮同步只该拉一次分钟线：calls=%d", live.minuteCalls)
	}
	symbol := futures.MainSymbol(mustVariety(t, "JM"))
	rows, err := st.FuturesBars(symbol, "5", 0)
	if err != nil || len(rows) != 1 || rows[0].Close != 100 {
		t.Fatalf("应落到本地库：rows=%v err=%v", rows, err)
	}
	got, ok := cache.Get(symbol, "5")
	if !ok || len(got) != 1 || got[0].Close != 100 {
		t.Fatalf("同步结果应进内存：ok=%v %+v", ok, got)
	}
}

func TestWarmCancelledBeforeSyncSkipsNetwork(t *testing.T) {
	st := openStore(t)
	live := &baseSource{name: "live", minute: []futures.Bar{minuteBar(21, 0, 100)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Warm(ctx, st, NewBarCache(), []futures.BarSource{live}, WarmConfig{Periods: []string{"5"}})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&live.minuteCalls) != 0 {
		t.Fatal("取消后不该打接口")
	}
}
