package futures

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 参数扫描：把「突破识别 + 止盈止损」的参数做成候选轴，跑笛卡尔积，
// 找胜率 / 平均收益 / 期望 R / 盈利因子最好的组合。
//
// 三个刻意的设计：
//  1. **数据按级别只取一次**：同一级别的所有组合共用一份 K 线（否则 N 个组合 = N 次上游请求）；
//     级别不同必须各自取数（K 线本身不一样），取数按顺序做，避免把上游打限流。
//  2. **样本不足的组合标 reliable=false**：3 笔 100% 胜率的组合排不到可靠组合前面，避免选出过拟合参数。
//  3. **跳过的级别要说清楚**：某个级别取不到数据（上游没这个级别的历史）不该拖垮整次扫描，
//     但必须回带 skipped 让用户知道"这次没比这个级别"。

// 扫描目标
const (
	SweepObjectiveWinRate      = "win_rate"
	SweepObjectiveAvgReturn    = "avg_return"
	SweepObjectiveAvgR         = "avg_r"
	SweepObjectiveProfitFactor = "profit_factor"
)

const (
	// SweepDefaultLimit 一次最多跑多少组合（只挡明显的手滑）。
	// 千万级上限下，"能不能跑完"由内存和耗时决定：每个组合都要留一份
	// Params + SweepRow（千万组约几个 GB），单核实测约 31ms/组合 —— 配合 workers 用。
	SweepDefaultLimit = 10000000
	// SweepDefaultMinTrades 少于这个样本数的组合标记「样本不足」
	SweepDefaultMinTrades = 20
	// SweepDefaultPeriod 不指定级别时用哪个
	SweepDefaultPeriod = "5"
	// SweepMaxWorkers 并发上限（再多也快不了，只会抢 CPU）
	SweepMaxWorkers = 64
	sweepMaxAxis    = 30 // 单轴候选值上限
	sweepMaxPeriods = 6  // 级别最多几个（每个级别都要取一次数）
)

// sweepAllowedPeriods 支持的级别（与前端下拉一致）
var sweepAllowedPeriods = []string{"5", "15", "30", "60", "120"}

// SweepRequest 参数扫描请求：每个字段是一组候选值，空 = 用默认值（单组合）。
type SweepRequest struct {
	Symbol    string    `json:"symbol"`
	Period    string    `json:"period"`  // 基准级别（Periods 为空时用它）
	Periods   []string  `json:"periods"` // 级别也作为轴：每个级别各自取数
	ORB       []int     `json:"orb"`
	Donchian  []int     `json:"donchian"`
	ATRPeriod []int     `json:"atr_period"`
	ATRK      []float64 `json:"atr_k"`
	VolRatio  []float64 `json:"vol_ratio"`
	HoldBars  []int     `json:"hold_bars"`
	StopATR   []float64 `json:"stop_atr"`
	// StopModes 止损方式候选（atr / prev_low）——「对照策略」就靠这根轴：
	// 同一段行情、同一套参数，一次跑出两种止损方式直接比。
	StopModes   []string  `json:"stop_modes"`
	StopPoints  []float64 `json:"stop_points"`  // prev_low 的缓冲点数
	NoOvernight []int     `json:"no_overnight"` // 0 = 允许隔夜，1 = 日内策略（可作为筛选轴）
	From        string    `json:"from"`         // 时间范围筛选（对每个组合都生效，不是轴）
	To          string    `json:"to"`
	RR          []float64 `json:"rr"`
	Objective   string    `json:"objective"`  // win_rate | avg_return | avg_r | profit_factor
	MinTrades   int       `json:"min_trades"` // 少于这个样本数标记「样本不足」
	Limit       int       `json:"limit"`      // 组合数上限
	Workers     int       `json:"workers"`    // 并发数，0 = 自动（CPU 核数），上限 64
	// Token 进度令牌：非空时服务端会把进度登记下来，前端用
	// GET /api/futures/sweep/progress?token= 轮询（见 internal/api）。
	Token string `json:"token"`
	// OnProgress 每跑完一个组合回调一次（done 由 0 涨到 total，total 在开跑时确定）。
	// 只给服务端上报进度用，不参与 JSON。
	OnProgress func(done, total int) `json:"-"`
	// Run 运行时句柄：绑上它之后并发可以中途 SetWorkers 实时调、进度随时可读。
	// nil = 按固定 Workers 跑完拉倒。同样不参与 JSON。
	Run *SweepRun `json:"-"`
}

// NormalizeSweepWorkers 并发数：0/负数 → CPU 核数；超过上限 → 截到上限。
func NormalizeSweepWorkers(n int) int {
	if n <= 0 {
		n = runtime.NumCPU()
	}
	if n < 1 {
		n = 1
	}
	if n > SweepMaxWorkers {
		n = SweepMaxWorkers
	}
	return n
}

// SweepRow 一个参数组合的回测结果。
type SweepRow struct {
	Params       Params  `json:"params"`
	Trades       int     `json:"trades"`
	WinRate      float64 `json:"win_rate"`
	AvgReturn    float64 `json:"avg_return"`
	AvgR         float64 `json:"avg_r"`
	ProfitFactor float64 `json:"profit_factor"`
	StopExits    int     `json:"stop_exits"`
	TPExits      int     `json:"tp_exits"`
	HoldExits    int     `json:"hold_exits"`
	Reliable     bool    `json:"reliable"` // 样本数够不够（不够的组合不该被当成"最优"）
}

// SweepResult 扫描结果；Rows 已按目标排好序（可靠组合优先）。
type SweepResult struct {
	Symbol     string         `json:"symbol"`
	Periods    []string       `json:"periods"` // 实际参与比较的级别
	Objective  string         `json:"objective"`
	MinTrades  int            `json:"min_trades"`
	Combos     int            `json:"combos"`  // 级别 × 组合
	Workers    int            `json:"workers"` // 实际用了几个并发
	Best       *SweepRow      `json:"best"`
	Rows       []SweepRow     `json:"rows"`
	Skipped    []string       `json:"skipped"`     // 取不到数据的级别（带原因）
	PeriodBars map[string]int `json:"period_bars"` // 各级别用了多少根 K 线（覆盖区间不同，别直接横向比）
	ElapsedMS  int64          `json:"elapsed_ms"`
}

// sweepDataset 一个级别的数据（所有组合共用）。
type sweepDataset struct {
	period  string
	minutes []Bar
	daily   []Daily
}

// normalizeSweep 校验并补齐默认值，同时算出组合数（超上限直接报错）。
func normalizeSweep(req SweepRequest) (SweepRequest, error) {
	d := DefaultParams()
	if strings.TrimSpace(req.Symbol) == "" {
		req.Symbol = d.Symbol
	}
	if strings.TrimSpace(req.Period) == "" {
		req.Period = d.Period
	}
	periods, err := normalizePeriods(req.Periods, req.Period)
	if err != nil {
		return req, err
	}
	req.Periods = periods
	switch req.Objective {
	case "":
		req.Objective = SweepObjectiveAvgReturn
	case SweepObjectiveWinRate, SweepObjectiveAvgReturn, SweepObjectiveAvgR, SweepObjectiveProfitFactor:
	default:
		return req, fmt.Errorf("未知扫描目标 %s（可选：%s/%s/%s/%s）",
			req.Objective, SweepObjectiveWinRate, SweepObjectiveAvgReturn, SweepObjectiveAvgR, SweepObjectiveProfitFactor)
	}
	if req.MinTrades <= 0 {
		req.MinTrades = SweepDefaultMinTrades
	}
	if req.Limit <= 0 {
		req.Limit = SweepDefaultLimit
	}
	req.Workers = NormalizeSweepWorkers(req.Workers)
	if err := ValidateBacktestParams(Params{From: req.From, To: req.To}); err != nil {
		return req, err
	}
	req.ORB = intAxis(req.ORB, d.ORB)
	req.Donchian = intAxis(req.Donchian, d.Donchian)
	req.ATRPeriod = intAxis(req.ATRPeriod, d.ATRPeriod)
	req.ATRK = floatAxis(req.ATRK, d.ATRK)
	req.VolRatio = floatAxis(req.VolRatio, d.VolRatio)
	req.HoldBars = intAxis(req.HoldBars, d.HoldBars)
	req.StopATR = floatAxis(req.StopATR, d.StopATR)
	modes, err := normalizeStopModes(req.StopModes, d.StopMode)
	if err != nil {
		return req, err
	}
	req.StopModes = modes
	req.StopPoints = floatAxis(req.StopPoints, d.StopPoints)
	req.NoOvernight = flagAxis(req.NoOvernight, boolToFlag(d.NoOvernight))
	req.RR = floatAxis(req.RR, d.RR)

	per := 1
	for _, size := range []int{
		len(req.ORB), len(req.Donchian), len(req.ATRPeriod), len(req.ATRK),
		len(req.VolRatio), len(req.HoldBars), len(req.StopATR), len(req.NoOvernight), len(req.RR),
		len(req.StopModes), len(req.StopPoints),
	} {
		per *= size
	}
	if total := per * len(req.Periods); total > req.Limit {
		return req, fmt.Errorf("组合数 %d 超过上限 %d（%d 个级别 × %d 组参数），请减少候选值（每项最多 %d 个）",
			total, req.Limit, len(req.Periods), per, sweepMaxAxis)
	}
	return req, nil
}

// normalizeStopModes 止损方式候选值：空 → 用默认；非法值直接报错
// （不能静默当 atr 跑，否则用户以为在对照，其实两边同一条策略）。
func normalizeStopModes(vals []string, def string) ([]string, error) {
	if len(vals) == 0 {
		return []string{normalizeStopMode(def)}, nil
	}
	if len(vals) > sweepMaxAxis {
		return nil, fmt.Errorf("止损方式最多 %d 个候选", sweepMaxAxis)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(vals))
	for _, raw := range vals {
		v := strings.TrimSpace(raw)
		if v == "" {
			v = def
		}
		if !IsValidStopMode(v) {
			return nil, fmt.Errorf("未知止损方式 %q（可选：%s / %s）", raw, StopModeATR, StopModePrevLow)
		}
		n := normalizeStopMode(v)
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

// normalizePeriods 级别去重升序 + 白名单校验；空 → 用基准级别。
func normalizePeriods(vals []string, def string) ([]string, error) {
	if len(vals) == 0 {
		return []string{def}, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(vals))
	for _, raw := range vals {
		v := strings.TrimSpace(raw)
		if v == "" || seen[v] {
			continue
		}
		if !isSweepPeriod(v) {
			return nil, fmt.Errorf("不支持的级别 %s（可选：%s）", raw, strings.Join(sweepAllowedPeriods, "/"))
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return []string{def}, nil
	}
	sort.Slice(out, func(i, j int) bool { return periodLess(out[i], out[j]) })
	if len(out) > sweepMaxPeriods {
		out = out[:sweepMaxPeriods]
	}
	return out, nil
}

func isSweepPeriod(v string) bool {
	for _, p := range sweepAllowedPeriods {
		if p == v {
			return true
		}
	}
	return false
}

// periodLess 按分钟数排序（"5" < "15" < "60" < "120"）。
func periodLess(a, b string) bool {
	na, oka := periodMinutes(a)
	nb, okb := periodMinutes(b)
	if oka && okb {
		return na < nb
	}
	return a < b
}

func periodMinutes(v string) (int, bool) {
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

func intAxis(vals []int, def int) []int {
	if len(vals) == 0 {
		return []int{def}
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(vals))
	for _, v := range vals {
		if v <= 0 || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return []int{def}
	}
	sort.Ints(out)
	if len(out) > sweepMaxAxis {
		out = out[:sweepMaxAxis]
	}
	return out
}

func boolToFlag(on bool) int {
	if on {
		return 1
	}
	return 0
}

// flagAxis 0/1 开关轴：0 是有意义的值（0 = 允许隔夜），所以不能像数值轴那样把 <=0 过滤掉。
func flagAxis(vals []int, def int) []int {
	if len(vals) == 0 {
		return []int{def}
	}
	seen := map[int]bool{}
	out := make([]int, 0, 2)
	for _, v := range vals {
		f := boolToFlag(v != 0)
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Ints(out)
	return out
}

func floatAxis(vals []float64, def float64) []float64 {
	if len(vals) == 0 {
		return []float64{def}
	}
	seen := map[float64]bool{}
	out := make([]float64, 0, len(vals))
	for _, v := range vals {
		if !finite(v) || v <= 0 || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return []float64{def}
	}
	sort.Float64s(out)
	if len(out) > sweepMaxAxis {
		out = out[:sweepMaxAxis]
	}
	return out
}

// sweepCombos 展开参数笛卡尔积（不含级别，级别在跑每个数据集时套上去）。
func sweepCombos(p SweepRequest) []Params {
	type axis struct {
		vals []float64
		set  func(*Params, float64)
	}
	toF := func(vals []int) []float64 {
		out := make([]float64, 0, len(vals))
		for _, v := range vals {
			out = append(out, float64(v))
		}
		return out
	}
	axes := []axis{
		{toF(p.ORB), func(pp *Params, v float64) { pp.ORB = int(v) }},
		{toF(p.Donchian), func(pp *Params, v float64) { pp.Donchian = int(v) }},
		{toF(p.ATRPeriod), func(pp *Params, v float64) { pp.ATRPeriod = int(v) }},
		{p.ATRK, func(pp *Params, v float64) { pp.ATRK = v }},
		{p.VolRatio, func(pp *Params, v float64) { pp.VolRatio = v }},
		{toF(p.HoldBars), func(pp *Params, v float64) { pp.HoldBars = int(v) }},
		{p.StopATR, func(pp *Params, v float64) { pp.StopATR = v }},
		{p.StopPoints, func(pp *Params, v float64) { pp.StopPoints = v }},
		{toF(p.NoOvernight), func(pp *Params, v float64) { pp.NoOvernight = v > 0 }},
		{p.RR, func(pp *Params, v float64) { pp.RR = v }},
	}
	out := []Params{{Symbol: p.Symbol, Period: p.Period, From: p.From, To: p.To}}
	for _, a := range axes {
		next := make([]Params, 0, len(out)*len(a.vals))
		for _, cur := range out {
			for _, v := range a.vals {
				cp := cur
				a.set(&cp, v)
				next = append(next, cp)
			}
		}
		out = next
	}
	// 止损方式是字符串轴，单独展开（数值轴那套塞不进去）
	if len(p.StopModes) > 0 {
		next := make([]Params, 0, len(out)*len(p.StopModes))
		for _, cur := range out {
			for _, m := range p.StopModes {
				cp := cur
				cp.StopMode = m
				next = append(next, cp)
			}
		}
		out = next
	}
	return out
}

func sweepMetric(r SweepRow, objective string) float64 {
	switch objective {
	case SweepObjectiveWinRate:
		return r.WinRate
	case SweepObjectiveAvgR:
		return r.AvgR
	case SweepObjectiveProfitFactor:
		return r.ProfitFactor
	default:
		return r.AvgReturn
	}
}

// sortSweepRows 可靠组合优先，其次按目标降序，最后按样本数降序。
func sortSweepRows(rows []SweepRow, objective string) []SweepRow {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Reliable != rows[j].Reliable {
			return rows[i].Reliable
		}
		mi, mj := sweepMetric(rows[i], objective), sweepMetric(rows[j], objective)
		if math.Abs(mi-mj) > 1e-12 {
			return mi > mj
		}
		return rows[i].Trades > rows[j].Trades
	})
	return rows
}

// sweepOne 跑一个组合。纯函数（只读 K 线），所以可以并发跑多个组合。
func sweepOne(ds sweepDataset, params Params, minTrades int) SweepRow {
	res := runBacktest(ds.minutes, ds.daily, params, false)
	return SweepRow{
		Params:       params,
		Trades:       res.Trades,
		WinRate:      res.WinRate,
		AvgReturn:    res.AvgReturn,
		AvgR:         res.AvgR,
		ProfitFactor: res.ProfitFactor,
		StopExits:    res.StopExits,
		TPExits:      res.TPExits,
		HoldExits:    res.HoldExits,
		Reliable:     res.Trades >= minTrades,
	}
}

// sweepOver 在若干级别的数据上跑完整扫描（纯计算，不取数）。
// 组合之间互相独立 → 用 worker 池并行（实测 31ms/组合，400 组合串行要 12s）。
func sweepOver(ctx context.Context, datasets []sweepDataset, p SweepRequest) SweepResult {
	base := sweepCombos(p)
	type job struct {
		ds    int
		combo int
	}
	jobs := make([]job, 0, len(datasets)*len(base))
	rows := make([]SweepRow, 0, len(datasets)*len(base))
	paramsOf := make([]Params, 0, len(datasets)*len(base))
	for di, ds := range datasets {
		for ci := range base {
			cp := base[ci]
			cp.Period = ds.period
			paramsOf = append(paramsOf, cp)
			jobs = append(jobs, job{ds: di, combo: len(paramsOf) - 1})
		}
	}
	rows = make([]SweepRow, len(paramsOf))

	// 进度上报：先把 total 报出去，前端才能画进度条（组合数要到这里才知道）
	total := len(paramsOf)
	progress := p.OnProgress
	var done atomic.Int64
	if progress != nil {
		progress(0, total)
	}

	// 并发：带着句柄就以句柄里的期望值为准（用户可能在建池之前就改过并发）
	workers := NormalizeSweepWorkers(p.Workers)
	if p.Run != nil {
		workers = p.Run.Workers()
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers < 1 {
		workers = 1
	}

	pool, err := newSweepPool(workers) // 建池失败不该让整次扫描挂掉 → 退化成串行
	if err != nil {
		pool = nil
	}
	if pool != nil {
		defer pool.Release()
		if p.Run != nil {
			// 绑上句柄：这之后 SetWorkers 会直接 Tune 这个池
			p.Run.bindPool(pool)
			defer p.Run.releasePool()
		}
	}

	execute := func(j job) {
		if ctx.Err() != nil { // 客户端断开 / 取消 → 立刻停手，别白烧 CPU
			rows[j.combo] = SweepRow{Params: paramsOf[j.combo]}
			return
		}
		rows[j.combo] = sweepOne(datasets[j.ds], paramsOf[j.combo], p.MinTrades)
		if progress != nil {
			progress(int(done.Add(1)), total)
		}
	}

	var wg sync.WaitGroup
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		if pool == nil {
			execute(j)
			continue
		}
		wg.Add(1)
		// 池满时 Submit 会阻塞：既是背压（内存里只挂 ~并发数 个任务），
		// 也是 Tune 能立刻见效的原因（容量一涨，这里立刻能继续提交）
		if err := pool.Submit(func() { defer wg.Done(); execute(j) }); err != nil {
			wg.Done()
			break
		}
	}
	wg.Wait()

	rows = sortSweepRows(rows, p.Objective)

	out := SweepResult{
		Symbol:    p.Symbol,
		Periods:   p.Periods,
		Objective: p.Objective,
		MinTrades: p.MinTrades,
		Combos:    len(paramsOf),
		Workers:   workers,
		Rows:      rows,
	}
	if len(rows) > 0 {
		best := rows[0] // 可靠组合已被排到最前
		out.Best = &best
	}
	return out
}

// SweepBars 在给定 K 线上跑完整扫描（纯计算，不取数；单级别）。
func SweepBars(minutes []Bar, daily []Daily, req SweepRequest) SweepResult {
	p, err := normalizeSweep(req)
	if err != nil {
		return SweepResult{Symbol: req.Symbol, Periods: []string{req.Period}, Objective: SweepObjectiveAvgReturn}
	}
	return sweepOver(context.Background(), []sweepDataset{{period: p.Periods[0], minutes: minutes, daily: daily}}, p)
}

// ValidateSweep 只校验入参（目标/级别是否合法、组合数是否超限），不取数。
// 上层可以据此把参数错误和取数失败区分成 400 / 502。
func ValidateSweep(req SweepRequest) error {
	_, err := normalizeSweep(req)
	return err
}

// SweepWithSource 按级别取数（每个级别一次）→ 跑全部组合。
// 日线取一次共用；某个级别取不到数据只跳过它，并在 Skipped 里说明原因。
func SweepWithSource(ctx context.Context, src BarSource, req SweepRequest) (SweepResult, error) {
	p, err := normalizeSweep(req)
	if err != nil {
		return SweepResult{}, err
	}
	v, ok := VarietyOfSymbol(p.Symbol)
	if !ok {
		return SweepResult{}, fmt.Errorf("未知品种 %s", p.Symbol)
	}
	daily, err := src.Daily(ctx, v)
	if err != nil {
		return SweepResult{}, fmt.Errorf("日线: %w", err)
	}

	started := time.Now()
	datasets := make([]sweepDataset, 0, len(p.Periods))
	out := SweepResult{Symbol: p.Symbol, Periods: []string{}, Skipped: []string{}, PeriodBars: map[string]int{}}
	for _, period := range p.Periods { // 取数按顺序做，别把上游打限流
		minutes, err := src.Minute(ctx, v, period)
		if err != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s分钟：%v", period, err))
			continue
		}
		if len(minutes) == 0 {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s分钟：无数据", period))
			continue
		}
		if err := checkMinuteDailyAgree(v.Prefix, minutes, daily); err != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s分钟：%v", period, err))
			continue
		}
		datasets = append(datasets, sweepDataset{period: period, minutes: minutes, daily: daily})
		out.Periods = append(out.Periods, period)
		out.PeriodBars[period] = len(minutes)
	}
	if len(datasets) == 0 {
		return SweepResult{}, fmt.Errorf("所有级别都取不到数据：%s", strings.Join(out.Skipped, "；"))
	}

	computed := sweepOver(ctx, datasets, p)
	if err := ctx.Err(); err != nil { // 客户端断开 / 超时：不要把半截结果当成结论
		return SweepResult{}, fmt.Errorf("扫描已取消：%w", err)
	}
	computed.Skipped = out.Skipped
	computed.PeriodBars = out.PeriodBars
	computed.ElapsedMS = time.Since(started).Milliseconds()
	return computed, nil
}
