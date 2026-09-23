package futuresync

import (
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func TestBudgetIsSeventyPercentOfFreePlusCache(t *testing.T) {
	c := NewBarCache()
	c.SetFreeMemory(func() uint64 { return 1000 })
	if got := c.BudgetBytes(); got != 700 {
		t.Fatalf("空缓存预算应为 700，实际 %d", got)
	}
	c.Fill("JM0", "1", []store.FuturesBar{cacheBar("JM0", "1", 0, 1)})
	// 缓存自己占的字节要加回空闲里，否则会越用越缩
	used := c.UsedBytes()
	want := int64(1000+uint64(used)) * 7 / 10
	if got := c.BudgetBytes(); got != want {
		t.Fatalf("预算=%d，期望 %d（已用 %d）", got, want, used)
	}
}

func TestCacheDropsOldestWhenOverBudget(t *testing.T) {
	c := NewBarCache()
	// 注入的空闲不会随分配下降，裁剪阈值大约是 7 根
	c.SetFreeMemory(func() uint64 { return 384 })
	rows := make([]store.FuturesBar, 25)
	for i := range rows {
		rows[i] = cacheBar("JM0", "1", i, float64(i))
		rows[i].Time = time.Date(2026, 9, 21, 9, 0, 0, 0, cst).Add(time.Duration(i) * time.Minute)
	}
	c.Fill("JM0", "1", rows)
	got, ok := c.Get("JM0", "1")
	if !ok {
		t.Fatal("应还有缓存")
	}
	if len(got) != 7 {
		t.Fatalf("应裁到 7 根，实际 %d", len(got))
	}
	if got[len(got)-1].Close != 24 {
		t.Fatalf("应留下最新的 K 线，末根收盘 %v", got[len(got)-1].Close)
	}
	if !got[0].Time.Before(got[len(got)-1].Time) {
		t.Fatal("裁剪后仍应按时间升序")
	}
}
