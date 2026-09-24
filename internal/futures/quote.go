package futures

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	defaultQuoteTTL     = 3 * time.Second  // 一次报价的可复用时间
	defaultDayTTL       = 10 * time.Minute // 昨收变化很慢，缓存久一点
	defaultQuoteWorkers = 6                // 一次快照里并发取分钟线的上限
	bookPriceTolerance  = 0.02             // 实时口价格与分钟线差异超过 2% 就不信它的盘口
)

// Quote 一个合约的实时快照：价格 / 持仓量 / 成交量 / 买一卖一 / 涨跌。
//
// 取数分工（踩过坑后的定版）：
//   - 价格、持仓量、成交量：**以分钟 K 线的最后一根为准**（收盘价 / p 字段），
//     这条路的字段顺序在仓库里有测试钉着，持仓量是准的；
//   - 买一/卖一：只有新浪实时行情（`hq.sinajs.cn`）有，但那个口的字段位置社区说法不一致，
//     所以先拿它和分钟线价格**交叉校验**，对得上才采用，对不上就只显示价格、不显示盘口。
//
// 换句话说：宁可没有盘口，也不能让持仓量/价格错。
type Quote struct {
	Symbol    string  `json:"symbol"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Hold      float64 `json:"hold"`    // 持仓量（来自分钟线）
	Volume    float64 `json:"volume"`  // 成交量（来自分钟线）
	Bid       float64 `json:"bid"`     // 买一价
	Ask       float64 `json:"ask"`     // 卖一价
	BidVol    float64 `json:"bid_vol"` // 买一量
	AskVol    float64 `json:"ask_vol"` // 卖一量
	Time      string  `json:"time"`    // 行情时间（北京时间）
	PrevClose float64 `json:"prev_close"`
	ChangePct float64 `json:"change_pct"`
	Source    string  `json:"source"` // kline | kline+hq（带盘口）| hq（分钟线取不到时的兜底）
	Stale     bool    `json:"stale"`  // true = 这次没取到，用的是上一次的价
	Error     string  `json:"error,omitempty"`
}

// QuoteService 报价快照服务：带短缓存，浮窗每几秒轮询也不会把上游打爆。
type QuoteService struct {
	Client   *Client
	QuoteTTL time.Duration
	DayTTL   time.Duration
	Workers  int
	Now      func() time.Time

	mu    sync.Mutex
	items map[string]quoteEntry
}

type quoteEntry struct {
	quote  Quote
	at     time.Time // 报价取得时间
	prev   float64   // 昨收
	prevAt time.Time // 昨收取得时间
}

func NewQuoteService(c *Client) *QuoteService {
	if c == nil {
		c = &Client{}
	}
	return &QuoteService{Client: c, items: map[string]quoteEntry{}}
}

func (s *QuoteService) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *QuoteService) quoteTTL() time.Duration {
	if s != nil && s.QuoteTTL > 0 {
		return s.QuoteTTL
	}
	return defaultQuoteTTL
}

func (s *QuoteService) dayTTL() time.Duration {
	if s != nil && s.DayTTL > 0 {
		return s.DayTTL
	}
	return defaultDayTTL
}

func (s *QuoteService) workers() int {
	if s != nil && s.Workers > 0 {
		return s.Workers
	}
	return defaultQuoteWorkers
}

func (s *QuoteService) cached(symbol string) (quoteEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[symbol]
	return e, ok
}

func (s *QuoteService) store(symbol string, e quoteEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]quoteEntry{}
	}
	s.items[symbol] = e
}

// Snapshot 按请求顺序返回（含取失败的项，前端才好按顺序渲染并标红）。
func (s *QuoteService) Snapshot(ctx context.Context, symbols []string) []Quote {
	now := s.now()
	normalized := make([]string, 0, len(symbols))
	for _, raw := range symbols {
		normalized = append(normalized, normalizeSymbol(raw))
	}

	// 1) 命中缓存的直接拿走
	cachedQuotes := map[string]Quote{}
	wanted := []string{}
	for _, sym := range normalized {
		if sym == "" {
			continue
		}
		if e, ok := s.cached(sym); ok && now.Sub(e.at) < s.quoteTTL() {
			cachedQuotes[sym] = e.quote
			continue
		}
		wanted = append(wanted, sym)
	}

	// 2) 批量打实时口（只为盘口；失败不影响主流程）
	ticks := map[string]RealtimeTick{}
	if len(wanted) > 0 && s != nil && s.Client != nil {
		if got, err := s.Client.RealtimeTicks(ctx, dedupe(wanted)); err == nil {
			ticks = got
		}
	}

	// 3) 并发取分钟线（价格/持仓量的权威来源）
	klines := s.fetchKlines(ctx, dedupe(wanted), now)

	out := make([]Quote, 0, len(symbols))
	for _, sym := range normalized {
		if q, ok := cachedQuotes[sym]; ok {
			out = append(out, q)
			continue
		}
		out = append(out, s.assemble(ctx, sym, klines[sym], ticks[sym], now))
	}
	return out
}

func (s *QuoteService) fetchKlines(ctx context.Context, symbols []string, now time.Time) map[string][]Bar {
	out := map[string][]Bar{}
	if len(symbols) == 0 || s == nil || s.Client == nil {
		return out
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, s.workers())
	)
	for _, sym := range symbols {
		if _, ok := VarietyOfSymbol(sym); !ok {
			continue // 未知代码不浪费请求
		}
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			bars, err := s.Client.Minute(ctx, sym, "1")
			if err != nil || len(bars) == 0 {
				return
			}
			mu.Lock()
			out[sym] = bars
			mu.Unlock()
		}(sym)
	}
	wg.Wait()
	return out
}

// assemble 组装一条报价：分钟线为准，实时口只负责补盘口（要过价格交叉校验）。
func (s *QuoteService) assemble(ctx context.Context, symbol string, bars []Bar, tick RealtimeTick, now time.Time) Quote {
	q := Quote{Symbol: symbol}
	if symbol == "" {
		q.Error = "代码为空"
		return q
	}
	v, ok := VarietyOfSymbol(symbol)
	if !ok {
		q.Error = "未知代码 " + symbol
		return q
	}
	q.Name = quoteName(v, symbol)
	if s == nil || s.Client == nil {
		q.Error = "缺少行情客户端"
		return q
	}

	entry, hasEntry := s.cached(symbol)
	hasTick := tick.Symbol != "" && tick.Price > 0

	if len(bars) > 0 {
		last := bars[len(bars)-1]
		q.Price = last.Close
		q.Hold = last.Hold
		q.Volume = last.Volume
		q.Time = last.Time.In(locCST).Format("2006-01-02 15:04")
		q.Source = "kline"
		// 盘口：只有当实时口的「最新价」和分钟线对得上时才敢用它的买一卖一
		if hasTick && priceClose(tick.Price, q.Price) {
			q.Bid, q.Ask, q.BidVol, q.AskVol = tick.Bid, tick.Ask, tick.BidVol, tick.AskVol
			q.Source = "kline+hq"
			if t := tick.TickTime(); t != "" {
				q.Time = t // 实时口时间更细
			}
		}
	} else if hasTick {
		// 分钟线取不到 → 用实时口兜底（这时持仓量可能不如分钟线准，前端看 source 能分辨）
		q.Price = tick.Price
		q.Hold = tick.Hold
		q.Volume = tick.Volume
		q.Time = tick.TickTime()
		q.Bid, q.Ask, q.BidVol, q.AskVol = tick.Bid, tick.Ask, tick.BidVol, tick.AskVol
		q.Source = "hq"
	} else {
		if hasEntry && entry.quote.Price > 0 {
			stale := entry.quote
			stale.Stale = true
			stale.Error = "上游没有返回数据"
			return stale
		}
		q.Error = "上游没有返回数据"
		return q
	}

	prev, prevAt, ok := s.prevClose(ctx, symbol, now, entry, hasEntry)
	if ok {
		q.PrevClose = prev
		if prev > 0 {
			q.ChangePct = (q.Price - prev) / prev * 100
		}
	}
	s.store(symbol, quoteEntry{quote: q, at: now, prev: prev, prevAt: prevAt})
	return q
}

// priceClose 两个价格是否够接近（实时口 vs 分钟线）：差太多说明实时口那行认错了字段。
func priceClose(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff/b <= bookPriceTolerance
}

// prevClose 昨收 = 今天之前最后一根日线的收盘价（当天那根还没走完，不能当昨收）。
func (s *QuoteService) prevClose(ctx context.Context, symbol string, now time.Time, entry quoteEntry, hasEntry bool) (float64, time.Time, bool) {
	if hasEntry && entry.prev > 0 && now.Sub(entry.prevAt) < s.dayTTL() {
		return entry.prev, entry.prevAt, true
	}
	days, err := s.Client.Daily(ctx, symbol)
	if err != nil || len(days) == 0 {
		if hasEntry && entry.prev > 0 {
			return entry.prev, entry.prevAt, true // 用旧的昨收，总比没有强
		}
		return 0, time.Time{}, false
	}
	today := now.In(locCST).Format("2006-01-02")
	for i := len(days) - 1; i >= 0; i-- {
		if days[i].Date.In(locCST).Format("2006-01-02") < today {
			return days[i].Close, now, true
		}
	}
	return 0, time.Time{}, false
}

func normalizeSymbol(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// quoteName 页面展示名：主连叫「焦煤主连」，月份合约叫「焦煤2601」。
func quoteName(v Variety, symbol string) string {
	up := normalizeSymbol(symbol)
	if up == MainSymbol(v) {
		return v.Name + "主连"
	}
	rest := strings.TrimPrefix(up, strings.ToUpper(v.Prefix))
	if rest == "" || rest == "0" {
		return v.Name + "主连"
	}
	return v.Name + rest
}
