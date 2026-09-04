package strategy

import (
	"testing"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func turtleBase(sym string, last func(*store.Bar)) []store.Bar {
	return nBars(sym, 21, func(i int, b *store.Bar) {
		b.Open, b.High, b.Low, b.Close = 10, 10, 10, 10
		b.Volume, b.Turnover = 1e6, 2e8
		if i == 20 {
			b.Open, b.High, b.Low, b.Close = 10, 12, 10, 11
			b.Turnover = 2e8
			if last != nil {
				last(b)
			}
		}
	})
}

func TestTurtleHitAndMiss(t *testing.T) {
	hitHi := turtleBase("HIT_HI", func(b *store.Bar) { b.Turnover = 3e8 })
	hitLo := turtleBase("HIT_LO", func(b *store.Bar) { b.Turnover = 2e8 })
	noBrk := turtleBase("NOBRK", func(b *store.Bar) { b.High, b.Close = 10, 10 })
	lowTov := turtleBase("LOWTOV", func(b *store.Bar) { b.Turnover = 1e7 })
	yin := turtleBase("YIN", func(b *store.Bar) { b.Open, b.Close = 11.5, 11 })
	noUp := turtleBase("NOUP", func(b *store.Bar) {
		b.Open, b.High, b.Low, b.Close = 9.5, 10.5, 9.5, 10
	})
	asOf := lastDate(hitHi)
	future := hitHi[len(hitHi)-1]
	future.Date = "2099-01-01"
	future.Close = 99
	hitHi = append(hitHi, future)

	got := Turtle{}.Run(map[string][]store.Bar{
		"HIT_HI": hitHi, "HIT_LO": hitLo, "NOBRK": noBrk,
		"LOWTOV": lowTov, "YIN": yin, "NOUP": noUp,
	}, asOf, nil)
	if len(got) != 2 || got[0].Symbol != "HIT_HI" || got[1].Symbol != "HIT_LO" {
		t.Fatalf("sort/hit got %v", got)
	}
	notPicked(t, got, "NOBRK")
	notPicked(t, got, "LOWTOV")
	notPicked(t, got, "YIN")
	notPicked(t, got, "NOUP")
}
