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
			c, h = 120, 121
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
	if got := evaluate(ev, bars, 2); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}
