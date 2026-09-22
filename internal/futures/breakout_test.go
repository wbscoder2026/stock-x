package futures

import (
	"math"
	"testing"
	"time"
)

func ts(day string, hh, mm int) time.Time {
	t, err := time.ParseInLocation("2006-01-02", day, locCST)
	if err != nil {
		panic(err)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), hh, mm, 0, 0, locCST)
}

func bar(t time.Time, o, h, l, c, v float64) Bar {
	return Bar{Time: t, Open: o, High: h, Low: l, Close: c, Volume: v}
}

func TestPivotLevels(t *testing.T) {
	daily := []Daily{{
		Date: ts("2024-06-03", 0, 0), High: 110, Low: 100, Close: 105,
	}}
	got := PivotLevels(daily, ts("2024-06-04", 0, 0))
	want := map[string]float64{
		"R2(枢轴)": 115, "R1(枢轴)": 110, "PDH(昨高)": 110,
		"PDL(昨低)": 100, "S1(枢轴)": 100, "S2(枢轴)": 95,
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d %+v", len(got), got)
	}
	for _, lv := range got {
		if math.Abs(lv.Value-want[lv.Name]) > 1e-9 {
			t.Fatalf("%s=%v want %v", lv.Name, lv.Value, want[lv.Name])
		}
	}
}

func TestORBLevels(t *testing.T) {
	day := "2024-06-04"
	var min5 []Bar
	for i, h := range []float64{101, 103, 102, 104, 100, 102} {
		min5 = append(min5, bar(ts(day, 9, 5+i*5), 100, h, 99, 100, 1000))
	}
	got := ORBLevels(min5, ts(day, 0, 0), 30)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0].Value != 104 || got[1].Value != 99 {
		t.Fatalf("orb=%+v", got)
	}
}

func warmup(day string, n int) []Bar {
	out := make([]Bar, n)
	t0 := ts(day, 9, 5)
	for i := range out {
		out[i] = bar(t0.Add(time.Duration(i)*5*time.Minute), 90, 92, 88, 90, 10000)
	}
	return out
}

func TestScanBreakoutUpAndVolumeFilter(t *testing.T) {
	prev := warmup("2024-06-03", 30)
	day := "2024-06-04"
	var today []Bar
	for i := 0; i < 8; i++ {
		v := 10000.0
		c := 90.0
		h := 92.0
		if i == 6 {
			v = 20000
			c = 112
			h = 113
		}
		today = append(today, bar(ts(day, 9, 5+i*5), 90, h, 88, c, v))
	}
	df := append(prev, today...)
	daily := []Daily{{Date: ts("2024-06-03", 0, 0), High: 100, Low: 80, Close: 90}}
	levels := PivotLevels(daily, ts(day, 0, 0))
	p := Params{Donchian: 20, ATRPeriod: 14, ATRK: 0.25, VolRatio: 1.5}
	ev := ScanTimeframe(df, ts(day, 0, 0), levels, p)
	found := false
	for _, e := range ev {
		if e.Level == "PDH(昨高)" && e.Direction == DirUp {
			found = true
			if e.Close != 112 {
				t.Fatalf("close=%v", e.Close)
			}
			if e.ATR <= 0 { // 推荐止损/止盈要用它
				t.Fatalf("事件应带上 ATR：%+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("want PDH up, got %+v", ev)
	}

	for i := range today {
		today[i].Volume = 10000
		if today[i].Close == 112 {
			today[i].High = 113
		}
	}
	df2 := append(prev, today...)
	ev2 := ScanTimeframe(df2, ts(day, 0, 0), levels, p)
	for _, e := range ev2 {
		if e.Level == "PDH(昨高)" {
			t.Fatalf("volume filter failed: %+v", ev2)
		}
	}
}

func TestScanFirstHitOnly(t *testing.T) {
	prev := warmup("2024-06-03", 30)
	day := "2024-06-04"
	today := []Bar{
		bar(ts(day, 9, 5), 90, 92, 88, 90, 10000),
		bar(ts(day, 9, 10), 90, 113, 88, 112, 20000),
		bar(ts(day, 9, 15), 112, 115, 110, 114, 20000),
	}
	df := append(prev, today...)
	levels := []Level{{"PDH(昨高)", KindR, 100}}
	ev := ScanTimeframe(df, ts(day, 0, 0), levels, Params{Donchian: 50, ATRPeriod: 14, VolRatio: 1.5, ATRK: 0.25})
	n := 0
	for _, e := range ev {
		if e.Level == "PDH(昨高)" {
			n++
			if e.Time != ts(day, 9, 10) {
				t.Fatalf("first hit %v", e.Time)
			}
		}
	}
	if n != 1 {
		t.Fatalf("n=%d %+v", n, ev)
	}
}

func TestDetermineDaysUpcoming(t *testing.T) {
	real := warmup("2024-06-03", 10)
	for i := range real {
		real[i].Volume = 2000
	}
	place := []Bar{
		bar(ts("2024-06-04", 9, 5), 1, 1, 1, 1, 0),
		bar(ts("2024-06-04", 9, 10), 1, 1, 1, 1, 0),
	}
	day, upcoming, last := determineDays(append(real, place...), nil)
	if !upcoming || day.Format("2006-01-02") != "2024-06-04" || last.Format("2006-01-02") != "2024-06-03" {
		t.Fatalf("day=%v upcoming=%v last=%v", day, upcoming, last)
	}
}

func TestScanBarsIncludesOHLC(t *testing.T) {
	min5 := warmup("2024-06-03", 10)
	for i := range min5 {
		min5[i].Volume = 2000
	}
	min15 := min5
	daily := []Daily{{Date: ts("2024-06-02", 0, 0), High: 100, Low: 80, Close: 90}}
	snap := ScanBars(min5, min15, daily, Params{Symbol: "JM0"})
	if len(snap.Bars5) != len(min5) || snap.Bars5[0].Open != 90 {
		t.Fatalf("bars5=%+v", snap.Bars5)
	}
	if len(snap.Bars15) != len(min15) {
		t.Fatalf("bars15=%d", len(snap.Bars15))
	}
}

func TestEMATrendUp(t *testing.T) {
	var df []Bar
	t0 := ts("2024-06-04", 9, 0)
	for i := 0; i < 30; i++ {
		c := 100 + float64(i)
		df = append(df, bar(t0.Add(time.Duration(i)*15*time.Minute), c, c, c, c, 1000))
	}
	if g := emaTrend(df, 20); g != "向上" {
		t.Fatalf("trend=%s", g)
	}
}
