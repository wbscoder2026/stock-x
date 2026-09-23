package futures

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestSweepCombosExpansion(t *testing.T) {
	req := SweepRequest{
		RR:        []float64{2, 1, 1.5, 1}, // 含重复且乱序
		HoldBars:  []int{8, 6},
		Donchian:  nil, // 空轴 → 用默认值（单值）
		Objective: SweepObjectiveAvgReturn,
	}
	p, err := normalizeSweep(req)
	if err != nil {
		t.Fatal(err)
	}
	combos := sweepCombos(p)
	if len(combos) != 3*2*1 {
		t.Fatalf("组合数不对：%d", len(combos))
	}
	// 轴值应升序去重：RR 1 / 1.5 / 2
	seen := map[float64]bool{}
	for _, c := range combos {
		seen[c.RR] = true
		if c.Donchian != DefaultParams().Donchian {
			t.Fatalf("空轴应回落默认值：%+v", c)
		}
	}
	if len(seen) != 3 || !seen[1] || !seen[1.5] || !seen[2] {
		t.Fatalf("RR 轴不对：%v", seen)
	}
}

func TestSweepRejectsTooManyCombos(t *testing.T) {
	// 上限 1000 万：30^5 = 2430 万组要拦住
	thirty := make([]int, 0, 30)
	for i := 1; i <= 30; i++ {
		thirty = append(thirty, i)
	}
	thirtyF := make([]float64, 0, 30)
	for i := 1; i <= 30; i++ {
		thirtyF = append(thirtyF, float64(i))
	}
	req := SweepRequest{HoldBars: thirty, Donchian: thirty, ATRPeriod: thirty, ORB: thirty, ATRK: thirtyF, Objective: SweepObjectiveAvgReturn}
	if _, err := normalizeSweep(req); err == nil {
		t.Fatal("组合数超上限应报错")
	} else if !strings.Contains(err.Error(), "组合数") {
		t.Fatalf("错误信息应说明组合数：%v", err)
	}
}

func TestSweepReportsProgress(t *testing.T) {
	minutes, daily := sweepFixture()
	var mu sync.Mutex
	seen := map[int]int{} // done → total
	var order []int
	req := SweepRequest{
		Symbol: "RB0", Period: "5",
		Donchian: []int{50}, VolRatio: []float64{1.5}, HoldBars: []int{6},
		RR: []float64{1, 2}, StopATR: []float64{1},
		MinTrades: 1, Workers: 2,
	}
	req.OnProgress = func(done, total int) {
		mu.Lock()
		defer mu.Unlock()
		seen[done] = total
		order = append(order, done)
	}
	res := SweepBars(minutes, daily, req)
	if res.Combos != 2 {
		t.Fatalf("组合数应为 2：%d", res.Combos)
	}
	// 开跑前先报一次 total（done=0），前端才能画进度条
	if len(order) == 0 || order[0] != 0 {
		t.Fatalf("第一次回调应该是 done=0：%v", order)
	}
	// 之后每个组合回调一次，done 覆盖 0..2（回调可能在多个 worker 上并发，不保证顺序）
	if len(seen) != 3 {
		t.Fatalf("应回调 3 次（0/1/2）：%v", seen)
	}
	for done := 0; done <= 2; done++ {
		if total, ok := seen[done]; !ok || total != 2 {
			t.Fatalf("done=%d 的 total 不对：%v", done, seen)
		}
	}
}

func TestSweepRejectsUnknownObjective(t *testing.T) {
	if _, err := normalizeSweep(SweepRequest{Objective: "whatever"}); err == nil {
		t.Fatal("未知目标应报错")
	}
	// 空目标回落默认（平均收益）
	p, err := normalizeSweep(SweepRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Objective != SweepObjectiveAvgReturn || p.MinTrades != SweepDefaultMinTrades {
		t.Fatalf("默认值不对：%+v", p)
	}
}

func TestSortSweepRowsByObjective(t *testing.T) {
	rows := []SweepRow{
		{Params: Params{RR: 1}, Trades: 50, WinRate: 0.60, AvgReturn: 0.010, AvgR: 0.10, ProfitFactor: 1.2},
		{Params: Params{RR: 2}, Trades: 50, WinRate: 0.40, AvgReturn: 0.050, AvgR: 0.30, ProfitFactor: 1.9},
		{Params: Params{RR: 3}, Trades: 50, WinRate: 0.30, AvgReturn: 0.020, AvgR: 0.50, ProfitFactor: 1.5},
	}
	cases := map[string]float64{
		SweepObjectiveWinRate:      1,
		SweepObjectiveAvgReturn:    2,
		SweepObjectiveAvgR:         3,
		SweepObjectiveProfitFactor: 2,
	}
	for objective, wantRR := range cases {
		got := sortSweepRows(append([]SweepRow(nil), rows...), objective)
		if got[0].Params.RR != wantRR {
			t.Fatalf("%s 的最优应是 RR=%v：%+v", objective, wantRR, got[0].Params)
		}
	}
}

func TestSortSweepRowsPrefersReliable(t *testing.T) {
	// 3 笔 100% 胜率的组合不该盖过 50 笔的可靠组合
	rows := []SweepRow{
		{Params: Params{RR: 9}, Trades: 3, WinRate: 1.0, AvgReturn: 0.90, Reliable: false},
		{Params: Params{RR: 1.5}, Trades: 50, WinRate: 0.5, AvgReturn: 0.05, Reliable: true},
	}
	got := sortSweepRows(rows, SweepObjectiveWinRate)
	if got[0].Params.RR != 1.5 {
		t.Fatalf("可靠组合应排在前面：%+v", got[0])
	}
}

// 造一段「突破后只走一小段就回头」的行情：盈亏比越小越容易止盈。
func sweepFixture() ([]Bar, []Daily) {
	prev := warmup("2024-06-03", 30)
	day := "2024-06-04"
	today := make([]Bar, 0, 15)
	for i := 0; i < 6; i++ {
		today = append(today, bar(ts(day, 9, 5+i*5), 90, 92, 88, 90, 10000))
	}
	// 突破根：收 112 > PDH 100（ATR ≈ 5.5：止损 106.5，止盈 RR0.5→114.75 / RR1→117.5 / RR1.5→120.25 / RR3→128.5）
	today = append(today, bar(ts(day, 9, 35), 90, 113, 88, 112, 20000))
	// 之后冲到 118 就横住回落：小盈亏比止盈命中，大盈亏比拿不到止盈 → 持有到期
	for i := 1; i <= 8; i++ {
		h := 118.0
		c := 114.0
		if i > 2 {
			h, c = 115.0, 113.0
		}
		today = append(today, bar(ts(day, 9, 35+i*5), 112, h, 111, c, 15000))
	}
	daily := []Daily{{Date: ts("2024-06-03", 0, 0), High: 100, Low: 80, Close: 90}}
	return append(prev, today...), daily
}

func TestSweepBarsVariesWithAxis(t *testing.T) {
	minutes, daily := sweepFixture()
	req := SweepRequest{
		Symbol: "RB0", Period: "5",
		Donchian:  []int{50},
		VolRatio:  []float64{1.5},
		HoldBars:  []int{6},
		RR:        []float64{0.5, 1, 1.5, 3},
		Objective: SweepObjectiveAvgReturn,
		MinTrades: 1,
	}
	res := SweepBars(minutes, daily, req)

	if res.Combos != 4 || len(res.Rows) != 4 {
		t.Fatalf("组合数不对：combos=%d rows=%d", res.Combos, len(res.Rows))
	}
	if res.Objective != SweepObjectiveAvgReturn || res.Best == nil {
		t.Fatalf("结果不完整：%+v", res)
	}
	if res.Best.Params.RR != res.Rows[0].Params.RR {
		t.Fatalf("Best 应是排序后的第一行：%+v / %+v", res.Best.Params, res.Rows[0].Params)
	}
	// 排序单调（平均收益非递增）
	for i := 1; i < len(res.Rows); i++ {
		if res.Rows[i].AvgReturn > res.Rows[i-1].AvgReturn+1e-12 {
			t.Fatalf("排序不对：%+v", res.Rows)
		}
	}
	// 轴真的生效：不同盈亏比给出不同结果
	distinct := map[float64]bool{}
	for _, r := range res.Rows {
		distinct[r.AvgReturn] = true
		if r.Trades == 0 {
			t.Fatalf("该组合没有样本：%+v", r.Params)
		}
	}
	if len(distinct) < 2 {
		t.Fatalf("盈亏比轴没生效：%+v", res.Rows)
	}
}

func TestSweepFetchOnce(t *testing.T) {
	src := &fakeSource{
		name:   "s",
		minute: func() []Bar { m, _ := sweepFixture(); return m }(),
		daily:  func() []Daily { _, d := sweepFixture(); return d }(),
	}
	req := SweepRequest{
		Symbol: "RB0", Period: "5",
		RR: []float64{1, 1.5, 2, 3}, HoldBars: []int{4, 6, 8}, VolRatio: []float64{1.0, 1.5},
		Objective: SweepObjectiveAvgR, MinTrades: 1,
	}
	res, err := SweepWithSource(context.Background(), src, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Combos != 4*3*2 || len(res.Rows) != 24 {
		t.Fatalf("组合数不对：%+v", res)
	}
	// 数据只取一次（24 个组合共用同一份 K 线）
	if src.minuteCalls != 1 || src.dailyCalls != 1 {
		t.Fatalf("应只取一次数据：minute=%d daily=%d", src.minuteCalls, src.dailyCalls)
	}
	if res.ElapsedMS < 0 {
		t.Fatalf("耗时应被记录：%d", res.ElapsedMS)
	}
}

func TestSweepWithSourceValidatesSymbol(t *testing.T) {
	src := &fakeSource{name: "s"}
	if _, err := SweepWithSource(context.Background(), src, SweepRequest{Symbol: "ZZZ0"}); err == nil {
		t.Fatal("未知品种应报错")
	}
}
