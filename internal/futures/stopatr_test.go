package futures

import (
	"math"
	"testing"
	"time"
)

func TestParamsDefaultStopATR(t *testing.T) {
	if got := DefaultParams().StopATR; got != DefaultStopATR {
		t.Fatalf("默认止损倍数应为 %v，实际 %v", DefaultStopATR, got)
	}
	// 不填 / 非法值 → 回落默认（不然止损变 0，回测会退化成纯持有）
	for _, v := range []float64{0, -1, math.NaN()} {
		if got := mergeParams(Params{StopATR: v}).StopATR; got != DefaultStopATR {
			t.Fatalf("StopATR=%v 应回落默认，实际 %v", v, got)
		}
	}
	if got := mergeParams(Params{StopATR: 0.5}).StopATR; got != 0.5 {
		t.Fatalf("显式值应保留：%v", got)
	}
}

func TestRecommendPricesWithStopMultiple(t *testing.T) {
	// 螺纹钢（报价单位 1）：现价 100、ATR 4
	// 止损 = 现价 − stopATR×ATR；止盈距离 = stopATR×ATR×盈亏比
	stop, tp := RecommendPrices("RB", DirUp, 100, 4, 0.5, 1.5)
	if stop != 98 || tp != 103 {
		t.Fatalf("0.5×ATR 止损不对：stop=%v tp=%v", stop, tp)
	}
	stop, tp = RecommendPrices("RB", DirUp, 100, 4, 1, 1.5)
	if stop != 96 || tp != 106 {
		t.Fatalf("1×ATR 止损不对：stop=%v tp=%v", stop, tp)
	}
	stop, tp = RecommendPrices("RB", DirUp, 100, 4, 2, 1.5)
	if stop != 92 || tp != 112 {
		t.Fatalf("2×ATR 止损不对：stop=%v tp=%v", stop, tp)
	}
	// 做空镜像
	stop, tp = RecommendPrices("RB", DirDown, 100, 4, 0.5, 2)
	if stop != 102 || tp != 96 {
		t.Fatalf("做空 0.5×ATR 不对：stop=%v tp=%v", stop, tp)
	}
	// 倍数非法 → 按默认 1×ATR
	stop, tp = RecommendPrices("RB", DirUp, 100, 4, 0, 1.5)
	if stop != 96 || tp != 106 {
		t.Fatalf("非法倍数应回落 1×ATR：stop=%v tp=%v", stop, tp)
	}
	// 仍然要对齐报价单位（焦煤 0.5）：1234−6.15=1227.85→1228；1234+9.225=1243.225→1243
	stop, tp = RecommendPrices("JM", DirUp, 1234, 12.3, 0.5, 1.5)
	if stop != 1228 || tp != 1243 {
		t.Fatalf("焦煤 0.5×ATR 没对齐：stop=%v tp=%v", stop, tp)
	}
}

func TestMergeParamsRejectsNaN(t *testing.T) {
	// NaN ≤ 0 为 false，不显式挡会一路漏到价格里 → JSON 编码失败
	p := mergeParams(Params{ATRK: math.NaN(), VolRatio: math.NaN(), RR: math.NaN(), StopATR: math.NaN()})
	d := DefaultParams()
	if p.ATRK != d.ATRK || p.VolRatio != d.VolRatio || p.RR != d.RR || p.StopATR != d.StopATR {
		t.Fatalf("NaN 应全部回落默认值：%+v", p)
	}
}

func TestEvaluateTighterStopStopsOutEarlier(t *testing.T) {
	// 入场 100、ATR 4：价格只回撤 3（没到 1×ATR 的 96，但破了 0.5×ATR 的 98）
	bars := btBars(
		btBar(0, 99, 101, 99, 100),
		btBar(5, 100, 100.5, 97, 98.5),
		btBar(10, 98.5, 99, 98, 98.6),
		btBar(15, 98.6, 99, 98, 98.6),
		btBar(20, 98.6, 99, 98, 98.6),
		btBar(25, 98.6, 99, 98, 98.6),
		btBar(30, 98.6, 99, 98, 98.6),
	)
	ev := []Event{btEvent(DirUp, 100, 4)}

	loose := evalFirst(t, ev, bars, "RB", Params{HoldBars: 6, StopATR: 1, RR: 1.5}) // 止损 96
	if loose.ExitReason == ExitStop {
		t.Fatalf("1×ATR 不该被打掉：%+v", loose)
	}
	tight := evalFirst(t, ev, bars, "RB", Params{HoldBars: 6, StopATR: 0.5, RR: 1.5}) // 止损 98
	if tight.ExitReason != ExitStop || tight.ExitPrice != 98 {
		t.Fatalf("0.5×ATR 应在 98 止损：%+v", tight)
	}
	if math.Abs(tight.R+1) > 1e-9 {
		t.Fatalf("止损应 −1R（R 按新的风险归一）：%+v", tight)
	}
	if tight.StopATR != 0.5 {
		t.Fatalf("出场记录里要带倍数：%+v", tight)
	}
}

func TestSweepStopATRAxis(t *testing.T) {
	minutes, daily := sweepFixture()
	res := SweepBars(minutes, daily, SweepRequest{
		Symbol: "RB0", Period: "5",
		Donchian: []int{50}, VolRatio: []float64{1.5}, HoldBars: []int{6}, RR: []float64{1.5},
		StopATR:   []float64{0.5, 1, 1.5},
		Objective: SweepObjectiveAvgR, MinTrades: 1,
	})
	if res.Combos != 3 || len(res.Rows) != 3 {
		t.Fatalf("止损倍数应成为一个轴：%+v", res)
	}
	seen := map[float64]bool{}
	for _, r := range res.Rows {
		seen[r.Params.StopATR] = true
	}
	if len(seen) != 3 {
		t.Fatalf("三档倍数都要出现：%v", seen)
	}
}

func TestCollectNewCarriesStopATR(t *testing.T) {
	v := Variety{Name: "螺纹钢", Prefix: "RB"}
	ts0 := time.Date(2026, 9, 22, 9, 50, 0, 0, locCST)
	got := collectNew(map[string]int64{}, []Event{
		{Time: ts0, Direction: DirUp, Level: "PDH(昨高)", Close: 3000, ATR: 20},
	}, time.Time{}, "2026-09-22", v, 0.5, 1.5)
	if len(got) != 1 {
		t.Fatalf("应有 1 条：%+v", got)
	}
	if got[0].StopATR != 0.5 || got[0].StopPrice != 2990 || got[0].TPPrice != 3015 {
		t.Fatalf("提醒要按倍数算推荐价：%+v", got[0])
	}
}
