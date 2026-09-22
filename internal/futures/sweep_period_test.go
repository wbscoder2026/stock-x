package futures

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// periodSource 按级别返回不同 K 线的假源（级别轴必须各自取数）。
type periodSource struct {
	byPeriod  map[string][]Bar
	daily     []Daily
	minuteErr map[string]error
	minuteN   map[string]int
	dailyN    int
}

func (s *periodSource) Name() string { return "period-src" }

func (s *periodSource) Minute(_ context.Context, _ Variety, period string) ([]Bar, error) {
	if s.minuteN == nil {
		s.minuteN = map[string]int{}
	}
	s.minuteN[period]++
	if err := s.minuteErr[period]; err != nil {
		return nil, err
	}
	bars, ok := s.byPeriod[period]
	if !ok {
		return nil, errors.New("该级别无数据")
	}
	return bars, nil
}

func (s *periodSource) Daily(_ context.Context, _ Variety) ([]Daily, error) {
	s.dailyN++
	return s.daily, nil
}

func periodFixture() (*periodSource, []Daily) {
	minutes, daily := sweepFixture()
	return &periodSource{
		byPeriod: map[string][]Bar{
			"5":  minutes,
			"15": minutes,
			"60": minutes,
		},
		daily: daily,
	}, daily
}

func TestNormalizeSweepPeriods(t *testing.T) {
	// 空 → 用单个基准级别；去重、排序、白名单
	p, err := normalizeSweep(SweepRequest{Period: "15"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Periods) != 1 || p.Periods[0] != "15" {
		t.Fatalf("空级别列表应回落基准级别：%v", p.Periods)
	}

	p, err = normalizeSweep(SweepRequest{Periods: []string{"60", "5", "60", " 15 "}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Periods, ",") != "5,15,60" {
		t.Fatalf("应去重升序：%v", p.Periods)
	}

	if _, err := normalizeSweep(SweepRequest{Periods: []string{"7"}}); err == nil {
		t.Fatal("非法级别应报错")
	} else if !strings.Contains(err.Error(), "级别") {
		t.Fatalf("错误信息应说明级别：%v", err)
	}
}

func TestSweepPeriodsAxisExpandsCombos(t *testing.T) {
	src, daily := periodFixture()
	req := SweepRequest{
		Symbol:   "RB0",
		Periods:  []string{"15", "60"},
		Donchian: []int{50}, VolRatio: []float64{1.5}, HoldBars: []int{6}, RR: []float64{1, 2},
		Objective: SweepObjectiveAvgReturn, MinTrades: 1,
	}
	res, err := SweepWithSource(context.Background(), src, req)
	if err != nil {
		t.Fatal(err)
	}
	// 2 个级别 × 2 个盈亏比 = 4 个组合
	if res.Combos != 4 || len(res.Rows) != 4 {
		t.Fatalf("组合数不对：combos=%d rows=%d", res.Combos, len(res.Rows))
	}
	byPeriod := map[string]int{}
	for _, r := range res.Rows {
		byPeriod[r.Params.Period]++
	}
	if byPeriod["15"] != 2 || byPeriod["60"] != 2 {
		t.Fatalf("两个级别都应出现：%v", byPeriod)
	}
	if strings.Join(res.Periods, ",") != "15,60" {
		t.Fatalf("结果里要回带级别列表：%v", res.Periods)
	}
	// 数据只按「级别」取，日线共用一个
	if src.minuteN["15"] != 1 || src.minuteN["60"] != 1 || src.dailyN != 1 {
		t.Fatalf("取数次数不对：%v daily=%d", src.minuteN, src.dailyN)
	}
	if res.PeriodBars["15"] == 0 || res.PeriodBars["60"] == 0 {
		t.Fatalf("要回带各级别的 K 线根数（样本覆盖不同）：%v", res.PeriodBars)
	}
	_ = daily
}

func TestSweepSkipsFailedPeriod(t *testing.T) {
	src, _ := periodFixture()
	// 15 分钟取数失败（例如上游没这个级别的历史），其余级别照跑
	src.minuteErr = map[string]error{"15": errors.New("上游 456")}
	res, err := SweepWithSource(context.Background(), src, SweepRequest{
		Symbol: "RB0", Periods: []string{"5", "15", "60"},
		Donchian: []int{50}, VolRatio: []float64{1.5}, HoldBars: []int{6}, RR: []float64{1.5},
		Objective: SweepObjectiveAvgReturn, MinTrades: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Combos != 2 || len(res.Rows) != 2 {
		t.Fatalf("只该跑成功的两个级别：%+v", res.Rows)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "15") {
		t.Fatalf("要说明跳过了哪个级别：%v", res.Skipped)
	}
	for _, r := range res.Rows {
		if r.Params.Period == "15" {
			t.Fatalf("跳过的级别不该出现在结果里：%+v", r.Params)
		}
	}
}

func TestSweepAllPeriodsFail(t *testing.T) {
	src, _ := periodFixture()
	src.minuteErr = map[string]error{"5": errors.New("boom"), "15": errors.New("boom")}
	if _, err := SweepWithSource(context.Background(), src, SweepRequest{
		Symbol: "RB0", Periods: []string{"5", "15"}, RR: []float64{1},
	}); err == nil {
		t.Fatal("全部级别都取不到数据应报错")
	}
}

func TestSweepLimitCountsAcrossPeriods(t *testing.T) {
	// 上限要按「级别 × 组合」算，不能只看单级别的组合数
	src, _ := periodFixture()
	_, err := SweepWithSource(context.Background(), src, SweepRequest{
		Symbol: "RB0", Periods: []string{"5", "15", "60"},
		RR:    []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		Limit: 40,
	})
	if err == nil {
		t.Fatal("3×15=45 个组合超过 40 上限应报错")
	}
}
