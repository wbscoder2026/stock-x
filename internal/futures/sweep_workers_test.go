package futures

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeWorkers(t *testing.T) {
	cases := map[int]int{0: 0, -1: 0, 1: 1, 8: 8, 64: 64, 999: SweepMaxWorkers}
	for in, want := range cases {
		got := NormalizeSweepWorkers(in)
		if got < 1 {
			t.Fatalf("NormalizeSweepWorkers(%d) 至少是 1：%d", in, got)
		}
		if want > 0 && got != want {
			t.Fatalf("NormalizeSweepWorkers(%d) = %d，期望 %d", in, got, want)
		}
		if got > SweepMaxWorkers {
			t.Fatalf("不能超过上限 %d：%d", SweepMaxWorkers, got)
		}
	}
	// 0 = 自动（CPU 核数）
	if got := NormalizeSweepWorkers(0); got != runtime.NumCPU() {
		t.Fatalf("0 应该等于 CPU 核数 %d，实际 %d", runtime.NumCPU(), got)
	}
}

func TestSweepRunWorkersBeforeStart(t *testing.T) {
	run := NewSweepRun(2)
	if got := run.Workers(); got != 2 {
		t.Fatalf("初始并发不对：%d", got)
	}
	// 还没建池：改并发只是记下期望值（建池时按它来）
	run.SetWorkers(8)
	if got := run.Workers(); got != 8 {
		t.Fatalf("建池前改并发应记下：%d", got)
	}
	run.SetWorkers(0) // 0 = 自动核数
	if got := run.Workers(); got != runtime.NumCPU() {
		t.Fatalf("0 应等于核数 %d：%d", runtime.NumCPU(), got)
	}
	run.SetWorkers(999) // 超上限要截断
	if got := run.Workers(); got != SweepMaxWorkers {
		t.Fatalf("应截到上限 %d：%d", SweepMaxWorkers, got)
	}
	if done, total := run.Progress(); done != 0 || total != 0 {
		t.Fatalf("还没开跑不该有进度：%d/%d", done, total)
	}
	run.Report(3, 10)
	if done, total := run.Progress(); done != 3 || total != 10 {
		t.Fatalf("进度没读对：%d/%d", done, total)
	}
}

// 动态并发的关键：Tune 之后，被 Submit 阻塞的任务要立刻能用上新容量。
func TestSweepRunTuneScalesUpMidRun(t *testing.T) {
	run := NewSweepRun(1)
	pool, err := newSweepPool(run.Workers())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release()
	run.bindPool(pool)
	defer run.releasePool()

	const tasks = 8
	release := make(chan struct{})
	var inFlight, peak atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < tasks; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := pool.Submit(func() {
				n := inFlight.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				<-release
				inFlight.Add(-1)
			}); err != nil {
				t.Errorf("提交任务失败：%v", err)
			}
		}()
	}

	// 容量 1：只该有一个任务在跑，其余卡在 Submit 上
	time.Sleep(200 * time.Millisecond)
	if got := inFlight.Load(); got != 1 {
		t.Fatalf("容量 1 时应该只有 1 个任务在跑：%d", got)
	}
	if got := pool.Cap(); got != 1 {
		t.Fatalf("初始容量应为 1：%d", got)
	}

	run.SetWorkers(4)
	if got := pool.Cap(); got != 4 {
		t.Fatalf("Tune 后容量应为 4：%d", got)
	}
	if !waitFor(t, func() bool { return inFlight.Load() == 4 }) {
		t.Fatalf("Tune 到 4 之后应有 4 个任务在跑：%d", inFlight.Load())
	}

	// 再调小：已经在跑的不打断，但从 4 收敛（这里只验证峰值没超过 4）
	run.SetWorkers(2)
	if got := pool.Cap(); got != 2 {
		t.Fatalf("Tune 后容量应为 2：%d", got)
	}

	close(release)
	wg.Wait()
	if got := peak.Load(); got != 4 {
		t.Fatalf("峰值并发应为 4：%d", got)
	}
	if got := inFlight.Load(); got != 0 {
		t.Fatalf("任务都该结束了：%d", got)
	}
}

func waitFor(t *testing.T, ok func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// 途中反复改并发，结果必须和固定并发完全一致（并发只影响速度）。
func TestSweepResultsUnaffectedByTuning(t *testing.T) {
	minutes, daily := sweepFixture()
	base := SweepRequest{
		Symbol: "RB0", Period: "5",
		ORB: []int{15, 30}, Donchian: []int{40, 50, 60}, ATRPeriod: []int{7, 14},
		VolRatio: []float64{1.2, 1.5}, HoldBars: []int{4, 6, 8},
		RR: []float64{0.5, 1, 1.5, 2}, StopATR: []float64{0.5, 1},
		Objective: SweepObjectiveAvgReturn, MinTrades: 1, Workers: 4,
	}
	want := SweepBars(minutes, daily, base)

	run := NewSweepRun(1)
	tuned := base
	tuned.Run = run
	tuned.OnProgress = run.Report

	quit := make(chan struct{})
	var tuner sync.WaitGroup
	tuner.Add(1)
	go func() {
		defer tuner.Done()
		for i := 1; ; i++ {
			select {
			case <-quit:
				return
			default:
				run.SetWorkers(1 + i%8) // 一边跑一边来回改
			}
		}
	}()
	got := SweepBars(minutes, daily, tuned)
	close(quit)
	tuner.Wait()

	if got.Combos != want.Combos || len(got.Rows) != len(want.Rows) {
		t.Fatalf("组合数不一致：%d/%d vs %d/%d", got.Combos, len(got.Rows), want.Combos, len(want.Rows))
	}
	for i := range want.Rows {
		if got.Rows[i].Params != want.Rows[i].Params || got.Rows[i].Trades != want.Rows[i].Trades {
			t.Fatalf("第 %d 行不一致：%+v(%d) vs %+v(%d)",
				i, got.Rows[i].Params, got.Rows[i].Trades, want.Rows[i].Params, want.Rows[i].Trades)
		}
	}
	if done, total := run.Progress(); total != int64(want.Combos) || done != total {
		t.Fatalf("进度应跑满：%d/%d（组合 %d）", done, total, want.Combos)
	}
}

func TestSweepLimitRaised(t *testing.T) {
	// 上限是 10000000（1000 万）：
	thirty := make([]int, 0, 30)
	for i := 0; i < 30; i++ {
		thirty = append(thirty, i+1)
	}
	thirtyF := make([]float64, 0, 30)
	for i := 0; i < 30; i++ {
		thirtyF = append(thirtyF, float64(i+1))
	}

	// 30^5 = 2430 万组，超过上限要拦（别把机器打死）
	if _, err := normalizeSweep(SweepRequest{
		HoldBars: thirty, Donchian: thirty, ATRPeriod: thirty, ORB: thirty, ATRK: thirtyF,
	}); err == nil {
		t.Fatal("24300000 组超过上限应报错")
	}
	// 30^4 = 81 万组要放行
	p, err := normalizeSweep(SweepRequest{HoldBars: thirty, Donchian: thirty, ATRPeriod: thirty, ORB: thirty})
	if err != nil {
		t.Fatalf("810000 组不该报错：%v", err)
	}
	if SweepDefaultLimit != 10000000 {
		t.Fatalf("默认上限应为 10000000：%d", SweepDefaultLimit)
	}
	if p.Limit != SweepDefaultLimit {
		t.Fatalf("没填 limit 时应落到默认上限：%d", p.Limit)
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
