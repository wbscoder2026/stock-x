package futuresync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

const (
	// DefaultPeriods 默认同步「日线 + 5 分钟」
	DefaultPeriods = "1d,5"
	DefaultWorkers = 6
	// DefaultPages 日线深挖页数（一页 800 根 ≈ 3.3 年）
	DefaultPages = 4
	// 一个源连续失败多少次就在本轮里跳过它
	sourceFailLimit = 3
)

// errSkipSource 该源不支持这次查询（例如不支持翻页），不算失败。
var errSkipSource = errors.New("源不支持该查询")

// Options 同步选项
type Options struct {
	Periods    []string  // 空 = ["1d","5"]
	Prefixes   []string  // 空 = 全市场
	Workers    int       // 默认 6
	Deep       bool      // 日线往前翻页挖历史（依赖源的 RangeSource 能力）
	Pages      int       // 深挖页数，默认 4
	Start      time.Time // 深挖到该日期就停（零值 = 只看页数）
	MinuteBars int       // 分钟线单次窗口（默认 1000）
	Progress   func(done, total int, msg string)
}

// Stats 同步结果
type Stats struct {
	Symbols int
	Jobs    int
	Fetched int
	Saved   int
	Failed  []string
}

// ParsePeriods 解析 "--periods 1d,5"
func ParsePeriods(raw string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, token := range strings.Split(raw, ",") {
		p := strings.TrimSpace(token)
		if p == "" {
			continue
		}
		if p != DailyPeriod {
			switch p {
			case "5", "15", "30", "60", "120":
			default:
				return nil, fmt.Errorf("非法周期 %s（可选 5/15/30/60/120/1d）", p)
			}
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("空周期列表")
	}
	return out, nil
}

// sourcePool 一轮同步里的源池：某个源连续失败就本轮跳过它，避免逐个品种白等。
type sourcePool struct {
	sources []futures.BarSource
	mu      sync.Mutex
	fails   []int
	skip    []bool
}

func newSourcePool(sources []futures.BarSource) *sourcePool {
	return &sourcePool{
		sources: sources,
		fails:   make([]int, len(sources)),
		skip:    make([]bool, len(sources)),
	}
}

func (p *sourcePool) order() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int, 0, len(p.sources))
	for i := range p.sources {
		if !p.skip[i] {
			out = append(out, i)
		}
	}
	if len(out) == 0 { // 全熔断也要试一个，别整轮啥都不做
		for i := range p.sources {
			out = append(out, i)
		}
	}
	return out
}

func (p *sourcePool) success(i int) {
	p.mu.Lock()
	p.fails[i] = 0
	p.mu.Unlock()
}

func (p *sourcePool) failure(i int) {
	p.mu.Lock()
	p.fails[i]++
	if p.fails[i] >= sourceFailLimit {
		p.skip[i] = true
	}
	p.mu.Unlock()
}

// poolFetch 依次尝试源池里的源，返回第一个成功且有数据的。
func poolFetch[T any](
	pool *sourcePool, v futures.Variety, call func(futures.BarSource) ([]T, error),
) ([]T, error) {
	var lastErr error
	for _, i := range pool.order() {
		src := pool.sources[i]
		if filter, ok := src.(futures.VarietyFilter); ok && !filter.Supports(v) {
			continue
		}
		list, err := call(src)
		if errors.Is(err, errSkipSource) {
			continue
		}
		if err == nil && len(list) == 0 {
			err = fmt.Errorf("空数据")
		}
		if err == nil {
			pool.success(i)
			return list, nil
		}
		pool.failure(i)
		lastErr = fmt.Errorf("%s: %w", src.Name(), err)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的数据源")
	}
	return nil, lastErr
}

func fetchMinuteWindow(
	ctx context.Context, pool *sourcePool, v futures.Variety, period string, minBars int,
) ([]futures.Bar, error) {
	return poolFetch(pool, v, func(src futures.BarSource) ([]futures.Bar, error) {
		bars, err := src.Minute(ctx, v, period)
		if err != nil {
			return nil, err
		}
		if minBars > 0 && len(bars) > minBars { // 只要最新的一段
			bars = bars[len(bars)-minBars:]
		}
		return bars, nil
	})
}

func fetchDaysPaged(
	ctx context.Context, pool *sourcePool, v futures.Variety, end time.Time, limit int,
) ([]futures.Daily, error) {
	return poolFetch(pool, v, func(src futures.BarSource) ([]futures.Daily, error) {
		if rs, ok := src.(futures.RangeSource); ok {
			return rs.DailyRange(ctx, v, end, limit)
		}
		if !end.IsZero() {
			return nil, errSkipSource // 不支持翻页的源只能取最新一页
		}
		return src.Daily(ctx, v)
	})
}

// Sync 把各品种各周期的 K 线增量写入本地库：
// 网络只拉一个窗口（接口限制），但本地 upsert 去重，只落新出现的 K 线；
// 回测/研究之后直接读本地，不再走网络。
func Sync(ctx context.Context, st *store.Store, sources []futures.BarSource, opts Options) (Stats, error) {
	if st == nil {
		return Stats{}, fmt.Errorf("缺少本地存储")
	}
	if len(sources) == 0 {
		return Stats{}, fmt.Errorf("缺少数据源")
	}
	periods := opts.Periods
	if len(periods) == 0 {
		var err error
		periods, err = ParsePeriods(DefaultPeriods)
		if err != nil {
			return Stats{}, err
		}
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	varieties, err := universeOf(opts.Prefixes)
	if err != nil {
		return Stats{}, err
	}

	type job struct {
		v      futures.Variety
		period string
	}
	jobs := make([]job, 0, len(varieties)*len(periods))
	for _, v := range varieties {
		for _, period := range periods {
			jobs = append(jobs, job{v: v, period: period})
		}
	}

	pool := newSourcePool(sources)
	stats := Stats{Symbols: len(varieties), Jobs: len(jobs), Failed: []string{}}
	var mu sync.Mutex
	var done int
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for _, jb := range jobs {
		wg.Add(1)
		go func(jb job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			fetched, saved, err := syncOne(ctx, st, pool, jb.v, jb.period, opts)

			mu.Lock()
			done++
			stats.Fetched += fetched
			stats.Saved += saved
			if err != nil {
				stats.Failed = append(stats.Failed, fmt.Sprintf("%s/%s: %v", jb.v.Prefix, jb.period, err))
			}
			if opts.Progress != nil {
				opts.Progress(done, len(jobs), fmt.Sprintf("%s %s（+%d 根）", jb.v.Prefix, jb.period, saved))
			}
			mu.Unlock()
		}(jb)
	}
	wg.Wait()
	return stats, nil
}

func syncOne(
	ctx context.Context, st *store.Store, pool *sourcePool, v futures.Variety, period string, opts Options,
) (fetched, saved int, err error) {
	symbol := futures.MainSymbol(v)
	last, hasLast, err := st.FuturesLastTime(symbol, period)
	if err != nil {
		return 0, 0, err
	}

	if period == DailyPeriod && opts.Deep {
		return syncDailyDeep(ctx, st, pool, v, symbol, last, hasLast, opts)
	}

	if period == DailyPeriod {
		days, err := fetchDaysPaged(ctx, pool, v, time.Time{}, 0)
		if err != nil {
			return 0, 0, err
		}
		rows := rowsFromDays(symbol, days)
		fetched = len(rows)
		if hasLast {
			rows = rowsAfter(rows, last)
		}
		if len(rows) == 0 {
			return fetched, 0, nil
		}
		n, err := st.UpsertFuturesBars(rows)
		return fetched, n, err
	}

	bars, err := fetchMinuteWindow(ctx, pool, v, period, opts.MinuteBars)
	if err != nil {
		return 0, 0, err
	}
	rows := rowsFromBars(symbol, period, bars)
	fetched = len(rows)
	if hasLast {
		rows = rowsAfter(rows, last)
	}
	if len(rows) == 0 {
		return fetched, 0, nil
	}
	n, err := st.UpsertFuturesBars(rows)
	return fetched, n, err
}

// syncDailyDeep 从最新往前翻页挖日线历史：第一页只补新，后续页补更老的历史。
func syncDailyDeep(
	ctx context.Context, st *store.Store, pool *sourcePool,
	v futures.Variety, symbol string, last time.Time, hasLast bool, opts Options,
) (fetched, saved int, err error) {
	pages := opts.Pages
	if pages <= 0 {
		pages = DefaultPages
	}
	end := time.Time{}
	var prevOldest time.Time
	for page := 0; page < pages; page++ {
		days, ferr := fetchDaysPaged(ctx, pool, v, end, 0)
		if ferr != nil {
			if page == 0 {
				return fetched, saved, ferr
			}
			break // 翻到没数据就收工
		}
		rows := rowsFromDays(symbol, days)
		if len(rows) == 0 {
			break
		}
		fetched += len(rows)
		toSave := rows
		if page == 0 && hasLast {
			toSave = rowsAfter(rows, last)
		}
		if len(toSave) > 0 {
			n, uerr := st.UpsertFuturesBars(toSave)
			if uerr != nil {
				return fetched, saved, uerr
			}
			saved += n
		}

		oldest := rows[0].Time
		if !prevOldest.IsZero() && !oldest.Before(prevOldest) {
			break // 上游没按 end 往前给，避免死循环
		}
		prevOldest = oldest
		if !opts.Start.IsZero() && !oldest.After(opts.Start) {
			break // 到目标起点了
		}
		end = oldest.AddDate(0, 0, -1)
	}
	return fetched, saved, nil
}

func rowsAfter(rows []store.FuturesBar, after time.Time) []store.FuturesBar {
	out := make([]store.FuturesBar, 0, len(rows))
	for _, r := range rows {
		if r.Time.After(after) {
			out = append(out, r)
		}
	}
	return out
}

func universeOf(prefixes []string) ([]futures.Variety, error) {
	if len(prefixes) == 0 {
		return futures.ListVarieties(), nil
	}
	want := map[string]bool{}
	for _, raw := range prefixes {
		key := strings.ToUpper(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		want[key] = true
	}
	if len(want) == 0 {
		return futures.ListVarieties(), nil
	}
	out := []futures.Variety{}
	for _, v := range futures.ListVarieties() {
		if want[strings.ToUpper(v.Prefix)] {
			out = append(out, v)
			delete(want, strings.ToUpper(v.Prefix))
		}
	}
	if len(want) > 0 {
		unknown := make([]string, 0, len(want))
		for k := range want {
			unknown = append(unknown, k)
		}
		return nil, fmt.Errorf("未知品种：%s", strings.Join(unknown, ","))
	}
	return out, nil
}
