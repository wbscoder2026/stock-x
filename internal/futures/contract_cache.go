package futures

import (
	"context"
	"strings"
	"sync"
	"time"
)

// ContractCache 月份合约缓存。
//
// 列一次（该品种在交易的全部月份合约）要打一次行情接口，而「本地期货」页是轮询的，
// 不能每 10 秒把 66 个品种全打一遍。所以结果按品种缓存 ttl 这么久；
// 失败的品种记成「短间隔后重试」，一次抖动不会让整批卡住。
//
// 只读方法（Get/ListOf/MonthSnapshot）永不打接口；RefreshAsync 在后台补齐缺失/过期的品种。
type ContractCache struct {
	lister  ContractLister
	ttl     time.Duration
	retry   time.Duration
	workers int
	timeout time.Duration
	pace    time.Duration

	mu     sync.Mutex
	months map[string][]Contract // 品种 → 全部月份合约（按代码升序）
	main   map[string]Contract   // 品种 → 主力月份合约（持仓最大）
	at     map[string]time.Time
	busy   bool
}

func NewContractCache(l ContractLister, ttl time.Duration) *ContractCache {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &ContractCache{
		lister:  l,
		ttl:     ttl,
		retry:   10 * time.Minute,
		workers: 4,
		timeout: 8 * time.Second,
		pace:    150 * time.Millisecond,
		months:  map[string][]Contract{},
		main:    map[string]Contract{},
		at:      map[string]time.Time{},
	}
}

// Get 主力月份合约（只读缓存）。
func (c *ContractCache) Get(prefix string) (Contract, bool) {
	if c == nil {
		return Contract{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	got, ok := c.main[strings.ToUpper(strings.TrimSpace(prefix))]
	return got, ok
}

// ListOf 该品种当前在交易的全部月份合约（只读缓存）。
func (c *ContractCache) ListOf(prefix string) []Contract {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	got := c.months[strings.ToUpper(strings.TrimSpace(prefix))]
	return append([]Contract(nil), got...)
}

// MonthSymbols 该品种全部月份合约的代码（只读缓存）。
func (c *ContractCache) MonthSymbols(prefix string) []string {
	list := c.ListOf(prefix)
	out := make([]string, 0, len(list))
	for _, c := range list {
		if c.Symbol != "" {
			out = append(out, c.Symbol)
		}
	}
	return out
}

// MonthSnapshot 当前缓存快照：品种前缀 → 全部月份合约代码（如 JM → [JM2611 JM2612 JM2701]）。
func (c *ContractCache) MonthSnapshot() map[string][]string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string][]string, len(c.months))
	for prefix, list := range c.months {
		syms := make([]string, 0, len(list))
		for _, c := range list {
			if c.Symbol != "" {
				syms = append(syms, c.Symbol)
			}
		}
		if len(syms) > 0 {
			out[prefix] = syms
		}
	}
	return out
}

// RefreshAsync 后台补齐缺失或过期的品种；已经有刷新在跑就直接返回（不堆积请求）。
func (c *ContractCache) RefreshAsync(list []Variety) {
	if c == nil || c.lister == nil || len(list) == 0 {
		return
	}
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return
	}
	c.busy = true
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			c.busy = false
			c.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		c.Refresh(ctx, list)
	}()
}

// Refresh 同步刷新缺失/过期的品种（RefreshAsync 与测试都走这里）。
func (c *ContractCache) Refresh(ctx context.Context, list []Variety) {
	if c == nil || c.lister == nil {
		return
	}
	pending := c.pending(list)
	if len(pending) == 0 {
		return
	}
	sem := make(chan struct{}, c.workers)
	var wg sync.WaitGroup
	for _, v := range pending {
		wg.Add(1)
		sem <- struct{}{}
		go func(v Variety) {
			defer wg.Done()
			defer func() { <-sem }()
			c.resolveOne(ctx, v)
			if c.pace > 0 {
				time.Sleep(c.pace)
			}
		}(v)
	}
	wg.Wait()
}

func (c *ContractCache) resolveOne(ctx context.Context, v Variety) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	list, err := c.lister.List(ctx, v)
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := strings.ToUpper(v.Prefix)
	if err != nil || len(list) == 0 {
		// 失败：记成「retry 之后再试」，而不是继续当缺失每轮都打
		c.at[prefix] = time.Now().Add(-c.ttl).Add(c.retry)
		return
	}
	c.months[prefix] = list
	// 主力 = 持仓量最大的那个月份
	best := list[0]
	for _, item := range list[1:] {
		if item.Position > best.Position {
			best = item
		}
	}
	if best.Label == "" {
		best.Label = SymbolLabel(best.Symbol)
	}
	if best.Kind == "" {
		best.Kind = "month"
	}
	c.main[prefix] = best
	c.at[prefix] = time.Now()
}

// pending 这一轮该重新解析的品种：没解析过，或缓存已过期。
func (c *ContractCache) pending(list []Variety) []Variety {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	out := make([]Variety, 0, len(list))
	for _, v := range list {
		prefix := strings.ToUpper(v.Prefix)
		if at, ok := c.at[prefix]; ok && now.Sub(at) < c.ttl {
			continue
		}
		out = append(out, v)
	}
	return out
}
