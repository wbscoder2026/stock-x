package futures

import "testing"

func TestBacktestCorrectness(t *testing.T) {
	prev := warmup("2024-06-03", 30)
	day := "2024-06-04"
	today := make([]Bar, 12)
	for i := range today {
		c := 90.0
		h := 92.0
		v := 10000.0
		if i == 6 {
			c, h, v = 112, 113, 20000
		}
		if i > 6 {
			// 突破后继续走：开/低也跟着上移。
			// （原来只改收盘/最高，留下「开 90 低 88 收 120」的畸形 K 线，
			//   带上 1×ATR 止损后会被跳空打穿，那是 fixture 不自洽，不是策略问题。）
			today[i] = bar(ts(day, 9, 5+i*5), 112, 121, 111, 120, 10000)
			continue
		}
		today[i] = bar(ts(day, 9, 5+i*5), 90, h, 88, c, v)
	}
	minutes := append(prev, today...)
	daily := []Daily{{Date: ts("2024-06-03", 0, 0), High: 100, Low: 80, Close: 90}}
	res := BacktestBars(minutes, daily, Params{Period: "15", HoldBars: 2, Donchian: 50, VolRatio: 1.5})
	if res.Trades == 0 {
		t.Fatalf("no trades")
	}
	var pdh *Outcome
	for i := range res.Items {
		if res.Items[i].Level == "PDH(昨高)" {
			pdh = &res.Items[i]
		}
	}
	if pdh == nil {
		t.Fatalf("items=%+v", res.Items)
	}
	if !pdh.Correct || pdh.Return <= 0 {
		t.Fatalf("%+v", pdh)
	}
	if res.WinRate <= 0 {
		t.Fatalf("win_rate=%v", res.WinRate)
	}
	if pdh.ExitReason == "" || pdh.TPPrice <= pdh.Close || pdh.StopPrice >= pdh.Close {
		t.Fatalf("出场信息不全：%+v", pdh)
	}
	if res.StopExits+res.TPExits+res.HoldExits != res.Trades {
		t.Fatalf("出场分布应等于样本数：%+v", res)
	}

	// 盈亏比要真的从表单参数传进回测（不能"看着生效其实是死的"）
	bigRR := BacktestBars(minutes, daily, Params{
		Period: "15", HoldBars: 2, Donchian: 50, VolRatio: 1.5, RR: 3,
	})
	for _, it := range bigRR.Items {
		if it.Level == "PDH(昨高)" && it.TPPrice <= pdh.TPPrice {
			t.Fatalf("盈亏比没传进回测：%+v", it)
		}
	}

	for i := 7; i < len(today); i++ {
		today[i].Close, today[i].High, today[i].Low = 80, 81, 79
	}
	minutes = append(prev, today...)
	res = BacktestBars(minutes, daily, Params{Period: "15", HoldBars: 2, Donchian: 50, VolRatio: 1.5})
	for i := range res.Items {
		if res.Items[i].Level == "PDH(昨高)" && res.Items[i].Correct {
			t.Fatalf("false breakout should be wrong: %+v", res.Items[i])
		}
	}
}

func TestEvaluateSkipsTail(t *testing.T) {
	bars := []Bar{
		bar(ts("2024-06-04", 9, 5), 1, 1, 1, 100, 1),
		bar(ts("2024-06-04", 9, 10), 1, 1, 1, 110, 1),
	}
	ev := []Event{{Time: bars[1].Time, Direction: DirUp, Close: 110, Level: "x"}}
	if got, _ := evaluate(ev, bars, "RB", Params{HoldBars: 2, StopATR: 1, RR: DefaultRR}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}
