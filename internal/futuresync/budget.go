package futuresync

import (
	"time"

	"github.com/wbscoder2026/stock-x/internal/store"
)

// 一根 K 线在内存里的粗算体积（结构体 + 切片摊销）。用来把「空闲内存的 70%」换成能放多少根。
const barEstimate = 128

// memFractionNum / memFractionDen = 70%。
const (
	memFractionNum = 7
	memFractionDen = 10
	// 单次从库里捞进内存的根数上限，避免一次查询把进程打满；不够的由 Maintain 慢慢补。
	maxLoadBars = 30000
)

// SetFreeMemory 注入空闲内存读数（测试用）。传 nil 恢复为读系统。
func (c *BarCache) SetFreeMemory(fn func() uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.free = fn
	c.mu.Unlock()
}

func (c *BarCache) freeBytes() uint64 {
	if c != nil && c.free != nil {
		return c.free()
	}
	return systemFreeMemory()
}

// BudgetBytes 当前允许缓存占用的字节数：系统空闲内存的 70%。
// 计算时把缓存自己已经占用的部分加回去，避免「越缓存越觉得内存不够」来回收缩。
func (c *BarCache) BudgetBytes() int64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	used := c.usedLocked()
	c.mu.RUnlock()
	free := c.freeBytes()
	reclaimable := free + uint64(used)
	return int64(reclaimable) * memFractionNum / memFractionDen
}

// UsedBytes 当前缓存里的 K 线大约占多少字节。
func (c *BarCache) UsedBytes() int64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.usedLocked()
}

func (c *BarCache) usedLocked() int64 {
	n := 0
	for _, rows := range c.rows {
		n += len(rows)
	}
	return int64(n) * barEstimate
}

func barsForBytes(budget int64) int {
	if budget < barEstimate {
		return 1
	}
	n := int(budget / barEstimate)
	if n > maxLoadBars {
		return maxLoadBars
	}
	if n < 1 {
		return 1
	}
	return n
}

// Trim 按当前预算丢掉最老的 K 线，给新数据腾位置。
func (c *BarCache) Trim() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trimLocked()
}

// trimLocked 调用方必须已持有写锁。优先丢掉时间最早的那一段，最近的行情留下来给扫描用。
func (c *BarCache) trimLocked() {
	for {
		target := c.budgetLocked()
		if c.usedLocked() <= target || len(c.rows) == 0 {
			return
		}
		oldestKey := ""
		var oldest time.Time
		found := false
		for key, rows := range c.rows {
			if len(rows) == 0 {
				delete(c.rows, key)
				continue
			}
			if !found || rows[0].Time.Before(oldest) {
				found = true
				oldest = rows[0].Time
				oldestKey = key
			}
		}
		if !found {
			return
		}
		rows := c.rows[oldestKey]
		drop := len(rows) / 10
		if drop < 1 {
			drop = 1
		}
		if drop >= len(rows) {
			delete(c.rows, oldestKey)
			continue
		}
		c.rows[oldestKey] = append([]store.FuturesBar(nil), rows[drop:]...)
	}
}

func (c *BarCache) budgetLocked() int64 {
	free := c.freeBytes()
	reclaimable := free + uint64(c.usedLocked())
	return int64(reclaimable) * memFractionNum / memFractionDen
}

// MemoryView 给页面看的内存占用。
type MemoryView struct {
	UsedBytes   int64   `json:"used_bytes"`
	BudgetBytes int64   `json:"budget_bytes"`
	FreeBytes   int64   `json:"free_bytes"`
	Fraction    float64 `json:"fraction"`
}

func (c *BarCache) MemoryView() MemoryView {
	free := uint64(0)
	if c != nil {
		free = c.freeBytes()
	}
	return MemoryView{
		UsedBytes:   c.UsedBytes(),
		BudgetBytes: c.BudgetBytes(),
		FreeBytes:   int64(free),
		Fraction:    float64(memFractionNum) / float64(memFractionDen),
	}
}

// Maintain 先按预算裁剪；还明显空着时，从本地库再捞一段最近的 K 线进来（不打接口）。
func (c *BarCache) Maintain(st interface {
	FuturesBars(string, string, int) ([]store.FuturesBar, error)
}) {
	if c == nil {
		return
	}
	c.Trim()
	if st == nil {
		return
	}
	if c.UsedBytes() >= c.BudgetBytes()*85/100 {
		return
	}
	c.extendOne(st)
}

func (c *BarCache) extendOne(st interface {
	FuturesBars(string, string, int) ([]store.FuturesBar, error)
}) {
	c.mu.RLock()
	var symbol, period string
	shortest := int(^uint(0) >> 1)
	found := false
	for key, rows := range c.rows {
		if len(rows) < shortest {
			shortest = len(rows)
			symbol, period = splitCacheKey(key)
			found = true
		}
	}
	c.mu.RUnlock()
	if !found || symbol == "" {
		return
	}
	room := c.BudgetBytes() - c.UsedBytes()
	extra := barsForBytes(room)
	if extra < 1 {
		return
	}
	rows, err := st.FuturesBars(symbol, period, shortest+extra)
	if err != nil || len(rows) <= shortest {
		return
	}
	c.replace(symbol, period, rows)
}

func (c *BarCache) replace(symbol, period string, rows []store.FuturesBar) {
	if c == nil || len(rows) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rows == nil {
		c.rows = map[string][]store.FuturesBar{}
	}
	c.rows[cacheKey(symbol, period)] = append([]store.FuturesBar(nil), rows...)
	c.trimLocked()
}

func splitCacheKey(key string) (string, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}
