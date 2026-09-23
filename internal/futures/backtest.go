package futures

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// 出场原因
const (
	ExitStop = "止损"   // 触及止损（stop_atr × ATR）
	ExitTP   = "止盈"   // 触及止盈（止损距离 × 盈亏比）
	ExitHold = "持有到期" // 都没触发，第 N 根收盘平仓
	ExitEOD  = "日内收盘" // 禁止隔夜：当日日盘最后一根收盘平仓
)

// VarietyOfSymbol 从合约代码取品种（JM0 / JM2701 → 焦煤）。
func VarietyOfSymbol(symbol string) (Variety, bool) {
	return varietyByPrefix(letterPrefix(strings.ToUpper(strings.TrimSpace(symbol))))
}

// BacktestWithSource 用指定数据源跑突破回测。
// api 侧传「本地优先」源：先在 SQLite 里读，缺数据才走网络（并回写本地）。
func BacktestWithSource(ctx context.Context, src BarSource, p Params) (Result, error) {
	p = mergeParams(p)
	v, ok := VarietyOfSymbol(p.Symbol)
	if !ok {
		return Result{}, fmt.Errorf("未知品种 %s", p.Symbol)
	}
	minutes, err := src.Minute(ctx, v, p.Period)
	if err != nil {
		return Result{}, fmt.Errorf("%s分钟: %w", p.Period, err)
	}
	daily, err := src.Daily(ctx, v)
	if err != nil {
		return Result{}, fmt.Errorf("日线: %w", err)
	}
	// 分钟线与日线价差异常（源错量级）→ 直接报错，别拿脏数据算出一堆假样本
	if err := checkMinuteDailyAgree(v.Prefix, minutes, daily); err != nil {
		return Result{}, err
	}
	return BacktestBars(minutes, daily, p), nil
}

func BacktestBars(minutes []Bar, daily []Daily, p Params) Result {
	return runBacktest(minutes, daily, p, true)
}

// runBacktest 跑回测。withBars 为 false 时不算图表 K 线：参数扫描每个组合都要跑一遍，
// 复制整段行情只会制造大量临时内存，并行时更容易把进程顶到换页。
func runBacktest(minutes []Bar, daily []Daily, p Params, withBars bool) Result {
	p = mergeParams(p)
	out := Result{Symbol: p.Symbol, Period: p.Period, Items: []Outcome{}}
	if len(minutes) == 0 {
		return out
	}
	v, _ := VarietyOfSymbol(p.Symbol) // 取品种码：止损/止盈要按它的报价单位对齐
	from, to, fromText, toText := backtestRange(p)
	out.From, out.To = fromText, toText
	fromDay, toDay := truncateDate(from), truncateDate(to)

	days := realSessionDays(minutes)
	var items []Outcome
	for _, day := range days {
		// 按「信号所在交易日」先粗筛（指标仍在全量序列上算，不受范围影响）
		if !from.IsZero() && day.Before(fromDay) {
			continue
		}
		if !to.IsZero() && day.After(toDay) {
			continue
		}
		levels := PivotLevels(daily, day)
		if p.Period == "5" {
			if orb := ORBLevels(minutes, day, p.ORB); len(orb) > 0 {
				levels = append(levels, orb...)
			}
		}
		ev := ScanTimeframe(minutes, day, levels, p)
		ev = filterEventsByRange(ev, from, to)
		got, skipped := evaluate(ev, minutes, v.Prefix, p)
		items = append(items, got...)
		out.SkippedEOD += skipped
	}
	fillStats(&out, items)
	if withBars {
		out.Bars = klineBars(minutes)
	}
	return out
}

// parseRangeTime 宽松解析起止时间：支持 "2006-01-02"、"2006-01-02 15:04"、RFC3339。
// 解析不了就返回 false（调用方按「不限」处理，不因为一个手滑的输入把回测打成 0 笔）。
func parseRangeTime(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		"2006-01-02 15:04:05", "2006-01-02 15:04",
		"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, locCST); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// backtestRange 解析回测范围。只给日期时：起始补 00:00、结束补 23:59（当天整天算在范围内）。
func backtestRange(p Params) (from, to time.Time, fromText, toText string) {
	if t, ok := parseRangeTime(p.From); ok {
		from = t
		fromText = t.In(locCST).Format("2006-01-02 15:04")
	}
	if t, ok := parseRangeTime(p.To); ok {
		to = t
		if len(strings.TrimSpace(p.To)) <= len("2006-01-02") {
			to = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 0, 0, locCST)
		}
		toText = to.In(locCST).Format("2006-01-02 15:04")
	}
	return from, to, fromText, toText
}

// filterEventsByRange 按信号时间过滤（含边界）；两端都为空则原样返回。
func filterEventsByRange(ev []Event, from, to time.Time) []Event {
	if from.IsZero() && to.IsZero() {
		return ev
	}
	out := make([]Event, 0, len(ev))
	for _, e := range ev {
		if !from.IsZero() && e.Time.Before(from) {
			continue
		}
		if !to.IsZero() && e.Time.After(to) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ValidateBacktestParams 校验时间范围，供上层把参数错误区分成 400。
func ValidateBacktestParams(p Params) error {
	for _, raw := range []string{p.From, p.To} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if _, ok := parseRangeTime(raw); !ok {
			return fmt.Errorf("无法解析时间 %q（可用 2006-01-02 或 2006-01-02 15:04）", raw)
		}
	}
	from, to, _, _ := backtestRange(p)
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return fmt.Errorf("起始时间 %s 不能晚于结束时间 %s", from.Format("2006-01-02 15:04"), to.Format("2006-01-02 15:04"))
	}
	return nil
}

// fillStats 汇总统计：胜率 / 平均收益 / 盈利因子 / 期望 R / 出场分布。
func fillStats(out *Result, items []Outcome) {
	out.Items = items
	out.Trades = len(items)
	var win, wins, losses int
	var sum, sumR, winSum, lossSum float64
	for _, it := range items {
		sum += it.Return
		sumR += it.R
		if it.Return > 0 {
			wins++
			winSum += it.Return
		} else if it.Return < 0 {
			losses++
			lossSum += it.Return
		}
		if it.Correct {
			win++
		}
		switch it.ExitReason {
		case ExitStop:
			out.StopExits++
		case ExitTP:
			out.TPExits++
		case ExitHold:
			out.HoldExits++
		case ExitEOD:
			out.EODExits++
		}
	}
	out.Correct = win
	if n := len(items); n > 0 {
		out.WinRate = float64(win) / float64(n)
		out.AvgReturn = sum / float64(n)
		out.AvgR = sumR / float64(n)
	}
	if wins > 0 {
		out.AvgWin = winSum / float64(wins)
	}
	if losses > 0 {
		out.AvgLoss = lossSum / float64(losses)
	}
	if winSum > 0 && lossSum < 0 { // 没有亏损单时不写 Inf/NaN（JSON 会编码失败）
		out.ProfitFactor = winSum / -lossSum
	}
}

func klineBars(minutes []Bar) []KlineBar {
	out := make([]KlineBar, 0, len(minutes))
	for _, b := range minutes {
		out = append(out, KlineBar{
			Time: b.Time.In(locCST).Format("2006-01-02 15:04"),
			Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume,
		})
	}
	return out
}

// evaluate 逐笔出场模拟（walk-forward，无未来函数）：
// 入场 = 信号那根的收盘价；止损 = 入场 ∓ stop_atr×ATR，止盈 = 入场 ± 止损距离×盈亏比（都按报价单位对齐，
// 与提醒里的推荐价同一套规则）；从下一根开始逐根检查，最多持有 HoldBars 根。
//
// 保守约定：① 同一根既破止损又触止盈 → 按止损算；② 跳空开盘已越过止损 → 按开盘价成交。
// 日内策略（NoOvernight）：出场必须落在「入场当日的日盘」内（hour < 16 的最后一根收盘平仓，ExitEOD）；
// 夜盘 21:00+ 属次日交易时段，同样不持有；入场就在日盘收盘后/夜盘的信号不可交易 → 跳过并计数。
//
// 返回值第二项是「因禁止隔夜被跳过」的信号数。
func evaluate(ev []Event, bars []Bar, prefix string, p Params) ([]Outcome, int) {
	p = mergeParams(p) // 止损方式 / 点数 / 倍数可能是零值，统一补默认
	hold := p.HoldBars
	if hold <= 0 {
		hold = 6
	}
	idx := map[int64]int{}
	for i, b := range bars {
		idx[b.Time.Unix()] = i
	}
	out := make([]Outcome, 0, len(ev))
	skipped := 0
	for _, e := range ev {
		i, ok := idx[e.Time.Unix()]
		if !ok || e.Close <= 0 || i+1 >= len(bars) { // 入场后至少要有下一根可交易
			continue
		}
		up := e.Direction != DirDown
		entry := e.Close
		// prev_low 模式要「前一根」：事件里没带（老数据）就从 K 线序列补
		prevLow, prevHigh := e.PrevLow, e.PrevHigh
		if (prevLow <= 0 || prevHigh <= 0) && i > 0 {
			prevLow, prevHigh = bars[i-1].Low, bars[i-1].High
		}
		stop, tp := RecommendStop(StopInput{
			Prefix: prefix, Direction: e.Direction, Entry: entry, ATR: e.ATR,
			PrevLow: prevLow, PrevHigh: prevHigh,
			StopMode: p.StopMode, StopATR: p.StopATR, StopPoints: p.StopPoints, RR: p.RR,
		})
		tick := TickSize(prefix)

		complete := i+hold < len(bars) // 数据够走完持有周期
		last := i + hold
		if !complete {
			last = len(bars) - 1
		}
		if p.NoOvernight {
			day := bars[i].Time.In(locCST)
			if day.Hour() >= 16 { // 信号出现在夜盘：当日日盘已经结束，日内策略吃不到
				skipped++
				continue
			}
			dayEnd := i // 入场当日「日盘」的最后一根
			for k := i + 1; k < len(bars); k++ {
				bt := bars[k].Time.In(locCST)
				if !sameDate(bt, day) {
					break // 跨自然日 → 后面的都不算当日
				}
				if bt.Hour() >= 16 {
					continue // 夜盘不算日盘
				}
				dayEnd = k
			}
			if dayEnd <= i { // 入场就是日盘最后一根，平不掉
				skipped++
				continue
			}
			last = dayEnd
			complete = true // 日内模式的终点是确定的（当日日盘收盘）
		}
		exitIdx, exitPrice, reason := -1, 0.0, ""
		for k := i + 1; k <= last; k++ {
			b := bars[k]
			if stop > 0 { // 盘口第一笔就是跳空价：直接按开盘价，别美化成交价
				if (up && b.Open <= stop) || (!up && b.Open >= stop) {
					exitIdx, exitPrice, reason = k, b.Open, ExitStop
					break
				}
				if (up && b.Low <= stop) || (!up && b.High >= stop) {
					exitIdx, exitPrice, reason = k, stop, ExitStop
					break
				}
			}
			if tp > 0 && ((up && b.High >= tp) || (!up && b.Low <= tp)) {
				exitIdx, exitPrice, reason = k, tp, ExitTP
				break
			}
		}
		if exitIdx < 0 {
			switch {
			case p.NoOvernight:
				exitIdx, exitPrice, reason = last, bars[last].Close, ExitEOD
			case !complete:
				continue // 还没走到持有终点，这笔没结束，不计入统计
			default:
				exitIdx, exitPrice, reason = i+hold, bars[i+hold].Close, ExitHold
			}
		}

		ret := exitPrice/entry - 1
		if !up { // 收益按突破方向折算：做空跌了才是赚
			ret = -ret
		}
		r := 0.0
		if risk := math.Abs(entry-stop) / entry; stop > 0 && risk > 0 {
			r = ret / risk
		}
		out = append(out, Outcome{
			Time:      e.Time.In(locCST).Format("2006-01-02 15:04"),
			Direction: e.Direction, Level: e.Level,
			Close: entry, Volume: e.Volume, LevelPrice: e.LevelPrice,
			ExitTime:  bars[exitIdx].Time.In(locCST).Format("2006-01-02 15:04"),
			ExitPrice: exitPrice, Return: ret, Correct: ret > 0,
			ExitReason: reason, StopPrice: stop, TPPrice: tp, R: r, StopATR: effectiveStopATR(p.StopATR),
			TickSize: tick, StopMode: normalizeStopMode(p.StopMode), StopPoints: effectiveStopPoints(p.StopPoints),
		})
	}
	return out, skipped
}

func realSessionDays(min []Bar) []time.Time {
	type stat struct {
		n int
		v float64
	}
	stats := map[string]*stat{}
	var keys []string
	for _, b := range min {
		t := b.Time.In(locCST)
		if t.Hour() >= 16 {
			continue
		}
		k := t.Format("2006-01-02")
		s := stats[k]
		if s == nil {
			s = &stat{}
			stats[k] = s
			keys = append(keys, k)
		}
		s.n++
		s.v += b.Volume
	}
	var days []time.Time
	for _, k := range keys {
		s := stats[k]
		if s.n >= 3 && s.v >= 5000 {
			d, _ := time.ParseInLocation("2006-01-02", k, locCST)
			days = append(days, d)
		}
	}
	return days
}
