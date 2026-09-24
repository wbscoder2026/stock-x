package futures

import (
	"context"
	"sync"
	"time"
)

const (
	defaultCatalogTTL    = 10 * time.Minute // 合约清单变化很慢
	defaultCatalogWorker = 6                // 并发打上游的上限（67 个品种别一次全轰出去）
)

// VarietyContracts 一个品种的合约清单（主连 + 月份合约）。总览页面按它渲染树。
type VarietyContracts struct {
	Prefix     string `json:"prefix"`
	Name       string `json:"name"`
	Exchange   string `json:"exchange"`
	MainSymbol string `json:"main_symbol"`
	// Contracts 只是「有哪些合约」；实时价/持仓/成交量另走报价接口。
	Contracts []Contract `json:"contracts"`
	Error     string     `json:"error,omitempty"` // 这个品种取合约失败（页面仍显示主连）
}

// ContractCatalog 全市场合约清单：
// 总览页面要「全部品种和月份」，逐个品种打上游是 67 个请求，所以这里并发取一次并缓存住。
type ContractCatalog struct {
	Client  *Client
	TTL     time.Duration
	Workers int
	Now     func() time.Time

	mu    sync.Mutex
	items []VarietyContracts
	at    time.Time
}

func NewContractCatalog(c *Client) *ContractCatalog {
	if c == nil {
		c = &Client{}
	}
	return &ContractCatalog{Client: c}
}

func (c *ContractCatalog) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *ContractCatalog) ttl() time.Duration {
	if c != nil && c.TTL > 0 {
		return c.TTL
	}
	return defaultCatalogTTL
}

func (c *ContractCatalog) workers() int {
	if c != nil && c.Workers > 0 {
		return c.Workers
	}
	return defaultCatalogWorker
}

// All 返回全部品种的合约（命中缓存就直接返回）。
func (c *ContractCatalog) All(ctx context.Context) []VarietyContracts {
	c.mu.Lock()
	cached, at := c.items, c.at
	c.mu.Unlock()
	if len(cached) > 0 && c.now().Sub(at) < c.ttl() {
		return cached
	}
	return c.Refresh(ctx)
}

// Refresh 忽略缓存重新拉一遍。某个品种失败不会让整页挂掉 —— 退化成「只有主连」。
func (c *ContractCatalog) Refresh(ctx context.Context) []VarietyContracts {
	list := ListVarieties()
	out := make([]VarietyContracts, len(list))
	sem := make(chan struct{}, c.workers())
	var wg sync.WaitGroup

	for i, v := range list {
		out[i] = VarietyContracts{
			Prefix: v.Prefix, Name: v.Name, Exchange: v.Exchange,
			MainSymbol: MainSymbol(v),
			Contracts:  []Contract{mainOf(v)},
		}
		wg.Add(1)
		go func(i int, v Variety) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows, err := c.Client.fetchVariety(ctx, v)
			if err != nil {
				out[i].Error = err.Error()
				return
			}
			if len(rows) == 0 {
				return // 上游空数据：保留主连
			}
			sortContracts(rows)
			out[i].Contracts = rows
		}(i, v)
	}
	wg.Wait()

	c.mu.Lock()
	c.items, c.at = out, c.now()
	c.mu.Unlock()
	return out
}
