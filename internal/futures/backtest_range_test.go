package futures

import (
	"strings"
	"testing"
	"time"
)

// 三天各造一批突破信号（每天 ORB高 / PDH / R1 等多个级别可能同时触发，
// 所以断言一律用「相对关系」，不写死具体笔数）。
func rangeFixture() ([]Bar, []Daily) {
	loc := locCST
	var bars []Bar
	for d := 1; d <= 3; d++ {
		day := time.Date(2026, 9, 10+d, 0, 0, 0, 0, loc)
		for i := 0; i < 6; i++ { // 09:00–09:25 窄幅（喂 ORB）
			bars = append(bars, Bar{
				Time: day.Add(9*time.Hour + time.Duration(i*5)*time.Minute),
				Open: 90, High: 100, Low: 89, Close: 99, Volume: 10000,
			})
		}
		bars = append(bars, Bar{ // 09:35 放量突破
			Time: day.Add(9*time.Hour + 35*time.Minute),
			Open: 99, High: 113, Low: 99, Close: 112, Volume: 20000,
		})
		for i := 1; i <= 6; i++ { // 跟随，够走完持有周期
			bars = append(bars, Bar{
				Time: day.Add(9*time.Hour + time.Duration(35+i*5)*time.Minute),
				Open: 112, High: 118, Low: 111, Close: 114, Volume: 15000,
			})
		}
	}
	daily := []Daily{{Date: time.Date(2026, 9, 10, 0, 0, 0, 0, loc), Open: 99, High: 100, Low: 80, Close: 99}}
	return bars, daily
}

func rangeParams(from, to string) Params {
	return Params{Symbol: "RB0", Period: "5", HoldBars: 6, Donchian: 50, VolRatio: 1.5, RR: 1.5, From: from, To: to}
}

func onlyDates(items []Outcome) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[it.Time[:10]] = true
	}
	return out
}

func TestBacktestRangeFiltersDays(t *testing.T) {
	bars, daily := rangeFixture()
	all := BacktestBars(bars, daily, rangeParams("", ""))
	if all.Trades == 0 {
		t.Fatal("不限范围应有样本")
	}
	if all.From != "" || all.To != "" {
		t.Fatalf("不限范围不该回带范围：%q ~ %q", all.From, all.To)
	}

	// 只要中间那天（边界含当天）
	mid := BacktestBars(bars, daily, rangeParams("2026-09-12", "2026-09-12"))
	if mid.Trades == 0 || mid.Trades >= all.Trades {
		t.Fatalf("单日范围应少于全量且非空：%d / %d", mid.Trades, all.Trades)
	}
	if got := onlyDates(mid.Items); len(got) != 1 || !got["2026-09-12"] {
		t.Fatalf("只该留 09-12：%v", got)
	}
	if mid.From != "2026-09-12 00:00" || mid.To != "2026-09-12 23:59" {
		t.Fatalf("结果要回带生效范围：%q ~ %q", mid.From, mid.To)
	}

	// 后两天
	late := BacktestBars(bars, daily, rangeParams("2026-09-12 09:00", "2026-09-13 15:00"))
	got := onlyDates(late.Items)
	if len(got) != 2 || !got["2026-09-12"] || !got["2026-09-13"] {
		t.Fatalf("应只剩后两天：%v", got)
	}
	if late.Trades <= mid.Trades {
		t.Fatalf("两天应多于一天：%d / %d", late.Trades, mid.Trades)
	}

	// 范围之外
	none := BacktestBars(bars, daily, rangeParams("2026-10-01", "2026-10-02"))
	if none.Trades != 0 || len(none.Items) != 0 {
		t.Fatalf("范围外应为 0 笔：%d", none.Trades)
	}
}

func TestBacktestRangeFiltersByTimeOfDay(t *testing.T) {
	bars, daily := rangeFixture()
	// 信号都在 09:35：起始晚于它就当天也不该计入
	late := BacktestBars(bars, daily, rangeParams("2026-09-11 10:00", "2026-09-11 15:00"))
	if late.Trades != 0 {
		t.Fatalf("09:35 的信号不该落进 10:00 之后的范围：%+v", late.Items)
	}
	early := BacktestBars(bars, daily, rangeParams("2026-09-11 09:00", "2026-09-11 09:40"))
	if early.Trades == 0 {
		t.Fatal("09:35 在范围内应计入")
	}
	for _, it := range early.Items {
		if !strings.HasSuffix(it.Time, "09:35") {
			t.Fatalf("不该带进 09:35 之外的信号：%+v", it)
		}
	}
}

func TestBacktestRangeAllowsExitAfterEnd(t *testing.T) {
	// 按「信号时间」筛选：出场可以自然延续到结束时间之后（否则会系统性砍掉当日尾盘单）
	bars, daily := rangeFixture()
	res := BacktestBars(bars, daily, rangeParams("2026-09-11 09:00", "2026-09-11 09:40"))
	if res.Trades == 0 {
		t.Fatal("应有样本")
	}
	if res.Items[0].ExitTime <= "2026-09-11 09:40" {
		t.Fatalf("出场应延续到范围之后：%+v", res.Items[0])
	}
}

func TestBacktestRangeLenientParsing(t *testing.T) {
	bars, daily := rangeFixture()
	full := BacktestBars(bars, daily, rangeParams("", ""))
	// 非法格式当作没填（不能把整次回测打成 0 笔）
	for _, bad := range []string{"", "  ", "2026/09/12", "昨天", "2026-13-45"} {
		res := BacktestBars(bars, daily, rangeParams(bad, bad))
		if res.Trades != full.Trades {
			t.Fatalf("非法范围 %q 应视为不限：%d != %d", bad, res.Trades, full.Trades)
		}
	}
	// RFC3339 也认
	res := BacktestBars(bars, daily, rangeParams("2026-09-12T00:00:00+08:00", "2026-09-12T23:59:00+08:00"))
	if got := onlyDates(res.Items); len(got) != 1 || !got["2026-09-12"] {
		t.Fatalf("RFC3339 应被识别：%v", got)
	}
}

func TestValidateBacktestParams(t *testing.T) {
	if err := ValidateBacktestParams(rangeParams("", "")); err != nil {
		t.Fatalf("空范围应通过：%v", err)
	}
	if err := ValidateBacktestParams(rangeParams("2026-09-12", "2026-09-11")); err == nil {
		t.Fatal("起止时间颠倒应报错")
	}
	if err := ValidateBacktestParams(rangeParams("刚才", "2026-09-11")); err == nil {
		t.Fatal("无法解析的起始时间应报错")
	}
	if err := ValidateBacktestParams(rangeParams("2026-09-01", "2026-09-22")); err != nil {
		t.Fatalf("合法范围应通过：%v", err)
	}
}

func TestSweepHonorsRange(t *testing.T) {
	bars, daily := rangeFixture()
	base := SweepRequest{
		Symbol: "RB0", Period: "5", Donchian: []int{50}, VolRatio: []float64{1.5},
		HoldBars: []int{6}, RR: []float64{1.5}, MinTrades: 1, Objective: SweepObjectiveAvgReturn,
	}
	full := SweepBars(bars, daily, base)
	narrow := base
	narrow.From, narrow.To = "2026-09-12", "2026-09-12"
	one := SweepBars(bars, daily, narrow)
	if one.Rows[0].Trades == 0 || one.Rows[0].Trades >= full.Rows[0].Trades {
		t.Fatalf("扫描也要按范围过滤：%d / %d", one.Rows[0].Trades, full.Rows[0].Trades)
	}
	// 范围是筛选而不是轴：组合数不变
	if full.Combos != one.Combos {
		t.Fatalf("范围不该改变组合数：%d / %d", full.Combos, one.Combos)
	}
}
