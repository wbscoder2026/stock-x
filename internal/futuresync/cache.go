package futuresync

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// BarCache 进程内的期货 K 线。扫描和回测先读这里，避免每次都打 SQLite。
// 序列一旦放进来就按时间合并；不会用一个短窗口盖掉已经在内存里的更早历史。
type BarCache struct {
	mu   sync.RWMutex
	rows map[string][]store.FuturesBar
	// free 测试注入的空闲内存；nil 时读系统当前空闲物理内存。
	free func() uint64
}

func NewBarCache() *BarCache {
	return &BarCache{rows: map[string][]store.FuturesBar{}}
}

func cacheKey(symbol, period string) string {
	return symbol + "\x00" + period
}

// Get 返回已缓存的序列（升序）。调用方只读，不要改切片。
func (c *BarCache) Get(symbol, period string) ([]store.FuturesBar, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	rows, ok := c.rows[cacheKey(symbol, period)]
	if !ok || len(rows) == 0 {
		return nil, false
	}
	return rows, true
}

// Fill 仅在该序列还不在内存里时放入。已有数据时不覆盖。
func (c *BarCache) Fill(symbol, period string, rows []store.FuturesBar) {
	if c == nil || symbol == "" || period == "" || len(rows) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rows == nil {
		c.rows = map[string][]store.FuturesBar{}
	}
	key := cacheKey(symbol, period)
	if _, ok := c.rows[key]; ok {
		return
	}
	c.rows[key] = append([]store.FuturesBar(nil), rows...)
	c.trimLocked()
}

// Merge 把增量并进已有序列（同一时刻后来的覆盖）。序列还不在内存里时什么都不做。
func (c *BarCache) Merge(symbol, period string, extra []store.FuturesBar) {
	if c == nil || symbol == "" || period == "" || len(extra) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := cacheKey(symbol, period)
	cur, ok := c.rows[key]
	if !ok {
		return
	}
	c.rows[key] = mergeBars(cur, extra)
	c.trimLocked()
}

func mergeBars(base, extra []store.FuturesBar) []store.FuturesBar {
	by := make(map[int64]store.FuturesBar, len(base)+len(extra))
	for _, b := range base {
		by[b.Time.Unix()] = b
	}
	for _, b := range extra {
		by[b.Time.Unix()] = b
	}
	out := make([]store.FuturesBar, 0, len(by))
	for _, b := range by {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func tailBars(rows []store.FuturesBar, limit int) []store.FuturesBar {
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	return rows[len(rows)-limit:]
}

// touchCache 同步写库成功后更新内存。
// 库里原本没有这条序列时，这次拉到的窗口就是全部，可以直接放进内存；
// 已经有历史时只合并新增，避免一个短窗口把更早的 K 线盖掉。
func touchCache(cache *BarCache, symbol, period string, fetched []store.FuturesBar, hadHistory bool, saved []store.FuturesBar) {
	if cache == nil {
		return
	}
	if !hadHistory {
		cache.Fill(symbol, period, fetched)
		return
	}
	cache.Merge(symbol, period, saved)
}

// Preload 把本地库里已有的期货 K 线装进内存（每个周期只留扫描用得到的最近一段）。
func Preload(st *store.Store, cache *BarCache) error {
	if st == nil || cache == nil {
		return fmt.Errorf("缺少本地存储或内存缓存")
	}
	cov, err := st.FuturesCoverage()
	if err != nil {
		return err
	}
	if len(cov) == 0 {
		return nil
	}
	share := cache.BudgetBytes() / int64(len(cov))
	for _, item := range cov {
		rows, err := st.FuturesBars(item.Symbol, item.Period, barsForBytes(share))
		if err != nil {
			return err
		}
		cache.Fill(item.Symbol, item.Period, rows)
	}
	return nil
}

// WarmConfig 后台增量同步。Prefixes 为空 = 全市场。
type WarmConfig struct {
	Periods  []string
	Prefixes []string
	Workers  int
	Interval time.Duration
	Progress func(done, total int, msg string)
	OnRound  func(Stats)
}

// Warm 先把本地库装进内存，再按间隔增量拉新 K 线落到 SQLite，并同步进内存。
// ctx 取消后停止，不再打接口。
func Warm(ctx context.Context, st *store.Store, cache *BarCache, sources []futures.BarSource, cfg WarmConfig) error {
	if st == nil || cache == nil {
		return fmt.Errorf("缺少本地存储或内存缓存")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := Preload(st, cache); err != nil {
		return err
	}
	if len(sources) == 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		stats, err := Sync(ctx, st, sources, Options{
			Periods:  cfg.Periods,
			Prefixes: cfg.Prefixes,
			Workers:  cfg.Workers,
			Cache:    cache,
			Progress: cfg.Progress,
		})
		if err != nil {
			return err
		}
		if cfg.OnRound != nil {
			cfg.OnRound(stats)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
