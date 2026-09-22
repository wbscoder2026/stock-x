package futures

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeWorkers(t *testing.T) {
	cases := map[int]int{0: 0, -1: 0, 1: 1, 8: 8, 64: 64, 999: SweepMaxWorkers}
	for in, want := range cases {
		got := normalizeWorkers(in)
		if got < 1 {
			t.Fatalf("normalizeWorkers(%d) 至少是 1：%d", in, got)
		}
		if want > 0 && got != want {
			t.Fatalf("normalizeWorkers(%d) = %d，期望 %d", in, got, want)
		}
		if got > SweepMaxWorkers {
			t.Fatalf("不能超过上限 %d：%d", SweepMaxWorkers, got)
		}
	}
	// 0 = 自动（CPU 核数）
	if got := normalizeWorkers(0); got != runtime.NumCPU() {
		t.Fatalf("0 应该等于 CPU 核数 %d，实际 %d", runtime.NumCPU(), got)
	}
}

func TestSweepLimitRaised(t *testing.T) {
	// 上限放开到 10000：30×30×30 = 27000 组仍然要拦（别把机器打死）
	thirty := make([]int, 0, 30)
	for i := 0; i < 30; i++ {
		thirty = append(thirty, i+1)
	}
	if _, err := normalizeSweep(SweepRequest{HoldBars: thirty, Donchian: thirty, Limit: 10000, ATRPeriod: thirty}); err == nil {
		t.Fatal("27000 组超过上限应报错")
	}
	// 210 组要放行
	p, err := normalizeSweep(SweepRequest{HoldBars: thirty, Donchian: []int{1, 2, 3, 4, 5, 6, 7}, Limit: 10000})
	if err != nil {
		t.Fatalf("210 组不该报错：%v", err)
	}
	if p.Limit != 10000 {
		t.Fatalf("上限应为 10000：%d", p.Limit)
	}
	if p.Workers < 1 {
		t.Fatalf("并发应被补齐：%d", p.Workers)
	}
}

func TestSweepWorkersEchoAndDeterminism(t *testing.T) {
	minutes, daily := sweepFixture()
	req := SweepRequest{
		Symbol: "RB0", Period: "5",
		Donchian: []int{50}, VolRatio: []float64{1.5}, HoldBars: []int{6},
		RR: []float64{0.5, 1, 1.5, 3}, StopATR: []float64{0.5, 1},
		Objective: SweepObjectiveAvgReturn, MinTrades: 1,
	}

	seq := req
	seq.Workers = 1
	res1 := SweepBars(minutes, daily, seq)
	if res1.Workers != 1 {
		t.Fatalf("并发数应回带：%d", res1.Workers)
	}

	par := req
	par.Workers = 4
	res4 := SweepBars(minutes, daily, par)
	if res4.Workers != 4 {
		t.Fatalf("并发数应回带：%d", res4.Workers)
	}

	// 并发不影响结果（顺序 = 排序后的确定性顺序）
	if len(res1.Rows) != len(res4.Rows) {
		t.Fatalf("行数不一致：%d vs %d", len(res1.Rows), len(res4.Rows))
	}
	for i := range res1.Rows {
		a, b := res1.Rows[i], res4.Rows[i]
		if a.Params.RR != b.Params.RR || a.Params.StopATR != b.Params.StopATR ||
			a.Trades != b.Trades || a.AvgReturn != b.AvgReturn || a.Reliable != b.Reliable {
			t.Fatalf("第 %d 行并发结果不一致：%+v vs %+v", i, a, b)
		}
	}
}

func TestSweepWorkersCappedByJobs(t *testing.T) {
	// 只有 1 个组合时不该开 8 个 worker
	minutes, daily := sweepFixture()
	res := SweepBars(minutes, daily, SweepRequest{
		Symbol: "RB0", Period: "5", Donchian: []int{50}, VolRatio: []float64{1.5},
		HoldBars: []int{6}, RR: []float64{1.5}, MinTrades: 1, Workers: 8,
	})
	if res.Workers != 1 {
		t.Fatalf("实际并发不该超过组合数：%d", res.Workers)
	}
}

// cancelSource 取数时取消 context，模拟客户端断开/超时。
type cancelSource struct {
	byPeriod map[string][]Bar
	daily    []Daily
	cancel   context.CancelFunc
}

func (s *cancelSource) Name() string { return "cancel-src" }
func (s *cancelSource) Minute(_ context.Context, _ Variety, period string) ([]Bar, error) {
	if s.cancel != nil {
		s.cancel()
	}
	return s.byPeriod[period], nil
}
func (s *cancelSource) Daily(_ context.Context, _ Variety) ([]Daily, error) { return s.daily, nil }

func TestSweepStopsWhenContextCanceled(t *testing.T) {
	minutes, daily := sweepFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &cancelSource{byPeriod: map[string][]Bar{"5": minutes}, daily: daily, cancel: cancel}

	_, err := SweepWithSource(ctx, src, SweepRequest{
		Symbol: "RB0", Period: "5", Donchian: []int{50}, VolRatio: []float64{1.5},
		HoldBars: []int{6}, RR: []float64{1.5},
	})
	if err == nil {
		t.Fatal("context 取消后应报错，而不是返回半截结果")
	}
	if !strings.Contains(err.Error(), "取消") {
		t.Fatalf("错误信息应说明取消：%v", err)
	}
}
