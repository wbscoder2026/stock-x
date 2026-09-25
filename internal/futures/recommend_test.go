package futures

import (
	"math"
	"testing"
	"time"
)

func TestRecommendPrices(t *testing.T) {
	// 做多：止损 = 现价 − 1×ATR；止盈 = 现价 + 1×ATR×RR（用螺纹钢：报价单位 1，便于看整数）
	stop, tp := RecommendStop(StopInput{Prefix: "RB", Direction: DirUp, Entry: 100, ATR: 4, StopATR: 1, RR: 1.5, StopMode: StopModeATR})
	if stop != 96 || tp != 106 {
		t.Fatalf("做多推荐不对：stop=%v tp=%v", stop, tp)
	}

	// 做空镜像
	stop, tp = RecommendStop(StopInput{Prefix: "RB", Direction: DirDown, Entry: 100, ATR: 4, StopATR: 1, RR: 1.5, StopMode: StopModeATR})
	if stop != 104 || tp != 94 {
		t.Fatalf("做空推荐不对：stop=%v tp=%v", stop, tp)
	}

	// 盈亏比没给 → 用默认 1.5
	_, tp = RecommendStop(StopInput{Prefix: "RB", Direction: DirUp, Entry: 100, ATR: 4, StopATR: 1, RR: 0, StopMode: StopModeATR})
	if tp != 106 {
		t.Fatalf("默认盈亏比应为 %v：tp=%v", DefaultRR, tp)
	}

	// 自定义盈亏比 = 3
	_, tp = RecommendStop(StopInput{Prefix: "RB", Direction: DirUp, Entry: 100, ATR: 4, StopATR: 1, RR: 3, StopMode: StopModeATR})
	if tp != 112 {
		t.Fatalf("RR=3 应为 112：tp=%v", tp)
	}

	// ATR 缺失/异常 → 不给建议，且绝不能漏出 NaN（NaN 会让 JSON 编码整个失败）
	for _, atr := range []float64{0, -1, math.NaN()} {
		if s, p := RecommendStop(StopInput{Prefix: "RB", Direction: DirUp, Entry: 100, ATR: atr, StopATR: 1, RR: 1.5, StopMode: StopModeATR}); s != 0 || p != 0 {
			t.Fatalf("atr=%v 应返回 0：stop=%v tp=%v", atr, s, p)
		}
	}
	if s, p := RecommendStop(StopInput{Prefix: "RB", Direction: DirUp, Entry: 0, ATR: 4, StopATR: 1, RR: 1.5, StopMode: StopModeATR}); s != 0 || p != 0 {
		t.Fatalf("没价格应返回 0：stop=%v tp=%v", s, p)
	}

	// 负盈亏比按默认处理，不倒挂
	stop, tp = RecommendStop(StopInput{Prefix: "RB", Direction: DirUp, Entry: 100, ATR: 4, StopATR: 1, RR: -2, StopMode: StopModeATR})
	if stop != 96 || tp != 106 {
		t.Fatalf("负盈亏比应按默认：stop=%v tp=%v", stop, tp)
	}
}

func TestCollectNewRecommendsStopAndTakeProfit(t *testing.T) {
	v := Variety{Name: "焦煤", Prefix: "JM"}
	t1 := time.Date(2026, 9, 22, 9, 50, 0, 0, locCST)

	got := collectNew(map[string]int64{}, []Event{
		{Time: t1, Direction: DirUp, Level: "ORB高(开盘30分钟)", Close: 100, ATR: 2, Volume: 1000},
	}, time.Time{}, "2026-09-22", v, Params{StopATR: 1, RR: 1.5})
	if len(got) != 1 {
		t.Fatalf("应有 1 条：%+v", got)
	}
	e := got[0]
	if e.StopPrice != 98 || e.TPPrice != 103 || e.RR != 1.5 {
		t.Fatalf("做多推荐不对 %+v", e)
	}

	// 盈亏比可配
	got = collectNew(map[string]int64{}, []Event{
		{Time: t1, Direction: DirUp, Level: "ORB高(开盘30分钟)", Close: 100, ATR: 2},
	}, time.Time{}, "2026-09-22", v, Params{StopATR: 1, RR: 2})
	if got[0].TPPrice != 104 || got[0].RR != 2 {
		t.Fatalf("RR=2 推荐不对 %+v", got[0])
	}

	// 向下突破镜像
	got = collectNew(map[string]int64{}, []Event{
		{Time: t1, Direction: DirDown, Level: "ORB低(开盘30分钟)", Close: 100, ATR: 2},
	}, time.Time{}, "2026-09-22", v, Params{StopATR: 1, RR: 1.5})
	if got[0].StopPrice != 102 || got[0].TPPrice != 97 {
		t.Fatalf("做空推荐不对 %+v", got[0])
	}

	// ATR 缺失（数据不足）→ 价格为 0，前端显示「-」，但 RR / 报价单位仍带上
	got = collectNew(map[string]int64{}, []Event{
		{Time: t1, Direction: DirUp, Level: "PDH(昨高)", Close: 100},
	}, time.Time{}, "2026-09-22", v, Params{StopATR: 1, RR: 1.5})
	if got[0].StopPrice != 0 || got[0].TPPrice != 0 || got[0].RR != 1.5 || got[0].TickSize != 0.5 {
		t.Fatalf("无 ATR 时应给 0 %+v", got[0])
	}
}
