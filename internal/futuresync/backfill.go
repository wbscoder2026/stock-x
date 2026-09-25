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

// symbolMinuteRanger 能按「具体合约代码」翻分钟线的源；月份合约（JM2701）只能走这一层。
type symbolMinuteRanger interface {
	MinuteRangeSymbol(ctx context.Context, symbol, period string, end time.Time, limit int) ([]futures.Bar, error)
}

// BackfillRequest 补某一个标的。Symbol 是合约代码（JM0 主连 / JM2701 月份）；
// 留空表示补该品种的主连。From/To 为零表示不限制那一端。
type BackfillRequest struct {
	Prefix string
	Symbol string
	From   time.Time
	To     time.Time
}

// BackfillStatus 补全进度，给页面轮询。
//
// Percent 是「当前这一个标的」的进度（按已挖到的时间占目标区间的比例）；
// Done/Total 是「这一批」的进度（批量补全主连+月份时用）。两者叠起来就能画出完整的进度条。
type BackfillStatus struct {
	Paused  bool    `json:"paused"`
	Running bool    `json:"running"`
	Mode    string  `json:"mode"` // auto | manual | idle
	Prefix  string  `json:"prefix"`
	Name    string  `json:"name"`
	Symbol  string  `json:"symbol"` // 正在补的合约代码
	Kind    string  `json:"kind"`   // main（主连）| month（月份）
	Label   string  `json:"label"`  // 主连 / 2701
	Period  string  `json:"period"`
	From    string  `json:"from"`
	To      string  `json:"to"`
	Oldest  string  `json:"oldest"`
	Saved   int     `json:"saved"`
	Message string  `json:"message"`
	Queued  int     `json:"queued"`
	Done    int     `json:"done"`    // 本批已完成的标的数
	Total   int     `json:"total"`   // 本批标的总数（0 = 不是批量任务）
	Percent float64 `json:"percent"` // 当前标的进度 0~100
}

// Backfiller 后台慢慢补 1 分钟历史：一次只打一个请求，请求之间留间隔，避免把接口打限流。
type Backfiller struct {
	Store *store.Store
	Cache *BarCache
	Live  futures.BarSource
	Pace  time.Duration

	mu         sync.Mutex
	paused     bool
	resume     chan struct{}
	manual     []BackfillRequest
	kick       chan struct{}
	varieties  []futures.Variety
	cursor     int
	status     BackfillStatus
	retryAt    map[string]time.Time
	histDone   map[string]time.Time
	roundDeep  bool
	batchDone  int // 本批已完成的标的数
	batchTotal int // 本批标的总数
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
	st.Done = b.batchDone
	st.Total = b.batchTotal
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

// Enqueue 插队补一个标的。正在跑的自动任务会在当前这一页之后让路。
func (b *Backfiller) Enqueue(req BackfillRequest) error {
	return b.EnqueueAll([]BackfillRequest{req})
}

// EnqueueAll 一次排一批标的（比如「主连 + 月份」），进度按整批算。
// 校验是整批一起做的：有一个不合法就全部不排队，免得排进去一半。
func (b *Backfiller) EnqueueAll(reqs []BackfillRequest) error {
	if len(reqs) == 0 {
		return fmt.Errorf("请指定要补的标的")
	}
	norm := make([]BackfillRequest, 0, len(reqs))
	for _, req := range reqs {
		if err := b.normalize(&req); err != nil {
			return err
		}
		if !req.From.IsZero() && !req.To.IsZero() && req.From.After(req.To) {
			return fmt.Errorf("开始日期不能晚于结束日期")
		}
		norm = append(norm, req)
	}
	b.mu.Lock()
	// 上一批已经跑完（done 追上 total）就归零，新的一批从 0 开始数
	if b.batchTotal > 0 && b.batchDone >= b.batchTotal {
		b.batchDone = 0
		b.batchTotal = 0
	}
	b.manual = append(b.manual, norm...)
	b.batchTotal += len(norm)
	b.mu.Unlock()
	b.nudge()
	return nil
}

// normalize 把请求补全成「品种 + 合约代码」：给了合约代码就能反查品种，
// 两个都没有（或对不上）就直接报错，别等到开跑时才发现。
func (b *Backfiller) normalize(req *BackfillRequest) error {
	req.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))
	req.Prefix = strings.ToUpper(strings.TrimSpace(req.Prefix))
	if req.Symbol != "" {
		v, ok := futures.VarietyOfSymbol(req.Symbol)
		if !ok {
			return fmt.Errorf("未知合约 %s", req.Symbol)
		}
		if req.Prefix == "" {
			req.Prefix = v.Prefix
		} else if !strings.EqualFold(req.Prefix, v.Prefix) {
			return fmt.Errorf("合约 %s 不属于品种 %s", req.Symbol, req.Prefix)
		}
		return nil
	}
	if req.Prefix == "" {
		return fmt.Errorf("请指定品种或合约")
	}
	if _, ok := b.varietyOf(req.Prefix); !ok {
		return fmt.Errorf("未知品种 %s", req.Prefix)
	}
	return nil
}

// finishTarget 一个标的补完，推进整批进度；整批跑完且队列也空了才归零
// （留着 done==total 让页面能看到「跑满了」而不是瞬间跳回 0）。
func (b *Backfiller) finishTarget() {
	b.mu.Lock()
	if b.batchTotal > 0 {
		b.batchDone++
		if b.batchDone >= b.batchTotal && len(b.manual) == 0 {
			b.batchDone = 0
			b.batchTotal = 0
		}
	}
	b.mu.Unlock()
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
			symbol := req.Symbol
			if symbol == "" {
				symbol = futures.MainSymbol(v)
			}
			b.visit(ctx, v, symbol, req.From, req.To, true)
			b.finishTarget()
			continue
		}
		v, ok := b.nextVariety()
		if !ok {
			if err := b.sleep(ctx, settledRoundWait); err != nil {
				return err
			}
			continue
		}
		deep := b.visit(ctx, v, futures.MainSymbol(v), time.Time{}, time.Time{}, false)
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
// symbol 是具体合约代码：主连（JM0）或月份合约（JM2701）。
func (b *Backfiller) visit(ctx context.Context, v futures.Variety, symbol string, from, to time.Time, manual bool) bool {
	pages := defaultVisitPages
	if manual {
		pages = manualVisitPages
	} else if b.historySettled(symbol) {
		pages = 1
	}
	last, hadHistory, _ := b.Store.FuturesLastTime(symbol, backfillPeriod)
	// 进度尺：从「本地最新」往「目标起点」挖，挖到哪儿算哪儿。
	// 没指定 from（自动模式）时测不出总量，Percent 就一直是 0，页面按不确定态显示。
	progStart := time.Now().In(backfillCST)
	if hadHistory && last.After(progStart) {
		progStart = last.In(backfillCST)
	}
	progGoal := from
	end := time.Time{}
	var prevOldest time.Time
	deep := false
	label := futures.SymbolLabel(symbol)
	b.setStatus(func(st *BackfillStatus) {
		st.Running = true
		st.Mode = "auto"
		if manual {
			st.Mode = "manual"
		}
		st.Prefix = v.Prefix
		st.Name = v.Name
		st.Symbol = symbol
		st.Kind = futures.SymbolKind(symbol)
		st.Label = label
		st.Period = backfillPeriod
		st.From = formatDay(from)
		st.To = formatDay(to)
		st.Percent = 0
		st.Message = fmt.Sprintf("正在补 %s %s 1分钟", v.Name, label)
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
		bars, err := b.fetch(ctx, v, symbol, end)
		if err != nil {
			if isNoMore(err) {
				b.markHistoryDone(symbol)
				b.setStatus(func(st *BackfillStatus) {
					st.Percent = 100
					st.Message = fmt.Sprintf("%s %s 上游没有更早的 1 分钟数据", v.Name, label)
				})
				return deep
			}
			b.markRetry(symbol)
			b.setStatus(func(st *BackfillStatus) {
				st.Message = fmt.Sprintf("%s %s 暂缓：%v", v.Name, label, err)
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
			pct := progressPercent(progStart, progGoal, oldest)
			b.setStatus(func(st *BackfillStatus) {
				st.Saved += n
				st.Oldest = oldest.In(backfillCST).Format("2006-01-02 15:04")
				if pct > st.Percent {
					st.Percent = pct
				}
				st.Message = fmt.Sprintf("%s %s 1分钟已补到 %s（本页 +%d）", v.Name, label, st.Oldest, n)
			})
		}
		if page > 0 {
			deep = true
		}
		if !from.IsZero() && !oldest.After(from) {
			b.setStatus(func(st *BackfillStatus) {
				st.Percent = 100
				st.Message = fmt.Sprintf("%s %s 已补到指定起点 %s", v.Name, label, formatDay(from))
			})
			return deep
		}
		if !prevOldest.IsZero() && !oldest.Before(prevOldest) {
			b.markHistoryDone(symbol)
			b.setStatus(func(st *BackfillStatus) {
				st.Percent = 100
				st.Message = fmt.Sprintf("%s %s 1分钟历史暂时到此（%s）", v.Name, label, oldest.In(backfillCST).Format("2006-01-02"))
			})
			return deep
		}
		prevOldest = oldest
		end = time.Date(oldest.Year(), oldest.Month(), oldest.Day(), 0, 0, 0, 0, backfillCST).AddDate(0, 0, -1)
	}
	return deep
}

// fetch 按合约代码翻一页。月份合约必须走 symbolMinuteRanger（品种口径只认主连）；
// 源不支持时退回品种口径，主连照样能补。
func (b *Backfiller) fetch(ctx context.Context, v futures.Variety, symbol string, end time.Time) ([]futures.Bar, error) {
	if rs, ok := b.Live.(symbolMinuteRanger); ok {
		return rs.MinuteRangeSymbol(ctx, symbol, backfillPeriod, end, 0)
	}
	rs, ok := b.Live.(minuteRanger)
	if !ok {
		return nil, fmt.Errorf("数据源不支持按时间翻分钟线")
	}
	return rs.MinuteRange(ctx, v, backfillPeriod, end, 0)
}

// progressPercent 当前标的挖到 oldest 时的进度：以「本地最新 → 目标起点」为总量算比例。
// 目标起点未知（自动补全）时测不出来，返回 0。
func progressPercent(start, goal, oldest time.Time) float64 {
	if start.IsZero() || goal.IsZero() || oldest.IsZero() {
		return 0
	}
	total := start.Sub(goal)
	if total <= 0 {
		return 0
	}
	pct := float64(start.Sub(oldest)) / float64(total) * 100
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
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

// VarietySpan 一个标的（主连或某个月份合约）各周期的补全情况。
type VarietySpan struct {
	Prefix  string       `json:"prefix"`
	Name    string       `json:"name"`
	Symbol  string       `json:"symbol"`
	Kind    string       `json:"kind"`  // main（主连）| month（月份）
	Label   string       `json:"label"` // 主连 / 2701
	Periods []PeriodSpan `json:"periods"`
}

// LocalCoverage 全市场标的的本地覆盖：主连和每个月份合约各占一行，都能单独补全。
//
// months 是「品种 → 在交易的全部月份合约」（由 futures.ContractCache 提供），
// 传 nil 就只列本地已经有数据的月份合约；没数据的品种照样列出，方便看到还没补上的。
func LocalCoverage(st *store.Store, months map[string][]string) ([]VarietySpan, error) {
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
	// 本地已经存过的月份合约，按品种归堆
	stored := map[string][]string{}
	for sym := range bySymbol {
		if futures.IsMainSymbol(sym) {
			continue
		}
		v, ok := futures.VarietyOfSymbol(sym)
		if !ok {
			continue
		}
		stored[v.Prefix] = append(stored[v.Prefix], sym)
	}
	out := make([]VarietySpan, 0, len(futures.ListVarieties())*2)
	for _, v := range futures.ListVarieties() {
		out = append(out, spanOf(v, futures.MainSymbol(v), bySymbol))
		list := append([]string(nil), stored[v.Prefix]...)
		// 在交易的月份合约：哪怕本地一根都没有也要全列出来，否则没法从零开始补
		for _, m := range months[strings.ToUpper(v.Prefix)] {
			m = strings.ToUpper(strings.TrimSpace(m))
			if m == "" || futures.SymbolKind(m) != "month" || containsSym(list, m) {
				continue
			}
			if mv, ok := futures.VarietyOfSymbol(m); ok && strings.EqualFold(mv.Prefix, v.Prefix) {
				list = append(list, m)
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(list))) // 远月在前
		for _, sym := range list {
			out = append(out, spanOf(v, sym, bySymbol))
		}
	}
	return out, nil
}

func spanOf(v futures.Variety, symbol string, bySymbol map[string][]store.FuturesCoverage) VarietySpan {
	item := VarietySpan{
		Prefix: v.Prefix, Name: v.Name, Symbol: symbol,
		Kind: futures.SymbolKind(symbol), Label: futures.SymbolLabel(symbol),
		Periods: []PeriodSpan{},
	}
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
	return item
}

func containsSym(list []string, sym string) bool {
	for _, s := range list {
		if strings.EqualFold(s, sym) {
			return true
		}
	}
	return false
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
