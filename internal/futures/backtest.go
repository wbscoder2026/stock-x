package futures

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// VarietyOfSymbol 从合约代码取品种（JM0 / JM2701 → 焦煤）。
func VarietyOfSymbol(symbol string) (Variety, bool) {
	return varietyByPrefix(letterPrefix(strings.ToUpper(strings.TrimSpace(symbol))))
}

// BacktestWithSource 用指定数据源跑突破回测。
// api 侧传「本地优先」源：先在 SQLite 里读，缺数据才走网络（并回写本地）。
func BacktestWithSource(ctx context.Context, src BarSource, p Params) (Result, error) {
	p = mergeParams(p)
	v, ok := VarietyOfSymbol(p.Symbol)
	if !ok {
		return Result{}, fmt.Errorf("未知品种 %s", p.Symbol)
	}
	minutes, err := src.Minute(ctx, v, p.Period)
	if err != nil {
		return Result{}, fmt.Errorf("%s分钟: %w", p.Period, err)
	}
	daily, err := src.Daily(ctx, v)
	if err != nil {
		return Result{}, fmt.Errorf("日线: %w", err)
	}
	return BacktestBars(minutes, daily, p), nil
}

func BacktestBars(minutes []Bar, daily []Daily, p Params) Result {
	p = mergeParams(p)
	out := Result{Symbol: p.Symbol, Period: p.Period, Items: []Outcome{}}
	if len(minutes) == 0 {
		return out
	}
	days := realSessionDays(minutes)
	var items []Outcome
	for _, day := range days {
		levels := PivotLevels(daily, day)
		if p.Period == "5" {
			if orb := ORBLevels(minutes, day, p.ORB); len(orb) > 0 {
				levels = append(levels, orb...)
			}
		}
		ev := ScanTimeframe(minutes, day, levels, p)
		items = append(items, evaluate(ev, minutes, p.HoldBars)...)
	}
	var win int
	var sum float64
	for _, it := range items {
		sum += it.Return
		if it.Correct {
			win++
		}
	}
	out.Items = items
	out.Trades = len(items)
	out.Correct = win
	if n := len(items); n > 0 {
		out.WinRate = float64(win) / float64(n)
		out.AvgReturn = sum / float64(n)
	}
	out.Bars = klineBars(minutes)
	return out
}

func klineBars(minutes []Bar) []KlineBar {
	out := make([]KlineBar, 0, len(minutes))
	for _, b := range minutes {
		out = append(out, KlineBar{
			Time: b.Time.In(locCST).Format("2006-01-02 15:04"),
			Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume,
		})
	}
	return out
}

func evaluate(ev []Event, bars []Bar, hold int) []Outcome {
	if hold <= 0 {
		hold = 6
	}
	idx := map[int64]int{}
	for i, b := range bars {
		idx[b.Time.Unix()] = i
	}
	var out []Outcome
	for _, e := range ev {
		i, ok := idx[e.Time.Unix()]
		if !ok {
			continue
		}
		j := i + hold
		if j >= len(bars) || e.Close <= 0 {
			continue
		}
		exit := bars[j]
		ret := exit.Close/e.Close - 1
		correct := (e.Direction == DirUp && ret > 0) || (e.Direction == DirDown && ret < 0)
		out = append(out, Outcome{
			Time:      e.Time.In(locCST).Format("2006-01-02 15:04"),
			Direction: e.Direction, Level: e.Level,
			Close: e.Close, Volume: e.Volume, LevelPrice: e.LevelPrice,
			ExitTime:  exit.Time.In(locCST).Format("2006-01-02 15:04"),
			ExitPrice: exit.Close, Return: ret, Correct: correct,
		})
	}
	return out
}

func realSessionDays(min []Bar) []time.Time {
	type stat struct {
		n int
		v float64
	}
	stats := map[string]*stat{}
	var keys []string
	for _, b := range min {
		t := b.Time.In(locCST)
		if t.Hour() >= 16 {
			continue
		}
		k := t.Format("2006-01-02")
		s := stats[k]
		if s == nil {
			s = &stat{}
			stats[k] = s
			keys = append(keys, k)
		}
		s.n++
		s.v += b.Volume
	}
	var days []time.Time
	for _, k := range keys {
		s := stats[k]
		if s.n >= 3 && s.v >= 5000 {
			d, _ := time.ParseInLocation("2006-01-02", k, locCST)
			days = append(days, d)
		}
	}
	return days
}
