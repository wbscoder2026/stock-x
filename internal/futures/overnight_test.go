package futures

import (
	"testing"
	"time"
)

// 跨日/含夜盘的 K 线：day1 日盘 10:00 → 14:00（当日日盘最后一根）→ 21:05（夜盘，属次日交易时段）
// → day2 日盘 09:00 直接高开大涨（隔夜才吃得到）。
func overnightBars() []Bar {
	loc := locCST
	return []Bar{
		{Time: time.Date(2026, 9, 22, 10, 0, 0, 0, loc), Open: 99, High: 101, Low: 99, Close: 100, Volume: 1000},
		{Time: time.Date(2026, 9, 22, 13, 0, 0, 0, loc), Open: 100, High: 101, Low: 99.5, Close: 100.5, Volume: 800},
		{Time: time.Date(2026, 9, 22, 14, 0, 0, 0, loc), Open: 100.5, High: 101, Low: 100, Close: 100.5, Volume: 900},
		{Time: time.Date(2026, 9, 22, 21, 5, 0, 0, loc), Open: 100.5, High: 112, Low: 100, Close: 111, Volume: 1200},
		{Time: time.Date(2026, 9, 23, 9, 0, 0, 0, loc), Open: 111, High: 121, Low: 110, Close: 120, Volume: 1500},
	}
}

func overnightEvent() Event {
	return Event{
		Time: time.Date(2026, 9, 22, 10, 0, 0, 0, locCST), Direction: DirUp,
		Level: "PDH(昨高)", Close: 100, ATR: 4, Volume: 1000, LevelPrice: 99,
	}
}

func TestNoOvernightExitsAtDayClose(t *testing.T) {
	bars := overnightBars()
	// 允许隔夜（默认）：持有 3 根 → 出场在 21:05 夜盘那根（日内模式下这是不该碰的时候）
	allow, _ := evaluate([]Event{overnightEvent()}, bars, "RB", Params{HoldBars: 3, StopATR: 1, RR: 1.5})
	if len(allow) != 1 {
		t.Fatalf("允许隔夜应有 1 笔：%+v", allow)
	}
	if allow[0].ExitTime != "2026-09-22 21:05" {
		t.Fatalf("允许隔夜应持有到夜盘：%+v", allow[0])
	}

	// 禁止隔夜：必须在当日日盘最后一根（14:00）收盘平掉，吃不到夜盘/次日的涨
	noOvernight, skipped := evaluate([]Event{overnightEvent()}, bars, "RB",
		Params{HoldBars: 2, StopATR: 1, RR: 1.5, NoOvernight: true})
	if skipped != 0 {
		t.Fatalf("不该跳过：%d", skipped)
	}
	if len(noOvernight) != 1 {
		t.Fatalf("应有 1 笔：%+v", noOvernight)
	}
	o := noOvernight[0]
	if o.ExitReason != ExitEOD || o.ExitTime != "2026-09-22 14:00" {
		t.Fatalf("应日内收盘平仓：%+v", o)
	}
	if o.ExitPrice != 100.5 {
		t.Fatalf("出场价应为当日日盘收盘 100.5：%+v", o)
	}
	// 关键：不能吃到次日 120 的跳空（这正是"不隔夜"要规避的风险）
	if o.Return > 0.01 {
		t.Fatalf("禁止隔夜不该吃到次日涨幅：%+v", o)
	}

	// 持有更久也一样：日内的终点是当日日盘收盘，不是 N 根之后
	longer, _ := evaluate([]Event{overnightEvent()}, bars, "RB",
		Params{HoldBars: 4, StopATR: 1, RR: 1.5, NoOvernight: true})
	if longer[0].ExitTime != "2026-09-22 14:00" || longer[0].ExitReason != ExitEOD {
		t.Fatalf("持有根数再多也不该跨日：%+v", longer[0])
	}
}

func TestNoOvernightStillHonorsStopBeforeClose(t *testing.T) {
	bars := overnightBars()
	bars[1].Low = 90 // 13:00 那根直接打穿止损 96
	out, _ := evaluate([]Event{overnightEvent()}, bars, "RB",
		Params{HoldBars: 2, StopATR: 1, RR: 1.5, NoOvernight: true})
	if out[0].ExitReason != ExitStop || out[0].ExitPrice != 96 {
		t.Fatalf("日内模式也要先认止损：%+v", out[0])
	}
}

func TestNoOvernightSkipsUntradeableSignals(t *testing.T) {
	bars := overnightBars()
	// 入场就在当日日盘最后一根 → 日内策略没法进也不能立刻走
	last := Event{Time: bars[2].Time, Direction: DirUp, Level: "PDH(昨高)", Close: 100.5, ATR: 4}
	// 入场在夜盘 → 当日的日盘已经结束
	night := Event{Time: bars[3].Time, Direction: DirUp, Level: "PDH(昨高)", Close: 111, ATR: 4}
	out, skipped := evaluate([]Event{last, night}, bars, "RB",
		Params{HoldBars: 2, StopATR: 1, RR: 1.5, NoOvernight: true})
	if len(out) != 0 || skipped != 2 {
		t.Fatalf("不可交易的信号应跳过并计数：out=%d skipped=%d", len(out), skipped)
	}
	// 允许隔夜时它们照常成为样本
	allow, allowSkipped := evaluate([]Event{last, night}, bars, "RB", Params{HoldBars: 2, StopATR: 1, RR: 1.5})
	if len(allow) != 2 || allowSkipped != 0 {
		t.Fatalf("允许隔夜不该跳过：out=%d skipped=%d", len(allow), allowSkipped)
	}
}

func TestBacktestCountsNoOvernight(t *testing.T) {
	// 结果里要能看到「日内收盘」出场数与「因禁止隔夜跳过」的样本数
	items := []Outcome{
		{Return: 0.01, Correct: true, ExitReason: ExitEOD, R: 0.5},
		{Return: -0.02, Correct: false, ExitReason: ExitStop, R: -1},
	}
	res := Result{}
	fillStats(&res, items)
	if res.EODExits != 1 {
		t.Fatalf("日内收盘出场数不对：%+v", res)
	}
	if res.StopExits != 1 || res.TPExits != 0 || res.HoldExits != 0 {
		t.Fatalf("其它出场数不对：%+v", res)
	}
}

func TestSweepNoOvernightAxis(t *testing.T) {
	minutes, daily := sweepFixture()
	res := SweepBars(minutes, daily, SweepRequest{
		Symbol: "RB0", Period: "5", Donchian: []int{50}, VolRatio: []float64{1.5},
		HoldBars: []int{6}, RR: []float64{1.5}, NoOvernight: []int{0, 1},
		Objective: SweepObjectiveAvgReturn, MinTrades: 1,
	})
	if res.Combos != 2 || len(res.Rows) != 2 {
		t.Fatalf("隔夜开关应成为一个轴：%+v", res)
	}
	seen := map[bool]bool{}
	for _, r := range res.Rows {
		seen[r.Params.NoOvernight] = true
	}
	if !seen[true] || !seen[false] {
		t.Fatalf("允许/禁止都要出现：%v", seen)
	}
}
