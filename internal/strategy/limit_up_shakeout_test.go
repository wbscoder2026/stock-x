package strategy

import (
	"testing"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func shake3(sym string, tweak func(d0, y, t *store.Bar)) []store.Bar {
	d0 := store.Bar{Symbol: sym, Date: "2024-01-01", Open: 10, High: 10, Low: 10, Close: 10, Volume: 100}
	y := store.Bar{Symbol: sym, Date: "2024-01-02", Open: 10, High: 11, Low: 10, Close: 10.95, Volume: 100}
	today := store.Bar{Symbol: sym, Date: "2024-01-03", Open: 11.2, High: 11.2, Low: 10.95, Close: 11.0, Volume: 250}
	if tweak != nil {
		tweak(&d0, &y, &today)
	}
	return []store.Bar{d0, y, today}
}

func TestLimitUpShakeoutHitAndMiss(t *testing.T) {
	hit := shake3("HIT", nil)
	broke := shake3("BROKE", func(_, y, t *store.Bar) { t.Low = y.Close - 0.01 })
	quiet := shake3("QUIET", func(_, _, t *store.Bar) { t.Volume = 150 })
	noLim := shake3("NOLIM", func(d0, y, _ *store.Bar) { y.Close = d0.Close * 1.05 })
	yang := shake3("YANG", func(_, _, t *store.Bar) { t.Open, t.Close = 10.95, 11.1 })
	got := LimitUpShakeout{}.Run(map[string][]store.Bar{
		"HIT": hit, "BROKE": broke, "QUIET": quiet, "NOLIM": noLim, "YANG": yang,
	}, "2024-01-03", nil)
	picked(t, got, "HIT")
	notPicked(t, got, "BROKE")
	notPicked(t, got, "QUIET")
	notPicked(t, got, "NOLIM")
	notPicked(t, got, "YANG")
}
