package futures

import (
	"context"
	"math"
	"testing"
)

func TestSummarizeSymbolStats_EqualWeightAndPooled(t *testing.T) {
	rows := []SymbolStat{
		{Symbol: "A", Trades: 10, Correct: 10, WinRate: 1, AvgReturn: 0.1, AvgR: 0.5, ProfitFactor: 2},
		{Symbol: "B", Trades: 4, Correct: 2, WinRate: 0.5, AvgReturn: -0.02, AvgR: -0.1, ProfitFactor: 0.4},
		{Symbol: "C", Trades: 0},
		{Symbol: "D", Error: "无数据"},
	}
	got := summarizeSymbolStats(rows, 20)
	if got.covered != 2 || got.noSample != 1 || got.failed != 1 || got.reliable != 0 {
		t.Fatalf("计数不对：%+v", got)
	}
	if math.Abs(got.avgWin-0.75) > 1e-9 {
		t.Fatalf("等权胜率 %v", got.avgWin)
	}
	if math.Abs(got.avgRet-0.04) > 1e-9 {
		t.Fatalf("等权收益 %v", got.avgRet)
	}
	if math.Abs(got.avgR-0.2) > 1e-9 {
		t.Fatalf("等权期望R %v", got.avgR)
	}
	if math.Abs(got.avgPF-1.2) > 1e-9 {
		t.Fatalf("盈利因子 %v", got.avgPF)
	}
	if math.Abs(got.pooledWin-12.0/14.0) > 1e-9 {
		t.Fatalf("加权胜率 %v", got.pooledWin)
	}
	wantRet := (0.1*10 + -0.02*4) / 14
	if math.Abs(got.pooledRet-wantRet) > 1e-9 {
		t.Fatalf("加权收益 %v want %v", got.pooledRet, wantRet)
	}

	pf := summarizeSymbolStats([]SymbolStat{
		{Trades: 2, Correct: 2, WinRate: 1, AvgReturn: 0.1, ProfitFactor: 0},
		{Trades: 2, Correct: 1, WinRate: 0.5, AvgReturn: 0.01, ProfitFactor: 3},
	}, 1)
	if pf.avgPF != 3 || pf.reliable != 2 {
		t.Fatalf("没有亏损的盈利因子不该拉低平均：%+v", pf)
	}
}

func TestScanAcross_TwoVarietiesMatchSingleBacktest(t *testing.T) {
	minutes, daily := breakoutFixture()
	jm, ok := varietyByPrefix("JM")
	if !ok {
		t.Fatal("没有焦煤")
	}
	rb, ok := varietyByPrefix("RB")
	if !ok {
		t.Fatal("没有螺纹")
	}
	src := &scriptedBars{minutes: minutes, daily: daily}
	base := Params{Period: "15", HoldBars: 2, Donchian: 50, VolRatio: 1.5, RR: 1.5}
	wide := base
	wide.RR = 3
	res, err := ScanAcross(context.Background(), src, []Variety{jm, rb}, []FavConfig{
		{ID: 1, Name: "紧", Params: base},
		{ID: 2, Name: "宽", Params: wide},
	}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Configs) != 2 || res.Symbols != 2 {
		t.Fatalf("%+v", res)
	}
	one := runBacktest(minutes, daily, Params{Symbol: MainSymbol(jm), Period: "15", HoldBars: 2, Donchian: 50, VolRatio: 1.5, RR: 1.5}, false)
	if one.Trades == 0 {
		t.Fatal("夹具应有成交")
	}
	for _, cfg := range res.Configs {
		if cfg.Covered != 2 || cfg.NoSample != 0 || cfg.Failed != 0 {
			t.Fatalf("%s 覆盖不对：covered=%d no=%d fail=%d", cfg.Name, cfg.Covered, cfg.NoSample, cfg.Failed)
		}
		if cfg.TotalTrades != one.Trades*2 {
			t.Fatalf("%s 成交 %d", cfg.Name, cfg.TotalTrades)
		}
	}
	if math.Abs(res.Configs[0].AvgWinRate-one.WinRate) > 1e-9 {
		t.Fatalf("等权胜率应等于单品种：%v vs %v", res.Configs[0].AvgWinRate, one.WinRate)
	}
	wantOverall := (res.Configs[0].AvgWinRate + res.Configs[1].AvgWinRate) / 2
	if math.Abs(res.Overall.AvgWinRate-wantOverall) > 1e-9 || res.Overall.Configs != 2 {
		t.Fatalf("总平均应是两条规则的等权：%+v", res.Overall)
	}
	if _, err := ScanAcross(context.Background(), src, []Variety{jm}, nil, 1, 1); err == nil {
		t.Fatal("空配置应报错")
	}
}

func TestScanAcross_SkipsFailedVariety(t *testing.T) {
	minutes, daily := breakoutFixture()
	jm, _ := varietyByPrefix("JM")
	rb, _ := varietyByPrefix("RB")
	src := &scriptedBars{
		minutes: minutes, daily: daily,
		failDaily:  map[string]string{"RB": "日线超时"},
		failPeriod: map[string]string{"60": "没有60分钟"},
	}
	res, err := ScanAcross(context.Background(), src, []Variety{jm, rb}, []FavConfig{
		{ID: 1, Name: "15", Params: Params{Period: "15", HoldBars: 2, Donchian: 50, VolRatio: 1.5}},
		{ID: 2, Name: "60", Params: Params{Period: "60", HoldBars: 2, Donchian: 50, VolRatio: 1.5}},
	}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || res.Configs[0].Covered != 1 || res.Configs[0].Failed != 1 {
		t.Fatalf("日线失败应只跳过螺纹：skipped=%v covered=%d failed=%d", res.Skipped, res.Configs[0].Covered, res.Configs[0].Failed)
	}
	if res.Configs[1].Covered != 0 || res.Configs[1].Failed != 2 {
		t.Fatalf("60分钟取数失败不应出成交：%+v", res.Configs[1])
	}
	if res.Overall.Configs != 1 {
		t.Fatalf("没有样本的规则不进总平均：%+v", res.Overall)
	}
}

func breakoutFixture() ([]Bar, []Daily) {
	prev := warmup("2024-06-03", 30)
	day := "2024-06-04"
	today := make([]Bar, 12)
	for i := range today {
		c, h, v := 90.0, 92.0, 10000.0
		if i == 6 {
			c, h, v = 112, 113, 20000
		}
		if i > 6 {
			today[i] = bar(ts(day, 9, 5+i*5), 112, 121, 111, 120, 10000)
			continue
		}
		today[i] = bar(ts(day, 9, 5+i*5), 90, h, 88, c, v)
	}
	return append(prev, today...), []Daily{{Date: ts("2024-06-03", 0, 0), High: 100, Low: 80, Close: 90}}
}

type scriptedBars struct {
	minutes    []Bar
	daily      []Daily
	failDaily  map[string]string
	failPeriod map[string]string
}

func (s *scriptedBars) Name() string { return "scripted" }

func (s *scriptedBars) Minute(_ context.Context, _ Variety, period string) ([]Bar, error) {
	if msg, ok := s.failPeriod[period]; ok {
		return nil, errString(msg)
	}
	return s.minutes, nil
}

func (s *scriptedBars) Daily(_ context.Context, v Variety) ([]Daily, error) {
	if msg, ok := s.failDaily[v.Prefix]; ok {
		return nil, errString(msg)
	}
	return s.daily, nil
}

type errString string

func (e errString) Error() string { return string(e) }
