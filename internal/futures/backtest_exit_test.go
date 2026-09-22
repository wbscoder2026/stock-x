package futures

import (
	"math"
	"testing"
	"time"
)

// ---- 出场模拟（止盈止损）测试。约定：入场 = 信号那根的收盘价；
// 止损 = 入场 ∓ 1×ATR，止盈 = 入场 ± 1×ATR×盈亏比；最多持有 hold 根。

func btTime(min int) time.Time {
	return time.Date(2026, 9, 22, 9, 0, 0, 0, locCST).Add(time.Duration(min) * time.Minute)
}

func btBar(min int, o, h, l, c float64) Bar {
	return Bar{Time: btTime(min), Open: o, High: h, Low: l, Close: c, Volume: 1000}
}

func btEvent(dir string, close, atr float64) Event {
	return Event{
		Time: btTime(0), Direction: dir, Level: "PDH(昨高)",
		Close: close, ATR: atr, Volume: 1000, LevelPrice: close - 1,
	}
}

// evalFirst 取第一笔出场结果（测试便利函数）。
func evalFirst(t *testing.T, ev []Event, bars []Bar, prefix string, p Params) Outcome {
	t.Helper()
	out, _ := evaluate(ev, bars, prefix, p)
	if len(out) == 0 {
		t.Fatalf("没有样本：%+v", ev)
	}
	return out[0]
}

// entryBar + 一根或多根后续 K 线
func btBars(entry Bar, rest ...Bar) []Bar {
	return append([]Bar{entry}, rest...)
}

func TestEvaluateTakeProfitHit(t *testing.T) {
	// 螺纹钢：报价单位 1，止损 98、止盈 103（100 + 2×1.5）
	bars := btBars(
		btBar(0, 99, 101, 99, 100),
		btBar(5, 100, 105, 99, 104),
		btBar(10, 104, 106, 103, 105),
	)
	out, _ := evaluate([]Event{btEvent(DirUp, 100, 2)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	if len(out) != 1 {
		t.Fatalf("应有 1 条：%+v", out)
	}
	o := out[0]
	if o.ExitReason != ExitTP || o.ExitPrice != 103 {
		t.Fatalf("应止盈 103：%+v", o)
	}
	if !o.Correct || math.Abs(o.R-1.5) > 1e-9 {
		t.Fatalf("止盈应 +1.5R：%+v", o)
	}
	if o.StopPrice != 98 || o.TPPrice != 103 {
		t.Fatalf("出场价没记录：%+v", o)
	}
}

func TestEvaluateStopLossHit(t *testing.T) {
	bars := btBars(
		btBar(0, 99, 101, 99, 100),
		btBar(5, 100, 101, 96, 97),
	)
	out, _ := evaluate([]Event{btEvent(DirUp, 100, 2)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	o := out[0]
	if o.ExitReason != ExitStop || o.ExitPrice != 98 {
		t.Fatalf("应止损 98：%+v", o)
	}
	if o.Correct || math.Abs(o.R+1) > 1e-9 {
		t.Fatalf("止损应 -1R：%+v", o)
	}
}

func TestEvaluateStopWinsWhenBothTouchedInSameBar(t *testing.T) {
	// 同一根既破止损又触止盈 → 按保守（止损）算，不能拿最好的价
	bars := btBars(
		btBar(0, 99, 101, 99, 100),
		btBar(5, 100, 105, 96, 104),
	)
	out, _ := evaluate([]Event{btEvent(DirUp, 100, 2)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	if out[0].ExitReason != ExitStop || out[0].ExitPrice != 98 {
		t.Fatalf("同根触发应算止损：%+v", out[0])
	}
}

func TestEvaluateGapThroughStopUsesOpen(t *testing.T) {
	// 跳空开盘已在止损下方 → 按开盘价成交，不能美化到 98
	bars := btBars(
		btBar(0, 99, 101, 99, 100),
		btBar(5, 95, 96, 94, 95),
	)
	out, _ := evaluate([]Event{btEvent(DirUp, 100, 2)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	o := out[0]
	if o.ExitReason != ExitStop || o.ExitPrice != 95 {
		t.Fatalf("跳空应按开盘价 95 出场：%+v", o)
	}
	if math.Abs(o.R+2.5) > 1e-9 {
		t.Fatalf("跳空应亏 2.5R：%+v", o)
	}
}

func TestEvaluateHoldsWhenNeitherTouched(t *testing.T) {
	rest := make([]Bar, 0, 8)
	for i := 1; i <= 8; i++ {
		rest = append(rest, btBar(i*5, 100, 101, 99, 100.5))
	}
	bars := btBars(btBar(0, 99, 101, 99, 100), rest...)
	out, _ := evaluate([]Event{btEvent(DirUp, 100, 2)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	o := out[0]
	if o.ExitReason != ExitHold {
		t.Fatalf("没触发应持有到期：%+v", o)
	}
	// 第 6 根（下标 6）收盘平仓
	if o.ExitTime != bars[6].Time.In(locCST).Format("2006-01-02 15:04") {
		t.Fatalf("应持有到第 6 根：%+v", o)
	}
	if math.Abs(o.R-0.25) > 1e-9 { // 0.5% / 2% 风险
		t.Fatalf("R 不对：%+v", o)
	}
}

func TestEvaluateShortMirror(t *testing.T) {
	// 空头：止损在上方 102、止盈在下方 97
	bars := btBars(
		btBar(0, 101, 101, 99, 100),
		btBar(5, 100, 101, 95, 96),
	)
	out, _ := evaluate([]Event{btEvent(DirDown, 100, 2)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	o := out[0]
	if o.ExitReason != ExitTP || o.ExitPrice != 97 {
		t.Fatalf("空头应止盈 97：%+v", o)
	}
	if !o.Correct || math.Abs(o.R-1.5) > 1e-9 {
		t.Fatalf("空头止盈应 +1.5R（收益要按方向调整）：%+v", o)
	}
	if o.StopPrice != 102 || o.TPPrice != 97 {
		t.Fatalf("空头出场价不对：%+v", o)
	}

	// 空头同根双触发 → 止损
	bars = btBars(
		btBar(0, 101, 101, 99, 100),
		btBar(5, 100, 106, 95, 105),
	)
	o = evalFirst(t, []Event{btEvent(DirDown, 100, 2)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	if o.ExitReason != ExitStop || o.ExitPrice != 102 || math.Abs(o.R+1) > 1e-9 {
		t.Fatalf("空头同根触发应算止损：%+v", o)
	}
}

func TestEvaluateNoLookAheadOnEntryBar(t *testing.T) {
	// 入场那根之后才可能出场：入场当根自己冲到 106 也不算止盈
	bars := btBars(
		btBar(0, 99, 106, 99, 100), // 当根最高 106 > 止盈 103
		btBar(5, 100, 101, 99, 100.5),
		btBar(10, 100, 101, 99, 100.5),
		btBar(15, 100, 101, 99, 100.5),
		btBar(20, 100, 101, 99, 100.5),
		btBar(25, 100, 101, 99, 100.5),
		btBar(30, 100, 101, 99, 100.5),
	)
	out, _ := evaluate([]Event{btEvent(DirUp, 100, 2)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	if out[0].ExitReason != ExitHold {
		t.Fatalf("入场当根的影线不该算出场（未来函数）：%+v", out[0])
	}
}

func TestEvaluateWithoutATRKeepsHolding(t *testing.T) {
	// ATR 数据不足（给不出止损止盈）→ 退化成纯持有到期，而不是丢弃样本
	rest := make([]Bar, 0, 8)
	for i := 1; i <= 8; i++ {
		rest = append(rest, btBar(i*5, 100, 101, 99, 100.5))
	}
	bars := btBars(btBar(0, 99, 101, 99, 100), rest...)
	o := evalFirst(t, []Event{btEvent(DirUp, 100, 0)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	if o.ExitReason != ExitHold || o.StopPrice != 0 || o.TPPrice != 0 || o.R != 0 {
		t.Fatalf("无 ATR 应纯持有：%+v", o)
	}
}

func TestEvaluateMatchesAlertRecommendation(t *testing.T) {
	// 回测用的止损/止盈必须与提醒推荐价完全一致（同一套规则，防两边漂移）
	rest := make([]Bar, 0, 6)
	for i := 1; i <= 6; i++ {
		rest = append(rest, btBar(i*5, 1234, 1240, 1230, 1235))
	}
	bars := btBars(btBar(0, 1230, 1240, 1225, 1234), rest...)
	o := evalFirst(t, []Event{btEvent(DirUp, 1234, 12.3)}, bars, "JM", Params{HoldBars: 6, StopATR: 1, RR: 1.5})
	stop, tp := RecommendPrices("JM", DirUp, 1234, 12.3, 1, 1.5)
	if o.StopPrice != stop || o.TPPrice != tp {
		t.Fatalf("回测与提醒规则不一致：回测 %v/%v，提醒 %v/%v", o.StopPrice, o.TPPrice, stop, tp)
	}
	if o.StopPrice != 1221.5 || o.TPPrice != 1252.5 { // 焦煤 0.5 的整数倍
		t.Fatalf("没按报价单位对齐：%+v", o)
	}
}

func TestEvaluateSkipsSampleWithoutEnoughBars(t *testing.T) {
	// 末尾不足 hold 根 → 不给样本（保持原有行为）
	bars := btBars(btBar(0, 99, 101, 99, 100), btBar(5, 100, 101, 99, 100))
	if out, _ := evaluate([]Event{btEvent(DirUp, 100, 2)}, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5}); len(out) != 0 {
		t.Fatalf("不该给样本：%+v", out)
	}
}

func TestEvaluateRespectsRR(t *testing.T) {
	// 同一段行情（最高 +2% 即 104），盈亏比越大越难止盈
	rest := []Bar{btBar(5, 100, 104, 99, 101)}
	for i := 2; i <= 6; i++ {
		rest = append(rest, btBar(i*5, 101, 101.5, 100.5, 101))
	}
	bars := btBars(btBar(0, 99, 101, 99, 100), rest...)
	ev := []Event{btEvent(DirUp, 100, 2)}

	low := evalFirst(t, ev, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 0.5}) // 止盈 101
	if low.ExitReason != ExitTP || low.ExitPrice != 101 {
		t.Fatalf("RR=0.5 应止盈 101：%+v", low)
	}
	mid := evalFirst(t, ev, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5}) // 止盈 103
	if mid.ExitReason != ExitTP || mid.ExitPrice != 103 {
		t.Fatalf("RR=1.5 应止盈 103：%+v", mid)
	}
	high := evalFirst(t, ev, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 3}) // 止盈 106 够不到 → 持有到期
	if high.ExitReason != ExitHold || high.TPPrice != 106 {
		t.Fatalf("RR=3 应够不到止盈：%+v", high)
	}
}

func TestFillStatsMetrics(t *testing.T) {
	res := Result{}
	fillStats(&res, []Outcome{
		{Return: 0.03, Correct: true, ExitReason: ExitTP, R: 1.5},
		{Return: -0.02, Correct: false, ExitReason: ExitStop, R: -1},
		{Return: 0.01, Correct: true, ExitReason: ExitHold, R: 0.5},
	})

	if res.Trades != 3 || res.Correct != 2 {
		t.Fatalf("样本/正确不对：%+v", res)
	}
	if math.Abs(res.WinRate-2.0/3.0) > 1e-9 {
		t.Fatalf("胜率不对：%v", res.WinRate)
	}
	if res.StopExits != 1 || res.TPExits != 1 || res.HoldExits != 1 {
		t.Fatalf("出场分布不对：%+v", res)
	}
	// 盈利因子 = 总盈利 / |总亏损|
	if math.Abs(res.ProfitFactor-0.04/0.02) > 1e-9 {
		t.Fatalf("盈利因子不对：%v", res.ProfitFactor)
	}
	if math.Abs(res.AvgWin-0.02) > 1e-9 || math.Abs(res.AvgLoss+0.02) > 1e-9 {
		t.Fatalf("平均盈亏不对：%v %v", res.AvgWin, res.AvgLoss)
	}
	if math.Abs(res.AvgR-1.0/3.0) > 1e-9 { // (1.5 − 1 + 0.5) / 3
		t.Fatalf("期望 R 不对：%v", res.AvgR)
	}
	if math.Abs(res.AvgReturn-0.02/3) > 1e-9 {
		t.Fatalf("平均收益不对：%v", res.AvgReturn)
	}
}

func TestFillStatsWithoutLosses(t *testing.T) {
	// 没有亏损单：盈利因子不能是 NaN/Inf（JSON 会编码失败）
	res := Result{}
	fillStats(&res, []Outcome{{Return: 0.03, Correct: true, ExitReason: ExitTP, R: 1.5}})
	if math.IsInf(res.ProfitFactor, 0) || math.IsNaN(res.ProfitFactor) {
		t.Fatalf("盈利因子不能是 Inf/NaN：%v", res.ProfitFactor)
	}
	if res.ProfitFactor != 0 {
		t.Fatalf("没有亏损时按 0 处理（前端显示「-」）：%v", res.ProfitFactor)
	}
	res = Result{}
	fillStats(&res, nil)
	if res.Trades != 0 || res.WinRate != 0 || res.ProfitFactor != 0 || res.AvgR != 0 {
		t.Fatalf("空样本应全 0：%+v", res)
	}
}
