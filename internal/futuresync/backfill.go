package futuresync

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

const (
	backfillPeriod     = "1"
	backfillMetaPaused = "futures_backfill_paused"
	defaultPace        = 600 * time.Millisecond
	defaultVisitPages  = 6
	manualVisitPages   = 400
	historyRetryAfter  = 6 * time.Hour
	failRetryAfter     = 30 * time.Second
	settledRoundWait   = 15 * time.Minute
)

var backfillCST = time.FixedZone("CST", 8*3600)

// minuteRanger 能按结束日期往前翻分钟线的源。
type minuteRanger interface {
	MinuteRange(ctx context.Context, v futures.Variety, period string, end time.Time, limit int) ([]futures.Bar, error)
}

// BackfillRequest 手动补某一个品种。From/To 为零表示不限制那一端。
type BackfillRequest struct {
	Prefix string
	From   time.Time
	To     time.Time
}

// BackfillStatus 补全进度，给页面轮询。
type BackfillStatus struct {
	Paused  bool   `json:"paused"`
	Running bool   `json:"running"`
	Mode    string `json:"mode"` // auto | manual | idle
	Prefix  string `json:"prefix"`
	Name    string `json:"name"`
	Period  string `json:"period"`
	From    string `json:"from"`
	To      string `json:"to"`
	Oldest  string `json:"oldest"`
	Saved   int    `json:"saved"`
	Message string `json:"message"`
	Queued  int    `json:"queued"`
}

// Backfiller 后台慢慢补 1 分钟历史：一次只打一个请求，请求之间留间隔，避免把接口打限流。
type Backfiller struct {
	Store *store.Store
	Cache *BarCache
	Live  futures.BarSource
	Pace  time.Duration

	mu        sync.Mutex
	paused    bool
	resume    chan struct{}
	manual    []BackfillRequest
	kick      chan struct{}
	varieties []futures.Variety
	cursor    int
	status    BackfillStatus
	retryAt   map[string]time.Time
	histDone  map[string]time.Time
	roundDeep bool
}

func NewBackfiller(st *store.Store, cache *BarCache, live futures.BarSource) *Backfiller {
	b := &Backfiller{
		Store:    st,
		Cache:    cache,
		Live:     live,
		kick:     make(chan struct{}, 1),
		retryAt:  map[string]time.Time{},
		histDone: map[string]time.Time{},
		status:   BackfillStatus{Mode: "idle", Period: backfillPeriod, Message: "等待开始"},
	}
	if st != nil {
		if v, ok, err := st.GetMeta(backfillMetaPaused); err == nil && ok && v == "1" {
			b.paused = true
			b.resume = make(chan struct{})
			b.status.Paused = true
			b.status.Message = "已暂停"
		}
	}
	return b
}

// SetVarieties 限定自动补全的品种（测试用）。nil 表示全市场。
func (b *Backfiller) SetVarieties(list []futures.Variety) {
	b.mu.Lock()
	b.varieties = list
	b.cursor = 0
	b.mu.Unlock()
}

func (b *Backfiller) Status() BackfillStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.status
	st.Paused = b.paused
	st.Queued = len(b.manual)
	return st
}

func (b *Backfiller) Pause() error {
	b.mu.Lock()
	if !b.paused {
		b.paused = true
		b.resume = make(chan struct{})
	}
	b.status.Paused = true
	b.status.Message = "已暂停"
	b.mu.Unlock()
	if b.Store != nil {
		return b.Store.SetMeta(backfillMetaPaused, "1")
	}
	return nil
}

func (b *Backfiller) Resume() error {
	b.mu.Lock()
	b.paused = false
	b.status.Paused = false
	if b.status.Message == "已暂停" {
		b.status.Message = "继续补全"
	}
	ch := b.resume
	b.resume = nil
	b.mu.Unlock()
	if ch != nil {
		close(ch)
	}
	b.nudge()
	if b.Store != nil {
		return b.Store.SetMeta(backfillMetaPaused, "0")
	}
	return nil
}

// Enqueue 插队补某个品种。正在跑的自动任务会在当前这一页之后让路。
func (b *Backfiller) Enqueue(req BackfillRequest) error {
	req.Prefix = strings.ToUpper(strings.TrimSpace(req.Prefix))
	if req.Prefix == "" {
		return fmt.Errorf("请指定品种")
	}
	if _, ok := b.varietyOf(req.Prefix); !ok {
		return fmt.Errorf("未知品种 %s", req.Prefix)
	}
	if !req.From.IsZero() && !req.To.IsZero() && req.From.After(req.To) {
		return fmt.Errorf("开始日期不能晚于结束日期")
	}
	b.mu.Lock()
	b.manual = append(b.manual, req)
	b.mu.Unlock()
	b.nudge()
	return nil
}

func (b *Backfiller) nudge() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// Run 一直跑到 ctx 取消。自动轮询各品种往前翻 1 分钟 K 线；手动任务优先。
func (b *Backfiller) Run(ctx context.Context) error {
	if b.Store == nil || b.Live == nil {
		return fmt.Errorf("缺少存储或数据源")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.waitReady(ctx); err != nil {
			return err
		}
		if req, ok := b.popManual(); ok {
			v, _ := b.varietyOf(req.Prefix)
			b.visit(ctx, v, req.From, req.To, true)
			continue
		}
		v, ok := b.nextVariety()
		if !ok {
			if err := b.sleep(ctx, settledRoundWait); err != nil {
				return err
			}
			continue
		}
		deep := b.visit(ctx, v, time.Time{}, time.Time{}, false)
		if deep {
			b.roundDeep = true
		}
		if b.finishedRound() && !b.roundDeep {
			b.roundDeep = false
			if err := b.sleep(ctx, settledRoundWait); err != nil {
				return err
			}
			continue
		}
		if b.finishedRound() {
			b.roundDeep = false
		}
	}
}

func (b *Backfiller) finishedRound() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cursor == 0
}

func (b *Backfiller) waitReady(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.mu.Lock()
		paused := b.paused
		ch := b.resume
		if paused {
			b.status.Message = "已暂停"
		}
		b.mu.Unlock()
		if !paused {
			return nil
		}
		if ch == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (b *Backfiller) popManual() (BackfillRequest, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.manual) == 0 {
		return BackfillRequest{}, false
	}
	req := b.manual[0]
	b.manual = b.manual[1:]
	return req, true
}

func (b *Backfiller) hasManual() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.manual) > 0
}

func (b *Backfiller) nextVariety() (futures.Variety, bool) {
	list := b.varietyList()
	if len(list) == 0 {
		return futures.Variety{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	start := b.cursor
	for i := 0; i < len(list); i++ {
		idx := (start + i) % len(list)
		v := list[idx]
		b.cursor = (idx + 1) % len(list)
		if until, ok := b.retryAt[v.Prefix]; ok && time.Now().Before(until) {
			continue
		}
		return v, true
	}
	return futures.Variety{}, false
}

func (b *Backfiller) varietyList() []futures.Variety {
	b.mu.Lock()
	list := b.varieties
	b.mu.Unlock()
	if list != nil {
		return list
	}
	return futures.ListVarieties()
}

func (b *Backfiller) varietyOf(prefix string) (futures.Variety, bool) {
	for _, v := range b.varietyList() {
		if strings.EqualFold(v.Prefix, prefix) {
			return v, true
		}
	}
	for _, v := range futures.ListVarieties() {
		if strings.EqualFold(v.Prefix, prefix) {
			return v, true
		}
	}
	return futures.Variety{}, false
}

// visit 往前翻若干页。返回值表示这一轮有没有在挖更早的历史（而不是只刷新最新一页）。
func (b *Backfiller) visit(ctx context.Context, v futures.Variety, from, to time.Time, manual bool) bool {
	pages := defaultVisitPages
	if manual {
		pages = manualVisitPages
	} else if b.historySettled(v.Prefix) {
		pages = 1
	}
	symbol := futures.MainSymbol(v)
	_, hadHistory, _ := b.Store.FuturesLastTime(symbol, backfillPeriod)
	end := time.Time{}
	var prevOldest time.Time
	deep := false
	b.setStatus(func(st *BackfillStatus) {
		st.Running = true
		st.Mode = "auto"
		if manual {
			st.Mode = "manual"
		}
		st.Prefix = v.Prefix
		st.Name = v.Name
		st.Period = backfillPeriod
		st.From = formatDay(from)
		st.To = formatDay(to)
		st.Message = fmt.Sprintf("正在补 %s 1分钟", v.Name)
	})
	defer b.setStatus(func(st *BackfillStatus) { st.Running = false })

	for page := 0; page < pages; page++ {
		if err := ctx.Err(); err != nil {
			return deep
		}
		if err := b.waitReady(ctx); err != nil {
			return deep
		}
		if !manual && b.hasManual() {
			return deep
		}
		if err := b.sleep(ctx, b.gap()); err != nil {
			return deep
		}
		bars, err := b.fetch(ctx, v, end)
		if err != nil {
			if isNoMore(err) {
				b.markHistoryDone(v.Prefix)
				b.setStatus(func(st *BackfillStatus) {
					st.Message = fmt.Sprintf("%s 上游没有更早的 1 分钟数据", v.Name)
				})
				return deep
			}
			b.markRetry(v.Prefix)
			b.setStatus(func(st *BackfillStatus) {
				st.Message = fmt.Sprintf("%s 暂缓：%v", v.Name, err)
			})
			return deep
		}
		oldest := bars[0].Time
		save := clipBars(bars, from, to)
		if len(save) > 0 {
			rows := rowsFromBars(symbol, backfillPeriod, save)
			n, uerr := b.Store.UpsertFuturesBars(rows)
			if uerr != nil {
				b.setStatus(func(st *BackfillStatus) { st.Message = uerr.Error() })
				return deep
			}
			touchCache(b.Cache, symbol, backfillPeriod, rows, hadHistory || page > 0, rows)
			hadHistory = true
			b.setStatus(func(st *BackfillStatus) {
				st.Saved += n
				st.Oldest = oldest.In(backfillCST).Format("2006-01-02 15:04")
				st.Message = fmt.Sprintf("%s 1分钟已补到 %s（本页 +%d）", v.Name, st.Oldest, n)
			})
		}
		if page > 0 {
			deep = true
		}
		if !from.IsZero() && !oldest.After(from) {
			b.setStatus(func(st *BackfillStatus) {
				st.Message = fmt.Sprintf("%s 已补到指定起点 %s", v.Name, formatDay(from))
			})
			return deep
		}
		if !prevOldest.IsZero() && !oldest.Before(prevOldest) {
			b.markHistoryDone(v.Prefix)
			b.setStatus(func(st *BackfillStatus) {
				st.Message = fmt.Sprintf("%s 1分钟历史暂时到此（%s）", v.Name, oldest.In(backfillCST).Format("2006-01-02"))
			})
			return deep
		}
		prevOldest = oldest
		end = time.Date(oldest.Year(), oldest.Month(), oldest.Day(), 0, 0, 0, 0, backfillCST).AddDate(0, 0, -1)
	}
	return deep
}

func (b *Backfiller) fetch(ctx context.Context, v futures.Variety, end time.Time) ([]futures.Bar, error) {
	rs, ok := b.Live.(minuteRanger)
	if !ok {
		return nil, fmt.Errorf("数据源不支持按时间翻分钟线")
	}
	return rs.MinuteRange(ctx, v, backfillPeriod, end, 0)
}

func (b *Backfiller) gap() time.Duration {
	if b.Pace > 0 {
		return b.Pace
	}
	return defaultPace
}

func (b *Backfiller) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.kick:
		return nil
	case <-timer.C:
		return nil
	}
}

func (b *Backfiller) historySettled(prefix string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.histDone[prefix]
	return ok && time.Now().Before(until)
}

func (b *Backfiller) markHistoryDone(prefix string) {
	b.mu.Lock()
	b.histDone[prefix] = time.Now().Add(historyRetryAfter)
	b.mu.Unlock()
}

func (b *Backfiller) markRetry(prefix string) {
	b.mu.Lock()
	b.retryAt[prefix] = time.Now().Add(failRetryAfter)
	b.mu.Unlock()
}

func (b *Backfiller) setStatus(fn func(*BackfillStatus)) {
	b.mu.Lock()
	fn(&b.status)
	b.mu.Unlock()
}

func isNoMore(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "空数据") || strings.Contains(msg, "无数据")
}

func clipBars(bars []futures.Bar, from, to time.Time) []futures.Bar {
	out := make([]futures.Bar, 0, len(bars))
	for _, b := range bars {
		if !from.IsZero() && b.Time.Before(from) {
			continue
		}
		if !to.IsZero() && b.Time.After(to) {
			continue
		}
		out = append(out, b)
	}
	return out
}

func formatDay(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(backfillCST).Format("2006-01-02")
}

// PeriodSpan 某个周期在本地库里的起止。
type PeriodSpan struct {
	Period string `json:"period"`
	Bars   int    `json:"bars"`
	First  string `json:"first"`
	Last   string `json:"last"`
}

// VarietySpan 一个品种各周期的补全情况。
type VarietySpan struct {
	Prefix  string       `json:"prefix"`
	Name    string       `json:"name"`
	Symbol  string       `json:"symbol"`
	Periods []PeriodSpan `json:"periods"`
}

// LocalCoverage 全市场品种的本地覆盖。没有数据的品种也会列出，方便看到还没补上的。
func LocalCoverage(st *store.Store) ([]VarietySpan, error) {
	if st == nil {
		return nil, fmt.Errorf("缺少本地存储")
	}
	cov, err := st.FuturesCoverage()
	if err != nil {
		return nil, err
	}
	bySymbol := map[string][]store.FuturesCoverage{}
	for _, c := range cov {
		bySymbol[c.Symbol] = append(bySymbol[c.Symbol], c)
	}
	out := make([]VarietySpan, 0, len(futures.ListVarieties()))
	for _, v := range futures.ListVarieties() {
		symbol := futures.MainSymbol(v)
		item := VarietySpan{Prefix: v.Prefix, Name: v.Name, Symbol: symbol, Periods: []PeriodSpan{}}
		rows := bySymbol[symbol]
		sort.Slice(rows, func(i, j int) bool { return periodOrder(rows[i].Period) < periodOrder(rows[j].Period) })
		for _, row := range rows {
			item.Periods = append(item.Periods, PeriodSpan{
				Period: row.Period,
				Bars:   row.Bars,
				First:  formatBarTime(row.Period, row.First),
				Last:   formatBarTime(row.Period, row.Last),
			})
		}
		out = append(out, item)
	}
	return out, nil
}

func periodOrder(period string) int {
	if period == DailyPeriod {
		return 100000
	}
	n := 0
	for _, ch := range period {
		if ch < '0' || ch > '9' {
			return 10000
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func formatBarTime(period string, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	local := t.In(backfillCST)
	if period == DailyPeriod {
		return local.Format("2006-01-02")
	}
	return local.Format("2006-01-02 15:04")
}
