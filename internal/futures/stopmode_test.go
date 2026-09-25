package futures

import (
	"testing"
)

func pvIn(prefix, dir string, entry, prevLow, prevHigh, points, rr float64) StopInput {
	return StopInput{
		Prefix: prefix, Direction: dir, Entry: entry,
		PrevLow: prevLow, PrevHigh: prevHigh,
		StopMode: StopModePrevLow, StopPoints: points, RR: rr,
	}
}

// 新策略：做多止损 = 信号那根的前一根最低 − 点数；做空 = 前一根最高 + 点数。
func TestRecommendStopPrevLow(t *testing.T) {
	// 焦煤报价单位 0.5：前低 1220 → 止损 1219，止损距离 15，止盈 1234+15×1.5
	long := pvIn("JM", DirUp, 1234, 1220, 1232, 1, 1.5)
	stop, tp := RecommendStop(long)
	if stop != 1219 {
		t.Fatalf("做多止损应为前低−1 点：%v", stop)
	}
	if tp != 1256.5 {
		t.Fatalf("止盈 = 实际止损距离 × 盈亏比：%v", tp)
	}

	// 2 点缓冲
	long2 := long
	long2.StopPoints = 2
	if stop, _ := RecommendStop(long2); stop != 1218 {
		t.Fatalf("2 点缓冲：%v", stop)
	}

	// 做空：止损 = 前高 + 点数
	short := pvIn("JM", DirDown, 1210, 1200, 1212, 1, 1.5)
	stop, tp = RecommendStop(short)
	if stop != 1213 {
		t.Fatalf("做空止损应为前高+1 点：%v", stop)
	}
	if tp != 1205.5 { // 1210 − 3×1.5
		t.Fatalf("做空止盈：%v", tp)
	}
}

// 结果按品种最小变动价位对齐（黄金 0.02 / 螺纹钢 1）
func TestRecommendStopPrevLowAlignsToTick(t *testing.T) {
	au := pvIn("AU", DirUp, 623.5, 623.44, 623.9, 1, 1)
	if stop, _ := RecommendStop(au); stop != 622.44 {
		t.Fatalf("黄金：623.44−1=622.44（0.02 的倍数）：%v", stop)
	}
	rb := pvIn("RB", DirUp, 3420, 3416.3, 3418, 1, 1)
	if stop, _ := RecommendStop(rb); stop != 3415 {
		t.Fatalf("螺纹钢：3416.3−1=3415.3 → 对齐 1 → 3415：%v", stop)
	}
}

// 前低离入场太近（甚至高于入场）时，止损保底离入场 1 个跳，止盈同步缩短
func TestRecommendStopPrevLowOneTickFloor(t *testing.T) {
	in := pvIn("RB", DirUp, 3420, 3421, 3422, 1, 2) // 前低 3421 > 入场 3420
	stop, tp := RecommendStop(in)
	if stop != 3419 {
		t.Fatalf("止损应保底到入场下方 1 个跳：%v", stop)
	}
	if tp != 3422 { // 距离 1 × 2
		t.Fatalf("止盈应跟着实际距离走：%v", tp)
	}
	// 做空方向同理
	sh := pvIn("RB", DirDown, 3420, 3418, 3419, 1, 1)
	stop, _ = RecommendStop(sh)
	if stop != 3421 {
		t.Fatalf("做空止损保底到入场上方 1 个跳：%v", stop)
	}
}

// 前一根数据缺失（首根 K 线）→ 退回 ATR 模式，不能因此把这一笔丢掉
func TestRecommendStopPrevLowFallsBackToATR(t *testing.T) {
	in := StopInput{
		Prefix: "RB", Direction: DirUp, Entry: 3420, ATR: 10,
		StopMode: StopModePrevLow, StopATR: 1, RR: 1,
	}
	stop, tp := RecommendStop(in)
	if stop != 3410 || tp != 3430 {
		t.Fatalf("缺前低时应退回 1×ATR：stop=%v tp=%v", stop, tp)
	}
}

// ATR 模式行为不变（回归）
func TestRecommendStopATRMode(t *testing.T) {
	in := StopInput{Prefix: "RB", Direction: DirUp, Entry: 100, ATR: 4, StopMode: StopModeATR, StopATR: 1, RR: 1.5}
	stop, tp := RecommendStop(in)
	if stop != 96 || tp != 106 {
		t.Fatalf("1×ATR：stop=%v tp=%v", stop, tp)
	}
}

// 参数默认与归一化：未知模式回落 atr，点数缺省 1
func TestParamsStopModeDefaults(t *testing.T) {
	d := DefaultParams()
	if d.StopMode != StopModeATR || d.StopPoints != DefaultStopPoints {
		t.Fatalf("默认应为 atr / %v：%+v", DefaultStopPoints, d)
	}
	m := mergeParams(Params{StopMode: "bogus", StopPoints: 0})
	if m.StopMode != StopModeATR {
		t.Fatalf("未知止损模式应回落 atr：%q", m.StopMode)
	}
	if m.StopPoints != DefaultStopPoints {
		t.Fatalf("点数缺省应为 %v：%v", DefaultStopPoints, m.StopPoints)
	}
	if m2 := mergeParams(Params{StopMode: StopModePrevLow, StopPoints: 2}); m2.StopMode != StopModePrevLow || m2.StopPoints != 2 {
		t.Fatalf("显式填写不该被改：%+v", m2)
	}
	if !IsValidStopMode(StopModePrevLow) || !IsValidStopMode(StopModeATR) || IsValidStopMode("x") {
		t.Fatal("模式白名单不对")
	}
}

// 突破事件要带上「前一根」的高低价，前低止损才有依据
func TestScanCarriesPrevBar(t *testing.T) {
	bars, daily := rangeFixture()
	days := realSessionDays(bars)
	var breakout *Event
	ev := ScanTimeframe(bars, days[0], PivotLevels(daily, days[0]), Params{Period: "5", ORB: 30, Donchian: 50, VolRatio: 1.5})
	for i := range ev {
		if ev[i].Close == 112 {
			breakout = &ev[i]
		}
	}
	if breakout == nil {
		t.Fatal("没扫到突破根")
	}
	// 前一根是 09:25 那根（开 90 / 高 100 / 低 89 / 收 99）
	if breakout.PrevLow != 89 || breakout.PrevHigh != 100 {
		t.Fatalf("事件应带前一根高低：%+v", breakout)
	}
}

// 同一段行情，两种止损给出不同结果（这就是对照的意义）
func TestEvaluateStopModePrevLowVsATR(t *testing.T) {
	prev := btBar(-5, 96, 97, 96, 96.5)
	entry := btBar(0, 99, 101, 99, 100)
	// 之后回落到 97.5：会打掉 1×ATR 的 98，但够不到前低止损 95
	rest := []Bar{btBar(5, 99, 100, 97.5, 98), btBar(10, 98, 99, 97, 98.5), btBar(15, 98.5, 99, 98, 98.8)}
	bars := btBars(prev, entry)
	bars = append(bars, rest...)
	ev := []Event{{
		Time: entry.Time, Direction: DirUp, Level: "ORB高(开盘30分钟)",
		Close: 100, LevelPrice: 99, ATR: 2, PrevLow: 96, PrevHigh: 97,
	}}

	atrOut, _ := evaluate(ev, bars, "RB", Params{HoldBars: 3, StopATR: 1, RR: 1.5, StopMode: StopModeATR})
	atr := atrOut[0]
	if atr.StopPrice != 98 || atr.ExitReason != ExitStop {
		t.Fatalf("ATR 止损应被打掉：%+v", atr)
	}

	pvOut, _ := evaluate(ev, bars, "RB", Params{HoldBars: 3, StopATR: 1, RR: 1.5, StopMode: StopModePrevLow, StopPoints: 1})
	pv := pvOut[0]
	if pv.StopPrice != 95 {
		t.Fatalf("前低止损应为 96−1=95：%+v", pv)
	}
	if pv.ExitReason == ExitStop {
		t.Fatalf("前低止损不该被打掉：%+v", pv)
	}
	if pv.StopMode != StopModePrevLow || pv.StopPoints != 1 {
		t.Fatalf("出场明细要能看出用的哪种止损：%+v", pv)
	}
}

// 扫描：止损方式作为一根轴 → 一次跑出两种策略直接对照
func TestSweepStopModeAxis(t *testing.T) {
	bars, daily := rangeFixture()
	base := SweepRequest{
		Symbol: "RB0", Period: "5", Donchian: []int{50}, VolRatio: []float64{1.5},
		HoldBars: []int{6}, RR: []float64{1.5}, MinTrades: 1, Objective: SweepObjectiveAvgReturn,
	}
	if got := SweepBars(bars, daily, base).Combos; got != 1 {
		t.Fatalf("单组合基线：%d", got)
	}

	two := base
	two.StopModes = []string{StopModeATR, StopModePrevLow}
	res := SweepBars(bars, daily, two)
	if res.Combos != 2 {
		t.Fatalf("两种止损方式应 2 个组合：%d", res.Combos)
	}
	modes := map[string]bool{}
	for _, r := range res.Rows {
		modes[r.Params.StopMode] = true
	}
	if len(modes) != 2 {
		t.Fatalf("每行应带自己的止损方式：%v", modes)
	}

	// 再加点数轴 → 组合数相乘
	three := two
	three.StopPoints = []float64{1, 2}
	if got := SweepBars(bars, daily, three).Combos; got != 4 {
		t.Fatalf("止损方式 × 点数 = 4 个组合：%d", got)
	}

	// 非法模式要报错，不能静默当 atr
	if _, err := normalizeSweep(SweepRequest{StopModes: []string{"bogus"}}); err == nil {
		t.Fatal("非法止损方式应报错")
	}
	if _, err := normalizeSweep(SweepRequest{StopModes: []string{"", StopModePrevLow}}); err != nil {
		t.Fatalf("空值应回落默认而不是报错：%v", err)
	}
}
