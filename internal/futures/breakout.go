package futures

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/wbscoder2026/stock-x/internal/indicator"
)

func PivotLevels(daily []Daily, day time.Time) []Level {
	day = truncateDate(day)
	var prev *Daily
	for i := range daily {
		d := truncateDate(daily[i].Date)
		if d.Before(day) {
			prev = &daily[i]
		}
	}
	if prev == nil {
		return nil
	}
	pdh, pdl, pdc := prev.High, prev.Low, prev.Close
	p := (pdh + pdl + pdc) / 3
	rng := pdh - pdl
	return []Level{
		{"R2(枢轴)", KindR, p + rng},
		{"R1(枢轴)", KindR, 2*p - pdl},
		{"PDH(昨高)", KindR, pdh},
		{"PDL(昨低)", KindS, pdl},
		{"S1(枢轴)", KindS, 2*p - pdh},
		{"S2(枢轴)", KindS, p - rng},
	}
}

func ORBLevels(min5 []Bar, day time.Time, orbMin int) []Level {
	if orbMin <= 0 {
		orbMin = 30
	}
	n := int(math.Ceil(float64(orbMin) / 5.0))
	t0 := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, locCST)
	var bars []Bar
	for _, b := range min5 {
		t := b.Time.In(locCST)
		if !sameDate(t, day) || t.Hour() >= 11 || t.Before(t0) {
			continue
		}
		bars = append(bars, b)
		if len(bars) >= n {
			break
		}
	}
	if len(bars) < n {
		return nil
	}
	hi, lo := bars[0].High, bars[0].Low
	for _, b := range bars[1:] {
		if b.High > hi {
			hi = b.High
		}
		if b.Low < lo {
			lo = b.Low
		}
	}
	return []Level{
		{fmt.Sprintf("ORB高(开盘%d分钟)", orbMin), KindR, hi},
		{fmt.Sprintf("ORB低(开盘%d分钟)", orbMin), KindS, lo},
	}
}

func SessionVWAP(min5 []Bar, day time.Time) *float64 {
	var tpVol, vol float64
	n := 0
	for _, b := range min5 {
		t := b.Time.In(locCST)
		if !sameDate(t, day) || t.Hour() >= 16 || b.Volume <= 0 {
			continue
		}
		tp := (b.High + b.Low + b.Close) / 3
		tpVol += tp * b.Volume
		vol += b.Volume
		n++
	}
	if n < 3 || vol < 5000 {
		return nil
	}
	v := tpVol / vol
	return &v
}

func ScanTimeframe(df []Bar, day time.Time, levels []Level, p Params) []Event {
	p = mergeParams(p)
	if len(df) == 0 {
		return nil
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, locCST)
	from := 0
	for from < len(df) && df[from].Time.Before(start) {
		from++
	}
	if from >= len(df) {
		return nil
	}
	atr := atrSMA(df, p.ATRPeriod)
	volMean := rollingMean(volumes(df), 20, 5)
	donUp, donDn := donchian(df, p.Donchian)

	var events []Event
	findFirst := func(ok []bool, dir, name string, valAt func(i int) float64) {
		for i, hit := range ok {
			if !hit {
				continue
			}
			j := from + i
			events = append(events, Event{
				Time: df[j].Time, Direction: dir, Level: name,
				Close: df[j].Close, Volume: int64(df[j].Volume),
				LevelPrice: valAt(j),
				ATR:        eventATR(atr[j]), // 供推荐止损/止盈用
			})
			return
		}
	}

	n := len(df) - from
	volOK := make([]bool, n)
	buf := make([]float64, n)
	bufOK := make([]bool, n)
	for i := 0; i < n; i++ {
		j := from + i
		b := atr[j] * p.ATRK
		if math.IsNaN(atr[j]) {
			continue
		}
		if b < 1 {
			b = 1
		}
		buf[i] = b
		bufOK[i] = true
		if !math.IsNaN(volMean[j]) && df[j].Volume >= p.VolRatio*volMean[j] {
			volOK[i] = true
		}
	}

	for _, lv := range levels {
		ok := make([]bool, n)
		for i := 0; i < n; i++ {
			if !bufOK[i] || !volOK[i] {
				continue
			}
			c := df[from+i].Close
			if lv.Kind == KindR {
				ok[i] = c > lv.Value+buf[i]
			} else {
				ok[i] = c < lv.Value-buf[i]
			}
		}
		dir := DirUp
		if lv.Kind != KindR {
			dir = DirDown
		}
		val := lv.Value
		findFirst(ok, dir, lv.Name, func(int) float64 { return val })
	}

	upOK := make([]bool, n)
	dnOK := make([]bool, n)
	for i := 0; i < n; i++ {
		j := from + i
		if !bufOK[i] || !volOK[i] {
			continue
		}
		c := df[j].Close
		if !math.IsNaN(donUp[j]) {
			upOK[i] = c > donUp[j]+buf[i]
		}
		if !math.IsNaN(donDn[j]) {
			dnOK[i] = c < donDn[j]-buf[i]
		}
	}
	w := p.Donchian
	findFirst(upOK, DirUp, fmt.Sprintf("Donchian高(%d根)", w), func(j int) float64 { return donUp[j] })
	findFirst(dnOK, DirDown, fmt.Sprintf("Donchian低(%d根)", w), func(j int) float64 { return donDn[j] })

	sort.Slice(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	seen := map[string]bool{}
	uniq := events[:0]
	for _, e := range events {
		k := e.Direction + "\x00" + e.Level
		if seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, e)
	}
	return uniq
}

func ScanBars(min5, min15 []Bar, daily []Daily, p Params) Snapshot {
	return ScanBarsPeriod(min5, min15, nil, daily, p)
}

// ScanBarsPeriod 同 ScanBars，但额外带上「当前级别」（非 5/15 分钟）的 K 线，
// 让前端能画与提醒同级别的价格图。
func ScanBarsPeriod(min5, min15, periodBars []Bar, daily []Daily, p Params) Snapshot {
	p = mergeParams(p)
	day, upcoming, lastDay := determineDays(min5, daily)
	base := PivotLevels(daily, day)
	var orb []Level
	var vwap *float64
	if !upcoming {
		orb = ORBLevels(min5, day, p.ORB)
		vwap = SessionVWAP(min5, day)
	}
	levels := append([]Level{}, base...)
	levels = append(levels, orb...)

	evtDay := day
	if upcoming {
		evtDay = lastDay
	}
	baseEvt := PivotLevels(daily, evtDay)
	orbEvt := ORBLevels(min5, evtDay, p.ORB)
	ev5 := ScanTimeframe(min5, evtDay, append(append([]Level{}, baseEvt...), orbEvt...), p)
	ev15 := ScanTimeframe(min15, evtDay, baseEvt, p)
	trend := emaTrend(min15, 20)

	last5 := lastReal(min5)
	last15 := lastReal(min15)
	var pos string
	if last5 != nil {
		pos = currentPosition(last5.Close, levels, vwap)
	}

	k5 := map[string]bool{}
	for _, e := range ev5 {
		k5[e.Direction+"\x00"+e.Level] = true
	}
	var res []string
	for _, e := range ev15 {
		k := e.Direction + "\x00" + e.Level
		if k5[k] {
			res = append(res, e.Direction+" "+e.Level)
		}
	}
	sort.Strings(res)

	sort.Slice(levels, func(i, j int) bool { return levels[i].Value > levels[j].Value })
	return Snapshot{
		Symbol: p.Symbol, Day: day.In(locCST).Format("2006-01-02"), Upcoming: upcoming,
		Trend: trend, Last5: toView(last5), Last15: toView(last15),
		Levels: levels, VWAP: vwap, Position: pos,
		Events5: toDTO(ev5), Events15: toDTO(ev15), Resonance: res,
		Bars5: klineBars(min5), Bars15: klineBars(min15), BarsPeriod: klineBars(periodBars),
	}
}

func determineDays(min5 []Bar, daily []Daily) (day time.Time, upcoming bool, lastDay time.Time) {
	type stat struct {
		n int
		v float64
	}
	stats := map[string]*stat{}
	var keys []string
	for _, b := range min5 {
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
	sort.Strings(keys)
	var realKeys []string
	for _, k := range keys {
		s := stats[k]
		if s.n >= 3 && s.v >= 5000 {
			realKeys = append(realKeys, k)
		}
	}
	if len(realKeys) > 0 {
		lastDay, _ = time.ParseInLocation("2006-01-02", realKeys[len(realKeys)-1], locCST)
	} else if len(daily) > 0 {
		lastDay = truncateDate(daily[len(daily)-1].Date)
	}
	var allMax time.Time
	if len(keys) > 0 {
		allMax, _ = time.ParseInLocation("2006-01-02", keys[len(keys)-1], locCST)
	} else {
		allMax = lastDay
	}
	upcoming = !allMax.IsZero() && allMax.After(lastDay)
	if upcoming {
		return allMax, true, lastDay
	}
	return lastDay, false, lastDay
}

func emaTrend(df []Bar, span int) string {
	if len(df) == 0 {
		return "震荡"
	}
	closes := make([]float64, len(df))
	for i, b := range df {
		closes[i] = b.Close
	}
	e := ema(closes, span)
	price := df[len(df)-1].Close
	k := 6
	if k > len(e) {
		k = len(e)
	}
	slope := e[len(e)-1] - e[len(e)-k]
	if slope > 0.0015*price {
		return "向上"
	}
	if slope < -0.0015*price {
		return "向下"
	}
	return "震荡"
}

func currentPosition(last float64, levels []Level, vwap *float64) string {
	var res, sup []float64
	for _, lv := range levels {
		if lv.Kind == KindR && lv.Value > last {
			res = append(res, lv.Value)
		}
		if lv.Kind == KindS && lv.Value < last {
			sup = append(sup, lv.Value)
		}
	}
	sort.Float64s(res)
	sort.Float64s(sup)
	var parts []string
	if len(res) > 0 {
		parts = append(parts, fmt.Sprintf("最近压力 %.1f（+%.1f）", res[0], res[0]-last))
	}
	if len(sup) > 0 {
		s := sup[len(sup)-1]
		parts = append(parts, fmt.Sprintf("最近支撑 %.1f（%.1f）", s, s-last))
	}
	if vwap != nil {
		side := "下方"
		if last >= *vwap {
			side = "上方"
		}
		parts = append(parts, fmt.Sprintf("价格在VWAP%s（VWAP %.1f）", side, *vwap))
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "；"
		}
		out += p
	}
	return out
}

func atrSMA(df []Bar, n int) []float64 {
	tr := make([]float64, len(df))
	for i, b := range df {
		hl := b.High - b.Low
		if i == 0 {
			tr[i] = hl
			continue
		}
		pc := df[i-1].Close
		tr[i] = math.Max(hl, math.Max(math.Abs(b.High-pc), math.Abs(b.Low-pc)))
	}
	return rollingMean(tr, n, 5)
}

func donchian(df []Bar, w int) (up, dn []float64) {
	highs := make([]float64, len(df))
	lows := make([]float64, len(df))
	for i, b := range df {
		highs[i] = b.High
		lows[i] = b.Low
	}
	ru := indicator.RollingMax(highs, w)
	rd := rollingMin(lows, w)
	up = shift1(ru)
	dn = shift1(rd)
	return up, dn
}

func rollingMin(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	for i := range out {
		out[i] = math.NaN()
	}
	if n <= 0 {
		return out
	}
	for i := n - 1; i < len(xs); i++ {
		m := xs[i-n+1]
		for j := i - n + 2; j <= i; j++ {
			if xs[j] < m {
				m = xs[j]
			}
		}
		out[i] = m
	}
	return out
}

func rollingMean(xs []float64, window, minP int) []float64 {
	out := make([]float64, len(xs))
	for i := range out {
		out[i] = math.NaN()
	}
	if window <= 0 {
		return out
	}
	var sum float64
	cnt := 0
	for i := 0; i < len(xs); i++ {
		if !math.IsNaN(xs[i]) {
			sum += xs[i]
			cnt++
		}
		if i >= window {
			if !math.IsNaN(xs[i-window]) {
				sum -= xs[i-window]
				cnt--
			}
		}
		if cnt >= minP {
			out[i] = sum / float64(cnt)
		}
	}
	return out
}

func ema(xs []float64, span int) []float64 {
	out := make([]float64, len(xs))
	if len(xs) == 0 || span <= 0 {
		return out
	}
	a := 2 / float64(span+1)
	out[0] = xs[0]
	for i := 1; i < len(xs); i++ {
		out[i] = a*xs[i] + (1-a)*out[i-1]
	}
	return out
}

func shift1(xs []float64) []float64 {
	out := make([]float64, len(xs))
	if len(xs) == 0 {
		return out
	}
	out[0] = math.NaN()
	copy(out[1:], xs[:len(xs)-1])
	return out
}

func volumes(df []Bar) []float64 {
	out := make([]float64, len(df))
	for i, b := range df {
		out[i] = b.Volume
	}
	return out
}

func lastReal(df []Bar) *Bar {
	for i := len(df) - 1; i >= 0; i-- {
		if df[i].Volume > 50 {
			b := df[i]
			return &b
		}
	}
	if len(df) == 0 {
		return nil
	}
	b := df[len(df)-1]
	return &b
}

func toView(b *Bar) *BarView {
	if b == nil {
		return nil
	}
	return &BarView{
		Time: b.Time.In(locCST).Format("01-02 15:04"), Close: b.Close,
		Volume: int64(b.Volume), Hold: b.Hold,
	}
}

func toDTO(ev []Event) []EventDTO {
	out := make([]EventDTO, 0, len(ev))
	for _, e := range ev {
		out = append(out, EventDTO{
			Time:      e.Time.In(locCST).Format("2006-01-02 15:04"),
			Direction: e.Direction, Level: e.Level,
			Close: e.Close, Volume: e.Volume, LevelPrice: e.LevelPrice,
		})
	}
	return out
}

func truncateDate(t time.Time) time.Time {
	t = t.In(locCST)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, locCST)
}

func sameDate(a, b time.Time) bool {
	a, b = a.In(locCST), b.In(locCST)
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
