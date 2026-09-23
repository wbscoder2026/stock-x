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

// minuteSymbolRanger 能按「合约代码」翻分钟线的源（补月份合约必须靠它）。
type minuteSymbolRanger interface {
	MinuteRangeSymbol(ctx context.Context, symbol, period string, end time.Time, limit int) ([]futures.Bar, error)
}

// BackfillRequest 手动补某一个品种。From/To 为零表示不限制那一端。
// Symbol 为空 = 补主连（JM0）；填了就是补具体月份合约（如 JM2601）。
type BackfillRequest struct {
	Prefix string
	Symbol string
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
	Symbol  string `json:"symbol"` // 正在补的代码（主连 JM0 或月份合约 JM2601）
	Period  string `json:"period"`
	From    string `json:"from"`
	To      string `json:"to"`
	Oldest  string `json:"oldest"`
	Saved   int    `json:"saved"`
	Message string `json:"message"`
	Queued  int    `json:"queued"`
	// 进度：页面靠它画进度条。Done/Total = 当前品种已翻/计划翻的页数；
	// RoundIdx/RoundAll = 本轮第几个品种 / 共几个（自动模式一轮扫完所有品种）。
	Done       int   `json:"done"`
	Total      int   `json:"total"`
	RoundIdx   int   `json:"round_idx"`
	RoundAll   int   `json:"round_all"`
	Percent    int   `json:"percent"`     // 0~100，由上面的字段算出来
	StartedAt  int64 `json:"started_at"`  // 当前这轮开始的 unix 秒
	ElapsedSec int   `json:"elapsed_sec"` // 当前这轮已跑秒数
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
	// 百分比 =（本轮已跑完的品种数 + 当前品种的页内进度）÷ 本轮品种总数
	st.Percent = 0
	if st.RoundAll > 0 {
		done := st.RoundIdx - 1
		if done < 0 {
			done = 0
		}
		frac := float64(done)
		if st.Total > 0 && st.Done > 0 {
			f := float64(st.Done) / float64(st.Total)
			if f > 1 {
				f = 1
			}
			frac += f
		}
		st.Percent = int(frac / float64(st.RoundAll) * 100)
		if st.Percent > 100 {
			st.Percent = 100
		}
	}
	st.ElapsedSec = 0
	if st.StartedAt > 0 {
		st.ElapsedSec = int(time.Now().Unix() - st.StartedAt)
	}
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
			b.visit(ctx, v, req.Symbol, req.From, req.To, true)
			continue
		}
		v, ok := b.nextVariety()
		if !ok {
			if err := b.sleep(ctx, settledRoundWait); err != nil {
				return err
			}
			continue
		}
		deep := b.visit(ctx, v, "", time.Time{}, time.Time{}, false)
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
		b.status.RoundIdx = idx + 1
		b.status.RoundAll = len(list)
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
// visit 往前翻若干页。symbol 为空表示补主连，否则补该月份合约。
func (b *Backfiller) visit(ctx context.Context, v futures.Variety, symbol string, from, to time.Time, manual bool) bool {
	pages := defaultVisitPages
	if manual {
		pages = manualVisitPages
	} else if b.historySettled(v.Prefix) {
		pages = 1
	}
	// 补全目标：没指定就补主连（JM0），指定了就补该月份合约（JM2601）
	if strings.TrimSpace(symbol) == "" {
		symbol = futures.MainSymbol(v)
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	// 本轮位置：手动补全不走 nextVariety，这里自己算，页面才看得到「第几个品种」
	list := b.varietyList()
	roundIdx := 0
	for i, item := range list {
		if strings.EqualFold(item.Prefix, v.Prefix) {
			roundIdx = i + 1
			break
		}
	}
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
		st.Symbol = symbol
		st.Period = backfillPeriod
		st.From = formatDay(from)
		st.To = formatDay(to)
		st.Message = fmt.Sprintf("正在补 %s 1分钟", targetLabel(v, symbol))
		st.Total = pages
		st.Done = 0
		st.RoundIdx = roundIdx
		st.RoundAll = len(list)
		st.StartedAt = time.Now().Unix()
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
		b.setStatus(func(st *BackfillStatus) { st.Done = page + 1 }) // 翻完一页就 +1（失败也算，页面看得到在动）
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

func (b *Backfiller) fetch(ctx context.Context, v futures.Variety, symbol string, end time.Time) ([]futures.Bar, error) {
	// 优先按合约代码取（补月份合约只能走这条路）
	if rs, ok := b.Live.(minuteSymbolRanger); ok {
		return rs.MinuteRangeSymbol(ctx, symbol, backfillPeriod, end, 0)
	}
	rs, ok := b.Live.(minuteRanger)
	if !ok {
		return nil, fmt.Errorf("数据源不支持按时间翻分钟线")
	}
	return rs.MinuteRange(ctx, v, backfillPeriod, end, 0)
}

// targetLabel 进度文案里的目标名：主连说「焦煤主连」，合约直接说代码。
func targetLabel(v futures.Variety, symbol string) string {
	if symbol == "" || symbol == futures.MainSymbol(v) {
		return v.Name + "主连"
	}
	return symbol
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

// DayDetail 某一天同步到了多少根 K 线。
type DayDetail struct {
	Day     string `json:"day"` // 2006-01-02（北京时间）
	Bars    int    `json:"bars"`
	Weekday string `json:"weekday"` // 周一…周日（页面直接展示）
}

// MonthGroup 一个月的覆盖汇总 —— 二级分类的「月份」层，下面才是具体日期。
type MonthGroup struct {
	Month   string   `json:"month"` // 2026-09
	Days    int      `json:"days"`
	Bars    int      `json:"bars"`
	Missing []string `json:"missing,omitempty"`
}

// PeriodDetail 一个周期的明细：是否 1 分钟级别 + 已同步的每一天 + 首末之间的工作日缺口。
type PeriodDetail struct {
	Period   string      `json:"period"`
	IsMinute bool        `json:"is_minute"` // period == "1"
	Bars     int         `json:"bars"`
	First    string      `json:"first"`
	Last     string      `json:"last"`
	Days     []DayDetail `json:"days"`
	// Months 按月份汇总：页面做「月份 → 日期」二级分类就靠它。
	Months []MonthGroup `json:"months"`
	// Missing 首末之间「工作日却没有数据」的日期。节假日会误报，页面上按「疑似缺失」措辞。
	Missing []string `json:"missing,omitempty"`
}

// VarietyDetail 一个品种的同步明细（页面点「详情」看的就是它）。
type VarietyDetail struct {
	Prefix       string         `json:"prefix"`
	Name         string         `json:"name"`
	Symbol       string         `json:"symbol"`
	MinuteSynced bool           `json:"minute_synced"` // 有没有 1 分钟级别的数据
	MinuteDays   int            `json:"minute_days"`   // 1 分钟覆盖了多少天
	MinuteFirst  string         `json:"minute_first"`
	MinuteLast   string         `json:"minute_last"`
	Periods      []PeriodDetail `json:"periods"`
}

// localPeriods 明细里要展开的周期顺序（1 分钟在最前，日线在最后）。
var localPeriods = []string{"1", "5", "15", "30", "60", "120", "1d"}

var weekdayCN = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// LocalDetail 一个品种「到底同步了哪些日期」。
// 只有起止日期看不出中间有没有断档 —— 所以这里按天列出，顺便标出工作日缺口。
// LocalDetail 一个品种「到底同步了哪些日期」。symbol 为空看主连，否则看该月份合约。
// 只有起止日期看不出中间有没有断档 —— 所以这里按天列出，并按月份做二级分类。
func LocalDetail(st *store.Store, prefix, symbol string) (VarietyDetail, error) {
	if st == nil {
		return VarietyDetail{}, fmt.Errorf("缺少本地存储")
	}
	var v futures.Variety
	found := false
	for _, item := range futures.ListVarieties() {
		if strings.EqualFold(item.Prefix, prefix) {
			v, found = item, true
			break
		}
	}
	if !found {
		return VarietyDetail{}, fmt.Errorf("未知品种 %q", prefix)
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		symbol = futures.MainSymbol(v)
	}
	// 代码必须属于这个品种，别让 JM 的页面读到 RB 的数据
	owner, ok := futures.VarietyOfSymbol(symbol)
	if !ok || !strings.EqualFold(owner.Prefix, v.Prefix) {
		return VarietyDetail{}, fmt.Errorf("%s 不属于品种 %s", symbol, v.Prefix)
	}
	out := VarietyDetail{Prefix: v.Prefix, Name: v.Name, Symbol: symbol, Periods: []PeriodDetail{}}

	for _, period := range localPeriods {
		days, err := st.FuturesCoverageDays(symbol, period)
		if err != nil {
			return VarietyDetail{}, err
		}
		if len(days) == 0 {
			continue // 这个级别还没同步过，不展示空行
		}
		detail := PeriodDetail{
			Period:   period,
			IsMinute: period == backfillPeriod,
			First:    days[0].Day,
			Last:     days[len(days)-1].Day,
			Days:     make([]DayDetail, 0, len(days)),
		}
		have := map[string]int{}
		for _, d := range days {
			have[d.Day] = d.Bars
			detail.Bars += d.Bars
			wd := ""
			if t, err := time.Parse("2006-01-02", d.Day); err == nil {
				wd = weekdayCN[t.Weekday()]
			}
			detail.Days = append(detail.Days, DayDetail{Day: d.Day, Bars: d.Bars, Weekday: wd})
		}
		detail.Missing = weekdayGaps(detail.First, detail.Last, have)
		detail.Months = monthGroups(detail.Days)
		out.Periods = append(out.Periods, detail)
	}

	for _, pd := range out.Periods {
		if !pd.IsMinute {
			continue
		}
		out.MinuteSynced = true
		out.MinuteDays = len(pd.Days)
		out.MinuteFirst, out.MinuteLast = pd.First, pd.Last
	}
	return out, nil
}

// monthGroups 把「按天明细」汇总成月份（升序），并算出每月的工作日缺口。
func monthGroups(days []DayDetail) []MonthGroup {
	if len(days) == 0 {
		return nil
	}
	out := []MonthGroup{}
	index := map[string]int{}
	for _, d := range days {
		if len(d.Day) < 7 {
			continue
		}
		key := d.Day[:7]
		i, ok := index[key]
		if !ok {
			index[key] = len(out)
			out = append(out, MonthGroup{Month: key})
			i = len(out) - 1
		}
		out[i].Days++
		out[i].Bars += d.Bars
	}
	// 每月内部再看一次工作日缺口
	for i := range out {
		have := map[string]int{}
		first, last := "", ""
		for _, d := range days {
			if len(d.Day) < 7 || d.Day[:7] != out[i].Month {
				continue
			}
			have[d.Day] = d.Bars
			if first == "" {
				first = d.Day
			}
			last = d.Day
		}
		out[i].Missing = weekdayGaps(first, last, have)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Month < out[j].Month })
	return out
}

// weekdayGaps 首末之间「周一至周五却没有数据」的日期（升序）。
func weekdayGaps(first, last string, have map[string]int) []string {
	start, err := time.Parse("2006-01-02", first)
	if err != nil {
		return nil
	}
	end, err := time.Parse("2006-01-02", last)
	if err != nil {
		return nil
	}
	out := []string{}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		wd := d.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			continue
		}
		key := d.Format("2006-01-02")
		if _, ok := have[key]; !ok {
			out = append(out, key)
		}
	}
	return out
}

// PeriodSpan 某个周期在本地库里的起止。
type PeriodSpan struct {
	Period string `json:"period"`
	Bars   int    `json:"bars"`
	First  string `json:"first"`
	Last   string `json:"last"`
	Days   int    `json:"days"` // 覆盖了多少天（比「根数」更能说明同步到什么程度）
}

// ContractSpan 一个月份合约（非主连）的本地覆盖情况。
type ContractSpan struct {
	Symbol  string `json:"symbol"`  // JM2601
	Label   string `json:"label"`   // 2601（页面展示用）
	Periods int    `json:"periods"` // 有几个周期有数据
	Days    int    `json:"days"`    // 覆盖天数（取各周期里最多的那个）
}

// VarietySpan 一个品种各周期的补全情况。
type VarietySpan struct {
	Prefix    string         `json:"prefix"`
	Name      string         `json:"name"`
	Symbol    string         `json:"symbol"`
	Periods   []PeriodSpan   `json:"periods"`
	Contracts []ContractSpan `json:"contracts"` // 该品种已同步的月份合约（不含主连）
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
	// 每个组合覆盖了多少天：一次查完（页面每 2 秒轮询，不能逐品种问）
	dayCounts, _ := st.FuturesCoverageDayCounts()
	out := make([]VarietySpan, 0, len(futures.ListVarieties()))
	for _, v := range futures.ListVarieties() {
		symbol := futures.MainSymbol(v)
		item := VarietySpan{
			Prefix: v.Prefix, Name: v.Name, Symbol: symbol,
			Periods: []PeriodSpan{}, Contracts: []ContractSpan{},
		}
		// 该品种的月份合约（JM2601 之类）：页面要在「主连」之外区分它们
		for other, rows := range bySymbol {
			if strings.EqualFold(other, symbol) {
				continue
			}
			owner, ok := futures.VarietyOfSymbol(other)
			if !ok || !strings.EqualFold(owner.Prefix, v.Prefix) {
				continue
			}
			span := ContractSpan{Symbol: other, Periods: len(rows)}
			span.Label = strings.TrimPrefix(strings.ToUpper(other), strings.ToUpper(v.Prefix))
			for _, r := range rows {
				if d := dayCounts[other+"|"+r.Period]; d > span.Days {
					span.Days = d
				}
			}
			item.Contracts = append(item.Contracts, span)
		}
		sort.Slice(item.Contracts, func(i, j int) bool { return item.Contracts[i].Symbol < item.Contracts[j].Symbol })
		rows := bySymbol[symbol]
		sort.Slice(rows, func(i, j int) bool { return periodOrder(rows[i].Period) < periodOrder(rows[j].Period) })
		for _, row := range rows {
			item.Periods = append(item.Periods, PeriodSpan{
				Period: row.Period,
				Bars:   row.Bars,
				First:  formatBarTime(row.Period, row.First),
				Last:   formatBarTime(row.Period, row.Last),
				Days:   dayCounts[row.Symbol+"|"+row.Period],
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
