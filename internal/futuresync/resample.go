package futuresync

import (
	"strconv"
	"time"

	"github.com/wbscoder2026/stock-x/internal/store"
)

// 本地合成：只补 1 分钟，5/15/30/60/120 分钟由本地 1 分钟聚合出来，不再各问上游要一遍。
//
// 口径是照着上游（东财/新浪）的实际数据定的，合成出来的时间戳必须和上游一致，
// 否则同一个周期会混进两套不同的时间戳、把序列搞乱：
//   - K 线标注**结束时刻**：日盘第一根是 09:01（覆盖 09:00-09:01），最后一根 15:00；
//   - 按**交易分钟**累计：小节休息（10:15-10:30）那 15 分钟不算在内，午休也不算，
//     所以 60 分钟线是 10:00 / 11:15 / 14:15 / 15:00 而不是按整点切；
//   - 一个**交易日**结束时没凑满的那一根照样给：14:16-15:00 只有 45 分钟，也收成一根 15:00。
//     交易日 = 夜盘(21:00 起) + 次日日盘，所以午休不断、日盘收盘才断。

// nightStartHour 夜盘起始钟点：20 点之后的行情算下一个交易日
// （和期货「夜盘属次日」的口径一致，用的就是上游自己的排法）。
const nightStartHour = 20

// deriveWindow 用最近这么多根 1 分钟线合成。只做增量维护够用了，
// 也避免每轮把几年的一分钟线全读出来。
const deriveWindow = 8000

// minutePeriod 周期字符串 → 分钟数。基频（1 分钟）和日线不能合成，返回 false。
func minutePeriod(period string) (int, bool) {
	if period == "" || period == "1" || period == DailyPeriod {
		return 0, false
	}
	n, err := strconv.Atoi(period)
	if err != nil || n <= 1 {
		return 0, false
	}
	return n, true
}

// ResampleBars 把升序的 1 分钟 K 线聚合成 n 分钟 K 线。
//
// 只吐「已经走完」的 K 线：凑满 n 根交易分钟，或者遇到收盘/午休的大空隙。
// 序列末尾那根还没走完的先不落库——等它凑满（或下一轮数据带来收盘空隙）再说，
// 不然会写进一根上游根本不存在的半根 K 线。
//
// 例外：序列末尾那根若整个交易日都已经走完了（比如昨天日盘 14:16-15:00 那根），
// 就是定稿的，照样收出来；所以要看一眼 now。
func ResampleBars(rows []store.FuturesBar, n int) []store.FuturesBar {
	return ResampleBarsAt(rows, n, time.Now())
}

// ResampleBarsAt 同 ResampleBars，now 可注入（测试用固定时钟）。
func ResampleBarsAt(rows []store.FuturesBar, n int, now time.Time) []store.FuturesBar {
	if n <= 1 || len(rows) == 0 {
		return nil
	}
	period := strconv.Itoa(n)
	out := make([]store.FuturesBar, 0, len(rows)/n+1)
	// 本地 1 分钟线的开头几乎总是半截的（往回翻页翻到哪算哪，不是从开盘那根开始），
	// 从半截的日子开始数会把这一天的分组边界整体带偏，合成出上游不存在的 K 线。
	// 所以只认「从开盘那根开始」的交易日。
	dayComplete := completeTradingDays(rows)
	var cur *store.FuturesBar
	count := 0
	flush := func() {
		if cur != nil {
			if dayComplete[tradingDay(cur.Time)] {
				out = append(out, *cur)
			}
			cur = nil
			count = 0
		}
	}
	for i, b := range rows {
		if cur == nil {
			cur = &store.FuturesBar{
				Symbol: b.Symbol, Period: period, Time: b.Time,
				Open: b.Open, High: b.High, Low: b.Low,
				Close: b.Close, Volume: b.Volume, Hold: b.Hold,
			}
			count = 1
		} else {
			if b.High > cur.High {
				cur.High = b.High
			}
			if b.Low < cur.Low {
				cur.Low = b.Low
			}
			cur.Close = b.Close
			cur.Volume += b.Volume
			cur.Hold = b.Hold // 持仓量是时点值，取最后一根
			cur.Time = b.Time
			count++
		}
		if count >= n {
			flush()
			continue
		}
		// 换交易日（日盘收盘 → 夜盘）→ 这一天的最后一根收掉，哪怕没凑满
		if i+1 < len(rows) && tradingDay(rows[i+1].Time) != tradingDay(b.Time) {
			flush()
		}
	}
	// 末尾那根：整个交易日都过去了就是定稿的（昨天 15:00 那根），收出来；
	// 还在当天的话它会继续长，等凑满再说。
	if cur != nil && tradingDay(now) != tradingDay(rows[len(rows)-1].Time) {
		flush()
	}
	return out
}

// completeTradingDays 标出哪些交易日是从开盘那根开始的（这种才合成）。
func completeTradingDays(rows []store.FuturesBar) map[string]bool {
	out := make(map[string]bool)
	seen := make(map[string]bool, len(rows)/300+1)
	for _, b := range rows {
		d := tradingDay(b.Time)
		if seen[d] {
			continue
		}
		seen[d] = true
		out[d] = isSessionOpen(b.Time)
	}
	return out
}

// isSessionOpen 这根是不是某个交易日的开盘第一根。
// 国内期货的交易日要么从夜盘 21:01 开始，要么（中金所等没有夜盘的）从日盘 09:01/09:31 开始。
func isSessionOpen(t time.Time) bool {
	local := t.In(backfillCST)
	switch local.Hour() {
	case 21:
		return local.Minute() == 1
	case 9:
		return local.Minute() == 1 || local.Minute() == 31
	}
	return false
}

// tradingDay 一根 K 线属于哪个交易日。夜盘（20 点之后）归到次日，
// 于是「午休不算换天、日盘收盘才换天」——和上游的切法一致。
func tradingDay(t time.Time) string {
	local := t.In(backfillCST)
	if local.Hour() >= nightStartHour {
		return local.AddDate(0, 0, 1).Format("2006-01-02")
	}
	return local.Format("2006-01-02")
}

// DerivePeriod 用本地 1 分钟线合成出某个分钟周期并落库（upsert，不会删已有数据）。
// 返回写入/更新的根数。
func DerivePeriod(st *store.Store, cache *BarCache, symbol, period string) (int, error) {
	n, ok := minutePeriod(period)
	if !ok || st == nil || symbol == "" {
		return 0, nil
	}
	rows, err := st.FuturesBars(symbol, "1", deriveWindow)
	if err != nil {
		return 0, err
	}
	out := ResampleBars(rows, n)
	if len(out) == 0 {
		return 0, nil
	}
	saved, err := st.UpsertFuturesBars(out)
	if err != nil {
		return 0, err
	}
	// 并进内存：已经有这段序列就增量合并，没有就整段放入（和增量同步一个口径）
	if cache != nil {
		_, had := cache.Get(symbol, period)
		touchCache(cache, symbol, period, out, had, out)
	}
	return saved, nil
}

// shouldDerive 这个周期该本地合成还是照旧问上游要？
//
// 判据是「本地 1 分钟有没有挖到这个周期前面」：
// 上游每个周期只给固定窗口（约 1000 根），5 分钟能覆盖 16 天、120 分钟能覆盖一年多，
// 而 1 分钟目前只有几天——这时候改用合成会把已有的历史**缩水**。
// 所以只有等 1 分钟补得比这个周期更深了，才切成合成。切过去之后就不再打这个周期的接口。
func shouldDerive(st *store.Store, symbol, period string) bool {
	if _, ok := minutePeriod(period); !ok {
		return false
	}
	firstMinute, _, nMinute, err := st.FuturesRange(symbol, "1")
	if err != nil || nMinute == 0 {
		return false // 本地还没有 1 分钟线，合成不了
	}
	firstPeriod, _, nPeriod, err := st.FuturesRange(symbol, period)
	if err != nil || nPeriod == 0 {
		return false // 这个周期本地还没有 → 照旧问上游要，先把历史铺起来
	}
	return !firstPeriod.Before(firstMinute) // 1 分钟已经更深（或持平）→ 合成
}
