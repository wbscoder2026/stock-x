package futures

import (
	"math"
	"testing"
	"time"
)

func TestTickSizeKnownVarieties(t *testing.T) {
	cases := map[string]float64{
		"JM": 0.5,   // 焦煤：0.5 元/吨
		"I":  0.5,   // 铁矿
		"J":  0.5,   // 焦炭
		"RB": 1,     // 螺纹钢：1 元/吨
		"HC": 1,     // 热卷
		"M":  1,     // 豆粕
		"Y":  2,     // 豆油
		"CU": 10,    // 铜
		"AL": 5,     // 铝
		"NI": 10,    // 镍
		"AU": 0.02,  // 黄金
		"AG": 1,     // 白银
		"SC": 0.1,   // 原油
		"IF": 0.2,   // 沪深300
		"TS": 0.002, // 二年国债
		"LC": 20,    // 碳酸锂
		"SI": 5,     // 工业硅
		"CF": 5,     // 棉花
		"ZC": 0.2,   // 动力煤
		"TA": 2,     // PTA
		"LH": 5,     // 生猪
		"SP": 2,     // 纸浆
	}
	for prefix, want := range cases {
		if got := TickSize(prefix); got != want {
			t.Fatalf("%s 最小变动价位应为 %v，实际 %v", prefix, want, got)
		}
	}
}

func TestEveryVarietyHasTickSize(t *testing.T) {
	// 品种池里每个品种都要显式登记报价单位：漏登记会静默回落成 1，
	// 对焦煤/黄金这类非整数报价的品种就会给出挂不出去的价格。
	for _, v := range ListVarieties() {
		if _, ok := tickSizes[v.Prefix]; !ok {
			t.Fatalf("%s(%s) 没登记最小变动价位", v.Name, v.Prefix)
		}
	}
}

func TestTickSizeFallback(t *testing.T) {
	if got := TickSize("ZZ"); got != 1 {
		t.Fatalf("未知品种应回落 1：%v", got)
	}
	if got := TickSize(""); got != 1 {
		t.Fatalf("空品种应回落 1：%v", got)
	}
	if got := TickSize(" jm "); got != 0.5 {
		t.Fatalf("应容错大小写与空格：%v", got)
	}
}

func TestRoundToTick(t *testing.T) {
	cases := []struct{ price, tick, want float64 }{
		{1221.7, 0.5, 1221.5}, // 焦煤：0.5 的整数倍
		{1221.75, 0.5, 1222},  // 正好半个跳 → 向上进位
		{3456.4, 1, 3456},     // 螺纹钢：整数
		{623.447, 0.02, 623.44},
		{62531.2, 20, 62540}, // 碳酸锂
		{100.0000001, 1, 100},
	}
	for _, c := range cases {
		if got := RoundToTick(c.price, c.tick); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("RoundToTick(%v, %v) = %v，期望 %v", c.price, c.tick, got, c.want)
		}
	}

	// 浮点残渣要消掉：0.1×3 不能给出 0.30000000000000004
	if got := RoundToTick(0.30000000000000004, 0.1); got != 0.3 {
		t.Fatalf("浮点残渣没消掉：%v", got)
	}

	// 非法 tick → 原样返回（宁可不取整，也不要算出错价）
	for _, tick := range []float64{0, -1, math.NaN()} {
		if got := RoundToTick(1221.7, tick); got != 1221.7 {
			t.Fatalf("tick=%v 应原样返回：%v", tick, got)
		}
	}
}

func TestFormatPrice(t *testing.T) {
	cases := []struct {
		price, tick float64
		want        string
	}{
		{3416, 1, "3416"},       // 螺纹钢：整数
		{1221.5, 0.5, "1221.5"}, // 焦煤：1 位小数
		{623.44, 0.02, "623.44"},
		{62135, 20, "62135"}, // 碳酸锂
		{100.5, 0, "100.5"},  // 未知报价单位 → 1 位，不崩
	}
	for _, c := range cases {
		if got := FormatPrice(c.price, c.tick); got != c.want {
			t.Fatalf("FormatPrice(%v, %v) = %q，期望 %q", c.price, c.tick, got, c.want)
		}
	}
	if got := TickDecimals(0.002); got != 3 {
		t.Fatalf("TS 报价单位小数位应为 3：%d", got)
	}
}

func TestRecommendPricesAlignsToVarietyTick(t *testing.T) {
	// 焦煤 tick=0.5：现价 1234，ATR 12.3，盈亏比 1.5
	// 止损 = 1234 − 12.3 = 1221.7 → 1221.5；止盈 = 1234 + 18.45 = 1252.45 → 1252.5
	stop, tp := RecommendPrices("JM", DirUp, 1234, 12.3, 1, 1.5)
	if stop != 1221.5 || tp != 1252.5 {
		t.Fatalf("焦煤推荐价没对齐 0.5：stop=%v tp=%v", stop, tp)
	}

	// 螺纹钢 tick=1：现价 3456，ATR 40 → 止损 3416，止盈 3516（整数）
	stop, tp = RecommendPrices("RB", DirUp, 3456, 40, 1, 1.5)
	if stop != 3416 || tp != 3516 {
		t.Fatalf("螺纹钢推荐价应为整数：stop=%v tp=%v", stop, tp)
	}

	// 黄金 tick=0.02：结果必须落在 0.02 的整数倍上
	stop, tp = RecommendPrices("AU", DirUp, 623.437, 5.016, 1, 1.5)
	for _, v := range []float64{stop, tp} {
		if math.Abs(v/0.02-math.Round(v/0.02)) > 1e-6 {
			t.Fatalf("黄金推荐价没对齐 0.02：stop=%v tp=%v", stop, tp)
		}
	}

	// 向下突破同样对齐
	stop, tp = RecommendPrices("JM", DirDown, 1234, 12.3, 1, 1.5)
	if stop != 1246.5 || tp != 1215.5 {
		t.Fatalf("做空推荐价没对齐：stop=%v tp=%v", stop, tp)
	}
}

func TestRecommendPricesKeepsOneTickDistance(t *testing.T) {
	// ATR 比一个跳还小时（极端周期/异常数据），止损止盈至少要离入场价 1 个跳，
	// 否则取整会把止损取到入场价上（等于没有止损）。
	stop, tp := RecommendPrices("RB", DirUp, 100, 0.1, 1, 1.5)
	if stop != 99 || tp != 101 {
		t.Fatalf("应保底 1 个跳：stop=%v tp=%v", stop, tp)
	}
	stop, tp = RecommendPrices("JM", DirDown, 100, 0.1, 1, 1.5)
	if stop != 100.5 || tp != 99.5 {
		t.Fatalf("做空也应保底 1 个跳：stop=%v tp=%v", stop, tp)
	}
}

func TestCollectNewCarriesTickSize(t *testing.T) {
	v := Variety{Name: "焦煤", Prefix: "JM"}
	ts := time.Date(2026, 9, 22, 9, 50, 0, 0, locCST)
	got := collectNew(map[string]int64{}, []Event{
		{Time: ts, Direction: DirUp, Level: "ORB高(开盘30分钟)", Close: 1234, ATR: 12.3},
	}, time.Time{}, "2026-09-22", v, 1, 1.5)
	if len(got) != 1 {
		t.Fatalf("应有 1 条：%+v", got)
	}
	if got[0].TickSize != 0.5 {
		t.Fatalf("事件应带上品种最小变动价位：%+v", got[0])
	}
	if got[0].StopPrice != 1221.5 || got[0].TPPrice != 1252.5 {
		t.Fatalf("价格应已对齐：%+v", got[0])
	}
}
