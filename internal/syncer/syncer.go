// Package syncer 从 baostock 拉取股票基础信息与日 K，写入本地 store。
package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wbscoder2026/stock-x/internal/baostock"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// ProgressFunc 报告进度：done/total 为已处理/全部股票数。
type ProgressFunc func(done, total int, msg string)

// Syncer 负责股票列表刷新、历史回填与增量同步。
type Syncer struct {
	Store     *store.Store
	StartDate string
	Workers   int
	Addr      string

	loginMu   sync.Mutex
	lastLogin time.Time
	workers   atomic.Int32
}

const (
	defaultStart   = "2024-01-01"
	defaultWorkers = 4
	minWorkers     = 1
	maxWorkers     = 8
	adjustForward  = "1" // 后复权
	loginGap       = 400 * time.Millisecond
)

// RefreshSymbols 登录 baostock，拉取证券列表并 Upsert 到 store，返回写入条数。
func (s *Syncer) RefreshSymbols(ctx context.Context) (int, error) {
	if s.Store == nil {
		return 0, errors.New("syncer: Store 为空")
	}
	c, err := baostock.Dial(ctx, s.dialAddr())
	if err != nil {
		return 0, err
	}
	defer c.Close()
	if err := c.Login(ctx); err != nil {
		return 0, err
	}
	basics, err := c.StockBasics(ctx)
	if err != nil {
		return 0, err
	}
	stocks := make([]store.Stock, 0, len(basics))
	for _, b := range basics {
		sym := baostock.SymbolFromBS(b.Code)
		if sym == "" {
			continue
		}
		stocks = append(stocks, store.Stock{
			Symbol: sym,
			Name:   b.Name,
			Market: marketFromBS(b.Code),
			Listed: true,
		})
	}
	if err := s.Store.UpsertStocks(stocks); err != nil {
		return 0, err
	}
	return len(stocks), nil
}

// Backfill 刷新列表后并行回填日 K；已有 lastDate 则从次日续传，单票失败不中断全部。
func (s *Syncer) Backfill(ctx context.Context, progress func(done, total int, msg string)) error {
	if _, err := s.RefreshSymbols(ctx); err != nil {
		return err
	}
	today := todayCN()
	stocks, err := s.listedStocks()
	if err != nil {
		return err
	}
	lastMap, err := s.Store.LastDates()
	if err != nil {
		return err
	}
	jobs := make([]fetchJob, 0, len(stocks))
	skipped := 0
	for _, st := range stocks {
		last, ok := lastMap[st.Symbol]
		if caughtUp(last, ok, today) {
			skipped++
			continue
		}
		start := jobStart(last, ok, s.startDate())
		if start > today {
			skipped++
			continue
		}
		jobs = append(jobs, fetchJob{symbol: st.Symbol, start: start, end: today})
	}
	total := skipped + len(jobs)
	report(progress, skipped, total, fmt.Sprintf("跳过已最新 %d 只", skipped))
	_, err = s.runJobs(ctx, jobs, progress, total, skipped)
	return err
}

// BackfillRange 按指定区间拉取日 K；symbols 空则全市场。已有日期会覆盖写入。
func (s *Syncer) BackfillRange(ctx context.Context, from, to string, symbols []string, progress func(done, total int, msg string)) error {
	if s.Store == nil {
		return errors.New("syncer: Store 为空")
	}
	from, to, err := clipRange(from, to, todayCN(), s.startDate())
	if err != nil {
		return err
	}
	stocks, err := s.rangeStocks(ctx, symbols)
	if err != nil {
		return err
	}
	if len(stocks) == 0 {
		return fmt.Errorf("没有可回填的股票")
	}
	jobs := make([]fetchJob, 0, len(stocks))
	for _, st := range stocks {
		jobs = append(jobs, fetchJob{symbol: st.Symbol, start: from, end: to})
	}
	report(progress, 0, len(jobs), fmt.Sprintf("回填 %s ~ %s，共 %d 只", from, to, len(jobs)))
	_, err = s.runJobs(ctx, jobs, progress, len(jobs), 0)
	return err
}

func (s *Syncer) rangeStocks(ctx context.Context, symbols []string) ([]store.Stock, error) {
	if len(symbols) == 0 {
		if _, err := s.RefreshSymbols(ctx); err != nil {
			return nil, err
		}
		return s.listedStocks()
	}
	out := make([]store.Stock, 0, len(symbols))
	seen := map[string]bool{}
	for _, raw := range symbols {
		sym := strings.TrimSpace(raw)
		if sym == "" || seen[sym] {
			continue
		}
		seen[sym] = true
		st, ok, err := s.Store.GetStock(sym)
		if err != nil {
			return nil, err
		}
		if !ok {
			st = store.Stock{Symbol: sym, Listed: true}
		}
		out = append(out, st)
	}
	return out, nil
}

func clipRange(from, to, today, startDate string) (string, string, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" {
		from = startDate
	}
	if to == "" {
		to = today
	}
	if from > to {
		from, to = to, from
	}
	if to > today {
		to = today
	}
	if from > today {
		return "", "", fmt.Errorf("区间尚未开始")
	}
	return from, to, nil
}

// SyncIncremental 对已有 K 线且 lastDate < 今天的股票补到今天；无本地数据时提示先 Backfill。
func (s *Syncer) SyncIncremental(ctx context.Context, progress func(done, total int, msg string)) (int, error) {
	if s.Store == nil {
		return 0, errors.New("syncer: Store 为空")
	}
	dates, err := s.Store.LastDates()
	if err != nil {
		return 0, err
	}
	if len(dates) == 0 {
		report(progress, 0, 0, "请先执行 Backfill")
		return 0, fmt.Errorf("请先执行 Backfill")
	}
	today := todayCN()
	jobs := make([]fetchJob, 0, len(dates))
	skipped := 0
	for sym, last := range dates {
		if caughtUp(last, last != "", today) {
			skipped++
			continue
		}
		start := jobStart(last, last != "", s.startDate())
		if start > today {
			skipped++
			continue
		}
		jobs = append(jobs, fetchJob{symbol: sym, start: start, end: today})
	}
	total := skipped + len(jobs)
	report(progress, skipped, total, fmt.Sprintf("增量 %d 只待补", len(jobs)))
	return s.runJobs(ctx, jobs, progress, total, skipped)
}

func (s *Syncer) listedStocks() ([]store.Stock, error) {
	all, err := s.Store.ListStocks("", 0)
	if err != nil {
		return nil, err
	}
	var out []store.Stock
	for _, st := range all {
		if st.Listed {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s *Syncer) dialAddr() string {
	if a := strings.TrimSpace(s.Addr); a != "" {
		return a
	}
	return baostock.DefaultAddr
}

func ClampWorkers(n int) int {
	if n < minWorkers {
		return minWorkers
	}
	if n > maxWorkers {
		return maxWorkers
	}
	return n
}

func MinWorkers() int { return minWorkers }
func MaxWorkers() int { return maxWorkers }

func (s *Syncer) SetWorkers(n int) int {
	n = ClampWorkers(n)
	s.workers.Store(int32(n))
	return n
}

func (s *Syncer) WorkerCount() int {
	if n := int(s.workers.Load()); n > 0 {
		return ClampWorkers(n)
	}
	if s.Workers > 0 {
		return ClampWorkers(s.Workers)
	}
	return defaultWorkers
}

func (s *Syncer) workerN() int { return s.WorkerCount() }

func (s *Syncer) startDate() string {
	if d := strings.TrimSpace(s.StartDate); d != "" {
		return d
	}
	return defaultStart
}

func marketFromBS(code string) string {
	s := strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i]
	}
	return ""
}

func kbarsToBars(symbol string, ks []baostock.KBar) []store.Bar {
	out := make([]store.Bar, 0, len(ks))
	for _, k := range ks {
		out = append(out, store.Bar{
			Symbol:   symbol,
			Date:     k.Date,
			Open:     k.Open,
			High:     k.High,
			Low:      k.Low,
			Close:    k.Close,
			Volume:   k.Volume,
			Turnover: k.Amount,
			Turn:     k.Turn,
		})
	}
	return out
}

func caughtUp(last string, ok bool, today string) bool {
	return ok && last >= today
}

// jobStart：有 lastDate 则从次日续传，否则用 StartDate。
func jobStart(last string, ok bool, startDate string) string {
	if !ok || last == "" {
		return startDate
	}
	n, err := addDays(last, 1)
	if err != nil {
		return startDate
	}
	return n
}

func addDays(date string, n int) (string, error) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 0, n).Format("2006-01-02"), nil
}

func todayCN() string {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Now().Format("2006-01-02")
	}
	return time.Now().In(loc).Format("2006-01-02")
}

func report(p func(done, total int, msg string), done, total int, msg string) {
	if p != nil {
		p(done, total, msg)
	}
}
