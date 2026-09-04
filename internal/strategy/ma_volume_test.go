package strategy

import (
	"testing"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func maVolSeries(sym string, lastClose, lastVol float64) []store.Bar {
	return nBars(sym, 21, func(i int, b *store.Bar) {
		c := 20.0
		if i >= 15 && i <= 19 {
			c = 10
		}
		if i == 20 {
			c = lastClose
		}
		b.Open, b.High, b.Low, b.Close = c, c, c, c
		b.Volume = 100
		if i == 20 {
			b.Volume = lastVol
		}
	})
}

func TestMAVolumeHitAndMiss(t *testing.T) {
	hit := maVolSeries("HIT", 60, 200)
	noX := maVolSeries("NOX", 10, 200)
	lowV := maVolSeries("LOWV", 60, 100)
	asOf := lastDate(hit)
	got := MAVolume{}.Run(map[string][]store.Bar{
		"HIT": hit, "NOX": noX, "LOWV": lowV,
	}, asOf, nil)
	picked(t, got, "HIT")
	notPicked(t, got, "NOX")
	notPicked(t, got, "LOWV")
}
